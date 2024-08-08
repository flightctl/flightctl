package config

import (
	"os"
	"testing"

	"github.com/coreos/ignition/v2/config/shared/errors"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestSync(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name                  string
		current               *v1alpha1.RenderedDeviceSpec
		desired               *v1alpha1.RenderedDeviceSpec
		desiredTrackFileCount int
		wantErr               error
	}{
		{
			name:    "no desired config",
			current: &v1alpha1.RenderedDeviceSpec{},
			desired: &v1alpha1.RenderedDeviceSpec{},
		},
		{
			name: "desired config is invalid",
			current: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr(`{"ignition":{"version":"3.4.0"}}`),
			},
			desired: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr("invalid"),
			},
			wantErr: errors.ErrInvalid,
		},
		{
			name: "desired config is valid current is nil",
			current: &v1alpha1.RenderedDeviceSpec{
				Config: nil,
			},
			desired: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr(`{"ignition":{"version":"3.4.0"}}`),
			},
		},
		{
			name: "validate removal of files",
			current: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr(ignitionConfigCurrent),
			},
			desired: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr(ignitionConfigDesired),
			},
			desiredTrackFileCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDir := t.TempDir()
			deviceWriter := fileio.NewWriter()
			deviceWriter.SetRootdir(testDir)
			controller := NewController(
				deviceWriter,
				log.NewPrefixLogger("test"),
			)

			// Write the current config to the disk
			if tt.current.Config != nil {
				currentConfigRaw := []byte(*tt.current.Config)
				currentIgnitionConfig, err := ParseAndConvertConfig(currentConfigRaw)
				require.NoError(err)
				err = controller.deviceWriter.WriteIgnitionFiles(currentIgnitionConfig.Storage.Files...)
				require.NoError(err)
			}

			err := controller.Sync(tt.current, tt.desired)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
			require.Equal(tt.desiredTrackFileCount, len(controller.trackedFiles))
			require.Equal(tt.desiredTrackFileCount, countFilesInDir(testDir+"/etc/example"))
		})
	}
}

func countFilesInDir(dir string) int {
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	fileCount := 0
	for _, file := range files {
		if !file.IsDir() {
			fileCount++
		}
	}

	return fileCount
}

var ignitionConfigCurrent = `{
  "ignition": {
    "version": "3.1.0"
  },
  "storage": {
    "files": [
      {
        "path": "/etc/example/file1.txt",
        "contents": {
          "source": "data:,File%201%20contents"
        },
        "mode": 420
      },
      {
        "path": "/etc/example/file2.txt",
        "contents": {
          "source": "data:,File%202%20contents"
        },
        "mode": 420
      },
      {
        "path": "/etc/example/file3.txt",
        "contents": {
          "source": "data:,File%203%20contents"
        },
        "mode": 420
      }
    ]
  }
}`

var ignitionConfigDesired = `{
  "ignition": {
    "version": "3.1.0"
  },
  "storage": {
    "files": [
      {
        "path": "/etc/example/file1.txt",
        "contents": {
          "source": "data:,File%201%20contents"
        },
        "mode": 420
      },
      {
        "path": "/etc/example/file2.txt",
        "contents": {
          "source": "data:,File%202%20contents"
        },
        "mode": 420
      }
    ]
  }
}`
