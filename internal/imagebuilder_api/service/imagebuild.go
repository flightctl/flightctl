package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
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
	Create(ctx context.Context, orgId uuid.UUID, imageBuild domain.ImageBuild) (*domain.ImageBuild, domain.Status)
	Get(ctx context.Context, orgId uuid.UUID, name string, withExports bool) (*domain.ImageBuild, domain.Status)
	List(ctx context.Context, orgId uuid.UUID, params domain.ListImageBuildsParams) (*domain.ImageBuildList, domain.Status)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImageBuild, domain.Status)
	// Cancel cancels an ImageBuild. Returns ErrNotCancelable if not in cancelable state.
	Cancel(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImageBuild, error)
	// CancelWithReason cancels an ImageBuild with a custom reason message (e.g., for timeout).
	// Returns ErrNotCancelable if not in cancelable state.
	CancelWithReason(ctx context.Context, orgId uuid.UUID, name string, reason string) (*domain.ImageBuild, error)
	GetLogs(ctx context.Context, orgId uuid.UUID, name string, follow bool) (LogStreamReader, string, domain.Status)
	// Internal methods (not exposed via API)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *domain.ImageBuild) (*domain.ImageBuild, error)
	UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
	UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error
}

// imageBuildService is the concrete implementation of ImageBuildService
type imageBuildService struct {
	store              store.ImageBuildStore
	repositoryStore    mainstore.Repository
	imageExportService ImageExportService
	eventHandler       *internalservice.EventHandler
	queueProducer      queues.QueueProducer
	kvStore            kvstore.KVStore
	cfg                *config.ImageBuilderServiceConfig
	log                logrus.FieldLogger
}

// NewImageBuildService creates a new ImageBuildService
func NewImageBuildService(s store.ImageBuildStore, repositoryStore mainstore.Repository, imageExportService ImageExportService, eventHandler *internalservice.EventHandler, queueProducer queues.QueueProducer, kvStore kvstore.KVStore, cfg *config.ImageBuilderServiceConfig, log logrus.FieldLogger) ImageBuildService {
	return &imageBuildService{
		store:              s,
		repositoryStore:    repositoryStore,
		imageExportService: imageExportService,
		eventHandler:       eventHandler,
		queueProducer:      queueProducer,
		kvStore:            kvStore,
		cfg:                cfg,
		log:                log,
	}
}

func (s *imageBuildService) Create(ctx context.Context, orgId uuid.UUID, imageBuild domain.ImageBuild) (*domain.ImageBuild, domain.Status) {
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
		return result, StoreErrorToApiStatus(err, true, string(domain.ResourceKindImageBuild), imageBuild.Metadata.Name)
	}
	// Clear any stale Redis keys from a previous resource with the same name
	// This prevents old cancellation signals from affecting the new resource
	if s.kvStore != nil && imageBuild.Metadata.Name != nil {
		name := *imageBuild.Metadata.Name
		if err := s.kvStore.Delete(ctx, getImageBuildCancelStreamKey(orgId, name)); err != nil {
			s.log.WithError(err).Debug("Failed to clear stale cancel stream key (may not exist)")
		}
		if err := s.kvStore.Delete(ctx, getImageBuildCanceledStreamKey(orgId, name)); err != nil {
			s.log.WithError(err).Debug("Failed to clear stale canceled stream key (may not exist)")
		}
	}
	// Create event separately (no transaction)
	var event *coredomain.Event
	if result != nil && s.eventHandler != nil {
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, coredomain.ResourceKind(string(domain.ResourceKindImageBuild)), lo.FromPtr(result.Metadata.Name), nil, s.log, nil)
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

	return result, StoreErrorToApiStatus(nil, true, string(domain.ResourceKindImageBuild), imageBuild.Metadata.Name)
}

func (s *imageBuildService) Get(ctx context.Context, orgId uuid.UUID, name string, withExports bool) (*domain.ImageBuild, domain.Status) {
	result, err := s.store.Get(ctx, orgId, name, store.GetWithExports(withExports))
	return result, StoreErrorToApiStatus(err, false, string(domain.ResourceKindImageBuild), &name)
}

