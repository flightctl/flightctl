package systemd

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestStatus(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		matchPatterns []string
		mockStdout    string
		mockStderr    string
		mockExitCode  int
		expected      *[]v1beta1.SystemdUnitStatus
		expectError   bool
		exclusions    []string
	}{
		{
			name:          "typical systemd output with multiple units",
			matchPatterns: []string{"*.service"},
			mockStdout: `Id=sshd.service
Description=OpenSSH server daemon
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled

Id=nginx.service
Description=The nginx HTTP server
LoadState=loaded
ActiveState=failed
SubState=failed
UnitFileState=enabled

Id=systemd-resolved.service
Description=Network Name Resolution
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "sshd.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "OpenSSH server daemon", EnableState: "enabled"},
				{Unit: "nginx.service", LoadState: "loaded", ActiveState: "failed", SubState: "failed", Description: "The nginx HTTP server", EnableState: "enabled"},
				{Unit: "systemd-resolved.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Network Name Resolution", EnableState: "enabled"},
			},
		},
		{
			name:          "typical systemd output with multiple units and exclusions",
			matchPatterns: []string{"*.service"},
			mockStdout: `Id=sshd.service
Description=OpenSSH server daemon
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled

Id=nginx.service
Description=The nginx HTTP server
LoadState=loaded
ActiveState=failed
SubState=failed
UnitFileState=enabled

Id=systemd-resolved.service
Description=Network Name Resolution
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "sshd.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "OpenSSH server daemon", EnableState: "enabled"},
				{Unit: "systemd-resolved.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Network Name Resolution", EnableState: "enabled"},
			},
			exclusions: []string{
				"nginx.service",
			},
		},
		{
			name:          "different unit types (service, socket, timer)",
			matchPatterns: []string{"test*"},
			mockStdout: `Id=test.service
Description=Test service
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled

Id=test.socket
Description=Test socket
LoadState=loaded
ActiveState=active
SubState=listening
UnitFileState=static

Id=test.timer
Description=Test timer
LoadState=loaded
ActiveState=active
SubState=waiting
UnitFileState=enabled
`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Test service", EnableState: "enabled"},
				{Unit: "test.socket", LoadState: "loaded", ActiveState: "active", SubState: "listening", Description: "Test socket", EnableState: "static"},
				{Unit: "test.timer", LoadState: "loaded", ActiveState: "active", SubState: "waiting", Description: "Test timer", EnableState: "enabled"},
			},
		},
		{
			name:          "non-matching exclusions",
			matchPatterns: []string{"test*"},
			mockStdout: `Id=test.service
Description=Test service
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled

Id=test.socket
Description=Test socket
LoadState=loaded
ActiveState=active
SubState=listening
UnitFileState=static

Id=test.timer
Description=Test timer
LoadState=loaded
ActiveState=active
SubState=waiting
UnitFileState=enabled
`,
			exclusions: []string{
				"nginx.service",
			},
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Test service", EnableState: "enabled"},
				{Unit: "test.socket", LoadState: "loaded", ActiveState: "active", SubState: "listening", Description: "Test socket", EnableState: "static"},
				{Unit: "test.timer", LoadState: "loaded", ActiveState: "active", SubState: "waiting", Description: "Test timer", EnableState: "enabled"},
			},
		},
		{
			name:          "various unit states and enable states",
			matchPatterns: []string{"test*.service"},
			mockStdout: `Id=test1.service
Description=One-shot service
LoadState=loaded
ActiveState=active
SubState=exited
UnitFileState=enabled

Id=test2.service
Description=Starting service
LoadState=loaded
ActiveState=activating
SubState=start-pre
UnitFileState=disabled

Id=test3.service
Description=Stopped service
LoadState=loaded
ActiveState=inactive
SubState=dead
UnitFileState=masked
`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "test1.service", LoadState: "loaded", ActiveState: "active", SubState: "exited", Description: "One-shot service", EnableState: "enabled"},
				{Unit: "test2.service", LoadState: "loaded", ActiveState: "activating", SubState: "start-pre", Description: "Starting service", EnableState: "disabled"},
				{Unit: "test3.service", LoadState: "loaded", ActiveState: "inactive", SubState: "dead", Description: "Stopped service", EnableState: "masked"},
			},
		},
		{
			name:          "invalid enum values normalized to unknown",
			matchPatterns: []string{"test.service"},
			mockStdout: `Id=test.service
Description=Future systemd version
LoadState=invalid-load-state
ActiveState=invalid-active-state
SubState=future-sub-state
UnitFileState=invalid-enable-state
`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "unknown", ActiveState: "unknown", SubState: "future-sub-state", Description: "Future systemd version", EnableState: "unknown"},
			},
		},
		{
			name:          "not-found unit with empty states",
			matchPatterns: []string{"nonexistent.service"},
			mockStdout: `Id=nonexistent.service
Description=
LoadState=
ActiveState=
SubState=
UnitFileState=
`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "nonexistent.service", LoadState: "unknown", ActiveState: "unknown", SubState: "", Description: "", EnableState: ""},
			},
		},
		{
			name:          "special characters in description",
			matchPatterns: []string{"test.service"},
			mockStdout: `Id=test.service
Description=Service with "quotes" and unicode: 日本語
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Service with \"quotes\" and unicode: 日本語", EnableState: "enabled"},
			},
		},
		{
			name:          "empty output - no matching units",
			matchPatterns: []string{"nonexistent*.service"},
			mockStdout:    ``,
			expected:      &[]v1beta1.SystemdUnitStatus{},
		},
		{
			name:          "no match patterns",
			matchPatterns: []string{},
			expected:      nil, // Status() returns early, doesn't set Systemd field
		},
		{
			name:          "systemctl command failure",
			matchPatterns: []string{"test.service"},
			mockStderr:    "Failed to show units: Connection refused",
			mockExitCode:  1,
			expectError:   true,
		},
		{
			name:          "equals sign in description value",
			matchPatterns: []string{"test.service"},
			mockStdout: `Id=test.service
Description=Test service with = sign and key=value pairs
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Test service with = sign and key=value pairs", EnableState: "enabled"},
			},
		},
		{
			name:          "unit without trailing blank line",
			matchPatterns: []string{"test.service"},
			mockStdout: `Id=test.service
Description=Test
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Test", EnableState: "enabled"},
			},
		},
		{
			name:          "whitespace and extra blank lines",
			matchPatterns: []string{"test.service"},
			mockStdout: `

Id=test.service
Description=Test
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled


`,
			expected: &[]v1beta1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Test", EnableState: "enabled"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			execMock := executer.NewMockExecuter(ctrl)

			log := log.NewPrefixLogger("test")
			client := client.NewSystemd(execMock, v1beta1.RootUsername)

			m := &manager{
				log:              log,
				client:           client,
				patterns:         tt.matchPatterns,
				excludedServices: make(map[string]struct{}),
			}

			if len(tt.matchPatterns) > 0 {
				args := append([]string{"show", "--all", "--"}, tt.matchPatterns...)
				execMock.EXPECT().ExecuteWithContext(gomock.Any(), gomock.Any(), args).Return(tt.mockStdout, tt.mockStderr, tt.mockExitCode)
			}

			if len(tt.exclusions) > 0 {
				m.AddExclusions(tt.exclusions...)
			}

			status := v1beta1.NewDeviceStatus()
			err := m.Status(context.Background(), &status)

			if tt.expectError {
				require.Error(err)
			} else {
				require.NoError(err)
				require.Equal(tt.expected, status.Systemd)
			}
		})
	}
}

func TestNormalizeEnabledStateValue(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")
	m := &manager{log: log}

	tests := []struct {
		name     string
		input    v1beta1.SystemdEnableStateType
		expected v1beta1.SystemdEnableStateType
	}{
		{
			name:     "valid enabled state",
			input:    v1beta1.SystemdEnableStateEnabled,
			expected: v1beta1.SystemdEnableStateEnabled,
		},
		{
			name:     "valid disabled state",
			input:    v1beta1.SystemdEnableStateDisabled,
			expected: v1beta1.SystemdEnableStateDisabled,
		},
		{
			name:     "valid static state",
			input:    v1beta1.SystemdEnableStateStatic,
			expected: v1beta1.SystemdEnableStateStatic,
		},
		{
			name:     "valid indirect state",
			input:    v1beta1.SystemdEnableStateIndirect,
			expected: v1beta1.SystemdEnableStateIndirect,
		},
		{
			name:     "valid masked state",
			input:    v1beta1.SystemdEnableStateMasked,
			expected: v1beta1.SystemdEnableStateMasked,
		},
		{
			name:     "valid unknown state",
			input:    v1beta1.SystemdEnableStateUnknown,
			expected: v1beta1.SystemdEnableStateUnknown,
		},
		{
			name:     "valid empty state",
			input:    v1beta1.SystemdEnableStateEmpty,
			expected: v1beta1.SystemdEnableStateEmpty,
		},
		{
			name:     "valid alias state",
			input:    v1beta1.SystemdEnableStateAlias,
			expected: v1beta1.SystemdEnableStateAlias,
		},
		{
			name:     "valid bad state",
			input:    v1beta1.SystemdEnableStateBad,
			expected: v1beta1.SystemdEnableStateBad,
		},
		{
			name:     "valid enabled-runtime state",
			input:    v1beta1.SystemdEnableStateEnabledRuntime,
			expected: v1beta1.SystemdEnableStateEnabledRuntime,
		},
		{
			name:     "valid linked state",
			input:    v1beta1.SystemdEnableStateLinked,
			expected: v1beta1.SystemdEnableStateLinked,
		},
		{
			name:     "valid linked-runtime state",
			input:    v1beta1.SystemdEnableStateLinkedRuntime,
			expected: v1beta1.SystemdEnableStateLinkedRuntime,
		},
		{
			name:     "valid masked-runtime state",
			input:    v1beta1.SystemdEnableStateMaskedRuntime,
			expected: v1beta1.SystemdEnableStateMaskedRuntime,
		},
		{
			name:     "valid generated state",
			input:    v1beta1.SystemdEnableStateGenerated,
			expected: v1beta1.SystemdEnableStateGenerated,
		},
		{
			name:     "valid transient state",
			input:    v1beta1.SystemdEnableStateTransient,
			expected: v1beta1.SystemdEnableStateTransient,
		},
		{
			name:     "completely invalid value normalized to unknown",
			input:    v1beta1.SystemdEnableStateType("invalid-value"),
			expected: v1beta1.SystemdEnableStateUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := m.normalizeEnabledStateValue(tt.input)
			require.Equal(tt.expected, result)
		})
	}
}

func TestNormalizeLoadStateValue(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")
	m := &manager{log: log}

	tests := []struct {
		name     string
		input    v1beta1.SystemdLoadStateType
		expected v1beta1.SystemdLoadStateType
	}{
		{
			name:     "valid loaded state",
			input:    v1beta1.SystemdLoadStateLoaded,
			expected: v1beta1.SystemdLoadStateLoaded,
		},
		{
			name:     "valid not-found state",
			input:    v1beta1.SystemdLoadStateNotFound,
			expected: v1beta1.SystemdLoadStateNotFound,
		},
		{
			name:     "valid error state",
			input:    v1beta1.SystemdLoadStateError,
			expected: v1beta1.SystemdLoadStateError,
		},
		{
			name:     "valid masked state",
			input:    v1beta1.SystemdLoadStateMasked,
			expected: v1beta1.SystemdLoadStateMasked,
		},
		{
			name:     "valid unknown state",
			input:    v1beta1.SystemdLoadStateUnknown,
			expected: v1beta1.SystemdLoadStateUnknown,
		},
		{
			name:     "valid stub state",
			input:    v1beta1.SystemdLoadStateStub,
			expected: v1beta1.SystemdLoadStateStub,
		},
		{
			name:     "valid bad-setting state",
			input:    v1beta1.SystemdLoadStateBadSetting,
			expected: v1beta1.SystemdLoadStateBadSetting,
		},
		{
			name:     "valid merged state",
			input:    v1beta1.SystemdLoadStateMerged,
			expected: v1beta1.SystemdLoadStateMerged,
		},
		{
			name:     "completely invalid value normalized to unknown",
			input:    v1beta1.SystemdLoadStateType("invalid-value"),
			expected: v1beta1.SystemdLoadStateUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := m.normalizeLoadStateValue(tt.input)
			require.Equal(tt.expected, result)
		})
	}
}

func TestNormalizeActiveStateValue(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")
	m := &manager{log: log}

	tests := []struct {
		name     string
		input    v1beta1.SystemdActiveStateType
		expected v1beta1.SystemdActiveStateType
	}{
		{
			name:     "valid active state",
			input:    v1beta1.SystemdActiveStateActive,
			expected: v1beta1.SystemdActiveStateActive,
		},
		{
			name:     "valid inactive state",
			input:    v1beta1.SystemdActiveStateInactive,
			expected: v1beta1.SystemdActiveStateInactive,
		},
		{
			name:     "valid activating state",
			input:    v1beta1.SystemdActiveStateActivating,
			expected: v1beta1.SystemdActiveStateActivating,
		},
		{
			name:     "valid deactivating state",
			input:    v1beta1.SystemdActiveStateDeactivating,
			expected: v1beta1.SystemdActiveStateDeactivating,
		},
		{
			name:     "valid failed state",
			input:    v1beta1.SystemdActiveStateFailed,
			expected: v1beta1.SystemdActiveStateFailed,
		},
		{
			name:     "valid unknown state",
			input:    v1beta1.SystemdActiveStateUnknown,
			expected: v1beta1.SystemdActiveStateUnknown,
		},
		{
			name:     "valid reloading state",
			input:    v1beta1.SystemdActiveStateReloading,
			expected: v1beta1.SystemdActiveStateReloading,
		},
		{
			name:     "valid maintenance state",
			input:    v1beta1.SystemdActiveStateMaintenance,
			expected: v1beta1.SystemdActiveStateMaintenance,
		},
		{
			name:     "valid refreshing state",
			input:    v1beta1.SystemdActiveStateRefreshing,
			expected: v1beta1.SystemdActiveStateRefreshing,
		},
		{
			name:     "completely invalid value normalized to unknown",
			input:    v1beta1.SystemdActiveStateType("invalid-value"),
			expected: v1beta1.SystemdActiveStateUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := m.normalizeActiveStateValue(tt.input)
			require.Equal(tt.expected, result)
		})
	}
}
