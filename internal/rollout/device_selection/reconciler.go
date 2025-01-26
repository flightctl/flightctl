package device_selection

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

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

func (r *reconciler) reconcileFleet(ctx context.Context, orgId uuid.UUID, fleet api.Fleet) {
	r.log.Infof("device selection: starting reconciling fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
	defer r.log.Infof("device selection: finished reconciling fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))

	if fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeviceSelection == nil {
		r.log.Debugf("no device selection definition for fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
		return
	}
	annotations := lo.FromPtr(fleet.Metadata.Annotations)
	if annotations == nil {
		r.log.Infof("no annotations for fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
		return
	}
	templateVersionName, exists := annotations[api.FleetAnnotationTemplateVersion]
	if !exists {
		r.log.Warnf("no template version for fleet %v/%s", orgId, lo.FromPtr(fleet.Metadata.Name))
		return
	}
	selector, err := NewRolloutDeviceSelector(fleet.Spec.RolloutPolicy.DeviceSelection, fleet.Spec.RolloutPolicy.DefaultUpdateTimeout, r.store, orgId, &fleet, templateVersionName, r.log)
	if err != nil {
		r.log.WithError(err).Errorf("%v/%s: NewRolloutDeviceSelector", orgId, lo.FromPtr(fleet.Metadata.Name))
		return
	}

	if selector.IsRolloutNew() {
		// There is a new template version
		if err := selector.OnNewRollout(ctx); err != nil {
			r.log.WithError(err).Errorf("%v/%s: OnNewRollout", orgId, lo.FromPtr(fleet.Metadata.Name))
			return
		}
		if err := selector.Reset(ctx); err != nil {
			r.log.WithError(err).Errorf("%v/%s: Reset", orgId, lo.FromPtr(fleet.Metadata.Name))
			return
		}
	}

	for {
		selection, err := selector.CurrentSelection(ctx)
		if err != nil {
			r.log.WithError(err).Errorf("%v/%s: CurrentSelection", orgId, lo.FromPtr(fleet.Metadata.Name))
			return
		}

		// Check if all devices in the batch have been processed by the fleet-rollout task
		isRolledOut, err := selection.IsRolledOut(ctx)
		if err != nil {
			r.log.WithError(err).Errorf("%v/%s: IsRolledOut", orgId, lo.FromPtr(fleet.Metadata.Name))
			break
		}
		if !isRolledOut {
			if !selection.IsApproved() {

				// A batch may be approved either by a user or automatically
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

			// Send the current batch to be rolled out.
			r.callbackManager.FleetRolloutSelectionUpdated(orgId, lo.FromPtr(fleet.Metadata.Name))
		}

		// Is the current batch complete
		isComplete, err := selection.IsComplete(ctx)
		if err != nil {
			r.log.WithError(err).Errorf("%v/%s: IsComplete", orgId, lo.FromPtr(fleet.Metadata.Name))
			break
		}
		if !isComplete {
			break
		}

		// Once the batch is complete, set the success percentage of the current batch
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

		// Proceed to the next batch
		if err = selector.Advance(ctx); err != nil {
			r.log.WithError(err).Errorf("%v/%s: Advance", orgId, lo.FromPtr(fleet.Metadata.Name))
			break
		}
	}
}

func (r *reconciler) Reconcile(ctx context.Context) {
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
