package prepare

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fleetServiceMock struct {
	fleetservice.Service
	get func(context.Context, uuid.UUID, string) (*domain.Fleet, error)
}

func (m *fleetServiceMock) GetFleet(ctx context.Context, orgID uuid.UUID, name string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
	fleet, err := m.get(ctx, orgID, name)
	return fleet, resolverServiceStatus(err)
}

type deviceServiceMock struct {
	deviceservice.Service
	get   func(context.Context, uuid.UUID, string) (*domain.Device, error)
	list  func(context.Context, uuid.UUID, string) ([]*domain.Device, error)
	pages func(context.Context, uuid.UUID, domain.ListDevicesParams) (*domain.DeviceList, error)
}

func (m *deviceServiceMock) GetDevice(ctx context.Context, orgID uuid.UUID, name string) (*domain.Device, domain.Status) {
	device, err := m.get(ctx, orgID, name)
	return device, resolverServiceStatus(err)
}

func (m *deviceServiceMock) ListDevices(ctx context.Context, orgID uuid.UUID, params domain.ListDevicesParams, _ *selector.AnnotationSelector) (*domain.DeviceList, domain.Status) {
	if m.pages != nil {
		devices, err := m.pages(ctx, orgID, params)
		return devices, resolverServiceStatus(err)
	}
	owner := ""
	if params.FieldSelector != nil {
		owner, _, _ = strings.Cut(*params.FieldSelector, ",")
	}
	devices, err := m.list(ctx, orgID, owner)
	if err != nil {
		return nil, resolverServiceStatus(err)
	}
	items := make([]domain.Device, 0, len(devices))
	for _, device := range devices {
		if device != nil {
			items = append(items, *device)
		}
	}
	return &domain.DeviceList{Items: items}, domain.StatusOK()
}

type templateVersionServiceMock struct {
	templateversionservice.Service
	get func(context.Context, uuid.UUID, string, string) (*domain.TemplateVersion, error)
}

func (m *templateVersionServiceMock) GetTemplateVersion(ctx context.Context, orgID uuid.UUID, fleet, name string) (*domain.TemplateVersion, domain.Status) {
	tv, err := m.get(ctx, orgID, fleet, name)
	if tv != nil && tv.Spec.Fleet == "" {
		tv.Spec.Fleet = fleet
	}
	return tv, resolverServiceStatus(err)
}

func resolverServiceStatus(err error) domain.Status {
	if err != nil {
		return domain.StatusInternalServerError(err.Error())
	}
	return domain.StatusOK()
}

func mockFleetService(get func(context.Context, uuid.UUID, string) (*domain.Fleet, error)) fleetservice.Service {
	return &fleetServiceMock{get: get}
}

func mockDeviceService(get func(context.Context, uuid.UUID, string) (*domain.Device, error), list func(context.Context, uuid.UUID, string) ([]*domain.Device, error)) deviceservice.Service {
	return &deviceServiceMock{get: get, list: list}
}

func mockTemplateVersionService(get func(context.Context, uuid.UUID, string, string) (*domain.TemplateVersion, error)) templateversionservice.Service {
	return &templateVersionServiceMock{get: get}
}

type repositoryServiceMock struct {
	repositoryservice.Service
	list func(context.Context, uuid.UUID, domain.ListRepositoriesParams) (*domain.RepositoryList, error)
}

func (m *repositoryServiceMock) ListRepositories(ctx context.Context, orgID uuid.UUID, params domain.ListRepositoriesParams) (*domain.RepositoryList, domain.Status) {
	repositories, err := m.list(ctx, orgID, params)
	return repositories, resolverServiceStatus(err)
}

func mockRepositoryService(list func(context.Context, uuid.UUID, domain.ListRepositoriesParams) (*domain.RepositoryList, error)) repositoryservice.Service {
	return &repositoryServiceMock{list: list}
}

