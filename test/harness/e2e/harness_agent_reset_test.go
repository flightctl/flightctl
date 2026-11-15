package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

func TestResetAgentEnrollmentStateRetriesTransportFailures(t *testing.T) {
	transportErr := fmt.Errorf("ssh failed: %w", exec.Command("sh", "-c", "exit 255").Run())
	scriptErr := exec.Command("sh", "-c", "exit 1").Run()
	for _, tc := range []struct {
		name     string
		failures []error
		calls    int
		wantErr  error
	}{
		{"When SSH disconnects it should reconnect and finish", []error{transportErr, nil}, 2, nil},
		{"When SSH stays unavailable it should stop retrying", []error{transportErr, transportErr, transportErr}, 3, transportErr},
		{"When the remote script fails it should fail immediately", []error{scriptErr}, 1, scriptErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, pauses := 0, 0
			err := resetAgentEnrollmentState(func(string) (*bytes.Buffer, error) {
				if calls >= len(tc.failures) {
					t.Fatal("unexpected retry")
				}
				result := tc.failures[calls]
				calls++
				return nil, result
			}, func(time.Duration) { pauses++ })
			if !errors.Is(err, tc.wantErr) || calls != tc.calls || pauses != tc.calls-1 {
				t.Fatalf("got error %v, calls %d, pauses %d", err, calls, pauses)
			}
		})
	}
}
