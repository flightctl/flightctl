package disruption_budget

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const DisruptionBudgetReconcilationInterval = 30 * time.Second

type Reconciler interface {
	Reconcile(ctx context.Context)
}

type reconciler struct {
	store           store.Store
	log             logrus.FieldLogger
	callbackManager tasks.CallbackManager
}

func NewReconciler(store store.Store, callbackManager tasks.CallbackManager, log logrus.FieldLogger) Reconciler {
	return &reconciler{
		store:           store,
		log:             log,
		callbackManager: callbackManager,
	}
}

func (r *reconciler) getFleetCounts(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) (map[string]*Counts, error) {
	groupBy := lo.FromPtr(fleet.Spec.RolloutPolicy.DisruptionBudget.GroupBy)

	totalCounts, err := r.store.Device().CountByLabels(ctx, orgId, store.ListParams{
		FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"metadata.owner": util.ResourceOwner(api.FleetKind, lo.FromPtr(fleet.Metadata.Name))}),
	}, groupBy, false)
	if err != nil {
		return nil, err
	}
	annotations := lo.FromPtr(fleet.Metadata.Annotations)
	if annotations == nil {
		return nil, fmt.Errorf("annotations don't exist")
	}

	busyCounts, err := r.store.Device().CountByLabels(ctx, orgId, store.ListParams{
		FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"metadata.owner": util.ResourceOwner(api.FleetKind, lo.FromPtr(fleet.Metadata.Name))}),
	}, groupBy, true)
	if err != nil {
		return nil, err
	}
	return mergeDeviceAllowanceCounts(totalCounts, busyCounts, groupBy)
}

func (r *reconciler) reconcileSelectionDevices(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, key map[string]any, numToRender int) error {
	annotations := lo.FromPtr(fleet.Metadata.Annotations)
	if annotations == nil {
		return fmt.Errorf("annotations don't exist")
	}
	templateVersionName, exists := annotations[api.FleetAnnotationTemplateVersion]
	if !exists {
		return fmt.Errorf("template version doesn't exist")
	}
	listParams := store.ListParams{
		Limit: numToRender,

		// The list of labels is converted to MatchExpressions.  In case that the label does not exist
		// (nil value), then the query requests explicitly that the label should not exist.
		LabelSelector: selector.NewLabelSelectorOrDie(strings.Join(lo.MapToSlice(key, func(k string, v any) string {
			if v == nil {
				return api.MatchExpression{
					Key:      k,
					Operator: api.DoesNotExist,
				}.String()
			}
			return api.MatchExpression{
				Key:      k,
				Operator: api.In,
				Values:   lo.ToPtr([]string{v.(string)}),
			}.String()
		}), ",")),

		// The query should get only devices that are ready for rendering
		// but have not been rendered yet.  It means that the annotation 'device-controller/templateVersion'
		// is equal to the expected template version but the annotation 'device-controller/renderedTemplateVersion'
		// should not equal to the template version.
		AnnotationSelector: selector.NewAnnotationSelectorOrDie(strings.Join([]string{
			api.MatchExpression{
				Key:      api.DeviceAnnotationTemplateVersion,
				Operator: api.In,
				Values:   lo.ToPtr([]string{templateVersionName}),
			}.String(),
			api.MatchExpression{
				Key:      api.DeviceAnnotationRenderedTemplateVersion,
				Operator: api.NotIn,
				Values:   lo.ToPtr([]string{templateVersionName}),
			}.String(),
		}, ",")),
		FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"metadata.owner": util.ResourceOwner(api.FleetKind, lo.FromPtr(fleet.Metadata.Name))}),
	}
	devices, err := r.store.Device().List(ctx, orgId, listParams)
	if err != nil {
		return err
	}
	for _, d := range devices.Items {
		r.log.Infof("%v/%s: sending device to rendering", orgId, lo.FromPtr(d.Metadata.Name))
		r.callbackManager.DeviceSourceUpdated(orgId, lo.FromPtr(d.Metadata.Name))
	}
	return nil
}

func (r *reconciler) reconcileFleet(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) error {
	r.log.Infof("disruption budget: starting reconciling fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
	defer r.log.Infof("disruption budget: finished reconciling fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))

	maxUnavailable := fleet.Spec.RolloutPolicy.DisruptionBudget.MaxUnavailable
	minAvailable := fleet.Spec.RolloutPolicy.DisruptionBudget.MinAvailable
	if maxUnavailable == nil && minAvailable == nil {
		return fmt.Errorf("both maxUnavailable and minAvailable for fleet %s are nil", lo.FromPtr(fleet.Metadata.Name))
	}
	m, err := r.getFleetCounts(ctx, orgId, fleet)
	if err != nil {
		return fmt.Errorf("getFleetCounts: %w", err)
	}
	for _, count := range m {
		unavailable := count.BusyCount
		available := count.TotalCount - count.BusyCount
		numToRender := math.MaxInt
		if maxUnavailable != nil {
			numToRender = util.Min(numToRender, lo.FromPtr(maxUnavailable)-unavailable)
		}
		if minAvailable != nil {
			numToRender = util.Min(numToRender, available-lo.FromPtr(minAvailable))
		}
		if numToRender > 0 {
			if err = r.reconcileSelectionDevices(ctx, orgId, fleet, count.key, numToRender); err != nil {
				return fmt.Errorf("reconcileSelectionDevices: %v", err)
			}
		}
	}
	return nil
}

func (r *reconciler) Reconcile(ctx context.Context) {
	// Get all relevant fleets
	orgId := store.NullOrgId

	fleetList, err := r.store.Fleet().ListDisruptionBudgetFleets(ctx, orgId)
	if err != nil {
		r.log.WithError(err).Error("Failed to query disruption budget fleets")
		return
	}
	for i := range fleetList.Items {
		fleet := &fleetList.Items[i]
		if fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DisruptionBudget == nil {
			continue
		}
		annotations := lo.FromPtr(fleet.Metadata.Annotations)
		if annotations == nil {
			continue
		}
		_, exists := annotations[api.FleetAnnotationTemplateVersion]
		if !exists {
			continue
		}
		if err := r.reconcileFleet(ctx, orgId, fleet); err != nil {
			r.log.WithError(err).Errorf("reconcile fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
		}
	}
}