func testRepositoryService() repositoryservice.Service {
	return mockRepositoryService(func(_ context.Context, _ uuid.UUID, _ domain.ListRepositoriesParams) (*domain.RepositoryList, error) {
		spec := domain.RepositorySpec{}
		if err := spec.FromOciRepoSpec(domain.OciRepoSpec{
			Registry:           "quay.io",
			Type:               domain.OciRepoSpecTypeOci,
			Repository:         lo.ToPtr("deltas"),
			DeltaStorageTarget: lo.ToPtr(true),
		}); err != nil {
			return nil, err
		}
		return &domain.RepositoryList{Items: []domain.Repository{{Spec: spec}}}, nil
	})
}

func TestDeltaCandidates_SkipPaths(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()

	t.Run("When the fleet template version differs from the event it should fail", func(t *testing.T) {
		r := Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return fleetWithTV("fleet-1", "tv-current"), nil
			}),
			TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{Spec: domain.TemplateVersionSpec{Fleet: "fleet-1"}}, nil
			}),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
		}
		_, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-event"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "template version changed")
	})

	t.Run("When generateDelta is false it should skip", func(t *testing.T) {
		r := Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				f := fleetWithTV("fleet-1", "tv-1")
				f.Spec.RolloutPolicy = &domain.RolloutPolicy{DeltaGeneration: &domain.RolloutPolicyDeltaGeneration{GenerateDelta: lo.ToPtr(false)}}
				return f, nil
			}),
			TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{}, nil
			}),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When there is no write target it should skip", func(t *testing.T) {
		r := Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return fleetWithTV("fleet-1", "tv-1"), nil
			}),
			RepositoryService: mockRepositoryService(func(_ context.Context, _ uuid.UUID, _ domain.ListRepositoriesParams) (*domain.RepositoryList, error) {
				return &domain.RepositoryList{}, nil
			}),
			Config: &deltaconfig.DeltaGenerationConfig{},
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When every fleet member is ineligible it should skip after loading the template version", func(t *testing.T) {
		r := Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return fleetWithTV("fleet-1", "tv-1"), nil
			}),
			TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{}, nil
			}),
			DeviceService: mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
				return []*domain.Device{
					deviceWithOS("d1", false, "sha256:aaa"),
					deviceWithOS("d2", false, "sha256:bbb"),
				}, nil
			}),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When eligible devices have no current digest it should skip", func(t *testing.T) {
		r := Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return fleetWithTV("fleet-1", "tv-1"), nil
			}),
			TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{}, nil
			}),
			DeviceService: mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
				return []*domain.Device{
					deviceWithOS("d1", false, "sha256:aaa"),
					deviceWithOS("d2", true, ""),
				}, nil
			}),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When the involved device is not eligible it should skip", func(t *testing.T) {
		r := Resolver{
			DeviceService: mockDeviceService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Device, error) {
				return deviceWithOS("d1", false, "sha256:aaa"), nil
			}, nil),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
			Render: func(_ context.Context, _ uuid.UUID, _ *domain.DeviceSpec) (tasks.RenderedSpec, error) {
				t.Fatal("render must not run for an ineligible device")
				return tasks.RenderedSpec{}, nil
			},
		}
		result, err := r.DeltaCandidates(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When the involved device has no current digest it should skip", func(t *testing.T) {
		r := Resolver{
			DeviceService: mockDeviceService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Device, error) {
				return deviceWithOS("d1", true, ""), nil
			}, nil),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
			Render: func(_ context.Context, _ uuid.UUID, _ *domain.DeviceSpec) (tasks.RenderedSpec, error) {
				t.Fatal("render must not run when current digest is missing and Expand is nil")
				return tasks.RenderedSpec{}, nil
			},
		}
		result, err := r.DeltaCandidates(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When write target loading fails it should fail the call", func(t *testing.T) {
		r := Resolver{
			RepositoryService: mockRepositoryService(func(_ context.Context, _ uuid.UUID, _ domain.ListRepositoriesParams) (*domain.RepositoryList, error) {
				return nil, fmt.Errorf("org lookup failed")
			}),
			Config: &deltaconfig.DeltaGenerationConfig{},
		}
		_, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.Error(t, err)
	})

	t.Run("When the involved object kind is unsupported it should fail", func(t *testing.T) {
		r := Resolver{
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
		}
		ev := fleetPrepareEvent(orgId, "fleet-1", "tv-1")
		ev.Event.InvolvedObject.Kind = "Repository"
		_, err := r.DeltaCandidates(ctx, ev)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported involved object kind")
	})

	t.Run("When the fleet loader fails it should fail the call", func(t *testing.T) {
		r := Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return nil, fmt.Errorf("fleet store down")
			}),
			TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, fleet, name string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
					Spec:     domain.TemplateVersionSpec{Fleet: fleet},
				}, nil
			}),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
		}
		_, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.Error(t, err)
	})

	t.Run("When the device list loader fails it should fail the call", func(t *testing.T) {
		r := Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return fleetWithTV("fleet-1", "tv-1"), nil
			}),
			TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, fleet, name string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
					Spec:     domain.TemplateVersionSpec{Fleet: fleet},
				}, nil
			}),
			DeviceService: mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
				return nil, fmt.Errorf("device list failed")
			}),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
		}
		_, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.Error(t, err)
	})

	t.Run("When the device loader fails it should fail the call", func(t *testing.T) {
		r := Resolver{
			DeviceService: mockDeviceService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Device, error) {
				return nil, fmt.Errorf("device store down")
			}, nil),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
		}
		_, err := r.DeltaCandidates(ctx, devicePrepareEvent(orgId, "d1"))
		require.Error(t, err)
	})

	t.Run("When fleet details are missing it should fail", func(t *testing.T) {
		r := Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return fleetWithTV("fleet-1", "tv-1"), nil
			}),
			TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
				t.Fatal("template version must not be loaded when details are missing")
				return nil, nil
			}),
			DeviceService: mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
				return []*domain.Device{deviceWithOS("d1", true, "sha256:aaa")}, nil
			}),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
		}
		ev := fleetPrepareEvent(orgId, "fleet-1", "tv-1")
		ev.Event.Details = nil
		_, err := r.DeltaCandidates(ctx, ev)
		require.Error(t, err)
	})
}

