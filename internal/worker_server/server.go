package workerserver

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/rendered"
	canaryservice "github.com/flightctl/flightctl/internal/service/canary"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	certificatesigningrequestservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmenthookpolicyservice "github.com/flightctl/flightctl/internal/service/enrollmenthookpolicy"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	updatercatalogservice "github.com/flightctl/flightctl/internal/service/updatercatalog"
	canarystore "github.com/flightctl/flightctl/internal/store/canary"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	checkpointstore "github.com/flightctl/flightctl/internal/store/checkpoint"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	enrollmenthooknotifysecrets "github.com/flightctl/flightctl/internal/store/enrollmenthooknotifysecrets"
	enrollmenthookpolicystore "github.com/flightctl/flightctl/internal/store/enrollmenthookpolicy"
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
	ca             *crypto.CAClient
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
) (*Server, error) {
	if cfg == nil || cfg.Service == nil {
		return nil, fmt.Errorf("worker service configuration is required")
	}
	client, err := enrollmentSigner(cfg.CA)
	if err != nil {
		log.WithError(err).Warn("automatic enrollment disabled; correct signing configuration and restart worker")
	}
	return &Server{
		cfg:            cfg,
		ca:             client,
		log:            log,
		db:             db,
		queuesProvider: queuesProvider,
		k8sClient:      k8sClient,
		workerMetrics:  workerMetrics,
	}, nil
}

func enrollmentSigner(cfg *ca.Config) (*crypto.CAClient, error) {
	if cfg == nil || cfg.InternalConfig == nil || cfg.DeviceEnrollmentSignerName == "" {
		return nil, fmt.Errorf("enrollment signing configuration is incomplete")
	}
	if cfg.CAType != ca.InternalCA {
		return nil, fmt.Errorf("enrollment signing backend is unsupported")
	}
	if cfg.InternalConfig.SerialFile != "" {
		return nil, fmt.Errorf("ca.internalConfig.serialFile is unsupported for worker enrollment; use random serial allocation")
	}
	backend, err := crypto.LoadInternalCA(cfg)
	if err != nil {
		return nil, fmt.Errorf("enrollment signing certificate or key is missing, unreadable or invalid")
	}
	certificates := backend.GetCABundleX509()
	if len(certificates) == 0 {
		return nil, fmt.Errorf("enrollment signing certificate is unavailable")
	}
	if err := crypto.ValidateSigningCA(certificates[0]); err != nil {
		return nil, err
	}
	client := crypto.NewCAClient(cfg, backend)
	if cfg.InternalConfig.CABundleFile != "" {
		bundle, err := client.GetCABundle()
		if err != nil || len(bytes.TrimSpace(bundle)) == 0 {
			return nil, fmt.Errorf("enrollment CA bundle is missing or unreadable")
		}
		for len(bytes.TrimSpace(bundle)) > 0 {
			bundle = bytes.TrimSpace(bundle)
			if !bytes.HasPrefix(bundle, []byte("-----BEGIN CERTIFICATE-----")) {
				return nil, fmt.Errorf("enrollment CA bundle is invalid")
			}
			block, rest := pem.Decode(bundle)
			if block == nil || block.Type != "CERTIFICATE" {
				return nil, fmt.Errorf("enrollment CA bundle is invalid")
			}
			if _, err := x509.ParseCertificate(block.Bytes); err != nil {
				return nil, fmt.Errorf("enrollment CA bundle is invalid")
			}
			bundle = rest
		}
	}
	return client, nil
}

func (s *Server) newEnrollmentService(deviceStore devicestore.Store, csrSvc certificatesigningrequestservice.Service, kvStore kvstore.KVStore, eventsSvc events.Service, notifySecretsStore enrollmenthooknotifysecrets.Store) *enrollmentrequestservice.ServiceHandler {
	enrollmentStore := enrollmentrequeststore.NewEnrollmentRequestStore(s.db, s.log.WithField("pkg", "enrollmentrequest-store"))
	hookStore := enrollmenthookpolicystore.NewStore(s.db, s.log.WithField("pkg", "enrollmenthookpolicy-store"))
	hookService := enrollmenthookpolicyservice.WrapWithTracing(enrollmenthookpolicyservice.NewServiceHandler(hookStore, eventsSvc, s.log))
	return enrollmentrequestservice.NewServiceHandler(enrollmentStore, deviceStore, csrSvc, s.ca, kvStore, eventsSvc, s.log,
		s.cfg.Service.TPMCAPaths, s.cfg.Service.BaseAgentEndpointUrl, s.cfg.Service.BaseUIUrl, hookService, notifySecretsStore)
}

