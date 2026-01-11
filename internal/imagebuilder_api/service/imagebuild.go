package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	internalservice "github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// ImageBuildService handles business logic for ImageBuild resources
type ImageBuildService interface {
	Create(ctx context.Context, orgId uuid.UUID, imageBuild api.ImageBuild) (*api.ImageBuild, v1beta1.Status)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, v1beta1.Status)
	List(ctx context.Context, orgId uuid.UUID, params api.ListImageBuildsParams) (*api.ImageBuildList, v1beta1.Status)
	Delete(ctx context.Context, orgId uuid.UUID, name string) v1beta1.Status
	// Internal methods (not exposed via API)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error)
	UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
}

// imageBuildService is the concrete implementation of ImageBuildService
type imageBuildService struct {
	store         store.ImageBuildStore
	eventHandler  *internalservice.EventHandler
	queueProducer queues.QueueProducer
	log           logrus.FieldLogger
}

// NewImageBuildService creates a new ImageBuildService
func NewImageBuildService(s store.ImageBuildStore, eventHandler *internalservice.EventHandler, queueProducer queues.QueueProducer, log logrus.FieldLogger) ImageBuildService {
	return &imageBuildService{
		store:         s,
		eventHandler:  eventHandler,
		queueProducer: queueProducer,
		log:           log,
	}
}

func (s *imageBuildService) Create(ctx context.Context, orgId uuid.UUID, imageBuild api.ImageBuild) (*api.ImageBuild, v1beta1.Status) {
	// Don't set fields that are managed by the service
	imageBuild.Status = nil
	NilOutManagedObjectMetaProperties(&imageBuild.Metadata)

	// Validate input
	if errs := s.validate(&imageBuild); len(errs) > 0 {
		return nil, StatusBadRequest(errors.Join(errs...).Error())
	}

	var result *api.ImageBuild
	var event *v1beta1.Event

	// Execute in a transaction - create ImageBuild and Event atomically
	err := s.store.Transaction(ctx, func(txCtx context.Context) error {
		var createErr error
		result, createErr = s.store.Create(txCtx, orgId, &imageBuild)
		if createErr != nil {
			return createErr
		}

		// Create event for ImageBuild creation
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(txCtx, true, v1beta1.ResourceKind(api.ImageBuildKind), lo.FromPtr(result.Metadata.Name), nil, s.log, nil)
		if event != nil {
			// Persist event in database using EventHandler (for audit/logging)
			// Note: workerClient is nil, so it won't push to TaskQueue
			// The transaction context is passed so the event is created in the same transaction
			if s.eventHandler != nil {
				s.eventHandler.CreateEvent(txCtx, orgId, event)
			}
		}

		return nil
	})

	if err != nil {
		// Check if the error is a statusError (from transaction rollback)
		// This allows us to return the proper status instead of converting to a generic error
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			return result, statusErr.status
		}
		// Otherwise convert the store error to an API status
		return result, StoreErrorToApiStatus(err, true, ImageBuildKind, imageBuild.Metadata.Name)
	}

	// Enqueue event to imagebuild-queue for worker processing (outside transaction)
	// This is done after the transaction commits to ensure the event is persisted first
	// Reuse the same event that was created and persisted in the transaction
	if result != nil && event != nil && s.queueProducer != nil {
		if err := s.enqueueImageBuildEvent(ctx, orgId, event); err != nil {
			s.log.WithError(err).WithField("orgId", orgId).WithField("name", lo.FromPtr(result.Metadata.Name)).Error("failed to enqueue imageBuild event")
			// Don't fail the creation if enqueue fails - the event can be retried later
		}
	}

	return result, StoreErrorToApiStatus(nil, true, ImageBuildKind, imageBuild.Metadata.Name)
}

func (s *imageBuildService) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, v1beta1.Status) {
	result, err := s.store.Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, ImageBuildKind, &name)
}

func (s *imageBuildService) List(ctx context.Context, orgId uuid.UUID, params api.ListImageBuildsParams) (*api.ImageBuildList, v1beta1.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if !IsStatusOK(status) {
		return nil, status
	}

	result, err := s.store.List(ctx, orgId, *listParams)
	if err == nil {
		return result, StatusOK()
	}

	var se *selector.SelectorError
	switch {
	case selector.AsSelectorError(err, &se):
		return nil, StatusBadRequest(se.Error())
	default:
		return nil, StatusInternalServerError(err.Error())
	}
}

func (s *imageBuildService) Delete(ctx context.Context, orgId uuid.UUID, name string) v1beta1.Status {
	err := s.store.Delete(ctx, orgId, name)
	return StoreErrorToApiStatus(err, false, ImageBuildKind, &name)
}

// Internal methods (not exposed via API)

func (s *imageBuildService) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error) {
	return s.store.UpdateStatus(ctx, orgId, imageBuild)
}

func (s *imageBuildService) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return s.store.UpdateLastSeen(ctx, orgId, name, timestamp)
}

// validate performs validation on an ImageBuild resource
func (s *imageBuildService) validate(imageBuild *api.ImageBuild) []error {
	var errs []error

	if lo.FromPtr(imageBuild.Metadata.Name) == "" {
		errs = append(errs, errors.New("metadata.name is required"))
	}

	if imageBuild.Spec.Source.Repository == "" {
		errs = append(errs, errors.New("spec.source.repository is required"))
	}
	if imageBuild.Spec.Source.ImageName == "" {
		errs = append(errs, errors.New("spec.source.imageName is required"))
	}
	if imageBuild.Spec.Source.ImageTag == "" {
		errs = append(errs, errors.New("spec.source.imageTag is required"))
	}

	if imageBuild.Spec.Destination.Repository == "" {
		errs = append(errs, errors.New("spec.destination.repository is required"))
	}
	if imageBuild.Spec.Destination.ImageName == "" {
		errs = append(errs, errors.New("spec.destination.imageName is required"))
	}
	if imageBuild.Spec.Destination.Tag == "" {
		errs = append(errs, errors.New("spec.destination.tag is required"))
	}

	// Binding validation is now enforced by the schema:
	// - EarlyBinding requires cert (enforced by schema)
	// - LateBinding has no additional required fields

	return errs
}

// enqueueImageBuildEvent enqueues an event to the imagebuild-queue
func (s *imageBuildService) enqueueImageBuildEvent(ctx context.Context, orgId uuid.UUID, event *v1beta1.Event) error {
	if event == nil {
		return errors.New("event is nil")
	}

	// Create EventWithOrgId structure for the queue
	eventWithOrgId := worker_client.EventWithOrgId{
		OrgId: orgId,
		Event: *event,
	}

	payload, err := json.Marshal(eventWithOrgId)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// Use creation timestamp if available, otherwise use current time
	var timestamp int64
	if event.Metadata.CreationTimestamp != nil {
		timestamp = event.Metadata.CreationTimestamp.UnixMicro()
	} else {
		timestamp = time.Now().UnixMicro()
	}

	if err := s.queueProducer.Enqueue(ctx, payload, timestamp); err != nil {
		return fmt.Errorf("failed to enqueue event: %w", err)
	}

	s.log.WithField("orgId", orgId).WithField("name", event.InvolvedObject.Name).Info("enqueued imageBuild event")
	return nil
}
