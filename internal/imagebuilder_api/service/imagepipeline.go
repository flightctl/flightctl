package service

import (
	"context"

	"github.com/flightctl/flightctl/api/v1beta1"
	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// ImagePipelineService handles atomic creation of ImageBuild with optional ImageExports
type ImagePipelineService interface {
	Create(ctx context.Context, orgId uuid.UUID, req api.ImagePipelineRequest) (*api.ImagePipelineResponse, v1beta1.Status)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImagePipelineResponse, v1beta1.Status)
	List(ctx context.Context, orgId uuid.UUID, params api.ListImagePipelinesParams) (*api.ImagePipelineList, v1beta1.Status)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImagePipelineResponse, v1beta1.Status)
}

// imagePipelineService is the concrete implementation
type imagePipelineService struct {
	store            store.ImagePipelineStore
	imageBuildSvc    ImageBuildService
	imageExportSvc   ImageExportService
	imageBuildStore  store.ImageBuildStore
	imageExportStore store.ImageExportStore
	log              logrus.FieldLogger
}

// NewImagePipelineService creates a new ImagePipelineService
func NewImagePipelineService(s store.ImagePipelineStore, imageBuildSvc ImageBuildService, imageExportSvc ImageExportService, imageBuildStore store.ImageBuildStore, imageExportStore store.ImageExportStore, log logrus.FieldLogger) ImagePipelineService {
	return &imagePipelineService{
		store:            s,
		imageBuildSvc:    imageBuildSvc,
		imageExportSvc:   imageExportSvc,
		imageBuildStore:  imageBuildStore,
		imageExportStore: imageExportStore,
		log:              log,
	}
}

// Create creates an ImageBuild and optionally a list of ImageExports atomically in a single transaction
func (s *imagePipelineService) Create(ctx context.Context, orgId uuid.UUID, req api.ImagePipelineRequest) (*api.ImagePipelineResponse, v1beta1.Status) {
	var createdBuild *api.ImageBuild
	var createdExports []api.ImageExport
	var status v1beta1.Status

	// Validate ImageBuild metadata name before dereferencing
	if req.ImageBuild.Metadata.Name == nil {
		return nil, StatusBadRequest("imageBuild.metadata.name is required")
	}
	imageBuildName := *req.ImageBuild.Metadata.Name

	// If ImageExports are provided, set up the source reference to the ImageBuild
	if req.ImageExports != nil {
		for i := range *req.ImageExports {
			source := api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: imageBuildName,
			}
			if err := (*req.ImageExports)[i].Spec.Source.FromImageBuildRefSource(source); err != nil {
				return nil, StatusInternalServerError("failed to set imageExport source: " + err.Error())
			}
		}
	}

	// Execute in a transaction - the transaction is passed via context to the stores
	err := s.store.Transaction(ctx, func(txCtx context.Context) error {
		// Create ImageBuild using the existing service
		createdBuild, status = s.imageBuildSvc.Create(txCtx, orgId, req.ImageBuild)
		if !IsStatusOK(status) {
			return statusToError(status)
		}

		// Create ImageExports if provided using the existing service
		if req.ImageExports != nil {
			createdExports = make([]api.ImageExport, 0, len(*req.ImageExports))
			for i := range *req.ImageExports {
				var createdExport *api.ImageExport
				createdExport, status = s.imageExportSvc.Create(txCtx, orgId, (*req.ImageExports)[i])
				if !IsStatusOK(status) {
					return statusToError(status)
				}
				createdExports = append(createdExports, *createdExport)
			}
		}

		return nil
	})

	if err != nil {
		// If we have a status error, extract and return it
		if se, ok := err.(*statusError); ok {
			return nil, se.status
		}
		// If status was set (from ImageBuild or ImageExport creation), return it
		if !IsStatusOK(status) {
			return nil, status
		}
		// Otherwise it's a transaction/db error
		return nil, StatusInternalServerError(err.Error())
	}

	return &api.ImagePipelineResponse{
		ImageBuild:   *createdBuild,
		ImageExports: lo.Ternary(len(createdExports) > 0, &createdExports, nil),
	}, StatusCreated()
}

// Get retrieves an ImagePipeline by ImageBuild name, including all associated ImageExports using JOIN query
func (s *imagePipelineService) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImagePipelineResponse, v1beta1.Status) {
	imageBuild, imageExports, err := s.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, "ImagePipeline", &name)
	}

	return &api.ImagePipelineResponse{
		ImageBuild:   *imageBuild,
		ImageExports: lo.Ternary(len(imageExports) > 0, &imageExports, nil),
	}, StatusOK()
}

// List retrieves a list of ImagePipelines using JOIN queries
func (s *imagePipelineService) List(ctx context.Context, orgId uuid.UUID, params api.ListImagePipelinesParams) (*api.ImagePipelineList, v1beta1.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if !IsStatusOK(status) {
		return nil, status
	}

	buildsWithExports, nextContinue, numRemaining, err := s.store.List(ctx, orgId, *listParams)
	if err != nil {
		var se *selector.SelectorError
		switch {
		case selector.AsSelectorError(err, &se):
			return nil, StatusBadRequest(se.Error())
		default:
			return nil, StatusInternalServerError(err.Error())
		}
	}

	// Convert to API response format
	items := make([]api.ImagePipelineResponse, 0, len(buildsWithExports))
	for i := range buildsWithExports {
		items = append(items, api.ImagePipelineResponse{
			ImageBuild:   *buildsWithExports[i].ImageBuild,
			ImageExports: lo.Ternary(len(buildsWithExports[i].ImageExports) > 0, &buildsWithExports[i].ImageExports, nil),
		})
	}

	// Build metadata
	metadata := v1beta1.ListMeta{
		Continue:           nextContinue,
		RemainingItemCount: numRemaining,
	}

	return &api.ImagePipelineList{
		ApiVersion: "v1beta1",
		Kind:       "ImagePipelineList",
		Metadata:   metadata,
		Items:      items,
	}, StatusOK()
}

// Delete deletes an ImagePipeline by name, deleting all associated ImageExports and then the ImageBuild in a single transaction
func (s *imagePipelineService) Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImagePipelineResponse, v1beta1.Status) {
	// First, get the ImageBuild and associated ImageExports to return in the response
	imageBuild, imageExports, err := s.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, "ImagePipeline", &name)
	}

	// Execute deletion in a transaction
	err = s.store.Transaction(ctx, func(txCtx context.Context) error {
		// Delete all associated ImageExports first
		for i := range imageExports {
			exportName := lo.FromPtr(imageExports[i].Metadata.Name)
			_, err := s.imageExportStore.Delete(txCtx, orgId, exportName)
			if err != nil {
				return err
			}
		}

		// Delete the ImageBuild
		_, err := s.imageBuildStore.Delete(txCtx, orgId, name)
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, "ImagePipeline", &name)
	}

	return &api.ImagePipelineResponse{
		ImageBuild:   *imageBuild,
		ImageExports: lo.Ternary(len(imageExports) > 0, &imageExports, nil),
	}, StatusOK()
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
