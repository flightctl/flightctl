package service

import (
	"context"

	"github.com/flightctl/flightctl/api/v1beta1"
	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// ImagePipelineService handles atomic creation of ImageBuild with optional ImageExport
type ImagePipelineService interface {
	Create(ctx context.Context, orgId uuid.UUID, req api.ImagePipelineRequest) (*api.ImagePipelineResponse, v1beta1.Status)
}

// imagePipelineService is the concrete implementation
type imagePipelineService struct {
	store          store.ImagePipelineStore
	imageBuildSvc  ImageBuildService
	imageExportSvc ImageExportService
	log            logrus.FieldLogger
}

// NewImagePipelineService creates a new ImagePipelineService
func NewImagePipelineService(s store.ImagePipelineStore, imageBuildSvc ImageBuildService, imageExportSvc ImageExportService, log logrus.FieldLogger) ImagePipelineService {
	return &imagePipelineService{
		store:          s,
		imageBuildSvc:  imageBuildSvc,
		imageExportSvc: imageExportSvc,
		log:            log,
	}
}

// Create creates an ImageBuild and optionally an ImageExport atomically in a single transaction
func (s *imagePipelineService) Create(ctx context.Context, orgId uuid.UUID, req api.ImagePipelineRequest) (*api.ImagePipelineResponse, v1beta1.Status) {
	var createdBuild *api.ImageBuild
	var createdExport *api.ImageExport
	var status v1beta1.Status

	// If ImageExport is provided, set up the source reference to the ImageBuild
	var imageExport *api.ImageExport
	if req.ImageExport != nil {
		imageExport = req.ImageExport

		// Validate ImageBuild metadata name before dereferencing
		if req.ImageBuild.Metadata.Name == nil {
			return nil, StatusBadRequest("imageBuild.metadata.name is required")
		}

		// Override source to reference the ImageBuild being created
		imageBuildName := *req.ImageBuild.Metadata.Name
		source := api.ImageBuildRefSource{
			Type:          api.ImageBuildRefSourceTypeImageBuild,
			ImageBuildRef: imageBuildName,
		}
		if err := imageExport.Spec.Source.FromImageBuildRefSource(source); err != nil {
			return nil, StatusInternalServerError("failed to set imageExport source: " + err.Error())
		}
	}

	// Execute in a transaction - the transaction is passed via context to the stores
	err := s.store.Transaction(ctx, func(txCtx context.Context) error {
		// Create ImageBuild using the existing service
		createdBuild, status = s.imageBuildSvc.Create(txCtx, orgId, req.ImageBuild)
		if !IsStatusOK(status) {
			return statusToError(status)
		}

		// Create ImageExport if provided using the existing service
		if imageExport != nil {
			createdExport, status = s.imageExportSvc.Create(txCtx, orgId, *imageExport)
			if !IsStatusOK(status) {
				return statusToError(status)
			}
		}

		return nil
	})

	if err != nil {
		// If we have a status error, return it directly
		if !IsStatusOK(status) {
			return nil, status
		}
		// Otherwise it's a transaction/db error
		return nil, StatusInternalServerError(err.Error())
	}

	return &api.ImagePipelineResponse{
		ImageBuild:  *createdBuild,
		ImageExport: createdExport,
	}, StatusCreated()
}

// statusToError converts a non-OK status to an error for transaction rollback
type statusError struct {
	status v1beta1.Status
}

func (e *statusError) Error() string {
	return e.status.Message
}

func statusToError(status v1beta1.Status) error {
	return &statusError{status: status}
}
