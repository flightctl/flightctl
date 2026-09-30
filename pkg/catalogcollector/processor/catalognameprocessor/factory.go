package catalognameprocessor

import (
	"context"
	"fmt"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
)

// Type is the component type for the catalog name mapping processor.
const Type catalogcollector.ComponentType = "catalogname"

type factory struct{}

// NewFactory returns a ProcessorFactory for the catalog name mapping processor.
func NewFactory() catalogcollector.ProcessorFactory {
	return &factory{}
}

func (f *factory) Type() catalogcollector.ComponentType {
	return Type
}

func (f *factory) CreateDefaultConfig() catalogcollector.ComponentConfig {
	return &Config{}
}

func (f *factory) CreateProcessor(
	_ context.Context,
	settings catalogcollector.Settings,
	cfg catalogcollector.ComponentConfig,
	next catalogcollector.Consumer,
) (catalogcollector.Consumer, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("processor %q: unexpected config type %T", settings.ID, cfg)
	}
	return &processor{
		id:       settings.ID,
		log:      settings.Logger,
		mappings: c.Mappings,
		next:     next,
	}, nil
}
