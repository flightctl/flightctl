package firmware

import (
	"encoding/json"
	"errors"
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
	marker, err := dir.ReadFile(name, 4096)
	require.NoError(t, err)
	require.Greater(t, len(marker), 512)

	retired, err := store.isRetired()
	require.NoError(t, err)
	require.True(t, retired)
}

type phaseRetirementSyncFault struct {
	fileio.Anchor
	err error
}

func (a phaseRetirementSyncFault) Sync() error { return a.err }

func TestPhaseRetirementRetryKeepsRecordUntilMarkerIsSynced(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	dir, err := rw.OpenAnchor("/", fileio.DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dir.Close()) })

	store := phaseRecordStore{dir: dir, attemptID: "retiring"}
	require.NoError(t, store.save(phaseRecord{AttemptID: store.attemptID}))
	marker, err := store.retiredFileName()
	require.NoError(t, err)
	raw, err := json.Marshal(phaseRetiredMarker{Version: phaseRecordVersion, AttemptID: store.attemptID})
	require.NoError(t, err)
	require.NoError(t, dir.WriteFileExclusive(marker, raw, 0o600))

	fault := errors.New("directory sync failed")
	retry := store
	retry.dir = phaseRetirementSyncFault{Anchor: dir, err: fault}
	require.ErrorIs(t, retry.retire(), fault)
	name, err := store.fileName()
	require.NoError(t, err)
	_, err = dir.Lstat(name)
	require.NoError(t, err)
	require.NoError(t, store.retire())
}