func (s *imageBuildService) List(ctx context.Context, orgId uuid.UUID, params domain.ListImageBuildsParams) (*domain.ImageBuildList, domain.Status) {
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

func (s *imageBuildService) Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImageBuild, domain.Status) {
	// First, get the ImageBuild to check its status
	imageBuild, err := s.store.Get(ctx, orgId, name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			// Idempotent delete - resource doesn't exist
			return nil, StatusOK()
		}
		return nil, StoreErrorToApiStatus(err, false, string(domain.ResourceKindImageBuild), &name)
	}

	// Delete all related ImageExports first (using service which does cancel-wait-delete)
	if s.imageExportService != nil {
		if err := s.deleteRelatedImageExports(ctx, orgId, name); err != nil {
			s.log.WithError(err).WithField("name", name).Warn("Error deleting related ImageExports, proceeding with ImageBuild deletion")
			// Don't fail - proceed with deleting the ImageBuild
		}
	}

	// If ImageBuild is in a cancelable state, cancel it first and wait
	if isCancelableBuildState(imageBuild) {
		s.log.WithField("orgId", orgId).WithField("name", name).Info("ImageBuild is in cancelable state, canceling before delete")

		if _, err := s.CancelWithReason(ctx, orgId, name, "Build cancellation requested"); err != nil {
			s.log.WithError(err).WithField("name", name).Warn("Failed to cancel ImageBuild before delete, proceeding with delete")
		} else {
			// Wait for cancellation to complete
			timeout := 30 * time.Second
			if s.cfg != nil {
				timeout = time.Duration(s.cfg.DeleteCancelTimeout)
			}
			s.log.WithField("name", name).Info("Waiting for ImageBuild cancellation to complete")
			if err := waitForCanceled(ctx, s.kvStore, s.log, getImageBuildCanceledStreamKey(orgId, name), timeout); err != nil {
				s.log.WithError(err).WithField("name", name).Warn("Timeout waiting for ImageBuild cancellation, proceeding with delete")
			} else {
				s.log.WithField("name", name).Info("ImageBuild cancellation completed, proceeding with delete")
			}
		}
	}

	// Now delete the ImageBuild
	result, err := s.store.Delete(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, string(domain.ResourceKindImageBuild), &name)
}

// deleteRelatedImageExports deletes all ImageExports that reference the given ImageBuild
func (s *imageBuildService) deleteRelatedImageExports(ctx context.Context, orgId uuid.UUID, imageBuildName string) error {
	// List all ImageExports that reference this ImageBuild
	fieldSelectorStr := fmt.Sprintf("spec.source.imageBuildRef=%s", imageBuildName)

	exports, status := s.imageExportService.List(ctx, orgId, domain.ListImageExportsParams{
		FieldSelector: lo.ToPtr(fieldSelectorStr),
	})
	if !IsStatusOK(status) {
		return fmt.Errorf("failed to list ImageExports: %s", status.Message)
	}

	// Delete each ImageExport using the service (which does cancel-wait-delete)
	for _, export := range exports.Items {
		exportName := lo.FromPtr(export.Metadata.Name)
		s.log.WithField("imageBuild", imageBuildName).WithField("imageExport", exportName).Info("Deleting related ImageExport")
		if _, delStatus := s.imageExportService.Delete(ctx, orgId, exportName); !IsStatusOK(delStatus) {
			s.log.WithField("imageExport", exportName).WithField("status", delStatus.Message).Warn("Failed to delete related ImageExport")
			// Continue deleting other exports
		}
	}

	return nil
}

func (s *imageBuildService) Cancel(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImageBuild, error) {
	return s.CancelWithReason(ctx, orgId, name, "Build cancellation requested")
}

