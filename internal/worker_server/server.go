package workerserver

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/tasks_client"
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
}

// New returns a new instance of a flightctl server.
func New(
	cfg *config.Config,
	log logrus.FieldLogger,
	store store.Store,
	queuesProvider queues.Provider,
	k8sClient k8sclient.K8SClient,
) *Server {
	return &Server{
		cfg:            cfg,
		log:            log,
		store:          store,
		queuesProvider: queuesProvider,
		k8sClient:      k8sClient,
	}
}

func (s *Server) Run(ctx context.Context) error {
	s.log.Println("Initializing async jobs")
	publisher, err := tasks_client.TaskQueuePublisher(s.queuesProvider)
	if err != nil {
		s.log.WithError(err).Error("failed to create fleet queue publisher")
		return err
	}
	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		s.log.WithError(err).Error("failed to create kvStore")
		return err
	}
	callbackManager := tasks_client.NewCallbackManager(publisher, s.log)
	serviceHandler := service.WrapWithTracing(
		service.NewServiceHandler(s.store, callbackManager, kvStore, nil, s.log, "", "", []string{}))

	if err = tasks.LaunchConsumers(ctx, s.queuesProvider, serviceHandler, callbackManager, s.k8sClient, kvStore, 1, 1); err != nil {
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
