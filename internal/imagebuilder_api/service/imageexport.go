package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
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

// ImageExportService handles business logic for ImageExport resources
type ImageExportService interface {
	Create(ctx context.Context, orgId uuid.UUID, imageExport api.ImageExport) (*api.ImageExport, v1beta1.Status)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status)
	List(ctx context.Context, orgId uuid.UUID, params api.ListImageExportsParams) (*api.ImageExportList, v1beta1.Status)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status)
	// Internal methods (not exposed via API)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error)
	UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
}

// imageExportService is the concrete implementation of ImageExportService
type imageExportService struct {
	imageExportStore store.ImageExportStore
	imageBuildStore  store.ImageBuildStore
	repositoryStore  mainstore.Repository
	eventHandler     *internalservice.EventHandler
	queueProducer    queues.QueueProducer
	log              logrus.FieldLogger
}

// NewImageExportService creates a new ImageExportService
func NewImageExportService(imageExportStore store.ImageExportStore, imageBuildStore store.ImageBuildStore, repositoryStore mainstore.Repository, eventHandler *internalservice.EventHandler, queueProducer queues.QueueProducer, log logrus.FieldLogger) ImageExportService {
	return &imageExportService{
		imageExportStore: imageExportStore,
		imageBuildStore:  imageBuildStore,
		repositoryStore:  repositoryStore,
		eventHandler:     eventHandler,
		queueProducer:    queueProducer,
		log:              log,
	}
}

func (s *imageExportService) Create(ctx context.Context, orgId uuid.UUID, imageExport api.ImageExport) (*api.ImageExport, v1beta1.Status) {
	// Don't set fields that are managed by the service
	imageExport.Status = nil
	NilOutManagedObjectMetaProperties(&imageExport.Metadata)

	// Validate input
	if errs, internalErr := s.validate(ctx, orgId, &imageExport); internalErr != nil {
		return nil, StatusInternalServerError(internalErr.Error())
	} else if len(errs) > 0 {
		return nil, StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := s.imageExportStore.Create(ctx, orgId, &imageExport)
	if err != nil {
		return result, StoreErrorToApiStatus(err, true, string(api.ResourceKindImageExport), imageExport.Metadata.Name)
	}

	// Create event separately (no transaction)
	var event *v1beta1.Event
	if result != nil && s.eventHandler != nil {
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, v1beta1.ResourceKind(string(api.ResourceKindImageExport)), lo.FromPtr(result.Metadata.Name), nil, s.log, nil)
		if event != nil {
			s.eventHandler.CreateEvent(ctx, orgId, event)
		}
	}

	// Enqueue event to imagebuild-queue for worker processing
	if result != nil && event != nil && s.queueProducer != nil {
		if err := s.enqueueImageExportEvent(ctx, orgId, event); err != nil {
			s.log.WithError(err).WithField("orgId", orgId).WithField("name", lo.FromPtr(result.Metadata.Name)).Error("failed to enqueue imageExport event")
			// Don't fail the creation if enqueue fails - the event can be retried later
		}
	}

	return result, StoreErrorToApiStatus(nil, true, string(api.ResourceKindImageExport), imageExport.Metadata.Name)
}

func (s *imageExportService) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status) {
	result, err := s.imageExportStore.Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, string(api.ResourceKindImageExport), &name)
}

func (s *imageExportService) List(ctx context.Context, orgId uuid.UUID, params api.ListImageExportsParams) (*api.ImageExportList, v1beta1.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if !IsStatusOK(status) {
		return nil, status
	}

	result, err := s.imageExportStore.List(ctx, orgId, *listParams)
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

func (s *imageExportService) Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status) {
	result, err := s.imageExportStore.Delete(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, string(api.ResourceKindImageExport), &name)
}

// Internal methods (not exposed via API)

func (s *imageExportService) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error) {
	return s.imageExportStore.UpdateStatus(ctx, orgId, imageExport)
}

func (s *imageExportService) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return s.imageExportStore.UpdateLastSeen(ctx, orgId, name, timestamp)
}

