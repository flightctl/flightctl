package v1alpha1

// Converter aggregates all resource-specific converters for imagebuilder v1alpha1 API.
type Converter interface {
	ImageBuild() ImageBuildConverter
	ImageExport() ImageExportConverter
}

type converterImpl struct {
	imageBuild  ImageBuildConverter
	imageExport ImageExportConverter
}

// NewConverter creates a new Converter instance with all resource converters.
func NewConverter() Converter {
	return &converterImpl{
		imageBuild:  NewImageBuildConverter(),
		imageExport: NewImageExportConverter(),
	}
}

func (c *converterImpl) ImageBuild() ImageBuildConverter {
	return c.imageBuild
}

func (c *converterImpl) ImageExport() ImageExportConverter {
	return c.imageExport
}
