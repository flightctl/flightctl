package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/cmdsetup"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/system"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	workerserver "github.com/flightctl/flightctl/internal/worker_server"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/rest"
)

func main() {
	ctx, cfg, log, shutdown := cmdsetup.InitService(context.Background(), "worker")
	defer shutdown()

	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	log.Println("Initializing data store")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))
	defer store.Close()

	processID := fmt.Sprintf("worker-%s-%s", util.GetHostname(), uuid.New().String())
	provider, err := queues.NewRedisProvider(ctx, log, processID, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		log.Fatalf("failed connecting to Redis queue: %v", err)
	}

	k8sClient, err := k8sclient.NewK8SClient()
	if err != nil {
		if errors.Is(err, rest.ErrNotInCluster) {
			log.Info("Kubernetes environment not detected, kubernetes features are disabled")
		} else {
			log.WithError(err).Error("initializing k8s client, kubernetes features are disabled")
		}
	}

	// Initialize metrics collectors
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

		if len(collectors) > 0 {
			go func() {
				if err := tracing.RunMetricsServer(ctx, log, cfg.Metrics.Address, collectors...); err != nil {
					log.Errorf("Error running metrics server: %s", err)
					cancel()
				}
			}()
		}
	}

	server := workerserver.New(cfg, log, store, provider, k8sClient, workerCollector)
	if err := server.Run(ctx); err != nil {
		log.Fatalf("Error running server: %s", err)
	}
}
