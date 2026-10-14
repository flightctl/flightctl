package deltastatus

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
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

func TestReconcileApplicationTargetsPreservesReindexedRuntimeResults(t *testing.T) {
	tests := []struct {
		name     string
		runtime  string
		complete bool
	}{
		{name: "When a Podman identity gains its runtime it should preserve only the Podman outcome", runtime: "PodmanImage", complete: true},
		{name: "When a CRI identity gains its runtime it should preserve only the CRI outcome", runtime: "CRIImage", complete: true},
		{name: "When collection is incomplete it should retire the previous index without resurrecting a cleared outcome", runtime: "CRIImage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			store := New(rw, "/var/lib/flightctl", logger)
			const image = "quay.io/acme/app:target"
			legacyID := Fingerprint("", image)
			targetKey := Fingerprint("", image, "", tt.runtime)
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{"app": "spec"}))
			require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"app": {legacyID: targetKey}}, true))
			applied := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
			require.NoError(store.RecordApplicationResult("app", "spec", legacyID, targetKey, applied))
			targets := make(map[string]string)
			for _, runtime := range []string{"PodmanImage", "CRIImage"} {
				targets[Fingerprint("", image, runtime)] = Fingerprint("", image, "", runtime)
			}
			require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"app": targets}, tt.complete))
			restarted := New(rw, "/var/lib/flightctl", logger)
			snapshot := restarted.ApplicationSnapshot("app")
			require.Equal(targets, snapshot.Targets)
			runtimeID := Fingerprint("", image, tt.runtime)
			require.Len(snapshot.Results, 1)
			require.Equal(applied, snapshot.Results[runtimeID].Status)
			require.NoError(restarted.ClearApplicationResult("app", "spec", runtimeID, targetKey))
			require.NoError(restarted.ReconcileApplicationTargets(map[string]map[string]string{"app": targets}, tt.complete))
			require.Empty(New(rw, "/var/lib/flightctl", logger).ApplicationResults("app"))
		})
	}
}

func TestWhenOSStagesAnotherImageItShouldKeepDigestAssociatedHistoryAcrossRestart(t *testing.T) {
	require := require.New(t)
	// Use one rooted reader and writer so a new Store sees the persisted history.
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	applied := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	fallback := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback}
	require.NoError(store.SetOSTarget("attempt-a"))
	require.NoError(store.RecordOSImageResult("attempt-a", "os:a", "sha256:a", applied))
	require.NoError(store.SetOSTarget("attempt-b"))
	require.NoError(store.RecordOSImageResult("attempt-b", "os:b", "sha256:b", fallback))
	require.Equal(applied, *store.ObservedOSResult("os:a", "sha256:a"))
	require.Nil(store.ObservedOSResult("os:b", "sha256:unknown"))
	require.Nil(store.ObservedOSResult("os:b", ""))
	restarted := New(rw, "/state", logger)
	require.Equal(applied, *restarted.ObservedOSResult("os:a", "sha256:a"))
	require.Equal(fallback, *restarted.ObservedOSResult("os:b", "sha256:b"))
	require.NoError(restarted.RecordOSImageResult("attempt-a", "os:a", "sha256:a", fallback))
	require.Equal(applied, *restarted.ObservedOSResult("os:a", "sha256:a"))
}

func TestWhenOSIsAlreadyPresentItShouldPreserveTheRecordedOutcome(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := New(rw, "/state", log.NewPrefixLogger("test"))
	applied := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	require.NoError(t, store.SetOSTarget("attempt"))
	require.NoError(t, store.RecordOSImageResult("attempt", "os:a", "sha256:a", applied))
	require.NoError(t, store.RecordOSImageResult("attempt", "os:a", "sha256:a", v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired}))
	require.Equal(t, applied, *store.ObservedOSResult("os:a", "sha256:a"))
}

func TestWhenOSPersistenceFailsItShouldRetryWithoutClaimingDurability(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	rw := fileio.NewMockReadWriter(ctrl)
	var durable []byte
	failWrite := true
	writeErr := errors.New("disk full")
	rw.EXPECT().ReadFile(gomock.Any()).DoAndReturn(func(string) ([]byte, error) {
		if durable == nil {
			return nil, fs.ErrNotExist
		}
		return durable, nil
	}).AnyTimes()
	rw.EXPECT().MkdirAll(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	rw.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ string, data []byte, _ fs.FileMode, _ ...fileio.FileOption) error {
		if failWrite {
			return writeErr
		}
		durable = append([]byte(nil), data...)
		return nil
	}).AnyTimes()
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	require.ErrorIs(store.SetOSTarget("attempt"), writeErr)
	failWrite = false
	require.NoError(store.SetOSTarget("attempt"))
	restarted := New(rw, "/state", logger)
	failWrite = true
	applied := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	require.ErrorIs(restarted.RecordOSImageResult("attempt", "os:a", "sha256:a", applied), writeErr)
	require.Nil(New(rw, "/state", logger).ObservedOSResult("os:a", "sha256:a"))
	failWrite = false
	require.NoError(restarted.RecordOSImageResult("attempt", "os:a", "sha256:a", applied))
	require.Equal(applied, *New(rw, "/state", logger).ObservedOSResult("os:a", "sha256:a"))
}

