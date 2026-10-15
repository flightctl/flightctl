package catalogcollector_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/stretchr/testify/require"
)

// These are plain Go tests, not Ginkgo specs: they cover the setup and
// teardown decisions the suite makes before and after a cluster is involved,
// so they must run without one. Run them with
//
//	go test ./test/e2e/catalogcollector/ -run 'TestResolveCollectorImage|TestSuiteCleanupSteps|TestStopCollector'
//
// They share a process, and a set of package-level variables, with the Ginkgo
// suite: a normal e2e invocation runs TestCatalogCollector first and leaves
// those variables populated. Every test below that depends on them therefore
// installs its own fixture through withSuiteState rather than assuming a zero
// value, so the result is the same either way.

// suiteState is the package-level state the suite's setup populates and its
// teardown consumes.
type suiteState struct {
	providers        *infra.Providers
	auxSvcs          *auxiliary.Services
	resources        *testResources
	suiteHarness     *e2e.Harness
	smokeModel       *seededModel
	seededModels     []*seededModel
	valuesFilePath   string
	chartInstalled   bool
	authProviderSet  bool
	collectorStopped bool
}

// captureSuiteState snapshots the suite variables as they are now.
func captureSuiteState() suiteState {
	return suiteState{
		providers:        providers,
		auxSvcs:          auxSvcs,
		resources:        resources,
		suiteHarness:     suiteHarness,
		smokeModel:       smokeModel,
		seededModels:     seededModels,
		valuesFilePath:   valuesFilePath,
		chartInstalled:   chartInstalled,
		authProviderSet:  authProviderSet,
		collectorStopped: collectorStopped,
	}
}

// applySuiteState writes a snapshot back over the suite variables.
func applySuiteState(state suiteState) {
	providers = state.providers
	auxSvcs = state.auxSvcs
	resources = state.resources
	suiteHarness = state.suiteHarness
	smokeModel = state.smokeModel
	seededModels = state.seededModels
	valuesFilePath = state.valuesFilePath
	chartInstalled = state.chartInstalled
	authProviderSet = state.authProviderSet
	collectorStopped = state.collectorStopped
}

// withSuiteState installs state for the duration of the test and restores
// whatever was there before, so these tests neither depend on nor disturb the
// Ginkgo suite that shares the same variables.
func withSuiteState(t *testing.T, state suiteState) {
	t.Helper()
	previous := captureSuiteState()
	t.Cleanup(func() { applySuiteState(previous) })
	applySuiteState(state)
}

// fakeChartDeployer is an infra.ChartDeployer covering the two cluster
// interactions these tests exercise: resolving the collector image, and the
// teardown sequence that stops the collector. Every other call is a bug in the
// code under test.
type fakeChartDeployer struct {
	// present is the set of images the host runtime reports.
	present map[string]bool
	// lookupErr, when set, is returned by every LocalImageExists call.
	lookupErr error
	// lookups records the images that were queried, in order.
	lookups []string

	// The teardown methods return these, so a test can fail any individual
	// step of stopping the collector.
	uninstallErr          error
	releaseStillInstalled bool
	releaseInstalledErr   error
	workloadGoneErr       error
	deleteNamespaceErr    error
	// calls records the teardown operations performed, in order.
	calls []string
}

func (f *fakeChartDeployer) LocalImageExists(_ context.Context, image string) (bool, error) {
	f.lookups = append(f.lookups, image)
	if f.lookupErr != nil {
		return false, f.lookupErr
	}
	return f.present[image], nil
}

func (f *fakeChartDeployer) UninstallChart(_ context.Context, name, namespace string, _ time.Duration) error {
	f.calls = append(f.calls, fmt.Sprintf("uninstall %s/%s", namespace, name))
	return f.uninstallErr
}

func (f *fakeChartDeployer) ReleaseInstalled(_ context.Context, name, namespace string) (bool, error) {
	f.calls = append(f.calls, fmt.Sprintf("release-installed %s/%s", namespace, name))
	return f.releaseStillInstalled, f.releaseInstalledErr
}

