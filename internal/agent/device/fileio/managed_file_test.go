package fileio

import (
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/stretchr/testify/require"
)

func TestExists(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		f             ign3types.File
		pathExists    bool
		expectedError error
	}{
		{
			name: "file exists",
			f: ign3types.File{
				Node: ign3types.Node{
					Path: "exists",
				},
			},
			pathExists: true,
		},
		{
			name: "file doesn't exist",
			f: ign3types.File{
				Node: ign3types.Node{
					Path: "doesn't_exist",
				},
			},
			pathExists: false,
		},
		{
			name: "path is dir",
			f: ign3types.File{
				Node: ign3types.Node{
					Path: "/",
				},
			},
			pathExists:    false,
			expectedError: ErrPathIsDir,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writer := NewWriter()
			writer.SetRootdir(tmpDir)
			if tt.pathExists {
				err := writer.WriteFile(tt.f.Node.Path, []byte("contents"), 0644)
				require.NoError(err)
			}

			managed, err := newManagedFile(tt.f, writer)
			if tt.expectedError != nil {
				require.Error(err)
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
			exists, err := managed.Exists()
			require.NoError(err)
			require.Equal(tt.pathExists, exists)

		})
	}
}

func TestIsUpToDate(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name string
		// current is the current managed file instance
		current ign3types.File
		// desired is the desired managed file
		desired      ign3types.File
		wantUpToDate bool
	}{
		{
			name:         "file is up to date",
			current:      createTestFile("up_to_date", "data:,This%20system%20is%20managed%20by%20flightctl.%0A"),
			desired:      createTestFile("up_to_date", "data:,This%20system%20is%20managed%20by%20flightctl.%0A"),
			wantUpToDate: true,
		},
		{
			name:    "file is not up to date",
			current: createTestFile("not_up_to_date", "data:,This%20system%20is%20managed%20by%20flightctl.%0A"),
			desired: createTestFile("not_up_to_date", "data:,This%20system%20is%20managed%20by%20flightctl%20v2.%0A"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writer := NewWriter()
			writer.SetRootdir(tmpDir)

			// write the current file to disk
			managed, err := writer.CreateManagedFile(tt.current)
			require.NoError(err)
			err = managed.Write()
			require.NoError(err)

			// compare with desired file
			managed, err = writer.CreateManagedFile(tt.desired)
			require.NoError(err)

			// check if the file is up to date
			upToDate, err := managed.IsUpToDate()
			require.NoError(err)
			require.Equal(tt.wantUpToDate, upToDate)
		})
	}
}

func createTestFile(path, data string) ign3types.File {
	return ign3types.File{
		Node: ign3types.Node{
			Path: path,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Contents: ign3types.Resource{
				Source: util.StrToPtr(data),
			},
		},
	}
}
