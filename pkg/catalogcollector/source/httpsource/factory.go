package httpsource

import (
	"context"
	"fmt"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
)

// Type is the component type for the HTTP snapshot source.
const Type catalogcollector.ComponentType = "http"

type factory struct{}

// NewFactory returns a SourceFactory for the HTTP snapshot source.
func NewFactory() catalogcollector.SourceFactory {
	return &factory{}
}

func (f *factory) Type() catalogcollector.ComponentType {
	return Type
}

func (f *factory) CreateDefaultConfig() catalogcollector.ComponentConfig {
	return &Config{
		ListenAddress: "127.0.0.1:8080",
		Path:          "/v1/snapshots",
	}
}

func (f *factory) CreateSource(
	_ context.Context,
	settings catalogcollector.Settings,
	cfg catalogcollector.ComponentConfig,
	next catalogcollector.Consumer,
) (catalogcollector.Source, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("source %q: unexpected config type %T", settings.ID, cfg)
	}
	return &server{
		cfg:  c,
		id:   settings.ID,
		log:  settings.Logger,
		next: next,
	}, nil
}
