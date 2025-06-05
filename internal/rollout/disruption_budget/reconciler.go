package disruption_budget

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	DisruptionBudgetReconcilationInterval = 30 * time.Second
	maxItemsToRender                      = 1000
)

type Reconciler interface {
	Reconcile(ctx context.Context)
}

type reconciler struct {
	serviceHandler  service.Service
	log             logrus.FieldLogger
	callbackManager tasks_client.CallbackManager
}

type groupCounts struct {
	totalCount         int64
	connectedCount     int64
	busyConnectedCount int64
	key                map[string]any
}

func NewReconciler(serviceHandler service.Service, callbackManager tasks_client.CallbackManager, log logrus.FieldLogger) Reconciler {
	return &reconciler{
		serviceHandler:  serviceHandler,
		log:             log,
		callbackManager: callbackManager,
	}
}

func intValue(m map[string]any, key string) (int64, error) {
	v, exists := util.GetFromMap(m, key)
	if !exists {
		return 0, fmt.Errorf("key %s doesn't exists in map", key)
	}
	intVal, ok := v.(int64)
	if !ok {
		return 0, fmt.Errorf("value %v has type %T, not int64", v, v)
	}
	return intVal, nil
}

func collectDeviceBudgetCounts(counts []map[string]any, groupBy []string) ([]*groupCounts, error) {
	var ret []*groupCounts
	for _, m := range counts {
		totalCount, err := intValue(m, "total")
		if err != nil {
			return nil, err
		}
		connectedCount, err := intValue(m, "connected")
		if err != nil {
			return nil, err
		}
		busyConnectedCount, err := intValue(m, "busy_connected")
		if err != nil {
			return nil, err
		}
		ret = append(ret, &groupCounts{
			totalCount:         totalCount,
			connectedCount:     connectedCount,
			busyConnectedCount: busyConnectedCount,
			key:                lo.SliceToMap(groupBy, func(k string) (string, any) { return k, m[k] }),
		})
	}
	return ret, nil
}

func (r *reconciler) getFleetCounts(ctx context.Context, _ uuid.UUID, fleet *api.Fleet) ([]*groupCounts, error) {
	groupBy := lo.FromPtr(fleet.Spec.RolloutPolicy.DisruptionBudget.GroupBy)

	listParams := api.ListDevicesParams{
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", util.ResourceOwner(api.FleetKind, lo.FromPtr(fleet.Metadata.Name)))),
	}
	counts, status := r.serviceHandler.CountDevicesByLabels(ctx, listParams, nil, groupBy)
	if status.Code != http.StatusOK {
		return nil, service.ApiStatusToErr(status)
	}
	return collectDeviceBudgetCounts(counts, groupBy)
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

	// The query should get only devices that are ready for rendering
	// but have not been rendered yet.  It means that the annotation 'device-controller/templateVersion'
	// is equal to the expected template version but the annotation 'device-controller/renderedTemplateVersion'
	// should not equal to the template version.
	annotationSelector := selector.NewAnnotationSelectorOrDie(strings.Join([]string{
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
	}, ","))
	listParams := api.ListDevicesParams{
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", util.ResourceOwner(api.FleetKind, lo.FromPtr(fleet.Metadata.Name)))),
	}
	if len(key) > 0 {
		// The list of labels is converted to MatchExpressions.  In case that the label does not exist
		// (nil value), then the query requests explicitly that the label should not exist.
		var labelSelectorParts []string
		for k, v := range key {
			switch val := v.(type) {
			case nil:
				labelSelectorParts = append(labelSelectorParts, api.MatchExpression{
					Key:      k,
					Operator: api.DoesNotExist,
				}.String())
			case string:
				labelSelectorParts = append(labelSelectorParts, api.MatchExpression{
					Key:      k,
					Operator: api.In,
					Values:   lo.ToPtr([]string{val}),
				}.String())
			default:
				return fmt.Errorf("unexpected type %T for label %s", v, k)
			}
		}
		listParams.LabelSelector = lo.ToPtr(strings.Join(labelSelectorParts, ","))
	}
	remaining := lo.Ternary(numToRender > 0, numToRender, math.MaxInt)
	for {
		listParams.Limit = lo.ToPtr(int32(math.Min(float64(remaining), float64(maxItemsToRender))))
		devices, status := r.serviceHandler.ListDevices(ctx, listParams, annotationSelector)
		if status.Code != http.StatusOK {
			return service.ApiStatusToErr(status)
		}
		for _, d := range devices.Items {
			r.log.Infof("%v/%s: sending device to rendering", orgId, lo.FromPtr(d.Metadata.Name))
			r.callbackManager.DeviceSourceUpdated(ctx, orgId, lo.FromPtr(d.Metadata.Name))
		}
		remaining = remaining - len(devices.Items)
		if devices.Metadata.Continue == nil || remaining == 0 {
			break
		}
		listParams.Continue = devices.Metadata.Continue
	}
	return nil
}

func (r *reconciler) reconcileFleet(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) error {
	r.log.Infof("disruption budget: starting reconciling fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
	defer r.log.Infof("disruption budget: finished reconciling fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))

	if fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DisruptionBudget == nil {
		if err := r.reconcileSelectionDevices(ctx, orgId, fleet, nil, 0); err != nil {
			return fmt.Errorf("reconcileSelectionDevices: %w", err)
		}
		return nil
	}
	maxUnavailable := fleet.Spec.RolloutPolicy.DisruptionBudget.MaxUnavailable
	minAvailable := fleet.Spec.RolloutPolicy.DisruptionBudget.MinAvailable
	if maxUnavailable == nil && minAvailable == nil {
		return fmt.Errorf("both maxUnavailable and minAvailable for fleet %s are nil", lo.FromPtr(fleet.Metadata.Name))
	}
	counts, err := r.getFleetCounts(ctx, orgId, fleet)
	if err != nil {
		return fmt.Errorf("getFleetCounts: %w", err)
	}
	for _, count := range counts {
		unavailable := int(count.busyConnectedCount)
		available := int(count.connectedCount - count.busyConnectedCount)
		numToRender := math.MaxInt
		if maxUnavailable != nil {
			numToRender = util.Min(numToRender, lo.FromPtr(maxUnavailable)-unavailable)
		}
		if minAvailable != nil {
			numToRender = util.Min(numToRender, available-util.Min(lo.FromPtr(minAvailable), int(count.totalCount-1)))
		}
		if numToRender > 0 {
			if err = r.reconcileSelectionDevices(ctx, orgId, fleet, count.key, numToRender); err != nil {
				return fmt.Errorf("reconcileSelectionDevices: %w", err)
			}
		}
	}
	return nil
}

func (r *reconciler) Reconcile(ctx context.Context) {
	// Get all relevant fleets
	orgId := store.NullOrgId

	fleetList, status := r.serviceHandler.ListDisruptionBudgetFleets(ctx)
	if status.Code != http.StatusOK {
		r.log.WithError(service.ApiStatusToErr(status)).Error("Failed to query disruption budget fleets")
		return
	}
	for i := range fleetList.Items {
		fleet := &fleetList.Items[i]
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
