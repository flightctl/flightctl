package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func migrationTestConfig() PackageModeTemplateMigrationConfig {
	return PackageModeTemplateMigrationConfig{
		PageSize:   10,
		BatchSize:  10,
		TimeBudget: time.Minute,
	}
}

func testLogger() logrus.FieldLogger {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
	return log
}

// fakeCheckpoints is an in-memory checkpoint service that records every write,
// so a test can assert the durable state the next poll would read.
type fakeCheckpoints struct {
	values       map[string][]byte
	writes       int
	getErr       *domain.Status
	setErr       *domain.Status
	lastWriteCtx context.Context           // ctx passed to the last SetCheckpoint
	onSet        func(ctx context.Context) // optional pre-write callback
}

func newFakeCheckpoints() *fakeCheckpoints {
	return &fakeCheckpoints{values: map[string][]byte{}}
}

func (f *fakeCheckpoints) GetCheckpoint(_ context.Context, consumer, key string) ([]byte, domain.Status) {
	if f.getErr != nil {
		return nil, *f.getErr
	}
	value, ok := f.values[consumer+"/"+key]
	if !ok {
		return nil, domain.StatusResourceNotFound("Checkpoint", key)
	}
	return value, domain.StatusOK()
}

func (f *fakeCheckpoints) SetCheckpoint(ctx context.Context, consumer, key string, value []byte) domain.Status {
	f.lastWriteCtx = ctx
	if f.onSet != nil {
		f.onSet(ctx)
	}
	if f.setErr != nil {
		return *f.setErr
	}
	if err := ctx.Err(); err != nil {
		return domain.Status{Code: http.StatusServiceUnavailable, Message: "context canceled"}
	}
	f.writes++
	f.values[consumer+"/"+key] = value
	return domain.StatusOK()
}

func (f *fakeCheckpoints) GetDatabaseTime(context.Context) (time.Time, domain.Status) {
	return time.Now(), domain.StatusOK()
}

func (f *fakeCheckpoints) stored(t *testing.T, orgID uuid.UUID) packageModeMigrationCheckpoint {
	t.Helper()
	raw, ok := f.values[packageModeTemplateMigrationConsumer+"/"+packageModeMigrationCheckpointKey(orgID)]
	require.True(t, ok, "expected a checkpoint to have been written")
	var checkpoint packageModeMigrationCheckpoint
	require.NoError(t, json.Unmarshal(raw, &checkpoint))
	return checkpoint
}

func (f *fakeCheckpoints) seed(t *testing.T, orgID uuid.UUID, checkpoint packageModeMigrationCheckpoint) {
	t.Helper()
	encoded, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	f.values[packageModeTemplateMigrationConsumer+"/"+packageModeMigrationCheckpointKey(orgID)] = encoded
}

var _ checkpointservice.Service = (*fakeCheckpoints)(nil)

func fleetWithImage(name, image string) domain.Fleet {
	fleet := domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name:            lo.ToPtr(name),
			ResourceVersion: lo.ToPtr("1"),
		},
	}
	if image != "" {
		fleet.Spec.Template.Spec.Os = &domain.DeviceOsSpec{Image: image}
	}
	return fleet
}

func fleetList(continueToken *string, fleets ...domain.Fleet) *domain.FleetList {
	return &domain.FleetList{
		Items:    fleets,
		Metadata: domain.ListMeta{Continue: continueToken},
	}
}

func TestNewPackageModeFleetTemplateMigrationTaskValidatesDependencies(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()

	tests := []struct {
		name     string
		fleetSvc fleetservice.Service
		cps      checkpointservice.Service
		config   PackageModeTemplateMigrationConfig
	}{
		{name: "When the fleet service is missing it should be rejected", fleetSvc: nil, cps: checkpoints, config: migrationTestConfig()},
		{name: "When the checkpoint service is missing it should be rejected", fleetSvc: fleetSvc, cps: nil, config: migrationTestConfig()},
		{
			name: "When the page size is below one it should be rejected", fleetSvc: fleetSvc, cps: checkpoints,
			config: PackageModeTemplateMigrationConfig{PageSize: 0, BatchSize: 1, TimeBudget: time.Minute},
		},
		{
			name: "When the batch size is below one it should be rejected", fleetSvc: fleetSvc, cps: checkpoints,
			config: PackageModeTemplateMigrationConfig{PageSize: 1, BatchSize: 0, TimeBudget: time.Minute},
		},
		{
			name: "When the time budget is not positive it should be rejected", fleetSvc: fleetSvc, cps: checkpoints,
			config: PackageModeTemplateMigrationConfig{PageSize: 1, BatchSize: 1, TimeBudget: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPackageModeFleetTemplateMigrationTask(tt.fleetSvc, tt.cps, tt.config, testLogger())
			require.Error(t, err)
		})
	}

	t.Run("When every dependency is supplied it should be accepted", func(t *testing.T) {
		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
		require.NoError(t, err)
		require.NotNil(t, task)
	})
}

func TestMigrationLifecycleReachesCompleteAndThenStops(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	// One page, exhausted immediately: the run should complete.
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(nil, fleetWithImage("a", "quay.io/org/os:v1")), domain.StatusOK()).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)

	task.Poll(t.Context(), orgID)
	require.Equal(t, packageModeMigrationComplete, checkpoints.stored(t, orgID).Phase)

	// A completed run must not list anything again; the mock would fail on an
	// unexpected second call.
	task.Poll(t.Context(), orgID)
	require.Equal(t, packageModeMigrationComplete, checkpoints.stored(t, orgID).Phase)
}

func TestMigrationAdvancesCursorAcrossPages(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	gomock.InOrder(
		fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
				require.Nil(t, params.Continue, "the first page must start with no cursor")
				return fleetList(lo.ToPtr("page2"), fleetWithImage("a", "quay.io/org/os:v1")), domain.StatusOK()
			}),
		fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
				require.Equal(t, "page2", lo.FromPtr(params.Continue))
				return fleetList(nil, fleetWithImage("b", "quay.io/org/os:v2")), domain.StatusOK()
			}),
	)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	stored := checkpoints.stored(t, orgID)
	require.Equal(t, packageModeMigrationComplete, stored.Phase)
	require.Nil(t, stored.Cursor)
}

func TestMigrationAbortsWhenTheCursorDoesNotAdvance(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()
	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Cursor:  lo.ToPtr("stuck"),
	})

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(lo.ToPtr("stuck"), fleetWithImage("a", "quay.io/org/os:v1")), domain.StatusOK()).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	stored := checkpoints.stored(t, orgID)
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase,
		"a listing that cannot advance must not be reported as a finished run")
}

func TestMigrationDiscardsCheckpointFromAnotherSchemaVersion(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	// A completed checkpoint written under a different schema version must not
	// suppress this version's run.
	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion - 1,
		Phase:   packageModeMigrationComplete,
	})

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(nil, fleetWithImage("a", "quay.io/org/os:v1")), domain.StatusOK()).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	require.Equal(t, packageModeMigrationComplete, checkpoints.stored(t, orgID).Phase)
}

func TestMigrationStopsAtTheTimeBudgetWithoutCompleting(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	config := migrationTestConfig()
	config.TimeBudget = time.Minute

	// Controlled clock that advances by 2× the budget on each call, so
	// the scan's outer budget check is exceeded deterministically.
	var calls int
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
	require.NoError(t, err)
	task.now = func() time.Time {
		calls++
		return epoch.Add(time.Duration(calls) * 2 * config.TimeBudget)
	}
	task.Poll(t.Context(), orgID)

	stored := checkpoints.stored(t, orgID)
	require.Equal(t, packageModeMigrationRunning, stored.Phase,
		"a poll cut short by its budget has not proven the listing is exhausted")
}

func TestMigrationDoesNotCompleteWhenCancelled(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(ctx, orgID)

	if stored, ok := checkpoints.values[packageModeTemplateMigrationConsumer+"/"+packageModeMigrationCheckpointKey(orgID)]; ok {
		var checkpoint packageModeMigrationCheckpoint
		require.NoError(t, json.Unmarshal(stored, &checkpoint))
		require.NotEqual(t, packageModeMigrationComplete, checkpoint.Phase)
	}
}

