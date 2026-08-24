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
	canaryservice "github.com/flightctl/flightctl/internal/service/canary"
	"github.com/flightctl/flightctl/internal/store"
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

	if err := canaryservice.InitEncryption(ctx, db, log); err != nil {
		log.Fatalf("initializing encryption canary store: %v", err)
	}

	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-delta-worker")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-delta-worker")

	processID := fmt.Sprintf("delta-worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("failed connecting to Redis queue: %v", err)
	}

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

	server := deltaworker.New(cfg, log, provider, db, workerCollector)
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
	cancel()
}