func (s *imageBuildService) CancelWithReason(ctx context.Context, orgId uuid.UUID, name string, reason string) (*domain.ImageBuild, error) {
	const maxRetries = 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err := s.tryCancelImageBuild(ctx, orgId, name, reason)
		if err == nil {
			return result, nil
		}

		// Retry on version conflict (race condition with worker)
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			s.log.WithField("name", name).WithField("attempt", attempt+1).Debug("Retrying cancel after version conflict")
			continue
		}

		// Non-retryable error
		return nil, err
	}

	return nil, fmt.Errorf("failed to cancel ImageBuild after %d attempts due to concurrent modifications", maxRetries)
}

// tryCancelImageBuild attempts to cancel an ImageBuild once
func (s *imageBuildService) tryCancelImageBuild(ctx context.Context, orgId uuid.UUID, name string, reason string) (*domain.ImageBuild, error) {
	// 1. Get current ImageBuild
	imageBuild, err := s.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, err
	}

	// 2. Validate cancelable state (Pending, Building, Pushing)
	if !isCancelableState(imageBuild) {
		return nil, ErrNotCancelable
	}

	// 3. Initialize status if needed
	if imageBuild.Status == nil {
		imageBuild.Status = &domain.ImageBuildStatus{}
	}
	if imageBuild.Status.Conditions == nil {
		imageBuild.Status.Conditions = &[]domain.ImageBuildCondition{}
	}

	// 4. Determine target state based on current state
	// - Pending: go directly to Canceled (no active processing to stop)
	// - Building/Pushing: go to Canceling (worker will complete the cancellation)
	currentState := getCurrentBuildState(imageBuild)
	isPending := currentState == "" || currentState == string(domain.ImageBuildConditionReasonPending)

	var targetReason string
	if isPending {
		targetReason = string(domain.ImageBuildConditionReasonCanceled)
	} else {
		targetReason = string(domain.ImageBuildConditionReasonCanceling)
	}

	condition := domain.ImageBuildCondition{
		Type:               domain.ImageBuildConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             targetReason,
		Message:            reason,
		LastTransitionTime: time.Now().UTC(),
	}
	domain.SetImageBuildStatusCondition(imageBuild.Status.Conditions, condition)

	result, err := s.store.UpdateStatus(ctx, orgId, imageBuild)
	if err != nil {
		return nil, err
	}

	// 5. If we set to Canceling (active processing), write to Redis Stream
	// If we set directly to Canceled, signal completion for cancel-then-delete flow
	if s.kvStore != nil {
		if targetReason == string(domain.ImageBuildConditionReasonCanceling) {
			if _, err := s.kvStore.StreamAdd(ctx, getImageBuildCancelStreamKey(orgId, name), []byte("cancel")); err != nil {
				s.log.WithError(err).Warn("Failed to write cancellation to Redis stream")
			}
			if err := s.kvStore.SetExpire(ctx, getImageBuildCancelStreamKey(orgId, name), 1*time.Hour); err != nil {
				s.log.WithError(err).Warn("Failed to set TTL on cancellation stream key")
			}
		} else {
			// Signal cancellation completion for cancel-then-delete flow
			canceledStreamKey := getImageBuildCanceledStreamKey(orgId, name)
			if _, err := s.kvStore.StreamAdd(ctx, canceledStreamKey, []byte("canceled")); err != nil {
				s.log.WithError(err).Warn("Failed to write cancellation completion signal to Redis")
			} else if err := s.kvStore.SetExpire(ctx, canceledStreamKey, 5*time.Minute); err != nil {
				s.log.WithError(err).Warn("Failed to set TTL on cancellation completion signal key")
			}
		}
	}

	s.log.WithField("orgId", orgId).WithField("name", name).WithField("reason", reason).WithField("targetState", targetReason).Info("ImageBuild cancellation requested")
	return result, nil
}

// getCurrentBuildState returns the current state reason or empty string if none
func getCurrentBuildState(imageBuild *domain.ImageBuild) string {
	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		return ""
	}
	readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		return ""
	}
	return readyCondition.Reason
}

// isCancelableState checks if an ImageBuild is in a state that can be canceled (used by Cancel API)
func isCancelableState(imageBuild *domain.ImageBuild) bool {
	return isCancelableBuildState(imageBuild)
}

