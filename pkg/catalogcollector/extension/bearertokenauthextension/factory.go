package bearertokenauthextension

import (
	"context"
	"fmt"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
)

// Type is the component type for the bearer token authentication extension.
const Type catalogcollector.ComponentType = "bearertokenauth"

type factory struct{}

// NewFactory returns an ExtensionFactory for the bearer token authentication
// extension.
func NewFactory() catalogcollector.ExtensionFactory {
	return &factory{}
}

func (f *factory) Type() catalogcollector.ComponentType { return Type }

func (f *factory) CreateDefaultConfig() catalogcollector.ComponentConfig { return &Config{} }

func (f *factory) CreateExtension(
	_ context.Context,
	settings catalogcollector.Settings,
	cfg catalogcollector.ComponentConfig,
) (catalogcollector.Extension, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("extension %q: unexpected config type %T", settings.ID, cfg)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("extension %q: %w", settings.ID, err)
	}
	return &extension{log: settings.Logger, token: c.Token, tokenFile: c.TokenFile}, nil
}