func fleetPrepareEvent(orgId uuid.UUID, fleet, tv string) worker_client.EventWithOrgId {
	details := domain.PrepareDeltasDetails{
		DetailType:      v1beta1.PrepareDeltas,
		TemplateVersion: lo.ToPtr(tv),
		ResourceVersion: lo.ToPtr("1"),
	}
	var eventDetails domain.EventDetails
	_ = eventDetails.FromPrepareDeltasDetails(details)
	return worker_client.EventWithOrgId{
		OrgId: orgId,
		Event: domain.Event{
			Reason: domain.EventReasonPrepareDeltas,
			InvolvedObject: domain.ObjectReference{
				Kind: domain.FleetKind,
				Name: fleet,
			},
			Details: &eventDetails,
		},
	}
}

func deviceWithOS(name string, eligible bool, digest string) *domain.Device {
	d := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name:        lo.ToPtr(name),
			Annotations: &map[string]string{domain.DeviceAnnotationRenderedSpecHash: prepareTestHash},
		},
		Spec: &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "quay.io/os/base:latest"}},
		Status: &domain.DeviceStatus{
			Os: domain.DeviceOsStatus{ImageDigest: digest},
		},
	}
	if eligible {
		d.Status.SystemInfo.DeltaEligible = lo.ToPtr(true)
		d.Status.SystemInfo.BootcVersion = lo.ToPtr("bootc 1.15.0")
	}
	return d
}

