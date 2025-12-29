package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// ImageExportService handles business logic for ImageExport resources
type ImageExportService interface {
	Create(ctx context.Context, orgId uuid.UUID, imageExport api.ImageExport) (*api.ImageExport, v1beta1.Status)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status)
	List(ctx context.Context, orgId uuid.UUID, params api.ListImageExportsParams) (*api.ImageExportList, v1beta1.Status)
	Delete(ctx context.Context, orgId uuid.UUID, name string) v1beta1.Status
	// Internal methods (not exposed via API)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error)
	UpdateNextRetryAt(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
	UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
	ListPendingRetry(ctx context.Context, orgId uuid.UUID, beforeTime time.Time) (*api.ImageExportList, error)
}

// imageExportService is the concrete implementation of ImageExportService
type imageExportService struct {
	imageExportStore store.ImageExportStore
	imageBuildStore  store.ImageBuildStore
	log              logrus.FieldLogger
}

// NewImageExportService creates a new ImageExportService
func NewImageExportService(imageExportStore store.ImageExportStore, imageBuildStore store.ImageBuildStore, log logrus.FieldLogger) ImageExportService {
	return &imageExportService{
		imageExportStore: imageExportStore,
		imageBuildStore:  imageBuildStore,
		log:              log,
	}
}

func (s *imageExportService) Create(ctx context.Context, orgId uuid.UUID, imageExport api.ImageExport) (*api.ImageExport, v1beta1.Status) {
	// Don't set fields that are managed by the service
	imageExport.Status = nil
	NilOutManagedObjectMetaProperties(&imageExport.Metadata)

	// Validate input
	if errs := s.validate(ctx, orgId, &imageExport); len(errs) > 0 {
		return nil, StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := s.imageExportStore.Create(ctx, orgId, &imageExport)
	return result, StoreErrorToApiStatus(err, true, api.ImageExportKind, imageExport.Metadata.Name)
}

func (s *imageExportService) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status) {
	result, err := s.imageExportStore.Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.ImageExportKind, &name)
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

func (s *imageExportService) Delete(ctx context.Context, orgId uuid.UUID, name string) v1beta1.Status {
	err := s.imageExportStore.Delete(ctx, orgId, name)
	return StoreErrorToApiStatus(err, false, api.ImageExportKind, &name)
}

// Internal methods (not exposed via API)

func (s *imageExportService) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error) {
	return s.imageExportStore.UpdateStatus(ctx, orgId, imageExport)
}

func (s *imageExportService) UpdateNextRetryAt(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return s.imageExportStore.UpdateNextRetryAt(ctx, orgId, name, timestamp)
}

func (s *imageExportService) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return s.imageExportStore.UpdateLastSeen(ctx, orgId, name, timestamp)
}

func (s *imageExportService) ListPendingRetry(ctx context.Context, orgId uuid.UUID, beforeTime time.Time) (*api.ImageExportList, error) {
	return s.imageExportStore.ListPendingRetry(ctx, orgId, beforeTime)
}

// validate performs validation on an ImageExport resource
func (s *imageExportService) validate(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) []error {
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
				if err != nil {
					errs = append(errs, fmt.Errorf("spec.source.imageBuildRef: ImageBuild %q not found", source.ImageBuildRef))
				}
			}
		case string(api.ImageExportSourceTypeImageReference):
			source, err := imageExport.Spec.Source.AsImageReferenceSource()
			if err != nil {
				errs = append(errs, errors.New("invalid imageReference source"))
			} else {
				if source.Repository == "" {
					errs = append(errs, errors.New("spec.source.repository is required for imageReference source type"))
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

	return errs
}
