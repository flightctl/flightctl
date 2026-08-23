package delta_worker

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
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
