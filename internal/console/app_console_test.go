package console

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mockAppDeviceService is a hand-written testify mock for AppConsoleDeviceService.
type mockAppDeviceService struct {
	mock.Mock
}

func (m *mockAppDeviceService) GetDevice(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status) {
	args := m.Called(ctx, orgId, name)
	dev, _ := args.Get(0).(*domain.Device)
	return dev, args.Get(1).(domain.Status)
}

func (m *mockAppDeviceService) UpdateDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, error) {
	args := m.Called(ctx, orgId, name, device, fieldsToUnset)
	dev, _ := args.Get(0).(*domain.Device)
	return dev, args.Error(1)
}

// mockAppSessionRegistration is a hand-written testify mock for AppConsoleSessionRegistration.
type mockAppSessionRegistration struct {
	mock.Mock
}

func (m *mockAppSessionRegistration) StartSession(session *AppConsoleSession) error {
	args := m.Called(session)
	return args.Error(0)
}

func (m *mockAppSessionRegistration) CloseSession(session *AppConsoleSession) error {
	args := m.Called(session)
	return args.Error(0)
}

// mockConsoleEventNotifier is a hand-written testify mock for ConsoleEventNotifier.
type mockConsoleEventNotifier struct {
	mock.Mock
}

func (m *mockConsoleEventNotifier) NotifyConsole(ctx context.Context, orgId uuid.UUID, name string) error {
	args := m.Called(ctx, orgId, name)
	return args.Error(0)
}

func (m *mockConsoleEventNotifier) ClearConsoleNotification(ctx context.Context, orgId uuid.UUID, name string) error {
	args := m.Called(ctx, orgId, name)
	return args.Error(0)
}

func newTestAppManager(svc *mockAppDeviceService, reg *mockAppSessionRegistration, pub *mockConsoleEventNotifier) *AppConsoleSessionManager {
	return NewAppConsoleSessionManager(svc, logrus.NewEntry(logrus.New()), reg, pub)
}

func makeTestDevice(name string) *domain.Device {
	return &domain.Device{
		Metadata: domain.ObjectMeta{
			Name:        &name,
			Annotations: lo.ToPtr(map[string]string{}),
		},
		Spec: &domain.DeviceSpec{},
	}
}

func TestAppConsoleSessionManager_StartSession_EmptyAppName(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	session, status := mgr.StartSession(context.Background(), uuid.New(), "device1", "", "serial", false)

	assert.Nil(t, session)
	assert.Equal(t, http.StatusBadRequest, int(status.Code))
	assert.Contains(t, status.Message, "appName")
	svc.AssertNotCalled(t, "GetDevice")
}

func TestAppConsoleSessionManager_StartSession_InvalidConsoleType(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	session, status := mgr.StartSession(context.Background(), uuid.New(), "device1", "app1", "invalid", false)

	assert.Nil(t, session)
	assert.Equal(t, http.StatusBadRequest, int(status.Code))
	assert.Contains(t, status.Message, "consoleType")
	svc.AssertNotCalled(t, "GetDevice")
}

func TestAppConsoleSessionManager_StartSession_DeviceNotFound(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(
		(*domain.Device)(nil),
		domain.StatusResourceNotFound("Device", "device1"),
	)

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial", false)

	assert.Nil(t, session)
	assert.Equal(t, http.StatusNotFound, int(status.Code))
	reg.AssertNotCalled(t, "StartSession")
}

func TestAppConsoleSessionManager_StartSession_DecommissionedDevice(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")
	device.Spec.Decommissioning = &domain.DeviceDecommission{}
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial", false)

	assert.Nil(t, session)
	assert.Equal(t, http.StatusConflict, int(status.Code))
	assert.Contains(t, status.Message, "decommissioned")
	reg.AssertNotCalled(t, "StartSession")
}

func TestAppConsoleSessionManager_StartSession_DuplicateAppName(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")
	// Pre-populate with an existing session for app1
	(*device.Metadata.Annotations)[domain.DeviceAnnotationRemoteSession] = `[{"sessionID":"existing-id","appName":"app1"}]`
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial", false)

	assert.Nil(t, session)
	assert.Equal(t, http.StatusConflict, int(status.Code))
	assert.Contains(t, status.Message, "app1")
	reg.AssertNotCalled(t, "StartSession")
}

