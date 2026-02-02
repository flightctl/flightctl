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
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

// mockImageExportServiceForStatusUpdater is a mock for testing status updater
type mockImageExportServiceForStatusUpdater struct {
	ctrl            *gomock.Controller
	imageExport     *api.ImageExport
	mu              sync.RWMutex
	getCalls        int
	updateCalls     int
	updateLogsCalls int
}

func newMockImageExportServiceForStatusUpdater(ctrl *gomock.Controller, imageExport *api.ImageExport) *mockImageExportServiceForStatusUpdater {
	return &mockImageExportServiceForStatusUpdater{
		ctrl:        ctrl,
		imageExport: imageExport,
	}
}

func (m *mockImageExportServiceForStatusUpdater) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status) {
	m.mu.Lock()
	m.getCalls++
	m.mu.Unlock()
	if m.imageExport == nil {
		return nil, v1beta1.Status{Code: 404, Message: "not found"}
	}
	return m.imageExport, v1beta1.StatusOK()
}

func (m *mockImageExportServiceForStatusUpdater) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error) {
	m.mu.Lock()
	m.updateCalls++
	if m.imageExport != nil {
		m.imageExport = imageExport
	}
	m.mu.Unlock()
	return imageExport, nil
}

func (m *mockImageExportServiceForStatusUpdater) UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error {
	m.mu.Lock()
	m.updateLogsCalls++
	m.mu.Unlock()
	return nil
}

func (m *mockImageExportServiceForStatusUpdater) Create(ctx context.Context, orgId uuid.UUID, imageExport api.ImageExport) (*api.ImageExport, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageExportServiceForStatusUpdater) List(ctx context.Context, orgId uuid.UUID, params api.ListImageExportsParams) (*api.ImageExportList, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageExportServiceForStatusUpdater) Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status) {
	return nil, v1beta1.StatusOK()
}

func (m *mockImageExportServiceForStatusUpdater) Cancel(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, error) {
	return m.imageExport, nil
}

func (m *mockImageExportServiceForStatusUpdater) CancelWithReason(ctx context.Context, orgId uuid.UUID, name string, reason string) (*api.ImageExport, error) {
	return m.imageExport, nil
}

func (m *mockImageExportServiceForStatusUpdater) Download(ctx context.Context, orgId uuid.UUID, name string) (*imagebuilderapi.ImageExportDownload, error) {
	return nil, nil
}

func (m *mockImageExportServiceForStatusUpdater) GetLogs(ctx context.Context, orgId uuid.UUID, name string, follow bool) (imagebuilderapi.LogStreamReader, string, v1beta1.Status) {
	return nil, "", v1beta1.StatusOK()
}

func (m *mockImageExportServiceForStatusUpdater) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return nil
}

func (m *mockImageExportServiceForStatusUpdater) getCallsCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getCalls
}

func (m *mockImageExportServiceForStatusUpdater) getUpdateCallsCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.updateCalls
}

func (m *mockImageExportServiceForStatusUpdater) getUpdateLogsCallsCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.updateLogsCalls
}

func TestImageExportStatusUpdater_getLogKey(t *testing.T) {
	orgID := uuid.New()
	name := "test-export"
	updater := &imageExportStatusUpdater{
		orgID:           orgID,
		imageExportName: name,
	}

	key := updater.getLogKey()
	expected := "imageexport:logs:" + orgID.String() + ":" + name
	assert.Equal(t, expected, key)
}

func TestImageExportStatusUpdater_writeLogToRedis(t *testing.T) {
	t.Run("writes to Redis when kvStore is available", func(t *testing.T) {
		mockStore := newMockKVStore()
		updater := &imageExportStatusUpdater{
			orgID:           uuid.New(),
			imageExportName: "test-export",
			kvStore:         mockStore,
			ctx:             context.Background(),
			log:             logrus.NewEntry(logrus.New()),
			logBuffer:       make([]string, 0, 500),
		}

		output := []byte("test log line\n")
		updater.writeLogToRedis(output)

		assert.Equal(t, 1, mockStore.getStreamAddCalls())
		assert.Equal(t, 1, mockStore.getSetExpireCalls())
		pushedValues := mockStore.getPushedValues()
		assert.Equal(t, 1, len(pushedValues))
		assert.Equal(t, output, pushedValues[0])
	})

	t.Run("skips when kvStore is nil", func(t *testing.T) {
		updater := &imageExportStatusUpdater{
			orgID:           uuid.New(),
			imageExportName: "test-export",
			kvStore:         nil,
			log:             logrus.NewEntry(logrus.New()),
			logBuffer:       make([]string, 0, 500),
		}

		output := []byte("test log line\n")
		// Should not panic when kvStore is nil
		updater.writeLogToRedis(output)
		// No assertions needed - just checking it doesn't panic
	})
}

