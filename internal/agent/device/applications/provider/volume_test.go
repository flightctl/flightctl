package provider

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestWriteComposeOverride(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name     string
		appName  string
		volumes  *[]v1alpha1.ApplicationVolume
		expected string
		written  bool
	}{
		{
			name:    "no volumes",
			appName: "testapp",
			volumes: nil,
			written: false,
		},
		{
			name:    "single volume",
			appName: "testapp",
			volumes: &[]v1alpha1.ApplicationVolume{
				{
					Name: "vol1",
				},
			},
			expected: `volumes:
  vol1:
    external: true
    name: testapp-vol1-258737`,
			written: true,
		},
		{
			name:    "multiple volumes",
			appName: "app1",
			volumes: &[]v1alpha1.ApplicationVolume{
				{Name: "data"},
				{Name: "cache"},
			},
			expected: `volumes:
  cache:
    external: true
    name: app1-cache-300518
  data:
    external: true
    name: app1-data-254868`,
			written: true,
		},
		{
			name:    "empty volumes slice",
			appName: "empty",
			volumes: &[]v1alpha1.ApplicationVolume{},
			written: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			log := log.NewPrefixLogger("test")
			writer := fileio.NewReadWriter()
			writer.SetRootdir(tmpDir)

			err := writeComposeOverride(log, "/etc/compose/manifest", tt.appName, tt.volumes, writer, client.ComposeOverrideFilename)
			require.NoError(err)

			path := filepath.Join("/etc/compose/manifest", client.ComposeOverrideFilename)
			exists, err := writer.PathExists(path)
			require.NoError(err)
			require.Equal(tt.written, exists)

			if tt.written {
				bytes, err := writer.ReadFile(path)
				require.NoError(err)
				require.Equal(strings.TrimSpace(tt.expected), strings.TrimSpace(string(bytes)))
			}
		})
	}
}