func TestDeltaCandidates_ResolveOSFromUnsavedRender(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()
	const (
		newImage   = "quay.io/acme/os:v2"
		repo       = "quay.io/acme/os"
		currentDig = "sha256:aaa"
		newDig     = "sha256:bbb"
	)

	baseResolver := func() Resolver {
		return Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return fleetWithTV("fleet-1", "tv-1"), nil
			}),
			TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv-1")},
					Status:   &domain.TemplateVersionStatus{Os: &domain.DeviceOsSpec{Image: newImage}},
				}, nil
			}),
			RepositoryService: testRepositoryService(),
			Config:            &deltaconfig.DeltaGenerationConfig{},
			Render: func(_ context.Context, _ uuid.UUID, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
				return tasks.RenderedSpec{OsImage: spec.Os.Image}, nil
			},
			Inspect: func(_ context.Context, _ uuid.UUID, image string) (string, error) {
				if image != newImage {
					return "", fmt.Errorf("unexpected image %s", image)
				}
				return newDig, nil
			},
		}
	}

	t.Run("When a fleet member is eligible it should return an OS candidate without writing spec", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		})

		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.False(t, result.Skip)
		require.Len(t, result.Candidates, 1)
		assert.Equal(t, DeltaCandidate{ImageRepository: repo, CurrentDigest: currentDig, NewDigest: newDig}, result.Candidates[0])
	})

	t.Run("When the involved object is a device it should render the existing spec", func(t *testing.T) {
		device := deviceWithOS("d1", true, currentDig)
		device.Spec.Os.Image = newImage
		r := baseResolver()
		r.DeviceService = mockDeviceService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Device, error) {
			return device, nil
		}, nil)
		result, err := r.DeltaCandidates(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		assert.False(t, result.Skip)
		require.Len(t, result.Candidates, 1)
		assert.Equal(t, repo, result.Candidates[0].ImageRepository)
		assert.Equal(t, currentDig, result.Candidates[0].CurrentDigest)
		assert.Equal(t, newDig, result.Candidates[0].NewDigest)
	})

	t.Run("When the device spec hash changed it should fail the call", func(t *testing.T) {
		device := deviceWithOS("d1", true, currentDig)
		r := baseResolver()
		r.DeviceService = mockDeviceService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Device, error) {
			return device, nil
		}, nil)
		_, err := r.DeltaCandidates(ctx, devicePrepareEventWithSpecHash(orgId, "d1", "different-hash"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "spec hash changed")
	})

	t.Run("When current digest is missing it should omit that device", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{
				deviceWithOS("missing", true, ""),
				deviceWithOS("ok", true, currentDig),
			}, nil
		})
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 1)
		assert.Equal(t, currentDig, result.Candidates[0].CurrentDigest)
	})

	t.Run("When a device is not eligible it should omit it", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{
				deviceWithOS("no", false, currentDig),
				deviceWithOS("yes", true, currentDig),
			}, nil
		})
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 1)
	})

	t.Run("When render fails for one device it should omit that device", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{
				deviceWithOS("bad", true, currentDig),
				deviceWithOS("good", true, currentDig),
			}, nil
		})
		r.TemplateVersionService = mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
			return &domain.TemplateVersion{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv-1")},
				Status:   &domain.TemplateVersionStatus{Os: &domain.DeviceOsSpec{Image: "quay.io/acme/os:{{ .metadata.name }}"}},
			}, nil
		})
		r.Render = func(_ context.Context, _ uuid.UUID, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			if spec.Os.Image == "quay.io/acme/os:bad" {
				return tasks.RenderedSpec{}, fmt.Errorf("git fetch failed")
			}
			return tasks.RenderedSpec{OsImage: newImage}, nil
		}

		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 1)
	})

	t.Run("When inspect fails it should fail the call", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		})
		r.Inspect = func(_ context.Context, _ uuid.UUID, _ string) (string, error) {
			return "", fmt.Errorf("registry down")
		}
		_, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.Error(t, err)
	})

	t.Run("When fleet details omit templateVersion it should fail", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		})
		ev := fleetPrepareEvent(orgId, "fleet-1", "tv-1")
		details := domain.PrepareDeltasDetails{DetailType: v1beta1.PrepareDeltas}
		var eventDetails domain.EventDetails
		require.NoError(t, eventDetails.FromPrepareDeltasDetails(details))
		ev.Event.Details = &eventDetails
		_, err := r.DeltaCandidates(ctx, ev)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "templateVersion")
	})

	t.Run("When one rendered OS image cannot be parsed it should omit that device", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{
				deviceWithOS("bad", true, currentDig),
				deviceWithOS("good", true, currentDig),
			}, nil
		})
		r.Render = func(_ context.Context, _ uuid.UUID, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			if spec.Os.Image == "bad" {
				return tasks.RenderedSpec{OsImage: "not a valid image!!!"}, nil
			}
			return tasks.RenderedSpec{OsImage: newImage}, nil
		}
		r.Inspect = func(_ context.Context, _ uuid.UUID, _ string) (string, error) {
			return newDig, nil
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 1)
		assert.Equal(t, currentDig, result.Candidates[0].CurrentDigest)
		assert.Equal(t, newDig, result.Candidates[0].NewDigest)
	})

	t.Run("When no rendered OS image can be parsed it should skip", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		})
		r.Render = func(_ context.Context, _ uuid.UUID, _ *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			return tasks.RenderedSpec{OsImage: "not a valid image!!!"}, nil
		}
		r.Inspect = func(_ context.Context, _ uuid.UUID, _ string) (string, error) {
			t.Fatal("inspect must not run for an unparseable image")
			return newDig, nil
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When desired spec fails for one device it should omit that device", func(t *testing.T) {
		r := baseResolver()
		good := deviceWithOS("good", true, currentDig)
		labels := map[string]string{"image": "v2"}
		good.Metadata.Labels = &labels
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("bad", true, currentDig), good}, nil
		})
		r.TemplateVersionService = mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
			return &domain.TemplateVersion{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv-1")},
				Status:   &domain.TemplateVersionStatus{Os: &domain.DeviceOsSpec{Image: "quay.io/acme/os:{{ .metadata.labels.image }}"}},
			}, nil
		})
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 1)
	})

	t.Run("When render returns no OS image it should skip", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		})
		r.Render = func(_ context.Context, _ uuid.UUID, _ *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			return tasks.RenderedSpec{}, nil
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When Expand is nil it should keep OS candidates", func(t *testing.T) {
		r := baseResolver()
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		})
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 1)
	})

	t.Run("When Expand is set it should append to OS candidates", func(t *testing.T) {
		r := baseResolver()
		device := deviceWithOS("d1", true, currentDig)
		r.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{device}, nil
		})
		r.Expand = func(expandCtx context.Context, expandOrg uuid.UUID, expandDevice *domain.Device, rendered tasks.RenderedSpec, cands []DeltaCandidate) []DeltaCandidate {
			assert.Equal(t, ctx, expandCtx)
			assert.Equal(t, orgId, expandOrg)
			assert.Equal(t, "d1", lo.FromPtr(expandDevice.Metadata.Name))
			assert.Equal(t, newImage, rendered.OsImage)
			return append(cands, DeltaCandidate{ImageRepository: "quay.io/apps/web", CurrentDigest: "sha256:ccc", NewDigest: "sha256:ddd"})
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 2)
		assert.Equal(t, "quay.io/apps/web", result.Candidates[1].ImageRepository)
	})

	t.Run("When the fleet device list is paginated it should process each page before loading the next", func(t *testing.T) {
		r := baseResolver()
		firstPageDevice := deviceWithOS("d1", true, currentDig)
		secondPageDevice := deviceWithOS("d2", true, "sha256:ccc")
		continueToken := "page-2"
		processedFirstPage := false
		r.Inspect = func(_ context.Context, _ uuid.UUID, _ string) (string, error) {
			processedFirstPage = true
			return newDig, nil
		}
		r.DeviceService = &deviceServiceMock{
			pages: func(_ context.Context, _ uuid.UUID, params domain.ListDevicesParams) (*domain.DeviceList, error) {
				assert.Equal(t, "metadata.owner=Fleet/fleet-1,status.systemInfo.deltaEligible=true", lo.FromPtr(params.FieldSelector))
				if params.Continue == nil {
					return &domain.DeviceList{
						Items:    []domain.Device{*firstPageDevice},
						Metadata: domain.ListMeta{Continue: &continueToken},
					}, nil
				}
				require.True(t, processedFirstPage)
				assert.Equal(t, continueToken, *params.Continue)
				return &domain.DeviceList{Items: []domain.Device{*secondPageDevice}}, nil
			},
		}

		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 2)
	})
}

