package tasks

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// mockImageBuildServiceForStatusUpdater is a mock for testing status updater
type mockImageBuildServiceForStatusUpdater struct {
	ctrl            *gomock.Controller
	imageBuild      *api.ImageBuild
	mu              sync.RWMutex
	getCalls        int
	updateCalls     int
	updateLogsCalls int
}

func newMockImageBuildServiceForStatusUpdater(ctrl *gomock.Controller, imageBuild *api.ImageBuild) *mockImageBuildServiceForStatusUpdater {
	return &mockImageBuildServiceForStatusUpdater{
		ctrl:       ctrl,
		imageBuild: imageBuild,
	}
}

func (m *mockImageBuildServiceForStatusUpdater) Get(ctx context.Context, orgId uuid.UUID, name string, withExports bool) (*api.ImageBuild, v1beta1.Status) {
	m.mu.Lock()
	m.getCalls++
	m.mu.Unlock()
	if m.imageBuild == nil {
		return nil, v1beta1.Status{Code: 404, Message: "not found"}
	}
	return m.imageBuild, v1beta1.StatusOK()
}

func (m *mockImageBuildServiceForStatusUpdater) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error) {
	m.mu.Lock()
	m.updateCalls++
	if m.imageBuild != nil {
		m.imageBuild = imageBuild
	}
	m.mu.Unlock()
	return imageBuild, nil
}

func (m *mockImageBuildServiceForStatusUpdater) UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error {
	m.mu.Lock()
	m.updateLogsCalls++
	m.mu.Unlock()
	return nil
}

func (m *mockImageBuildServiceForStatusUpdater) Create(ctx context.Context, orgId uuid.UUID, imageBuild api.ImageBuild) (*api.ImageBuild, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageBuildServiceForStatusUpdater) List(ctx context.Context, orgId uuid.UUID, params api.ListImageBuildsParams) (*api.ImageBuildList, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageBuildServiceForStatusUpdater) Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageBuildServiceForStatusUpdater) Cancel(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, error) {
	return nil, nil
}

func (m *mockImageBuildServiceForStatusUpdater) CancelWithReason(ctx context.Context, orgId uuid.UUID, name string, reason string) (*api.ImageBuild, error) {
	return nil, nil
}

func (m *mockImageBuildServiceForStatusUpdater) GetLogs(ctx context.Context, orgId uuid.UUID, name string, follow bool) (imagebuilderapi.LogStreamReader, string, v1beta1.Status) {
	return nil, "", v1beta1.StatusOK()
}

func (m *mockImageBuildServiceForStatusUpdater) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return nil
}

func (m *mockImageBuildServiceForStatusUpdater) getCallsCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getCalls
}

func (m *mockImageBuildServiceForStatusUpdater) getUpdateCallsCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.updateCalls
}

func (m *mockImageBuildServiceForStatusUpdater) getUpdateLogsCallsCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.updateLogsCalls
}

// mockKVStore is a mock KVStore for testing
type mockKVStore struct {
	mu             sync.RWMutex
	streamAddCalls int
	setExpireCalls int
	pushedValues   [][]byte
	keys           []string
}

func newMockKVStore() *mockKVStore {
	return &mockKVStore{
		pushedValues: make([][]byte, 0),
		keys:         make([]string, 0),
	}
}

func (m *mockKVStore) StreamAdd(ctx context.Context, key string, value []byte) (string, error) {
	m.mu.Lock()
	m.streamAddCalls++
	m.keys = append(m.keys, key)
	m.pushedValues = append(m.pushedValues, value)
	m.mu.Unlock()
	return "0-0", nil
}

func (m *mockKVStore) SetExpire(ctx context.Context, key string, expiration time.Duration) error {
	m.mu.Lock()
	m.setExpireCalls++
	m.mu.Unlock()
	return nil
}

func (m *mockKVStore) getStreamAddCalls() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.streamAddCalls
}

func (m *mockKVStore) getSetExpireCalls() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.setExpireCalls
}

func (m *mockKVStore) getPushedValues() [][]byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([][]byte, len(m.pushedValues))
	copy(result, m.pushedValues)
	return result
}