func TestAppConsoleSessionManager_CloseSession_RemovesAnnotation(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	sessionID := uuid.New().String()

	session := &AppConsoleSession{
		UUID:       sessionID,
		OrgId:      orgId,
		DeviceName: "device1",
		AppName:    "app1",
	}

	// Device has the annotation for this session
	device := makeTestDevice("device1")
	(*device.Metadata.Annotations)[domain.DeviceAnnotationRemoteSession] = `[{"sessionID":"` + sessionID + `","appName":"app1"}]`

	var capturedDevice domain.Device
	reg.On("CloseSession", session).Return(nil)
	// GetDevice is called during modifyAnnotations
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedDevice = args.Get(3).(domain.Device)
		}).
		Return(device, nil)

	status := mgr.CloseSession(ctx, session)

	assert.Equal(t, http.StatusOK, int(status.Code))
	reg.AssertCalled(t, "CloseSession", session)
	// Verify annotation was actually removed from the device passed to UpdateDevice.
	if capturedDevice.Metadata.Annotations != nil {
		assert.NotContains(t, *capturedDevice.Metadata.Annotations, domain.DeviceAnnotationRemoteSession,
			"remote-session annotation must be absent after CloseSession")
	}
}

func TestAppConsoleSessionManager_CloseSession_AnnotationFailure_DoesNotUnregister(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	sessionID := uuid.New().String()

	session := &AppConsoleSession{
		UUID:       sessionID,
		OrgId:      orgId,
		DeviceName: "device1",
		AppName:    "app1",
	}

	device := makeTestDevice("device1")
	(*device.Metadata.Annotations)[domain.DeviceAnnotationRemoteSession] = `[{"sessionID":"` + sessionID + `","appName":"app1"}]`

	// GetDevice succeeds, UpdateDevice fails — annotation persistence fails.
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).Return((*domain.Device)(nil), fmt.Errorf("db write error"))

	status := mgr.CloseSession(ctx, session)

	assert.NotEqual(t, http.StatusOK, int(status.Code))
	// CloseSession on the registration must NOT be called when annotation cleanup fails.
	reg.AssertNotCalled(t, "CloseSession")
}

func TestAppConsoleSessionManager_StartSession_RollsBackOnRegistrationFailure(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")

	// GetDevice called multiple times: fast-path check, addAppSession loop, removeAppSession rollback loop
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).Return(device, nil)
	pub.On("NotifyConsole", mock.Anything, orgId, "device1").Return(nil)
	reg.On("StartSession", mock.AnythingOfType("*console.AppConsoleSession")).Return(fmt.Errorf("registration failed"))

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial", false)

	assert.Nil(t, session)
	assert.Equal(t, http.StatusInternalServerError, int(status.Code))
	// UpdateDevice must be called twice: once for addAppSession, once for the rollback removeAppSession
	svc.AssertNumberOfCalls(t, "UpdateDevice", 2)
}

func TestAppConsoleSessionManager_StartSession_ProceedsWhenPublishFails(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")

	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).Return(device, nil)
	// NotifyConsole failure is non-fatal — the session still starts.
	pub.On("NotifyConsole", mock.Anything, orgId, "device1").Return(fmt.Errorf("redis unavailable")).Once()
	reg.On("StartSession", mock.AnythingOfType("*console.AppConsoleSession")).Return(nil)

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial", false)

	assert.NotNil(t, session)
	assert.Equal(t, http.StatusOK, int(status.Code))
	// UpdateDevice called exactly once — no rollback on publish failure.
	svc.AssertNumberOfCalls(t, "UpdateDevice", 1)
	reg.AssertCalled(t, "StartSession", mock.AnythingOfType("*console.AppConsoleSession"))
}

