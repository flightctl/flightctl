package cmdsetup

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

func InitService(ctx context.Context, name string) (newCtx context.Context, cfg *config.Config, logger *logrus.Logger, shutdown func()) {
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	serviceName := fmt.Sprintf("flightctl-%s", name)

	logger = log.InitLogs()
	logger.SetLevel(cfg.Service.LogLevel.ToLevelWithDefault(logrus.InfoLevel))
	logger.Printf("Initializing %s", serviceName)
	logger.Printf("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(logger, cfg, serviceName)

	ctx, _ = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)

	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, serviceName)
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, fmt.Sprintf("service:%s", serviceName))

	return ctx, cfg, logger, func() {
		logger.Infof("Shutting down %s", serviceName)

		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := tracerShutdown(ctx); err != nil {
			logger.Errorf("failed to shut down tracer: %v", err)
		}
	}
}
