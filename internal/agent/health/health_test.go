package health

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	activeServiceOutput = `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
`
	activeServiceWithStatusOutput = `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
StatusText=Connected to server
`
	disabledServiceOutput = `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=inactive
SubState=dead
UnitFileState=disabled
`
	activatingServiceOutput = `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=activating
SubState=start
UnitFileState=enabled
`
	failedServiceOutput = `Id=flightctl-agent.service
Description=Flight Control Agent
LoadState=loaded
ActiveState=failed
SubState=failed
UnitFileState=enabled
`
)

func TestWaitForServiceActive(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		setupMocks  func(*executer.MockExecuter)
		expectError bool
		errorMsg    string
	}{
		{
			name: "service immediately active",
			setupMocks: func(m *executer.MockExecuter) {
				m.EXPECT().
					ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
					Return(activeServiceOutput, "", 0)
			},
			expectError: false,
		},
		{
			name: "service disabled - fails health check",
			setupMocks: func(m *executer.MockExecuter) {
				m.EXPECT().
					ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
					Return(disabledServiceOutput, "", 0)
			},
			expectError: true,
			errorMsg:    "not enabled",
		},
		{
			name: "service failed",
			setupMocks: func(m *executer.MockExecuter) {
				m.EXPECT().
					ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
					Return(failedServiceOutput, "", 0)
			},
			expectError: true,
			errorMsg:    "has failed",
		},
		{
			name: "service becomes active after polling",
			setupMocks: func(m *executer.MockExecuter) {
				gomock.InOrder(
					m.EXPECT().
						ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
						Return(activatingServiceOutput, "", 0),
					m.EXPECT().
						ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
						Return(activeServiceOutput, "", 0),
				)
			},
			expectError: false,
		},
		{
			name: "systemctl command failure retries",
			setupMocks: func(m *executer.MockExecuter) {
				gomock.InOrder(
					m.EXPECT().
						ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
						Return("", "connection refused", 1),
					m.EXPECT().
						ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
						Return(activeServiceOutput, "", 0),
				)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			execMock := executer.NewMockExecuter(ctrl)
			tt.setupMocks(execMock)

			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}

			checker := NewChecker(
				logger,
				WithTimeout(30*time.Second),
				WithVerbose(true),
				WithOutput(output),
				WithSystemdClient(client.NewSystemd(execMock, v1beta1.RootUsername)),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			err := checker.waitForServiceActive(ctx)

			if tt.expectError {
				require.Error(err)
				require.Contains(err.Error(), tt.errorMsg)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestMonitorStability(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		setupMocks  func(*executer.MockExecuter)
		expectError bool
		errorMsg    string
	}{
		{
			name: "service remains stable",
			setupMocks: func(m *executer.MockExecuter) {
				// Called multiple times during stability window
				m.EXPECT().
					ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
					Return(activeServiceOutput, "", 0).
					MinTimes(2)
			},
			expectError: false,
		},
		{
			name: "service fails during stability window",
			setupMocks: func(m *executer.MockExecuter) {
				gomock.InOrder(
					m.EXPECT().
						ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
						Return(activeServiceOutput, "", 0),
					m.EXPECT().
						ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
						Return(failedServiceOutput, "", 0),
				)
			},
			expectError: true,
			errorMsg:    "failed during stability window",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			execMock := executer.NewMockExecuter(ctrl)
			tt.setupMocks(execMock)

			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}

			checker := NewChecker(
				logger,
				WithTimeout(30*time.Second),
				WithStabilityWindow(10*time.Second), // Short window for testing
				WithVerbose(true),
				WithOutput(output),
				WithSystemdClient(client.NewSystemd(execMock, v1beta1.RootUsername)),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err := checker.monitorStability(ctx)

			if tt.expectError {
				require.Error(err)
				require.Contains(err.Error(), tt.errorMsg)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestRun(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		setupMocks  func(*executer.MockExecuter)
		expectError bool
		errorMsg    string
	}{
		{
			name: "all checks pass",
			setupMocks: func(m *executer.MockExecuter) {
				m.EXPECT().
					ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
					Return(activeServiceOutput, "", 0).
					MinTimes(2) // At least once for active check, once+ for stability
			},
			expectError: false,
		},
		{
			name: "service disabled - fails health check",
			setupMocks: func(m *executer.MockExecuter) {
				m.EXPECT().
					ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
					Return(disabledServiceOutput, "", 0)
			},
			expectError: true,
			errorMsg:    "not enabled",
		},
		{
			name: "service fails during stability",
			setupMocks: func(m *executer.MockExecuter) {
				gomock.InOrder(
					// waitForServiceActive succeeds
					m.EXPECT().
						ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
						Return(activeServiceOutput, "", 0),
					// Service fails during stability window
					m.EXPECT().
						ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "show", "--all", "--", serviceName).
						Return(failedServiceOutput, "", 0),
				)
			},
			expectError: true,
			errorMsg:    "failed during stability window",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			execMock := executer.NewMockExecuter(ctrl)
			tt.setupMocks(execMock)

			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}

			checker := NewChecker(
				logger,
				WithTimeout(30*time.Second),
				WithStabilityWindow(10*time.Second),
				WithVerbose(true),
				WithOutput(output),
				WithSystemdClient(client.NewSystemd(execMock, v1beta1.RootUsername)),
			)

			err := checker.Run(context.Background())

			if tt.expectError {
				require.Error(err)
				require.Contains(err.Error(), tt.errorMsg)
			} else {
				require.NoError(err)
				require.Contains(output.String(), "All health checks passed")
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

func TestNewDefaults(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	checker := NewChecker(logger)

	require.Equal(150*time.Second, checker.timeout)
	require.Equal(defaultStabilityWindow, checker.stabilityWindow)
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
	customSystemd := client.NewSystemd(execMock, v1beta1.RootUsername)

	checker := NewChecker(
		logger,
		WithTimeout(60*time.Second),
		WithStabilityWindow(30*time.Second),
		WithVerbose(true),
		WithOutput(output),
		WithSystemdClient(customSystemd),
	)

	require.Equal(60*time.Second, checker.timeout)
	require.Equal(30*time.Second, checker.stabilityWindow)
	require.True(checker.verbose)
	require.Equal(output, checker.output)
	require.Equal(customSystemd, checker.systemd)
}