func TestImageExportStatusUpdater_writeLogToRedis_bufferLimit(t *testing.T) {
	updater := &imageExportStatusUpdater{
		orgID:           uuid.New(),
		imageExportName: "test-export",
		kvStore:         newMockKVStore(),
		ctx:             context.Background(),
		log:             logrus.NewEntry(logrus.New()),
		logBuffer:       make([]string, 0, 500),
	}

	// Write 600 lines to test buffer truncation
	for i := 0; i < 600; i++ {
		updater.writeLogToRedis([]byte(fmt.Sprintf("line %d\n", i)))
	}

	updater.logBufferMu.Lock()
	defer updater.logBufferMu.Unlock()

	// Should only keep last 500 lines
	assert.Equal(t, 500, len(updater.logBuffer))
	assert.Equal(t, "line 100", updater.logBuffer[0])
	assert.Equal(t, "line 599", updater.logBuffer[499])
}

func TestImageExportStatusUpdater_persistLogsToDB(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-export"
	imageExport := &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageExportStatus{},
	}

	mockService := newMockImageExportServiceForStatusUpdater(ctrl, imageExport)
	updater := &imageExportStatusUpdater{
		imageExportService: mockService,
		orgID:              orgID,
		imageExportName:    name,
		ctx:                context.Background(),
		log:                logrus.NewEntry(logrus.New()),
		logBuffer:          []string{"line 1", "line 2", "line 3"},
	}

	updater.persistLogsToDB()

	assert.Equal(t, 1, mockService.getUpdateLogsCallsCount())
}

func TestImageExportStatusUpdater_persistLogsToDB_emptyBuffer(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-export"
	imageExport := &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageExportStatus{},
	}

	mockService := newMockImageExportServiceForStatusUpdater(ctrl, imageExport)
	updater := &imageExportStatusUpdater{
		imageExportService: mockService,
		orgID:              orgID,
		imageExportName:    name,
		ctx:                context.Background(),
		log:                logrus.NewEntry(logrus.New()),
		logBuffer:          []string{},
	}

	updater.persistLogsToDB()

	// Should not call UpdateLogs when buffer is empty
	assert.Equal(t, 0, mockService.getUpdateLogsCallsCount())
}

func TestImageExportStatusUpdater_updateCondition(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-export"
	imageExport := &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageExportStatus{},
	}

	mockService := newMockImageExportServiceForStatusUpdater(ctrl, imageExport)
	updater, cleanup := startImageExportStatusUpdater(
		context.Background(),
		func() {}, // no-op cancelExport for testing
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

	condition := api.ImageExportCondition{
		Type:               api.ImageExportConditionTypeReady,
		Status:             v1beta1.ConditionStatusTrue,
		Reason:             string(api.ImageExportConditionReasonCompleted),
		Message:            "test",
		LastTransitionTime: time.Now().UTC(),
	}

	updater.updateCondition(condition)

	// Give goroutine time to process (condition updates are processed immediately)
	time.Sleep(200 * time.Millisecond)

	// Should have called Get and UpdateStatus
	assert.GreaterOrEqual(t, mockService.getCallsCount(), 1)
	assert.GreaterOrEqual(t, mockService.getUpdateCallsCount(), 1)
	// Should have persisted logs when condition is Completed
	assert.GreaterOrEqual(t, mockService.getUpdateLogsCallsCount(), 1)
}

func TestImageExportStatusUpdater_setManifestDigest(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-export"
	imageExport := &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageExportStatus{},
	}

	mockService := newMockImageExportServiceForStatusUpdater(ctrl, imageExport)
	updater, cleanup := startImageExportStatusUpdater(
		context.Background(),
		func() {}, // no-op cancelExport for testing
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

	manifestDigest := "sha256:abc123"
	updater.setManifestDigest(manifestDigest)

	// Give goroutine time to process
	time.Sleep(100 * time.Millisecond)

	// Should have called Get and UpdateStatus
	assert.GreaterOrEqual(t, mockService.getCallsCount(), 1)
	assert.GreaterOrEqual(t, mockService.getUpdateCallsCount(), 1)
}

func TestImageExportStatusUpdater_reportOutput(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-export"
	imageExport := &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageExportStatus{},
	}

	mockService := newMockImageExportServiceForStatusUpdater(ctrl, imageExport)
	mockKVStore := newMockKVStore()

	updater, cleanup := startImageExportStatusUpdater(
		context.Background(),
		func() {}, // no-op cancelExport for testing
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
	updater.reportOutput(output)

	// Give goroutine time to process
	time.Sleep(100 * time.Millisecond)

	// Should have written to Redis
	assert.Equal(t, 1, mockKVStore.getStreamAddCalls())
	assert.Equal(t, 1, mockKVStore.getSetExpireCalls())
	pushedValues := mockKVStore.getPushedValues()
	assert.Equal(t, output, pushedValues[0])
}