func TestAddAppSession_DuplicateAppName_ReturnsConflict(t *testing.T) {
	existing := `[{"sessionID":"existing-id","appName":"app1","consoleType":"serial"}]`
	updater := addAppSession("new-session-id", "app1", "serial")

	result, err := updater(existing)

	assert.Empty(t, result)
	assert.Error(t, err)
	var dupErr *duplicateAppSessionError
	assert.ErrorAs(t, err, &dupErr)
	assert.Contains(t, dupErr.Error(), "app1")
}

func TestReplaceAppSession_ExistingEntry_SetsReplacesSessionID(t *testing.T) {
	existing := `[{"sessionID":"old-id","appName":"app1","consoleType":"serial"}]`
	var replacedSessionID string
	updater := replaceAppSession("new-id", "app1", "vnc", &replacedSessionID)

	result, err := updater(existing)

	assert.NoError(t, err)
	var sessions []domain.DeviceRemoteSession
	require.NoError(t, json.Unmarshal([]byte(result), &sessions))
	require.Len(t, sessions, 1)
	assert.Equal(t, "new-id", sessions[0].SessionID)
	assert.Equal(t, "app1", sessions[0].AppName)
	assert.Equal(t, "vnc", sessions[0].ConsoleType)
	assert.Equal(t, "old-id", sessions[0].ReplacesSessionID,
		"the new entry must name the session it replaced")
	assert.Equal(t, "old-id", replacedSessionID,
		"the out-param must report the evicted session so callers can audit-log it")
}

func TestReplaceAppSession_NoExistingEntry_BehavesLikeAdd(t *testing.T) {
	var replacedSessionID string
	updater := replaceAppSession("new-id", "app1", "serial", &replacedSessionID)

	result, err := updater("")

	assert.NoError(t, err)
	var sessions []domain.DeviceRemoteSession
	require.NoError(t, json.Unmarshal([]byte(result), &sessions))
	require.Len(t, sessions, 1)
	assert.Equal(t, "new-id", sessions[0].SessionID)
	assert.Empty(t, sessions[0].ReplacesSessionID,
		"there was nothing to replace, so ReplacesSessionID must stay empty")
	assert.Empty(t, replacedSessionID, "there was nothing to replace, so the out-param must stay empty")
}

func TestReplaceAppSession_PreservesOtherAppSessions(t *testing.T) {
	existing := `[{"sessionID":"other-id","appName":"other-app","consoleType":"serial"},` +
		`{"sessionID":"old-id","appName":"app1","consoleType":"serial"}]`
	updater := replaceAppSession("new-id", "app1", "serial", nil)

	result, err := updater(existing)

	assert.NoError(t, err)
	var sessions []domain.DeviceRemoteSession
	require.NoError(t, json.Unmarshal([]byte(result), &sessions))
	require.Len(t, sessions, 2)
	byApp := make(map[string]domain.DeviceRemoteSession)
	for _, s := range sessions {
		byApp[s.AppName] = s
	}
	assert.Equal(t, "other-id", byApp["other-app"].SessionID, "unrelated app sessions must be untouched")
	assert.Equal(t, "new-id", byApp["app1"].SessionID)
	assert.Equal(t, "old-id", byApp["app1"].ReplacesSessionID)
}

func TestAppConsoleSessionManager_StartSession_Force_ReplacesExistingSession(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	logger, hook := test.NewNullLogger()
	mgr := NewAppConsoleSessionManager(svc, logrus.NewEntry(logger), reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")
	(*device.Metadata.Annotations)[domain.DeviceAnnotationRemoteSession] = `[{"sessionID":"existing-id","appName":"app1","consoleType":"serial"}]`

	var capturedDevice domain.Device
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedDevice = args.Get(3).(domain.Device)
		}).
		Return(device, nil)
	pub.On("NotifyConsole", mock.Anything, orgId, "device1").Return(nil)
	reg.On("StartSession", mock.AnythingOfType("*console.AppConsoleSession")).Return(nil)

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial", true)

	assert.Equal(t, http.StatusOK, int(status.Code), "force must bypass the 409 conflict")
	require.NotNil(t, session)

	require.NotNil(t, capturedDevice.Metadata.Annotations)
	val := (*capturedDevice.Metadata.Annotations)[domain.DeviceAnnotationRemoteSession]
	var sessions []domain.DeviceRemoteSession
	require.NoError(t, json.Unmarshal([]byte(val), &sessions))
	require.Len(t, sessions, 1, "the old entry for app1 must be replaced, not appended alongside")
	assert.Equal(t, session.UUID, sessions[0].SessionID)
	assert.Equal(t, "existing-id", sessions[0].ReplacesSessionID)

	var auditEntry *logrus.Entry
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "forcibly replaced active session") {
			auditEntry = entry
			break
		}
	}
	require.NotNil(t, auditEntry, "a forced takeover must emit an audit log line naming the evicted session")
	assert.Contains(t, auditEntry.Message, "existing-id")
	assert.Contains(t, auditEntry.Message, session.UUID)
}

