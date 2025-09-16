package publisher

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/util/wait"
)

type vars struct {
	assertions *require.Assertions
	ctrl       *gomock.Controller
	mockClient *client.MockManagement
	notifier   *publisher
	ctx        context.Context
	cancel     context.CancelFunc
	deviceName string
	sub        Subscription
}

func (v *vars) finish() {
	v.ctrl.Finish()
	v.cancel()
}

func setup(t *testing.T) *vars {
	ctrl := gomock.NewController(t)
	deviceName := "test-device"
	ctx, cancel := context.WithCancel(context.Background())
	mockClient := client.NewMockManagement(ctrl)

	// Create a mock device not found handler
	deviceNotFoundHandler := func() error {
		return nil // Mock handler that always succeeds
	}

	n := &publisher{
		managementClient: mockClient,
		deviceName:       deviceName,
		log:              log.NewPrefixLogger(""),
		interval:         time.Second,
		backoff: wait.Backoff{
			Steps: 1,
		},
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
		sub:        n.Subscribe(),
	}
}

func newVersionedDevice(version string) *v1alpha1.Device {
	deice := &v1alpha1.Device{
		Metadata: v1alpha1.ObjectMeta{
			Annotations: lo.ToPtr(map[string]string{
				v1alpha1.DeviceAnnotationRenderedVersion: version,
			}),
		},
	}
	deice.Spec = &v1alpha1.DeviceSpec{}
	return deice
}

func createTestRenderedDevice(image string) *v1alpha1.Device {
	device := newVersionedDevice("1")
	spec := v1alpha1.DeviceSpec{
		Os: &v1alpha1.DeviceOsSpec{
			Image: image,
		},
	}
	device.Spec = &spec
	return device
}

func Test_getRenderedFromManagementAPIWithRetry(t *testing.T) {
	t.Run("request error", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		requestErr := errors.New("failed to make request for spec")
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusInternalServerError, requestErr)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1alpha1.Device{})
		v.assertions.ErrorIs(err, errors.ErrGettingDeviceSpec)
	})

	t.Run("response status code has no content", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusNoContent, nil)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1alpha1.Device{})
		v.assertions.ErrorIs(err, errors.ErrNoContent)
	})

	t.Run("response status code has conflict", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusConflict, nil)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1alpha1.Device{})
		v.assertions.ErrorIs(err, errors.ErrNoContent)
	})

	t.Run("response is nil", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusOK, nil)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1alpha1.Device{})
		v.assertions.ErrorIs(err, errors.ErrNilResponse)
	})

	t.Run("makes a request with empty params if no rendered version is passed", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		device := createTestRenderedDevice("requested-image:latest")
		params := &v1alpha1.GetRenderedDeviceParams{}
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, params).Return(device, http.StatusOK, nil)

		rendered := &v1alpha1.Device{}
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
		params := &v1alpha1.GetRenderedDeviceParams{KnownRenderedVersion: &renderedVersion}
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, params).Return(device, http.StatusOK, nil)

		rendered := &v1alpha1.Device{}
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
		_, popped, err := v.sub.TryPop()
		require.NoError(t, err)
		require.False(t, popped)
	})
	t.Run("poll and notify no content", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, http.StatusNoContent, nil)
		v.notifier.pollAndPublish(v.ctx)
		_, popped, err := v.sub.TryPop()
		require.NoError(t, err)
		require.False(t, popped)
	})
	t.Run("poll and notify with content", func(tt *testing.T) {
		v := setup(tt)
		defer v.finish()
		renderedDesiredSpec := createTestRenderedDevice("flightctl-device:v2")
		v.mockClient.EXPECT().GetRenderedDevice(gomock.Any(), gomock.Any(), gomock.Any()).Return(renderedDesiredSpec, 200, nil)
		v.notifier.pollAndPublish(v.ctx)
		result, popped, err := v.sub.TryPop()
		require.NoError(t, err)
		require.True(t, popped)
		require.Equal(t, renderedDesiredSpec, result)
	})
}

func TestDevicePublisher_Run(t *testing.T) {
	t.Run("stops when context is canceled", func(tt *testing.T) {
		v := setup(tt)
		defer v.cancel()

		wg := &sync.WaitGroup{}
		wg.Add(1)

		ctx, cancel := context.WithCancel(context.Background())

		// Start the publisher in a goroutine
		go v.notifier.Run(ctx, wg)

		// Wait a short time to ensure it's started
		time.Sleep(10 * time.Millisecond)

		// Cancel the context to stop the publisher
		cancel()

		// Use a timeout to avoid hanging the test if wg.Wait() doesn't return
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

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
				managementClient: v.mockClient,
				deviceName:       v.deviceName,
				log:              log.NewPrefixLogger(""),
				interval:         time.Second,
				backoff: wait.Backoff{
					Steps: 1,
				},
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
