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
}

// service is the concrete implementation of Service
type service struct {
	imageBuild  ImageBuildService
	imageExport ImageExportService
}

// NewService creates a new aggregate Service with all sub-services
func NewService(s store.Store, log logrus.FieldLogger) Service {
	return &service{
		imageBuild:  NewImageBuildService(s.ImageBuild(), log),
		imageExport: NewImageExportService(s.ImageExport(), s.ImageBuild(), log),
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
