package workerserver

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

type Server struct {
	cfg      *config.Config
	log      logrus.FieldLogger
	store    store.Store
	provider queues.Provider
}

// New returns a new instance of a flightctl server.
func New(
	cfg *config.Config,
	log logrus.FieldLogger,
	store store.Store,
	provider queues.Provider,
) *Server {
	return &Server{
		cfg:      cfg,
		log:      log,
		store:    store,
		provider: provider,
	}
}

func (s *Server) Run() error {
	s.log.Println("Initializing async jobs")
	publisher, err := tasks.TaskQueuePublisher(s.provider)
	if err != nil {
		s.log.WithError(err).Error("failed to create fleet queue publisher")
		return err
	}
	callbackManager := tasks.NewCallbackManager(publisher, s.log)
	if err = tasks.LaunchConsumers(context.Background(), s.provider, s.store, callbackManager, 1, 1); err != nil {
		s.log.WithError(err).Error("failed to launch consumers")
		return err
	}
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigShutdown
		s.log.Println("Shutdown signal received")
		s.provider.Stop()
	}()
	s.provider.Wait()

	return nil
}
