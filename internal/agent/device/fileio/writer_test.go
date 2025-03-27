package fileio

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopyFile(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	currentBytes := []byte("current")
	desiredBytes := []byte("desired")
	rw := NewReadWriter()
	rw.SetRootdir(tmpDir)
	err := rw.WriteFile("current", currentBytes, 0644)
	require.NoError(err)
	err = rw.WriteFile("desired", desiredBytes, 0644)
	require.NoError(err)

	err = rw.CopyFile("current", "desired")
	require.NoError(err)

	current, err := rw.ReadFile("current")
	require.NoError(err)
	require.Equal(currentBytes, current)

	desired, err := rw.ReadFile("desired")
	require.NoError(err)
	require.Equal(currentBytes, desired)
}

func TestMkdirTemp(t *testing.T) {
	require := require.New(t)

	testFileName := "testFile"
	testFileBytes := []byte("test")

	t.Run("create temp dir and write/read file", func(t *testing.T) {
		tmpDir := t.TempDir()
		rw := NewReadWriter()
		rw.SetRootdir(tmpDir)
		dir, err := rw.MkdirTemp("test")
		require.NoError(err)
		require.NotEmpty(dir)

		err = rw.WriteFile(filepath.Join(dir, testFileName), testFileBytes, DefaultFilePermissions)
		require.NoError(err)

		fileBytes, err := rw.ReadFile(filepath.Join(dir, testFileName))
		require.NoError(err)
		require.Equal(testFileBytes, fileBytes)

		err = rw.RemoveAll(dir)
		require.NoError(err)

		exists, err := rw.PathExists(dir)
		require.NoError(err)
		require.False(exists)
	})

	t.Run("no rootdir create temp dir and write/read file", func(t *testing.T) {
		rw := NewReadWriter()

		dir, err := rw.MkdirTemp("test")
		require.NoError(err)
		require.NotEmpty(dir)
		defer func() {
			_ = rw.RemoveAll(dir)
		}()

		uid, gid, err := getUserIdentity()
		require.NoError(err)

		err = rw.WriteFile(filepath.Join(dir, testFileName), testFileBytes, DefaultFilePermissions, WithUid(uid), WithGid(gid))
		require.NoError(err)

		fileBytes, err := rw.ReadFile(filepath.Join(dir, testFileName))
		require.NoError(err)
		require.Equal(testFileBytes, fileBytes)
	})
}
