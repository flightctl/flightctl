package delta_worker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/oci"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/store"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	repostore "github.com/flightctl/flightctl/internal/store/repository"
	"github.com/flightctl/flightctl/internal/store/selector"
	tvstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Server struct {
	cfg            *config.Config
	log            logrus.FieldLogger
	queuesProvider queues.Provider
	db             *gorm.DB
	store          deltastore.Store
	workerMetrics  *worker.WorkerCollector
}

func New(cfg *config.Config, log logrus.FieldLogger, queuesProvider queues.Provider, db *gorm.DB, workerMetrics *worker.WorkerCollector) *Server {
	return &Server{
		cfg:            cfg,
		log:            log,
		queuesProvider: queuesProvider,
		db:             db,
		store:          deltastore.NewStore(db, log),
		workerMetrics:  workerMetrics,
	}
}

func (s *Server) Run(ctx context.Context) error {
	preparer, err := s.newPreparer(ctx)
	if err != nil {
		return err
	}
	if err := LaunchConsumers(ctx, s.queuesProvider, s.cfg, s.store, s.workerMetrics, s.log, preparer); err != nil {
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

func (s *Server) newPreparer(ctx context.Context) (*Preparer, error) {
	publisher, err := worker_client.DeltaQueuePublisher(ctx, s.queuesProvider)
	if err != nil {
		return nil, fmt.Errorf("delta publisher: %w", err)
	}
	taskPublisher, err := worker_client.QueuePublisher(ctx, s.queuesProvider)
	if err != nil {
		return nil, fmt.Errorf("task publisher: %w", err)
	}
	fleets := fleetstore.NewFleetStore(s.db, s.log)
	devices := devicestore.NewDeviceStore(s.db, s.log)
	tvs := tvstore.NewTemplateVersionStore(s.db, s.log)
	repos := repostore.NewRepositoryStore(s.db, s.log)
	eventsSvc := events.NewServiceHandler(eventstore.NewEventStore(s.db, s.log), worker_client.NewWorkerClient(taskPublisher, s.log, worker_client.WithDeltaPublisher(publisher)), s.log)
	deployWait := s.cfg.DeltaGeneration.EffectiveMaxWaitForDelta()
	deployTimeout := s.cfg.DeltaGeneration.EffectiveTimeout()
	return &Preparer{
		Resolver: storeResolver(s.cfg, fleets, devices, tvs, repos),
		Store:    s.store,
		Emit: func(ctx context.Context, orgId uuid.UUID, event *domain.Event) error {
			return worker_client.EnqueueEvent(ctx, publisher, orgId, event)
		},
		Now: time.Now,
		MaxWait: func(fleet *domain.Fleet) *time.Duration {
			d, err := maxWaitFromFleet(fleet, deployWait)
			if err != nil {
				return deployWait
			}
			return d
		},
		JobTimeout: func(fleet *domain.Fleet) time.Duration {
			d, err := jobTimeoutFromFleet(fleet, deployTimeout)
			if err != nil {
				return deployTimeout
			}
			return d
		},
		Status:    NewStorePreparingStatus(fleets, devices),
		Events:    eventsSvc,
		FleetSvc:  fleetservice.NewServiceHandler(fleets, nil, eventsSvc, s.log),
		DeviceSvc: deviceservice.NewDeviceServiceHandler(devices, nil, fleets, eventsSvc, nil, "", s.log),
	}, nil
}

func storeResolver(cfg *config.Config, fleets fleetstore.Store, devices devicestore.Store, tvs tvstore.Store, repos repostore.Store) *Resolver {
	return &Resolver{
		Fleet: func(ctx context.Context, orgId uuid.UUID, name string) (*domain.Fleet, error) {
			return fleets.Get(ctx, orgId, name)
		},
		TemplateVersion: func(ctx context.Context, orgId uuid.UUID, fleet, name string) (*domain.TemplateVersion, error) {
			return tvs.Get(ctx, orgId, fleet, name)
		},
		Devices: func(ctx context.Context, orgId uuid.UUID, owner string) ([]*domain.Device, error) {
			return listDevicesByOwner(ctx, devices, orgId, owner)
		},
		Device: func(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
			return devices.Get(ctx, orgId, name)
		},
		WriteTarget: func(ctx context.Context, orgId uuid.UUID) (*domain.OciRepoSpec, error) {
			return loadWriteTarget(ctx, repos, cfg, orgId)
		},
		Inspect: func(ctx context.Context, image string) (string, error) {
			return inspectImageDigest(ctx, image, inspectConfigFrom(cfg))
		},
		DesiredSpec: tasks.DesiredSpecFromTemplate,
		Render: func(_ context.Context, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			if spec == nil || spec.Os == nil {
				return tasks.RenderedSpec{}, nil
			}
			return tasks.RenderedSpec{OsImage: spec.Os.Image}, nil
		},
	}
}

func listDevicesByOwner(ctx context.Context, devices devicestore.Store, orgId uuid.UUID, owner string) ([]*domain.Device, error) {
	fs, err := selector.NewFieldSelectorFromMap(map[string]string{"metadata.owner": owner})
	if err != nil {
		return nil, err
	}
	list, err := devices.List(ctx, orgId, devicestore.DeviceListParams{ListParams: store.ListParams{FieldSelector: fs}})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Device, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

func loadWriteTarget(ctx context.Context, repos repostore.Store, cfg *config.Config, orgId uuid.UUID) (*domain.OciRepoSpec, error) {
	var orgSpec *domain.OciRepoSpec
	if repos != nil {
		repo, err := repos.GetDeltaStorageTarget(ctx, orgId)
		if err != nil {
			return nil, err
		}
		if repo != nil {
			spec, err := repo.Spec.AsOciRepoSpec()
			if err != nil {
				return nil, err
			}
			orgSpec = &spec
		}
	}
	return oci.SelectWriteTarget(orgSpec, writeSpecFromConfig(cfg)), nil
}

func inspectConfigFrom(cfg *config.Config) existenceConfig {
	out := existenceConfig{Client: &http.Client{Timeout: 30 * time.Second}, Scheme: "https"}
	if cfg == nil || cfg.DeltaGeneration == nil || cfg.DeltaGeneration.DefaultRepository == nil {
		return out
	}
	out.Username = cfg.DeltaGeneration.DefaultRepository.Username
	out.Password = string(cfg.DeltaGeneration.DefaultRepository.Password)
	return out
}

func inspectImageDigest(ctx context.Context, image string, cfg existenceConfig) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", err
	}
	if digested, ok := named.(reference.Digested); ok {
		return digested.Digest().String(), nil
	}
	tag := "latest"
	if tagged, ok := named.(reference.NamedTagged); ok {
		tag = tagged.Tag()
	}
	host, repo, err := splitRegistryRepository(named.Name())
	if err != nil {
		return "", err
	}
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, tag), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	if cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("inspect %s: status %d", image, resp.StatusCode)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("inspect %s: missing Docker-Content-Digest", image)
	}
	return digest, nil
}