func (f *fakeChartDeployer) WaitForWorkloadGone(_ context.Context, namespace, _ string, _ time.Duration) error {
	f.calls = append(f.calls, fmt.Sprintf("wait-workload-gone %s", namespace))
	return f.workloadGoneErr
}

func (f *fakeChartDeployer) DeleteNamespace(_ context.Context, namespace string, _ time.Duration) error {
	f.calls = append(f.calls, fmt.Sprintf("delete-namespace %s", namespace))
	return f.deleteNamespaceErr
}

func (f *fakeChartDeployer) EnsureNamespace(context.Context, string) error { panic("not used") }
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
func (f *fakeChartDeployer) WorkloadDiagnostics(context.Context, string, string) string {
	panic("not used")
}
func (f *fakeChartDeployer) WorkloadLogs(context.Context, string, string, int64) (string, error) {
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
// must not panic reaching into the variables its setup never populated.
func TestSuiteCleanupStepsSkipWhenNothingWasCreated(t *testing.T) {
	// The state a suite that skipped in BeforeSuite leaves behind. It is
	// installed rather than assumed: under the normal e2e invocation the
	// Ginkgo suite has already run in this process and populated these
	// variables, so the zero value is not what this test would otherwise find.
	withSuiteState(t, suiteState{})

	for _, step := range suiteCleanupSteps() {
		require.NotNil(t, step.Skip, "step %q must declare when it has nothing to do", step.Name)
		require.True(t, step.Skip(), "step %q must be skipped when its setup never ran", step.Name)
	}

	require.NoError(t, infra.RunCleanup(context.Background(), suiteCleanupSteps()))
}

// installedSuiteState is the state a suite has once BeforeSuite completed: the
// chart is installed and the collector has not been stopped yet.
func installedSuiteState(t *testing.T, charts infra.ChartDeployer) suiteState {
	t.Helper()
	res, err := newTestResources()
	require.NoError(t, err)

	return suiteState{
		providers: &infra.Providers{Charts: charts},
		resources: res,
		// Only its nil-ness is read on the paths these tests take; no call is
		// made through it.
		suiteHarness:   &e2e.Harness{},
		chartInstalled: true,
	}
}

// TestStopCollector covers the split between "teardown succeeded" and "the
// collector can no longer write", which are not the same answer: a
// namespace-deletion fallback that worked stops the collector even though the
// step still has to report the release it could not remove.
func TestStopCollector(t *testing.T) {
	uninstallFailure := errors.New("helm uninstall timed out")
	fallbackFailure := errors.New("namespace stuck terminating")

	tests := []struct {
		name     string
		deployer *fakeChartDeployer

		wantStopped bool
		// wantErrs are the underlying failures the returned error must still
		// carry, so the report names what actually broke.
		wantErrs []error
		// wantFallback is whether the namespace deletion fallback had to run.
		wantFallback bool
	}{
		{
			name:         "When the release is removed normally it should record the collector stopped without the namespace fallback",
			deployer:     &fakeChartDeployer{},
			wantStopped:  true,
			wantFallback: false,
		},
		{
			name:         "When removal fails but the namespace fallback succeeds it should still record the collector stopped",
			deployer:     &fakeChartDeployer{uninstallErr: uninstallFailure},
			wantStopped:  true,
			wantErrs:     []error{uninstallFailure},
			wantFallback: true,
		},
		{
			name: "When the workload outlives the release and the fallback succeeds it should still record the collector stopped",
			deployer: &fakeChartDeployer{
				releaseStillInstalled: true,
				workloadGoneErr:       errors.New("pod still running"),
			},
			wantStopped:  true,
			wantFallback: true,
		},
		{
			name: "When removal and the namespace fallback both fail it should record the collector not stopped",
			deployer: &fakeChartDeployer{
				uninstallErr:       uninstallFailure,
				deleteNamespaceErr: fallbackFailure,
			},
			wantStopped:  false,
			wantErrs:     []error{uninstallFailure, fallbackFailure},
			wantFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := installedSuiteState(t, tt.deployer)
			withSuiteState(t, state)

			err := stopCollector(context.Background())

			if len(tt.wantErrs) == 0 && tt.wantStopped && !tt.wantFallback {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "a teardown that could not remove the release must report it")
			}
			for _, wantErr := range tt.wantErrs {
				require.ErrorIs(t, err, wantErr, "the original failure must survive in the report")
			}
			require.Equal(t, tt.wantStopped, collectorStopped)

			wantCalls := []string{
				fmt.Sprintf("uninstall %s/%s", state.resources.namespace, state.resources.releaseName),
				fmt.Sprintf("release-installed %s/%s", state.resources.namespace, state.resources.releaseName),
				fmt.Sprintf("wait-workload-gone %s", state.resources.namespace),
			}
			if tt.wantFallback {
				wantCalls = append(wantCalls, fmt.Sprintf("delete-namespace %s", state.resources.namespace))
			}
			require.Equal(t, wantCalls, tt.deployer.calls, "unexpected teardown calls")
		})
	}
}