func TestDeltaCandidates_DedupInOrg(t *testing.T) {
	orgId := uuid.New()
	const (
		newImage   = "quay.io/acme/os:v2"
		currentDig = "sha256:aaa"
		newDig     = "sha256:bbb"
	)
	r := Resolver{
		FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
			return fleetWithTV("fleet-1", "tv-1"), nil
		}),
		TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
			return &domain.TemplateVersion{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv-1")},
				Status:   &domain.TemplateVersionStatus{Os: &domain.DeviceOsSpec{Image: newImage}},
			}, nil
		}),
		DeviceService: mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{
				deviceWithOS("d1", true, currentDig),
				deviceWithOS("d2", true, currentDig),
			}, nil
		}),
		RepositoryService: testRepositoryService(),
		Config:            &deltaconfig.DeltaGenerationConfig{},
		Render: func(_ context.Context, _ uuid.UUID, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			return tasks.RenderedSpec{OsImage: spec.Os.Image}, nil
		},
		Inspect: func(_ context.Context, _ uuid.UUID, _ string) (string, error) {
			return newDig, nil
		},
	}

	result, err := r.DeltaCandidates(context.Background(), fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)
	assert.Equal(t, "quay.io/acme/os", result.Candidates[0].ImageRepository)
	assert.Equal(t, currentDig, result.Candidates[0].CurrentDigest)
	assert.Equal(t, newDig, result.Candidates[0].NewDigest)
}

