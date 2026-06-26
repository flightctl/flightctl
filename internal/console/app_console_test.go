package console

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

// mockRenderedPublisher is a hand-written testify mock for RenderedVersionPublisher.
type mockRenderedPublisher struct {
	mock.Mock
}

func (m *mockRenderedPublisher) StoreAndNotify(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error {
	args := m.Called(ctx, orgId, name, renderedVersion)
	return args.Error(0)
}

func newTestAppManager(svc *mockAppDeviceService, reg *mockAppSessionRegistration, pub *mockRenderedPublisher) *AppConsoleSessionManager {
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
	pub := &mockRenderedPublisher{}
	mgr := newTestAppManager(svc, reg, pub)

	session, status := mgr.StartSession(context.Background(), uuid.New(), "device1", "", "serial")

	assert.Nil(t, session)
	assert.Equal(t, http.StatusBadRequest, int(status.Code))
	assert.Contains(t, status.Message, "appName")
	svc.AssertNotCalled(t, "GetDevice")
}

func TestAppConsoleSessionManager_StartSession_InvalidConsoleType(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockRenderedPublisher{}
	mgr := newTestAppManager(svc, reg, pub)

	session, status := mgr.StartSession(context.Background(), uuid.New(), "device1", "app1", "invalid")

	assert.Nil(t, session)
	assert.Equal(t, http.StatusBadRequest, int(status.Code))
	assert.Contains(t, status.Message, "consoleType")
	svc.AssertNotCalled(t, "GetDevice")
}

func TestAppConsoleSessionManager_StartSession_DeviceNotFound(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockRenderedPublisher{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(
		(*domain.Device)(nil),
		domain.StatusResourceNotFound("Device", "device1"),
	)

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial")

	assert.Nil(t, session)
	assert.Equal(t, http.StatusNotFound, int(status.Code))
	reg.AssertNotCalled(t, "StartSession")
}

func TestAppConsoleSessionManager_StartSession_DecommissionedDevice(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockRenderedPublisher{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")
	device.Spec.Decommissioning = &domain.DeviceDecommission{}
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial")

	assert.Nil(t, session)
	assert.Equal(t, http.StatusConflict, int(status.Code))
	assert.Contains(t, status.Message, "decommissioned")
	reg.AssertNotCalled(t, "StartSession")
}

func TestAppConsoleSessionManager_StartSession_DuplicateAppName(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockRenderedPublisher{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")
	// Pre-populate with an existing session for app1
	(*device.Metadata.Annotations)[domain.DeviceAnnotationRemoteSession] = `[{"sessionID":"existing-id","appName":"app1"}]`
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial")

	assert.Nil(t, session)
	assert.Equal(t, http.StatusConflict, int(status.Code))
	assert.Contains(t, status.Message, "app1")
	reg.AssertNotCalled(t, "StartSession")
}

func TestAppConsoleSessionManager_CloseSession_RemovesAnnotation(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockRenderedPublisher{}
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
	pub.On("StoreAndNotify", mock.Anything, orgId, "device1", mock.Anything).Return(nil)

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
	pub := &mockRenderedPublisher{}
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
	pub := &mockRenderedPublisher{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")

	// GetDevice called multiple times: fast-path check, addAppSession loop, removeAppSession rollback loop
	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).Return(device, nil)
	pub.On("StoreAndNotify", mock.Anything, orgId, "device1", mock.Anything).Return(nil)
	reg.On("StartSession", mock.AnythingOfType("*console.AppConsoleSession")).Return(fmt.Errorf("registration failed"))

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial")

	assert.Nil(t, session)
	assert.Equal(t, http.StatusInternalServerError, int(status.Code))
	// UpdateDevice must be called twice: once for addAppSession, once for the rollback removeAppSession
	svc.AssertNumberOfCalls(t, "UpdateDevice", 2)
}

func TestAppConsoleSessionManager_StartSession_RollsBackOnPublishFailure(t *testing.T) {
	svc := &mockAppDeviceService{}
	reg := &mockAppSessionRegistration{}
	pub := &mockRenderedPublisher{}
	mgr := newTestAppManager(svc, reg, pub)

	ctx := context.Background()
	orgId := uuid.New()
	device := makeTestDevice("device1")

	svc.On("GetDevice", mock.Anything, orgId, "device1").Return(device, domain.StatusOK())
	svc.On("UpdateDevice", mock.Anything, orgId, "device1", mock.Anything, mock.Anything).Return(device, nil)
	// StoreAndNotify fails on addAppSession, succeeds on the rollback removeAppSession
	pub.On("StoreAndNotify", mock.Anything, orgId, "device1", mock.Anything).Return(fmt.Errorf("redis unavailable")).Once()
	pub.On("StoreAndNotify", mock.Anything, orgId, "device1", mock.Anything).Return(nil).Once()

	session, status := mgr.StartSession(ctx, orgId, "device1", "app1", "serial")

	assert.Nil(t, session)
	assert.Equal(t, http.StatusInternalServerError, int(status.Code))
	// UpdateDevice must be called twice: once for addAppSession, once for the rollback removeAppSession
	svc.AssertNumberOfCalls(t, "UpdateDevice", 2)
	reg.AssertNotCalled(t, "StartSession")
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

func TestRemoveAppSession_LastSession_ReturnsEmpty(t *testing.T) {
	existing := `[{"sessionID":"session-1","appName":"app1","consoleType":"serial"}]`
	updater := removeAppSession("session-1")

	result, err := updater(existing)

	assert.NoError(t, err)
	assert.Empty(t, result)
}
