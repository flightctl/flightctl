package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type mappingScanMockState struct {
	targets          []labelsyncmappingservice.MappingScanToken
	targetErr        error
	reconciled       []string
	reconcileResults map[string]labelsyncmappingservice.ReconciliationResult
	reconcileErr     map[string]error
	failures         []mappingScanFailureCall
	failureTokens    map[uuid.UUID]labelsyncmappingservice.MappingScanToken
	failureSaved     map[uuid.UUID]bool
	failureErr       error
	completed        [][]labelsyncmappingservice.MappingScanToken
	completeResults  map[uuid.UUID]bool
	completeErr      error
}

type mappingScanFailureCall struct {
	token   labelsyncmappingservice.MappingScanToken
	message string
}

func newMappingScanServiceMock(ctrl *gomock.Controller, state *mappingScanMockState) *labelsyncmappingservice.MockService {
	mock := labelsyncmappingservice.NewMockService(ctrl)
	mock.EXPECT().ListMappingScanTargets(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, uuid.UUID) ([]labelsyncmappingservice.MappingScanToken, error) {
			return state.targets, state.targetErr
		},
	).AnyTimes()
	mock.EXPECT().ReconcileDeviceLabels(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ uuid.UUID, name string) (labelsyncmappingservice.ReconciliationResult, error) {
			state.reconciled = append(state.reconciled, name)
			return state.reconcileResults[name], state.reconcileErr[name]
		},
	).AnyTimes()
	mock.EXPECT().RecordMappingScanFailure(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ uuid.UUID, token labelsyncmappingservice.MappingScanToken, message string) (labelsyncmappingservice.MappingScanToken, bool, error) {
			state.failures = append(state.failures, mappingScanFailureCall{token: token, message: message})
			updated, exists := state.failureTokens[token.MappingID]
			if !exists {
				updated = token
				updated.FailureRevision++
			}
			saved := true
			if value, exists := state.failureSaved[token.MappingID]; exists {
				saved = value
			}
			if saved && state.failureErr == nil {
				for i := range state.targets {
					if state.targets[i].MappingID == token.MappingID {
						state.targets[i] = updated
					}
				}
			}
			return updated, saved, state.failureErr
		},
	).AnyTimes()
	mock.EXPECT().CompleteMappingScan(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ uuid.UUID, tokens []labelsyncmappingservice.MappingScanToken) (map[uuid.UUID]bool, error) {
			state.completed = append(state.completed, append([]labelsyncmappingservice.MappingScanToken(nil), tokens...))
			completed := state.completeResults
			if completed == nil {
				completed = map[uuid.UUID]bool{}
				for _, token := range tokens {
					completed[token.MappingID] = true
				}
			}
			if state.completeErr == nil {
				remaining := state.targets[:0]
				for _, target := range state.targets {
					if !completed[target.MappingID] {
						remaining = append(remaining, target)
					}
				}
				state.targets = remaining
			}
			return completed, state.completeErr
		},
	).AnyTimes()
	return mock
}

type mappingScanCheckpointHarness struct {
	getStatus domain.Status
	setStatus domain.Status
	data      []byte
	writes    [][]byte
	getCalls  int
	setCalls  int
}

func newMappingScanCheckpointMock(ctrl *gomock.Controller, orgID uuid.UUID, initial []byte) (*checkpointservice.MockService, *mappingScanCheckpointHarness) {
	harness := &mappingScanCheckpointHarness{data: append([]byte(nil), initial...)}
	mock := checkpointservice.NewMockService(ctrl)
	mock.EXPECT().GetCheckpoint(gomock.Any(), mappingScanCheckpointConsumer, orgID.String()).DoAndReturn(
		func(context.Context, string, string) ([]byte, domain.Status) {
			harness.getCalls++
			if harness.getStatus.Code != 0 {
				return nil, harness.getStatus
			}
			if harness.data == nil {
				return nil, domain.StatusResourceNotFound("Checkpoint", orgID.String())
			}
			return append([]byte(nil), harness.data...), domain.StatusOK()
		},
	).AnyTimes()
	mock.EXPECT().SetCheckpoint(gomock.Any(), mappingScanCheckpointConsumer, orgID.String(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _, _ string, data []byte) domain.Status {
			harness.setCalls++
			if harness.setStatus.Code != 0 {
				return harness.setStatus
			}
			checkpointBytes := append([]byte(nil), data...)
			harness.data = checkpointBytes
			harness.writes = append(harness.writes, checkpointBytes)
			return domain.StatusOK()
		},
	).AnyTimes()
	return mock, harness
}

func TestNewMappingScanTaskRejectsInvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config LabelMappingScanConfig
	}{
		{name: "When page size is zero it should be rejected", config: LabelMappingScanConfig{PageSize: 0, TimeBudget: time.Second}},
		{name: "When page size exceeds the device list maximum it should be rejected", config: LabelMappingScanConfig{PageSize: 1001, TimeBudget: time.Second}},
		{name: "When time budget is non-positive it should be rejected", config: LabelMappingScanConfig{PageSize: 100, TimeBudget: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			reconciler := newMappingScanServiceMock(ctrl, &mappingScanMockState{})
			_, err := NewLabelMappingScanTask(reconciler, deviceservice.NewMockService(ctrl), checkpointservice.NewMockService(ctrl), tc.config, logrus.New())
			require.Error(t, err)
		})
	}
	t.Run("When page size is at the maximum it should be accepted", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		reconciler := newMappingScanServiceMock(ctrl, &mappingScanMockState{})
		_, err := NewLabelMappingScanTask(reconciler, deviceservice.NewMockService(ctrl), checkpointservice.NewMockService(ctrl), LabelMappingScanConfig{PageSize: maxMappingScanPageSize, TimeBudget: time.Second}, logrus.New())
		require.NoError(t, err)
	})
}

func mappingScanTestDevice(name string) domain.Device {
	return domain.Device{Metadata: domain.ObjectMeta{Name: &name}}
}
func decodeMappingScanCheckpointBytes(t *testing.T, data []byte) mappingScanCheckpoint {
	t.Helper()
	var checkpoint mappingScanCheckpoint
	require.NoError(t, json.Unmarshal(data, &checkpoint))
	return checkpoint
}
func newScanTest(t *testing.T, state *mappingScanMockState, initial *mappingScanCheckpoint) (*LabelMappingScanTask, *deviceservice.MockService, *mappingScanCheckpointHarness, uuid.UUID) {
	t.Helper()
	ctrl := gomock.NewController(t)
	orgID := uuid.New()
	var data []byte
	if initial != nil {
		var err error
		data, err = json.Marshal(initial)
		require.NoError(t, err)
	}
	checkpoints, harness := newMappingScanCheckpointMock(ctrl, orgID, data)
	devices := deviceservice.NewMockService(ctrl)
	task, err := NewLabelMappingScanTask(newMappingScanServiceMock(ctrl, state), devices, checkpoints, LabelMappingScanConfig{PageSize: 1, TimeBudget: time.Nanosecond}, logrus.New())
	require.NoError(t, err)
	return task, devices, harness, orgID
}
func expectFailureCheck(t *testing.T, devices *deviceservice.MockService, orgID uuid.UUID, names []string) {
	t.Helper()
	devices.EXPECT().ListDevicesByServiceCondition(gomock.Any(), orgID, "LabelsSynced", "False", gomock.Any()).DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, _ string, params store.ListParams) (*domain.DeviceList, domain.Status) {
		require.Equal(t, 1, params.Limit)
		require.Nil(t, params.Continue)
		page := &domain.DeviceList{}
		for _, name := range names {
			page.Items = append(page.Items, mappingScanTestDevice(name))
		}
		return page, domain.StatusOK()
	})
}
func expectFullPage(t *testing.T, devices *deviceservice.MockService, orgID uuid.UUID, after *string, names []string, next *string) {
	t.Helper()
	devices.EXPECT().ListDevices(gomock.Any(), orgID, gomock.Any(), gomock.Nil()).DoAndReturn(func(_ context.Context, _ uuid.UUID, params domain.ListDevicesParams, _ any) (*domain.DeviceList, domain.Status) {
		require.Equal(t, after, params.Continue)
		require.EqualValues(t, 1, lo.FromPtr(params.Limit))
		page := &domain.DeviceList{Metadata: domain.ListMeta{Continue: next}}
		for _, name := range names {
			page.Items = append(page.Items, mappingScanTestDevice(name))
		}
		return page, domain.StatusOK()
	})
}