func TestMigrationKeepsARetryDurableWhenItsFleetCannotBeRead(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Retry:   []string{"a"},
	})

	// The retry is attempted first, by name, and the read fails.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		Return(nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}).Times(1)
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(nil), domain.StatusOK()).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	stored := checkpoints.stored(t, orgID)
	require.Equal(t, []string{"a"}, stored.Retry,
		"a fleet that could not be read still owes a retry on the next poll")
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase,
		"a run that still owes a retry must not report itself finished")
}

func TestMigrationStopsWhenTheCheckpointCannotBeRead(t *testing.T) {
	ctrl := gomock.NewController(t)
	// No fleet service call is expected: an unreadable checkpoint means the poll
	// does not know what it has already done, so it must not walk fleets.
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	checkpoints.getErr = &domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
	orgID := uuid.New()

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	require.Zero(t, checkpoints.writes,
		"a poll that cannot read its checkpoint must not overwrite it either")
}

func TestMigrationLeavesNoCheckpointWhenItCannotBeWritten(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	checkpoints.setErr = &domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
	orgID := uuid.New()

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(nil, fleetWithImage("a", "quay.io/org/os:v1")), domain.StatusOK()).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	_, ok := checkpoints.values[packageModeTemplateMigrationConsumer+"/"+packageModeMigrationCheckpointKey(orgID)]
	require.False(t, ok, "a checkpoint write that failed must not look like it succeeded")
}

func TestMigrationKeepsRetriesItCouldNotAttempt(t *testing.T) {
	// A bound can end a poll before the fleets owing a retry have been worked
	// through. Those left over still owe a retry on the next poll.
	seeded := packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Retry:   []string{"a", "b"},
	}

	t.Run("When the time budget stops the drain it should keep them", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		// No fleet service call is expected: the budget check fires before
		// the first retry attempt because the auto-advancing clock exceeds
		// the budget on its second read.
		fleetSvc := fleetservice.NewMockService(ctrl)
		checkpoints := newFakeCheckpoints()
		orgID := uuid.New()
		checkpoints.seed(t, orgID, seeded)

		config := migrationTestConfig()
		config.TimeBudget = time.Minute

		var calls int
		epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
		require.NoError(t, err)
		task.now = func() time.Time {
			calls++
			return epoch.Add(time.Duration(calls) * config.TimeBudget)
		}
		task.Poll(t.Context(), orgID)

		stored := checkpoints.stored(t, orgID)
		require.Equal(t, []string{"a", "b"}, stored.Retry)
		require.NotEqual(t, packageModeMigrationComplete, stored.Phase)
	})

	t.Run("When the poll is cancelled it should keep them", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		fleetSvc := fleetservice.NewMockService(ctrl)
		checkpoints := newFakeCheckpoints()
		orgID := uuid.New()
		checkpoints.seed(t, orgID, seeded)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
		require.NoError(t, err)
		task.Poll(ctx, orgID)

		stored := checkpoints.stored(t, orgID)
		require.Equal(t, []string{"a", "b"}, stored.Retry)
		require.NotEqual(t, packageModeMigrationComplete, stored.Phase)
	})
}

func TestMigrationProgressesAcrossPollsWhenListingConsumesTheBudget(t *testing.T) {
	// When ListFleets takes longer than TimeBudget, the page is saved as
	// pending identities and resumed via GetFleet on a later poll.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	config := migrationTestConfig()
	config.TimeBudget = time.Minute
	config.PageSize = 1

	clock := newFakeClock()

	// Poll 1: listing consumes the budget → page saved as pending.
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			require.Nil(t, params.Continue, "first listing must start with no cursor")
			clock.Advance(2 * config.TimeBudget)
			return fleetList(lo.ToPtr("page2"),
				fleetWithImage("a", "quay.io/org/os:v1"),
			), domain.StatusOK()
		}).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
	require.NoError(t, err)
	task.now = clock.Now
	require.NoError(t, task.poll(t.Context(), orgID))

	stored := checkpoints.stored(t, orgID)
	require.Equal(t, packageModeMigrationRunning, stored.Phase)
	require.Equal(t, []string{"a"}, stored.Pending,
		"the listed fleet must be saved as pending for the next poll")

	// Poll 2 (ScanFirst=true): resumes pending "a" via GetFleet, processes
	// it, advances cursor to "page2". Then lists page2.
	clock.current = newFakeClock().current.Add(5 * time.Minute)
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			f := fleetWithImage("a", "quay.io/org/os:v1")
			return &f, domain.StatusOK()
		}).Times(1)
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			require.Equal(t, "page2", lo.FromPtr(params.Continue))
			return fleetList(nil, fleetWithImage("b", "quay.io/org/os:v2")), domain.StatusOK()
		}).Times(1)
	task2, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
	require.NoError(t, err)
	task2.now = clock.Now
	require.NoError(t, task2.poll(t.Context(), orgID))
	stored = checkpoints.stored(t, orgID)
	require.Equal(t, packageModeMigrationComplete, stored.Phase)
}

func TestMigrationStopsMidPageAfterBudgetAndDoesNotSkip(t *testing.T) {
	// When the budget expires mid-page, the fleet that started before the
	// deadline IS processed, but no further fleet starts. The remaining
	// identities are saved as pending for the next poll.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()
	log, capture := captureLogger()

	config := migrationTestConfig()
	config.TimeBudget = time.Minute

	// Deterministic clock: each t.now() call advances by 16s.
	// Call sequence with 3-item page:
	//   startedAt(16s), pre-loop-budget(32s: 32-16=16s<60s OK),
	//   post-list-budget(48s: 48-16=32s<60s OK),
	//   pre-fleet-a(64s: 64-16=48s<60s OK → "a" processed),
	//   pre-fleet-b(80s: 80-16=64s>=60s exceeded → "b","c" saved as pending).
	var callCount int
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, log)
	require.NoError(t, err)
	task.now = func() time.Time {
		callCount++
		return epoch.Add(time.Duration(callCount) * 16 * time.Second)
	}

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(lo.ToPtr("page2"),
			fleetWithImage("a", "quay.io/org/os:v1"),
			fleetWithImage("b", "quay.io/org/os:v2"),
			fleetWithImage("c", "quay.io/org/os:v3"),
		), domain.StatusOK()).Times(1)

	require.NoError(t, task.poll(t.Context(), orgID))

	stored := checkpoints.stored(t, orgID)
	require.Equal(t, packageModeMigrationRunning, stored.Phase,
		"a poll stopped mid-page must not report completion")
	require.Nil(t, stored.Cursor,
		"cursor must not advance past an incomplete page")
	require.NotEmpty(t, capture.perPoll,
		"log capture must have recorded the poll-completed entry")
	require.Equal(t, 1, capture.total(),
		"exactly one fleet ('a') must have been processed before the budget stopped the page")
	require.Equal(t, []string{"b", "c"}, stored.Pending,
		"unprocessed fleets must be saved as pending for the next poll")
	require.NotNil(t, stored.NextCursor,
		"the continuation token must be saved for the pending page")
	require.Equal(t, "page2", *stored.NextCursor,
		"NextCursor must point to the page after the pending items")
}

func TestMigrationReportsBothPollAndCheckpointErrors(t *testing.T) {
	// Regression: persist() discarded the checkpoint write error when cause was
	// non-nil, silently losing checkpoint write failures.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	// ListFleets returns an error (this becomes the 'cause' in persist).
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(nil, domain.Status{Code: http.StatusInternalServerError, Message: "listing failed"}).Times(1)

	// Checkpoint writes also fail.
	checkpoints.setErr = &domain.Status{Code: http.StatusInternalServerError, Message: "checkpoint write failed"}

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)

	err = task.poll(t.Context(), orgID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "listing failed",
		"the original poll error must be present")
	require.Contains(t, err.Error(), "checkpoint",
		"the checkpoint write failure must not be silently discarded")
}

