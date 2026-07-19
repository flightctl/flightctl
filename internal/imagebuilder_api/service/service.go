package service

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

// Service is the aggregate service interface for the ImageBuilder API.
// It provides access to all sub-services (ImageBuild, ImageExport, and future services).
type Service interface {
	ImageBuild() ImageBuildService
	ImageExport() ImageExportService
	ImagePromotion() ImagePromotionService
}

// service is the concrete implementation of Service
type service struct {
	imageBuild     ImageBuildService
	imageExport    ImageExportService
	imagePromotion ImagePromotionService
}

// NewService creates a new aggregate Service with all sub-services
func NewService(ctx context.Context, cfg *config.Config, s imagebuilderstore.Store, catalogs CatalogLookup, repositories RepositoryLookup, eventSvc events.Service, queueProducer queues.QueueProducer, kvStore kvstore.KVStore, log logrus.FieldLogger) Service {
	// Get ImageBuilderService config (nil-safe)
	var imageBuilderServiceCfg *config.ImageBuilderServiceConfig
	if cfg != nil {
		imageBuilderServiceCfg = cfg.ImageBuilderService
	}

	// Create ImagePromotionService
	imagePromotionSvc := NewImagePromotionService(s.ImagePromotion(), s.ImageBuild(), catalogs, queueProducer, log)

	// Create ImageExportService first (ImageBuildService depends on it for delete flow)
	imageExportSvc := NewImageExportService(s.ImageExport(), s.ImageBuild(), repositories, eventSvc, queueProducer, kvStore, imageBuilderServiceCfg, log)

	// Create ImageBuildService with ImageExportService and ImagePromotionService dependencies.
	// eventSvc is constructed with a nil worker_client so events are stored in DB for
	// audit/logging but are not pushed to TaskQueue - events are manually enqueued to
	// ImageBuildTaskQueue instead.
	imageBuildSvc := NewImageBuildService(s.ImageBuild(), repositories, imageExportSvc, imagePromotionSvc, eventSvc, queueProducer, kvStore, imageBuilderServiceCfg, log)

	return &service{
		imageBuild:     imageBuildSvc,
		imageExport:    imageExportSvc,
		imagePromotion: imagePromotionSvc,
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

// ImagePromotion returns the ImagePromotionService
func (s *service) ImagePromotion() ImagePromotionService {
	return s.imagePromotion
}
