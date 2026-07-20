package os

import (
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/require"
)

func TestDetectMode(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name     string
		lookPath func(string) (string, error)
		expected v1beta1.OsModeType
	}{
		{
			name: "When bootc is available it should return image mode",
			lookPath: func(name string) (string, error) {
				if name == "bootc" {
					return "/usr/bin/bootc", nil
				}
				return "", fmt.Errorf("not found: %s", name)
			},
			expected: v1beta1.OsModeImage,
		},
		{
			name: "When rpm-ostree is available without bootc it should return image mode",
			lookPath: func(name string) (string, error) {
				if name == "rpm-ostree" {
					return "/usr/bin/rpm-ostree", nil
				}
				return "", fmt.Errorf("not found: %s", name)
			},
			expected: v1beta1.OsModeImage,
		},
		{
			name: "When both bootc and rpm-ostree are available it should return image mode",
			lookPath: func(name string) (string, error) {
				switch name {
				case "bootc":
					return "/usr/bin/bootc", nil
				case "rpm-ostree":
					return "/usr/bin/rpm-ostree", nil
				default:
					return "", fmt.Errorf("not found: %s", name)
				}
			},
			expected: v1beta1.OsModeImage,
		},
		{
			name: "When neither bootc nor rpm-ostree is available it should return package mode",
			lookPath: func(name string) (string, error) {
				return "", fmt.Errorf("not found: %s", name)
			},
			expected: v1beta1.OsModePackage,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mode := DetectMode(tc.lookPath)
			require.Equal(tc.expected, mode)
		})
	}
}
