package common

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestGetDependencyChangeDetectedEvent(t *testing.T) {
	tests := []struct {
		name        string
		kind        domain.ResourceKind
		resName     string
		resourceKey string
		fingerprint string
	}{
		{
			name:        "When a git dependency changes it should include resourceKey and fingerprint in details",
			kind:        domain.FleetKind,
			resName:     "fleet-a",
			resourceKey: "git:my-repo/main",
			fingerprint: "abc123def456",
		},
		{
			name:        "When an HTTP dependency changes it should create an event for the fleet",
			kind:        domain.FleetKind,
			resName:     "fleet-b",
			resourceKey: "http:my-repo/config.json",
			fingerprint: "etag-xyz",
		},
		{
			name:        "When a secret dependency changes it should create an event for the device",
			kind:        domain.DeviceKind,
			resName:     "device-1",
			resourceKey: "secret:prod/db-creds",
			fingerprint: "rv1234",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			event := GetDependencyChangeDetectedEvent(ctx, tc.kind, tc.resName, tc.resourceKey, tc.fingerprint)

			require.NotNil(t, event)
			require.Equal(t, domain.EventReasonDependencyChangeDetected, event.Reason)
			require.Equal(t, string(tc.kind), event.InvolvedObject.Kind)
			require.Equal(t, tc.resName, event.InvolvedObject.Name)
			require.Contains(t, event.Message, tc.resourceKey)

			require.NotNil(t, event.Details)
			details, err := event.Details.AsDependencyChangeDetectedDetails()
			require.NoError(t, err)
			require.Equal(t, tc.resourceKey, details.ResourceKey)
			require.Equal(t, tc.fingerprint, details.Fingerprint)
		})
	}
}

func TestGetDependencySyncProbeFailedEvent(t *testing.T) {
	tests := []struct {
		name        string
		kind        domain.ResourceKind
		resName     string
		resourceKey string
		errMsg      string
	}{
		{
			name:        "When git probe fails it should create a warning event with details",
			kind:        domain.FleetKind,
			resName:     "fleet-a",
			resourceKey: "git:my-repo/main",
			errMsg:      "connection refused",
		},
		{
			name:        "When HTTP probe fails it should create a warning event with details",
			kind:        domain.DeviceKind,
			resName:     "device-1",
			resourceKey: "http:my-repo/config.json",
			errMsg:      "timeout after 30s",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			event := GetDependencySyncProbeFailedEvent(ctx, tc.kind, tc.resName, tc.resourceKey, tc.errMsg)

			require.NotNil(t, event)
			require.Equal(t, domain.EventReasonDependencySyncProbeFailed, event.Reason)
			require.Equal(t, domain.Warning, event.Type)
			require.Equal(t, string(tc.kind), event.InvolvedObject.Kind)
			require.Equal(t, tc.resName, event.InvolvedObject.Name)
			require.Contains(t, event.Message, tc.resourceKey)

			require.NotNil(t, event.Details)
			details, err := event.Details.AsDependencySyncProbeFailedDetails()
			require.NoError(t, err)
			require.Equal(t, tc.resourceKey, details.ResourceKey)
			require.Equal(t, tc.errMsg, details.Error)
		})
	}
}

func TestComputeResourceUpdatedDetails(t *testing.T) {
	t.Run("When generation changes it should report a Spec update", func(t *testing.T) {
		old := domain.ObjectMeta{Name: lo.ToPtr("x"), Generation: lo.ToPtr(int64(1))}
		newM := domain.ObjectMeta{Name: lo.ToPtr("x"), Generation: lo.ToPtr(int64(2))}
		details := ComputeResourceUpdatedDetails(old, newM)
		require.NotNil(t, details)
		require.Contains(t, details.UpdatedFields, domain.Spec)
	})

	t.Run("When labels change it should report a Labels update", func(t *testing.T) {
		old := domain.ObjectMeta{Name: lo.ToPtr("x"), Labels: lo.ToPtr(map[string]string{"a": "1"})}
		newM := domain.ObjectMeta{Name: lo.ToPtr("x"), Labels: lo.ToPtr(map[string]string{"a": "2"})}
		details := ComputeResourceUpdatedDetails(old, newM)
		require.NotNil(t, details)
		require.Contains(t, details.UpdatedFields, domain.Labels)
	})

	t.Run("When owner changes it should report an Owner update with previous/new owner", func(t *testing.T) {
		old := domain.ObjectMeta{Name: lo.ToPtr("x"), Owner: lo.ToPtr("owner1")}
		newM := domain.ObjectMeta{Name: lo.ToPtr("x"), Owner: lo.ToPtr("owner2")}
		details := ComputeResourceUpdatedDetails(old, newM)
		require.NotNil(t, details)
		require.Contains(t, details.UpdatedFields, domain.Owner)
		require.Equal(t, lo.ToPtr("owner1"), details.PreviousOwner)
		require.Equal(t, lo.ToPtr("owner2"), details.NewOwner)
	})

	t.Run("When nothing changes it should return nil", func(t *testing.T) {
		old := domain.ObjectMeta{Name: lo.ToPtr("x"), Generation: lo.ToPtr(int64(1))}
		details := ComputeResourceUpdatedDetails(old, old)
		require.Nil(t, details)
	})
}

func TestCastResources(t *testing.T) {
	t.Run("When both resources are nil it should return ok=true with nil pointers", func(t *testing.T) {
		oldTyped, newTyped, ok := CastResources[domain.Device](nil, nil)
		require.True(t, ok)
		require.Nil(t, oldTyped)
		require.Nil(t, newTyped)
	})

	t.Run("When both resources are the correct type it should return ok=true", func(t *testing.T) {
		device := &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")}}
		oldTyped, newTyped, ok := CastResources[domain.Device](device, device)
		require.True(t, ok)
		require.Same(t, device, oldTyped)
		require.Same(t, device, newTyped)
	})

	t.Run("When a resource has the wrong type it should return ok=false", func(t *testing.T) {
		_, _, ok := CastResources[domain.Device]("not-a-device", nil)
		require.False(t, ok)
	})
}
