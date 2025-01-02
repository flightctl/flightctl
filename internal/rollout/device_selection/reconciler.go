package device_selection

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const RolloutDeviceSelectionInterval = 30 * time.Second

type Reconciler interface {
	Reconcile()
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

func (r *reconciler) reconcileFleet(ctx context.Context, orgId uuid.UUID, fleet v1alpha1.Fleet) {
	r.log.Infof("device selection: starting reconciling fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
	defer r.log.Infof("device selection: finished reconciling fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))

	if fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeviceSelection == nil {
		return
	}
	annotations := lo.FromPtr(fleet.Metadata.Annotations)
	if annotations == nil {
		r.log.Infof("no annotations for fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
		return
	}
	templateVersionName, exists := annotations[model.FleetAnnotationTemplateVersion]
	if !exists {
		r.log.Infof("no template version for fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
		return
	}
	selector, err := NewRolloutDeviceSelector(fleet.Spec.RolloutPolicy.DeviceSelection, r.store, orgId, &fleet, templateVersionName, r.log)
	if err != nil {
		r.log.WithError(err).Errorf("%v/%s: NewRolloutDeviceSelector", orgId, lo.FromPtr(fleet.Metadata.Name))
		return
	}

	if selector.IsRolloutNew() {
		if err := selector.OnNewRollout(ctx); err != nil {
			r.log.WithError(err).Errorf("%v/%s: OnNewRollout", orgId, lo.FromPtr(fleet.Metadata.Name))
			return
		}
		if err := selector.Reset(ctx); err != nil {
			r.log.WithError(err).Errorf("%v/%s: Reset", orgId, lo.FromPtr(fleet.Metadata.Name))
			return
		}
	}

	selection, err := selector.CurrentSelection(ctx)
	if err != nil {
		r.log.WithError(err).Errorf("%v/%s: CurrentSelection", orgId, lo.FromPtr(fleet.Metadata.Name))
		return
	}
	for {
		isRolledOut, err := selection.IsRolledOut(ctx)
		if err != nil {
			r.log.WithError(err).Errorf("%v/%s: IsRolledOut", orgId, lo.FromPtr(fleet.Metadata.Name))
			break
		}
		if !isRolledOut {
			if !selection.IsApproved() {
				mayApprove, err := selection.MayApproveAutomatically()
				if err != nil {
					r.log.WithError(err).Errorf("%v/%s: MayApproveAutomatically", orgId, lo.FromPtr(fleet.Metadata.Name))
					break
				}
				if mayApprove {
					if err = selection.Approve(ctx); err != nil {
						r.log.WithError(err).Errorf("%v/%s: Approve", orgId, lo.FromPtr(fleet.Metadata.Name))
						break
					}
				} else {
					break
				}
			}
			modelFleet, err := model.NewFleetFromApiResource(&fleet)
			if err != nil {
				r.log.WithError(err).Errorf("%v/%s: NewFleetFromApiResource", orgId, lo.FromPtr(fleet.Metadata.Name))
				break
			}
			r.callbackManager.FleetRolloutSelectionUpdated(modelFleet)
		}
		isComplete, err := selection.IsComplete(ctx)
		if err != nil {
			r.log.WithError(err).Errorf("%v/%s: IsComplete", orgId, lo.FromPtr(fleet.Metadata.Name))
			break
		}
		if !isComplete {
			break
		}
		if err = selection.SetSuccessPercentage(ctx); err != nil {
			r.log.WithError(err).Errorf("%v/%s: SetSuccessPercentage", orgId, lo.FromPtr(fleet.Metadata.Name))
			break
		}
		hasMoreSelections, err := selector.HasMoreSelections(ctx)
		if err != nil {
			r.log.WithError(err).Errorf("%v/%s: HasMoreSelections", orgId, lo.FromPtr(fleet.Metadata.Name))
			break
		}
		if !hasMoreSelections {
			break
		}
		if err = selector.Advance(ctx); err != nil {
			r.log.WithError(err).Errorf("%v/%s: Advance", orgId, lo.FromPtr(fleet.Metadata.Name))
			break
		}
		selection, err = selector.CurrentSelection(ctx)
		if err != nil {
			r.log.WithError(err).Errorf("%v/%s: CurrentSelection", orgId, lo.FromPtr(fleet.Metadata.Name))
			break
		}
	}
}

func (r *reconciler) Reconcile() {
	ctx := context.Background()

	// Get all relevant fleets
	orgId := store.NullOrgId

	fleetList, err := r.store.Fleet().ListRolloutDeviceSelection(ctx, orgId)
	if err != nil {
		r.log.WithError(err).Error("ListRolloutDeviceSelection")
		return
	}
	for _, fleet := range fleetList.Items {
		r.reconcileFleet(ctx, orgId, fleet)
	}
}
