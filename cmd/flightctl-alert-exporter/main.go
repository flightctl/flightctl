package main

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/alert_exporter"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/org/resolvers"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func main() {
	log := log.InitLogs()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdown.HandleSignals(log, cancel, shutdown.DefaultGracefulShutdownTimeout)
	if err := runCmd(ctx, log); err != nil {
		log.Fatalf("Alert exporter error: %v", err)
	}
}

func runCmd(ctx context.Context, log *logrus.Logger) error {
	log.Info("Starting alert exporter")
	defer log.Info("Alert exporter stopped")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		return fmt.Errorf("reading configuration: %w", err)
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-alert-exporter")
	defer func() {
		if err := tracerShutdown(context.Background()); err != nil {
			log.Errorf("failed to shut down tracer: %v", err)
		}
	}()

	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		return fmt.Errorf("initializing data store: %w", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	defer store.Close()

	processID := fmt.Sprintf("alert-exporter-%s-%s", util.GetHostname(), uuid.New().String())
	queuesProvider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return fmt.Errorf("initializing queue provider: %w", err)
	}
	defer func() {
		queuesProvider.Stop()
		queuesProvider.Wait()
	}()

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		return fmt.Errorf("initializing kv store: %w", err)
	}
	defer kvStore.Close()

	publisher, err := worker_client.QueuePublisher(ctx, queuesProvider)
	if err != nil {
		return fmt.Errorf("initializing task queue publisher: %w", err)
	}
	defer publisher.Close()
	workerClient := worker_client.NewWorkerClient(publisher, log)

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	go orgCache.Start()
	defer orgCache.Stop()

	buildResolverOpts := resolvers.BuildResolverOptions{
		Config: cfg,
		Store:  store.Organization(),
		Log:    log,
		Cache:  orgCache,
	}

	if cfg.Auth != nil && cfg.Auth.AAP != nil {
		membershipCache := cache.NewMembershipTTL(cache.DefaultTTL)
		go membershipCache.Start()
		defer membershipCache.Stop()
		buildResolverOpts.MembershipCache = membershipCache
	}

	orgResolver, err := resolvers.BuildResolver(buildResolverOpts)
	if err != nil {
		return fmt.Errorf("failed to build organization resolver: %w", err)
	}
	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(store, workerClient, kvStore, nil, log, "", "", []string{}, orgResolver))

	server := alert_exporter.New(cfg, log)
	log.Info("Starting alert exporter server, waiting for shutdown signal...")
	if err := server.Run(ctx, serviceHandler); err != nil {
		return fmt.Errorf("alert exporter server error: %w", err)
	}

	return nil
}
