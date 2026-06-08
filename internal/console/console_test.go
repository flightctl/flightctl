package console

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	gomock "go.uber.org/mock/gomock"
)

// MockSessionRegistration is a simple mock implementation for testing
type MockSessionRegistration struct {
	mock.Mock
}

func (m *MockSessionRegistration) StartSession(session *ConsoleSession) error {
	args := m.Called(session)
	return args.Error(0)
}

func (m *MockSessionRegistration) CloseSession(session *ConsoleSession) error {
	args := m.Called(session)
	return args.Error(0)
}

func TestConsoleSessionManager_StartSession_DeviceNotFound(t *testing.T) {
	// This test specifically addresses EDM-3084: ensure nonexistent devices return 404, not 500

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockService := service.NewMockService(ctrl)
	mockRegistration := &MockSessionRegistration{}
	logger := logrus.NewEntry(logrus.New())

	manager := NewConsoleSessionManager(mockService, logger, mockRegistration)

	ctx := context.Background()
	orgId := uuid.New()
	deviceName := "nonexistent-device"
	sessionMetadata := `{"tty": true}`

	// Mock GetDevice to return 404 Not Found for nonexistent device
	mockService.EXPECT().GetDevice(ctx, orgId, deviceName).Return(
		nil,
		domain.StatusResourceNotFound("Device", deviceName),
	).Times(1)

	// Call StartSession
	session, status := manager.StartSession(ctx, orgId, deviceName, sessionMetadata)

	// Assert that we get a 404 error, not 500
	assert.Nil(t, session, "Session should be nil for nonexistent device")
	assert.Equal(t, http.StatusNotFound, int(status.Code), "Should return 404 Not Found for nonexistent device")
	assert.Contains(t, status.Message, "not found", "Error message should indicate device not found")

	// Verify that session registration was never called (since device doesn't exist)
	mockRegistration.AssertNotCalled(t, "StartSession")
}

func TestConsoleSessionManager_StartSession_DecommissionedDevice(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockService := service.NewMockService(ctrl)
	mockRegistration := &MockSessionRegistration{}
	logger := logrus.NewEntry(logrus.New())

	manager := NewConsoleSessionManager(mockService, logger, mockRegistration)

	ctx := context.Background()
	orgId := uuid.New()
	deviceName := "decommissioned-device"
	sessionMetadata := `{"tty": true}`

	// Create a decommissioned device
	device := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: &deviceName,
		},
		Spec: &domain.DeviceSpec{
			Decommissioning: &domain.DeviceDecommission{}, // Device is decommissioned (non-nil)
		},
	}

	mockService.EXPECT().GetDevice(ctx, orgId, deviceName).Return(device, domain.StatusOK()).Times(1)

	// Call StartSession
	session, status := manager.StartSession(ctx, orgId, deviceName, sessionMetadata)

	// Assert that we get a 409 Conflict error for decommissioned device
	assert.Nil(t, session, "Session should be nil for decommissioned device")
	assert.Equal(t, http.StatusConflict, int(status.Code), "Should return 409 Conflict for decommissioned device")
	assert.Contains(t, status.Message, "decommissioned", "Error message should indicate device is decommissioned")

	mockRegistration.AssertExpectations(t)
}

func TestConsoleSessionManager_StartSession_MissingMetadata(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockService := service.NewMockService(ctrl)
	mockRegistration := &MockSessionRegistration{}
	logger := logrus.NewEntry(logrus.New())

	manager := NewConsoleSessionManager(mockService, logger, mockRegistration)

	ctx := context.Background()
	orgId := uuid.New()
	deviceName := "test-device"
	sessionMetadata := "" // Empty metadata

	// Call StartSession - should fail before any service calls
	session, status := manager.StartSession(ctx, orgId, deviceName, sessionMetadata)

	// Assert that we get a 400 Bad Request error for missing metadata
	assert.Nil(t, session, "Session should be nil for missing metadata")
	assert.Equal(t, http.StatusBadRequest, int(status.Code), "Should return 400 Bad Request for missing metadata")
	assert.Contains(t, status.Message, "missing session metadata", "Error message should indicate missing metadata")

	// Verify that no service calls were made (guard pattern prevents them)
	mockRegistration.AssertNotCalled(t, "StartSession")
}
