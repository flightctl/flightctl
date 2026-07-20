package main

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/alert_exporter"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org/cache"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	organizationservice "github.com/flightctl/flightctl/internal/service/organization"
	"github.com/flightctl/flightctl/internal/store"
	checkpointstore "github.com/flightctl/flightctl/internal/store/checkpoint"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
)

func main() {
	ctx := context.Background()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	log := log.InitLogs(cfg.Service.LogLevel)
	log.Println("Starting alert exporter")
	log.Printf("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-alert-exporter")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-alert-exporter")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-alert-exporter")

	if err := encryption.InitGlobalEncryption(log, cfg); err != nil {
		log.Fatalf("initializing encryption: %v", err)
	}

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	processID := fmt.Sprintf("alert-exporter-%s-%s", util.GetHostname(), uuid.New().String())
	queuesProvider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("initializing queue provider: %v", err)
	}
	defer func() {
		queuesProvider.Stop()
		queuesProvider.Wait()
	}()

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("initializing kv store: %v", err)
	}
	defer kvStore.Close()

	publisher, err := worker_client.QueuePublisher(ctx, queuesProvider)
	if err != nil {
		log.Fatalf("initializing task queue publisher: %v", err)
	}
	defer publisher.Close()
	workerClient := worker_client.NewWorkerClient(publisher, log)

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	orgCache.Start()
	defer orgCache.Stop()

	checkpointStore := checkpointstore.NewCheckpointStore(db, log.WithField("pkg", "checkpoint-store"))
	organizationStore := organizationstore.NewOrganizationStore(db)
	eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))

	eventsSvc := events.NewServiceHandler(eventStore, workerClient, log)

	checkpointSvc := checkpointservice.WrapWithTracing(checkpointservice.NewServiceHandler(checkpointStore))
	organizationSvc := organizationservice.WrapWithTracing(organizationservice.NewServiceHandler(organizationStore))
	eventSvc := eventservice.WrapWithTracing(eventservice.NewServiceHandler(eventStore, eventsSvc))

	server := alert_exporter.New(cfg, log)
	if err := server.Run(ctx, checkpointSvc, organizationSvc, eventSvc); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
