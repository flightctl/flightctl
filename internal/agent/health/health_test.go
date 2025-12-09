// Copyright 2025 Red Hat, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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

func TestCheckConnectivity(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name            string
		serverURL       string
		serverHandler   http.HandlerFunc
		expectReachable bool
		expectWarning   bool
	}{
		{
			name:      "no server URL configured",
			serverURL: "",
		},
		{
			name: "server reachable",
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			expectReachable: true,
		},
		{
			name: "server returns error status - still reachable",
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			expectReachable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}
			execMock := executer.NewMockExecuter(ctrl)

			serverURL := tt.serverURL
			if tt.serverHandler != nil {
				server := httptest.NewServer(tt.serverHandler)
				defer server.Close()
				serverURL = server.URL
			}

			checker := NewChecker(
				logger,
				WithVerbose(true),
				WithOutput(output),
				WithServerURL(serverURL),
				WithSystemdClient(client.NewSystemd(execMock)),
			)

			checker.checkConnectivity(context.Background())

			if tt.expectReachable {
				require.Contains(output.String(), "reachable")
			}
		})
	}
}

func TestCheckConnectivityUnreachable(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	output := &bytes.Buffer{}
	execMock := executer.NewMockExecuter(ctrl)

	checker := NewChecker(
		logger,
		WithVerbose(true),
		WithOutput(output),
		WithServerURL("http://192.0.2.1:9999"), // Non-routable IP
		WithTimeout(1*time.Second),
		WithSystemdClient(client.NewSystemd(execMock)),
	)

	// This should warn but not fail
	checker.checkConnectivity(context.Background())
	require.Contains(output.String(), "WARNING")
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
	require.Empty(checker.serverURL)
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
		WithServerURL("https://example.com"),
		WithSystemdClient(customSystemd),
	)

	require.Equal(60*time.Second, checker.timeout)
	require.True(checker.verbose)
	require.Equal(output, checker.output)
	require.Equal("https://example.com", checker.serverURL)
	require.Equal(customSystemd, checker.systemd)
}
