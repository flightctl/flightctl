package service

import (
	"context"

	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	internalservice "github.com/flightctl/flightctl/internal/service"
	mainstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/queues"
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
func NewService(ctx context.Context, s imagebuilderstore.Store, mainStore mainstore.Store, queueProducer queues.QueueProducer, log logrus.FieldLogger) Service {
	// Create event handler for ImageBuild events
	// Note: We pass nil for workerClient so events are stored in DB for audit/logging
	// but are not pushed to TaskQueue. Events are manually enqueued to ImageBuildTaskQueue instead.
	var eventHandler *internalservice.EventHandler
	if mainStore != nil {
		eventHandler = internalservice.NewEventHandler(mainStore, nil, log)
	}

	imageBuildSvc := NewImageBuildService(s.ImageBuild(), eventHandler, queueProducer, log)
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