func TestMappingScanProgressAndRecovery(t *testing.T) {
	t.Run("When an early device fails it should finish the full pass and wait for device events to repair it", func(t *testing.T) {
		token := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		clean := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{token, clean}, reconcileResults: map[string]labelsyncmappingservice.ReconciliationResult{"a": {MappingOutcomes: []labelsyncmappingservice.MappingOutcome{{MappingID: token.MappingID, Err: errors.New("invalid output")}}}}}
		task, devices, harness, orgID := newScanTest(t, state, nil)
		fullCursor := store.BuildContinueString([]string{"a"}, 1)
		expectFullPage(t, devices, orgID, nil, []string{"a"}, fullCursor)
		task.Poll(context.Background(), orgID)
		checkpoint := decodeMappingScanCheckpointBytes(t, harness.data)
		require.Equal(t, fullCursor, checkpoint.Cursor)
		require.True(t, checkpoint.Mappings[0].Failed)
		require.Empty(t, state.failures)
		expectFullPage(t, devices, orgID, fullCursor, []string{"z"}, nil)
		expectFailureCheck(t, devices, orgID, []string{"a"})
		task.Poll(context.Background(), orgID)
		checkpoint = decodeMappingScanCheckpointBytes(t, harness.data)
		require.False(t, checkpoint.Active)
		require.Len(t, checkpoint.Recovery, 1)
		require.Equal(t, [][]labelsyncmappingservice.MappingScanToken{{clean}}, state.completed)
		require.Len(t, state.failures, 1)
		expectFailureCheck(t, devices, orgID, []string{"a"})
		task.Poll(context.Background(), orgID)
		require.Len(t, state.completed, 1)
		require.Equal(t, []string{"a", "z"}, state.reconciled)
		// Event reconciliation has repaired the device; periodic only updates
		// the mapping status and never revisits healthy or failed devices.
		expectFailureCheck(t, devices, orgID, nil)
		task.Poll(context.Background(), orgID)
		require.Len(t, state.completed, 2)
		require.Equal(t, int64(1), state.completed[1][0].FailureRevision)
		require.Empty(t, decodeMappingScanCheckpointBytes(t, harness.data).Recovery)
		require.Equal(t, []string{"a", "z"}, state.reconciled)
	})
	t.Run("When there are no mapping targets it should avoid device queries and reconciliation", func(t *testing.T) {
		state := &mappingScanMockState{}
		task, _, _, orgID := newScanTest(t, state, nil)
		task.Poll(context.Background(), orgID)
		require.Empty(t, state.reconciled)
		require.Empty(t, state.completed)
	})
	t.Run("When every active token becomes stale it should finish the saved traversal before restarting new coverage", func(t *testing.T) {
		old := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		current := old
		current.Generation = 2
		cursor := store.BuildContinueString([]string{"a"}, 1)
		checkpoint := &mappingScanCheckpoint{Version: 1, Active: true, Cursor: cursor, Mappings: []mappingScanProgress{{Token: old}}}
		state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{current}}
		task, devices, harness, orgID := newScanTest(t, state, checkpoint)
		expectFullPage(t, devices, orgID, cursor, []string{"z"}, nil)
		task.Poll(context.Background(), orgID)
		require.Empty(t, state.completed)
		require.False(t, decodeMappingScanCheckpointBytes(t, harness.data).Active)
		expectFullPage(t, devices, orgID, nil, []string{"a"}, nil)
		task.Poll(context.Background(), orgID)
		require.Equal(t, [][]labelsyncmappingservice.MappingScanToken{{current}}, state.completed)
	})
}

