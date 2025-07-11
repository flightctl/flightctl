package main

import (
	"context"

	"github.com/flightctl/flightctl/internal/alert_exporter"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx := context.Background()

	log := log.InitLogs()
	log.Println("Starting alert exporter")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-alert-exporter")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-alert-exporter")

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	defer store.Close()

	queuesProvider, err := queues.NewRedisProvider(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("initializing queue provider: %v", err)
	}
	defer queuesProvider.Stop()

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("initializing kv store: %v", err)
	}

	publisher, err := tasks_client.TaskQueuePublisher(queuesProvider)
	if err != nil {
		log.Fatalf("initializing task queue publisher: %v", err)
	}
	callbackManager := tasks_client.NewCallbackManager(publisher, log)
	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(store, callbackManager, kvStore, nil, log, "", ""))

	server := alert_exporter.New(cfg, log)
	if err := server.Run(ctx, serviceHandler); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
