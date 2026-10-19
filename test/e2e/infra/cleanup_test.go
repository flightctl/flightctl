package infra

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingStep builds a CleanupStep that appends its name to ran and then
// returns err.
func recordingStep(t *testing.T, ran *[]string, name string, err error) CleanupStep {
	t.Helper()
	return CleanupStep{
		Name: name,
		Run: func(context.Context) error {
			*ran = append(*ran, name)
			return err
		},
	}
}

func TestRunCleanup(t *testing.T) {
	uninstallErr := errors.New("helm uninstall timed out")
	archiveErr := errors.New("model registry rejected the archive")

	tests := []struct {
		name  string
		steps func(t *testing.T, ran *[]string) []CleanupStep

		// wantRan is the ordered list of steps that must have executed.
		wantRan []string
		// wantErrs are errors the joined result must wrap.
		wantErrs []error
		// wantErrContains are substrings the joined message must name, so a
		// failure report points at the step that left state behind.
		wantErrContains []string
	}{
		{
			name: "When every step succeeds it should run them in order and return nil",
			steps: func(t *testing.T, ran *[]string) []CleanupStep {
				return []CleanupStep{
					recordingStep(t, ran, "stop the collector", nil),
					recordingStep(t, ran, "delete the catalog resources", nil),
					recordingStep(t, ran, "archive the registry resources", nil),
				}
			},
			wantRan: []string{"stop the collector", "delete the catalog resources", "archive the registry resources"},
		},
		{
			name: "When the uninstall step fails it should still run every remaining step",
			steps: func(t *testing.T, ran *[]string) []CleanupStep {
				return []CleanupStep{
					recordingStep(t, ran, "stop the collector", uninstallErr),
					recordingStep(t, ran, "delete the catalog resources", nil),
					recordingStep(t, ran, "archive the registry resources", nil),
					recordingStep(t, ran, "delete the authprovider", nil),
					recordingStep(t, ran, "delete the namespace", nil),
					recordingStep(t, ran, "clean up the aux services", nil),
				}
			},
			wantRan: []string{
				"stop the collector",
				"delete the catalog resources",
				"archive the registry resources",
				"delete the authprovider",
				"delete the namespace",
				"clean up the aux services",
			},
			wantErrs:        []error{uninstallErr},
			wantErrContains: []string{"stop the collector"},
		},
		{
			name: "When several steps fail it should report all of them",
			steps: func(t *testing.T, ran *[]string) []CleanupStep {
				return []CleanupStep{
					recordingStep(t, ran, "stop the collector", uninstallErr),
					recordingStep(t, ran, "archive the registry resources", archiveErr),
					recordingStep(t, ran, "delete the namespace", nil),
				}
			},
			wantRan:         []string{"stop the collector", "archive the registry resources", "delete the namespace"},
			wantErrs:        []error{uninstallErr, archiveErr},
			wantErrContains: []string{"stop the collector", "archive the registry resources"},
		},
		{
			name: "When a step is skipped it should not run and should not fail the cleanup",
			steps: func(t *testing.T, ran *[]string) []CleanupStep {
				skipped := recordingStep(t, ran, "delete the namespace", errors.New("must not run"))
				skipped.Skip = func() bool { return true }
				return []CleanupStep{
					recordingStep(t, ran, "stop the collector", nil),
					skipped,
					recordingStep(t, ran, "clean up the aux services", nil),
				}
			},
			wantRan: []string{"stop the collector", "clean up the aux services"},
		},
		{
			name: "When a step has no action it should be reported instead of silently ignored",
			steps: func(t *testing.T, ran *[]string) []CleanupStep {
				return []CleanupStep{
					{Name: "delete the namespace"},
					recordingStep(t, ran, "clean up the aux services", nil),
				}
			},
			wantRan:         []string{"clean up the aux services"},
			wantErrContains: []string{"delete the namespace", "has no action"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran []string

			err := RunCleanup(context.Background(), tt.steps(t, &ran))

			// Nonfatal: which steps ran and what the joined error says are
			// independent facts about the same call, and a run that reports
			// both at once localises the regression in one pass.
			assert.Equal(t, tt.wantRan, ran, "cleanup must run the expected steps, in order")
			if len(tt.wantErrs) == 0 && len(tt.wantErrContains) == 0 {
				assert.NoError(t, err)
				return
			}
			// Fatal: the content checks below dereference err.
			require.Error(t, err)
			for _, wantErr := range tt.wantErrs {
				assert.ErrorIs(t, err, wantErr, "the joined error must keep every underlying failure")
			}
			for _, substring := range tt.wantErrContains {
				assert.Contains(t, err.Error(), substring)
			}
		})
	}
}

// TestRunCleanupContinuesAfterContextExpiry documents that an expired context
// is handed to every step rather than ending the sequence: steps that can still
// make progress (local file removal, in-process state) must get their turn.
func TestRunCleanupContinuesAfterContextExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var ran []string
	err := RunCleanup(ctx, []CleanupStep{
		{
			Name: "stop the collector",
			Run: func(ctx context.Context) error {
				ran = append(ran, "stop the collector")
				return ctx.Err()
			},
		},
		recordingStep(t, &ran, "remove the rendered values file", nil),
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"stop the collector", "remove the rendered values file"}, ran)
}
