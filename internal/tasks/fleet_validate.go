package tasks

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
)

func FleetValidate(taskManager TaskManager) {
	for {
		select {
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		case resourceRef := <-taskManager.channels[ChannelFleetValidate]:
			requestID := reqid.NextRequestID()
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(ctx, taskManager.log)
			logic := NewFleetValidateLogic(taskManager, log, taskManager.store, resourceRef)

			switch {
			case resourceRef.Op == FleetValidateOpUpdate && resourceRef.Kind == model.FleetKind:
				err := logic.CreateNewTemplateVersionIfFleetValid(ctx)
				if err != nil {
					log.Errorf("failed validating fleet %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
				}
			default:
				log.Errorf("FleetValidate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
			}
		}
	}
}

type FleetValidateLogic struct {
	taskManager TaskManager
	log         logrus.FieldLogger
	store       store.Store
	resourceRef ResourceReference
}

func NewFleetValidateLogic(taskManager TaskManager, log logrus.FieldLogger, store store.Store, resourceRef ResourceReference) FleetValidateLogic {
	return FleetValidateLogic{taskManager: taskManager, log: log, store: store, resourceRef: resourceRef}
}

func (t *FleetValidateLogic) CreateNewTemplateVersionIfFleetValid(ctx context.Context) error {
	fleet, err := t.store.Fleet().Get(ctx, t.resourceRef.OrgID, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed getting fleet %s/%s: %w", t.resourceRef.OrgID, t.resourceRef.Name, err)
	}

	_, repoNames, validationErr := renderConfig(ctx, t.resourceRef.OrgID, t.store, fleet.Spec.Template.Spec.Config, true)

	// Set the many-to-many relationship with the repos (we do this even if the validation failed so that we will
	// validate the fleet again if the repository is updated, and then it might be fixed).
	err = t.store.Fleet().OverwriteRepositoryRefs(ctx, t.resourceRef.OrgID, *fleet.Metadata.Name, repoNames...)
	if err != nil {
		return fmt.Errorf("setting repository references: %w", err)
	}

	if validationErr != nil {
		return t.setStatus(ctx, validationErr)
	}

	templateVersion := api.TemplateVersion{
		Metadata: api.ObjectMeta{
			Name:  util.TimeStampStringPtr(),
			Owner: util.SetResourceOwner(model.FleetKind, *fleet.Metadata.Name),
		},
		Spec: api.TemplateVersionSpec{Fleet: *fleet.Metadata.Name},
	}

	tv, err := t.store.TemplateVersion().Create(ctx, t.resourceRef.OrgID, &templateVersion, t.taskManager.TemplateVersionCreatedCallback)
	if err != nil {
		return t.setStatus(ctx, fmt.Errorf("creating templateVersion for valid fleet: %w", err))
	}

	annotations := map[string]string{
		model.FleetAnnotationTemplateVersion: *tv.Metadata.Name,
	}
	err = t.store.Fleet().UpdateAnnotations(ctx, t.resourceRef.OrgID, *fleet.Metadata.Name, annotations, nil)
	if err != nil {
		return t.setStatus(ctx, fmt.Errorf("setting fleet annotation with newly-created templateVersion: %w", err))
	}

	return t.setStatus(ctx, nil)
}

func (t *FleetValidateLogic) setStatus(ctx context.Context, validationErr error) error {
	condition := api.Condition{Type: api.FleetValid}

	if validationErr == nil {
		condition.Status = api.ConditionStatusTrue
		condition.Reason = "Valid"
	} else {
		condition.Status = api.ConditionStatusFalse
		condition.Reason = "Invalid"
		condition.Message = validationErr.Error()
	}

	err := t.store.Fleet().UpdateConditions(ctx, t.resourceRef.OrgID, t.resourceRef.Name, []api.Condition{condition})
	if err != nil {
		t.log.Errorf("Failed setting condition for fleet %s/%s: %v", t.resourceRef.OrgID, t.resourceRef.Name, err)
	}
	return validationErr
}
