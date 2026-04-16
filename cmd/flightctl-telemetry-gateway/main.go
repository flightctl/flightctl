package main

import (
	"context"

	"github.com/flightctl/flightctl/internal/cmdsetup"
	tg "github.com/flightctl/flightctl/internal/telemetry_gateway"
)

func main() {
	ctx, cfg, log, shutdown := cmdsetup.InitService(context.Background(), "telemetry-gateway")
	defer shutdown()

	log.SetLevel(cfg.TelemetryGateway.LogLevel.ToLevelWithDefault(log.Level))

	if err := tg.Run(ctx, cfg); err != nil {
		log.Fatalf("failed to create telemetry gateway: %v", err)
	}
}