func TestMigrationPreservesCancellationIdentityWithCheckpointError(t *testing.T) {
	// When a poll is cancelled and the checkpoint also fails to write, the
	// returned error must remain identifiable as a cancellation so callers
	// can distinguish shutdowns from unexpected failures.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	checkpoints.setErr = &domain.Status{Code: http.StatusInternalServerError, Message: "storage down"}

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)

	err = task.poll(ctx, orgID)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled,
		"cancellation identity must be preserved even when the checkpoint also fails")
	require.Contains(t, err.Error(), "checkpoint",
		"the checkpoint write failure must also be reported")
}

func TestMigrationScanReachedAfterExpensiveRetry(t *testing.T) {
	// Regression: when a retained retry's GetFleet consumed the entire
	// budget on every poll, scanning was permanently starved.
	//
	// The fix uses fair alternating scheduling (ScanFirst): on the poll
	// where scanning runs first, it completes before the expensive retry
	// consumes the budget. The retry remains durable for the next poll.
	//
	// This test verifies that the scan IS reached within bounded polls
	// and that the retry remains durable.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	config := migrationTestConfig()
	config.TimeBudget = time.Minute

	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := epoch

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Retry:   []string{"a"},
	})

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
	require.NoError(t, err)
	task.now = func() time.Time { return now }

	// Poll 1: retry "a" fails and consumes the budget. Scan does not start.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			now = now.Add(2 * config.TimeBudget) // consumes the budget
			return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
		}).Times(1)

	now = epoch
	require.NoError(t, task.poll(t.Context(), orgID))
	stored := checkpoints.stored(t, orgID)
	require.Contains(t, stored.Retry, "a", "retry must remain durable after expensive failure")
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase)

	// Poll 2: fair scheduling gives scan priority. Scan starts, reaches "b".
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		Return(nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}).Times(1)
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(nil, fleetWithImage("b", "quay.io/org/os:v1")), domain.StatusOK()).Times(1)

	now = epoch.Add(5 * time.Minute) // reset clock for poll 2
	require.NoError(t, task.poll(t.Context(), orgID))
	stored = checkpoints.stored(t, orgID)
	require.Contains(t, stored.Retry, "a", "retry must remain durable")
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase,
		"the run must not complete while retry 'a' is still pending")
}

func TestMigrationStopsRetryingAFleetThatIsGone(t *testing.T) {
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Retry:   []string{"a"},
	})

	// The fleet was deleted between the two polls, so there is nothing left to
	// migrate for it and it must not hold the run open forever.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		Return(nil, domain.StatusResourceNotFound("Fleet", "a")).Times(1)
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(nil), domain.StatusOK()).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	stored := checkpoints.stored(t, orgID)
	require.Empty(t, stored.Retry, "a fleet that no longer exists stops owing a retry")
	require.Equal(t, packageModeMigrationComplete, stored.Phase)
}

func TestMigrationPendingPageDeletedFleet(t *testing.T) {
	// A fleet saved in the pending page is deleted between polls (GetFleet
	// returns 404). It must be removed from pending, and the remaining
	// fleet on the page must still be processed.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	// Seed a checkpoint with a pending page: fleets "a" (will be deleted)
	// and "b" (will be processed), with NextCursor for the following page.
	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version:    packageModeTemplateMigrationVersion,
		Phase:      packageModeMigrationRunning,
		Pending:    []string{"a", "b"},
		NextCursor: lo.ToPtr("page2"),
		ScanFirst:  true, // scan first so pending is resumed
	})

	// "a" was deleted.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		Return(nil, domain.StatusResourceNotFound("Fleet", "a")).Times(1)
	// "b" is still present.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "b", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			f := fleetWithImage("b", "quay.io/org/os:v1")
			return &f, domain.StatusOK()
		}).Times(1)
	// After pending is drained, cursor advances to "page2" and listing starts.
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			require.Equal(t, "page2", lo.FromPtr(params.Continue))
			return fleetList(nil), domain.StatusOK()
		}).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	stored := checkpoints.stored(t, orgID)
	require.Empty(t, stored.Pending, "pending must be drained")
	require.Equal(t, packageModeMigrationComplete, stored.Phase,
		"the migration must complete when all pages are exhausted")
}

func TestMigrationLegacyCheckpointLoading(t *testing.T) {
	// A v1 checkpoint written without Pending, NextCursor, or ScanFirst
	// must load correctly with zero/nil defaults.
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	// Manually write a JSON checkpoint without the new fields.
	raw := []byte(`{"version":1,"phase":"running","cursor":"page2","retry":["x"]}`)
	checkpoints.values[packageModeTemplateMigrationConsumer+"/"+packageModeMigrationCheckpointKey(orgID)] = raw

	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)

	// Expect retry "x" to be attempted (budget allows because clock is real).
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "x", gomock.Any()).
		Return(nil, domain.StatusResourceNotFound("Fleet", "x")).Times(1)
	// Expect listing from cursor "page2" (no pending to resume).
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			require.Equal(t, "page2", lo.FromPtr(params.Continue))
			return fleetList(nil), domain.StatusOK()
		}).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	stored := checkpoints.stored(t, orgID)
	require.Equal(t, packageModeMigrationComplete, stored.Phase)
	require.Empty(t, stored.Pending)
	require.Empty(t, stored.Retry)
}

func TestMigrationBoundedRetryAttempts(t *testing.T) {
	// A retry that re-fails must not be attempted again in the same poll.
	// The bounded per-poll attempt set prevents infinite loops with a
	// frozen clock.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Retry:   []string{"a"},
	})

	// "a" fails and is re-queued. It must be called only once per poll.
	var getFleetCalls int
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			getFleetCalls++
			return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
		}).AnyTimes()
	// Listing is expected when scan runs (fair scheduling alternation).
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(nil), domain.StatusOK()).AnyTimes()

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	// "a" must have been attempted exactly once in this poll.
	require.Equal(t, 1, getFleetCalls,
		"a re-failed retry must be attempted exactly once per poll")
	stored := checkpoints.stored(t, orgID)
	require.Contains(t, stored.Retry, "a",
		"a re-failed retry must remain durable")
}

func TestMigrationRetryCapRetainsPendingFleet(t *testing.T) {
	// When a pending fleet fails during resumePending and the retry list is
	// already at capacity, queueRetryOrKeepPending returns false. The name
	// must be kept in Pending so it is not lost. This tests the cap-retention
	// path, not the overflow-rewind path (see the separate overflow test).
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	retries := make([]string, maxPackageModeTemplateMigrationRetries)
	for i := range retries {
		retries[i] = fmt.Sprintf("r%d", i)
	}
	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version:    packageModeTemplateMigrationVersion,
		Phase:      packageModeMigrationRunning,
		Retry:      retries,
		Pending:    []string{"fail-fleet"},
		NextCursor: lo.ToPtr("stale-next"),
		ScanFirst:  true,
	})

	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "fail-fleet", gomock.Any()).
		Return(nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}).Times(1)
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, gomock.Any(), gomock.Any()).
		Return(nil, domain.Status{Code: http.StatusInternalServerError, Message: "retry"}).AnyTimes()

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	stored := checkpoints.stored(t, orgID)
	require.Contains(t, stored.Pending, "fail-fleet",
		"a fleet that cannot be queued for retry must remain in Pending")
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase)
}