func TestAppConsoleSessionManager_StartSession_Force_NoExistingSession_AddsNormally(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")

	var capturedDevice domain.Device
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedDevice = args.Get(3).(domain.Device)
		}).
		Return(device, nil)
	pub.On("NotifyConsole", mock.Anything, orgId, "device1").Return(nil)
	reg.On("StartSession", mock.AnythingOfType("*console.AppConsoleSession")).Return(nil)

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial", true)

	assert.Equal(t, http.StatusOK, int(status.Code))
	require.NotNil(t, session)

	require.NotNil(t, capturedDevice.Metadata.Annotations)
	val := (*capturedDevice.Metadata.Annotations)[domain.DeviceAnnotationRemoteSession]
	var sessions []domain.DeviceRemoteSession
	require.NoError(t, json.Unmarshal([]byte(val), &sessions))
	require.Len(t, sessions, 1)
	assert.Empty(t, sessions[0].ReplacesSessionID, "there was nothing to replace")
}

func TestRemoveAppSession_LastSession_ReturnsEmpty(t *testing.T) {
	existing := `[{"sessionID":"session-1","appName":"app1","consoleType":"serial"}]`
	updater := removeAppSession("session-1")

	result, err := updater(existing)

	assert.NoError(t, err)
	assert.Empty(t, result)
}

func TestAppConsoleSessionManager_StartSession_DoesNotBumpDeviceAnnotationRenderedVersion(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")
	// Seed an existing rendered version that must survive the console start.
	(*device.Metadata.Annotations)[domain.DeviceAnnotationRenderedVersion] = "99"

	var capturedDevice domain.Device
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedDevice = args.Get(3).(domain.Device)
		}).
		Return(device, nil)
	pub.On("NotifyConsole", mock.Anything, orgId, "device1").Return(nil)
	reg.On("StartSession", mock.AnythingOfType("*console.AppConsoleSession")).Return(nil)

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial", false)

	assert.Equal(t, http.StatusOK, int(status.Code))
	assert.NotNil(t, session)
	pub.AssertExpectations(t)

	// DeviceAnnotationRenderedVersion must remain exactly as before — console start must not bump it.
	require.NotNil(t, capturedDevice.Metadata.Annotations)
	v, exists := (*capturedDevice.Metadata.Annotations)[domain.DeviceAnnotationRenderedVersion]
	assert.True(t, exists, "DeviceAnnotationRenderedVersion should still be present in the saved device")
	assert.Equal(t, "99", v, "DeviceAnnotationRenderedVersion must not be changed by console start")
}

func TestAppConsoleSessionManager_CloseSession_DoesNotCallNotifier(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockConsoleEventNotifier{} // no expectations — must not be called
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	sessionID := uuid.New().String()

	session := &AppConsoleSession{
		UUID:       sessionID,
		OrgId:      orgId,
		DeviceName: "device1",
		AppName:    "app1",
	}

	device := makeTestDevice("device1")
	(*device.Metadata.Annotations)[domain.DeviceAnnotationRemoteSession] = `[{"sessionID":"` + sessionID + `","appName":"app1"}]`

	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).Return(device, nil)
	reg.On("CloseSession", session).Return(nil)

	status := mgr.CloseSession(ctx, session)

	assert.Equal(t, http.StatusOK, int(status.Code))
	pub.AssertNotCalled(t, "NotifyConsole")
	pub.AssertNotCalled(t, "ClearConsoleNotification")
}
