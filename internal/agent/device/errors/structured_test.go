package errors

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// TestFormatError tests the full FormatError pipeline with real-world errors from CSV documentation.
func TestFormatError(t *testing.T) {
	testCases := []struct {
		name        string
		err         error
		expectedErr string
	}{
		{
			name: "sentinel: ErrNetwork from applications",
			err: fmt.Errorf("applications: parsing apps: getting image provider spec: %w",
				errors.Join(ErrPhasePreparing, ErrComponentApplications, ErrNetwork)),
			expectedErr: `While Preparing, applications failed: Network issue - Service unavailable (network issue)`,
		},
		{
			name: "sentinel: ErrAuthenticationFailed",
			err: fmt.Errorf("applications: verify app provider: no retry: verifying image: pulling image: %w",
				errors.Join(ErrPhasePreparing, ErrComponentApplications, ErrAuthenticationFailed)),
			expectedErr: `While Preparing, applications failed: Security issue - Authentication failed`,
		},
		{
			name: "sentinel: ErrCriticalResourceAlert from prefetch",
			err: fmt.Errorf("prefetch: critical resource alert: insufficient disk storage space, please clear storage: %w",
				errors.Join(ErrPhasePreparing, ErrComponentPrefetch, ErrCriticalResourceAlert)),
			expectedErr: `While Preparing, applications failed: Resource issue - Insufficient resources (disk space, memory)`,
		},
		{
			name: "sentinel: ErrDownloadPolicyNotReady",
			err: fmt.Errorf("download policy: download policy not ready: %w",
				errors.Join(ErrPhasePreparing, ErrComponentDownloadPolicy, ErrDownloadPolicyNotReady)),
			expectedErr: `While Preparing, update policy failed: Configuration issue - Precondition not met (waiting for dependencies)`,
		},
		{
			name: "sentinel: ErrNotFound from hooks workdir",
			err: fmt.Errorf("hooks: failed to execute BeforeUpdating hook action #1: workdir /opt/hooks: file does not exist: %w",
				errors.Join(ErrPhasePreparing, ErrComponentHooks, ErrNotFound)),
			expectedErr: `While Preparing, lifecycle failed for "/opt/hooks": Filesystem issue - Required resource not found`,
		},
		{
			name: "sentinel: ErrUnsupportedAppType",
			err: fmt.Errorf(`applications: parsing apps: constructing inline app handler: unsupported application type: "unknown": %w`,
				errors.Join(ErrPhasePreparing, ErrComponentApplications, ErrUnsupportedAppType)),
			expectedErr: `While Preparing, applications failed for "unknown": System issue - Feature not supported`,
		},
		{
			name: "component keyword: app type mismatch",
			err: fmt.Errorf(`applications: parsing apps: ensuring app type: required label not found: app type mismatch: declared "compose" discovered "quadlet": %w`,
				errors.Join(ErrPhasePreparing, ErrComponentApplications, ErrAppLabel)),
			expectedErr: `While Preparing, applications failed for "compose": Configuration issue - Precondition not met (waiting for dependencies)`,
		},
		{
			name: "component keyword: stage image",
			err: fmt.Errorf("stage image: bootc switch failed: image not found: %w",
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentOS, ErrImageNotFound)),
			expectedErr: `While ApplyingUpdate, os failed: Network issue - Service unavailable (network issue)`,
		},
		{
			name: "component keyword: failed to push status",
			err: fmt.Errorf("failed to update status with decommission started Condition: failed to update decommission started status: failed to push status update: %w",
				errors.Join(ErrPhaseActivatingConfig, ErrComponentLifecycle, ErrNetwork)),
			expectedErr: `While Rebooting, lifecycle failed: Network issue - Service unavailable (network issue)`,
		},
		{
			name: "component keyword: invalid patterns systemd",
			err: fmt.Errorf("systemd: invalid patterns: invalid regex: [bad, error: unexpected end of pattern: %w",
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentSystemd)),
			expectedErr: `While ApplyingUpdate, systemd failed for "[bad": Configuration issue - Invalid configuration or input`,
		},
		{
			name: "component keyword: exit code hooks",
			err: fmt.Errorf("hooks: failed to execute BeforeUpdating hook action #1: command failed (exit code 1): %w",
				errors.Join(ErrPhasePreparing, ErrComponentHooks)),
			expectedErr: `While Preparing, lifecycle failed: Configuration issue - Invalid configuration or input`,
		},
		{
			name: "component keyword: convert string to int64 policy",
			err: fmt.Errorf("update policy: convert string to int64: strconv.ParseInt: parsing: syntax error: %w",
				errors.Join(ErrPhasePreparing, ErrComponentUpdatePolicy)),
			expectedErr: `While Preparing, update policy failed: System issue - Internal error occurred`,
		},
		{
			name: "global keyword: permission denied",
			err: fmt.Errorf(`failed to apply configuration: write file /etc/app.conf: failed to create directory "/etc/app": permission denied: %w`,
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentConfig)),
			expectedErr: `While ApplyingUpdate, config failed for "/etc/app": Security issue - Permission denied`,
		},
		{
			name: "global keyword: no space left",
			err: fmt.Errorf("installing application: writing env file: no space left on device: %w",
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentApplications)),
			expectedErr: `While ApplyingUpdate, applications failed: Resource issue - Insufficient resources (disk space, memory)`,
		},
		{
			name: "global keyword: context canceled",
			err: fmt.Errorf("resources: context canceled: %w",
				errors.Join(ErrPhasePreparing, ErrComponentResources)),
			expectedErr: `While Preparing, resources failed: System issue - Operation was cancelled`,
		},
		{
			name: "global keyword: does not exist",
			err: fmt.Errorf(`reading hook actions from "/etc/flightctl/hooks.d/pre-update.yaml": file does not exist: %w`,
				errors.Join(ErrPhasePreparing, ErrComponentHooks)),
			expectedErr: `While Preparing, lifecycle failed for "/etc/flightctl/hooks.d/pre-update.yaml": Filesystem issue - Required resource not found`,
		},
		{
			name: "global keyword: failed to decode",
			err: fmt.Errorf("failed to apply configuration: failed to decode base64 content: illegal base64: %w",
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentConfig)),
			expectedErr: `While ApplyingUpdate, config failed: Configuration issue - Invalid configuration or input`,
		},
		{
			name: "subcomponent: hooks -> lifecycle",
			err: fmt.Errorf("hooks: validation failed: missing required field: %w",
				errors.Join(ErrPhasePreparing, ErrComponentHooks)),
			expectedErr: `While Preparing, lifecycle failed: Configuration issue - Invalid configuration or input`,
		},
		{
			name: "subcomponent: prefetch -> applications",
			err: fmt.Errorf("prefetch: prefetch collector 0 failed: getting OS status: bootc status: command failed: %w",
				errors.Join(ErrPhasePreparing, ErrComponentPrefetch)),
			expectedErr: `While Preparing, applications failed: System issue - Internal error occurred`,
		},
		{
			name: "subcomponent: downloadPolicy -> updatePolicy",
			err: fmt.Errorf("download policy: version number cannot be negative: -1: %w",
				errors.Join(ErrPhasePreparing, ErrComponentDownloadPolicy)),
			expectedErr: `While Preparing, update policy failed: System issue - Internal error occurred`,
		},
		{
			name: "subcomponent: osReconciled -> os",
			err: fmt.Errorf("getting current bootc status: bootc status: exit code 1: %w",
				errors.Join(ErrPhaseActivatingConfig, ErrComponentOSReconciled, ErrGettingBootcStatus)),
			expectedErr: `While Rebooting, os failed: System issue - Internal error occurred`,
		},
		{
			name: "phase: Preparing (before update)",
			err: fmt.Errorf("applications: verify app provider: invalid spec: validating env vars: %w",
				errors.Join(ErrPhasePreparing, ErrComponentApplications, ErrInvalidSpec)),
			expectedErr: `While Preparing, applications failed: Configuration issue - Invalid configuration or input`,
		},
		{
			name: "phase: ApplyingUpdate (sync device)",
			err: fmt.Errorf(`removing application: removing application: remove path "/var/lib/app": permission denied: %w`,
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentApplications)),
			expectedErr: `While ApplyingUpdate, applications failed for "/var/lib/app": Security issue - Permission denied`,
		},
		{
			name: "phase: ActivatingConfig -> Rebooting (after update)",
			err: fmt.Errorf(`error executing actions: starting target myapp.target: service: "myapp.service" logs: failed to start: %w`,
				errors.Join(ErrPhaseActivatingConfig, ErrComponentApplications)),
			expectedErr: `While Rebooting, applications failed for "myapp.service": System issue - Internal error occurred`,
		},
		{
			name: "phase: missing -> Unknown",
			err: fmt.Errorf("some error without phase: %w",
				errors.Join(ErrComponentApplications, ErrNetwork)),
			expectedErr: `While Unknown, applications failed: Network issue - Service unavailable (network issue)`,
		},
		{
			name: "element: file path in quotes",
			err: fmt.Errorf(`failed to remove obsolete files: deleting files failed: remove file "/etc/old.conf": permission denied: %w`,
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentConfig)),
			expectedErr: `While ApplyingUpdate, config failed for "/etc/old.conf": Security issue - Permission denied`,
		},
		{
			name: "element: directory in quotes",
			err: fmt.Errorf(`failed to apply configuration: write file /etc/app.conf: failed to create directory "/etc/app": permission denied: %w`,
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentConfig)),
			expectedErr: `While ApplyingUpdate, config failed for "/etc/app": Security issue - Permission denied`,
		},
		{
			name: "element: service name in quotes",
			err: fmt.Errorf(`error executing actions: starting target myapp.target: service: "myapp.service" logs: failed to start: %w`,
				errors.Join(ErrPhaseActivatingConfig, ErrComponentApplications)),
			expectedErr: `While Rebooting, applications failed for "myapp.service": System issue - Internal error occurred`,
		},
		{
			name: "element: volume name",
			err: fmt.Errorf(`error executing actions: creating volumes: pulling image volume: inspect volume "data-vol": podman failed: %w`,
				errors.Join(ErrPhaseActivatingConfig, ErrComponentApplications)),
			expectedErr: `While Rebooting, applications failed for "data-vol": System issue - Internal error occurred`,
		},
		{
			name: "element: image reference",
			err: fmt.Errorf("prefetch: prefetch collector 1 failed: collecting nested OCI targets: getting image digest for quay.io/myapp:v1: podman inspect failed: %w",
				errors.Join(ErrPhasePreparing, ErrComponentPrefetch)),
			expectedErr: `While Preparing, applications failed for "quay.io/myapp": Network issue - Service unavailable (network issue)`,
		},
		{
			name: "element: target name",
			err: fmt.Errorf("error executing actions: removing: stopping target myapp.target: failed to stop: %w",
				errors.Join(ErrPhaseActivatingConfig, ErrComponentApplications)),
			expectedErr: `While Rebooting, applications failed for "myapp.target": System issue - Internal error occurred`,
		},
		{
			name: "element: unsupported type quoted",
			err: fmt.Errorf(`installing application: decoding application content: unsupported content encoding: "gzip": %w`,
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentApplications)),
			expectedErr: `While ApplyingUpdate, applications failed for "gzip": System issue - Internal error occurred`,
		},
		{
			name: "element: invalid duration quoted",
			err: fmt.Errorf(`resources: time: invalid duration "30 minutes": %w`,
				errors.Join(ErrPhasePreparing, ErrComponentResources)),
			expectedErr: `While Preparing, resources failed for "30 minutes": System issue - Internal error occurred`,
		},
		{
			name: "edge: unknown status code (no matching keyword)",
			err: fmt.Errorf("completely random error xyz123: %w",
				errors.Join(ErrPhasePreparing, ErrComponentResources)),
			expectedErr: `While Preparing, resources failed: System issue - An error occurred`,
		},
		{
			name:        "edge: missing component -> unknown",
			err:         fmt.Errorf("error without component: %w", ErrPhasePreparing),
			expectedErr: `While Preparing, unknown failed: System issue - An error occurred`,
		},
		{
			name: "edge: deeply nested error",
			err: fmt.Errorf("applications: verify app provider: no retry: verifying inline: ensuring dependencies: podman not found: %w",
				errors.Join(ErrPhasePreparing, ErrComponentApplications)),
			expectedErr: `While Preparing, applications failed: System issue - Internal error occurred`,
		},
		{
			name: "edge: error with multiple quoted strings extracts first",
			err: fmt.Errorf(`app type mismatch: declared "compose" discovered "quadlet": %w`,
				errors.Join(ErrPhasePreparing, ErrComponentApplications)),
			expectedErr: `While Preparing, applications failed for "compose": Configuration issue - Precondition not met (waiting for dependencies)`,
		},
		{
			name: "component: applications",
			err: fmt.Errorf("applications: parsing apps: getting volumes: json error: %w",
				errors.Join(ErrPhasePreparing, ErrComponentApplications)),
			expectedErr: `While Preparing, applications failed: System issue - Internal error occurred`,
		},
		{
			name: "component: config",
			err: fmt.Errorf("failed to apply configuration: safecast.ToUint32 overflow: %w",
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentConfig)),
			expectedErr: `While ApplyingUpdate, config failed: System issue - Internal error occurred`,
		},
		{
			name: "component: systemd",
			err: fmt.Errorf("systemd: invalid patterns: regex compilation failed: %w",
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentSystemd)),
			expectedErr: `While ApplyingUpdate, systemd failed: Configuration issue - Invalid configuration or input`,
		},
		{
			name: "component: lifecycle",
			err: fmt.Errorf("failed to initiate system reboot: reboot systemd: failed to start: %w",
				errors.Join(ErrPhaseActivatingConfig, ErrComponentLifecycle)),
			expectedErr: `While Rebooting, lifecycle failed: System issue - Internal error occurred`,
		},
		{
			name: "component: os",
			err: fmt.Errorf("unable to parse image reference into a valid bootc target: invalid format: %w",
				errors.Join(ErrPhaseApplyingUpdate, ErrComponentOS)),
			expectedErr: `While ApplyingUpdate, os failed: Configuration issue - Invalid configuration or input`,
		},
		{
			name: "component: resources",
			err: fmt.Errorf("resources: AsDiskResourceMonitorSpec() error: json unmarshal: %w",
				errors.Join(ErrPhasePreparing, ErrComponentResources)),
			expectedErr: `While Preparing, resources failed: System issue - Internal error occurred`,
		},
		{
			name: "component: updatePolicy",
			err: fmt.Errorf("update policy: update policy not ready: maintenance window not reached: %w",
				errors.Join(ErrPhasePreparing, ErrComponentUpdatePolicy, ErrUpdatePolicyNotReady)),
			expectedErr: `While Preparing, update policy failed: Configuration issue - Precondition not met (waiting for dependencies)`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ts := FormatError(tc.err)

			require.NotEmpty(t, msg)
			require.False(t, ts.IsZero())
			require.Equal(t, tc.expectedErr, stripTimestamp(msg))
		})
	}
}

// TestBuildMessage tests the buildMessage function - the core formatting logic.
func TestBuildMessage(t *testing.T) {
	fixedTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	t.Run("with element", func(t *testing.T) {
		se := StructuredError{
			Phase: "Preparing", Component: "applications", Element: "/etc/app.conf",
			Category: CategorySecurity, StatusCode: codes.PermissionDenied, Timestamp: fixedTime,
		}
		require.Equal(t, `[2025-01-15 10:30:00 +0000 UTC] While Preparing, applications failed for "/etc/app.conf": Security issue - Permission denied`, se.buildMessage())
	})

	t.Run("without element", func(t *testing.T) {
		se := StructuredError{
			Phase: "ApplyingUpdate", Component: "config", Element: "",
			Category: CategoryNetwork, StatusCode: codes.Unavailable, Timestamp: fixedTime,
		}
		require.Equal(t, `[2025-01-15 10:30:00 +0000 UTC] While ApplyingUpdate, config failed: Network issue - Service unavailable (network issue)`, se.buildMessage())
	})
}

// stripTimestamp removes the timestamp prefix from a formatted error message.
func stripTimestamp(msg string) string {
	if idx := strings.Index(msg, "] "); idx != -1 {
		return msg[idx+2:]
	}
	return msg
}
