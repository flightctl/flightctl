package os

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/require"
)

func TestClientCapabilitiesMode(t *testing.T) {
	testCases := []struct {
		name     string
		client   Client
		wantMode v1beta1.OsModeType
	}{
		{
			name:     "When client is bootc it should report image mode",
			client:   &bootc{lookPath: lookPathNone(), bootcVersion: versionOK("0.0.0"), ociDeltaVersion: versionOK("")},
			wantMode: v1beta1.OsModeImage,
		},
		{
			name:     "When client is rpm-ostree it should report image mode",
			client:   &rpmOSTree{lookPath: lookPathNone(), bootcVersion: versionOK(""), ociDeltaVersion: versionOK("")},
			wantMode: v1beta1.OsModeImage,
		},
		{
			name:     "When client is dummy it should report package mode",
			client:   &dummy{lookPath: lookPathNone(), bootcVersion: versionOK(""), ociDeltaVersion: versionOK("")},
			wantMode: v1beta1.OsModePackage,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			caps := tc.client.Capabilities(t.Context())
			require.Equal(tc.wantMode, caps.OsMode)
		})
	}
}

func TestBootcCapabilitiesDeltaEligible(t *testing.T) {
	testCases := []struct {
		name            string
		lookPath        func(string) (string, error)
		bootcVersion    func(context.Context) (string, error)
		ociDeltaVersion func(context.Context) (string, error)
		expected        bool
		wantBootc       string
		wantOCI         string
	}{
		{
			name:            "When bootc is 1.15.0 and oci-delta is present it should return true",
			lookPath:        lookPathPresent("bootc", "oci-delta"),
			bootcVersion:    versionOK("bootc 1.15.0"),
			ociDeltaVersion: versionOK("oci-delta 0.2.1"),
			expected:        true,
			wantBootc:       "bootc 1.15.0",
			wantOCI:         "oci-delta 0.2.1",
		},
		{
			name:            "When bootc --version is a bare 1.15.0 it should return true",
			lookPath:        lookPathPresent("bootc", "oci-delta"),
			bootcVersion:    versionOK("1.15.0"),
			ociDeltaVersion: versionOK("oci-delta 0.1.0"),
			expected:        true,
			wantBootc:       "1.15.0",
			wantOCI:         "oci-delta 0.1.0",
		},
		{
			name:            "When bootc is newer than 1.15.0 and oci-delta is present it should return true",
			lookPath:        lookPathPresent("bootc", "oci-delta"),
			bootcVersion:    versionOK("1.15.1"),
			ociDeltaVersion: versionOK("oci-delta 0.1.0"),
			expected:        true,
			wantBootc:       "1.15.1",
			wantOCI:         "oci-delta 0.1.0",
		},
		{
			name:            "When bootc is 2.0.0 and oci-delta is present it should return true",
			lookPath:        lookPathPresent("bootc", "oci-delta"),
			bootcVersion:    versionOK("bootc 2.0.0"),
			ociDeltaVersion: versionOK("oci-delta 0.1.0"),
			expected:        true,
			wantBootc:       "bootc 2.0.0",
			wantOCI:         "oci-delta 0.1.0",
		},
		{
			name:            "When bootc version output has a trailing newline it should return true",
			lookPath:        lookPathPresent("bootc", "oci-delta"),
			bootcVersion:    versionOK("bootc 1.15.0\n"),
			ociDeltaVersion: versionOK("oci-delta 0.1.0"),
			expected:        true,
			wantBootc:       "bootc 1.15.0",
			wantOCI:         "oci-delta 0.1.0",
		},
		{
			name:            "When bootc is 1.14.9 and oci-delta is present it should return false",
			lookPath:        lookPathPresent("bootc", "oci-delta"),
			bootcVersion:    versionOK("bootc 1.14.9"),
			ociDeltaVersion: versionOK("oci-delta 0.1.0"),
			expected:        false,
			wantBootc:       "bootc 1.14.9",
			wantOCI:         "oci-delta 0.1.0",
		},
		{
			name:            "When bootc is 1.14.0 and oci-delta is present it should return false",
			lookPath:        lookPathPresent("bootc", "oci-delta"),
			bootcVersion:    versionOK("1.14.0"),
			ociDeltaVersion: versionOK("oci-delta 0.1.0"),
			expected:        false,
			wantBootc:       "1.14.0",
			wantOCI:         "oci-delta 0.1.0",
		},
		{
			name:         "When oci-delta is missing it should return false",
			lookPath:     lookPathPresent("bootc"),
			bootcVersion: versionOK("bootc 1.15.0"),
			expected:     false,
			wantBootc:    "bootc 1.15.0",
		},
		{
			name:            "When bootc is missing it should return false",
			lookPath:        lookPathPresent("oci-delta"),
			bootcVersion:    versionOK("bootc 1.15.0"),
			ociDeltaVersion: versionOK("oci-delta 0.1.0"),
			expected:        false,
			wantOCI:         "oci-delta 0.1.0",
		},
		{
			name:            "When bootc --version fails it should return false",
			lookPath:        lookPathPresent("bootc", "oci-delta"),
			bootcVersion:    versionErr,
			ociDeltaVersion: versionOK("oci-delta 0.1.0"),
			expected:        false,
			wantOCI:         "oci-delta 0.1.0",
		},
		{
			name:            "When bootc version output is unparsable it should return false",
			lookPath:        lookPathPresent("bootc", "oci-delta"),
			bootcVersion:    versionOK("not a version"),
			ociDeltaVersion: versionOK("oci-delta 0.1.0"),
			expected:        false,
			wantBootc:       "not a version",
			wantOCI:         "oci-delta 0.1.0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			c := &bootc{lookPath: tc.lookPath, bootcVersion: tc.bootcVersion, ociDeltaVersion: tc.ociDeltaVersion}
			caps := c.Capabilities(t.Context())
			require.Equal(v1beta1.OsModeImage, caps.OsMode)
			require.Equal(tc.expected, caps.DeltaEligible)
			require.Equal(tc.wantBootc, caps.BootcVersion)
			require.Equal(tc.wantOCI, caps.OCIDeltaVersion)
		})
	}
}

func lookPathPresent(present ...string) func(string) (string, error) {
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

func lookPathNone() func(string) (string, error) {
	return lookPathPresent()
}

func versionOK(out string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return out, nil }
}

func versionErr(context.Context) (string, error) {
	return "", fmt.Errorf("bootc --version failed")
}
