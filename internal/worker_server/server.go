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
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/rendered"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Server struct {
	cfg            *config.Config
	log            logrus.FieldLogger
	db             *gorm.DB
	queuesProvider queues.Provider
	k8sClient      k8sclient.K8SClient
	workerMetrics  *worker.WorkerCollector
}

// New returns a new instance of a flightctl server.
func New(
	cfg *config.Config,
	log logrus.FieldLogger,
	db *gorm.DB,
	queuesProvider queues.Provider,
	k8sClient k8sclient.K8SClient,
	workerMetrics *worker.WorkerCollector,
) *Server {
	return &Server{
		cfg:            cfg,
		log:            log,
		db:             db,
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

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	orgCache.Start()
	defer orgCache.Stop()

	deviceStore := devicestore.NewDeviceStore(s.db, s.log.WithField("pkg", "device-store"))
	fleetStore := fleetstore.NewFleetStore(s.db, s.log.WithField("pkg", "fleet-store"))
	templateVersionStore := templateversionstore.NewTemplateVersionStore(s.db, s.log.WithField("pkg", "templateversion-store"))
	dependencyRefStore := dependencyrefstore.NewDependencyRefStore(s.db, s.log.WithField("pkg", "dependencyref-store"))
	repositoryStore := repositorystore.NewRepositoryStore(s.db, s.log.WithField("pkg", "repository-store"))
	eventStore := eventstore.NewEventStore(s.db, s.log.WithField("pkg", "event-store"))
	catStore := catalogstore.NewCatalogStore(s.db, s.log.WithField("pkg", "catalog-store"))

	eventsSvc := events.NewServiceHandler(eventStore, workerClient, s.log)

	fleetSvc := fleetservice.WrapWithTracing(fleetservice.NewServiceHandler(fleetStore, eventsSvc, s.log))
	templateVersionSvc := templateversionservice.WrapWithTracing(templateversionservice.NewServiceHandler(templateVersionStore, kvStore, eventsSvc, s.log))
	deviceSvc := deviceservice.WrapWithTracing(deviceservice.NewDeviceServiceHandler(deviceStore, fleetStore, eventsSvc, kvStore, "", s.log))
	dependencyrefSvc := dependencyrefservice.WrapWithTracing(dependencyrefservice.NewServiceHandler(dependencyRefStore, s.log))
	repositorySvc := repositoryservice.WrapWithTracing(repositoryservice.NewServiceHandler(repositoryStore, eventsSvc, s.log))
	catalogSvc := catalogservice.WrapWithTracing(catalogservice.NewServiceHandler(catStore, eventsSvc, s.log))
	eventSvc := eventservice.WrapWithTracing(eventservice.NewServiceHandler(eventStore, eventsSvc))

	if err = tasks.LaunchConsumers(ctx, s.queuesProvider, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, repositorySvc, catalogSvc, eventSvc, s.k8sClient, kvStore, s.cfg, 1, 1, s.workerMetrics); err != nil {
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
