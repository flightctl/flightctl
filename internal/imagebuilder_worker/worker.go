package imagebuilder_worker

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/tasks"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

// Worker represents the ImageBuilder Worker server
type Worker struct {
	cfg            *config.Config
	log            logrus.FieldLogger
	store          imagebuilderstore.Store
	mainStore      store.Store
	kvStore        kvstore.KVStore
	queuesProvider queues.Provider
	ca             *crypto.CAClient
	serviceHandler *service.ServiceHandler
}

// New returns a new instance of an ImageBuilder Worker server.
func New(
	cfg *config.Config,
	log logrus.FieldLogger,
	store imagebuilderstore.Store,
	mainStore store.Store,
	kvStore kvstore.KVStore,
	queuesProvider queues.Provider,
	ca *crypto.CAClient,
) *Worker {
	// Create service handler for internal operations (enrollment credential generation)
	agentEndpoint := fmt.Sprintf("https://%s", cfg.Service.AgentEndpointAddress)
	uiUrl := cfg.Service.BaseUIUrl
	serviceHandler := service.NewServiceHandler(mainStore, nil, kvStore, ca, log.WithField("component", "service"), agentEndpoint, uiUrl, nil)

	return &Worker{
		cfg:            cfg,
		log:            log,
		store:          store,
		mainStore:      mainStore,
		kvStore:        kvStore,
		queuesProvider: queuesProvider,
		ca:             ca,
		serviceHandler: serviceHandler,
	}
}

// Run starts the ImageBuilder Worker service
func (w *Worker) Run(ctx context.Context) error {
	w.log.Println("Initializing ImageBuilder Worker")
	w.log.Printf("Starting with maxConcurrentBuilds: %d", w.cfg.ImageBuilderWorker.MaxConcurrentBuilds)

	// Launch queue consumers
	if err := tasks.LaunchConsumers(ctx, w.queuesProvider, w.store, w.mainStore, w.kvStore, w.serviceHandler, w.cfg, w.log); err != nil {
		w.log.WithError(err).Error("failed to launch consumers")
		return err
	}

	// Setup signal handling for graceful shutdown
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigShutdown
		w.log.Println("Shutdown signal received")
		w.queuesProvider.Stop()
		w.kvStore.Close()
	}()

	w.queuesProvider.Wait()

	return nil
}
