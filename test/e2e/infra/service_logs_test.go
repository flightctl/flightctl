package infra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const observerTestMessage = "undefined environment variable: TEST_MISSING_TOKEN"

type observerLifecycle struct {
	ServiceLifecycleProvider
	readyChecks int
	calls       int
}

type observerLogs struct {
	output string
	err    error
}

func TestServiceFailureObserver(t *testing.T) {
	tests := []struct {
		name        string
		readyChecks int
		output      string
		logErr      error
		wantSuccess bool
	}{
		{"When startup fails it should return the expected log", 0, observerTestMessage, nil, true},
		{"When initially ready it should wait for startup failure", 1, observerTestMessage, nil, true},
		{"When readiness persists it should fail even with matching logs", 100, observerTestMessage, nil, false},
		{"When a different error is logged it should not pass", 0, "unrelated startup error", nil, false},
		{"When logs cannot be read it should report the retrieval error", 0, "", errors.New("log stream unavailable"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := &observerLifecycle{readyChecks: tt.readyChecks}
			logs := &observerLogs{output: tt.output, err: tt.logErr}
			output, err := ServiceFailureObserver(context.Background(), lifecycle, logs, ServiceTelemetryGateway, 2*serviceFailurePollInterval, 10, observerTestMessage)
			if tt.wantSuccess {
				require.NoError(t, err)
				require.Contains(t, output, observerTestMessage)
				require.Equal(t, tt.readyChecks+1, lifecycle.calls)
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				if tt.logErr != nil {
					require.ErrorContains(t, err, tt.logErr.Error())
				}
			}
		})
	}
}

func TestServiceFailureObserverCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lifecycle := &observerLifecycle{}
	_, err := ServiceFailureObserver(ctx, lifecycle, &observerLogs{}, ServiceTelemetryGateway, time.Second, 10, observerTestMessage)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, lifecycle.calls)
}

// WaitForReady simulates a briefly ready container followed by startup failure.
func (l *observerLifecycle) WaitForReady(ServiceName, time.Duration) error {
	l.calls++
	if l.calls <= l.readyChecks {
		return nil
	}
	return errors.New("service is not ready")
}

// GetServiceLogs supplies deterministic startup output without a running cluster.
func (l *observerLogs) GetServiceLogs(context.Context, ServiceName, int) (string, error) {
	return l.output, l.err
}
