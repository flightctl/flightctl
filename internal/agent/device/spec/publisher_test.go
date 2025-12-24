package spec

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type vars struct {
	assertions *require.Assertions
	ctrl       *gomock.Controller
	mockClient *client.MockManagement
	notifier   *publisher
	ctx        context.Context
	cancel     context.CancelFunc
	deviceName string
	watcher    Watcher
}

func (v *vars) finish() {
	v.ctrl.Finish()
	v.cancel()
}

func setup(t *testing.T) *vars {
	return setupWithInitialVersion(t, "")
}

func setupWithInitialVersion(t *testing.T, initialVersion string) *vars {
	ctrl := gomock.NewController(t)
	deviceName := "test-device"
	ctx, cancel := context.WithCancel(context.Background())
	mockClient := client.NewMockManagement(ctrl)

	// Create a mock device not found handler
	deviceNotFoundHandler := func() error {
		return nil // Mock handler that always succeeds
	}

	n := &publisher{
		managementClient:      mockClient,
		deviceName:            deviceName,
		log:                   log.NewPrefixLogger(""),
		lastKnownVersion:      initialVersion,
		pollConfig:            poll.NewConfig(time.Second, 1.5),
		deviceNotFoundHandler: deviceNotFoundHandler,
	}

	return &vars{
		assertions: require.New(t),
		ctrl:       ctrl,
		mockClient: mockClient,
		notifier:   n,
		ctx:        ctx,
		cancel:     cancel,
		deviceName: deviceName,
		watcher:    n.Watch(),
	}
}

func Test_getRenderedFromManagementAPIWithRetry(t *testing.T) {
	t.Run("request error", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		requestErr := errors.New("failed to make request for spec")
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusInternalServerError, requestErr)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1beta1.Device{})
		v.assertions.ErrorIs(err, errors.ErrGettingDeviceSpec)
	})

	t.Run("response status code has no content", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusNoContent, nil)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1beta1.Device{})
		v.assertions.ErrorIs(err, errors.ErrNoContent)
	})

	t.Run("response status code has conflict", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusConflict, nil)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1beta1.Device{})
		v.assertions.ErrorIs(err, errors.ErrNoContent)
	})

	t.Run("response is nil", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusOK, nil)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1beta1.Device{})
		v.assertions.ErrorIs(err, errors.ErrNilResponse)
	})

	t.Run("makes a request with empty params if no rendered version is passed", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		device := createTestRenderedDevice("requested-image:latest")
		params := &v1beta1.GetRenderedDeviceParams{}
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, params).Return(device, http.StatusOK, nil)

		rendered := &v1beta1.Device{}
		success, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "", rendered)
		v.assertions.NoError(err)
		v.assertions.True(success)
		v.assertions.Equal(device, rendered)
	})

	t.Run("makes a request with the passed renderedVersion when set", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		device := createTestRenderedDevice("requested-image:latest")
		renderedVersion := "24"
		params := &v1beta1.GetRenderedDeviceParams{KnownRenderedVersion: &renderedVersion}
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, params).Return(device, http.StatusOK, nil)

		rendered := &v1beta1.Device{}
		success, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "24", rendered)
		v.assertions.NoError(err)
		v.assertions.True(success)
		v.assertions.Equal(device, rendered)
	})
}

func TestSetClient(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := client.NewMockManagement(ctrl)

	t.Run("sets the client", func(t *testing.T) {
		s := &publisher{}
		s.SetClient(mockClient)
		require.Equal(mockClient, s.managementClient)
	})
}

func TestDevicePublisher_pollAndNotify(t *testing.T) {
	specErr := errors.New("problem with spec")
	t.Run("poll and notify failure", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, http.StatusServiceUnavailable, specErr)
		v.notifier.pollAndPublish(v.ctx)
		_, popped, err := v.watcher.TryPop()
		require.NoError(t, err)
		require.False(t, popped)
	})
	t.Run("poll and notify no content", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, http.StatusNoContent, nil)
		v.notifier.pollAndPublish(v.ctx)
		_, popped, err := v.watcher.TryPop()
		require.NoError(t, err)
		require.False(t, popped)
	})
	t.Run("poll and notify with content", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		renderedDesiredSpec := createTestRenderedDevice("flightctl-device:v2")
		v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), gomock.Any(), gomock.Any()).Return(renderedDesiredSpec, 200, nil)
		v.notifier.pollAndPublish(v.ctx)
		result, popped, err := v.watcher.TryPop()
		require.NoError(t, err)
		require.True(t, popped)
		require.Equal(t, renderedDesiredSpec, result)
	})
}

