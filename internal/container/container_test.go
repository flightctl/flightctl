package container

import (
	"encoding/json"
	"os"
	"testing"

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
	// transport
	require.Equal("registry", status.Spec.Image.Transport)
	// booted
	require.Equal("quay.io/flightctl/flightctl-agent-fedora", status.Status.Booted.Image.Image.Image)
	// booted digest
	require.Equal("sha256:6adcbcf13b489758cc6fc8e659b8a2e310d3af609b8d319decef1e434b83c2a7", status.Status.Booted.Image.ImageDigest)
	// rollback image
	require.Equal("quay.io/flightctl/flightctl-agent-basic-nginx", status.Status.Rollback.Image.Image.Image)
	// staged image
	require.Equal("quay.io/flightctl/flightctl-agent-basic-nginx", status.Status.Staged.Image.Image.Image)
	// version
	require.Equal("stream9.20240224.0", status.Status.Staged.Image.Version)
	// timestamp
	require.Equal("", status.Status.Staged.Image.Timestamp)
	// ostree checksum
	require.Equal("f627c830e921afe918402486d5fe8a7ffaf3bd8c0d21311cba28facc9b17b9e2", status.Status.Staged.Ostree.Checksum)
	// pinned
	require.Equal(false, status.Status.Staged.Pinned)
	// deploy serial
	require.Equal(4, status.Status.Staged.Ostree.DeploySerial)

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
			expectedError: ErrParsingImage,
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
			target, err := imageToBootcTarget(testCase.image)

			if testCase.expectedError != nil {
				require.ErrorIs(err, testCase.expectedError)
				return
			}

			require.NoError(err)
			require.Equal(testCase.expectedResult, target)
		})
	}
}