func TestImageExportStatusUpdater_periodicLogPersistence(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-export"
	imageExport := &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageExportStatus{},
	}

	mockService := newMockImageExportServiceForStatusUpdater(ctrl, imageExport)
	cfg := config.NewDefaultImageBuilderWorkerConfig()
	cfg.LastSeenUpdateInterval = util.Duration(100 * time.Millisecond)
	updater, cleanup := startImageExportStatusUpdater(
		context.Background(),
		func() {}, // no-op cancelExport for testing
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
	updater.reportOutput([]byte("new output\n"))

	// Wait for periodic ticker to fire (wait longer than the interval)
	time.Sleep(250 * time.Millisecond)

	// Should have persisted logs periodically
	assert.GreaterOrEqual(t, mockService.getUpdateLogsCallsCount(), 1)
}

func TestImageExportStatusUpdater_contextCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-export"
	imageExport := &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageExportStatus{},
	}

	mockService := newMockImageExportServiceForStatusUpdater(ctrl, imageExport)
	ctx, cancel := context.WithCancel(context.Background())

	_, cleanup := startImageExportStatusUpdater(
		ctx,
		func() {}, // no-op cancelExport for testing
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

func TestImageExportStatusUpdater_updateStatus_withFailedCondition(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-export"
	imageExport := &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageExportStatus{},
	}

	mockService := newMockImageExportServiceForStatusUpdater(ctrl, imageExport)
	updater, cleanup := startImageExportStatusUpdater(
		context.Background(),
		func() {}, // no-op cancelExport for testing
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

	// Add some logs to buffer
	updater.logBufferMu.Lock()
	updater.logBuffer = []string{"line 1", "line 2"}
	updater.logBufferMu.Unlock()

	failedCondition := api.ImageExportCondition{
		Type:               api.ImageExportConditionTypeReady,
		Status:             v1beta1.ConditionStatusFalse,
		Reason:             string(api.ImageExportConditionReasonFailed),
		Message:            "export failed",
		LastTransitionTime: time.Now().UTC(),
	}

	updater.updateCondition(failedCondition)

	// Give goroutine time to process
	time.Sleep(200 * time.Millisecond)

	// Should have persisted logs when condition is Failed
	assert.GreaterOrEqual(t, mockService.getUpdateLogsCallsCount(), 1)
}

func TestImageExportStatusUpdater_writeStreamCompleteMarker(t *testing.T) {
	t.Run("writes completion marker to Redis", func(t *testing.T) {
		orgID := uuid.New()
		name := "test-export"
		mockStore := newMockKVStore()

		updater := &imageExportStatusUpdater{
			orgID:           orgID,
			imageExportName: name,
			kvStore:         mockStore,
			ctx:             context.Background(),
			log:             logrus.NewEntry(logrus.New()),
		}

		updater.writeStreamCompleteMarker()

		assert.Equal(t, 1, mockStore.getStreamAddCalls())
		pushedValues := mockStore.getPushedValues()
		assert.Equal(t, 1, len(pushedValues))
		assert.Equal(t, []byte(api.LogStreamCompleteMarker), pushedValues[0])
	})

	t.Run("skips when kvStore is nil", func(t *testing.T) {
		updater := &imageExportStatusUpdater{
			orgID:           uuid.New(),
			imageExportName: "test-export",
			kvStore:         nil,
			log:             logrus.NewEntry(logrus.New()),
		}

		// Should not panic when kvStore is nil
		updater.writeStreamCompleteMarker()
	})
}

func TestImageExportStatusUpdater_completedConditionWritesMarker(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	name := "test-export"
	imageExport := &api.ImageExport{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &api.ImageExportStatus{},
	}

	mockService := newMockImageExportServiceForStatusUpdater(ctrl, imageExport)
	mockKVStore := newMockKVStore()

	updater, cleanup := startImageExportStatusUpdater(
		context.Background(),
		func() {}, // no-op cancelExport for testing
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

	completedCondition := api.ImageExportCondition{
		Type:               api.ImageExportConditionTypeReady,
		Status:             v1beta1.ConditionStatusTrue,
		Reason:             string(api.ImageExportConditionReasonCompleted),
		Message:            "export completed",
		LastTransitionTime: time.Now().UTC(),
	}

	updater.updateCondition(completedCondition)

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
