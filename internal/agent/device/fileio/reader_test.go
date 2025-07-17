package fileio

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/stretchr/testify/require"
)

func TestCheckPathExistsAndReadable(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	readWriter := NewReadWriter(WithTestRootDir(tmpDir))
	filePath := "testfile"

	err := readWriter.WriteFile(filePath, []byte("test data"), 0644)
	require.NoError(err)

	// ensure readable
	exists, err := readWriter.PathExists(filePath)
	require.NoError(err)
	require.True(exists)

	// change permissions to 0200 (write-only for the owner)
	err = os.Chmod(filepath.Join(tmpDir, filePath), 0200)
	require.NoError(err)

	// exists but not readable
	exists, err = readWriter.PathExists(filePath)
	require.ErrorIs(err, errors.ErrReadingPath)
	require.False(exists)

	subDir := "sub"
	err = readWriter.MkdirAll(subDir, 0200)
	require.NoError(err)

	exists, err = readWriter.PathExists(subDir)
	require.ErrorIs(err, errors.ErrReadingPath)
	require.False(exists)

	// empty dir
	subDir2 := "sub2"
	err = readWriter.MkdirAll(subDir2, 0700)
	require.NoError(err)
	exists, err = readWriter.PathExists(subDir2)
	require.NoError(err)
	require.True(exists)

	// non-existent path
	exists, err = readWriter.PathExists("nonexistent")
	require.NoError(err)
	require.False(exists)
}

func TestPathExistsWithSkipContentCheck(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	readWriter := NewReadWriter(WithTestRootDir(tmpDir))
	emptyFilePath := "emptyfile"

	// create empty file
	err := readWriter.WriteFile(emptyFilePath, []byte{}, 0644)
	require.NoError(err)

	// without skip content check - should fail on empty file
	exists, err := readWriter.PathExists(emptyFilePath)
	require.Error(err)
	require.False(exists)

	// with skip content check - should pass for empty file
	exists, err = readWriter.PathExists(emptyFilePath, WithSkipContentCheck())
	require.NoError(err)
	require.True(exists)

	// test with empty directory
	emptyDirPath := "emptydir"
	err = readWriter.MkdirAll(emptyDirPath, 0755)
	require.NoError(err)

	// without skip content check - should pass for empty directory
	exists, err = readWriter.PathExists(emptyDirPath)
	require.NoError(err)
	require.True(exists)

	// with skip content check - should also pass for empty directory
	exists, err = readWriter.PathExists(emptyDirPath, WithSkipContentCheck())
	require.NoError(err)
	require.True(exists)
}