func TestMappingScanFailuresRetainSafeWork(t *testing.T) {
	for _, tc := range []struct {
		name          string
		reconcileErr  error
		deleted       bool
		shouldAdvance bool
		failed        bool
	}{
		{"When the device condition cannot be persisted it should retain the page", labelsyncmappingservice.ErrConditionPersistence, false, false, false},
		{"When a device call times out while the poll is live it should advance with all targets failed", context.DeadlineExceeded, false, true, true},
		{"When a device is confirmed deleted it should skip it", nil, true, true, false},
		{"When a foreign key returns not found for an existing device it should retain failure attribution", errors.New("owner not found"), false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
			state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{token}, reconcileErr: map[string]error{"a": tc.reconcileErr}, reconcileResults: map[string]labelsyncmappingservice.ReconciliationResult{"a": {DeviceDeleted: tc.deleted}}}
			task, devices, harness, orgID := newScanTest(t, state, nil)
			cursor := store.BuildContinueString([]string{"a"}, 1)
			expectFullPage(t, devices, orgID, nil, []string{"a"}, cursor)
			task.Poll(context.Background(), orgID)
			if tc.shouldAdvance {
				checkpoint := decodeMappingScanCheckpointBytes(t, harness.data)
				require.Equal(t, cursor, checkpoint.Cursor)
				require.Equal(t, tc.failed, checkpoint.Mappings[0].Failed)
			} else {
				require.Empty(t, harness.writes)
			}
		})
	}
	t.Run("When a whole-device apply fails before commit it should leave every campaign target unconfirmed", func(t *testing.T) {
		first := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		second := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{first, second}, reconcileErr: map[string]error{"a": errors.New("apply failed"), "b": errors.New("apply failed")}}
		task, devices, harness, orgID := newScanTest(t, state, nil)
		expectFullPage(t, devices, orgID, nil, []string{"a", "b"}, nil)
		expectFailureCheck(t, devices, orgID, []string{"a"})
		task.Poll(context.Background(), orgID)
		require.Len(t, state.failures, 2)
		require.Empty(t, state.completed)
		require.Len(t, decodeMappingScanCheckpointBytes(t, harness.data).Recovery, 2)
	})
	t.Run("When mapping labels commit before verification fails it should degrade only mappings with failed outcomes", func(t *testing.T) {
		failed := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		clean := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		state := &mappingScanMockState{
			targets: []labelsyncmappingservice.MappingScanToken{failed, clean},
			reconcileResults: map[string]labelsyncmappingservice.ReconciliationResult{
				"a": {
					MappingApplyCommitted: true,
					MappingOutcomes: []labelsyncmappingservice.MappingOutcome{
						{MappingID: failed.MappingID, Err: errors.New("mapping conflict")},
						{MappingID: clean.MappingID},
					},
				},
			},
			reconcileErr: map[string]error{"a": errors.New("verification read failed")},
		}
		task, devices, harness, orgID := newScanTest(t, state, nil)
		expectFullPage(t, devices, orgID, nil, []string{"a"}, nil)
		expectFailureCheck(t, devices, orgID, []string{"a"})

		task.Poll(context.Background(), orgID)

		require.Len(t, state.failures, 1)
		require.Equal(t, failed.MappingID, state.failures[0].token.MappingID)
		require.Equal(t, [][]labelsyncmappingservice.MappingScanToken{{clean}}, state.completed)
		checkpoint := decodeMappingScanCheckpointBytes(t, harness.data)
		require.Len(t, checkpoint.Recovery, 1)
		require.Equal(t, failed.MappingID, checkpoint.Recovery[0].Token.MappingID)
	})
	t.Run("When completion fails it should replay full coverage without listing the full fleet again", func(t *testing.T) {
		token := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{token}, completeErr: errors.New("database unavailable")}
		task, devices, harness, orgID := newScanTest(t, state, nil)
		expectFullPage(t, devices, orgID, nil, []string{"a"}, nil)
		task.Poll(context.Background(), orgID)
		require.True(t, decodeMappingScanCheckpointBytes(t, harness.data).ScanComplete)
		state.completeErr = nil
		task.Poll(context.Background(), orgID)
		require.Len(t, state.completed, 2)
		require.Equal(t, []string{"a"}, state.reconciled)
	})
	t.Run("When clean completion fails it should retain the published failure fence without degrading twice", func(t *testing.T) {
		failed := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		clean := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
		state := &mappingScanMockState{
			targets:          []labelsyncmappingservice.MappingScanToken{failed, clean},
			reconcileResults: map[string]labelsyncmappingservice.ReconciliationResult{"a": {MappingOutcomes: []labelsyncmappingservice.MappingOutcome{{MappingID: failed.MappingID, Err: errors.New("invalid output")}}}},
			completeErr:      errors.New("unavailable"),
		}
		task, devices, harness, orgID := newScanTest(t, state, nil)
		expectFullPage(t, devices, orgID, nil, []string{"a"}, nil)
		require.Error(t, task.poll(context.Background(), orgID))
		checkpoint := decodeMappingScanCheckpointBytes(t, harness.data)
		require.True(t, checkpoint.ScanComplete)
		require.Equal(t, int64(1), checkpoint.Mappings[0].Token.FailureRevision)
		require.Nil(t, checkpoint.Mappings[0].FailureMessage)
		state.completeErr = nil
		expectFailureCheck(t, devices, orgID, []string{"a"})
		require.NoError(t, task.poll(context.Background(), orgID))
		require.Len(t, state.failures, 1)
		require.Equal(t, []string{"a"}, state.reconciled)
		require.Len(t, decodeMappingScanCheckpointBytes(t, harness.data).Recovery, 1)
	})
	t.Run("When the poll is canceled it should stop before listing devices", func(t *testing.T) {
		task, _, harness, orgID := newScanTest(t, &mappingScanMockState{}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		task.Poll(ctx, orgID)
		require.Empty(t, harness.writes)
	})
}

