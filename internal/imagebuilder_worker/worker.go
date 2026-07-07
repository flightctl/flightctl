package imagebuilder_worker

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/tasks"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Worker represents the ImageBuilder Worker server
type Worker struct {
	cfg               *config.Config
	log               logrus.FieldLogger
	imageBuilderStore imagebuilderstore.Store
	organizationStore organizationstore.Store
	repositoryStore   repositorystore.Store
	catalogStore      catalogstore.Store
	eventStore        eventstore.Store
	kvStore           kvstore.KVStore
	queuesProvider    queues.Provider
	ca                *crypto.CAClient
	serviceHandler    *service.ServiceHandler
}

// New returns a new instance of an ImageBuilder Worker server.
func New(
	cfg *config.Config,
	log logrus.FieldLogger,
	imageBuilderStore imagebuilderstore.Store,
	db *gorm.DB,
	kvStore kvstore.KVStore,
	queuesProvider queues.Provider,
	ca *crypto.CAClient,
) *Worker {
	organizationStore := organizationstore.NewOrganizationStore(db)
	repositoryStore := repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
	catalogStore := catalogstore.NewCatalogStore(db, log.WithField("pkg", "catalog-store"))
	eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))

	// Create service handler for internal operations (enrollment credential generation).
	// GenerateEnrollmentCredential has no focused-package home yet (see EDM-4687), so
	// this still requires constructing the full monolith store/service handler.
	mainStore := store.NewStore(db, log.WithField("pkg", "store"))
	serviceHandler := service.NewServiceHandler(mainStore, nil, kvStore, ca, log.WithField("component", "service"), cfg.Service.BaseAgentEndpointUrl, cfg.Service.BaseUIUrl, nil, false)

	return &Worker{
		cfg:               cfg,
		log:               log,
		imageBuilderStore: imageBuilderStore,
		organizationStore: organizationStore,
		repositoryStore:   repositoryStore,
		catalogStore:      catalogStore,
		eventStore:        eventStore,
		kvStore:           kvStore,
		queuesProvider:    queuesProvider,
		ca:                ca,
		serviceHandler:    serviceHandler,
	}
}

// Run starts the ImageBuilder Worker service
func (w *Worker) Run(ctx context.Context) error {
	w.log.Println("Initializing ImageBuilder Worker")
	w.log.Printf("Starting with maxConcurrentBuilds: %d", w.cfg.ImageBuilderWorker.MaxConcurrentBuilds)

	// Create imagebuilder service
	queueProducer, err := w.queuesProvider.NewQueueProducer(ctx, consts.ImageBuildTaskQueue)
	if err != nil {
		w.log.WithError(err).Error("failed to create queue producer for service")
		return err
	}
	// nil worker_client: events are stored in DB for audit/logging but are not pushed to
	// TaskQueue - events are manually enqueued to ImageBuildTaskQueue instead.
	eventsSvc := events.NewServiceHandler(w.eventStore, nil, w.log)
	imageBuilderService := imagebuilderapi.NewService(ctx, w.cfg, w.imageBuilderStore, w.catalogStore, w.repositoryStore, eventsSvc, queueProducer, w.kvStore, w.log)

	// Launch queue consumers
	if err := tasks.LaunchConsumers(ctx, w.queuesProvider, w.imageBuilderStore, w.organizationStore, w.repositoryStore, w.catalogStore, w.kvStore, w.serviceHandler, imageBuilderService, w.cfg, w.log); err != nil {
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
