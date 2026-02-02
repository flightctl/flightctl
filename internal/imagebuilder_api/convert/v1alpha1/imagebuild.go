package v1alpha1

import (
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
)

// ImageBuildConverter converts between v1alpha1 API types and domain types for ImageBuild resources.
type ImageBuildConverter interface {
	// Core resource conversions
	ToDomain(api.ImageBuild) domain.ImageBuild
	FromDomain(*domain.ImageBuild) *api.ImageBuild
	ListFromDomain(*domain.ImageBuildList) *api.ImageBuildList

	// Params conversions
	ListParamsToDomain(api.ListImageBuildsParams) domain.ListImageBuildsParams
	GetParamsToDomain(api.GetImageBuildParams) domain.GetImageBuildParams
	GetLogParamsToDomain(api.GetImageBuildLogParams) domain.GetImageBuildLogParams
}

type imageBuildConverter struct{}

// NewImageBuildConverter creates a new ImageBuildConverter.
func NewImageBuildConverter() ImageBuildConverter {
	return &imageBuildConverter{}
}

func (c *imageBuildConverter) ToDomain(ib api.ImageBuild) domain.ImageBuild {
	return ib
}

func (c *imageBuildConverter) FromDomain(ib *domain.ImageBuild) *api.ImageBuild {
	return ib
}

func (c *imageBuildConverter) ListFromDomain(l *domain.ImageBuildList) *api.ImageBuildList {
	return l
}

func (c *imageBuildConverter) ListParamsToDomain(p api.ListImageBuildsParams) domain.ListImageBuildsParams {
	return p
}

func (c *imageBuildConverter) GetParamsToDomain(p api.GetImageBuildParams) domain.GetImageBuildParams {
	return p
}

func (c *imageBuildConverter) GetLogParamsToDomain(p api.GetImageBuildLogParams) domain.GetImageBuildLogParams {
	return p
}
