package workerserver

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

type Server struct {
	cfg       *config.Config
	log       logrus.FieldLogger
	store     store.Store
	provider  queues.Provider
	k8sClient k8sclient.K8SClient
	ca	  crypto.CA
}

// New returns a new instance of a flightctl server.
func New(
	cfg *config.Config,
	log logrus.FieldLogger,
	store store.Store,
	provider queues.Provider,
	k8sClient k8sclient.K8SClient,
	ca crypto.CA,
) *Server {
	return &Server{
		cfg:       cfg,
		log:       log,
		store:     store,
		provider:  provider,
		k8sClient: k8sClient,
		ca:        ca,
	}
}

func (s *Server) Run() error {
	s.log.Println("Initializing async jobs")
	publisher, err := tasks.TaskQueuePublisher(s.provider)
	if err != nil {
		s.log.WithError(err).Error("failed to create fleet queue publisher")
		return err
	}
	configStorage, err := tasks.NewConfigStorage(s.cfg.KV.Hostname, s.cfg.KV.Port)
	if err != nil {
		s.log.WithError(err).Error("failed to create configstorage")
		return err
	}
	callbackManager := tasks.NewCallbackManager(publisher, s.log)
	if err = tasks.LaunchConsumers(context.Background(), s.provider, s.store, callbackManager, s.k8sClient, configStorage, s.ca, 1, 1); err != nil {
		s.log.WithError(err).Error("failed to launch consumers")
		return err
	}
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigShutdown
		s.log.Println("Shutdown signal received")
		s.provider.Stop()
		configStorage.Close()
	}()
	s.provider.Wait()

	return nil
}
