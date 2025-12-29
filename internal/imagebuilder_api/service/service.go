package service

import (
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/sirupsen/logrus"
)

// Service is the aggregate service interface for the ImageBuilder API.
// It provides access to all sub-services (ImageBuild, ImageExport, and future services).
type Service interface {
	ImageBuild() ImageBuildService
	ImageExport() ImageExportService
	ImagePipeline() ImagePipelineService
}

// service is the concrete implementation of Service
type service struct {
	imageBuild    ImageBuildService
	imageExport   ImageExportService
	imagePipeline ImagePipelineService
}

// NewService creates a new aggregate Service with all sub-services
func NewService(s store.Store, log logrus.FieldLogger) Service {
	imageBuildSvc := NewImageBuildService(s.ImageBuild(), log)
	imageExportSvc := NewImageExportService(s.ImageExport(), s.ImageBuild(), log)
	return &service{
		imageBuild:    imageBuildSvc,
		imageExport:   imageExportSvc,
		imagePipeline: NewImagePipelineService(s.ImagePipeline(), imageBuildSvc, imageExportSvc, log),
	}
}

// ImageBuild returns the ImageBuildService
func (s *service) ImageBuild() ImageBuildService {
	return s.imageBuild
}

// ImageExport returns the ImageExportService
func (s *service) ImageExport() ImageExportService {
	return s.imageExport
}

// ImagePipeline returns the ImagePipelineService
func (s *service) ImagePipeline() ImagePipelineService {
	return s.imagePipeline
}
