// Copyright 2025 Red Hat, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

// mockSystemdClient is a mock implementation of SystemdClient for testing.
type mockSystemdClient struct {
	enabled     bool
	enabledErr  error
	active      bool
	activeErr   error
	closeCalled bool
}

func (m *mockSystemdClient) Close() {
	m.closeCalled = true
}

func (m *mockSystemdClient) IsServiceEnabled(_ context.Context, _ string) (bool, error) {
	return m.enabled, m.enabledErr
}

func (m *mockSystemdClient) IsServiceActive(_ context.Context, _ string) (bool, error) {
	return m.active, m.activeErr
}

func TestChecker_checkServiceStatus(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name        string
		mock        *mockSystemdClient
		expectError bool
		errorMsg    string
	}{
		{
			name: "service enabled and active",
			mock: &mockSystemdClient{
				enabled: true,
				active:  true,
			},
			expectError: false,
		},
		{
			name: "service not enabled - exits successfully",
			mock: &mockSystemdClient{
				enabled: false,
				active:  false,
			},
			expectError: false,
		},
		{
			name: "service enabled but not active",
			mock: &mockSystemdClient{
				enabled: true,
				active:  false,
			},
			expectError: true,
			errorMsg:    "service is not active",
		},
		{
			name: "service enabled but failed",
			mock: &mockSystemdClient{
				enabled:   true,
				activeErr: fmt.Errorf("service %s has failed", serviceName),
			},
			expectError: true,
			errorMsg:    "has failed",
		},
		{
			name: "error checking enabled status",
			mock: &mockSystemdClient{
				enabledErr: fmt.Errorf("D-Bus connection failed"),
			},
			expectError: true,
			errorMsg:    "D-Bus connection failed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}

			checker := New(
				logger,
				WithVerbose(true),
				WithOutput(output),
				WithSystemdFactory(func(_ context.Context) (SystemdClient, error) {
					return tc.mock, nil
				}),
			)

			err := checker.checkServiceStatus(context.Background())

			if tc.expectError {
				require.Error(err)
				require.Contains(err.Error(), tc.errorMsg)
			} else {
				require.NoError(err)
			}
			require.True(tc.mock.closeCalled, "Close() should be called")
		})
	}
}

func TestChecker_checkServiceStatus_FactoryError(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	checker := New(
		logger,
		WithSystemdFactory(func(_ context.Context) (SystemdClient, error) {
			return nil, fmt.Errorf("failed to connect to D-Bus")
		}),
	)

	err := checker.checkServiceStatus(context.Background())
	require.Error(err)
	require.Contains(err.Error(), "failed to connect to D-Bus")
}

func TestChecker_checkConnectivity(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name           string
		serverURL      string
		serverHandler  http.HandlerFunc
		expectWarning  bool
		expectReachable bool
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
			name: "server returns error status",
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			expectReachable: true, // Still reachable, just returns error
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}

			serverURL := tc.serverURL
			if tc.serverHandler != nil {
				server := httptest.NewServer(tc.serverHandler)
				defer server.Close()
				serverURL = server.URL
			}

			// Use mock systemd to avoid D-Bus dependency
			mockSystemd := &mockSystemdClient{enabled: true, active: true}
			checker := New(
				logger,
				WithVerbose(true),
				WithOutput(output),
				WithServerURL(serverURL),
				WithSystemdFactory(func(_ context.Context) (SystemdClient, error) {
					return mockSystemd, nil
				}),
			)

			checker.checkConnectivity(context.Background())

			if tc.expectReachable {
				require.Contains(output.String(), "reachable")
			}
		})
	}
}

func TestChecker_checkConnectivity_Unreachable(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")
	output := &bytes.Buffer{}

	checker := New(
		logger,
		WithVerbose(true),
		WithOutput(output),
		WithServerURL("http://192.0.2.1:9999"), // Non-routable IP
		WithTimeout(1*time.Second),
	)

	// This should warn but not fail
	checker.checkConnectivity(context.Background())
	require.Contains(output.String(), "WARNING")
}

func TestChecker_Run(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name        string
		mock        *mockSystemdClient
		expectError bool
	}{
		{
			name: "all checks pass",
			mock: &mockSystemdClient{
				enabled: true,
				active:  true,
			},
			expectError: false,
		},
		{
			name: "service check fails",
			mock: &mockSystemdClient{
				enabled:   true,
				activeErr: fmt.Errorf("service has failed"),
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger := log.NewPrefixLogger("test")
			output := &bytes.Buffer{}

			checker := New(
				logger,
				WithVerbose(true),
				WithOutput(output),
				WithSystemdFactory(func(_ context.Context) (SystemdClient, error) {
					return tc.mock, nil
				}),
			)

			err := checker.Run(context.Background())

			if tc.expectError {
				require.Error(err)
			} else {
				require.NoError(err)
				require.Contains(output.String(), "All health checks passed")
			}
		})
	}
}

func TestNew_Defaults(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")

	checker := New(logger)

	require.Equal(30*time.Second, checker.timeout)
	require.NotNil(checker.output)
	require.NotNil(checker.systemdFactory)
	require.False(checker.verbose)
	require.False(checker.greenbootMode)
	require.Empty(checker.serverURL)
}

func TestNew_WithOptions(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")
	output := &bytes.Buffer{}

	checker := New(
		logger,
		WithTimeout(60*time.Second),
		WithVerbose(true),
		WithOutput(output),
		WithServerURL("https://example.com"),
		WithGreenbootMode(true),
	)

	require.Equal(60*time.Second, checker.timeout)
	require.True(checker.verbose)
	require.Equal(output, checker.output)
	require.Equal("https://example.com", checker.serverURL)
	require.True(checker.greenbootMode)
}

