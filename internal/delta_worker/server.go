package delta_worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltapreparegeneration"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
	"github.com/flightctl/flightctl/internal/delta_worker/tasks"
	generateTask "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	generationcomplete "github.com/flightctl/flightctl/internal/delta_worker/tasks/generationcomplete"
	preparetask "github.com/flightctl/flightctl/internal/delta_worker/tasks/prepare"
	preparecomplete "github.com/flightctl/flightctl/internal/delta_worker/tasks/preparecomplete"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/oci"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	repostore "github.com/flightctl/flightctl/internal/store/repository"
	tvstore "github.com/flightctl/flightctl/internal/store/templateversion"
	internaltasks "github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Server runs the delta-generation worker process.
type Server struct {
	cfg            *deltaconfig.DeltaGenerationConfig
	log            logrus.FieldLogger
	queuesProvider queues.Provider
	workerMetrics  *worker.WorkerCollector
	generationSvc  deltageneration.Service
	prepareSvc     deltaprepare.Service
	prepareGenSvc  deltapreparegeneration.Service
	progressSvc    *deltapreparegeneration.ProgressHandler
	prepareHandler *deltaprepare.ServiceHandler
	status         *workerservice.StorePreparingStatus
	repositorySvc  repositoryservice.Service
	eventsHandler  *events.ServiceHandler
	resolver       *preparetask.Resolver
}

func New(log logrus.FieldLogger, cfg *deltaconfig.DeltaGenerationConfig, db *gorm.DB, kvStore kvstore.KVStore, queuesProvider queues.Provider, workerMetrics *worker.WorkerCollector) (*Server, error) {
	deltaPrepareStore := deltapreparestore.NewStore(db, log.WithField("pkg", "delta-prepare-store"))
	deltaGenerationStore := deltagenerationstore.NewStore(db, log.WithField("pkg", "delta-generation-store"))
	deltaPrepareGenerationStore := deltapreparegenerationstore.NewStore(db, log.WithField("pkg", "delta-prepare-generation-store"))
	deviceStore := devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
	eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
	fleetStore := fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
	repositoryStore := repostore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
	catalogStore := catalogstore.NewCatalogStore(db, log.WithField("pkg", "catalog-store"))
	templateVersionStore := tvstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))

	eventSvc := events.NewServiceHandler(eventStore, nil, log)
	status := workerservice.NewStorePreparingStatus(fleetStore, deviceStore)
	deviceSvc := deviceservice.WrapWithTracing(deviceservice.NewDeviceServiceHandler(deviceStore, nil, fleetStore, eventSvc, kvStore, "", log))
	fleetSvc := fleetservice.WrapWithTracing(fleetservice.NewServiceHandler(fleetStore, nil, eventSvc, log))
	repositorySvc := repositoryservice.WrapWithTracing(repositoryservice.NewServiceHandler(repositoryStore, eventSvc, log))
	catalogSvc := catalogservice.WrapWithTracing(catalogservice.NewServiceHandler(catalogStore, deviceStore, fleetStore, eventSvc, log))
	templateVersionSvc := templateversionservice.WrapWithTracing(templateversionservice.NewServiceHandler(templateVersionStore, kvStore, eventSvc, log))
	prepareHandler, err := deltaprepare.NewCompletionService(deltaPrepareStore, status, eventSvc)
	if err != nil {
		return nil, fmt.Errorf("create delta prepare service: %w", err)
	}
	prepareSvc := deltaprepare.WrapWithTracing(prepareHandler)
	progressSvc := deltapreparegeneration.NewProgressHandler(deltaPrepareGenerationStore, prepareSvc, eventSvc)
	generationSvc := deltageneration.WrapWithTracing(deltageneration.NewServiceHandler(deltaGenerationStore, log))
	prepareGenerationSvc := deltapreparegeneration.WrapWithTracing(deltapreparegeneration.NewServiceHandler(deltaPrepareGenerationStore, generationSvc, eventSvc))

	return &Server{
		cfg:            cfg,
		log:            log,
		queuesProvider: queuesProvider,
		workerMetrics:  workerMetrics,
		generationSvc:  generationSvc,
		prepareSvc:     prepareSvc,
		prepareGenSvc:  prepareGenerationSvc,
		progressSvc:    progressSvc,
		prepareHandler: prepareHandler,
		status:         status,
		repositorySvc:  repositorySvc,
		resolver:       serviceResolver(cfg, fleetSvc, deviceSvc, templateVersionSvc, repositorySvc, catalogSvc, kvStore, log),
		eventsHandler:  eventSvc,
	}, nil
}

