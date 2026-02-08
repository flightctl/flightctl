package v1alpha1

import (
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	apiversioning "github.com/flightctl/flightctl/api/versioning"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
)

// Converter aggregates all resource-specific converters for imagebuilder v1alpha1 API.
type Converter interface {
	ImageBuild() ImageBuildConverter
	ImageExport() ImageExportConverter
	Common() CommonConverter
}

type converterImpl struct {
	imageBuild  ImageBuildConverter
	imageExport ImageExportConverter
	common      CommonConverter
}

// NewConverter creates a new Converter instance with all resource converters.
func NewConverter() Converter {
	return &converterImpl{
		imageBuild:  NewImageBuildConverter(),
		imageExport: NewImageExportConverter(),
		common:      NewCommonConverter(),
	}
}

func (c *converterImpl) ImageBuild() ImageBuildConverter {
	return c.imageBuild
}

func (c *converterImpl) ImageExport() ImageExportConverter {
	return c.imageExport
}

func (c *converterImpl) Common() CommonConverter {
	return c.common
}

// CommonConverter converts common types for imagebuilder v1alpha1 API.
type CommonConverter interface {
	StatusFromDomain(domain.Status) api.Status
}

type commonConverter struct{}

// NewCommonConverter creates a new CommonConverter.
func NewCommonConverter() CommonConverter {
	return &commonConverter{}
}

func (c *commonConverter) StatusFromDomain(s domain.Status) api.Status {
	return api.Status{
		ApiVersion: apiversioning.QualifiedV1Alpha1,
		Kind:       api.StatusKind,
		Code:       s.Code,
		Message:    s.Message,
		Reason:     s.Reason,
		Status:     s.Status,
	}
}
