package image

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_areImagesEquivalent(t *testing.T) {
	require := require.New(t)

	digest := "sha256:6cf77c2a98dd4df274d14834fab9424b6e96ef3ed3f49f792b27c163763f52b5"
	digestTwo := "sha256:bd1b50c5a1df1bcb701e3556075a890c4e4a87765f985ee3a4b87df91db98c4d"
	testCases := []struct {
		name           string
		imageOne       *Image
		imageTwo       *Image
		expectedResult bool
	}{
		{
			name:           "both are nil",
			imageOne:       nil,
			imageTwo:       nil,
			expectedResult: true,
		},
		{
			name:           "one is defined and the other nil",
			imageOne:       nil,
			imageTwo:       &Image{},
			expectedResult: false,
		},
		{
			name: "image digests are equal",
			imageOne: &Image{
				Digest: digest,
			},
			imageTwo: &Image{
				Digest: digest,
			},
			expectedResult: true,
		},
		{
			name: "image digests are not equal",
			imageOne: &Image{
				Digest: digest,
			},
			imageTwo: &Image{
				Digest: digestTwo,
			},
			expectedResult: false,
		},
		{
			name: "image bases match",
			imageOne: &Image{
				Base: "flightct-device",
			},
			imageTwo: &Image{
				Base: "flightct-device",
			},
			expectedResult: true,
		},
		{
			name: "image bases match when one image has a digest defined",
			imageOne: &Image{
				Base:   "flightct-device",
				Digest: digest,
			},
			imageTwo: &Image{
				Base: "flightct-device",
			},
			expectedResult: true,
		},
		{
			name: "image bases are different",
			imageOne: &Image{
				Base: "flightct-device",
			},
			imageTwo: &Image{
				Base: "device-os",
			},
			expectedResult: false,
		},
		{
			name: "image bases are different but digests are identical",
			imageOne: &Image{
				Base:   "flightct-device",
				Digest: digest,
			},
			imageTwo: &Image{
				Base:   "device-os",
				Digest: digest,
			},
			expectedResult: true,
		},
		{
			name: "image bases match and one has a tag",
			imageOne: &Image{
				Base: "flightct-device",
				Tag:  "v1",
			},
			imageTwo: &Image{
				Base: "flightct-device",
			},
			expectedResult: false,
		},
		{
			name: "image bases match and tags match",
			imageOne: &Image{
				Base: "flightct-device",
				Tag:  "v2",
			},
			imageTwo: &Image{
				Base: "flightct-device",
				Tag:  "v2",
			},
			expectedResult: true,
		},
		{
			name: "image bases match and tags are different",
			imageOne: &Image{
				Base: "flightct-device",
				Tag:  "v2",
			},
			imageTwo: &Image{
				Base: "flightct-device",
				Tag:  "v9",
			},
			expectedResult: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := AreImagesEquivalent(testCase.imageOne, testCase.imageTwo)
			require.Equal(testCase.expectedResult, result)
		})
	}
}

func Test_parseImage(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name           string
		image          string
		expectedResult *Image
	}{
		{
			name:  "image with a tag and digest",
			image: "flightctl-device:v3@sha256:123abc",
			expectedResult: &Image{
				Base:   "flightctl-device",
				Tag:    "v3",
				Digest: "sha256:123abc",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := parseImage(testCase.image)
			require.Equal(testCase.expectedResult, result)
		})
	}
}
