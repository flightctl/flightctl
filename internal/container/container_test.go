package container

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/stretchr/testify/require"
)

func TestBootcHost(t *testing.T) {
	require := require.New(t)
	statusBytes, err := os.ReadFile("testdata/bootc_status.json")
	require.NoError(err)

	var status BootcHost
	err = json.Unmarshal(statusBytes, &status)
	require.NoError(err)

	// spec image
	require.Equal("quay.io/flightctl/flightctl-agent-basic-nginx", status.Spec.Image.Image)
	// booted
	require.Equal("quay.io/flightctl/flightctl-agent-fedora", status.Status.Booted.Image.Image.Image)
	// booted digest
	require.Equal("sha256:6adcbcf13b489758cc6fc8e659b8a2e310d3af609b8d319decef1e434b83c2a7", status.Status.Booted.Image.ImageDigest)
	// rollback image
	require.Equal("quay.io/flightctl/flightctl-agent-basic-nginx", status.Status.Rollback.Image.Image.Image)
	// staged image
	require.Equal("quay.io/flightctl/flightctl-agent-basic-nginx", status.Status.Staged.Image.Image.Image)
}

func TestIsOsImageReconciled(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name           string
		bootedImage    string
		desiredOs      *v1beta1.DeviceOsSpec
		expectedResult bool
		expectedError  error
	}{
		{
			name:           "booted and desired are the same",
			bootedImage:    "quay.io/org/flightctl-device",
			desiredOs:      &v1beta1.DeviceOsSpec{Image: "quay.io/org/flightctl-device"},
			expectedResult: true,
		},
		{
			name:           "booted and desired have different tags",
			bootedImage:    "quay.io/org/flightctl-device:v3",
			desiredOs:      &v1beta1.DeviceOsSpec{Image: "quay.io/org/flightctl-device:v9"},
			expectedResult: false,
		},
		{
			name:           "booted and desired are the same after image parsed to target",
			bootedImage:    "quay.io/org/flightctl-device@sha256:6cf77c2a98dd4df274d14834fab9424b6e96ef3ed3f49f792b27c163763f52b5",
			desiredOs:      &v1beta1.DeviceOsSpec{Image: "quay.io/org/flightctl-device:v3@sha256:6cf77c2a98dd4df274d14834fab9424b6e96ef3ed3f49f792b27c163763f52b5"},
			expectedResult: true,
		},
		{
			name:          "desired image cannot be parsed",
			bootedImage:   "quay.io/org/flightctl-device",
			desiredOs:     &v1beta1.DeviceOsSpec{Image: "_invalid"},
			expectedError: errors.ErrUnableToParseImageReference,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testHost := &BootcHost{}
			testHost.Status.Booted.Image.Image.Image = testCase.bootedImage
			testSpec := &v1beta1.DeviceSpec{Os: testCase.desiredOs}

			reconciled, err := IsOsImageReconciled(testHost, testSpec)

			if testCase.expectedError != nil {
				require.ErrorIs(err, testCase.expectedError)
				return
			}

			require.NoError(err)
			require.Equal(testCase.expectedResult, reconciled)
		})
	}
}

func Test_imageToBootcTarget(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name           string
		image          string
		expectedResult string
		expectedError  error
	}{
		{
			name:          "invalid image",
			image:         "_invalid",
			expectedError: errors.ErrUnableToParseImageReference,
		},
		{
			name:           "image with no tag or digest",
			image:          "quay.io/org/flightctl-device",
			expectedResult: "quay.io/org/flightctl-device",
		},
		{
			name:           "image with a port",
			image:          "some-registry:5000/flightctl-device",
			expectedResult: "some-registry:5000/flightctl-device",
		},
		{
			name:           "image with a tag and a port",
			image:          "some-registry:5000/flightctl-device:v3",
			expectedResult: "some-registry:5000/flightctl-device:v3",
		},
		{
			name:           "image with a tag",
			image:          "quay.io/org/flightctl-device:v3",
			expectedResult: "quay.io/org/flightctl-device:v3",
		},
		{
			name:           "image with a digest",
			image:          "quay.io/org/flightctl-device@sha256:6cf77c2a98dd4df274d14834fab9424b6e96ef3ed3f49f792b27c163763f52b5",
			expectedResult: "quay.io/org/flightctl-device@sha256:6cf77c2a98dd4df274d14834fab9424b6e96ef3ed3f49f792b27c163763f52b5",
		},
		{
			name:           "image with a tag and digest",
			image:          "quay.io/org/flightctl-device:v3@sha256:6cf77c2a98dd4df274d14834fab9424b6e96ef3ed3f49f792b27c163763f52b5",
			expectedResult: "quay.io/org/flightctl-device@sha256:6cf77c2a98dd4df274d14834fab9424b6e96ef3ed3f49f792b27c163763f52b5",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			target, err := ImageToBootcTarget(testCase.image)

			if testCase.expectedError != nil {
				require.ErrorIs(err, testCase.expectedError)
				return
			}

			require.NoError(err)
			require.Equal(testCase.expectedResult, target)
		})
	}
}
