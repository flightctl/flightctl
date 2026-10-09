package catalogcollector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/stretchr/testify/require"
)

// These are plain Go tests, not Ginkgo specs: they cover the setup and
// teardown decisions the suite makes before and after a cluster is involved,
// so they must run without one. Run them with
//
//	go test ./test/e2e/catalogcollector/ -run 'TestResolveCollectorImage|TestSuiteCleanupSteps'

// fakeChartDeployer is an infra.ChartDeployer that only answers image lookups.
// Any other call is a bug in the code under test.
type fakeChartDeployer struct {
	// present is the set of images the host runtime reports.
	present map[string]bool
	// lookupErr, when set, is returned by every LocalImageExists call.
	lookupErr error
	// lookups records the images that were queried, in order.
	lookups []string
}

func (f *fakeChartDeployer) LocalImageExists(_ context.Context, image string) (bool, error) {
	f.lookups = append(f.lookups, image)
	if f.lookupErr != nil {
		return false, f.lookupErr
	}
	return f.present[image], nil
}

func (f *fakeChartDeployer) EnsureNamespace(context.Context, string) error { panic("not used") }
func (f *fakeChartDeployer) DeleteNamespace(context.Context, string, time.Duration) error {
	panic("not used")
}
func (f *fakeChartDeployer) ApplySecret(context.Context, string, string, map[string]string) error {
	panic("not used")
}
func (f *fakeChartDeployer) ApplyConfigMap(context.Context, string, string, map[string]string) error {
	panic("not used")
}
func (f *fakeChartDeployer) LoadLocalImage(context.Context, string) error { panic("not used") }
func (f *fakeChartDeployer) InstallChart(context.Context, infra.ChartRelease) error {
	panic("not used")
}
func (f *fakeChartDeployer) UninstallChart(context.Context, string, string, time.Duration) error {
	panic("not used")
}
func (f *fakeChartDeployer) ReleaseInstalled(context.Context, string, string) (bool, error) {
	panic("not used")
}
func (f *fakeChartDeployer) WaitForWorkloadGone(context.Context, string, string, time.Duration) error {
	panic("not used")
}
func (f *fakeChartDeployer) WorkloadDiagnostics(context.Context, string, string) string {
	panic("not used")
}

