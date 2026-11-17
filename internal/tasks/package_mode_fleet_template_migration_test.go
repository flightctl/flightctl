package tasks

import (
	"context"
	"encoding/json"
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
	values map[string][]byte
	writes int
	getErr *domain.Status
	setErr *domain.Status
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

func (f *fakeCheckpoints) SetCheckpoint(_ context.Context, consumer, key string, value []byte) domain.Status {
	if f.setErr != nil {
		return *f.setErr
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
	config.TimeBudget = time.Nanosecond

	task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
	require.NoError(t, err)
	time.Sleep(time.Millisecond)
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
		// No fleet service call is expected: the budget is spent on arrival.
		fleetSvc := fleetservice.NewMockService(ctrl)
		checkpoints := newFakeCheckpoints()
		orgID := uuid.New()
		checkpoints.seed(t, orgID, seeded)

		config := migrationTestConfig()
		config.TimeBudget = time.Nanosecond

		task, err := NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpoints, config, testLogger())
		require.NoError(t, err)
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
