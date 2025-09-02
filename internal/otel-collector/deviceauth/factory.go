package deviceauth

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

func NewFactory(cfg *config.Config) extension.Factory {
	return extension.NewFactory(
		component.MustNewType("deviceauth"),
		func() component.Config {
			return &deviceAuthConfig{
				AppCfg: cfg,
			}
		},
		createExtension,
		component.StabilityLevelAlpha,
	)
}

func createExtension(
	ctx context.Context,
	set extension.Settings,
	cfg component.Config,
) (extension.Extension, error) {
	c := cfg.(*deviceAuthConfig)
	return newDeviceAuth(ctx, set, c), nil
}
