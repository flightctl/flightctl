package catalogcollector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/stretchr/testify/require"
)

// These are plain Go tests, not Ginkgo specs: they cover the teardown
// decisions this suite makes around a deployment, so they must run without
// one. Run them with
//
//	go test ./test/e2e/catalogcollector/ -run TestSuiteCleanupSteps
//
// How the collector is actually deployed and stopped is not tested here. That
// belongs to the deployment implementation and is covered in
// test/e2e/infra/k8s; what is covered here is what this suite does with the
// answers it gets back.
//
// They share a process, and a set of package-level variables, with the Ginkgo
// suite: a normal e2e invocation runs TestCatalogCollector first and leaves
// those variables populated. Every test below that depends on them therefore
// installs its own fixture through withSuiteState rather than assuming a zero
// value, so the result is the same either way.

// suiteState is the package-level state the suite's setup populates and its
// teardown consumes.
type suiteState struct {
	providers         *infra.Providers
	auxSvcs           *auxiliary.Services
	resources         *testResources
	suiteHarness      *e2e.Harness
	smokeModel        *seededModel
	seededModels      []*seededModel
	collectorInstance infra.CollectorInstance
	authProviderSet   bool
	collectorStopped  bool
}

// captureSuiteState snapshots the suite variables as they are now.
func captureSuiteState() suiteState {
	return suiteState{
		providers:         providers,
		auxSvcs:           auxSvcs,
		resources:         resources,
		suiteHarness:      suiteHarness,
		smokeModel:        smokeModel,
		seededModels:      seededModels,
		collectorInstance: collectorInstance,
		authProviderSet:   authProviderSet,
		collectorStopped:  collectorStopped,
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
	collectorInstance = state.collectorInstance
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

// fakeCollectorInstance is an infra.CollectorInstance that answers Stop the way
// a test wants and records what teardown asked of it.
type fakeCollectorInstance struct {
	// stopped and stopErr are the two answers Stop reports, which can
	// disagree: a deployment that fell back to a blunter mechanism confirms
	// the collector is down and still has a failure to report.
	stopped bool
	stopErr error
	// cleanupErr is what Cleanup reports.
	cleanupErr error
	// calls records the operations teardown performed, in order.
	calls []string
}

func (f *fakeCollectorInstance) Stop(context.Context) (bool, error) {
	f.calls = append(f.calls, "stop")
	return f.stopped, f.stopErr
}

func (f *fakeCollectorInstance) Cleanup(context.Context) error {
	f.calls = append(f.calls, "cleanup")
	return f.cleanupErr
}

func (f *fakeCollectorInstance) WaitReady(context.Context, time.Duration) error {
	panic("not used")
}

func (f *fakeCollectorInstance) Logs(context.Context, int64) (string, error) {
	panic("not used")
}

func (f *fakeCollectorInstance) Diagnostics(context.Context) string { panic("not used") }

var _ infra.CollectorInstance = (*fakeCollectorInstance)(nil)

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
		"remove the collector deployment",
		"clean up the aux services",
	}, names)
}

// TestSuiteCleanupStepsSkipWhenNothingWasCreated covers the early-skip path: a
// suite that never got past its availability check must tear down nothing, and
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

// deployedSuiteState is the state a suite has once BeforeSuite completed: the
// collector is deployed and has not been stopped yet.
func deployedSuiteState(t *testing.T, instance infra.CollectorInstance) suiteState {
	t.Helper()
	res, err := newTestResources()
	require.NoError(t, err)

	return suiteState{
		resources:         res,
		collectorInstance: instance,
		// Only its nil-ness is read on the paths these tests take; no call is
		// made through it.
		suiteHarness: &e2e.Harness{},
	}
}