func TestResolveCollectorImage(t *testing.T) {
	const ciImage = "quay.io/flightctl/flightctl-catalog-collector-el9:v0.9.0-123"

	tests := []struct {
		name string
		// env is the environment the run is given.
		env map[string]string
		// deployer is the host runtime state.
		deployer func() *fakeChartDeployer

		wantReference string
		wantPreloaded bool
		wantRequired  bool
		// wantLookups is the host runtime queries that must have happened.
		wantLookups []string
		wantErr     bool
	}{
		{
			name: "When CI passes a preloaded image it should use it without touching the host runtime",
			env: map[string]string{
				collectorImageEnv:          ciImage,
				collectorImagePreloadedEnv: "true",
				collectorImageRequiredEnv:  "true",
			},
			deployer:      func() *fakeChartDeployer { return &fakeChartDeployer{} },
			wantReference: ciImage,
			wantPreloaded: true,
			wantRequired:  true,
			// The CI job loads the image bundle straight into kind, so the
			// host runtime never has it and must not be consulted.
			wantLookups: nil,
		},
		{
			name: "When an image is named without the preloaded flag it should still be loaded from the host runtime",
			env:  map[string]string{collectorImageEnv: "localhost/my-collector:dev"},
			deployer: func() *fakeChartDeployer {
				return &fakeChartDeployer{}
			},
			wantReference: "localhost/my-collector:dev",
			wantPreloaded: false,
			// An explicitly named image means the suite was meant to run.
			wantRequired: true,
			wantLookups:  nil,
		},
		{
			name: "When only the local el9 build is present it should prefer it",
			deployer: func() *fakeChartDeployer {
				return &fakeChartDeployer{present: map[string]bool{collectorImageEL9: true}}
			},
			wantReference: collectorImageEL9,
			wantLookups:   []string{collectorImageEL9},
		},
		{
			name: "When only the un-suffixed local build is present it should fall back to it",
			deployer: func() *fakeChartDeployer {
				return &fakeChartDeployer{present: map[string]bool{collectorImageGeneric: true}}
			},
			wantReference: collectorImageGeneric,
			wantLookups:   []string{collectorImageEL9, collectorImageGeneric},
		},
		{
			name:          "When no image is available locally it should report none and not require one",
			deployer:      func() *fakeChartDeployer { return &fakeChartDeployer{} },
			wantReference: "",
			wantRequired:  false,
			wantLookups:   []string{collectorImageEL9, collectorImageGeneric},
		},
		{
			name:          "When the suite is marked required but no image is available it should say so",
			env:           map[string]string{collectorImageRequiredEnv: "true"},
			deployer:      func() *fakeChartDeployer { return &fakeChartDeployer{} },
			wantReference: "",
			wantRequired:  true,
			wantLookups:   []string{collectorImageEL9, collectorImageGeneric},
		},
		{
			name: "When the container runtime fails it should return the error instead of reporting no image",
			deployer: func() *fakeChartDeployer {
				return &fakeChartDeployer{lookupErr: errors.New("cannot connect to Podman socket")}
			},
			wantLookups: []string{collectorImageEL9},
			wantErr:     true,
		},
		{
			name:        "When the preloaded flag is not a boolean it should fail rather than guess",
			env:         map[string]string{collectorImagePreloadedEnv: "yes-please"},
			deployer:    func() *fakeChartDeployer { return &fakeChartDeployer{} },
			wantLookups: nil,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cleared explicitly so a developer's own environment cannot
			// change the outcome of these tests.
			for _, name := range []string{collectorImageEnv, collectorImagePreloadedEnv, collectorImageRequiredEnv} {
				t.Setenv(name, "")
			}
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			deployer := tt.deployer()
			got, err := resolveCollectorImage(context.Background(), deployer)

			require.Equal(t, tt.wantLookups, deployer.lookups, "unexpected host runtime image lookups")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantReference, got.Reference)
			require.Equal(t, tt.wantPreloaded, got.Preloaded)
			require.Equal(t, tt.wantRequired, got.Required)
		})
	}
}

// TestSuiteCleanupStepsOrder pins the teardown order the suite documents. The
// first two entries carry the only real dependency: the collector has to be
// stopped before the Catalog resources it writes can be deleted for good.
func TestSuiteCleanupStepsOrder(t *testing.T) {
	names := make([]string, 0, len(suiteCleanupSteps()))
	for _, step := range suiteCleanupSteps() {
		names = append(names, step.Name)
	}

	require.Equal(t, []string{
		"stop the collector",
		"delete the catalog resources the collector created",
		"archive the seeded Model Registry resources",
		"delete the suite's AuthProvider",
		"delete the collector namespace",
		"remove the rendered Helm values file",
		"clean up the aux services",
	}, names)
}

// TestSuiteCleanupStepsSkipWhenNothingWasCreated covers the early-skip path: a
// suite that never got past its environment checks must tear down nothing, and
// must not panic reaching into the globals its setup never populated.
func TestSuiteCleanupStepsSkipWhenNothingWasCreated(t *testing.T) {
	// The suite globals are already zero in a plain "go test -run" invocation;
	// this asserts the guards actually cover that state.
	require.Nil(t, providers)
	require.Nil(t, resources)
	require.Nil(t, auxSvcs)

	for _, step := range suiteCleanupSteps() {
		require.NotNil(t, step.Skip, "step %q must declare when it has nothing to do", step.Name)
		require.True(t, step.Skip(), "step %q must be skipped when its setup never ran", step.Name)
	}

	require.NoError(t, infra.RunCleanup(context.Background(), suiteCleanupSteps()))
}
