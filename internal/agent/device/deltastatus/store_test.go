package deltastatus

import (
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestStorePersistsDeltaResultsAndClearsChangedTargets(t *testing.T) {
	rootDir := t.TempDir()
	dataDir := "/var/lib/flightctl"
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(rootDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(rootDir)),
	)
	logger := log.NewPrefixLogger("test")

	store := New(readWriter, dataDir, logger)
	osTarget := Fingerprint("quay.io/acme/os:v2", "quay.io/acme/os-delta:v1")
	require.NoError(t, store.SetOSTarget(osTarget))
	require.NoError(t, store.RecordOSResult(osTarget, v1beta1.DeviceDeltaApplyStatus{
		Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
	}))

	appSpecKey := Fingerprint("rendered web app v2")
	targetID := "root\x00quay.io/acme/web:v2"
	targetKey := Fingerprint("quay.io/acme/web:v2", "sha256:web-v2")
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{"web": appSpecKey}))
	appTargets := map[string]map[string]string{"web": {targetID: targetKey}}
	require.NoError(t, store.ReconcileApplicationTargets(appTargets, true))
	require.NoError(t, store.RecordApplicationResult(
		"web", appSpecKey, targetID, targetKey,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
	))

	// Constructing a new store simulates an agent process restart.
	restarted := New(readWriter, dataDir, logger)
	require.NoError(t, restarted.SetOSTarget(osTarget))
	osResult := restarted.OSResult(osTarget)
	require.NotNil(t, osResult)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, osResult.Outcome)

	require.NoError(t, restarted.ReconcileApplicationSpecs(map[string]string{"web": appSpecKey}))
	require.NoError(t, restarted.ReconcileApplicationTargets(appTargets, true))
	appResults := restarted.ApplicationResults("web")
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, appResults[targetID].Status.Outcome)

	changedOS := Fingerprint("quay.io/acme/os:v3", "quay.io/acme/os-delta:v2")
	require.NoError(t, restarted.SetOSTarget(changedOS))
	require.Nil(t, restarted.OSResult(changedOS))

	changedTargetKey := Fingerprint("quay.io/acme/web:v3", "sha256:web-v3")
	changedAppTargets := map[string]map[string]string{"web": {targetID: changedTargetKey}}
	require.NoError(t, restarted.ReconcileApplicationTargets(changedAppTargets, true))
	require.Empty(t, restarted.ApplicationResults("web"))
	// Results arriving late for the superseded target cannot repopulate state.
	require.NoError(t, restarted.RecordApplicationResult(
		"web", appSpecKey, targetID, targetKey,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
	))
	require.Empty(t, restarted.ApplicationResults("web"))

	require.NoError(t, restarted.ReconcileApplicationSpecs(nil))
	data, err := readWriter.ReadFile(filepath.Join(dataDir, FileName))
	require.NoError(t, err)
	require.NotEmpty(t, data)
}

func TestClearApplicationResultIgnoresSupersededTargets(t *testing.T) {
	cases := []struct {
		name, specKey, targetKey string
		wantCleared              bool
	}{
		{name: "When the target matches it should durably clear only its result", specKey: "spec", targetKey: "target", wantCleared: true},
		{name: "When the spec is stale it should preserve the current result", specKey: "old-spec", targetKey: "target"},
		{name: "When the target is stale it should preserve the current result", specKey: "spec", targetKey: "old-target"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			store := New(rw, "/var/lib/flightctl", logger)
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{"app": "spec"}))
			require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"app": {"image": "target", "other-image": "other-target"}}, true))
			result := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
			require.NoError(store.RecordApplicationResult("app", "spec", "image", "target", result))
			require.NoError(store.RecordApplicationResult("app", "spec", "other-image", "other-target", result))
			require.NoError(store.ClearApplicationResult("app", tt.specKey, "image", tt.targetKey))
			restored := New(rw, "/var/lib/flightctl", logger).ApplicationSnapshot("app")
			_, exists := restored.Results["image"]
			require.Equal(!tt.wantCleared, exists)
			require.Equal("target", restored.Targets["image"])
			require.Equal(result, restored.Results["other-image"].Status)
		})
	}
}
