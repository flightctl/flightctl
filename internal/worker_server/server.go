package workerserver

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/rendered"
	canaryservice "github.com/flightctl/flightctl/internal/service/canary"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	canarystore "github.com/flightctl/flightctl/internal/store/canary"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	checkpointstore "github.com/flightctl/flightctl/internal/store/checkpoint"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	enrollmenthooknotifysecrets "github.com/flightctl/flightctl/internal/store/enrollmenthooknotifysecrets"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
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

	deltaPublisher, err := worker_client.DeltaQueuePublisher(ctx, s.queuesProvider)
	if err != nil {
		s.log.WithError(err).Error("failed to create delta queue publisher")
		return err
	}
	defer deltaPublisher.Close()

	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		s.log.WithError(err).Error("failed to create kvStore")
		return err
	}

	workerClient := worker_client.NewWorkerClient(publisher, s.log, worker_client.WithDeltaPublisher(deltaPublisher))
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
	checkpointStore := checkpointstore.NewCheckpointStore(s.db, s.log.WithField("pkg", "checkpoint-store"))
	canaryStore := canarystore.NewCanaryStore(s.db, s.log.WithField("pkg", "canary-store"))
	canarySvc := canaryservice.WrapWithTracing(canaryservice.NewServiceHandler(canaryStore))
	catStore := catalogstore.NewCatalogStore(s.db, s.log.WithField("pkg", "catalog-store"))
	deltaStore := deltastore.NewStore(s.db, s.log.WithField("pkg", "delta-store"))

	enrollmentRequestStore := enrollmentrequeststore.NewEnrollmentRequestStore(s.db, s.log.WithField("pkg", "enrollmentrequest-store"))
	notifySecretsStore := enrollmenthooknotifysecrets.NewStore(s.db, s.log.WithField("pkg", "enrollmenthooknotifysecret-store"))

	eventsSvc := events.NewServiceHandler(eventStore, workerClient, s.log)
	labelSyncMappingStore := labelsyncmappingstore.NewStore(s.db, s.log.WithField("pkg", "labelsyncmapping-store"))
	labelSyncMappingEvaluator, err := labelsyncmappingservice.NewEvaluator()
	if err != nil {
		s.log.WithError(err).Error("failed to create device label mapping evaluator")
		return err
	}
	labelSyncMappingHandler, err := labelsyncmappingservice.NewServiceHandler(labelSyncMappingStore, deviceStore, labelSyncMappingEvaluator, eventsSvc, s.log)
	if err != nil {
		s.log.WithError(err).Error("failed to create device label mapping service")
		return err
	}
	labelSyncMappingSvc := labelsyncmappingservice.WrapWithTracing(labelSyncMappingHandler)

	fleetSvc := fleetservice.WrapWithTracing(fleetservice.NewServiceHandler(fleetStore, catStore, eventsSvc, s.log))
	templateVersionSvc := templateversionservice.WrapWithTracing(templateversionservice.NewServiceHandler(templateVersionStore, kvStore, eventsSvc, s.log))
	deviceSvc := deviceservice.WrapWithTracing(deviceservice.NewDeviceServiceHandler(deviceStore, catStore, fleetStore, eventsSvc, kvStore, "", s.log))
	dependencyrefSvc := dependencyrefservice.WrapWithTracing(dependencyrefservice.NewServiceHandler(dependencyRefStore, s.log))
	repositorySvc := repositoryservice.WrapWithTracing(repositoryservice.NewServiceHandler(repositoryStore, eventsSvc, s.log))
	catalogSvc := catalogservice.WrapWithTracing(catalogservice.NewServiceHandler(catStore, deviceStore, fleetStore, eventsSvc, s.log))
	eventSvc := eventservice.WrapWithTracing(eventservice.NewServiceHandler(eventStore, eventsSvc))
	enrollmentRequestSvc := enrollmentrequestservice.WrapWithTracing(enrollmentrequestservice.NewServiceHandler(
		enrollmentRequestStore, deviceStore, nil, nil, kvStore, eventsSvc, s.log, nil, "", "", nil, notifySecretsStore,
	))

	encryptionMigrator := tasks.NewEncryptionMigrator(
		ctx,
		s.db,
		encryption.GlobalManager(),
		checkpointStore,
		tasks.NewPostgresEncryptionMigrationLocker(s.db),
		canarySvc,
		eventSvc,
		s.log.WithField("pkg", "encryption-migration"),
	)

	deviceRenderer, err := tasks.NewDeviceRenderLogicWithDeltaStore(
		s.log.WithField("pkg", "device-render"),
		deviceSvc,
		repositorySvc,
		catalogSvc,
		s.k8sClient,
		kvStore,
		deltaStore,
		s.cfg,
	)
	if err != nil {
		s.log.WithError(err).Error("failed to create device render logic")
		return err
	}

	if err = tasks.LaunchConsumers(ctx, s.queuesProvider, tasks.TaskConsumer{
		FleetSvc:             fleetSvc,
		TemplateversionSvc:   templateVersionSvc,
		DeviceSvc:            deviceSvc,
		DependencyrefSvc:     dependencyrefSvc,
		RepositorySvc:        repositorySvc,
		CatalogSvc:           catalogSvc,
		EventSvc:             eventSvc,
		EnrollmentRequestSvc: enrollmentRequestSvc,
		NotifySecretsStore:   notifySecretsStore,
		K8sClient:            s.k8sClient,
		KVStore:              kvStore,
		Cfg:                  s.cfg,
		WorkerMetrics:        s.workerMetrics,
		EncryptionMigrator:   encryptionMigrator,
		QueuePublisher:       publisher,
		WorkerClient:         workerClient,
		DeviceRenderer:       deviceRenderer,
		LabelSyncMappingSvc:  labelSyncMappingSvc,
	}, 1, 1); err != nil {
		s.log.WithError(err).Error("failed to launch consumers")
		return err
	}
	enqueueCtx, cancelEnqueue := context.WithCancel(ctx)
	var enqueueWG sync.WaitGroup
	enqueueWG.Add(1)
	go func() {
		defer enqueueWG.Done()
		if err := tasks.EnqueueEncryptionMigrationIfNeeded(enqueueCtx, publisher, encryptionMigrator, s.log); err != nil {
			// Migration is best-effort at startup; do not block fleet/device workers.
			s.log.WithError(err).Error("failed to enqueue encryption migration on worker start; continuing")
		}
	}()
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigShutdown
		s.log.Println("Shutdown signal received")
		cancelEnqueue()
		enqueueWG.Wait()
		s.queuesProvider.Stop()
		kvStore.Close()
	}()
	s.queuesProvider.Wait()
	cancelEnqueue()
	enqueueWG.Wait()

	return nil
}
