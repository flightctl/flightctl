package periodic

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	periodicmetrics "github.com/flightctl/flightctl/internal/instrumentation/metrics/periodic"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/rendered"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	organizationservice "github.com/flightctl/flightctl/internal/service/organization"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	resourcesyncservice "github.com/flightctl/flightctl/internal/service/resourcesync"
	syncstateservice "github.com/flightctl/flightctl/internal/service/syncstate"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	checkpointstore "github.com/flightctl/flightctl/internal/store/checkpoint"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	resourcesyncstore "github.com/flightctl/flightctl/internal/store/resourcesync"
	syncstatestore "github.com/flightctl/flightctl/internal/store/syncstate"
	vulnerabilityfindingstore "github.com/flightctl/flightctl/internal/store/vulnerabilityfinding"
	"github.com/flightctl/flightctl/internal/tasks"
	trustifyv2 "github.com/flightctl/flightctl/internal/trustify/v2"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Server struct {
	cfg *config.Config
	log logrus.FieldLogger
	db  *gorm.DB
}

// New returns a new instance of a flightctl server.
func New(
	cfg *config.Config,
	log logrus.FieldLogger,
	db *gorm.DB,
) *Server {
	return &Server{
		cfg: cfg,
		log: log,
		db:  db,
	}
}

func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-periodic")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-periodic")
	defer cancel()

	processID := fmt.Sprintf("periodic-%s-%s", util.GetHostname(), uuid.New().String())
	queuesProvider, err := queues.NewRedisProvider(ctx, s.log, processID, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return err
	}
	defer func() {
		queuesProvider.Stop()
		queuesProvider.Wait()
	}()

	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}
	defer kvStore.Close()

	queuePublisher, err := worker_client.QueuePublisher(ctx, queuesProvider)
	if err != nil {
		return err
	}
	defer queuePublisher.Close() // Close publisher on server shutdown

	workerClient := worker_client.NewWorkerClient(queuePublisher, s.log)
	if err = rendered.Bus.Initialize(ctx, kvStore, queuesProvider, time.Duration(s.cfg.Service.RenderedWaitTimeout), s.log); err != nil {
		return err
	}

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	orgCache.Start()
	defer orgCache.Stop()

	repositoryStore := repositorystore.NewRepositoryStore(s.db, s.log.WithField("pkg", "repository-store"))
	fleetStore := fleetstore.NewFleetStore(s.db, s.log.WithField("pkg", "fleet-store"))
	resourceSyncStore := resourcesyncstore.NewResourceSyncStore(s.db, s.log.WithField("pkg", "resourcesync-store"))
	catalogStore := catalogstore.NewCatalogStore(s.db, s.log.WithField("pkg", "catalog-store"))
	deviceStore := devicestore.NewDeviceStore(s.db, s.log.WithField("pkg", "device-store"))
	eventStore := eventstore.NewEventStore(s.db, s.log.WithField("pkg", "event-store"))
	checkpointStore := checkpointstore.NewCheckpointStore(s.db, s.log.WithField("pkg", "checkpoint-store"))
	organizationStore := organizationstore.NewOrganizationStore(s.db)
	dependencyRefStore := dependencyrefstore.NewDependencyRefStore(s.db, s.log.WithField("pkg", "dependencyref-store"))
	syncStateStore := syncstatestore.NewSyncStateStore(s.db, s.log.WithField("pkg", "syncstate-store"))
	vulnerabilityFindingStore := vulnerabilityfindingstore.NewVulnerabilityFindingStore(s.db, s.log.WithField("pkg", "vulnerabilityfinding-store"))

	eventsSvc := events.NewServiceHandler(eventStore, workerClient, s.log)

	repositorySvc := repositoryservice.WrapWithTracing(repositoryservice.NewServiceHandler(repositoryStore, eventsSvc, s.log))
	fleetSvc := fleetservice.WrapWithTracing(fleetservice.NewServiceHandler(fleetStore, eventsSvc, s.log))
	resourceSyncSvc := resourcesyncservice.WrapWithTracing(resourcesyncservice.NewServiceHandler(resourceSyncStore, catalogStore, fleetStore, eventsSvc, s.log))
	catalogSvc := catalogservice.WrapWithTracing(catalogservice.NewServiceHandler(catalogStore, eventsSvc, s.log))
	deviceSvc := deviceservice.WrapWithTracing(deviceservice.NewDeviceServiceHandler(deviceStore, fleetStore, eventsSvc, kvStore, "", s.log))
	eventSvc := eventservice.WrapWithTracing(eventservice.NewServiceHandler(eventStore, eventsSvc))
	checkpointSvc := checkpointservice.WrapWithTracing(checkpointservice.NewServiceHandler(checkpointStore))
	organizationSvc := organizationservice.WrapWithTracing(organizationservice.NewServiceHandler(organizationStore))
	dependencyrefSvc := dependencyrefservice.WrapWithTracing(dependencyrefservice.NewServiceHandler(dependencyRefStore, s.log))
	syncstateSvc := syncstateservice.WrapWithTracing(syncstateservice.NewServiceHandler(syncStateStore))

	var secretInformerClientset kubernetes.Interface
	if s.cfg.Periodic != nil && s.cfg.Periodic.ClusterLevelSecretAccess {
		if restConfig, err := rest.InClusterConfig(); err != nil {
			s.log.WithError(err).Warn("Secret informer enabled but in-cluster config is unavailable")
		} else {
			clientset, err := kubernetes.NewForConfig(restConfig)
			if err != nil {
				s.log.WithError(err).Error("Failed to create K8s clientset for secret informer")
			} else {
				secretInformerClientset = clientset
			}
		}
	} else {
		s.log.Debug("Secret informer disabled by configuration")
	}

	var vulnClient trustifyv2.VulnerabilityClient
	if s.cfg.VulnerabilityReporting != nil && s.cfg.VulnerabilityReporting.Enabled {
		if s.cfg.VulnerabilityReporting.Trustify == nil {
			s.log.Warn("Vulnerability syncing is enabled but Trustify config is missing; vulnerability-sync executor will be skipped")
		} else {
			var err error
			vulnClient, err = trustifyv2.NewVulnerabilityClient(ctx, s.cfg.VulnerabilityReporting.Trustify)
			if err != nil {
				s.log.WithError(err).Error("Failed to initialize Trustify client, vulnerability sync will be disabled")
			}
		}
	} else {
		s.log.Debug("Vulnerability syncing is disabled")
	}

	depSyncMetrics := periodicmetrics.NewDependencySyncCollector()

	// Initialize the task executors.
	periodicTaskExecutors := InitializeTaskExecutors(s.log,
		repositorySvc, fleetSvc, resourceSyncSvc, catalogSvc, deviceSvc, eventSvc,
		checkpointSvc, organizationSvc, dependencyrefSvc, syncstateSvc,
		s.cfg, queuesProvider, workerClient, nil, vulnerabilityFindingStore, vulnClient, depSyncMetrics)

	// Create channel manager for task distribution
	channelManagerConfig := ChannelManagerConfig{
		Log: s.log,
	}
	if s.cfg.Periodic != nil {
		channelManagerConfig.ChannelBufferSize = s.cfg.Periodic.Consumers * 2
	}
	channelManager, err := NewChannelManager(channelManagerConfig)
	if err != nil {
		return err
	}
	defer channelManager.Close()

	// Periodic task consumer
	consumerConfig := PeriodicTaskConsumerConfig{
		ChannelManager: channelManager,
		Log:            s.log,
		Executors:      periodicTaskExecutors,
	}
	if s.cfg.Periodic != nil {
		consumerConfig.ConsumerCount = s.cfg.Periodic.Consumers
	}
	periodicTaskConsumer, err := NewPeriodicTaskConsumer(consumerConfig)
	if err != nil {
		return err
	}

	// Periodic task publisher
	publisherConfig := PeriodicTaskPublisherConfig{
		Log:            s.log,
		OrgService:     organizationSvc,
		TasksMetadata:  MergeTasksWithConfig(s.cfg),
		ChannelManager: channelManager,
		WorkerClient:   workerClient,
		TaskBackoff: &poll.Config{
			BaseDelay:    100 * time.Millisecond,
			Factor:       3,
			MaxDelay:     10 * time.Second,
			JitterFactor: 0.1,
		},
	}
	periodicTaskPublisher, err := NewPeriodicTaskPublisher(publisherConfig)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		periodicTaskConsumer.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		periodicTaskPublisher.Run(ctx)
	}()

	if s.cfg.Metrics != nil && s.cfg.Metrics.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tracing.RunMetricsServer(ctx, s.log, s.cfg.Metrics.Address, depSyncMetrics); err != nil {
				s.log.WithError(err).Error("Metrics server failed")
				cancel()
			}
		}()
	}

	if secretInformerClientset != nil {
		secretSync := tasks.NewDependencySyncSecret(s.log, dependencyrefSvc, eventSvc, syncstateSvc, s.cfg.Periodic.ReleaseNamespace, depSyncMetrics)
		wg.Add(1)
		go func() {
			defer wg.Done()
			secretSync.Run(ctx, secretInformerClientset)
		}()
		s.log.Info("Secret change detection informer started")
	}

	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	<-sigShutdown
	s.log.Println("Shutdown signal received")
	cancel()

	// Wait for consumer and publisher to finish
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.log.Info("Shutdown of publisher and consumer complete")
	case <-time.After(10 * time.Second):
		s.log.Error("Shutdown timeout exceeded, forcing exit")
		return fmt.Errorf("shutdown timeout exceeded")
	}

	return nil
}
