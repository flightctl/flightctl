package common

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
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
