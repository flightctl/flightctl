package workerserver

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

type Server struct {
	cfg            *config.Config
	log            logrus.FieldLogger
	store          store.Store
	queuesProvider queues.Provider
	k8sClient      k8sclient.K8SClient
	workerMetrics  *worker.WorkerCollector
}

// New returns a new instance of a flightctl server.
func New(
	cfg *config.Config,
	log logrus.FieldLogger,
	store store.Store,
	queuesProvider queues.Provider,
	k8sClient k8sclient.K8SClient,
	workerMetrics *worker.WorkerCollector,
) *Server {
	return &Server{
		cfg:            cfg,
		log:            log,
		store:          store,
		queuesProvider: queuesProvider,
		k8sClient:      k8sClient,
		workerMetrics:  workerMetrics,
	}
}

func (s *Server) Run(ctx context.Context) error {
	s.log.Println("Initializing async jobs")
	publisher, err := worker_client.QueuePublisher(ctx, s.queuesProvider)
	if err != nil {
		s.log.WithError(err).Error("failed to create worker queue publisher")
		return err
	}
	defer publisher.Close()

	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		s.log.WithError(err).Error("failed to create kvStore")
		return err
	}

	workerClient := worker_client.NewWorkerClient(publisher, s.log)
	if err = rendered.Bus.Initialize(ctx, kvStore, s.queuesProvider, time.Duration(s.cfg.Service.RenderedWaitTimeout), s.log); err != nil {
		s.log.WithError(err).Error("failed to create rendered version manager")
		return err
	}
	serviceHandler := service.WrapWithTracing(
		service.NewServiceHandler(s.store, workerClient, kvStore, nil, s.log, "", "", []string{}))

	if err = tasks.LaunchConsumers(ctx, s.queuesProvider, serviceHandler, s.k8sClient, kvStore, 1, 1, s.workerMetrics); err != nil {
		s.log.WithError(err).Error("failed to launch consumers")
		return err
	}
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigShutdown
		s.log.Println("Shutdown signal received")
		s.queuesProvider.Stop()
		kvStore.Close()
	}()
	s.queuesProvider.Wait()

	return nil
}