func TestMappingScanCheckpointValidation(t *testing.T) {
	token := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
	for _, checkpoint := range []mappingScanCheckpoint{
		{Version: 2}, {Version: 1, ScanComplete: true}, {Version: 1, Cursor: lo.ToPtr("cursor")},
		{Version: 1, Active: true, Mappings: []mappingScanProgress{{Token: token}, {Token: token}}},
		{Version: 1, Active: true, Cursor: lo.ToPtr("invalid"), Mappings: []mappingScanProgress{{Token: token}}},
	} {
		require.False(t, validMappingScanCheckpoint(checkpoint))
	}
	require.True(t, validMappingScanCheckpoint(mappingScanCheckpoint{Version: 1, Recovery: []mappingScanProgress{{Token: token, Failed: true}}}))
	t.Run("When an unsupported checkpoint is loaded it should restart coverage", func(t *testing.T) {
		state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{token}}
		task, devices, _, orgID := newScanTest(t, state, &mappingScanCheckpoint{Version: 2, Cursor: lo.ToPtr("invalid")})
		expectFullPage(t, devices, orgID, nil, []string{"a"}, nil)
		task.Poll(context.Background(), orgID)
		require.Len(t, state.completed, 1)
	})
}

func TestMappingScanInfrastructureErrors(t *testing.T) {
	for _, kind := range []string{"checkpoint load", "device listing", "checkpoint write"} {
		t.Run("When "+kind+" fails it should retain safe work", func(t *testing.T) {
			token := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
			state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{token}}
			task, devices, harness, orgID := newScanTest(t, state, nil)
			switch kind {
			case "checkpoint load":
				harness.getStatus = domain.StatusInternalServerError("database unavailable")
			case "device listing":
				devices.EXPECT().ListDevices(gomock.Any(), orgID, gomock.Any(), gomock.Nil()).Return(nil, domain.StatusInternalServerError("database unavailable"))
			case "checkpoint write":
				harness.setStatus = domain.StatusInternalServerError("database unavailable")
				expectFullPage(t, devices, orgID, nil, []string{"a"}, store.BuildContinueString([]string{"a"}, 1))
			}
			task.Poll(context.Background(), orgID)
			require.Empty(t, harness.data)
			require.Empty(t, state.completed)
		})
	}
}

