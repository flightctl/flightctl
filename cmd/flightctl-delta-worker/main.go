package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	deltaworker "github.com/flightctl/flightctl/internal/delta_worker"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	encmetrics "github.com/flightctl/flightctl/internal/instrumentation/metrics/encryption"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/system"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	instpprof "github.com/flightctl/flightctl/internal/instrumentation/pprof"
	"github.com/flightctl/flightctl/internal/instrumentation/profiling"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	canaryservice "github.com/flightctl/flightctl/internal/service/canary"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	repostore "github.com/flightctl/flightctl/internal/store/repository"
	tvstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	ctx := context.Background()

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	log := log.InitLogs(cfg.Service.LogLevel)
	log.Println("Starting delta-worker service")
	defer log.Println("Delta-worker service stopped")
	log.Printf("Using config: %s", cfg)

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-delta-worker")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	profiling.Start(ctx, log, cfg, "flightctl-delta-worker", instpprof.DefaultPortDeltaWorker)

	if err := encryption.InitGlobalEncryption(log, cfg); err != nil {
		log.Fatalf("initializing encryption: %v", err)
	}

	var encCollector prometheus.Collector
	if cfg.Metrics != nil && cfg.Metrics.Enabled {
		if encMgr := encryption.GlobalManager(); encMgr != nil {
			ec := encmetrics.NewEncryptionCollector(encMgr)
			encMgr.SetMetricsRecorder(ec)
			encCollector = ec
		}
	}

	// Postgres startup matches flightctl-worker: encryption canary needs DB access now;
	// follow-on stack stories use the same connection for delta generation.
	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}
	defer func() {
		sqlDB, err := db.DB()
		if err != nil {
			log.Errorf("failed to get database handle for close: %v", err)
			return
		}
		if err := sqlDB.Close(); err != nil {
			log.Errorf("failed to close database: %v", err)
		}
	}()

	if err := canaryservice.InitEncryption(ctx, db, log); err != nil {
		log.Fatalf("initializing encryption canary store: %v", err)
	}

	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("connecting to KV store: %v", err)
	}
	defer kvStore.Close()

	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-delta-worker")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-delta-worker")

	processID := fmt.Sprintf("delta-worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("failed connecting to Redis queue: %v", err)
	}
	defer func() {
		provider.Stop()
		provider.Wait()
	}()

	var workerCollector *worker.WorkerCollector
	if cfg.Metrics != nil && cfg.Metrics.Enabled {
		var collectors []prometheus.Collector
		if cfg.Metrics.WorkerCollector != nil && cfg.Metrics.WorkerCollector.Enabled {
			workerCollector = worker.NewWorkerCollector(ctx, log, cfg, provider)
			collectors = append(collectors, workerCollector)
		}
		if cfg.Metrics.SystemCollector != nil && cfg.Metrics.SystemCollector.Enabled {
			if systemMetricsCollector := system.NewSystemCollector(ctx, cfg); systemMetricsCollector != nil {
				collectors = append(collectors, systemMetricsCollector)
			}
		}
		if encCollector != nil {
			collectors = append(collectors, encCollector)
		}
		if len(collectors) > 0 {
			go func() {
				if err := tracing.RunMetricsServer(ctx, log, cfg.Metrics.Address, collectors...); err != nil {
					log.Errorf("Error running metrics server: %s", err)
				}
				cancel()
			}()
		}
	}

	deltaStore := deltastore.NewStore(db, log)
	deviceStore := devicestore.NewDeviceStore(db, log)
	eventStore := eventstore.NewEventStore(db, log)
	fleetStore := fleetstore.NewFleetStore(db, log)
	repositoryStore := repostore.NewRepositoryStore(db, log)
	templateVersionStore := tvstore.NewTemplateVersionStore(db, log)
	deltaPublisher, err := worker_client.DeltaQueuePublisher(ctx, provider)
	if err != nil {
		log.Fatalf("creating delta publisher: %v", err)
	}
	taskPublisher, err := worker_client.QueuePublisher(ctx, provider)
	if err != nil {
		log.Fatalf("creating task publisher: %v", err)
	}
	workerClient := worker_client.NewWorkerClient(taskPublisher, log, worker_client.WithDeltaPublisher(deltaPublisher))
	eventsSvc := events.NewServiceHandler(eventStore, workerClient, log)
	fleetSvc := fleetservice.WrapWithTracing(fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, log))
	deviceSvc := deviceservice.WrapWithTracing(deviceservice.NewDeviceServiceHandler(deviceStore, nil, fleetStore, eventsSvc, kvStore, "", log))
	templateVersionSvc := templateversionservice.WrapWithTracing(templateversionservice.NewServiceHandler(templateVersionStore, kvStore, eventsSvc, log))
	repositorySvc := repositoryservice.WrapWithTracing(repositoryservice.NewServiceHandler(repositoryStore, eventsSvc, log))
	eventSvc := eventservice.WrapWithTracing(eventservice.NewServiceHandler(eventStore, eventsSvc))

	server := deltaworker.New(cfg, log, provider, deltaStore, fleetSvc, deviceSvc, templateVersionSvc, repositorySvc, eventSvc, eventsSvc, kvStore, workerCollector)
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
	cancel()
}