func TestDevicePublisher_Run(t *testing.T) {
	t.Run("stops when context is canceled", func(tt *testing.T) {
		v := setup(tt)
		defer v.ctrl.Finish()

		// short minDelay for testing
		v.notifier.minDelay = 10 * time.Millisecond

		v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, http.StatusNoContent, nil).AnyTimes()

		ctx, cancel := context.WithCancel(context.Background())

		// Start the publisher in a goroutine
		done := make(chan struct{})
		go func() {
			v.notifier.Run(ctx)
			close(done)
		}()

		// Wait a short time to ensure it's started
		time.Sleep(20 * time.Millisecond)

		// Cancel the context to stop the publisher
		cancel()

		// Wait for the publisher to stop with timeout
		select {
		case <-done:
			// Success - the publisher stopped when context was canceled
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Timed out waiting for publisher to stop")
		}

		// Verify that the publisher is marked as stopped
		v.assertions.True(v.notifier.stopped.Load())
	})
}

func TestDevicePublisher_DeviceNotFoundHandling(t *testing.T) {
	tests := []struct {
		name                  string
		deviceNotFoundHandler func() error
		mockReturnError       error
		expectedHandlerCalled bool
		expectedHandlerError  bool
		description           string
	}{
		{
			name: "handles device not found error successfully",
			deviceNotFoundHandler: func() error {
				return nil
			},
			mockReturnError:       client.ErrDeviceNotFound,
			expectedHandlerCalled: true,
			expectedHandlerError:  false,
			description:           "Device not found handler should have been called",
		},
		{
			name: "handles device not found handler error",
			deviceNotFoundHandler: func() error {
				return errors.New("handler failed")
			},
			mockReturnError:       client.ErrDeviceNotFound,
			expectedHandlerCalled: true,
			expectedHandlerError:  true,
			description:           "Device not found handler should have been called even when it returns an error",
		},
		{
			name:                  "handles device not found when handler is nil",
			deviceNotFoundHandler: nil,
			mockReturnError:       client.ErrDeviceNotFound,
			expectedHandlerCalled: false,
			expectedHandlerError:  false,
			description:           "Should not panic when handler is nil",
		},
		{
			name: "does not call handler for generic 404 error",
			deviceNotFoundHandler: func() error {
				return nil
			},
			mockReturnError:       nil, // Generic 404, not ErrDeviceNotFound
			expectedHandlerCalled: false,
			expectedHandlerError:  false,
			description:           "Device not found handler should NOT have been called for generic 404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := setup(t)
			defer v.finish()

			// Track if the handler was called
			handlerCalled := false
			var handlerError error

			// Create a publisher with the test handler
			var deviceNotFoundHandler func() error
			if tt.deviceNotFoundHandler != nil {
				deviceNotFoundHandler = func() error {
					handlerCalled = true
					handlerError = tt.deviceNotFoundHandler()
					return handlerError
				}
			}

			publisher := &publisher{
				managementClient:      v.mockClient,
				deviceName:            v.deviceName,
				log:                   log.NewPrefixLogger(""),
				pollConfig:            poll.NewConfig(time.Second, 1.5),
				deviceNotFoundHandler: deviceNotFoundHandler,
			}

			// Mock the management client based on test case
			v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), v.deviceName, gomock.Any()).Return(nil, http.StatusNotFound, tt.mockReturnError)

			// Call pollAndPublish
			publisher.pollAndPublish(v.ctx)

			// Verify the handler behavior
			v.assertions.Equal(tt.expectedHandlerCalled, handlerCalled, tt.description)

			if tt.expectedHandlerCalled && tt.expectedHandlerError {
				v.assertions.Error(handlerError, "Handler should have returned an error")
			} else if tt.expectedHandlerCalled && !tt.expectedHandlerError {
				v.assertions.NoError(handlerError, "Handler should not have returned an error")
			}
		})
	}
}

