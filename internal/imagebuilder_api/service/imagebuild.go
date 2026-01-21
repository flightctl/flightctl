package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/kvstore"
	internalservice "github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/common"
	mainstore "github.com/flightctl/flightctl/internal/store"
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
	Get(ctx context.Context, orgId uuid.UUID, name string, withExports bool) (*api.ImageBuild, v1beta1.Status)
	List(ctx context.Context, orgId uuid.UUID, params api.ListImageBuildsParams) (*api.ImageBuildList, v1beta1.Status)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, v1beta1.Status)
	GetLogs(ctx context.Context, orgId uuid.UUID, name string, follow bool) (LogStreamReader, string, v1beta1.Status)
	// Internal methods (not exposed via API)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error)
	UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
	UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error
}

// imageBuildService is the concrete implementation of ImageBuildService
type imageBuildService struct {
	store           store.ImageBuildStore
	repositoryStore mainstore.Repository
	eventHandler    *internalservice.EventHandler
	queueProducer   queues.QueueProducer
	kvStore         kvstore.KVStore
	log             logrus.FieldLogger
}

// NewImageBuildService creates a new ImageBuildService
func NewImageBuildService(s store.ImageBuildStore, repositoryStore mainstore.Repository, eventHandler *internalservice.EventHandler, queueProducer queues.QueueProducer, kvStore kvstore.KVStore, log logrus.FieldLogger) ImageBuildService {
	return &imageBuildService{
		store:           s,
		repositoryStore: repositoryStore,
		eventHandler:    eventHandler,
		queueProducer:   queueProducer,
		kvStore:         kvStore,
		log:             log,
	}
}

func (s *imageBuildService) Create(ctx context.Context, orgId uuid.UUID, imageBuild api.ImageBuild) (*api.ImageBuild, v1beta1.Status) {
	// Don't set fields that are managed by the service
	imageBuild.Status = nil
	NilOutManagedObjectMetaProperties(&imageBuild.Metadata)

	// Validate input
	if errs, internalErr := s.validate(ctx, orgId, &imageBuild); internalErr != nil {
		return nil, StatusInternalServerError(internalErr.Error())
	} else if len(errs) > 0 {
		return nil, StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := s.store.Create(ctx, orgId, &imageBuild)
	if err != nil {
		return result, StoreErrorToApiStatus(err, true, string(api.ResourceKindImageBuild), imageBuild.Metadata.Name)
	}

	// Create event separately (no transaction)
	var event *v1beta1.Event
	if result != nil && s.eventHandler != nil {
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, v1beta1.ResourceKind(string(api.ResourceKindImageBuild)), lo.FromPtr(result.Metadata.Name), nil, s.log, nil)
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

	return result, StoreErrorToApiStatus(nil, true, string(api.ResourceKindImageBuild), imageBuild.Metadata.Name)
}

func (s *imageBuildService) Get(ctx context.Context, orgId uuid.UUID, name string, withExports bool) (*api.ImageBuild, v1beta1.Status) {
	result, err := s.store.Get(ctx, orgId, name, store.GetWithExports(withExports))
	return result, StoreErrorToApiStatus(err, false, string(api.ResourceKindImageBuild), &name)
}

func (s *imageBuildService) List(ctx context.Context, orgId uuid.UUID, params api.ListImageBuildsParams) (*api.ImageBuildList, v1beta1.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if !IsStatusOK(status) {
		return nil, status
	}

	result, err := s.store.List(ctx, orgId, *listParams, store.ListWithExports(lo.FromPtrOr(params.WithExports, false)))
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

func (s *imageBuildService) Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, v1beta1.Status) {
	result, err := s.store.Delete(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, string(api.ResourceKindImageBuild), &name)
}

// Internal methods (not exposed via API)

func (s *imageBuildService) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error) {
	// Update status
	result, err := s.store.UpdateStatus(ctx, orgId, imageBuild)
	if err != nil {
		return result, err
	}

	// Create event for status update
	var event *v1beta1.Event
	if result != nil && result.Metadata.Name != nil && s.eventHandler != nil {
		// Create a simple status update event since status is not in UpdatedFields enum
		event = domain.GetBaseEvent(
			ctx,
			v1beta1.ResourceKind(string(api.ResourceKindImageBuild)),
			*result.Metadata.Name,
			domain.EventReasonResourceUpdated,
			fmt.Sprintf("%s status was updated successfully.", string(api.ResourceKindImageBuild)),
			nil,
		)
		if event != nil {
			s.eventHandler.CreateEvent(ctx, orgId, event)
		}
	}

	// Enqueue event to imagebuild-queue if image is ready (Completed)
	if result != nil && event != nil && s.queueProducer != nil {
		// Check if Ready condition is True with reason Completed
		if result.Status != nil && result.Status.Conditions != nil {
			readyCondition := api.FindImageBuildStatusCondition(*result.Status.Conditions, api.ImageBuildConditionTypeReady)
			if readyCondition != nil &&
				readyCondition.Status == v1beta1.ConditionStatusTrue &&
				readyCondition.Reason == string(api.ImageBuildConditionReasonCompleted) {
				if err := s.enqueueImageBuildEvent(ctx, orgId, event); err != nil {
					s.log.WithError(err).WithField("orgId", orgId).WithField("name", *result.Metadata.Name).Error("failed to enqueue imageBuild event")
					// Don't fail the update if enqueue fails - the event can be retried later
				}
			}
		}
	}

	return result, err
}

func (s *imageBuildService) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return s.store.UpdateLastSeen(ctx, orgId, name, timestamp)
}

func (s *imageBuildService) UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error {
	return s.store.UpdateLogs(ctx, orgId, name, logs)
}

