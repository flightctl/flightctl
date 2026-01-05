// Copyright 2025 Red Hat, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCheckServiceStatus(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name         string
		mockStdout   string
		mockStderr   string
		mockExitCode int
		expectError  bool
		errorMsg     string
	}{
		{
			name: "service enabled and active",
			mockStdout: `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
`,
			expectError: false,
		},
		{
			name: "service enabled and active with status text",
			mockStdout: `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
StatusText=Connected to server
`,
			expectError: false,
		},
		{
			name: "service enabled and reloading",
			mockStdout: `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=reloading
SubState=running
UnitFileState=enabled
`,
			expectError: false,
		},
		{
			name: "service not enabled - exits successfully",
			mockStdout: `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=inactive
SubState=dead
UnitFileState=disabled
`,
			expectError: false,
		},
		{
			name: "service enabled but inactive",
			mockStdout: `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=inactive
SubState=dead
UnitFileState=enabled
`,
			expectError: true,
			errorMsg:    "not active",
		},
		{
			name: "service enabled but failed",
			mockStdout: `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=failed
SubState=failed
UnitFileState=enabled
`,
			expectError: true,
			errorMsg:    "has failed",
		},
		{
			name:         "systemctl command failure",
			mockStderr:   "Failed to get properties: Connection refused",
			mockExitCode: 1,
			expectError:  true,
			errorMsg:     "getting service status",
		},
		{
			name:        "service not found",
			mockStdout:  "",
			expectError: true,
			errorMsg:    "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			execMock := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}

			systemdClient := client.NewSystemd(execMock)
			checker := NewChecker(
				logger,
				WithVerbose(true),
				WithOutput(output),
				WithSystemdClient(systemdClient),
			)

			// Set up mock expectation for systemctl show
			execMock.EXPECT().
				ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
				Return(tt.mockStdout, tt.mockStderr, tt.mockExitCode)

			err := checker.checkServiceStatus(context.Background())

			if tt.expectError {
				require.Error(err)
				require.Contains(err.Error(), tt.errorMsg)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestReportConnectivityStatus(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name           string
		props          map[string]string
		expectedOutput string
	}{
		{
			name:           "status text present",
			props:          map[string]string{"StatusText": "Connected to server"},
			expectedOutput: "Agent status: Connected to server",
		},
		{
			name:           "status text empty",
			props:          map[string]string{"StatusText": ""},
			expectedOutput: "unknown (agent has not reported status)",
		},
		{
			name:           "status text missing",
			props:          map[string]string{},
			expectedOutput: "unknown (agent has not reported status)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}

			checker := NewChecker(
				logger,
				WithVerbose(true),
				WithOutput(output),
			)

			checker.reportConnectivityStatus(tt.props)
			require.Contains(output.String(), tt.expectedOutput)
		})
	}
}

func TestRun(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name         string
		mockStdout   string
		mockStderr   string
		mockExitCode int
		expectError  bool
	}{
		{
			name: "all checks pass",
			mockStdout: `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
`,
			expectError: false,
		},
		{
			name: "service check fails",
			mockStdout: `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=failed
SubState=failed
UnitFileState=enabled
`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			execMock := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}

			checker := NewChecker(
				logger,
				WithVerbose(true),
				WithOutput(output),
				WithSystemdClient(client.NewSystemd(execMock)),
			)

			execMock.EXPECT().
				ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
				Return(tt.mockStdout, tt.mockStderr, tt.mockExitCode)

			err := checker.Run(context.Background())

			if tt.expectError {
				require.Error(err)
			} else {
				require.NoError(err)
				require.Contains(output.String(), "All health checks passed")
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	checker := NewChecker(logger)

	require.Equal(30*time.Second, checker.timeout)
	require.NotNil(checker.output)
	require.NotNil(checker.systemd)
	require.False(checker.verbose)
}

func TestNewWithOptions(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	output := &bytes.Buffer{}
	execMock := executer.NewMockExecuter(ctrl)
	customSystemd := client.NewSystemd(execMock)

	checker := NewChecker(
		logger,
		WithTimeout(60*time.Second),
		WithVerbose(true),
		WithOutput(output),
		WithSystemdClient(customSystemd),
	)

	require.Equal(60*time.Second, checker.timeout)
	require.True(checker.verbose)
	require.Equal(output, checker.output)
	require.Equal(customSystemd, checker.systemd)
}
