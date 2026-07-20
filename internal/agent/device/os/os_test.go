package os

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestManagerStatus(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name              string
		osMode            v1beta1.OsModeType
		bootedImage       string
		bootedImageDigest string
		expectedImage     string
		expectedDigest    string
	}{
		{
			name:              "When image mode it should populate os fields and capabilities",
			osMode:            v1beta1.OsModeImage,
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
		},
		{
			name:              "When package mode it should report empty os fields and package capabilities",
			osMode:            v1beta1.OsModePackage,
			bootedImage:       "",
			bootedImageDigest: "",
			expectedImage:     "",
			expectedDigest:    "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := NewMockClient(ctrl)

			bootcHost := container.BootcHost{}
			bootcHost.Status.Booted.Image.Image.Image = tc.bootedImage
			bootcHost.Status.Booted.Image.ImageDigest = tc.bootedImageDigest

			mockClient.EXPECT().Status(gomock.Any()).Return(&Status{BootcHost: bootcHost}, nil)

			m := &manager{
				client: mockClient,
				osMode: tc.osMode,
			}

			ctx := context.Background()
			status := &v1beta1.DeviceStatus{}

			err := m.Status(ctx, status)
			require.NoError(err)
			require.Equal(tc.expectedImage, status.Os.Image)
			require.Equal(tc.expectedDigest, status.Os.ImageDigest)
			require.NotNil(status.Capabilities)
			require.NotNil(status.Capabilities.OsMode)
			require.Equal(tc.osMode, *status.Capabilities.OsMode)
		})
	}
}

func TestManagerStatusWhenClientFails(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockClient(ctrl)
	clientErr := errors.New("status unavailable")
	mockClient.EXPECT().Status(gomock.Any()).Return(nil, clientErr)

	m := &manager{
		client: mockClient,
		osMode: v1beta1.OsModePackage,
	}

	status := &v1beta1.DeviceStatus{}
	err := m.Status(context.Background(), status)
	require.ErrorIs(err, clientErr)
	require.Nil(status.Capabilities)
}
