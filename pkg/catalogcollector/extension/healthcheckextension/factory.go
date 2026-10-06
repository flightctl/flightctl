package healthcheckextension

import (
	"context"
	"fmt"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
)

// Type is the component type for the healthcheck extension.
const Type catalogcollector.ComponentType = "healthcheck"

type factory struct{}

// NewFactory returns an ExtensionFactory for the healthcheck extension.
func NewFactory() catalogcollector.ExtensionFactory {
	return &factory{}
}

func (f *factory) Type() catalogcollector.ComponentType {
	return Type
}

func (f *factory) CreateDefaultConfig() catalogcollector.ComponentConfig {
	return &Config{
		Endpoint:  defaultEndpoint,
		LivePath:  defaultLivePath,
		ReadyPath: defaultReadyPath,
	}
}

func (f *factory) CreateExtension(
	_ context.Context,
	settings catalogcollector.Settings,
	cfg catalogcollector.ComponentConfig,
) (catalogcollector.Extension, error) {
	healthConfig, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf(
			"extension %q: unexpected config type %T",
			settings.ID,
			cfg,
		)
	}
	if err := healthConfig.Validate(); err != nil {
		return nil, fmt.Errorf("extension %q: %w", settings.ID, err)
	}
	if settings.Logger == nil {
		return nil, fmt.Errorf(
			"extension %q: logger must not be nil",
			settings.ID,
		)
	}

	return &extension{
		log: settings.Logger,
		cfg: healthConfig,
	}, nil
}