func TestMigrationRetryOverflowNormalizesCheckpointAtPersist(t *testing.T) {
	// When rememberRetry triggers overflow during a page loop, the caller's
	// local remaining/next variables can reassign Pending/NextCursor back
	// AFTER rememberRetry cleared them. The persist finalization must
	// normalize Cursor=nil, Pending=nil, NextCursor=nil when retryOverflow
	// is true, ensuring the next poll does a full replay and the unrecorded
	// fleet is not bypassed.
	//
	// This test directly exercises rememberRetry with a full retry list,
	// then models the caller's subsequent local assignments, and verifies
	// the stored checkpoint after persist.
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)

	// Pre-fill retry list to 256 (the cap).
	retries := make([]string, maxPackageModeTemplateMigrationRetries)
	for i := range retries {
		retries[i] = fmt.Sprintf("r%d", i)
	}

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)

	run := &migrationRun{
		checkpoint: packageModeMigrationCheckpoint{
			Version: packageModeTemplateMigrationVersion,
			Phase:   packageModeMigrationRunning,
			Retry:   retries,
			Cursor:  lo.ToPtr("current-page"),
		},
	}

	// Call rememberRetry with an unrecorded name — triggers overflow.
	task.rememberRetry(run, "unrecorded")
	require.True(t, run.retryOverflow, "retryOverflow must be set")

	// Model the caller's local bookkeeping: remaining items and stale next
	// token are assigned back after rememberRetry returned, as the page
	// loops do.
	run.checkpoint.Pending = []string{"leftover-a", "leftover-b"}
	run.checkpoint.NextCursor = lo.ToPtr("stale-next-page")

	// Persist — the normalization must clear the stale assignments.
	require.NoError(t, task.persist(t.Context(), orgID, run, nil))

	stored := checkpoints.stored(t, orgID)
	require.Nil(t, stored.Cursor,
		"Cursor must be nil after overflow rewind")
	require.Empty(t, stored.Pending,
		"Pending must be cleared at persist so stale cached state cannot bypass the rewind")
	require.Nil(t, stored.NextCursor,
		"NextCursor must be cleared at persist so stale cached state cannot bypass the rewind")
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase,
		"must not claim completion with retryOverflow")
	require.Equal(t, maxPackageModeTemplateMigrationRetries, len(stored.Retry),
		"all original retry names must be preserved")
}

func TestMigrationFairnessOnScanError(t *testing.T) {
	// When scanning repeatedly errors (e.g. listing always fails), the
	// ScanFirst preference must still alternate so retries get a turn.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	budget := time.Second
	config := PackageModeTemplateMigrationConfig{
		PageSize:   10,
		BatchSize:  100,
		TimeBudget: budget,
	}
	clock := newFakeClock()

	// Seed with retry "a" that can succeed and ScanFirst=true so scanning
	// runs first.
	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version:   packageModeTemplateMigrationVersion,
		Phase:     packageModeMigrationRunning,
		Retry:     []string{"a"},
		ScanFirst: true,
	})

	var retryAttempts int
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			retryAttempts++
			f := fleetWithImage("a", "")
			return &f, domain.StatusOK()
		}).AnyTimes()

	// Listing always fails (expensive).
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			clock.Advance(2 * budget)
			return nil, domain.Status{Code: http.StatusInternalServerError, Message: "listing down"}
		}).AnyTimes()

	for poll := 0; poll < 4; poll++ {
		clock.current = newFakeClock().current.Add(time.Duration(poll) * 10 * time.Minute)
		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
		require.NoError(t, err)
		task.now = clock.Now
		pollErr := task.poll(t.Context(), orgID)
		// Every poll must surface the listing error — ignoring it hides
		// broken scan phases.
		require.Error(t, pollErr,
			"poll %d must return the listing error", poll)
		require.Contains(t, pollErr.Error(), "listing",
			"poll %d error must originate from the listing failure", poll)
		require.NotContains(t, pollErr.Error(), "checkpoint",
			"poll %d must not report an additional checkpoint error", poll)
	}

	require.GreaterOrEqual(t, retryAttempts, 1,
		"retry 'a' must be attempted at least once despite scanning always failing")
}

func TestMigrationResumeRejectsNonAdvancingCursor(t *testing.T) {
	// When a pending page resumes and its NextCursor equals the current
	// Cursor, the non-advancing guard must fire rather than looping forever.
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version:    packageModeTemplateMigrationVersion,
		Phase:      packageModeMigrationRunning,
		Cursor:     lo.ToPtr("stuck"),
		Pending:    []string{"x"},
		NextCursor: lo.ToPtr("stuck"), // same as Cursor
	})

	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "x", gomock.Any()).
		Return(&domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr("x")}}, domain.StatusOK()).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	err = task.poll(t.Context(), orgID)
	require.Error(t, err, "non-advancing cursor through resumed pending must be rejected")
	require.Contains(t, err.Error(), "did not advance")
}

func TestMigrationNamelessFleetDoesNotSkipValidEntries(t *testing.T) {
	// A page containing a nameless entry before valid entries must still
	// process the valid entries without index misalignment.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()
	log, capture := captureLogger()

	namelessFleet := domain.Fleet{Metadata: domain.ObjectMeta{Name: nil}}

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(&domain.FleetList{
			Items: []domain.Fleet{
				namelessFleet,
				fleetWithImage("b", ""),
				fleetWithImage("c", ""),
			},
			Metadata: domain.ListMeta{Continue: nil},
		}, domain.StatusOK()).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), log)
	require.NoError(t, err)
	task.Poll(t.Context(), orgID)

	stored := checkpoints.stored(t, orgID)
	require.NotEmpty(t, capture.perPoll)
	require.Equal(t, 2, capture.total(),
		"both valid entries (b, c) must be processed; nameless entry skipped")
	require.Equal(t, packageModeMigrationComplete, stored.Phase,
		"the page must complete despite the nameless entry")
}

func TestMigrationEmptyContinuedPageAdvancesCursor(t *testing.T) {
	// An empty page with an advancing continuation token and expired budget
	// must advance the cursor durably rather than re-listing the same empty
	// page forever. A later page with a fleet must eventually be reached.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	budget := time.Second
	config := PackageModeTemplateMigrationConfig{
		PageSize:   10,
		BatchSize:  100,
		TimeBudget: budget,
	}
	clock := newFakeClock()
	log, capture := captureLogger()

	gomock.InOrder(
		// Poll 1: empty page with Continue="page2", listing consumes budget.
		fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
				require.Nil(t, params.Continue, "poll 1 must start with no cursor")
				clock.Advance(2 * budget)
				return fleetList(lo.ToPtr("page2")), domain.StatusOK()
			}),
		// Poll 2: page2 has a fleet, listing consumes budget, page saved as pending.
		fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
				require.NotNil(t, params.Continue)
				require.Equal(t, "page2", *params.Continue, "cursor must have advanced past the empty page")
				clock.Advance(2 * budget)
				return fleetList(nil, fleetWithImage("x", "")), domain.StatusOK()
			}),
	)
	// Poll 3: resume pending "x" via GetFleet.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "x", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			f := fleetWithImage("x", "")
			return &f, domain.StatusOK()
		}).Times(1)

	for poll := 0; poll < 3; poll++ {
		clock.current = newFakeClock().current.Add(time.Duration(poll) * 10 * time.Minute)
		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, log)
		require.NoError(t, err)
		task.now = clock.Now
		require.NoError(t, task.poll(t.Context(), orgID))
	}

	stored := checkpoints.stored(t, orgID)
	require.Equal(t, packageModeMigrationComplete, stored.Phase,
		"the fleet beyond the empty page must be processed and completed")
	require.NotEmpty(t, capture.perPoll)
	require.Equal(t, 1, capture.total(),
		"exactly fleet 'x' must have been processed")
}

// ---------------------------------------------------------------------------
// Pinned regression fixtures — deterministic fake clock where reads do not
// advance time; only explicit mocked operations advance it. The debug log
// emitted by persist carries the actual per-poll processed count, which is
// captured by logCapture and summed across polls to verify real fleet
// processing without duplicate-count substitutions.
// ---------------------------------------------------------------------------

// fakeClock is a deterministic clock whose value only changes via explicit
// Advance calls. Reads (via Now) return the current frozen value.
type fakeClock struct {
	current time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time          { return c.current }
func (c *fakeClock) Advance(d time.Duration) { c.current = c.current.Add(d) }

// logCapture is a logrus hook that records the "processed" field from every
// debug-level poll-completed entry. Tests assert that at least one entry was
// captured so a missing hook cannot falsely read as zero.
type logCapture struct {
	perPoll []int // processed count per captured entry
}

func (h *logCapture) Levels() []logrus.Level { return []logrus.Level{logrus.DebugLevel} }
func (h *logCapture) Fire(entry *logrus.Entry) error {
	if v, ok := entry.Data["processed"]; ok {
		if n, ok := v.(int); ok {
			h.perPoll = append(h.perPoll, n)
		}
	}
	return nil
}

func (h *logCapture) total() int {
	sum := 0
	for _, n := range h.perPoll {
		sum += n
	}
	return sum
}

func captureLogger() (logrus.FieldLogger, *logCapture) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	cap := &logCapture{}
	log.AddHook(cap)
	return log, cap
}