func (s *Server) Run(ctx context.Context) error {
	s.log.Println("Initializing async jobs")
	publisher, err := worker_client.QueuePublisher(ctx, s.queuesProvider)
	if err != nil {
		s.log.WithError(err).Error("failed to create worker queue publisher")
		return err
	}
	defer publisher.Close()
	labelSyncPublisher, err := worker_client.LabelSyncQueuePublisher(ctx, s.queuesProvider)
	if err != nil {
		return err
	}
	defer labelSyncPublisher.Close()

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
	defer kvStore.Close()

	workerClient := worker_client.NewWorkerClient(publisher, s.log, worker_client.WithDeltaPublisher(deltaPublisher), worker_client.WithLabelSyncPublisher(labelSyncPublisher))
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

	notifySecretsStore := enrollmenthooknotifysecrets.NewStore(s.db, s.log.WithField("pkg", "enrollmenthooknotifysecret-store"))

	updaterProvider, err := updatercatalogservice.Bootstrap(ctx, s.db, s.cfg.FirmwareUpdaterCatalogFile)
	if err != nil {
		return fmt.Errorf("initialize approved updater catalog: %w", err)
	}

	eventsSvc := events.NewServiceHandler(eventStore, workerClient, s.log)
	signerName := ""
	if s.cfg.CA != nil {
		signerName = s.cfg.CA.DeviceEnrollmentSignerName
	}
	csrStore := certificatesigningrequeststore.NewCertificateSigningRequestStore(s.db, s.log.WithField("pkg", "csr-store"), signerName)
	csrSvc := certificatesigningrequestservice.WrapWithTracing(certificatesigningrequestservice.NewServiceHandler(csrStore, nil, nil, eventsSvc, s.log, "", ""))
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

	fleetSvc := fleetservice.WrapWithTracing(fleetservice.NewServiceHandler(fleetStore, catStore, eventsSvc, s.log, fleetservice.WithApprovedDescriptorProvider(updaterProvider)))
	templateVersionSvc := templateversionservice.WrapWithTracing(templateversionservice.NewServiceHandler(templateVersionStore, kvStore, eventsSvc, s.log))
	deviceSvc := deviceservice.WrapWithTracing(deviceservice.NewDeviceServiceHandler(deviceStore, catStore, fleetStore, eventsSvc, kvStore, "", s.log, deviceservice.WithApprovedDescriptorProvider(updaterProvider)))
	dependencyrefSvc := dependencyrefservice.WrapWithTracing(dependencyrefservice.NewServiceHandler(dependencyRefStore, s.log))
	repositorySvc := repositoryservice.WrapWithTracing(repositoryservice.NewServiceHandler(repositoryStore, eventsSvc, s.log))
	catalogSvc := catalogservice.WrapWithTracing(catalogservice.NewServiceHandler(catStore, deviceStore, fleetStore, eventsSvc, s.log))
	eventSvc := eventservice.WrapWithTracing(eventservice.NewServiceHandler(eventStore, eventsSvc))
	enrollmentRequestHandler := s.newEnrollmentService(deviceStore, csrSvc, kvStore, eventsSvc, notifySecretsStore)
	enrollmentRequestSvc := enrollmentrequestservice.WrapWithTracing(enrollmentRequestHandler)

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
		CSRService:           csrSvc,
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
	policyNotifier, err := rendered.NewFirmwarePolicyNotifier(s.queuesProvider)
	if err != nil {
		return err
	}
	policyRefresh, err := tasks.NewUpdaterCatalogRefresh(updaterProvider, policyNotifier, s.log)
	if err != nil {
		return err
	}
	enqueueCtx, cancelEnqueue := context.WithCancel(ctx)
	var enqueueWG sync.WaitGroup
	enqueueWG.Add(2)
	go func() { defer enqueueWG.Done(); policyRefresh.Run(enqueueCtx) }()

	go func() {
		defer enqueueWG.Done()
		if err := tasks.EnqueueEncryptionMigrationIfNeeded(enqueueCtx, publisher, encryptionMigrator, s.log); err != nil {
			// Migration is best-effort at startup; do not block fleet/device workers.
			s.log.WithError(err).Error("failed to enqueue encryption migration on worker start; continuing")
		}
	}()
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(sigShutdown)
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-sigShutdown:
		case <-ctx.Done():
		case <-stopped:
			return
		}
		s.log.Println("Shutdown signal received")
		cancelEnqueue()
		enqueueWG.Wait()
		s.queuesProvider.Stop()
	}()
	s.queuesProvider.Wait()
	cancelEnqueue()
	enqueueWG.Wait()

	return nil
}
