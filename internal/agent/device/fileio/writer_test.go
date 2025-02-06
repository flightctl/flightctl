package fileio

import (
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