func TestMigrationRetryQueueFairness(t *testing.T) {
	// Retry queue [a, b, c]: "a" always fails and consumes 2×budget on every
	// GetFleet. "b" and "c" are healthy (cheap GetFleet succeeds). A cheap
	// listing returns fleet "d". Task is recreated between polls with the
	// same durable checkpoint service. After 6 polls:
	//  - "b" and "c" must have been processed (observed via debug log).
	//  - "a" must remain in the retry list (never succeeds).
	//  - "a" was attempted >= 2 times.
	//  - All unattempted names are retained (never dropped).
	//  - No operation starts after the poll deadline.
	//  - No premature completion while "a" is pending.
	//
	// Root cause: when the budget expires after a slow "a", unattempted
	// entries [b, c] were appended AFTER the already-requeued "a" in the
	// retry list. Next poll, "a" is first again. "b" and "c" are never
	// reached because "a" always consumes the budget.

	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	budget := time.Second
	config := PackageModeTemplateMigrationConfig{
		PageSize:   10,
		BatchSize:  100,
		TimeBudget: budget,
	}
	clock := newFakeClock()
	log, capture := captureLogger()

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Retry:   []string{"a", "b", "c"},
	})

	var getFleetACalls int
	var getFleetBCalls int
	var getFleetCCalls int
	var pollStarts []time.Time

	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			getFleetACalls++
			if len(pollStarts) > 0 {
				ps := pollStarts[len(pollStarts)-1]
				require.True(t, clock.Now().Sub(ps) < budget,
					"GetFleet retry must not start after the poll deadline")
			}
			clock.Advance(2 * budget) // always slow, always fails
			return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
		}).AnyTimes()

	// "b" and "c" are healthy retries — track each call explicitly.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "b", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			getFleetBCalls++
			if len(pollStarts) > 0 {
				ps := pollStarts[len(pollStarts)-1]
				require.True(t, clock.Now().Sub(ps) < budget,
					"GetFleet retry must not start after the poll deadline")
			}
			f := fleetWithImage("b", "")
			return &f, domain.StatusOK()
		}).AnyTimes()
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "c", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			getFleetCCalls++
			if len(pollStarts) > 0 {
				ps := pollStarts[len(pollStarts)-1]
				require.True(t, clock.Now().Sub(ps) < budget,
					"GetFleet retry must not start after the poll deadline")
			}
			f := fleetWithImage("c", "")
			return &f, domain.StatusOK()
		}).AnyTimes()

	// Cheap listing returns fleet "d".
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			if len(pollStarts) > 0 {
				ps := pollStarts[len(pollStarts)-1]
				require.True(t, clock.Now().Sub(ps) < budget,
					"ListFleets must not start after the poll deadline")
			}
			return fleetList(nil, fleetWithImage("d", "")), domain.StatusOK()
		}).AnyTimes()

	for poll := 0; poll < 6; poll++ {
		clock.current = newFakeClock().current.Add(time.Duration(poll) * 10 * time.Minute)
		pollStarts = append(pollStarts, clock.Now())
		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, log)
		require.NoError(t, err)
		task.now = clock.Now
		require.NoError(t, task.poll(t.Context(), orgID))
	}

	stored := checkpoints.stored(t, orgID)

	require.NotEmpty(t, capture.perPoll,
		"log capture must have recorded poll-completed entries")
	require.Contains(t, stored.Retry, "a",
		"permanently failing 'a' must remain in the retry list")
	require.GreaterOrEqual(t, getFleetACalls, 2,
		"'a' must be attempted at least twice across 6 polls")
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase,
		"must not complete while 'a' remains in the retry list")
	// The key fairness assertion: healthy retries "b" and "c" must have
	// been successfully retried and removed from the retry list. If they
	// remain, they were never reached because "a" consumed the budget first.
	require.NotContains(t, stored.Retry, "b",
		"healthy retry 'b' must have been processed and removed from the retry list")
	require.NotContains(t, stored.Retry, "c",
		"healthy retry 'c' must have been processed and removed from the retry list")
	require.GreaterOrEqual(t, getFleetBCalls, 1,
		"GetFleet('b') must have been called at least once")
	require.GreaterOrEqual(t, getFleetCCalls, 1,
		"GetFleet('c') must have been called at least once")
}

func TestMigrationRetryTailPreservedOnCancellation(t *testing.T) {
	// When a retry fails and the context is cancelled during drainRetries,
	// unattempted entries must be placed BEFORE the requeued failure so
	// they get a turn next poll. The error must preserve context.Canceled
	// identity through persist's errors.Join.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Retry:   []string{"a", "b", "c"},
	})

	ctx, cancel := context.WithCancel(t.Context())

	// "a" fails AND cancels — exercises the combination of a requeued
	// failure plus unattempted tail under cancellation.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			cancel()
			return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
		}).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	err = task.poll(ctx, orgID)

	// The poll must surface the cancellation.
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled,
		"cancellation identity must be preserved")

	stored := checkpoints.stored(t, orgID)
	// All three names must be retained: "b" and "c" unattempted, "a" requeued.
	require.Equal(t, 3, len(stored.Retry), "all retry names must be retained")
	require.Contains(t, stored.Retry, "a", "failed 'a' must be retained")
	require.Contains(t, stored.Retry, "b", "unattempted 'b' must be retained")
	require.Contains(t, stored.Retry, "c", "unattempted 'c' must be retained")
	// Unattempted entries must come before the requeued failure.
	require.Equal(t, []string{"b", "c", "a"}, stored.Retry,
		"unattempted [b,c] must precede requeued failure [a] for fair scheduling")
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase)
}

func TestMigrationCheckpointFinalizationContext(t *testing.T) {
	// Pre-canceled parent: "a" fails and cancels the parent before persist
	// is reached. The finalization context must still allow the write.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Retry:   []string{"a", "b"},
	})

	parentCtx, parentCancel := context.WithCancel(t.Context())
	type ctxKey struct{}
	parentCtx = context.WithValue(parentCtx, ctxKey{}, "test-value")

	var writeCtxSnapshot context.Context
	var writeCtxErrAtEntry error
	var writeDeadline time.Time
	var writeDeadlineOk bool
	var parentValueAtWrite any

	checkpoints.onSet = func(ctx context.Context) {
		writeCtxSnapshot = ctx
		writeCtxErrAtEntry = ctx.Err()
		writeDeadline, writeDeadlineOk = ctx.Deadline()
		parentValueAtWrite = ctx.Value(ctxKey{})
	}

	// "a" fails and cancels the parent context before persist is reached.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			parentCancel()
			return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
		}).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	err = task.poll(parentCtx, orgID)

	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, writeCtxErrAtEntry,
		"write context must not be cancelled at SetCheckpoint entry")
	require.True(t, writeDeadlineOk, "write context must have a deadline")
	require.True(t, time.Until(writeDeadline) <= 5*time.Second,
		"write context deadline must be bounded at <=5s")
	require.Equal(t, "test-value", parentValueAtWrite,
		"parent context values must propagate to the write context")
	require.Error(t, writeCtxSnapshot.Err(),
		"write context must be cancelled after persistCheckpoint returns (defer cleanup)")
	stored := checkpoints.stored(t, orgID)
	require.Equal(t, []string{"b", "a"}, stored.Retry,
		"rotated retry order must be durably saved despite parent cancellation")
}