// TestStopCollectorRecordsConfirmationSeparatelyFromFailure covers what this
// suite does with the two answers the deployment gives it. They are not the
// same answer: a deployment that had to fall back still confirms the collector
// is down while reporting the state it could not remove, and the catalog
// deletion keys off the confirmation rather than off the error.
func TestStopCollectorRecordsConfirmationSeparatelyFromFailure(t *testing.T) {
	stopFailure := errors.New("the deployment could not be removed")

	tests := []struct {
		name     string
		instance *fakeCollectorInstance

		wantStopped bool
		wantErr     bool
		// wantCatalogsNamed is whether the report has to list the catalogs
		// that were deliberately left in place.
		wantCatalogsNamed bool
	}{
		{
			name:        "When the deployment stopped the collector cleanly it should record it stopped and report no failure",
			instance:    &fakeCollectorInstance{stopped: true},
			wantStopped: true,
		},
		{
			name:        "When the deployment confirmed the collector is down but still failed it should record it stopped and report the failure",
			instance:    &fakeCollectorInstance{stopped: true, stopErr: stopFailure},
			wantStopped: true,
			wantErr:     true,
			// The collector cannot write any more, so the catalogs will be
			// deleted by the next step and nothing was left behind.
			wantCatalogsNamed: false,
		},
		{
			name:              "When the deployment could not confirm the collector is down it should record it running and name the catalogs it left",
			instance:          &fakeCollectorInstance{stopped: false, stopErr: stopFailure},
			wantStopped:       false,
			wantErr:           true,
			wantCatalogsNamed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := deployedSuiteState(t, tt.instance)
			withSuiteState(t, state)

			err := stopCollector(context.Background())

			require.Equal(t, tt.wantStopped, collectorStopped)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorIs(t, err, stopFailure, "the deployment's failure must survive in the report")
			for _, catalogName := range state.resources.catalogNames() {
				if tt.wantCatalogsNamed {
					require.Contains(t, err.Error(), catalogName,
						"the report must say which catalog resources were left behind")
				} else {
					require.NotContains(t, err.Error(), catalogName,
						"nothing was left behind, so the report must not claim otherwise")
				}
			}
		})
	}
}

// TestSuiteCleanupStepsDeferCatalogDeletionToTheCollectorStop pins the
// dependency itself: the Catalog resources may only be deleted once something
// confirmed the collector can no longer recreate them.
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
			state := deployedSuiteState(t, &fakeCollectorInstance{})
			state.collectorStopped = tt.collectorStopped
			withSuiteState(t, state)

			require.Equal(t, tt.wantSkip, catalogDeletionStep(t).Skip())
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

// TestSuiteCleanupStepsContinueWhenTheCollectorCannotBeStopped checks the other
// half of the dependency: skipping the dependent step must not stop the
// independent ones, and the suite must still fail with the failure that caused
// it.
func TestSuiteCleanupStepsContinueWhenTheCollectorCannotBeStopped(t *testing.T) {
	stopFailure := errors.New("the deployment could not be removed and the fallback failed too")

	instance := &fakeCollectorInstance{stopped: false, stopErr: stopFailure}
	state := deployedSuiteState(t, instance)
	withSuiteState(t, state)

	err := infra.RunCleanup(context.Background(), suiteCleanupSteps())

	require.Error(t, err)
	require.ErrorIs(t, err, stopFailure, "the original failure must survive in the report")
	for _, catalogName := range state.resources.catalogNames() {
		require.Contains(t, err.Error(), catalogName,
			"the report must say which catalog resources were left behind")
	}

	require.False(t, collectorStopped)
	// Removing what the deployment still owns is independent of stopping it,
	// so it has to have run anyway.
	require.Equal(t, []string{"stop", "cleanup"}, instance.calls,
		"independent cleanup must continue after the collector stop failed")
}

// TestSuiteCleanupStepsReportDeploymentRemovalFailures checks that the
// independent removal step is not merely reached but also accounted for: a
// deployment that could not be removed has to fail the suite, because what it
// left behind is what breaks the following run.
func TestSuiteCleanupStepsReportDeploymentRemovalFailures(t *testing.T) {
	cleanupFailure := errors.New("the deployment could not be removed")

	instance := &fakeCollectorInstance{stopped: true, cleanupErr: cleanupFailure}
	withSuiteState(t, deployedSuiteState(t, instance))

	err := infra.RunCleanup(context.Background(), suiteCleanupSteps())

	require.ErrorIs(t, err, cleanupFailure)
	require.Equal(t, []string{"stop", "cleanup"}, instance.calls)
}
