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

func TestDetectDeltaEligible(t *testing.T) {
	require := require.New(t)

	lookPath := func(present ...string) func(string) (string, error) {
		available := make(map[string]struct{}, len(present))
		for _, name := range present {
			available[name] = struct{}{}
		}
		return func(name string) (string, error) {
			if _, ok := available[name]; ok {
				return "/usr/bin/" + name, nil
			}
			return "", fmt.Errorf("not found: %s", name)
		}
	}
	versionOK := func(out string) func() (string, error) {
		return func() (string, error) { return out, nil }
	}
	versionErr := func() (string, error) {
		return "", fmt.Errorf("bootc --version failed")
	}

	testCases := []struct {
		name         string
		lookPath     func(string) (string, error)
		bootcVersion func() (string, error)
		expected     bool
	}{
		{
			name:         "When bootc is 1.15.0 and oci-delta is present it should return true",
			lookPath:     lookPath("bootc", "oci-delta"),
			bootcVersion: versionOK("bootc 1.15.0"),
			expected:     true,
		},
		{
			name:         "When bootc --version is a bare 1.15.0 it should return true",
			lookPath:     lookPath("bootc", "oci-delta"),
			bootcVersion: versionOK("1.15.0"),
			expected:     true,
		},
		{
			name:         "When bootc is newer than 1.15.0 and oci-delta is present it should return true",
			lookPath:     lookPath("bootc", "oci-delta"),
			bootcVersion: versionOK("1.15.1"),
			expected:     true,
		},
		{
			name:         "When bootc is 2.0.0 and oci-delta is present it should return true",
			lookPath:     lookPath("bootc", "oci-delta"),
			bootcVersion: versionOK("bootc 2.0.0"),
			expected:     true,
		},
		{
			name:         "When bootc version output has a trailing newline it should return true",
			lookPath:     lookPath("bootc", "oci-delta"),
			bootcVersion: versionOK("bootc 1.15.0\n"),
			expected:     true,
		},
		{
			name:         "When bootc is 1.14.9 and oci-delta is present it should return false",
			lookPath:     lookPath("bootc", "oci-delta"),
			bootcVersion: versionOK("bootc 1.14.9"),
			expected:     false,
		},
		{
			name:         "When bootc is 1.14.0 and oci-delta is present it should return false",
			lookPath:     lookPath("bootc", "oci-delta"),
			bootcVersion: versionOK("1.14.0"),
			expected:     false,
		},
		{
			name:         "When oci-delta is missing it should return false",
			lookPath:     lookPath("bootc"),
			bootcVersion: versionOK("bootc 1.15.0"),
			expected:     false,
		},
		{
			name:         "When bootc is missing it should return false",
			lookPath:     lookPath("oci-delta"),
			bootcVersion: versionOK("bootc 1.15.0"),
			expected:     false,
		},
		{
			name:         "When bootc --version fails it should return false",
			lookPath:     lookPath("bootc", "oci-delta"),
			bootcVersion: versionErr,
			expected:     false,
		},
		{
			name:         "When bootc version output is unparsable it should return false",
			lookPath:     lookPath("bootc", "oci-delta"),
			bootcVersion: versionOK("not a version"),
			expected:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectDeltaEligible(tc.lookPath, tc.bootcVersion)
			require.Equal(tc.expected, got)
		})
	}
}