func TestMigrationCheckpointWriteSurvivesLateCancelDuringWrite(t *testing.T) {
	// Late cancel: the parent is LIVE when persistCheckpoint creates the
	// finalization context. The parent is cancelled DURING SetCheckpoint
	// (inside the onSet hook). The write context must remain live after the
	// parent cancel because it was derived via WithoutCancel.
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)

	type ctxKey struct{}
	parentCtx, parentCancel := context.WithCancel(
		context.WithValue(t.Context(), ctxKey{}, "late-value"),
	)
	defer parentCancel()

	var writeCtxSnapshot context.Context
	var parentErrBeforeCancel error
	var parentErrAfterCancel error
	var writeErrAfterParentCancel error
	var writeDeadline time.Time
	var writeDeadlineOk bool
	var writeValue any

	checkpoints.onSet = func(ctx context.Context) {
		writeCtxSnapshot = ctx
		parentErrBeforeCancel = parentCtx.Err() // parent still live
		parentCancel()                          // cancel parent NOW
		parentErrAfterCancel = parentCtx.Err()  // parent is cancelled
		writeErrAfterParentCancel = ctx.Err()   // write ctx must still be live
		writeDeadline, writeDeadlineOk = ctx.Deadline()
		writeValue = ctx.Value(ctxKey{})
	}

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)

	run := &migrationRun{
		checkpoint: packageModeMigrationCheckpoint{
			Version: packageModeTemplateMigrationVersion,
			Phase:   packageModeMigrationRunning,
			Retry:   []string{"x"},
		},
	}
	require.NoError(t, task.persistCheckpoint(parentCtx, orgID, run.checkpoint))

	// Parent was live at entry, cancelled during write.
	require.NoError(t, parentErrBeforeCancel,
		"parent must be live when persistCheckpoint creates the write context")
	require.Error(t, parentErrAfterCancel,
		"parent must be cancelled after the hook cancels it")
	// Write context must survive the parent cancel.
	require.NoError(t, writeErrAfterParentCancel,
		"write context must remain live after parent is cancelled during SetCheckpoint")
	// Bounded deadline.
	require.True(t, writeDeadlineOk, "write context must have a deadline")
	remaining := time.Until(writeDeadline)
	require.True(t, remaining > 0 && remaining <= 5*time.Second,
		"write context deadline must be positive and bounded at <=5s")
	// Parent values propagated.
	require.Equal(t, "late-value", writeValue,
		"parent context values must propagate through WithoutCancel")
	// After persistCheckpoint returns, defer writeCancel() must have fired.
	require.Error(t, writeCtxSnapshot.Err(),
		"write context must be cancelled after persistCheckpoint returns (defer cleanup)")
	// Checkpoint data must have been saved.
	stored := checkpoints.stored(t, orgID)
	require.Equal(t, []string{"x"}, stored.Retry)
}

func TestMigrationPinnedMultiItemBudgetLiveness(t *testing.T) {
	// ListFleets always returns the same 3-item first page [a,b,c] with
	// Continue="page2", and a 1-item second page [d] with no Continue.
	// Every listing advances the fake clock by 2×TimeBudget. Clock reads
	// are frozen. 6 polls. The task is recreated between polls with the
	// same durable checkpoint service to rule out task-local flags.
	//
	// Assertions: the second page was requested, the checkpoint phase is
	// complete, and the total processed count across all polls includes
	// all 4 distinct fleets.
	//
	// Expected RED on the pre-fix code: the per-fleet budget check (whether
	// pre- or post-process) cannot advance the cursor past a multi-item page
	// whose listing always consumes the budget, because the same prefix is
	// replayed on every poll. The second page is never requested and
	// completion never occurs.

	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	budget := time.Second
	config := PackageModeTemplateMigrationConfig{
		PageSize:   3,
		BatchSize:  100,
		TimeBudget: budget,
	}
	clock := newFakeClock()
	log, capture := captureLogger()

	var listCalls []string // "nil" or the Continue value
	var page2Requested bool

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			cont := "nil"
			if params.Continue != nil {
				cont = *params.Continue
			}
			listCalls = append(listCalls, cont)
			if cont == "page2" {
				page2Requested = true
				clock.Advance(2 * budget)
				return fleetList(nil, fleetWithImage("d", "")), domain.StatusOK()
			}
			clock.Advance(2 * budget)
			return fleetList(lo.ToPtr("page2"),
				fleetWithImage("a", ""),
				fleetWithImage("b", ""),
				fleetWithImage("c", ""),
			), domain.StatusOK()
		}).AnyTimes()

	// Support GetFleet reads for a future resumable implementation.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, name string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			f := fleetWithImage(name, "")
			return &f, domain.StatusOK()
		}).AnyTimes()

	for poll := 0; poll < 6; poll++ {
		clock.current = newFakeClock().current.Add(time.Duration(poll) * 10 * time.Minute)
		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, log)
		require.NoError(t, err)
		task.now = clock.Now
		require.NoError(t, task.poll(t.Context(), orgID))
	}

	stored := checkpoints.stored(t, orgID)

	require.NotEmpty(t, capture.perPoll,
		"log capture must have recorded at least one poll-completed entry")
	require.True(t, page2Requested,
		"the second page must have been requested at least once across 6 polls")
	require.Equal(t, packageModeMigrationComplete, stored.Phase,
		"the migration must reach completion within 6 polls")
	require.Equal(t, 4, capture.total(),
		"exactly 4 fleets (a, b, c, d) must have been processed once each")
}

func TestMigrationPinnedPermanentSlowRetryFairness(t *testing.T) {
	// Seed durable retry "a". On EVERY GetFleet("a"), advance the fake
	// clock by 2×TimeBudget and return an error. It stays slow and failing
	// across all 6 polls — never cheaper, never succeeds, never disappears.
	// Fresh listing returns unrelated fleet "b" quickly (no clock advance).
	// The task is recreated between polls with the same checkpoint service.
	//
	// Assertions: "b" was processed (observed through the debug log), "a"
	// remains durable, "a" was attempted ≥ 2 times, no completion, and no
	// ListFleets or new retry call starts after the current poll's deadline.
	//
	// Expected RED on the pre-fix code: the retry always consumes the
	// budget and scanning never gets a turn because there is no fair
	// alternation. ListFleets is never called, so "b" is never processed.

	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	budget := time.Second
	config := PackageModeTemplateMigrationConfig{
		PageSize:   10,
		BatchSize:  100,
		TimeBudget: budget,
	}
	clock := newFakeClock()
	log, capture := captureLogger()

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version: packageModeTemplateMigrationVersion,
		Phase:   packageModeMigrationRunning,
		Retry:   []string{"a"},
	})

	var getFleetCalls int
	var listFleetCalls int
	// Record each poll's startedAt to verify no call starts past deadline.
	var pollStarts []time.Time

	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			getFleetCalls++
			// Verify this retry started before the poll deadline.
			if len(pollStarts) > 0 {
				ps := pollStarts[len(pollStarts)-1]
				require.True(t, clock.Now().Sub(ps) < budget,
					"GetFleet retry must not start after the poll deadline")
			}
			clock.Advance(2 * budget) // always slow, always fails
			return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
		}).AnyTimes()

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			listFleetCalls++
			// Verify this listing started before the poll deadline.
			if len(pollStarts) > 0 {
				ps := pollStarts[len(pollStarts)-1]
				require.True(t, clock.Now().Sub(ps) < budget,
					"ListFleets must not start after the poll deadline")
			}
			return fleetList(nil, fleetWithImage("b", "")), domain.StatusOK()
		}).AnyTimes()

	for poll := 0; poll < 6; poll++ {
		clock.current = newFakeClock().current.Add(time.Duration(poll) * 10 * time.Minute)
		pollStarts = append(pollStarts, clock.Now())
		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, log)
		require.NoError(t, err)
		task.now = clock.Now
		require.NoError(t, task.poll(t.Context(), orgID))
	}

	stored := checkpoints.stored(t, orgID)

	require.NotEmpty(t, capture.perPoll,
		"log capture must have recorded at least one poll-completed entry")
	require.Contains(t, stored.Retry, "a",
		"retry 'a' must remain durable after all 6 polls")
	require.GreaterOrEqual(t, getFleetCalls, 2,
		"retry 'a' must be attempted at least twice across 6 polls")
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase,
		"the task must not claim completion while 'a' remains in the retry list")
	require.Greater(t, listFleetCalls, 0,
		"ListFleets must have been called at least once so fresh fleet 'b' is scanned")
	require.Greater(t, capture.total(), 0,
		"fleet 'b' must have been processed at least once (observed via debug log)")
}