func TestNewMappingCoverageWithDegradedMappings(t *testing.T) {
	old := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
	newToken := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 2}
	checkpoint := &mappingScanCheckpoint{Version: 1, Recovery: []mappingScanProgress{{Token: old, Failed: true}}}
	state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{old, newToken}}
	task, devices, harness, orgID := newScanTest(t, state, checkpoint)
	expectFullPage(t, devices, orgID, nil, []string{"later"}, nil)
	expectFailureCheck(t, devices, orgID, []string{"failed"})
	task.Poll(context.Background(), orgID)
	require.False(t, decodeMappingScanCheckpointBytes(t, harness.data).Active)
	require.Len(t, decodeMappingScanCheckpointBytes(t, harness.data).Recovery, 1)
	require.Equal(t, [][]labelsyncmappingservice.MappingScanToken{{newToken}}, state.completed)
	require.Equal(t, []string{"later"}, state.reconciled)
}

func TestDegradedMappingChangesRequireFullCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*labelsyncmappingservice.MappingScanToken)
	}{
		{"When a mapping generation changes it should receive a new full pass", func(token *labelsyncmappingservice.MappingScanToken) { token.Generation++ }},
		{"When a mapping is recreated it should receive a new full pass", func(token *labelsyncmappingservice.MappingScanToken) { token.MappingID = uuid.New() }},
		{"When a mapping starts deletion it should receive a new cleanup pass", func(token *labelsyncmappingservice.MappingScanToken) { token.DeletionRevision = lo.ToPtr(int64(2)) }},
		{"When a failure fence changes it should rebuild full coverage", func(token *labelsyncmappingservice.MappingScanToken) { token.FailureRevision++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1}
			current := old
			tc.change(&current)
			checkpoint := &mappingScanCheckpoint{Version: 1, Recovery: []mappingScanProgress{{Token: old, Failed: true}}}
			state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{current}}
			task, devices, harness, orgID := newScanTest(t, state, checkpoint)
			cursor := store.BuildContinueString([]string{"a"}, 1)
			expectFullPage(t, devices, orgID, nil, []string{"a"}, cursor)
			task.Poll(context.Background(), orgID)
			updated := decodeMappingScanCheckpointBytes(t, harness.data)
			require.True(t, updated.Active)
			require.Equal(t, cursor, updated.Cursor)
			require.Empty(t, updated.Recovery)
			require.Equal(t, current, updated.Mappings[0].Token)
			require.Empty(t, state.completed)
		})
	}
}

func TestDegradedMappingCompletionRetainsCoverageOnErrors(t *testing.T) {
	for _, kind := range []string{"failure query", "completion"} {
		t.Run("When "+kind+" fails it should retain finished coverage without reconciling devices", func(t *testing.T) {
			token := labelsyncmappingservice.MappingScanToken{MappingID: uuid.New(), Generation: 1, FailureRevision: 1}
			checkpoint := &mappingScanCheckpoint{Version: 1, Recovery: []mappingScanProgress{{Token: token, Failed: true}}}
			state := &mappingScanMockState{targets: []labelsyncmappingservice.MappingScanToken{token}}
			task, devices, harness, orgID := newScanTest(t, state, checkpoint)
			if kind == "failure query" {
				devices.EXPECT().ListDevicesByServiceCondition(gomock.Any(), orgID, "LabelsSynced", "False", gomock.Any()).Return(nil, domain.StatusInternalServerError("unavailable"))
			} else {
				state.completeErr = errors.New("unavailable")
				expectFailureCheck(t, devices, orgID, nil)
			}
			require.Error(t, task.poll(context.Background(), orgID))
			require.Empty(t, harness.writes)
			require.Len(t, decodeMappingScanCheckpointBytes(t, harness.data).Recovery, 1)
			require.Empty(t, state.reconciled)
			state.completeErr = nil
			expectFailureCheck(t, devices, orgID, nil)
			require.NoError(t, task.poll(context.Background(), orgID))
			require.Empty(t, decodeMappingScanCheckpointBytes(t, harness.data).Recovery)
			require.Empty(t, state.reconciled)
		})
	}
}
