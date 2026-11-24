package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	imagepruning "github.com/flightctl/flightctl/internal/agent/device/image_pruning"
	"github.com/stretchr/testify/require"
)

type fakeRetainedImages struct {
	images []string
	err    error
}

func (f fakeRetainedImages) RetainedImageReferences(context.Context) ([]string, error) {
	return f.images, f.err
}

func TestFirmwareRequiredReferences(t *testing.T) {
	const updater = "quay.io/example/updater@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	readErr := errors.New("registry unreadable")
	testCases := []struct {
		name          string
		retention     fakeRetainedImages
		expected      []imagepruning.ImageRef
		expectedError error
	}{
		{
			name:      "When payloads are pinned it should require their images in the root podman store",
			retention: fakeRetainedImages{images: []string{updater}},
			expected:  []imagepruning.ImageRef{{Image: updater, Owner: v1beta1.CurrentProcessUsername, Type: imagepruning.RefTypePodman}},
		},
		{
			name:      "When no payload is pinned it should require nothing",
			retention: fakeRetainedImages{images: []string{}},
			expected:  []imagepruning.ImageRef{},
		},
		{
			name:          "When the retention registry cannot be read it should return the error",
			retention:     fakeRetainedImages{err: readErr},
			expectedError: readErr,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			refs, err := firmwareRequiredReferences(tc.retention)(context.Background())
			if tc.expectedError != nil {
				require.ErrorIs(err, tc.expectedError)
				require.Nil(refs)
				return
			}
			require.NoError(err)
			require.Equal(tc.expected, refs)
		})
	}
}