func TestMigrationPinnedNoNewFleetAfterExpiredListing(t *testing.T) {
	// A multi-item successful listing advances the fake clock past the ONE
	// poll deadline. After the expired listing, no new fleet operation
	// should start. The listing itself is an already-started indivisible
	// operation that may finish late, but processing its results is new
	// work. The task is recreated between polls with the same checkpoint
	// service to rule out task-local flags.
	//
	// Assertions: phase is "running" (no premature completion) AND the
	// actual processed count observed through the debug log is zero.
	//
	// Expected RED on the pre-fix code: the per-fleet loop processes
	// fleet "a" after the listing already consumed the budget. Even with
	// a pre-check, the original code started fleet processing because
	// the budget check was post-process. Phase is "running" (which alone
	// does not expose the violation), but processed=1 reveals that a
	// fleet operation started after the deadline.

	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	budget := time.Second
	config := PackageModeTemplateMigrationConfig{
		PageSize:   10,
		BatchSize:  100,
		TimeBudget: budget,
	}
	clock := newFakeClock()
	log, capture := captureLogger()

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			clock.Advance(2 * budget)
			return fleetList(nil,
				fleetWithImage("a", ""),
				fleetWithImage("b", ""),
				fleetWithImage("c", ""),
			), domain.StatusOK()
		}).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, log)
	require.NoError(t, err)
	task.now = clock.Now
	require.NoError(t, task.poll(t.Context(), orgID))

	stored := checkpoints.stored(t, orgID)

	require.NotEmpty(t, capture.perPoll,
		"log capture must have recorded the poll-completed entry")
	require.Equal(t, packageModeMigrationRunning, stored.Phase,
		"phase must be running: no premature completion after the listing consumed the budget")
	require.Equal(t, 0, capture.total(),
		"processed must be 0: no fleet operation should start after the listing consumed the budget")
}

func TestMigrationLateListParentCancelReturnsCanceled(t *testing.T) {
	// When a listing returns items AND the parent context is cancelled AND
	// the budget has expired, the poll must persist the pending page durably
	// AND return context.Canceled so callers can distinguish shutdowns from
	// normal budget stops. A live parent with the same budget stop returns nil.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	config := migrationTestConfig()
	config.TimeBudget = time.Minute

	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := epoch

	parentCtx, parentCancel := context.WithCancel(t.Context())

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
	require.NoError(t, err)
	task.now = func() time.Time { return now }

	// The listing succeeds but the parent is cancelled during the call and
	// the budget has expired by the time the result is inspected.
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			parentCancel()
			now = now.Add(2 * config.TimeBudget)
			return fleetList(lo.ToPtr("page2"),
				fleetWithImage("a", "quay.io/org/os:v1"),
			), domain.StatusOK()
		}).Times(1)

	err = task.poll(parentCtx, orgID)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled,
		"cancellation must be surfaced when the parent is cancelled during a late listing")

	stored := checkpoints.stored(t, orgID)
	require.Equal(t, []string{"a"}, stored.Pending,
		"the late page must be persisted as pending")
	require.NotNil(t, stored.NextCursor)
	require.Equal(t, "page2", *stored.NextCursor)

	// A live parent with the same budget stop must return nil.
	checkpoints2 := newFakeCheckpoints()
	now = epoch

	task2, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints2, config, testLogger())
	require.NoError(t, err)
	task2.now = func() time.Time { return now }

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			now = now.Add(2 * config.TimeBudget)
			return fleetList(lo.ToPtr("page2"),
				fleetWithImage("b", "quay.io/org/os:v1"),
			), domain.StatusOK()
		}).Times(1)

	err = task2.poll(t.Context(), orgID)
	require.NoError(t, err, "a live parent with budget stop must return nil")

	stored2 := checkpoints2.stored(t, orgID)
	require.Equal(t, []string{"b"}, stored2.Pending)

	// Empty terminal page + parent cancel: no completion on cancellation.
	checkpoints3 := newFakeCheckpoints()
	now = epoch

	parentCtx3, parentCancel3 := context.WithCancel(t.Context())

	task3, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints3, config, testLogger())
	require.NoError(t, err)
	task3.now = func() time.Time { return now }

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			parentCancel3()
			now = now.Add(2 * config.TimeBudget)
			return fleetList(nil), domain.StatusOK() // empty page, no continuation
		}).Times(1)

	err = task3.poll(parentCtx3, orgID)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled,
		"cancellation must be surfaced even on an empty terminal page")

	stored3 := checkpoints3.stored(t, orgID)
	require.NotEqual(t, packageModeMigrationComplete, stored3.Phase,
		"must not claim completion when the parent is cancelled")
}

func TestMigrationResumePendingRotatesUntouchedTailFirst(t *testing.T) {
	t.Run("budget stop rotates tail and processes healthy entries", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		fleetSvc := fleetservice.NewMockService(ctrl)
		checkpoints := newFakeCheckpoints()
		orgID := uuid.New()
		log, capture := captureLogger()

		budget := time.Second
		config := PackageModeTemplateMigrationConfig{
			PageSize:   10,
			BatchSize:  100,
			TimeBudget: budget,
		}
		clock := newFakeClock()

		retries := make([]string, maxPackageModeTemplateMigrationRetries)
		for i := range retries {
			retries[i] = fmt.Sprintf("r%d", i)
		}
		retrySet := make(map[string]bool, len(retries))
		for _, r := range retries {
			retrySet[r] = true
		}
		checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
			Version:    packageModeTemplateMigrationVersion,
			Phase:      packageModeMigrationRunning,
			Pending:    []string{"a", "b", "c"},
			NextCursor: lo.ToPtr("page2"),
			ScanFirst:  true,
			Retry:      retries,
		})

		var getFleetACalls int
		var getFleetBCalls int
		var getFleetCCalls int
		var pollStarts []time.Time

		fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
				if len(pollStarts) > 0 {
					require.True(t, clock.Now().Sub(pollStarts[len(pollStarts)-1]) < budget,
						"GetFleet('a') must not start after the poll deadline")
				}
				getFleetACalls++
				clock.Advance(2 * budget)
				return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
			}).AnyTimes()
		fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "b", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
				if len(pollStarts) > 0 {
					require.True(t, clock.Now().Sub(pollStarts[len(pollStarts)-1]) < budget,
						"GetFleet('b') must not start after the poll deadline")
				}
				getFleetBCalls++
				f := fleetWithImage("b", "")
				return &f, domain.StatusOK()
			}).Times(1)
		fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "c", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
				if len(pollStarts) > 0 {
					require.True(t, clock.Now().Sub(pollStarts[len(pollStarts)-1]) < budget,
						"GetFleet('c') must not start after the poll deadline")
				}
				getFleetCCalls++
				f := fleetWithImage("c", "")
				return &f, domain.StatusOK()
			}).Times(1)
		// Generic handler for the 256 seed retry names only.
		fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, name string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
				require.True(t, retrySet[name],
					"unexpected GetFleet name %q: only seed retry identities are allowed", name)
				if len(pollStarts) > 0 {
					require.True(t, clock.Now().Sub(pollStarts[len(pollStarts)-1]) < budget,
						"GetFleet('%s') must not start after the poll deadline", name)
				}
				return nil, domain.Status{Code: http.StatusInternalServerError, Message: "retry"}
			}).AnyTimes()

		for poll := 0; poll < 3; poll++ {
			clock.current = newFakeClock().current.Add(time.Duration(poll) * 10 * time.Minute)
			pollStarts = append(pollStarts, clock.Now())
			task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, log)
			require.NoError(t, err)
			task.now = clock.Now
			require.NoError(t, task.poll(t.Context(), orgID))

			stored := checkpoints.stored(t, orgID)
			require.Equal(t, maxPackageModeTemplateMigrationRetries, len(stored.Retry),
				"poll %d: all 256 retry identities must be retained", poll)
		}

		stored := checkpoints.stored(t, orgID)
		require.GreaterOrEqual(t, getFleetACalls, 2,
			"permanently failing 'a' must be attempted at least twice across polls")
		require.Contains(t, stored.Pending, "a",
			"permanently failing 'a' must remain in Pending")
		require.Equal(t, 1, getFleetBCalls, "healthy 'b' must be attempted exactly once")
		require.Equal(t, 1, getFleetCCalls, "healthy 'c' must be attempted exactly once")
		require.NotEmpty(t, capture.perPoll,
			"log capture must have recorded poll-completed entries")
		require.Equal(t, 2, capture.total(),
			"exactly two healthy fleets ('b' and 'c') must have been processed")
	})

	t.Run("cancellation rotates tail with exact pending order", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		fleetSvc := fleetservice.NewMockService(ctrl)
		checkpoints := newFakeCheckpoints()
		orgID := uuid.New()

		budget := time.Second
		config := PackageModeTemplateMigrationConfig{
			PageSize:   10,
			BatchSize:  100,
			TimeBudget: budget,
		}

		retries := make([]string, maxPackageModeTemplateMigrationRetries)
		for i := range retries {
			retries[i] = fmt.Sprintf("r%d", i)
		}
		checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
			Version:    packageModeTemplateMigrationVersion,
			Phase:      packageModeMigrationRunning,
			Pending:    []string{"a", "b", "c"},
			NextCursor: lo.ToPtr("page2"),
			ScanFirst:  true,
			Retry:      retries,
		})

		cancelCtx, cancelFn := context.WithCancel(t.Context())
		fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
				cancelFn()
				return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
			}).Times(1)
		// No GetFleet for "b" or "c" — context is cancelled before they are reached.

		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
		require.NoError(t, err)
		err = task.poll(cancelCtx, orgID)
		require.ErrorIs(t, err, context.Canceled)

		stored := checkpoints.stored(t, orgID)
		require.Equal(t, []string{"b", "c", "a"}, stored.Pending,
			"unattempted [b,c] must precede retained failure [a] on cancellation")
		require.Equal(t, maxPackageModeTemplateMigrationRetries, len(stored.Retry),
			"all 256 retry identities must be retained on cancellation")
	})
}

