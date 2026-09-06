package delta_worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/delta_worker/tasks"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/oci"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	internaltasks "github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// Server runs the delta-generation worker process.
type Server struct {
	cfg              *config.Config
	log              logrus.FieldLogger
	queuesProvider   queues.Provider
	store            deltastore.Store
	fleets           fleetservice.Service
	devices          deviceservice.Service
	templateVersions templateversionservice.Service
	repositories     repositoryservice.Service
	events           eventservice.Service
	eventCallbacks   events.Service
	kvStore          kvstore.KVStore
	workerMetrics    *worker.WorkerCollector
}

func New(cfg *config.Config, log logrus.FieldLogger, queuesProvider queues.Provider, deltaStore deltastore.Store, fleets fleetservice.Service, devices deviceservice.Service, templateVersions templateversionservice.Service, repositories repositoryservice.Service, events eventservice.Service, eventCallbacks events.Service, kvStore kvstore.KVStore, workerMetrics *worker.WorkerCollector) *Server {
	return &Server{
		cfg:              cfg,
		log:              log,
		queuesProvider:   queuesProvider,
		store:            deltaStore,
		fleets:           fleets,
		devices:          devices,
		templateVersions: templateVersions,
		repositories:     repositories,
		events:           events,
		eventCallbacks:   eventCallbacks,
		kvStore:          kvStore,
		workerMetrics:    workerMetrics,
	}
}

