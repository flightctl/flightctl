package debugdestination

import (
	"context"
	"fmt"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
)

// Type is the component type for the debug destination.
const Type catalogcollector.ComponentType = "debug"

type factory struct{}

// NewFactory returns a DestinationFactory for the debug destination.
func NewFactory() catalogcollector.DestinationFactory {
	return &factory{}
}

func (f *factory) Type() catalogcollector.ComponentType {
	return Type
}

func (f *factory) CreateDefaultConfig() catalogcollector.ComponentConfig {
	return &Config{
		Verbosity: VerbosityBasic,
	}
}

func (f *factory) CreateDestination(
	_ context.Context,
	settings catalogcollector.Settings,
	cfg catalogcollector.ComponentConfig,
) (catalogcollector.Destination, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf(
			"destination %q: unexpected config type %T",
			settings.ID,
			cfg,
		)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("destination %q: %w", settings.ID, err)
	}
	if settings.Logger == nil {
		return nil, fmt.Errorf(
			"destination %q: logger must not be nil",
			settings.ID,
		)
	}

	return &destination{
		log:       settings.Logger,
		verbosity: c.Verbosity,
	}, nil
}