func (s *Server) Run(ctx context.Context) error {
	workerPublisher, err := worker_client.QueuePublisher(ctx, s.queuesProvider)
	if err != nil {
		return fmt.Errorf("worker publisher: %w", err)
	}
	defer workerPublisher.Close()
	deltaPublisher, err := worker_client.DeltaQueuePublisher(ctx, s.queuesProvider)
	if err != nil {
		return fmt.Errorf("delta publisher: %w", err)
	}
	defer deltaPublisher.Close()
	if s.eventsHandler != nil {
		s.eventsHandler.SetWorkerClient(worker_client.NewWorkerClient(
			workerPublisher,
			s.log,
			worker_client.WithDeltaPublisher(deltaPublisher),
		))
	}

	preparer, err := s.newPreparer(ctx, deltaPublisher)
	if err != nil {
		return err
	}
	generator, err := generateTask.NewHandler(
		s.cfg,
		s.log.WithField("pkg", "generate-task"),
		s.repositorySvc,
		s.generationSvc,
		s.progressSvc,
		func(ctx context.Context, orgID uuid.UUID, event *domain.Event) error {
			return enqueueDeltaWorkerEvent(ctx, deltaPublisher, orgID, event)
		},
	)
	if err != nil {
		return err
	}
	completion, err := generationcomplete.NewHandler(s.prepareHandler, s.status)
	if err != nil {
		return fmt.Errorf("create generation-complete handler: %w", err)
	}
	prepareCompletion, err := preparecomplete.NewHandler(s.prepareHandler)
	if err != nil {
		return fmt.Errorf("create prepare-complete handler: %w", err)
	}
	wiring := &tasks.ConsumerWiring{
		Preparer:        preparer,
		Generator:       generator,
		Completion:      completion,
		PrepareComplete: prepareCompletion,
	}
	if err := tasks.LaunchConsumers(ctx, s.queuesProvider, s.cfg, s.workerMetrics, s.log, wiring); err != nil {
		s.log.WithError(err).Error("failed to launch delta-generation consumers")
		return err
	}
	go func() {
		<-ctx.Done()
		s.queuesProvider.Stop()
	}()
	s.queuesProvider.Wait()
	return nil
}

func (s *Server) newPreparer(ctx context.Context, publisher queues.QueueProducer) (*preparetask.Handler, error) {
	deployWait := s.cfg.EffectiveMaxWaitForDelta()
	deployTimeout := s.cfg.EffectiveTimeout()
	preparer, err := preparetask.NewHandler(
		s.resolver,
		func(ctx context.Context, orgId uuid.UUID, event *domain.Event) error {
			return enqueueDeltaWorkerEvent(ctx, publisher, orgId, event)
		},
		s.prepareSvc,
		s.generationSvc,
		s.prepareGenSvc,
	)
	if err != nil {
		return nil, err
	}
	preparer.MaxWaitForDelta = deployWait
	preparer.DeltaGenerationTimeout = deployTimeout
	preparer.Now = time.Now
	return preparer, nil
}

// enqueueDeltaWorkerEvent enqueues an internal delta-worker event and returns
// queue errors so the source task can be retried when publishing fails.
func enqueueDeltaWorkerEvent(ctx context.Context, producer queues.QueueProducer, orgID uuid.UUID, event *domain.Event) error {
	if event == nil {
		return errors.New("event is nil")
	}
	if producer == nil {
		return errors.New("queue producer is nil")
	}

	payload, err := json.Marshal(worker_client.EventWithOrgId{
		OrgId: orgID,
		Event: *event,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal delta-worker event: %w", err)
	}

	var timestamp int64
	if event.Metadata.CreationTimestamp != nil {
		timestamp = event.Metadata.CreationTimestamp.UnixMicro()
	} else {
		timestamp = time.Now().UnixMicro()
	}
	if err := producer.Enqueue(ctx, payload, timestamp); err != nil {
		return fmt.Errorf("failed to enqueue delta-worker event: %w", err)
	}
	return nil
}

func serviceResolver(cfg *deltaconfig.DeltaGenerationConfig, fleets fleetservice.Service, devices deviceservice.Service, tvs templateversionservice.Service, repos repositoryservice.Service, catalogs catalogservice.Service, kvStore kvstore.KVStore, log logrus.FieldLogger) *preparetask.Resolver {
	return &preparetask.Resolver{
		FleetService:           fleets,
		DeviceService:          devices,
		RepositoryService:      repos,
		TemplateVersionService: tvs,
		Config:                 cfg,
		Inspect: func(ctx context.Context, orgId uuid.UUID, image string) (string, error) {
			return oci.CachedImageDigest(ctx, kvStore, orgId, image, func(ctx context.Context) (string, error) {
				spec, err := generateTask.ResolveDeltaTargetRepo(ctx, repos, cfg, orgId)
				if err != nil {
					return "", err
				}
				return oci.InspectImageDigest(ctx, image, spec)
			})
		},
		Render: func(ctx context.Context, orgId uuid.UUID, spec *domain.DeviceSpec) (internaltasks.RenderedSpec, error) {
			logic := internaltasks.NewDeviceRenderLogic(log, devices, repos, catalogs, nil, kvStore, nil, orgId, domain.Event{})
			return logic.RenderSpec(ctx, spec)
		},
	}
}