func (m *mockKVStore) Close() {}
func (m *mockKVStore) SetNX(ctx context.Context, key string, value []byte) (bool, error) {
	return false, nil
}
func (m *mockKVStore) SetIfGreater(ctx context.Context, key string, newVal int64) (bool, error) {
	return false, nil
}
func (m *mockKVStore) Get(ctx context.Context, key string) ([]byte, error) { return nil, nil }
func (m *mockKVStore) GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error) {
	return nil, nil
}
func (m *mockKVStore) DeleteKeysForTemplateVersion(ctx context.Context, key string) error { return nil }
func (m *mockKVStore) DeleteAllKeys(ctx context.Context) error                            { return nil }
func (m *mockKVStore) PrintAllKeys(ctx context.Context)                                   {}
func (m *mockKVStore) StreamRange(ctx context.Context, key string, start, stop string) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (m *mockKVStore) StreamRead(ctx context.Context, key string, lastID string, block time.Duration, count int64) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (m *mockKVStore) Delete(ctx context.Context, key string) error { return nil }

func TestStatusUpdater_getLogKey(t *testing.T) {
	orgID := uuid.New()
	name := "test-build"
	updater := &statusUpdater{
		orgID:          orgID,
		imageBuildName: name,
	}

	key := updater.getLogKey()
	expected := "imagebuild:logs:" + orgID.String() + ":" + name
	assert.Equal(t, expected, key)
}

func TestStatusUpdater_writeLogToRedis(t *testing.T) {
	tests := []struct {
		name            string
		kvStore         kvstore.KVStore
		output          []byte
		expectedPushes  int
		expectedExpires int
	}{
		{
			name:            "writes to Redis when kvStore is available",
			kvStore:         newMockKVStore(),
			output:          []byte("test log line\n"),
			expectedPushes:  1,
			expectedExpires: 1,
		},
		{
			name:            "skips when kvStore is nil",
			kvStore:         nil,
			output:          []byte("test log line\n"),
			expectedPushes:  0,
			expectedExpires: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updater := &statusUpdater{
				orgID:          uuid.New(),
				imageBuildName: "test-build",
				kvStore:        tt.kvStore,
				log:            logrus.NewEntry(logrus.New()),
				logBuffer:      make([]string, 0, 500),
			}

			ctx := context.Background()
			updater.writeLogToRedis(ctx, tt.output)

			if tt.kvStore != nil {
				mockStore := tt.kvStore.(*mockKVStore)
				assert.Equal(t, tt.expectedPushes, mockStore.getStreamAddCalls())
				assert.Equal(t, tt.expectedExpires, mockStore.getSetExpireCalls())
				if tt.expectedPushes > 0 {
					pushedValues := mockStore.getPushedValues()
					assert.Equal(t, 1, len(pushedValues))
					assert.Equal(t, tt.output, pushedValues[0])
				}
			}
		})
	}
}

func TestStatusUpdater_writeLogToRedis_bufferLimit(t *testing.T) {
	updater := &statusUpdater{
		orgID:          uuid.New(),
		imageBuildName: "test-build",
		kvStore:        newMockKVStore(),
		log:            logrus.NewEntry(logrus.New()),
		logBuffer:      make([]string, 0, 500),
	}

	ctx := context.Background()

	// Write 600 lines to test buffer truncation
	for i := 0; i < 600; i++ {
		updater.writeLogToRedis(ctx, []byte(fmt.Sprintf("line %d\n", i)))
	}

	updater.logBufferMu.Lock()
	defer updater.logBufferMu.Unlock()

	// Should only keep last 500 lines
	assert.Equal(t, 500, len(updater.logBuffer))
	assert.Equal(t, "line 100", updater.logBuffer[0])
	assert.Equal(t, "line 599", updater.logBuffer[499])
}

func TestStatusUpdater_persistLogsToDB(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageBuildStatus{},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	updater := &statusUpdater{
		imageBuildService: mockService,
		orgID:             orgID,
		imageBuildName:    name,
		log:               logrus.NewEntry(logrus.New()),
		logBuffer:         []string{"line 1", "line 2", "line 3"},
	}

	updater.persistLogsToDB()

	assert.Equal(t, 1, mockService.getUpdateLogsCallsCount())
}