// validate performs validation on an ImageExport resource
// Returns validation errors (4xx) and internal errors (5xx) separately
func (s *imageExportService) validate(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) ([]error, error) {
	var errs []error

	if lo.FromPtr(imageExport.Metadata.Name) == "" {
		errs = append(errs, errors.New("metadata.name is required"))
	}

	// Validate source - uses discriminator pattern
	sourceType, err := imageExport.Spec.Source.Discriminator()
	if err != nil {
		errs = append(errs, errors.New("spec.source.type is required"))
	} else {
		switch sourceType {
		case string(api.ImageExportSourceTypeImageBuild):
			source, err := imageExport.Spec.Source.AsImageBuildRefSource()
			if err != nil {
				errs = append(errs, errors.New("invalid imageBuild source"))
			} else if source.ImageBuildRef == "" {
				errs = append(errs, errors.New("spec.source.imageBuildRef is required for imageBuild source type"))
			} else {
				// Check that the referenced ImageBuild exists
				_, err = s.imageBuildStore.Get(ctx, orgId, source.ImageBuildRef)
				if errors.Is(err, flterrors.ErrResourceNotFound) {
					errs = append(errs, fmt.Errorf("spec.source.imageBuildRef: ImageBuild %q not found", source.ImageBuildRef))
				} else if err != nil {
					return nil, fmt.Errorf("failed to get ImageBuild %q: %w", source.ImageBuildRef, err)
				}
			}
		case string(api.ImageExportSourceTypeImageReference):
			source, err := imageExport.Spec.Source.AsImageReferenceSource()
			if err != nil {
				errs = append(errs, errors.New("invalid imageReference source"))
			} else {
				if source.Repository == "" {
					errs = append(errs, errors.New("spec.source.repository is required for imageReference source type"))
				} else {
					// Validate source repository exists and is OCI type
					repo, err := s.repositoryStore.Get(ctx, orgId, source.Repository)
					if errors.Is(err, flterrors.ErrResourceNotFound) {
						errs = append(errs, fmt.Errorf("spec.source.repository: Repository %q not found", source.Repository))
					} else if err != nil {
						return nil, fmt.Errorf("failed to get source repository %q: %w", source.Repository, err)
					} else {
						specType, err := repo.Spec.Discriminator()
						if err != nil {
							return nil, fmt.Errorf("failed to get source repository spec type: %w", err)
						}
						if specType != string(v1beta1.RepoSpecTypeOci) {
							errs = append(errs, fmt.Errorf("spec.source.repository: Repository %q must be of type 'oci', got %q", source.Repository, specType))
						}
					}
				}
				if source.ImageName == "" {
					errs = append(errs, errors.New("spec.source.imageName is required for imageReference source type"))
				}
				if source.ImageTag == "" {
					errs = append(errs, errors.New("spec.source.imageTag is required for imageReference source type"))
				}
			}
		default:
			errs = append(errs, errors.New("spec.source.type must be 'imageBuild' or 'imageReference'"))
		}
	}

	// Validate output
	if imageExport.Spec.Destination.Repository == "" {
		errs = append(errs, errors.New("spec.destination.repository is required"))
	} else {
		// Validate destination repository exists, is OCI type, and has ReadWrite access
		repo, err := s.repositoryStore.Get(ctx, orgId, imageExport.Spec.Destination.Repository)
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			errs = append(errs, fmt.Errorf("spec.destination.repository: Repository %q not found", imageExport.Spec.Destination.Repository))
		} else if err != nil {
			return nil, fmt.Errorf("failed to get destination repository %q: %w", imageExport.Spec.Destination.Repository, err)
		} else {
			specType, err := repo.Spec.Discriminator()
			if err != nil {
				return nil, fmt.Errorf("failed to get destination repository spec type: %w", err)
			}
			if specType != string(v1beta1.RepoSpecTypeOci) {
				errs = append(errs, fmt.Errorf("spec.destination.repository: Repository %q must be of type 'oci', got %q", imageExport.Spec.Destination.Repository, specType))
			} else {
				ociSpec, err := repo.Spec.AsOciRepoSpec()
				if err != nil {
					return nil, fmt.Errorf("failed to get destination repository OCI spec: %w", err)
				}
				accessMode := lo.FromPtrOr(ociSpec.AccessMode, v1beta1.Read)
				if accessMode != v1beta1.ReadWrite {
					errs = append(errs, fmt.Errorf("spec.destination.repository: Repository %q must have 'ReadWrite' access mode, got %q", imageExport.Spec.Destination.Repository, accessMode))
				}
			}
		}
	}
	if imageExport.Spec.Destination.ImageName == "" {
		errs = append(errs, errors.New("spec.destination.imageName is required"))
	}
	if imageExport.Spec.Destination.Tag == "" {
		errs = append(errs, errors.New("spec.destination.tag is required"))
	}

	// Validate formats
	if imageExport.Spec.Format == "" {
		errs = append(errs, errors.New("spec.format is required"))
	}

	return errs, nil
}

// enqueueImageExportEvent enqueues an event to the imagebuild-queue
func (s *imageExportService) enqueueImageExportEvent(ctx context.Context, orgId uuid.UUID, event *v1beta1.Event) error {
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

	s.log.WithField("orgId", orgId).WithField("name", event.InvolvedObject.Name).Info("enqueued imageExport event")
	return nil
}