// isCancelableBuildState checks if an ImageBuild is in a state that can be canceled
// Anything NOT in a terminal state is cancelable
// Terminal states: Canceled, Canceling, Completed, Failed
func isCancelableBuildState(imageBuild *domain.ImageBuild) bool {
	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		// No status yet - treat as Pending, which is cancelable
		return true
	}

	readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		// No Ready condition - treat as Pending
		return true
	}

	reason := readyCondition.Reason
	// Anything NOT in a terminal state is cancelable
	return reason != string(domain.ImageBuildConditionReasonCanceled) &&
		reason != string(domain.ImageBuildConditionReasonCanceling) &&
		reason != string(domain.ImageBuildConditionReasonCompleted) &&
		reason != string(domain.ImageBuildConditionReasonFailed)
}

// getImageBuildCanceledStreamKey returns the Redis stream key for cancellation completion signals
func getImageBuildCanceledStreamKey(orgId uuid.UUID, name string) string {
	return fmt.Sprintf("imagebuild:canceled:%s:%s", orgId.String(), name)
}

// getImageBuildCancelStreamKey returns the Redis stream key for cancellation requests
func getImageBuildCancelStreamKey(orgId uuid.UUID, name string) string {
	return fmt.Sprintf("imagebuild:cancel:%s:%s", orgId.String(), name)
}

// Internal methods (not exposed via API)

func (s *imageBuildService) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *domain.ImageBuild) (*domain.ImageBuild, error) {
	// Update status
	result, err := s.store.UpdateStatus(ctx, orgId, imageBuild)
	if err != nil {
		return result, err
	}

	// Create event for status update
	var event *coredomain.Event
	if result != nil && result.Metadata.Name != nil && s.eventHandler != nil {
		// Create a simple status update event since status is not in UpdatedFields enum
		event = coredomain.GetBaseEvent(
			ctx,
			coredomain.ResourceKind(string(domain.ResourceKindImageBuild)),
			*result.Metadata.Name,
			coredomain.EventReasonResourceUpdated,
			fmt.Sprintf("%s status was updated successfully.", string(domain.ResourceKindImageBuild)),
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
			readyCondition := domain.FindImageBuildStatusCondition(*result.Status.Conditions, domain.ImageBuildConditionTypeReady)
			if readyCondition != nil &&
				readyCondition.Status == domain.ConditionStatusTrue &&
				readyCondition.Reason == string(domain.ImageBuildConditionReasonCompleted) {
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
func (s *imageBuildService) GetLogs(ctx context.Context, orgId uuid.UUID, name string, follow bool) (LogStreamReader, string, domain.Status) {
	// First, get the ImageBuild to check its status
	imageBuild, status := s.Get(ctx, orgId, name, false)
	if imageBuild == nil || !IsStatusOK(status) {
		return nil, "", status
	}

	// Check if build is active (Building or Pushing)
	isActive := false
	if imageBuild.Status != nil && imageBuild.Status.Conditions != nil {
		readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
		if readyCondition != nil {
			reason := readyCondition.Reason
			if reason == string(domain.ImageBuildConditionReasonBuilding) || reason == string(domain.ImageBuildConditionReasonPushing) {
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
func (s *imageBuildService) validate(ctx context.Context, orgId uuid.UUID, imageBuild *domain.ImageBuild) ([]error, error) {
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
			if specType != string(domain.RepoSpecTypeOci) {
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
			if specType != string(domain.RepoSpecTypeOci) {
				errs = append(errs, fmt.Errorf("spec.destination.repository: Repository %q must be of type 'oci', got %q", imageBuild.Spec.Destination.Repository, specType))
			} else {
				ociSpec, err := repo.Spec.AsOciRepoSpec()
				if err != nil {
					return nil, fmt.Errorf("failed to get destination repository OCI spec: %w", err)
				}
				accessMode := lo.FromPtrOr(ociSpec.AccessMode, domain.Read)
				if accessMode != domain.ReadWrite {
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
func (s *imageBuildService) enqueueImageBuildEvent(ctx context.Context, orgId uuid.UUID, event *coredomain.Event) error {
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