func TestStatusUpdater_persistLogsToDB_emptyBuffer(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageBuildStatus{},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	updater := &statusUpdater{
		imageBuildService: mockService,
		orgID:             orgID,
		imageBuildName:    name,
		log:               logrus.NewEntry(logrus.New()),
		logBuffer:         []string{},
	}

	updater.persistLogsToDB()

	// Should not call UpdateLogs when buffer is empty
	assert.Equal(t, 0, mockService.getUpdateLogsCallsCount())
}

func TestStatusUpdater_updateCondition(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageBuildStatus{},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	updater, cleanup := StartStatusUpdater(
		context.Background(),
		func() {}, // no-op cancel function
		mockService,
		orgID,
		name,
		nil,
		&config.Config{
			ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
		},
		logrus.NewEntry(logrus.New()),
	)
	defer cleanup()

	// Add some logs to buffer so they can be persisted
	updater.logBufferMu.Lock()
	updater.logBuffer = []string{"line 1", "line 2", "line 3"}
	updater.logBufferMu.Unlock()

	condition := api.ImageBuildCondition{
		Type:               api.ImageBuildConditionTypeReady,
		Status:             v1beta1.ConditionStatusTrue,
		Reason:             string(api.ImageBuildConditionReasonCompleted),
		Message:            "test",
		LastTransitionTime: time.Now().UTC(),
	}

	updater.UpdateCondition(condition)

	// Give goroutine time to process (condition updates are processed immediately)
	time.Sleep(200 * time.Millisecond)

	// Should have called Get and UpdateStatus
	assert.GreaterOrEqual(t, mockService.getCallsCount(), 1)
	assert.GreaterOrEqual(t, mockService.getUpdateCallsCount(), 1)
	// Should have persisted logs when condition is Completed
	assert.GreaterOrEqual(t, mockService.getUpdateLogsCallsCount(), 1)
}

func TestStatusUpdater_updateImageReference(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageBuildStatus{},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	updater, cleanup := StartStatusUpdater(
		context.Background(),
		func() {}, // no-op cancel function
		mockService,
		orgID,
		name,
		nil,
		&config.Config{
			ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
		},
		logrus.NewEntry(logrus.New()),
	)
	defer cleanup()

	imageRef := "quay.io/test/image:tag"
	updater.UpdateImageReference(imageRef)

	// Give goroutine time to process
	time.Sleep(100 * time.Millisecond)

	// Should have called Get and UpdateStatus
	assert.GreaterOrEqual(t, mockService.getCallsCount(), 1)
	assert.GreaterOrEqual(t, mockService.getUpdateCallsCount(), 1)
}

func TestStatusUpdater_reportOutput(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageBuildStatus{},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	mockKVStore := newMockKVStore()

	updater, cleanup := StartStatusUpdater(
		context.Background(),
		func() {}, // no-op cancel function
		mockService,
		orgID,
		name,
		mockKVStore,
		&config.Config{
			ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
		},
		logrus.NewEntry(logrus.New()),
	)
	defer cleanup()

	output := []byte("test output\n")
	updater.ReportOutput(output)

	// Give goroutine time to process
	time.Sleep(100 * time.Millisecond)

	// Should have written to Redis
	assert.Equal(t, 1, mockKVStore.getStreamAddCalls())
	assert.Equal(t, 1, mockKVStore.getSetExpireCalls())
	pushedValues := mockKVStore.getPushedValues()
	assert.Equal(t, output, pushedValues[0])
}

func TestStatusUpdater_periodicLogPersistence(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageBuildStatus{},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	cfg := config.NewDefaultImageBuilderWorkerConfig()
	cfg.LastSeenUpdateInterval = util.Duration(100 * time.Millisecond)
	updater, cleanup := StartStatusUpdater(
		context.Background(),
		func() {}, // no-op cancel function
		mockService,
		orgID,
		name,
		nil,
		&config.Config{
			ImageBuilderWorker: cfg,
		},
		logrus.NewEntry(logrus.New()),
	)
	defer cleanup()

	// Add some logs to buffer
	updater.logBufferMu.Lock()
	updater.logBuffer = []string{"line 1", "line 2", "line 3"}
	updater.logBufferMu.Unlock()

	// Send output to trigger periodic update
	updater.ReportOutput([]byte("new output\n"))

	// Wait for periodic ticker to fire (wait longer than the interval)
	time.Sleep(250 * time.Millisecond)

	// Should have persisted logs periodically
	assert.GreaterOrEqual(t, mockService.getUpdateLogsCallsCount(), 1)
}

