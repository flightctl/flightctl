package delta_worker

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeltaCandidates_SkipPaths(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()

	t.Run("When generateDelta is false it should skip", func(t *testing.T) {
		r := Resolver{
			Fleet: func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return &domain.Fleet{
					Spec: domain.FleetSpec{
						RolloutPolicy: &domain.RolloutPolicy{GenerateDelta: lo.ToPtr(false)},
					},
				}, nil
			},
			WriteTarget: func(_ context.Context, _ uuid.UUID) (*domain.OciRepoSpec, error) {
				return &domain.OciRepoSpec{Registry: "quay.io"}, nil
			},
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When there is no write target it should skip", func(t *testing.T) {
		r := Resolver{
			Fleet: func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return &domain.Fleet{Spec: domain.FleetSpec{}}, nil
			},
			WriteTarget: func(_ context.Context, _ uuid.UUID) (*domain.OciRepoSpec, error) {
				return nil, nil
			},
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})

	t.Run("When no device is eligible it should skip", func(t *testing.T) {
		r := Resolver{
			Fleet: func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return &domain.Fleet{Spec: domain.FleetSpec{}}, nil
			},
			TemplateVersion: func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{}, nil
			},
			Devices: func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
				return []*domain.Device{
					deviceWithOS("d1", false, "sha256:aaa"),
					deviceWithOS("d2", true, ""),
				}, nil
			},
			WriteTarget: func(_ context.Context, _ uuid.UUID) (*domain.OciRepoSpec, error) {
				return &domain.OciRepoSpec{Registry: "quay.io"}, nil
			},
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.True(t, result.Skip)
		assert.Empty(t, result.Candidates)
	})
}

func fleetPrepareEvent(orgId uuid.UUID, fleet, tv string) worker_client.EventWithOrgId {
	details := domain.PrepareDeltasDetails{
		DetailType:      v1beta1.PrepareDeltas,
		TemplateVersion: lo.ToPtr(tv),
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
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
		Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "quay.io/os/base:latest"}},
		Status: &domain.DeviceStatus{
			Os: domain.DeviceOsStatus{ImageDigest: digest},
		},
	}
	if eligible {
		d.Status.Capabilities = &domain.DeviceCapabilities{DeltaEligible: lo.ToPtr(true)}
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
			Fleet: func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return &domain.Fleet{Spec: domain.FleetSpec{}}, nil
			},
			TemplateVersion: func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv-1")}}, nil
			},
			WriteTarget: func(_ context.Context, _ uuid.UUID) (*domain.OciRepoSpec, error) {
				return &domain.OciRepoSpec{Registry: "quay.io"}, nil
			},
			DesiredSpec: func(_ *domain.Device, _ *domain.TemplateVersion) (*domain.DeviceSpec, error) {
				return &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: newImage}}, nil
			},
			Render: func(_ context.Context, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
				return tasks.RenderedSpec{OsImage: spec.Os.Image}, nil
			},
			Inspect: func(_ context.Context, image string) (string, error) {
				if image != newImage {
					return "", fmt.Errorf("unexpected image %s", image)
				}
				return newDig, nil
			},
		}
	}

	t.Run("When a fleet member is eligible it should return an OS candidate without writing spec", func(t *testing.T) {
		r := baseResolver()
		r.Devices = func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		}

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
		r.Device = func(_ context.Context, _ uuid.UUID, _ string) (*domain.Device, error) {
			return device, nil
		}
		r.DesiredSpec = func(_ *domain.Device, _ *domain.TemplateVersion) (*domain.DeviceSpec, error) {
			t.Fatal("device path must not build a fleet desired spec")
			return nil, nil
		}

		result, err := r.DeltaCandidates(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		assert.False(t, result.Skip)
		require.Len(t, result.Candidates, 1)
		assert.Equal(t, repo, result.Candidates[0].ImageRepository)
		assert.Equal(t, currentDig, result.Candidates[0].CurrentDigest)
		assert.Equal(t, newDig, result.Candidates[0].NewDigest)
	})

	t.Run("When current digest is missing it should omit that device", func(t *testing.T) {
		r := baseResolver()
		r.Devices = func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{
				deviceWithOS("missing", true, ""),
				deviceWithOS("ok", true, currentDig),
			}, nil
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 1)
		assert.Equal(t, currentDig, result.Candidates[0].CurrentDigest)
	})

	t.Run("When a device is not eligible it should omit it", func(t *testing.T) {
		r := baseResolver()
		r.Devices = func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{
				deviceWithOS("no", false, currentDig),
				deviceWithOS("yes", true, currentDig),
			}, nil
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 1)
	})

	t.Run("When render fails for one device it should omit that device", func(t *testing.T) {
		r := baseResolver()
		r.Devices = func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{
				deviceWithOS("bad", true, currentDig),
				deviceWithOS("good", true, currentDig),
			}, nil
		}
		r.DesiredSpec = func(device *domain.Device, _ *domain.TemplateVersion) (*domain.DeviceSpec, error) {
			return &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: lo.FromPtr(device.Metadata.Name)}}, nil
		}
		r.Render = func(_ context.Context, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			if spec.Os.Image == "bad" {
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
		r.Devices = func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		}
		r.Inspect = func(_ context.Context, _ string) (string, error) {
			return "", fmt.Errorf("registry down")
		}
		_, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.Error(t, err)
	})

	t.Run("When Expand is nil it should keep OS candidates", func(t *testing.T) {
		r := baseResolver()
		r.Devices = func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 1)
	})

	t.Run("When Expand is set it should append to OS candidates", func(t *testing.T) {
		r := baseResolver()
		r.Devices = func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return []*domain.Device{deviceWithOS("d1", true, currentDig)}, nil
		}
		r.Expand = func(_ tasks.RenderedSpec, cands []DeltaCandidate) []DeltaCandidate {
			return append(cands, DeltaCandidate{ImageRepository: "quay.io/apps/web", CurrentDigest: "sha256:ccc", NewDigest: "sha256:ddd"})
		}
		result, err := r.DeltaCandidates(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, result.Candidates, 2)
		assert.Equal(t, "quay.io/apps/web", result.Candidates[1].ImageRepository)
	})
}

func devicePrepareEvent(orgId uuid.UUID, name string) worker_client.EventWithOrgId {
	details := domain.PrepareDeltasDetails{DetailType: v1beta1.PrepareDeltas}
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