func TestMigrationPendingGetRetryNotDoubleAttempted(t *testing.T) {
	// When a pending GetFleet fails and the name is queued for retry,
	// drainRetries must not attempt it again in the same poll. The
	// per-poll attemptedRetries state must be shared.
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version:    packageModeTemplateMigrationVersion,
		Phase:      packageModeMigrationRunning,
		Pending:    []string{"a"},
		NextCursor: lo.ToPtr("page2"),
		ScanFirst:  true,
		Retry:      []string{"b"},
	})

	var getFleetACalls int
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			getFleetACalls++
			return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
		}).Times(1)

	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "b", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			f := fleetWithImage("b", "")
			return &f, domain.StatusOK()
		}).Times(1)

	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		Return(fleetList(nil), domain.StatusOK()).Times(1)

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, migrationTestConfig(), testLogger())
	require.NoError(t, err)
	require.NoError(t, task.poll(t.Context(), orgID))

	require.Equal(t, 1, getFleetACalls,
		"pending 'a' must be attempted exactly once; drainRetries must skip it")

	stored := checkpoints.stored(t, orgID)
	require.Contains(t, stored.Retry, "a",
		"failed 'a' must remain in retry for the next poll")
	require.NotContains(t, stored.Retry, "b",
		"healthy 'b' must have been processed and removed from retry")
}

func TestMigrationSharedAttemptsBothPhaseOrders(t *testing.T) {
	// Two-poll fixture verifying shared per-poll attempt tracking in BOTH
	// phase orders. Poll 1 (ScanFirst=true): listing produces pending [a,c]
	// and consumes budget so retry [a,b] is untouched. Poll 2 (ScanFirst=false):
	// drainRetries attempts "a" (fails) and "b" (succeeds); then resumePending
	// must NOT re-attempt "a" but must process "c".
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	checkpoints := newFakeCheckpoints()
	orgID := uuid.New()
	log, capture := captureLogger()

	budget := time.Second
	config := PackageModeTemplateMigrationConfig{
		PageSize:   10,
		BatchSize:  100,
		TimeBudget: budget,
	}
	clock := newFakeClock()

	checkpoints.seed(t, orgID, packageModeMigrationCheckpoint{
		Version:   packageModeTemplateMigrationVersion,
		Phase:     packageModeMigrationRunning,
		Retry:     []string{"a", "b"},
		ScanFirst: true,
	})

	var getFleetACalls int
	var pollStarts []time.Time

	// --- Poll 1 mocks (ScanFirst=true: scan first, then drain retries) ---
	// Listing returns [a, c] with NextCursor, consumes 2*budget.
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			if len(pollStarts) > 0 {
				require.True(t, clock.Now().Sub(pollStarts[len(pollStarts)-1]) < budget,
					"ListFleets must not start after the poll deadline")
			}
			clock.Advance(2 * budget) // consumes budget
			return fleetList(lo.ToPtr("page3"),
				fleetWithImage("a", "quay.io/org/os:v1"),
				fleetWithImage("c", "quay.io/org/os:v2"),
			), domain.StatusOK()
		}).Times(1)

	// --- Poll 2 mocks (ScanFirst=false: drain retries first, then scan) ---
	// "a" fails cheaply in drainRetries.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "a", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			if len(pollStarts) > 0 {
				require.True(t, clock.Now().Sub(pollStarts[len(pollStarts)-1]) < budget,
					"GetFleet('a') must not start after the poll deadline")
			}
			getFleetACalls++
			return nil, domain.Status{Code: http.StatusInternalServerError, Message: "boom"}
		}).Times(1) // exactly once: drainRetries attempts, resumePending must skip

	// "b" succeeds in drainRetries.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "b", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			if len(pollStarts) > 0 {
				require.True(t, clock.Now().Sub(pollStarts[len(pollStarts)-1]) < budget,
					"GetFleet('b') must not start after the poll deadline")
			}
			f := fleetWithImage("b", "")
			return &f, domain.StatusOK()
		}).Times(1)

	// "c" succeeds in resumePending.
	fleetSvc.EXPECT().GetFleet(gomock.Any(), orgID, "c", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			if len(pollStarts) > 0 {
				require.True(t, clock.Now().Sub(pollStarts[len(pollStarts)-1]) < budget,
					"GetFleet('c') must not start after the poll deadline")
			}
			f := fleetWithImage("c", "")
			return &f, domain.StatusOK()
		}).Times(1)

	// Poll 2: after pending drained, listing from page3.
	fleetSvc.EXPECT().ListFleets(gomock.Any(), orgID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
			require.Equal(t, "page3", lo.FromPtr(params.Continue),
				"poll 2 listing must start from the saved NextCursor")
			return fleetList(nil), domain.StatusOK()
		}).Times(1)

	// --- Run 2 polls ---
	for poll := 0; poll < 2; poll++ {
		clock.current = newFakeClock().current.Add(time.Duration(poll) * 10 * time.Minute)
		pollStarts = append(pollStarts, clock.Now())
		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, log)
		require.NoError(t, err)
		task.now = clock.Now
		require.NoError(t, task.poll(t.Context(), orgID))
	}

	require.Equal(t, 1, getFleetACalls,
		"'a' must be attempted exactly once across both phases in poll 2")

	stored := checkpoints.stored(t, orgID)
	require.Contains(t, stored.Retry, "a",
		"failed 'a' must remain in retry for the next poll")
	require.NotContains(t, stored.Retry, "b",
		"healthy 'b' must have been processed and removed from retry")
	require.Empty(t, stored.Pending,
		"all pending must be drained")
	require.NotEqual(t, packageModeMigrationComplete, stored.Phase,
		"must not complete while 'a' remains in retry")

	require.NotEmpty(t, capture.perPoll)
	total := capture.total()
	require.Equal(t, 2, total,
		"exactly 'b' and 'c' must have been processed (total across polls)")
}