func TestStatusUpdater_contextCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageBuildStatus{},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	ctx, cancel := context.WithCancel(context.Background())

	_, cleanup := StartStatusUpdater(
		ctx,
		func() {}, // no-op cancel function
		mockService,
		orgID,
		name,
		nil,
		&config.Config{
			ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
		},
		logrus.NewEntry(logrus.New()),
	)

	// Cancel context
	cancel()

	// Cleanup should complete without hanging
	done := make(chan bool)
	go func() {
		cleanup()
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not complete after context cancellation")
	}
}

func TestStatusUpdater_updateStatus_withFailedCondition(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageBuildStatus{},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	updater := &statusUpdater{
		imageBuildService: mockService,
		orgID:             orgID,
		imageBuildName:    name,
		log:               logrus.NewEntry(logrus.New()),
		logBuffer:         []string{"line 1", "line 2"},
	}

	failedCondition := api.ImageBuildCondition{
		Type:               api.ImageBuildConditionTypeReady,
		Status:             v1beta1.ConditionStatusFalse,
		Reason:             string(api.ImageBuildConditionReasonFailed),
		Message:            "build failed",
		LastTransitionTime: time.Now().UTC(),
	}

	updater.updateStatus(&failedCondition, nil, nil)

	// Should have persisted logs when condition is Failed
	assert.Equal(t, 1, mockService.getUpdateLogsCallsCount())
}

func TestStatusUpdater_writeStreamCompleteMarker(t *testing.T) {
	t.Run("writes completion marker to Redis", func(t *testing.T) {
		orgID := uuid.New()
		name := "test-build"
		mockStore := newMockKVStore()

		updater := &statusUpdater{
			orgID:          orgID,
			imageBuildName: name,
			kvStore:        mockStore,
			log:            logrus.NewEntry(logrus.New()),
		}

		updater.writeStreamCompleteMarker()

		assert.Equal(t, 1, mockStore.getStreamAddCalls())
		pushedValues := mockStore.getPushedValues()
		assert.Equal(t, 1, len(pushedValues))
		assert.Equal(t, []byte(api.LogStreamCompleteMarker), pushedValues[0])
	})

	t.Run("skips when kvStore is nil", func(t *testing.T) {
		updater := &statusUpdater{
			orgID:          uuid.New(),
			imageBuildName: "test-build",
			kvStore:        nil,
			log:            logrus.NewEntry(logrus.New()),
		}

		// Should not panic when kvStore is nil
		updater.writeStreamCompleteMarker()
	})
}

func TestStatusUpdater_completedConditionWritesMarker(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageBuildStatus{},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	mockKVStore := newMockKVStore()

	updater, cleanup := StartStatusUpdater(
		context.Background(),
		func() {}, // no-op cancel function
		mockService,
		orgID,
		name,
		mockKVStore,
		&config.Config{
			ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
		},
		logrus.NewEntry(logrus.New()),
	)
	defer cleanup()

	// Add some logs to buffer
	updater.logBufferMu.Lock()
	updater.logBuffer = []string{"line 1", "line 2"}
	updater.logBufferMu.Unlock()

	completedCondition := api.ImageBuildCondition{
		Type:               api.ImageBuildConditionTypeReady,
		Status:             v1beta1.ConditionStatusTrue,
		Reason:             string(api.ImageBuildConditionReasonCompleted),
		Message:            "build completed",
		LastTransitionTime: time.Now().UTC(),
	}

	updater.UpdateCondition(completedCondition)

	// Give goroutine time to process
	time.Sleep(200 * time.Millisecond)

	// Should have written completion marker to Redis
	pushedValues := mockKVStore.getPushedValues()
	foundMarker := false
	for _, v := range pushedValues {
		if string(v) == api.LogStreamCompleteMarker {
			foundMarker = true
			break
		}
	}
	assert.True(t, foundMarker, "Stream complete marker should have been written to Redis")
}

