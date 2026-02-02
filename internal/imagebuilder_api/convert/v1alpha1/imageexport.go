package v1alpha1

import (
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
)

// ImageExportConverter converts between v1alpha1 API types and domain types for ImageExport resources.
type ImageExportConverter interface {
	// Core resource conversions
	ToDomain(api.ImageExport) domain.ImageExport
	FromDomain(*domain.ImageExport) *api.ImageExport
	ListFromDomain(*domain.ImageExportList) *api.ImageExportList

	// Params conversions
	ListParamsToDomain(api.ListImageExportsParams) domain.ListImageExportsParams
	GetLogParamsToDomain(api.GetImageExportLogParams) domain.GetImageExportLogParams
}

type imageExportConverter struct{}

// NewImageExportConverter creates a new ImageExportConverter.
func NewImageExportConverter() ImageExportConverter {
	return &imageExportConverter{}
}

func (c *imageExportConverter) ToDomain(ie api.ImageExport) domain.ImageExport {
	return ie
}

func (c *imageExportConverter) FromDomain(ie *domain.ImageExport) *api.ImageExport {
	return ie
}

func (c *imageExportConverter) ListFromDomain(l *domain.ImageExportList) *api.ImageExportList {
	return l
}

func (c *imageExportConverter) ListParamsToDomain(p api.ListImageExportsParams) domain.ListImageExportsParams {
	return p
}

func (c *imageExportConverter) GetLogParamsToDomain(p api.GetImageExportLogParams) domain.GetImageExportLogParams {
	return p
}