// GetLogs retrieves logs for an ImageBuild
// Returns a LogStreamReader for active builds (if follow=true) or logs string for completed builds
func (s *imageBuildService) GetLogs(ctx context.Context, orgId uuid.UUID, name string, follow bool) (LogStreamReader, string, v1beta1.Status) {
	// First, get the ImageBuild to check its status
	imageBuild, status := s.Get(ctx, orgId, name, false)
	if imageBuild == nil || !IsStatusOK(status) {
		return nil, "", status
	}

	// Check if build is active (Building or Pushing)
	isActive := false
	if imageBuild.Status != nil && imageBuild.Status.Conditions != nil {
		readyCondition := api.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, api.ImageBuildConditionTypeReady)
		if readyCondition != nil {
			reason := readyCondition.Reason
			if reason == string(api.ImageBuildConditionReasonBuilding) || reason == string(api.ImageBuildConditionReasonPushing) {
				isActive = true
			}
		}
	}

	if isActive {
		// Active build - use Redis
		if s.kvStore == nil {
			return nil, "", StatusServiceUnavailable("Redis not available")
		}
		reader := newRedisLogStreamReader(s.kvStore, orgId, name, s.log)
		if follow {
			// Return reader for streaming
			return reader, "", StatusOK()
		}
		// Return all available logs from Redis
		logs, err := reader.ReadAll(ctx)
		if err != nil {
			s.log.WithError(err).Warn("Failed to read logs from Redis")
			// Return empty logs instead of error for active builds
			return nil, "", StatusOK()
		}
		return nil, logs, StatusOK()
	}

	// Completed/terminated build - use DB
	logs, err := s.store.GetLogs(ctx, orgId, name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil, "", StatusNotFound("ImageBuild not found")
		}
		return nil, "", StatusInternalServerError(err.Error())
	}
	// For completed builds, return logs string (follow doesn't matter - no new data)
	return nil, logs, StatusOK()
}

// validate performs validation on an ImageBuild resource
// Returns validation errors (4xx) and internal errors (5xx) separately
func (s *imageBuildService) validate(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) ([]error, error) {
	var errs []error

	if lo.FromPtr(imageBuild.Metadata.Name) == "" {
		errs = append(errs, errors.New("metadata.name is required"))
	}

	if imageBuild.Spec.Source.Repository == "" {
		errs = append(errs, errors.New("spec.source.repository is required"))
	} else {
		// Validate source repository exists and is OCI type
		repo, err := s.repositoryStore.Get(ctx, orgId, imageBuild.Spec.Source.Repository)
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			errs = append(errs, fmt.Errorf("spec.source.repository: Repository %q not found", imageBuild.Spec.Source.Repository))
		} else if err != nil {
			return nil, fmt.Errorf("failed to get source repository %q: %w", imageBuild.Spec.Source.Repository, err)
		} else {
			specType, err := repo.Spec.Discriminator()
			if err != nil {
				return nil, fmt.Errorf("failed to get source repository spec type: %w", err)
			}
			if specType != string(v1beta1.RepoSpecTypeOci) {
				errs = append(errs, fmt.Errorf("spec.source.repository: Repository %q must be of type 'oci', got %q", imageBuild.Spec.Source.Repository, specType))
			}
		}
	}
	errs = append(errs, ValidateImageName(&imageBuild.Spec.Source.ImageName, "spec.source.imageName")...)
	errs = append(errs, ValidateImageTag(&imageBuild.Spec.Source.ImageTag, "spec.source.imageTag")...)

	if imageBuild.Spec.Destination.Repository == "" {
		errs = append(errs, errors.New("spec.destination.repository is required"))
	} else {
		// Validate destination repository exists, is OCI type, and has ReadWrite access
		repo, err := s.repositoryStore.Get(ctx, orgId, imageBuild.Spec.Destination.Repository)
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			errs = append(errs, fmt.Errorf("spec.destination.repository: Repository %q not found", imageBuild.Spec.Destination.Repository))
		} else if err != nil {
			return nil, fmt.Errorf("failed to get destination repository %q: %w", imageBuild.Spec.Destination.Repository, err)
		} else {
			specType, err := repo.Spec.Discriminator()
			if err != nil {
				return nil, fmt.Errorf("failed to get destination repository spec type: %w", err)
			}
			if specType != string(v1beta1.RepoSpecTypeOci) {
				errs = append(errs, fmt.Errorf("spec.destination.repository: Repository %q must be of type 'oci', got %q", imageBuild.Spec.Destination.Repository, specType))
			} else {
				ociSpec, err := repo.Spec.AsOciRepoSpec()
				if err != nil {
					return nil, fmt.Errorf("failed to get destination repository OCI spec: %w", err)
				}
				accessMode := lo.FromPtrOr(ociSpec.AccessMode, v1beta1.Read)
				if accessMode != v1beta1.ReadWrite {
					errs = append(errs, fmt.Errorf("spec.destination.repository: Repository %q must have 'ReadWrite' access mode, got %q", imageBuild.Spec.Destination.Repository, accessMode))
				}
			}
		}
	}
	errs = append(errs, ValidateImageName(&imageBuild.Spec.Destination.ImageName, "spec.destination.imageName")...)
	errs = append(errs, ValidateImageTag(&imageBuild.Spec.Destination.ImageTag, "spec.destination.imageTag")...)

	// Validate userConfiguration if provided
	if imageBuild.Spec.UserConfiguration != nil {
		errs = append(errs, ValidateUsername(&imageBuild.Spec.UserConfiguration.Username, "spec.userConfiguration.username")...)
		errs = append(errs, ValidatePublicKey(&imageBuild.Spec.UserConfiguration.Publickey, "spec.userConfiguration.publickey")...)
	}

	return errs, nil
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
