package fileio

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/stretchr/testify/require"
)

func TestExists(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name                  string
		f                     ign3types.File
		pathExists            bool
		expectedError         bool
		errorMessageSubstring string
	}{
		{
			name: "file exists",
			f: ign3types.File{
				Node: ign3types.Node{
					Path: "exists",
				},
			},
			pathExists:    true,
			expectedError: false,
		},
		{
			name: "file doesn't exist",
			f: ign3types.File{
				Node: ign3types.Node{
					Path: "doesn't_exist",
				},
			},
			pathExists:    false,
			expectedError: false,
		},
		{
			name: "path is dir",
			f: ign3types.File{
				Node: ign3types.Node{
					Path: "/",
				},
			},
			pathExists:            false,
			expectedError:         true,
			errorMessageSubstring: "is a directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.f.Path = filepath.Join(tmpDir, tt.f.Path)
			if tt.pathExists {
				f, err := os.Create(tt.f.Path)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				defer f.Close()
			}
			mFile := newManagedFile(tt.f, tmpDir)
			exists, err := mFile.Exists()
			fmt.Println(err)
			require.Equal(tt.expectedError, err != nil)
			if tt.expectedError {
				require.Contains(err.Error(), tt.errorMessageSubstring)
			}
			require.Equal(tt.pathExists, exists)

		})
	}
}
