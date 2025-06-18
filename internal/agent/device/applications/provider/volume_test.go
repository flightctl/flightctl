package provider

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestWriteComposeOverrideDiff(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		base     *common.ComposeSpec
		patched  *common.ComposeSpec
		renamed  map[string]string
		expected string
		written  bool
	}{
		{
			name: "no changes",
			base: &common.ComposeSpec{
				Volumes: map[string]common.ComposeVolume{
					"vol1": {External: true},
				},
				Services: map[string]common.ComposeService{
					"svc1": {
						Image:   "alpine",
						Volumes: []string{"vol1:/data"},
					},
				},
			},
			patched: &common.ComposeSpec{
				Volumes: map[string]common.ComposeVolume{
					"vol1": {External: true},
				},
				Services: map[string]common.ComposeService{
					"svc1": {
						Image:   "alpine",
						Volumes: []string{"vol1:/data"},
					},
				},
			},
			renamed: nil,
			written: false,
		},
		{
			name: "volume renamed",
			base: &common.ComposeSpec{
				Volumes: map[string]common.ComposeVolume{
					"vol1": {External: true},
				},
			},
			patched: &common.ComposeSpec{
				Volumes: map[string]common.ComposeVolume{
					"vol1-renamed": {External: true},
				},
			},
			renamed: map[string]string{"vol1": "vol1-renamed"},
			expected: `volumes:
  vol1-renamed:
    external: true`,
			written: true,
		},
		{
			name: "volume and service references renamed",
			base: &common.ComposeSpec{
				Volumes: map[string]common.ComposeVolume{
					"vol1": {External: true},
				},
				Services: map[string]common.ComposeService{
					"svc1": {
						Image:   "nginx",
						Volumes: []string{"vol1:/mnt"},
					},
				},
			},
			patched: &common.ComposeSpec{
				Volumes: map[string]common.ComposeVolume{
					"vol1-renamed": {External: true},
				},
				Services: map[string]common.ComposeService{
					"svc1": {
						Image:   "nginx",
						Volumes: []string{"vol1-renamed:/mnt"},
					},
				},
			},
			renamed: map[string]string{"vol1": "vol1-renamed"},
			expected: `services:
  svc1:
    image: nginx
    volumes:
    - vol1-renamed:/mnt
volumes:
  vol1-renamed:
    external: true`,
			written: true,
		},
		{
			name: "multiple services with one referencing renamed volume",
			base: &common.ComposeSpec{
				Volumes: map[string]common.ComposeVolume{
					"vol1": {External: true},
				},
				Services: map[string]common.ComposeService{
					"svcA": {
						Image:   "busybox",
						Volumes: []string{}, // no volumes
					},
					"svcB": {
						Image:   "nginx",
						Volumes: []string{"vol1:/mnt"},
					},
					"svcC": {
						Image:   "redis",
						Volumes: []string{}, // no volumes
					},
				},
			},
			patched: &common.ComposeSpec{
				Volumes: map[string]common.ComposeVolume{
					"vol1-renamed": {External: true},
				},
				Services: map[string]common.ComposeService{
					"svcA": {
						Image:   "busybox",
						Volumes: []string{},
					},
					"svcB": {
						Image:   "nginx",
						Volumes: []string{"vol1-renamed:/mnt"},
					},
					"svcC": {
						Image:   "redis",
						Volumes: []string{},
					},
				},
			},
			renamed: map[string]string{"vol1": "vol1-renamed"},
			expected: `services:
  svcB:
    image: nginx
    volumes:
    - vol1-renamed:/mnt
volumes:
  vol1-renamed:
    external: true`,
			written: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := "/etc/compose/manifest/test-app"
			tmpDir := t.TempDir()
			log := log.NewPrefixLogger("test")
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)
			err := writeComposeOverrideDiff(log, root, tt.base, tt.patched, tt.renamed, readWriter, client.ComposeOverrideFilename)
			require.NoError(err)

			path := filepath.Join(root, client.ComposeOverrideFilename)
			exists, err := readWriter.PathExists(path)
			require.NoError(err)
			require.Equal(tt.written, exists)

			if tt.written {
				bytes, err := readWriter.ReadFile(path)
				require.NoError(err)
				require.Equal(strings.TrimSpace(tt.expected), strings.TrimSpace(string(bytes)))
			}
		})
	}
}
