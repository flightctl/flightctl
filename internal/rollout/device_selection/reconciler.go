package device_selection

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type Reconciler interface {
	Reconcile(ctx context.Context)
}

type reconciler struct {
	serviceHandler  service.Service
	log             logrus.FieldLogger
	callbackManager tasks_client.CallbackManager
}

const RolloutName = "rollout-device-selection"
const RolloutTask = "task:" + RolloutName

func NewReconciler(serviceHandler service.Service, callbackManager tasks_client.CallbackManager, log logrus.FieldLogger) Reconciler {
	return &reconciler{
		serviceHandler:  serviceHandler,
		log:             log,
		callbackManager: callbackManager,
	}
}

func (r *reconciler) sendEvent(ctx context.Context, resourceName string, successMessage string) {
	r.serviceHandler.CreateEvent(ctx,
		service.GetDeviceSelectionReconcilerSuccessTaskEvent(ctx, resourceName, successMessage, r.log),
	)
}

func (r *reconciler) sendLog(orgId uuid.UUID, fleetName string, action string, err error) {
	r.log.WithError(err).Error(fmt.Sprintf("%v/%s: %s", orgId, fleetName, action))
}

func (r *reconciler) reconcileFleet(ctx context.Context, orgId uuid.UUID, fleet api.Fleet) {
	fleetName := lo.FromPtr(fleet.Metadata.Name)
	r.log.Infof("device selection: starting reconciling fleet %v/%s", orgId, fleetName)
	defer r.log.Infof("device selection: finished reconciling fleet %v/%s", orgId, fleetName)

	annotations := lo.FromPtr(fleet.Metadata.Annotations)
	if annotations == nil {
		r.log.Infof("no annotations for fleet %v/%s", orgId, fleetName)
		return
	}
	if fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeviceSelection == nil {
		r.log.Debugf("no device selection definition for fleet %v/%s", orgId, fleetName)
		rolloutWasActive, err := cleanupRollout(ctx, &fleet, r.serviceHandler)
		if err != nil {
			r.sendLog(orgId, fleetName, "CleanupRollout", err)
		}
		if rolloutWasActive {
			// Send the entire fleet for rollout
			r.callbackManager.FleetRolloutSelectionUpdated(ctx, orgId, fleetName)
			r.sendEvent(ctx, fleetName, fmt.Sprintf("%s: Sent To Rollout", fleetName))
		}
		return
	}
	templateVersionName, exists := annotations[api.FleetAnnotationTemplateVersion]
	if !exists {
		r.log.Warnf("no template version for fleet %s", fleetName)
		return
	}
	selector, err := NewRolloutDeviceSelector(fleet.Spec.RolloutPolicy.DeviceSelection, fleet.Spec.RolloutPolicy.DefaultUpdateTimeout, r.serviceHandler, orgId, &fleet, templateVersionName, r.log)
	if err != nil {
		r.sendLog(orgId, fleetName, "NewRolloutDeviceSelector", err)
		return
	}
	definitionUpdated, err := selector.IsDefinitionUpdated()
	if err != nil {
		r.sendLog(orgId, fleetName, "IsDefinitionUpdated", err)
		return
	}
	if selector.IsRolloutNew() || definitionUpdated {
		// There is either a new template version, or the rollout definition was updated
		if err = selector.OnNewRollout(ctx); err != nil {
			r.sendLog(orgId, fleetName, "OnNewRollout", err)
			return
		}
		if err = selector.Reset(ctx); err != nil {
			r.sendLog(orgId, fleetName, "Reset", err)
			return
		}
		r.sendEvent(ctx, fleetName, fmt.Sprintf("Fleet %s reconciled on new rollout", fleetName))
	}

	for {
		selection, err := selector.CurrentSelection(ctx)
		if err != nil {
			r.sendLog(orgId, fleetName, "CurrentSelection", err)
			break
		}

		if !selection.IsApproved() {

			// A batch may be approved either by a user or automatically
			mayApprove, err := selection.MayApproveAutomatically()
			if err != nil {
				r.sendLog(orgId, fleetName, "MayApproveAutomatically", err)
				break
			}
			if mayApprove {
				if err = selection.Approve(ctx); err != nil {
					r.sendLog(orgId, fleetName, "Approval", err)
					break
				}
			} else {
				if err = selection.OnSuspended(ctx); err != nil {
					r.sendLog(orgId, fleetName, "OnSuspended", err)
				}
				break
			}
		}

		// Check if all devices in the batch have been processed by the fleet-rollout task
		isRolledOut, err := selection.IsRolledOut(ctx)
		if err != nil {
			r.sendLog(orgId, fleetName, "IsRolledOut", err)
			break
		}
		if !isRolledOut {
			if err = selection.OnRollout(ctx); err != nil {
				r.sendLog(orgId, fleetName, "OnRollout", err)
			}
			// Send the current batch to be rolled out.
			r.callbackManager.FleetRolloutSelectionUpdated(ctx, orgId, fleetName)
		}

		// Is the current batch complete
		isComplete, err := selection.IsComplete(ctx)
		if err != nil {
			r.sendLog(orgId, fleetName, "IsComplete", err)
			break
		}
		if !isComplete {
			break
		}

		// Once the batch is complete, set the success percentage of the current batch
		if err = selection.SetCompletionReport(ctx); err != nil {
			r.sendLog(orgId, fleetName, "SetCompletionReport", err)
			break
		}
		hasMoreSelections, err := selector.HasMoreSelections(ctx)
		if err != nil {
			r.sendLog(orgId, fleetName, "HasMoreSelections", err)
			break
		}
		if !hasMoreSelections {
			if err = selection.OnFinish(ctx); err != nil {
				r.sendLog(orgId, fleetName, "OnFinish", err)
			}
			break
		}

		// Proceed to the next batch
		if err = selector.Advance(ctx); err != nil {
			r.sendLog(orgId, fleetName, "Advance", err)
			break
		}
	}
}

func (r *reconciler) Reconcile(ctx context.Context) {
	reqid.OverridePrefix(RolloutTask)
	requestID := reqid.NextRequestID()
	ctx = context.WithValue(ctx, middleware.RequestIDKey, requestID)
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, RolloutTask)

	// Get all relevant fleets
	orgId := store.NullOrgId
	fleetList, status := r.serviceHandler.ListFleetRolloutDeviceSelection(ctx)
	if status.Code != http.StatusOK {
		r.log.WithError(service.ApiStatusToErr(status)).Error("ListRolloutDeviceSelection")
		return
	}
	for _, fleet := range fleetList.Items {
		r.reconcileFleet(ctx, orgId, fleet)
	}
}