func TestStatusUpdater_cancelingStateBlocksNonTerminalConditions(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-build"

	// Start with status set to Canceling
	canceling := string(api.ImageBuildConditionReasonCanceling)
	imageBuild := &api.ImageBuild{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status: &api.ImageBuildStatus{
			Conditions: &[]api.ImageBuildCondition{
				{
					Type:               api.ImageBuildConditionTypeReady,
					Status:             v1beta1.ConditionStatusFalse,
					Reason:             canceling,
					Message:            "Cancellation requested",
					LastTransitionTime: time.Now().UTC(),
				},
			},
		},
	}

	mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
	mockKVStore := newMockKVStore()

	updater, cleanup := StartStatusUpdater(
		context.Background(),
		func() {}, // no-op cancel function
		mockService,
		orgID,
		name,
		mockKVStore,
		&config.Config{
			ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
		},
		logrus.NewEntry(logrus.New()),
	)
	defer cleanup()

	// Try to set status to Building (non-terminal)
	buildingCondition := api.ImageBuildCondition{
		Type:               api.ImageBuildConditionTypeReady,
		Status:             v1beta1.ConditionStatusFalse,
		Reason:             string(api.ImageBuildConditionReasonBuilding),
		Message:            "Building",
		LastTransitionTime: time.Now().UTC(),
	}

	updater.UpdateCondition(buildingCondition)

	// Give goroutine time to process
	time.Sleep(200 * time.Millisecond)

	// Verify status is still Canceling (Building was blocked)
	mockService.mu.RLock()
	result := mockService.imageBuild
	mockService.mu.RUnlock()

	require.NotNil(t, result.Status)
	require.NotNil(t, result.Status.Conditions)
	readyCondition := api.FindImageBuildStatusCondition(*result.Status.Conditions, api.ImageBuildConditionTypeReady)
	require.NotNil(t, readyCondition)
	assert.Equal(t, canceling, readyCondition.Reason, "Canceling should not be overwritten by Building")
}

func TestStatusUpdater_cancelingStateAllowsTerminalConditions(t *testing.T) {
	testCases := []struct {
		name           string
		terminalReason api.ImageBuildConditionReason
	}{
		{"Canceled", api.ImageBuildConditionReasonCanceled},
		{"Failed", api.ImageBuildConditionReasonFailed},
		{"Completed", api.ImageBuildConditionReasonCompleted},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			orgID := uuid.New()
			name := "test-build"

			// Start with status set to Canceling
			imageBuild := &api.ImageBuild{
				Metadata: v1beta1.ObjectMeta{Name: &name},
				Status: &api.ImageBuildStatus{
					Conditions: &[]api.ImageBuildCondition{
						{
							Type:               api.ImageBuildConditionTypeReady,
							Status:             v1beta1.ConditionStatusFalse,
							Reason:             string(api.ImageBuildConditionReasonCanceling),
							Message:            "Cancellation requested",
							LastTransitionTime: time.Now().UTC(),
						},
					},
				},
			}

			mockService := newMockImageBuildServiceForStatusUpdater(ctrl, imageBuild)
			mockKVStore := newMockKVStore()

			updater, cleanup := StartStatusUpdater(
				context.Background(),
				func() {}, // no-op cancel function
				mockService,
				orgID,
				name,
				mockKVStore,
				&config.Config{
					ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
				},
				logrus.NewEntry(logrus.New()),
			)
			defer cleanup()

			// Set terminal condition
			terminalCondition := api.ImageBuildCondition{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(tc.terminalReason),
				Message:            "Terminal state",
				LastTransitionTime: time.Now().UTC(),
			}

			updater.UpdateCondition(terminalCondition)

			// Give goroutine time to process
			time.Sleep(200 * time.Millisecond)

			// Verify status changed to terminal state
			mockService.mu.RLock()
			result := mockService.imageBuild
			mockService.mu.RUnlock()

			require.NotNil(t, result.Status)
			require.NotNil(t, result.Status.Conditions)
			readyCondition := api.FindImageBuildStatusCondition(*result.Status.Conditions, api.ImageBuildConditionTypeReady)
			require.NotNil(t, readyCondition)
			assert.Equal(t, string(tc.terminalReason), readyCondition.Reason, "Terminal state should overwrite Canceling")
		})
	}
}
