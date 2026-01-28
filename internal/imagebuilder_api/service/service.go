package service

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/kvstore"
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
}

// service is the concrete implementation of Service
type service struct {
	imageBuild  ImageBuildService
	imageExport ImageExportService
}

// NewService creates a new aggregate Service with all sub-services
func NewService(ctx context.Context, cfg *config.Config, s imagebuilderstore.Store, mainStore mainstore.Store, queueProducer queues.QueueProducer, kvStore kvstore.KVStore, log logrus.FieldLogger) Service {
	// Create event handler for ImageBuild events
	// Note: We pass nil for workerClient so events are stored in DB for audit/logging
	// but are not pushed to TaskQueue. Events are manually enqueued to ImageBuildTaskQueue instead.
	var eventHandler *internalservice.EventHandler
	if mainStore != nil {
		eventHandler = internalservice.NewEventHandler(mainStore, nil, log)
	}

	// Get ImageBuilderService config (nil-safe)
	var imageBuilderServiceCfg *config.ImageBuilderServiceConfig
	if cfg != nil {
		imageBuilderServiceCfg = cfg.ImageBuilderService
	}

	// Create ImageExportService first (ImageBuildService depends on it for delete flow)
	imageExportSvc := NewImageExportService(s.ImageExport(), s.ImageBuild(), mainStore.Repository(), eventHandler, queueProducer, kvStore, imageBuilderServiceCfg, log)
	// Create ImageBuildService with ImageExportService dependency
	imageBuildSvc := NewImageBuildService(s.ImageBuild(), mainStore.Repository(), imageExportSvc, eventHandler, queueProducer, kvStore, imageBuilderServiceCfg, log)
	return &service{
		imageBuild:  imageBuildSvc,
		imageExport: imageExportSvc,
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