// catalogDeletionStep returns the teardown step that removes the Catalog
// resources, which is the one action that depends on the collector being gone.
func catalogDeletionStep(t *testing.T) infra.CleanupStep {
	t.Helper()
	const name = "delete the catalog resources the collector created"
	for _, step := range suiteCleanupSteps() {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("teardown has no %q step", name)
	return infra.CleanupStep{}
}

// TestSuiteCleanupStepsDeferCatalogDeletionToTheCollectorStop pins the dependency
// itself: the Catalog resources may only be deleted once something confirmed
// the collector can no longer recreate them.
func TestSuiteCleanupStepsDeferCatalogDeletionToTheCollectorStop(t *testing.T) {
	tests := []struct {
		name             string
		collectorStopped bool
		wantSkip         bool
	}{
		{
			name:             "When the collector is confirmed stopped it should delete the catalog resources",
			collectorStopped: true,
			wantSkip:         false,
		},
		{
			name: "When the collector could not be confirmed stopped it should leave the catalog resources alone",
			// Deleting them here would hand them straight back to a collector
			// that is still polling the Model Registry.
			collectorStopped: false,
			wantSkip:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := installedSuiteState(t, &fakeChartDeployer{})
			state.collectorStopped = tt.collectorStopped
			withSuiteState(t, state)

			require.Equal(t, tt.wantSkip, catalogDeletionStep(t).Skip())
		})
	}
}

// TestSuiteCleanupStepsContinueWhenTheCollectorCannotBeStopped checks the other
// half of the dependency: skipping the dependent step must not stop the
// independent ones, and the suite must still fail with both the original
// failures and the fallback's.
func TestSuiteCleanupStepsContinueWhenTheCollectorCannotBeStopped(t *testing.T) {
	uninstallFailure := errors.New("helm uninstall timed out")
	fallbackFailure := errors.New("namespace stuck terminating")

	deployer := &fakeChartDeployer{
		uninstallErr:       uninstallFailure,
		deleteNamespaceErr: fallbackFailure,
	}
	state := installedSuiteState(t, deployer)

	// An independent step with an observable effect, used to show the sequence
	// kept going after the collector stop failed.
	file, err := os.CreateTemp(t.TempDir(), "values-*.yaml")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	state.valuesFilePath = file.Name()

	withSuiteState(t, state)

	err = infra.RunCleanup(context.Background(), suiteCleanupSteps())

	require.Error(t, err)
	require.ErrorIs(t, err, uninstallFailure, "the original failure must survive in the report")
	require.ErrorIs(t, err, fallbackFailure, "the fallback failure must survive in the report")
	for _, catalogName := range state.resources.catalogNames() {
		require.Contains(t, err.Error(), catalogName,
			"the report must say which catalog resources were left behind")
	}

	require.False(t, collectorStopped)
	require.NoFileExists(t, state.valuesFilePath,
		"independent cleanup must continue after the collector stop failed")
}