func devicePrepareEvent(orgId uuid.UUID, name string) worker_client.EventWithOrgId {
	return devicePrepareEventWithSpecHash(orgId, name, prepareTestHash)
}

func devicePrepareEventWithSpecHash(orgId uuid.UUID, name, specHash string) worker_client.EventWithOrgId {
	details := domain.PrepareDeltasDetails{DetailType: v1beta1.PrepareDeltas, SpecHash: lo.ToPtr(specHash), ResourceVersion: lo.ToPtr("1")}
	var eventDetails domain.EventDetails
	_ = eventDetails.FromPrepareDeltasDetails(details)
	return worker_client.EventWithOrgId{
		OrgId: orgId,
		Event: domain.Event{
			Reason: domain.EventReasonPrepareDeltas,
			InvolvedObject: domain.ObjectReference{
				Kind: domain.DeviceKind,
				Name: name,
			},
			Details: &eventDetails,
		},
	}
}

func devicePrepareEventWithSpecHashAndResourceVersion(orgId uuid.UUID, name, specHash, resourceVersion string) worker_client.EventWithOrgId {
	details := domain.PrepareDeltasDetails{DetailType: v1beta1.PrepareDeltas, SpecHash: lo.ToPtr(specHash), ResourceVersion: lo.ToPtr(resourceVersion)}
	var eventDetails domain.EventDetails
	_ = eventDetails.FromPrepareDeltasDetails(details)
	return worker_client.EventWithOrgId{
		OrgId: orgId,
		Event: domain.Event{
			Reason: domain.EventReasonPrepareDeltas,
			InvolvedObject: domain.ObjectReference{
				Kind: domain.DeviceKind,
				Name: name,
			},
			Details: &eventDetails,
		},
	}
}
