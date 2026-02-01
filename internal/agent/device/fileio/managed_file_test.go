package fileio

import (
	"strconv"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestExists(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		f             v1beta1.FileSpec
		pathExists    bool
		expectedError error
	}{
		{
			name: "file exists",
			f: v1beta1.FileSpec{
				Path: "exists",
			},
			pathExists: true,
		},
		{
			name: "file doesn't exist",
			f: v1beta1.FileSpec{
				Path: "doesn't_exists",
			},
			pathExists: false,
		},
		{
			name: "path is dir",
			f: v1beta1.FileSpec{
				Path: "/",
			},
			pathExists:    false,
			expectedError: errors.ErrPathIsDir,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			writer := NewWriter(WithWriterRootDir(tmpDir))
			if tt.pathExists {
				err := writer.WriteFile(tt.f.Path, []byte("contents"), 0644)
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
	testUid, testGid, err := getUserIdentity()
	require.NoError(err)
	tests := []struct {
		name string
		// current is the current managed file instance
		current *v1beta1.FileSpec
		// desired is the desired managed file
		desired      *v1beta1.FileSpec
		wantUpToDate bool
	}{
		{
			name:         "file is up to date",
			current:      createTestFile("up_to_date", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", int(DefaultFilePermissions), testUid, testGid),
			desired:      createTestFile("up_to_date", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", int(DefaultFilePermissions), testUid, testGid),
			wantUpToDate: true,
		},
		{
			name:    "file content is not up to date",
			current: createTestFile("not_up_to_date", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", int(DefaultFilePermissions), testUid, testGid),
			desired: createTestFile("not_up_to_date", "data:,This%20system%20is%20managed%20by%20flightctl%20v2.%0A", int(DefaultFilePermissions), testUid, testGid),
		},
		{
			name:    "file does not exist",
			current: nil,
			desired: createTestFile("does_not_exist", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", int(DefaultFilePermissions), testUid, testGid),
		},
		{
			name:    "file with different permissions",
			current: createTestFile("diff_perms", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", 0o644, testUid, testGid),
			desired: createTestFile("diff_perms", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", 0o755, testUid, testGid),
		},
		{
			name:    "file with different user",
			current: createTestFile("diff_user", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", int(DefaultFilePermissions), testUid, testGid),
			desired: createTestFile("diff_user", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", int(DefaultFilePermissions), testUid+1, testGid),
		},
		{
			name:    "file with different group",
			current: createTestFile("diff_group", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", int(DefaultFilePermissions), testUid, testGid),
			desired: createTestFile("diff_group", "data:,This%20system%20is%20managed%20by%20flightctl.%0A", int(DefaultFilePermissions), testUid, testGid+1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			t.Log(tmpDir)
			writer := NewWriter(WithWriterRootDir(tmpDir))
			if tt.current != nil {
				// write the current file to disk if it exists
				managed, err := writer.CreateManagedFile(*tt.current)
				require.NoError(err)
				err = managed.Write()
				require.NoError(err)
			}
			// compare with desired file
			managed, err := writer.CreateManagedFile(*tt.desired)
			require.NoError(err)

			// check if the file is up to date
			upToDate, err := managed.IsUpToDate()
			require.NoError(err)
			require.Equal(tt.wantUpToDate, upToDate)
		})
	}
}

func createTestFile(path, data string, mode, user, group int) *v1beta1.FileSpec {
	return &v1beta1.FileSpec{
		Path:    path,
		User:    v1beta1.Username(strconv.Itoa(user)),
		Group:   strconv.Itoa(group),
		Content: data,
		Mode:    lo.ToPtr(mode),
	}
}
