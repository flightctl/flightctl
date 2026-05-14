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
		detector    string
	}{
		{
			name:        "When detector is git_ls_remote it should include it in details and message",
			kind:        domain.FleetKind,
			resName:     "fleet-a",
			resourceKey: "git:my-repo/main",
			fingerprint: "abc123def456",
			detector:    "git_ls_remote",
		},
		{
			name:        "When detector is http_conditional_get it should include it in details and message",
			kind:        domain.FleetKind,
			resName:     "fleet-b",
			resourceKey: "http:my-repo/config.json",
			fingerprint: "etag-xyz",
			detector:    "http_conditional_get",
		},
		{
			name:        "When detector is secret_informer it should include it in details and message",
			kind:        domain.DeviceKind,
			resName:     "device-1",
			resourceKey: "secret:prod/db-creds",
			fingerprint: "rv1234",
			detector:    "secret_informer",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			event := GetDependencyChangeDetectedEvent(ctx, tc.kind, tc.resName, tc.resourceKey, tc.fingerprint, tc.detector)

			require.NotNil(t, event)
			require.Equal(t, domain.EventReasonDependencyChangeDetected, event.Reason)
			require.Equal(t, string(tc.kind), event.InvolvedObject.Kind)
			require.Equal(t, tc.resName, event.InvolvedObject.Name)
			require.Contains(t, event.Message, "automated-sync")
			require.Contains(t, event.Message, tc.detector)
			require.Contains(t, event.Message, tc.resourceKey)

			require.NotNil(t, event.Details)
			details, err := event.Details.AsDependencyChangeDetectedDetails()
			require.NoError(t, err)
			require.Equal(t, tc.resourceKey, details.ResourceKey)
			require.Equal(t, tc.fingerprint, details.Fingerprint)
			require.NotNil(t, details.Detector)
			require.Equal(t, tc.detector, *details.Detector)
		})
	}
}

func TestGetDependencySyncProbeFailedEvent(t *testing.T) {
	tests := []struct {
		name        string
		kind        domain.ResourceKind
		resName     string
		resourceKey string
		detector    string
		errMsg      string
	}{
		{
			name:        "When git probe fails it should create a warning event with details",
			kind:        domain.FleetKind,
			resName:     "fleet-a",
			resourceKey: "git:my-repo/main",
			detector:    "git_ls_remote",
			errMsg:      "connection refused",
		},
		{
			name:        "When HTTP probe fails it should create a warning event with details",
			kind:        domain.DeviceKind,
			resName:     "device-1",
			resourceKey: "http:my-repo/config.json",
			detector:    "http_conditional_get",
			errMsg:      "timeout after 30s",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			event := GetDependencySyncProbeFailedEvent(ctx, tc.kind, tc.resName, tc.resourceKey, tc.detector, tc.errMsg)

			require.NotNil(t, event)
			require.Equal(t, domain.EventReasonDependencySyncProbeFailed, event.Reason)
			require.Equal(t, domain.Warning, event.Type)
			require.Equal(t, string(tc.kind), event.InvolvedObject.Kind)
			require.Equal(t, tc.resName, event.InvolvedObject.Name)
			require.Contains(t, event.Message, "automated-sync")
			require.Contains(t, event.Message, tc.detector)
			require.Contains(t, event.Message, tc.resourceKey)

			require.NotNil(t, event.Details)
			details, err := event.Details.AsDependencySyncProbeFailedDetails()
			require.NoError(t, err)
			require.Equal(t, tc.resourceKey, details.ResourceKey)
			require.Equal(t, tc.errMsg, details.Error)
		})
	}
}
