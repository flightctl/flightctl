package provider

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestWriteComposeOverride(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name     string
		appName  string
		volumes  []string
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
			volumes: []string{"vol1"},
			expected: `volumes:
  vol1:
    external: true
    name: testapp-vol1-258737`,
			written: true,
		},
		{
			name:    "multiple volumes",
			appName: "app1",
			volumes: []string{"data", "cache"},
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
			volumes: []string{},
			written: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			log := log.NewPrefixLogger("test")
			writer := fileio.NewReadWriter()
			writer.SetRootdir(tmpDir)

			volumeManager, err := NewVolumeManager(log, tt.appName, v1beta1.AppTypeCompose, newTestImageApplicationVolumes(require, tt.volumes))
			require.NoError(err)

			err = writeComposeOverride(log, "/etc/compose/manifest", volumeManager, writer, client.ComposeOverrideFilename)
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

func newTestImageApplicationVolumes(require *require.Assertions, names []string) *[]v1beta1.ApplicationVolume {
	spec := v1beta1.ImageVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{
			Reference: "quay.io/test/artifact:latest",
		},
	}
	volumes := []v1beta1.ApplicationVolume{}
	for _, volName := range names {
		vol := v1beta1.ApplicationVolume{Name: volName}
		err := vol.FromImageVolumeProviderSpec(spec)
		require.NoError(err)
		volumes = append(volumes, vol)
	}

	return &volumes
}

func Test_extractQuadletVolumesFromSpec(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name            string
		appID           string
		contents        []v1beta1.ApplicationContent
		expectedVolumes []Volume
		expectError     bool
	}{
		{
			name:  "single volume with image",
			appID: "test-app",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "data.volume",
					Content: lo.ToPtr(`[Volume]
Image=quay.io/test/data:latest`),
				},
			},
			expectedVolumes: []Volume{
				{
					Name:          "data.volume",
					ID:            "systemd-test-app-data",
					Reference:     "quay.io/test/data:latest",
					ReclaimPolicy: v1beta1.Retain,
				},
			},
			expectError: false,
		},
		{
			name:  "multiple volumes with images",
			appID: "myapp",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "data.volume",
					Content: lo.ToPtr(`[Volume]
Image=quay.io/test/data:v1.0`),
				},
				{
					Path: "cache.volume",
					Content: lo.ToPtr(`[Volume]
Image=quay.io/test/cache:v2.0`),
				},
			},
			expectedVolumes: []Volume{
				{
					Name:          "data.volume",
					ID:            "systemd-myapp-data",
					Reference:     "quay.io/test/data:v1.0",
					ReclaimPolicy: v1beta1.Retain,
				},
				{
					Name:          "cache.volume",
					ID:            "systemd-myapp-cache",
					Reference:     "quay.io/test/cache:v2.0",
					ReclaimPolicy: v1beta1.Retain,
				},
			},
			expectError: false,
		},
		{
			name:  "mixed quadlet types - only volumes with images extracted",
			appID: "mixed-app",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/test/app:latest`),
				},
				{
					Path: "data.volume",
					Content: lo.ToPtr(`[Volume]
Image=quay.io/test/data:latest`),
				},
				{
					Path: "empty.volume",
					Content: lo.ToPtr(`[Volume]
Device=/dev/sda1`),
				},
				{
					Path: "net.network",
					Content: lo.ToPtr(`[Network]
`),
				},
			},
			expectedVolumes: []Volume{
				{
					Name:          "data.volume",
					ID:            "systemd-mixed-app-data",
					Reference:     "quay.io/test/data:latest",
					ReclaimPolicy: v1beta1.Retain,
				},
			},
			expectError: false,
		},
		{
			name:  "volume with custom name",
			appID: "app",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "storage.volume",
					Content: lo.ToPtr(`[Volume]
Image=quay.io/test/storage:latest
VolumeName=custom-storage-name`),
				},
			},
			expectedVolumes: []Volume{
				{
					Name:          "storage.volume",
					ID:            "custom-storage-name",
					Reference:     "quay.io/test/storage:latest",
					ReclaimPolicy: v1beta1.Retain,
				},
			},
			expectError: false,
		},
		{
			name:  "no volumes - only containers",
			appID: "app",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/test/app:latest`),
				},
			},
			expectedVolumes: []Volume{},
			expectError:     false,
		},
		{
			name:            "empty contents",
			appID:           "app",
			contents:        []v1beta1.ApplicationContent{},
			expectedVolumes: nil,
			expectError:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			volumes, err := extractQuadletVolumesFromSpec(tt.appID, tt.contents)

			if tt.expectError {
				require.Error(err)
				return
			}

			require.NoError(err)
			require.Equal(len(tt.expectedVolumes), len(volumes))

			sort.Slice(volumes, func(i, j int) bool {
				return volumes[i].Name < volumes[j].Name
			})

			sort.Slice(tt.expectedVolumes, func(i, j int) bool {
				return tt.expectedVolumes[i].Name < tt.expectedVolumes[j].Name
			})

			for i, expectedVol := range tt.expectedVolumes {
				require.Equal(expectedVol.Name, volumes[i].Name)
				require.Equal(expectedVol.ID, volumes[i].ID)
				require.Equal(expectedVol.Reference, volumes[i].Reference)
				require.Equal(expectedVol.Available, volumes[i].Available)
				require.Equal(expectedVol.ReclaimPolicy, volumes[i].ReclaimPolicy, "volume %q reclaim policy mismatch", expectedVol.Name)
			}
		})
	}
}

