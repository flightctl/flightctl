package systemd

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
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
		expected      *[]v1alpha1.SystemdUnitStatus
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
			expected: &[]v1alpha1.SystemdUnitStatus{
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
			expected: &[]v1alpha1.SystemdUnitStatus{
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
ActiveState=listening
SubState=listening
UnitFileState=static

Id=test.timer
Description=Test timer
LoadState=loaded
ActiveState=active
SubState=waiting
UnitFileState=enabled
`,
			expected: &[]v1alpha1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Test service", EnableState: "enabled"},
				{Unit: "test.socket", LoadState: "loaded", ActiveState: "listening", SubState: "listening", Description: "Test socket", EnableState: "static"},
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
ActiveState=listening
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
			expected: &[]v1alpha1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Test service", EnableState: "enabled"},
				{Unit: "test.socket", LoadState: "loaded", ActiveState: "listening", SubState: "listening", Description: "Test socket", EnableState: "static"},
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
			expected: &[]v1alpha1.SystemdUnitStatus{
				{Unit: "test1.service", LoadState: "loaded", ActiveState: "active", SubState: "exited", Description: "One-shot service", EnableState: "enabled"},
				{Unit: "test2.service", LoadState: "loaded", ActiveState: "activating", SubState: "start-pre", Description: "Starting service", EnableState: "disabled"},
				{Unit: "test3.service", LoadState: "loaded", ActiveState: "inactive", SubState: "dead", Description: "Stopped service", EnableState: "masked"},
			},
		},
		{
			name:          "unknown/future enum values",
			matchPatterns: []string{"test.service"},
			mockStdout: `Id=test.service
Description=Future systemd version
LoadState=future-load-state
ActiveState=future-active-state
SubState=future-sub-state
UnitFileState=future-enable-state
`,
			expected: &[]v1alpha1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "future-load-state", ActiveState: "future-active-state", SubState: "future-sub-state", Description: "Future systemd version", EnableState: "future-enable-state"},
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
			expected: &[]v1alpha1.SystemdUnitStatus{
				{Unit: "nonexistent.service", LoadState: "", ActiveState: "", SubState: "", Description: "", EnableState: ""},
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
			expected: &[]v1alpha1.SystemdUnitStatus{
				{Unit: "test.service", LoadState: "loaded", ActiveState: "active", SubState: "running", Description: "Service with \"quotes\" and unicode: 日本語", EnableState: "enabled"},
			},
		},
		{
			name:          "empty output - no matching units",
			matchPatterns: []string{"nonexistent*.service"},
			mockStdout:    ``,
			expected:      &[]v1alpha1.SystemdUnitStatus{},
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
			expected: &[]v1alpha1.SystemdUnitStatus{
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
			expected: &[]v1alpha1.SystemdUnitStatus{
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
			expected: &[]v1alpha1.SystemdUnitStatus{
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
			client := client.NewSystemd(execMock)

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

			status := v1alpha1.NewDeviceStatus()
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
