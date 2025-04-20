package device_publisher

import (
	"context"
	"net/http"
	"testing"

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
	notifier   *devicePublisher
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
	n := &devicePublisher{
		managementClient: mockClient,
		deviceName:       deviceName,
		log:              log.NewPrefixLogger(""),
		backoff: wait.Backoff{
			Steps: 1,
		},
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
		v := setup(t)
		defer v.finish()
		requestErr := errors.New("failed to make request for spec")
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusInternalServerError, requestErr)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1alpha1.Device{})
		v.assertions.ErrorIs(err, errors.ErrGettingDeviceSpec)
	})

	t.Run("response status code has no content", func(tt *testing.T) {
		v := setup(t)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusNoContent, nil)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1alpha1.Device{})
		v.assertions.ErrorIs(err, errors.ErrNoContent)
	})

	t.Run("response status code has conflict", func(tt *testing.T) {
		v := setup(t)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusConflict, nil)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1alpha1.Device{})
		v.assertions.ErrorIs(err, errors.ErrNoContent)
	})

	t.Run("response is nil", func(tt *testing.T) {
		v := setup(t)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, v.deviceName, gomock.Any()).Return(nil, http.StatusOK, nil)

		_, err := v.notifier.getRenderedFromManagementAPIWithRetry(v.ctx, "1", &v1alpha1.Device{})
		v.assertions.ErrorIs(err, errors.ErrNilResponse)
	})

	t.Run("makes a request with empty params if no rendered version is passed", func(tt *testing.T) {
		v := setup(t)
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
		v := setup(t)
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
		s := &devicePublisher{}
		s.SetClient(mockClient)
		require.Equal(mockClient, s.managementClient)
	})
}

func TestNotifier_pollAndNotify(t *testing.T) {
	specErr := errors.New("problem with spec")
	t.Run("poll and notify failure", func(tt *testing.T) {
		v := setup(t)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, gomock.Any(), gomock.Any()).Return(nil, http.StatusServiceUnavailable, specErr)
		v.notifier.pollAndPublish(v.ctx)
		_, popped, err := v.sub.TryPop()
		require.NoError(t, err)
		require.False(t, popped)
	})
	t.Run("poll and notify no content", func(tt *testing.T) {
		v := setup(t)
		defer v.finish()
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, gomock.Any(), gomock.Any()).Return(nil, http.StatusNoContent, nil)
		v.notifier.pollAndPublish(v.ctx)
		_, popped, err := v.sub.TryPop()
		require.NoError(t, err)
		require.False(t, popped)
	})
	t.Run("poll and notify with content", func(tt *testing.T) {
		v := setup(t)
		defer v.finish()
		renderedDesiredSpec := createTestRenderedDevice("flightctl-device:v2")
		v.mockClient.EXPECT().GetRenderedDevice(v.ctx, gomock.Any(), gomock.Any()).Return(renderedDesiredSpec, 200, nil)
		v.notifier.pollAndPublish(v.ctx)
		result, popped, err := v.sub.TryPop()
		require.NoError(t, err)
		require.True(t, popped)
		require.Equal(t, renderedDesiredSpec, result)
	})
}