func TestVersionComparison(t *testing.T) {
	tests := []struct {
		name                 string
		initialVersion       string
		newVersion           string
		expectedUpdate       bool
		expectedFinalVersion string
		description          string
	}{
		{
			name:                 "new version greater than last known",
			initialVersion:       "5",
			newVersion:           "10",
			expectedUpdate:       true,
			expectedFinalVersion: "10",
			description:          "Should update when new version is greater",
		},
		{
			name:                 "new version equal to last known",
			initialVersion:       "5",
			newVersion:           "5",
			expectedUpdate:       false,
			expectedFinalVersion: "5",
			description:          "Should NOT update when new version equals last known",
		},
		{
			name:                 "new version less than last known",
			initialVersion:       "10",
			newVersion:           "5",
			expectedUpdate:       false,
			expectedFinalVersion: "10",
			description:          "Should NOT update when new version is less than last known",
		},
		{
			name:                 "initial version empty, new version valid",
			initialVersion:       "",
			newVersion:           "5",
			expectedUpdate:       true,
			expectedFinalVersion: "5",
			description:          "Should update when initial version is empty",
		},
		{
			name:                 "initial version valid, new version empty",
			initialVersion:       "5",
			newVersion:           "",
			expectedUpdate:       false,
			expectedFinalVersion: "5",
			description:          "Should NOT update when new version is empty (0 <= 5)",
		},
		{
			name:                 "both versions empty",
			initialVersion:       "",
			newVersion:           "",
			expectedUpdate:       false,
			expectedFinalVersion: "",
			description:          "Should NOT update when both versions are empty (0 <= 0)",
		},
		{
			name:                 "initial version invalid, new version valid",
			initialVersion:       "invalid",
			newVersion:           "5",
			expectedUpdate:       true,
			expectedFinalVersion: "5",
			description:          "Should update when initial version is invalid (parsing fails)",
		},
		{
			name:                 "initial version valid, new version invalid",
			initialVersion:       "5",
			newVersion:           "invalid",
			expectedUpdate:       false,
			expectedFinalVersion: "5",
			description:          "Should NOT update when new version is invalid (0 <= 5)",
		},
		{
			name:                 "both versions invalid",
			initialVersion:       "invalid1",
			newVersion:           "invalid2",
			expectedUpdate:       false,
			expectedFinalVersion: "invalid1",
			description:          "Should NOT update when both versions are invalid (0 <= 0)",
		},
		{
			name:                 "large version numbers",
			initialVersion:       "999999",
			newVersion:           "1000000",
			expectedUpdate:       true,
			expectedFinalVersion: "1000000",
			description:          "Should handle large version numbers correctly",
		},
		{
			name:                 "zero versions",
			initialVersion:       "0",
			newVersion:           "0",
			expectedUpdate:       false,
			expectedFinalVersion: "0",
			description:          "Should NOT update when both versions are zero (0 <= 0)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := setupWithInitialVersion(t, tt.initialVersion)
			defer v.finish()

			// Create a device with the new version
			newDevice := newVersionedDevice(tt.newVersion)

			// Mock the API call to return the new device
			v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), v.deviceName, gomock.Any()).Return(newDevice, http.StatusOK, nil)

			// Record the initial version
			initialVersion := v.notifier.lastKnownVersion

			// Call pollAndPublish
			v.notifier.pollAndPublish(v.ctx)

			// Check if the version was updated
			if tt.expectedUpdate {
				v.assertions.Equal(tt.expectedFinalVersion, v.notifier.lastKnownVersion, tt.description)
			} else {
				v.assertions.Equal(initialVersion, v.notifier.lastKnownVersion, tt.description)
			}

			// Verify that a device was published to subscribers (current implementation always publishes)
			device, popped, err := v.watcher.TryPop()
			v.assertions.NoError(err)
			v.assertions.True(popped, "Expected device to be published to subscribers")
			v.assertions.Equal(tt.newVersion, device.Version())
		})
	}
}

func TestVersionComparisonWithRealAPI(t *testing.T) {
	t.Run("version comparison with actual API response", func(t *testing.T) {
		v := setupWithInitialVersion(t, "3")
		defer v.finish()

		// Create a device with version "7" (greater than initial "3")
		newDevice := newVersionedDevice("7")
		v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), v.deviceName, gomock.Any()).Return(newDevice, http.StatusOK, nil)

		// Call pollAndPublish
		v.notifier.pollAndPublish(v.ctx)

		// Verify version was updated
		v.assertions.Equal("7", v.notifier.lastKnownVersion)

		// Verify device was published
		device, popped, err := v.watcher.TryPop()
		v.assertions.NoError(err)
		v.assertions.True(popped)
		v.assertions.Equal("7", device.Version())
	})

	t.Run("version comparison with older version", func(t *testing.T) {
		v := setupWithInitialVersion(t, "10")
		defer v.finish()

		// Create a device with version "5" (less than initial "10")
		newDevice := newVersionedDevice("5")
		v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), v.deviceName, gomock.Any()).Return(newDevice, http.StatusOK, nil)

		// Call pollAndPublish
		v.notifier.pollAndPublish(v.ctx)

		// Verify version was NOT updated
		v.assertions.Equal("10", v.notifier.lastKnownVersion)

		// Verify device was published (current implementation always publishes)
		device, popped, err := v.watcher.TryPop()
		v.assertions.NoError(err)
		v.assertions.True(popped)
		v.assertions.Equal("5", device.Version())
	})
}

func TestNewWithInitialVersion(t *testing.T) {
	pollCfg := poll.NewConfig(time.Second, 1.5)

	t.Run("creates publisher with initial version", func(t *testing.T) {
		initialVersion := "42"
		p := newPublisher("test-device", pollCfg, initialVersion, nil, log.NewPrefixLogger(""))

		publisher, ok := p.(*publisher)
		require.True(t, ok)
		require.Equal(t, initialVersion, publisher.lastKnownVersion)
	})

	t.Run("creates publisher with empty initial version", func(t *testing.T) {
		initialVersion := ""
		p := newPublisher("test-device", pollCfg, initialVersion, nil, log.NewPrefixLogger(""))

		publisher, ok := p.(*publisher)
		require.True(t, ok)
		require.Equal(t, initialVersion, publisher.lastKnownVersion)
	})
}
