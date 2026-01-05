package deviceauth

import (
	"context"

	tgconfig "github.com/flightctl/flightctl/internal/config/telemetrygateway"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

func NewFactory(cfg *tgconfig.Config) extension.Factory {
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
