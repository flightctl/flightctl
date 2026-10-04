package bootc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupportsDownloadOnlySwitch(t *testing.T) {
	testCases := []struct {
		name    string
		version string
		want    bool
	}{
		{
			name:    "When bootc is at the minimum version it should support download-only switch",
			version: "bootc 1.16.10",
			want:    true,
		},
		{
			name:    "When bootc is newer than the minimum it should support download-only switch",
			version: "bootc 1.17.0",
			want:    true,
		},
		{
			name:    "When bootc is older than the minimum it should not support download-only switch",
			version: "bootc 1.16.9",
			want:    false,
		},
		{
			name:    "When bootc is a prerelease of the minimum it should not support download-only switch",
			version: "bootc 1.16.10-rc.1",
			want:    false,
		},
		{
			name:    "When version output is malformed it should not support download-only switch",
			version: "bootc unknown",
			want:    false,
		},
		{
			name:    "When version output is missing it should not support download-only switch",
			version: "",
			want:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, SupportsDownloadOnlySwitch(tc.version))
		})
	}
}