func (s *Server) Run(ctx context.Context) error {
	preparer, err := s.newPreparer(ctx)
	if err != nil {
		return err
	}
	wiring := &tasks.ConsumerWiring{
		Preparer: preparer,
		WriteTarget: func(ctx context.Context, orgID uuid.UUID) (*domain.OciRepoSpec, error) {
			return resolveWriteSpec(ctx, s.cfg, preparer, orgID)
		},
		Persist:    preparer.Persist,
		PairCounts: preparer.Status,
	}
	if err := tasks.LaunchConsumers(ctx, s.queuesProvider, s.cfg, s.store, s.workerMetrics, s.log, wiring); err != nil {
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
	deployWait := s.cfg.DeltaGeneration.EffectiveMaxWaitForDelta()
	deployTimeout := s.cfg.DeltaGeneration.EffectiveTimeout()
	return &Preparer{
		Resolver: serviceResolver(s.cfg, s.fleets, s.devices, s.templateVersions, s.repositories, s.kvStore),
		Store:    s.store,
		Emit: func(ctx context.Context, orgId uuid.UUID, event *domain.Event) error {
			return worker_client.EnqueueEvent(ctx, publisher, orgId, event)
		},
		Persist: s.events.CreateEvent,
		Now:     time.Now,
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
		Status:    NewServicePreparingStatus(s.fleets, s.devices),
		Events:    s.eventCallbacks,
		FleetSvc:  s.fleets,
		DeviceSvc: s.devices,
		TVSvc:     s.templateVersions,
	}, nil
}

func serviceResolver(cfg *config.Config, fleets fleetservice.Service, devices deviceservice.Service, tvs templateversionservice.Service, repos repositoryservice.Service, cache oci.DigestCache) *Resolver {
	return &Resolver{
		Fleet: func(ctx context.Context, orgId uuid.UUID, name string) (*domain.Fleet, error) {
			fleet, status := fleets.GetFleet(ctx, orgId, name, domain.GetFleetParams{})
			return fleet, statusError(status)
		},
		TemplateVersion: func(ctx context.Context, orgId uuid.UUID, fleet, name string) (*domain.TemplateVersion, error) {
			version, status := tvs.GetTemplateVersion(ctx, orgId, fleet, name)
			return version, statusError(status)
		},
		Devices: func(ctx context.Context, orgId uuid.UUID, owner string) ([]*domain.Device, error) {
			list, status := devices.ListDevices(ctx, orgId, domain.ListDevicesParams{FieldSelector: &owner}, nil)
			if err := statusError(status); err != nil {
				return nil, err
			}
			out := make([]*domain.Device, 0, len(list.Items))
			for i := range list.Items {
				out = append(out, &list.Items[i])
			}
			return out, nil
		},
		Device: func(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
			device, status := devices.GetDevice(ctx, orgId, name)
			return device, statusError(status)
		},
		WriteTarget: func(ctx context.Context, orgId uuid.UUID) (*domain.OciRepoSpec, error) {
			return loadWriteTarget(ctx, repos, cfg, orgId)
		},
		Inspect: func(ctx context.Context, orgId uuid.UUID, image string) (string, error) {
			return oci.CachedImageDigest(ctx, cache, image, func(ctx context.Context) (string, error) {
				spec, err := loadWriteTarget(ctx, repos, cfg, orgId)
				if err != nil {
					return "", err
				}
				named, err := reference.ParseNormalizedNamed(image)
				if err != nil {
					return "", err
				}
				existCfg, err := tasks.ExistenceConfigFromSpec(ctx, spec, named.Name())
				if err != nil {
					return "", err
				}
				return inspectImageDigest(ctx, image, existCfg)
			})
		},
		DesiredSpec: internaltasks.DesiredSpecFromTemplate,
		Render: func(_ context.Context, spec *domain.DeviceSpec) (internaltasks.RenderedSpec, error) {
			result := internaltasks.RenderedSpec{}
			if spec != nil && spec.Os != nil {
				result.OsImage = spec.Os.Image
			}
			if spec != nil && spec.Applications != nil {
				appsBytes, err := json.Marshal(*spec.Applications)
				if err != nil {
					return result, nil
				}
				result.Applications = appsBytes
			}
			return result, nil
		},
		Expand: expandWithInspect(cache, repos, cfg),
	}
}

func resolveWriteSpec(ctx context.Context, cfg *config.Config, preparer *Preparer, orgID uuid.UUID) (*domain.OciRepoSpec, error) {
	if preparer != nil && preparer.Resolver != nil && preparer.Resolver.WriteTarget != nil {
		return preparer.Resolver.WriteTarget(ctx, orgID)
	}
	return tasks.WriteSpecFromConfig(cfg), nil
}

func loadWriteTarget(ctx context.Context, repos repositoryservice.Service, cfg *config.Config, orgId uuid.UUID) (*domain.OciRepoSpec, error) {
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
	return oci.SelectWriteTarget(orgSpec, tasks.WriteSpecFromConfig(cfg)), nil
}

func statusError(status domain.Status) error {
	if status.Code == http.StatusOK {
		return nil
	}
	if status.Message == "" {
		return fmt.Errorf("service request failed with status %d", status.Code)
	}
	return errors.New(status.Message)
}

func inspectImageDigest(ctx context.Context, image string, cfg tasks.ExistenceConfig) (string, error) {
	dgst, err := oci.DigestFromImageRef(image)
	if err != nil {
		return "", err
	}
	if dgst != "" {
		return dgst, nil
	}
	image, err = oci.RewriteImageRef(image)
	if err != nil {
		return "", err
	}
	named, err := reference.ParseNormalizedNamed(strings.TrimPrefix(image, "docker://"))
	if err != nil {
		return "", err
	}
	tag := "latest"
	if tagged, ok := named.(reference.NamedTagged); ok {
		tag = tagged.Tag()
	}
	host, repo, err := tasks.SplitRegistryRepository(named.Name())
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

// expandWithInspect returns an Expand callback that uses registry inspect to
// resolve new image digests and pairs them with current digests from device
// application status.
func expandWithInspect(cache oci.DigestCache, repos repostore.Store, cfg *config.Config) func(context.Context, uuid.UUID, *domain.Device, tasks.RenderedSpec, []DeltaCandidate) []DeltaCandidate {
	return func(ctx context.Context, orgId uuid.UUID, device *domain.Device, rendered tasks.RenderedSpec, cands []DeltaCandidate) []DeltaCandidate {
		inspect := func(ctx context.Context, orgId uuid.UUID, image string) (string, error) {
			return oci.CachedImageDigest(ctx, cache, image, func(ctx context.Context) (string, error) {
				spec, err := loadWriteTarget(ctx, repos, cfg, orgId)
				if err != nil {
					return "", err
				}
				named, err := reference.ParseNormalizedNamed(image)
				if err != nil {
					return "", err
				}
				existCfg, err := existenceConfigFromSpec(ctx, spec, named.Name())
				if err != nil {
					return "", err
				}
				return inspectImageDigest(ctx, image, existCfg)
			})
		}
		return expandAppCandidates(ctx, orgId, device, rendered, cands, inspect)
	}
}
