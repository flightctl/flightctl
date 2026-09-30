package oauth2clientauthextension

import (
	"context"
	"fmt"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
)

// Type is the component type for the OAuth2 client-credentials authentication
// extension.
const Type catalogcollector.ComponentType = "oauth2client"

var _ catalogcollector.ExtensionFactory = (*factory)(nil)

type factory struct{}

// NewFactory returns an ExtensionFactory for the OAuth2 client-credentials
// authentication extension.
func NewFactory() catalogcollector.ExtensionFactory {
	return &factory{}
}

func (f *factory) Type() catalogcollector.ComponentType {
	return Type
}

func (f *factory) CreateDefaultConfig() catalogcollector.ComponentConfig {
	return &Config{}
}

func (f *factory) CreateExtension(
	ctx context.Context,
	settings catalogcollector.Settings,
	cfg catalogcollector.ComponentConfig,
) (catalogcollector.Extension, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf(
			"extension %q: unexpected config type %T",
			settings.ID,
			cfg,
		)
	}

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("extension %q: %w", settings.ID, err)
	}

	ext, err := newExtension(ctx, c, settings.Logger)
	if err != nil {
		return nil, fmt.Errorf("extension %q: %w", settings.ID, err)
	}

	return ext, nil
}
