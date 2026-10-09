package infra

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingStep builds a CleanupStep that appends its name to ran and then
// returns err.
func recordingStep(ran *[]string, name string, err error) CleanupStep {
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
		steps func(ran *[]string) []CleanupStep

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
			steps: func(ran *[]string) []CleanupStep {
				return []CleanupStep{
					recordingStep(ran, "stop the collector", nil),
					recordingStep(ran, "delete the catalog resources", nil),
					recordingStep(ran, "archive the registry resources", nil),
				}
			},
			wantRan: []string{"stop the collector", "delete the catalog resources", "archive the registry resources"},
		},
		{
			name: "When the uninstall step fails it should still run every remaining step",
			steps: func(ran *[]string) []CleanupStep {
				return []CleanupStep{
					recordingStep(ran, "stop the collector", uninstallErr),
					recordingStep(ran, "delete the catalog resources", nil),
					recordingStep(ran, "archive the registry resources", nil),
					recordingStep(ran, "delete the authprovider", nil),
					recordingStep(ran, "delete the namespace", nil),
					recordingStep(ran, "clean up the aux services", nil),
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
			steps: func(ran *[]string) []CleanupStep {
				return []CleanupStep{
					recordingStep(ran, "stop the collector", uninstallErr),
					recordingStep(ran, "archive the registry resources", archiveErr),
					recordingStep(ran, "delete the namespace", nil),
				}
			},
			wantRan:         []string{"stop the collector", "archive the registry resources", "delete the namespace"},
			wantErrs:        []error{uninstallErr, archiveErr},
			wantErrContains: []string{"stop the collector", "archive the registry resources"},
		},
		{
			name: "When a step is skipped it should not run and should not fail the cleanup",
			steps: func(ran *[]string) []CleanupStep {
				skipped := recordingStep(ran, "delete the namespace", errors.New("must not run"))
				skipped.Skip = func() bool { return true }
				return []CleanupStep{
					recordingStep(ran, "stop the collector", nil),
					skipped,
					recordingStep(ran, "clean up the aux services", nil),
				}
			},
			wantRan: []string{"stop the collector", "clean up the aux services"},
		},
		{
			name: "When a step has no action it should be reported instead of silently ignored",
			steps: func(ran *[]string) []CleanupStep {
				return []CleanupStep{
					{Name: "delete the namespace"},
					recordingStep(ran, "clean up the aux services", nil),
				}
			},
			wantRan:         []string{"clean up the aux services"},
			wantErrContains: []string{"delete the namespace", "has no action"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran []string

			err := RunCleanup(context.Background(), tt.steps(&ran))

			require.Equal(t, tt.wantRan, ran, "cleanup must run the expected steps, in order")
			if len(tt.wantErrs) == 0 && len(tt.wantErrContains) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, wantErr := range tt.wantErrs {
				require.ErrorIs(t, err, wantErr, "the joined error must keep every underlying failure")
			}
			for _, substring := range tt.wantErrContains {
				require.Contains(t, err.Error(), substring)
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
		recordingStep(&ran, "remove the rendered values file", nil),
	})

	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"stop the collector", "remove the rendered values file"}, ran)
}
