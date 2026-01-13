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

	result, err := s.store.Create(ctx, orgId, &imageBuild)
	if err != nil {
		return result, StoreErrorToApiStatus(err, true, ImageBuildKind, imageBuild.Metadata.Name)
	}

	// Create event separately (no transaction)
	var event *v1beta1.Event
	if result != nil && s.eventHandler != nil {
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, v1beta1.ResourceKind(api.ImageBuildKind), lo.FromPtr(result.Metadata.Name), nil, s.log, nil)
		if event != nil {
			s.eventHandler.CreateEvent(ctx, orgId, event)
		}
	}

	// Enqueue event to imagebuild-queue for worker processing
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
	errs = append(errs, ValidateImageName(&imageBuild.Spec.Source.ImageName, "spec.source.imageName")...)
	errs = append(errs, ValidateImageTag(&imageBuild.Spec.Source.ImageTag, "spec.source.imageTag")...)

	if imageBuild.Spec.Destination.Repository == "" {
		errs = append(errs, errors.New("spec.destination.repository is required"))
	}
	errs = append(errs, ValidateImageName(&imageBuild.Spec.Destination.ImageName, "spec.destination.imageName")...)
	errs = append(errs, ValidateImageTag(&imageBuild.Spec.Destination.Tag, "spec.destination.tag")...)

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
