package firmware

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
)

func TestPhaseRecordRetiredMarkerBound(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	dir, err := rw.OpenAnchor("/", fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dir.Close()) })

	attemptID := strings.Repeat(`"`, 256)
	store := phaseRecordStore{dir: dir, attemptID: attemptID}
	require.NoError(t, store.save(phaseRecord{AttemptID: attemptID}))
	require.NoError(t, store.retire())

	name, err := store.retiredFileName()
	require.NoError(t, err)
	marker, err := os.ReadFile(filepath.Join(root, name))
	require.NoError(t, err)
	require.Greater(t, len(marker), 512)

	retired, err := store.isRetired()
	require.NoError(t, err)
	require.True(t, retired)
}
