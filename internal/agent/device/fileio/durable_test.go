package fileio

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDurablePolicyFile(t *testing.T) {
	root := t.TempDir()
	rw := NewReadWriter(NewReader(WithReaderRootDir(root)), NewWriter(WithWriterRootDir(root)))
	name := "state/firmware/policy.json"
	require.NoError(t, rw.WriteFileDurable(name, []byte("first"), 0700, 0600))
	require.NoError(t, rw.WriteFileDurable(name, []byte("second"), 0700, 0600))
	bytes, err := rw.ReadFileLimited(name, 6)
	require.NoError(t, err)
	require.Equal(t, "second", string(bytes))
	_, err = rw.ReadFileLimited(name, 5)
	require.Error(t, err)
	_, err = rw.ReadFileLimited(name, 0)
	require.Error(t, err)
	_, err = rw.ReadFileLimited("missing", 10)
	require.ErrorIs(t, err, os.ErrNotExist)
	for _, entry := range []struct {
		path string
		mode os.FileMode
	}{{name, 0600}, {"state/firmware", 0700}, {"state", 0700}} {
		info, err := os.Stat(filepath.Join(root, entry.path))
		require.NoError(t, err)
		require.Equal(t, entry.mode, info.Mode().Perm())
	}
	entries, err := rw.ReadDir("state/firmware")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, rw.WriteFile("blocked", []byte("original"), 0600))
	require.Error(t, rw.WriteFileDurable("blocked/policy.json", []byte("replacement"), 0700, 0600))
	bytes, err = rw.ReadFile("blocked")
	require.NoError(t, err)
	require.Equal(t, "original", string(bytes))
}

func TestDurableRetrySyncsExistingAncestors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires unprivileged directory access checks")
	}
	root := t.TempDir()
	rw := NewReadWriter(NewReader(WithReaderRootDir(root)), NewWriter(WithWriterRootDir(root)))
	name := "state/firmware/policy.json"
	// Chown fails after directory creation, before any directory sync.
	require.Error(t, rw.WriteFileDurable(name, []byte("first"), 0700, 0600, WithUid(os.Getuid()+1)))
	parent := filepath.Join(root, "state")
	require.NoError(t, os.Chmod(parent, 0111))
	defer func() { _ = os.Chmod(parent, 0700) }()
	// Leaf access still works, but the unsynced ancestor cannot be opened.
	require.Error(t, rw.WriteFileDurable(name, []byte("retry"), 0700, 0600))
	require.NoError(t, os.Chmod(parent, 0700))
	require.NoError(t, rw.WriteFileDurable(name, []byte("retained"), 0700, 0600))
	data, err := rw.ReadFileLimited(name, 20)
	require.NoError(t, err)
	require.Equal(t, "retained", string(data))
}