func Test_extractQuadletVolumesFromDir(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name            string
		appID           string
		files           map[string][]byte
		expectedVolumes []Volume
		expectError     bool
	}{
		{
			name:  "single volume file with image",
			appID: "test-app",
			files: map[string][]byte{
				"data.volume": []byte(`[Volume]
Image=quay.io/test/data:latest`),
			},
			expectedVolumes: []Volume{
				{
					Name:          "data.volume",
					ID:            "systemd-test-app-data",
					Reference:     "quay.io/test/data:latest",
					ReclaimPolicy: v1beta1.Retain,
				},
			},
			expectError: false,
		},
		{
			name:  "multiple volume files with images",
			appID: "myapp",
			files: map[string][]byte{
				"data.volume": []byte(`[Volume]
Image=quay.io/test/data:v1.0`),
				"cache.volume": []byte(`[Volume]
Image=quay.io/test/cache:v2.0`),
			},
			expectedVolumes: []Volume{
				{
					Name:          "data.volume",
					ID:            "systemd-myapp-data",
					Reference:     "quay.io/test/data:v1.0",
					ReclaimPolicy: v1beta1.Retain,
				},
				{
					Name:          "cache.volume",
					ID:            "systemd-myapp-cache",
					Reference:     "quay.io/test/cache:v2.0",
					ReclaimPolicy: v1beta1.Retain,
				},
			},
			expectError: false,
		},
		{
			name:  "mixed quadlet files - only volumes with images extracted",
			appID: "mixed-app",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/test/app:latest`),
				"data.volume": []byte(`[Volume]
Image=quay.io/test/data:latest`),
				"empty.volume": []byte(`[Volume]
Device=/dev/sda1`),
				"net.network": []byte(`[Network]
`),
			},
			expectedVolumes: []Volume{
				{
					Name:          "data.volume",
					ID:            "systemd-mixed-app-data",
					Reference:     "quay.io/test/data:latest",
					ReclaimPolicy: v1beta1.Retain,
				},
			},
			expectError: false,
		},
		{
			name:  "volume with custom name",
			appID: "app",
			files: map[string][]byte{
				"storage.volume": []byte(`[Volume]
Image=quay.io/test/storage:latest
VolumeName=custom-storage-name`),
			},
			expectedVolumes: []Volume{
				{
					Name:          "storage.volume",
					ID:            "custom-storage-name",
					Reference:     "quay.io/test/storage:latest",
					ReclaimPolicy: v1beta1.Retain,
				},
			},
			expectError: false,
		},
		{
			name:  "no volumes - only containers",
			appID: "app",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/test/app:latest`),
			},
			expectedVolumes: []Volume{},
			expectError:     false,
		},
		{
			name:            "empty directory",
			appID:           "app",
			files:           map[string][]byte{},
			expectedVolumes: nil,
			expectError:     true,
		},
		{
			name:  "invalid quadlet file causes parse error",
			appID: "app",
			files: map[string][]byte{
				"invalid.volume": []byte(`[Volume]
[Container]
Image=test`),
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			quadletDir := filepath.Join("/test", "quadlets")
			err := rw.MkdirAll(quadletDir, fileio.DefaultDirectoryPermissions)
			require.NoError(err)

			for filename, content := range tt.files {
				err := rw.WriteFile(filepath.Join(quadletDir, filename), content, fileio.DefaultFilePermissions)
				require.NoError(err)
			}

			volumes, err := extractQuadletVolumesFromDir(tt.appID, rw, quadletDir)

			if tt.expectError {
				require.Error(err)
				return
			}

			require.NoError(err)
			require.Equal(len(tt.expectedVolumes), len(volumes))

			sort.Slice(volumes, func(i, j int) bool {
				return volumes[i].Name < volumes[j].Name
			})

			sort.Slice(tt.expectedVolumes, func(i, j int) bool {
				return tt.expectedVolumes[i].Name < tt.expectedVolumes[j].Name
			})

			for i, expectedVol := range tt.expectedVolumes {
				require.Equal(expectedVol.Name, volumes[i].Name)
				require.Equal(expectedVol.ID, volumes[i].ID)
				require.Equal(expectedVol.Reference, volumes[i].Reference)
				require.Equal(expectedVol.Available, volumes[i].Available)
				require.Equal(expectedVol.ReclaimPolicy, volumes[i].ReclaimPolicy, "volume %q reclaim policy mismatch", expectedVol.Name)
			}
		})
	}
}
