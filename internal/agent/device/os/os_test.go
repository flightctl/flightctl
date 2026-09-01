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
		deltaEligible     bool
		bootedImage       string
		bootedImageDigest string
		expectedImage     string
		expectedDigest    string
		expectedEligible  bool
	}{
		{
			name:              "When image mode and delta eligible it should populate os fields and DeltaEligible true",
			osMode:            v1beta1.OsModeImage,
			deltaEligible:     true,
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
			expectedEligible:  true,
		},
		{
			name:              "When image mode and not delta eligible it should report DeltaEligible false",
			osMode:            v1beta1.OsModeImage,
			deltaEligible:     false,
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
			expectedEligible:  false,
		},
		{
			name:              "When package mode it should report empty os fields and DeltaEligible false",
			osMode:            v1beta1.OsModePackage,
			deltaEligible:     false,
			bootedImage:       "",
			bootedImageDigest: "",
			expectedImage:     "",
			expectedDigest:    "",
			expectedEligible:  false,
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
				caps: Capabilities{
					OsMode:          tc.osMode,
					DeltaEligible:   tc.deltaEligible,
					BootcVersion:    "bootc 1.15.0",
					OCIDeltaVersion: "oci-delta 0.2.1",
				},
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
			require.NotNil(status.SystemInfo.DeltaEligible)
			require.Equal(tc.expectedEligible, *status.SystemInfo.DeltaEligible)
			require.NotNil(status.SystemInfo.BootcVersion)
			require.Equal("bootc 1.15.0", *status.SystemInfo.BootcVersion)
			require.NotNil(status.SystemInfo.OciDeltaVersion)
			require.Equal("oci-delta 0.2.1", *status.SystemInfo.OciDeltaVersion)
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
		caps:   Capabilities{OsMode: v1beta1.OsModePackage},
	}

	status := &v1beta1.DeviceStatus{}
	err := m.Status(context.Background(), status)
	require.ErrorIs(err, clientErr)
	require.Nil(status.Capabilities)
}