func TestWhenOSStateIsLegacyOrCorruptItShouldWithholdUnverifiedOutcomes(t *testing.T) {
	cases := []struct {
		name, data  string
		appRetained bool
	}{
		{name: "When legacy state has no digest association it should retain unrelated application records", data: `{"version":1,"os":{"targetKey":"attempt","result":{"outcome":"Applied"}},"applications":{"web":{"specKey":"spec","targets":{"image":"target"},"results":{"image":{"targetKey":"target","status":{"outcome":"Fallback"}}}}}}`, appRetained: true},
		{name: "When state is corrupt it should start without an outcome", data: `{incomplete`},
		{name: "When state has an unsupported version it should start without an outcome", data: `{"version":999,"os":{"targetKey":"attempt","result":{"outcome":"Applied"}}}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			require.NoError(rw.MkdirAll("/state", 0o700))
			require.NoError(rw.WriteFile("/state/"+FileName, []byte(tt.data), 0o600))
			logger := log.NewPrefixLogger("test")
			store := New(rw, "/state", logger)
			require.Nil(store.ObservedOSResult("os:a", "sha256:a"))
			require.Nil(store.OSResult("attempt"))
			require.NoError(store.SetOSTarget("next-attempt"))
			applied := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
			require.NoError(store.RecordOSImageResult("next-attempt", "os:a", "sha256:a", applied))
			restarted := New(rw, "/state", logger)
			require.Equal(applied, *restarted.ObservedOSResult("os:a", "sha256:a"))
			if tt.appRetained {
				require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, restarted.ApplicationResults("web")["image"].Status.Outcome)
			}
		})
	}
}

func TestWhenObservedApplicationHistorySurvivesUpdateAndRestartItShouldAggregateMatchingImages(t *testing.T) {
	testCases := []struct {
		name       string
		images     []ApplicationImage
		complete   bool
		want       v1beta1.DeviceDeltaApplyOutcomeType
		wantReason string
	}{
		{
			name:       "When every observed digest has history it should aggregate unique results",
			images:     []ApplicationImage{{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image", Digest: "sha256:old"}, {Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image", Digest: "sha256:old"}, {Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image", Digest: "sha256:new"}},
			complete:   true,
			want:       v1beta1.DeviceDeltaApplyOutcomePartial,
			wantReason: "full pull",
		},
		{
			name:     "When an observed digest has no history it should withhold the aggregate",
			images:   []ApplicationImage{{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image", Digest: "sha256:old"}, {Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image"}},
			complete: true,
		},
		{
			name:     "When the observed collection is incomplete it should withhold the aggregate",
			images:   []ApplicationImage{{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image", Digest: "sha256:old"}},
			complete: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			rw, logger, _, _, _ := newUpdatedApplicationHistory(t)
			observed := New(rw, "/var/lib/flightctl", logger).ObservedApplicationResult("web", tc.images, tc.complete)
			if tc.want == "" {
				require.Nil(observed)
				return
			}
			require.NotNil(observed)
			require.Equal(tc.want, observed.Outcome)
			require.NotNil(observed.FallbackReason)
			require.Equal(tc.wantReason, *observed.FallbackReason)
		})
	}
}

func TestWhenAStaleApplicationResultIsRecordedItShouldKeepTheCurrentOutcome(t *testing.T) {
	require := require.New(t)
	rw, logger, store, _, newer := newUpdatedApplicationHistory(t)
	targetID := Fingerprint(newer.Owner, newer.Image, newer.OCIType)
	require.NoError(store.RecordApplicationImageResult("web", "old-spec", targetID, "old-target", newer,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))

	current := New(rw, "/var/lib/flightctl", logger).ObservedApplicationResult("web", []ApplicationImage{newer}, true)
	require.NotNil(current)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, current.Outcome)
}

func TestWhenARecordedImageIdentityReceivesAnotherOutcomeItShouldKeepItsOriginalOutcome(t *testing.T) {
	require := require.New(t)
	_, _, store, _, newer := newUpdatedApplicationHistory(t)
	targetID := Fingerprint(newer.Owner, newer.Image, newer.OCIType)
	require.NoError(store.RecordApplicationImageResult("web", "new-spec", targetID, "new-target", newer,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))

	current := store.ObservedApplicationResult("web", []ApplicationImage{newer}, true)
	require.NotNil(current)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, current.Outcome)
}

func TestWhenAnAggregatedFallbackReasonIsMutatedItShouldNotChangeStoredHistory(t *testing.T) {
	require := require.New(t)
	rw, logger, _, _, newer := newUpdatedApplicationHistory(t)
	restarted := New(rw, "/var/lib/flightctl", logger)
	observed := restarted.ObservedApplicationResult("web", []ApplicationImage{newer}, true)
	require.NotNil(observed)
	require.NotNil(observed.FallbackReason)
	*observed.FallbackReason = "mutated"
	require.Equal("full pull", *restarted.ObservedApplicationResult("web", []ApplicationImage{newer}, true).FallbackReason)
}

func TestWhenManyScopedImagesAreObservedItShouldIgnoreReplicaCount(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/var/lib/flightctl", logger)
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"app": "spec"}))
	targets := make(map[string]string)
	var images []ApplicationImage
	for i := 0; i < 12; i++ {
		image := ApplicationImage{Image: fmt.Sprintf("quay.io/acme/workload-%d:latest", i), Owner: "root", OCIType: "PodmanImage", Digest: fmt.Sprintf("sha256:observed-%d", i)}
		images = append(images, image)
		targets[Fingerprint(image.Owner, image.Image, image.OCIType)] = fmt.Sprintf("target-%d", i)
	}
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"app": targets}, true))
	for i, image := range images {
		outcome := v1beta1.DeviceDeltaApplyOutcomeApplied
		if i == 0 {
			outcome = v1beta1.DeviceDeltaApplyOutcomeNotRequired
		}
		targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
		require.NoError(store.RecordApplicationImageResult("app", "spec", targetID, targets[targetID], image, v1beta1.DeviceDeltaApplyStatus{Outcome: outcome}))
	}
	// Replica count does not consume another result or change aggregation.
	images = append(images, images[1], images[1])
	observed := New(rw, "/var/lib/flightctl", logger).ObservedApplicationResult("app", images, true)
	require.NotNil(observed)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, observed.Outcome)
}

func TestWhenAnObservedImageIdentityDiffersItShouldWithholdTheOutcome(t *testing.T) {
	const (
		appName = "app"
		image   = "quay.io/acme/web:latest"
		digest  = "sha256:observed"
	)
	expected := ApplicationImage{Image: image, Owner: "root", OCIType: "PodmanImage", Digest: digest}
	testCases := []struct {
		name  string
		image ApplicationImage
	}{
		{name: "When the owner differs it should withhold the result", image: ApplicationImage{Image: image, Owner: "alice", OCIType: expected.OCIType, Digest: digest}},
		{name: "When the runtime differs it should withhold the result", image: ApplicationImage{Image: image, Owner: expected.Owner, OCIType: "CRIImage", Digest: digest}},
		{name: "When the digest differs it should withhold the result", image: ApplicationImage{Image: image, Owner: expected.Owner, OCIType: expected.OCIType, Digest: "sha256:unverified-platform"}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			store := New(rw, "/var/lib/flightctl", logger)
			targetID := Fingerprint(expected.Owner, expected.Image, expected.OCIType)
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{appName: "spec"}))
			require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{appName: {targetID: "target"}}, true))
			require.NoError(store.RecordApplicationImageResult(appName, "spec", targetID, "target", expected,
				v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
			require.Nil(store.ObservedApplicationResult(appName, []ApplicationImage{tc.image}, true))
		})
	}
}

func newUpdatedApplicationHistory(t *testing.T) (fileio.ReadWriter, *log.PrefixLogger, *Store, ApplicationImage, ApplicationImage) {
	t.Helper()
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/var/lib/flightctl", logger)
	image := ApplicationImage{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image", Digest: "sha256:old"}
	targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "old-spec"}))
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {targetID: "old-target"}}, true))
	require.NoError(store.RecordApplicationImageResult("web", "old-spec", targetID, "old-target", image, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "new-spec"}))
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {targetID: "new-target"}}, true))
	newer := image
	newer.Digest = "sha256:new"
	reason := "full pull"
	require.NoError(store.RecordApplicationImageResult("web", "new-spec", targetID, "new-target", newer, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: &reason}))
	return rw, logger, store, image, newer
}
