package deltastatus

import (
	"encoding/json"
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
			unresolvedID := Fingerprint("", image)
			targetKey := Fingerprint("", image, "", tt.runtime)
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{"app": "spec"}))
			require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"app": {unresolvedID: targetKey}}, true))
			applied := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
			require.NoError(store.RecordApplicationResult("app", "spec", unresolvedID, targetKey, applied))
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
	require.NoError(t, store.RecordOSImageResult("attempt", "os:a", "sha256:a", v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback}))
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

func TestWhenApplicationLifetimeWritesFailItShouldRetryOnTheNextNoOp(t *testing.T) {
	for _, testCase := range []struct {
		name string
		run  func(*testing.T, *Store, *fileio.MockReadWriter, *bool, *[]byte, error)
	}{
		{
			name: "When ensuring a lifetime after a failed write it should persist the existing in-memory lifetime",
			run: func(t *testing.T, store *Store, rw *fileio.MockReadWriter, failWrite *bool, _ *[]byte, writeErr error) {
				require := require.New(t)
				*failWrite = true
				_, err := store.EnsureApplicationLifetime("web")
				require.ErrorIs(err, writeErr)
				*failWrite = false
				lifetime, err := store.EnsureApplicationLifetime("web")
				require.NoError(err)
				require.NotEmpty(lifetime)
				restarted := New(rw, "/state", log.NewPrefixLogger("test"))
				require.Equal(lifetime, restarted.ApplicationLifetime("web"))
			},
		},
		{
			name: "When removing an application after a failed write it should persist the removal",
			run: func(t *testing.T, store *Store, rw *fileio.MockReadWriter, failWrite *bool, _ *[]byte, writeErr error) {
				require := require.New(t)
				require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
				oldLifetime := store.ApplicationLifetime("web")
				*failWrite = true
				require.ErrorIs(store.RemoveApplication("web"), writeErr)
				*failWrite = false
				require.NoError(store.RemoveApplication("web"))
				restarted := New(rw, "/state", log.NewPrefixLogger("test"))
				require.Empty(restarted.ApplicationLifetime("web"))
				newLifetime, err := restarted.EnsureApplicationLifetime("web")
				require.NoError(err)
				require.NotEqual(oldLifetime, newLifetime)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			failWrite := false
			var durable []byte
			writeErr := errors.New("disk full")
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			rw := newFailingReadWriter(t, ctrl, &failWrite, &durable, writeErr)
			testCase.run(t, New(rw, "/state", log.NewPrefixLogger("test")), rw, &failWrite, &durable, writeErr)
		})
	}
}

func newFailingReadWriter(t *testing.T, ctrl *gomock.Controller, failWrite *bool, durable *[]byte, writeErr error) *fileio.MockReadWriter {
	t.Helper()
	rw := fileio.NewMockReadWriter(ctrl)
	rw.EXPECT().ReadFile(gomock.Any()).DoAndReturn(func(string) ([]byte, error) {
		if *durable == nil {
			return nil, fs.ErrNotExist
		}
		return append([]byte(nil), (*durable)...), nil
	}).AnyTimes()
	rw.EXPECT().MkdirAll(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	rw.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ string, data []byte, _ fs.FileMode, _ ...fileio.FileOption) error {
		if *failWrite {
			return writeErr
		}
		*durable = append([]byte(nil), data...)
		return nil
	}).AnyTimes()
	return rw
}

func TestWhenStateIsCorruptOrUnsupportedItShouldStartWithoutOutcomes(t *testing.T) {
	cases := []struct {
		name, data string
	}{
		{name: "When state has version two it should reject the unsupported format", data: `{"version":2,"os":{"targetKey":"attempt","result":{"outcome":"Applied"}}}`},
		{name: "When state has version three it should reject the unsupported format", data: `{"version":3,"os":{"targetKey":"attempt","result":{"outcome":"Applied"}}}`},
		{name: "When state has version four it should reject the unsupported format", data: `{"version":4,"os":{"targetKey":"attempt","result":{"outcome":"Applied"}}}`},
		{name: "When an application has no lifetime it should discard unscoped evidence", data: `{"version":1,"applications":{"web":{"specKey":"spec","targets":{"image":"target"},"results":{"image":{"targetKey":"target","status":{"outcome":"Applied"}}}}}}`},
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
			require.Nil(restarted.ApplicationSnapshot("web"))
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
			images:   []ApplicationImage{{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image", Digest: "sha256:old"}, {Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image", Digest: "sha256:unrecorded"}},
			complete: true,
		},
		{
			name:     "When an observed digest is empty it should withhold the aggregate",
			images:   []ApplicationImage{{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image"}},
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
	rw, logger, _, _, newer := newUpdatedApplicationHistory(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	unwritable := fileio.NewMockReadWriter(ctrl)
	unwritable.EXPECT().ReadFile(gomock.Any()).DoAndReturn(rw.ReadFile)
	unwritable.EXPECT().MkdirAll(gomock.Any(), gomock.Any()).Return(errors.New("disk unavailable")).AnyTimes()
	store := New(unwritable, "/var/lib/flightctl", logger)
	targetID := Fingerprint(newer.Owner, newer.Image, newer.OCIType)
	require.NoError(store.RecordApplicationImageResult("web", "new-spec", targetID, "new-target", newer,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))

	current := store.ObservedApplicationResult("web", []ApplicationImage{newer}, true)
	require.NotNil(current)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, current.Outcome)
}

func TestWhenCachedApplicationHistoryIsUpdatedItShouldOnlyUpgradeNotUsed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		previous v1beta1.DeviceDeltaApplyOutcomeType
		next     v1beta1.DeviceDeltaApplyOutcomeType
		want     v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{name: "When NotUsed becomes NotRequired it should persist the upgrade", previous: v1beta1.DeviceDeltaApplyOutcomeNotUsed, next: v1beta1.DeviceDeltaApplyOutcomeNotRequired, want: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		{name: "When Applied receives NotRequired it should preserve Applied", previous: v1beta1.DeviceDeltaApplyOutcomeApplied, next: v1beta1.DeviceDeltaApplyOutcomeNotRequired, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When Fallback receives NotRequired it should preserve Fallback", previous: v1beta1.DeviceDeltaApplyOutcomeFallback, next: v1beta1.DeviceDeltaApplyOutcomeNotRequired, want: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When NotRequired receives NotUsed it should preserve NotRequired", previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, next: v1beta1.DeviceDeltaApplyOutcomeNotUsed, want: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			store := New(rw, "/state", logger)
			image := ApplicationImage{Image: "quay.io/acme/launcher:v1", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:launcher"}
			targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
			targets := map[string]map[string]string{"app": {targetID: "target"}}
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{"app": "v1"}))
			require.NoError(store.ReconcileApplicationTargets(targets, true))
			previous := v1beta1.DeviceDeltaApplyStatus{Outcome: tc.previous}
			if tc.previous == v1beta1.DeviceDeltaApplyOutcomeFallback {
				reason := "delta import failed"
				previous.FallbackReason = &reason
			}
			require.NoError(store.RecordApplicationImageResult("app", "v1", targetID, "target", image, previous))
			require.NotNil(store.ObservedApplicationResult("app", []ApplicationImage{image}, true))
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{"app": "v2"}))
			require.NoError(store.ReconcileApplicationTargets(targets, true))
			require.NoError(store.RecordApplicationImageResult("app", "v2", targetID, "target", image, v1beta1.DeviceDeltaApplyStatus{Outcome: tc.next}))
			restarted := New(rw, "/state", logger)
			result := restarted.ObservedApplicationResult("app", []ApplicationImage{image}, true)
			require.NotNil(result)
			require.Equal(tc.want, result.Outcome)
			require.Equal(previous.FallbackReason, result.FallbackReason)
		})
	}
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

func TestWhenPendingApplicationHistoryExceedsSoftCapacityItShouldPruneInactiveResults(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	image := ApplicationImage{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "podman-image"}
	targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
	for n := 0; n < 130; n++ {
		specKey := fmt.Sprintf("spec-%d", n)
		targetKey := fmt.Sprintf("target-%d", n)
		require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": specKey}))
		require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {targetID: targetKey}}, true))
		image.Digest = fmt.Sprintf("sha256:%d", n)
		require.NoError(store.RecordApplicationImageResult("web", specKey, targetID, targetKey, image,
			v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
		if n == 0 {
			require.NotNil(store.ObservedApplicationResult("web", []ApplicationImage{image}, true))
		}
	}
	restarted := New(rw, "/state", logger)
	image.Digest = "sha256:0"
	observed := restarted.ObservedApplicationResult("web", []ApplicationImage{image}, true)
	require.NotNil(observed)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, observed.Outcome)
	for n := 1; n < 129; n++ {
		image.Digest = fmt.Sprintf("sha256:%d", n)
		require.Nil(restarted.ObservedApplicationResult("web", []ApplicationImage{image}, true))
	}
	image.Digest = "sha256:129"
	observed = restarted.ObservedApplicationResult("web", []ApplicationImage{image}, true)
	require.NotNil(observed)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, observed.Outcome)
}

func TestWhenApplicationHistoryPersistenceFailsItShouldRetryTheSameIdentity(t *testing.T) {
	require := require.New(t)
	rw, logger, _, _, image := newUpdatedApplicationHistory(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	failing := fileio.NewMockReadWriter(ctrl)
	failing.EXPECT().ReadFile(gomock.Any()).DoAndReturn(rw.ReadFile).AnyTimes()
	fail := true
	writeErr := errors.New("disk unavailable")
	failing.EXPECT().MkdirAll(gomock.Any(), gomock.Any()).DoAndReturn(func(path string, mode fs.FileMode) error {
		if fail {
			return writeErr
		}
		return rw.MkdirAll(path, mode)
	}).AnyTimes()
	failing.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(path string, data []byte, mode fs.FileMode, opts ...fileio.FileOption) error {
			return rw.WriteFile(path, data, mode, opts...)
		}).AnyTimes()
	store := New(failing, "/var/lib/flightctl", logger)
	targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
	image.Digest = "sha256:retry"
	result := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	require.ErrorIs(store.RecordApplicationImageResult("web", "new-spec", targetID, "new-target", image, result), writeErr)
	require.Nil(New(rw, "/var/lib/flightctl", logger).ObservedApplicationResult("web", []ApplicationImage{image}, true))
	fail = false
	require.NoError(store.RecordApplicationImageResult("web", "new-spec", targetID, "new-target", image, result))
	observed := New(rw, "/var/lib/flightctl", logger).ObservedApplicationResult("web", []ApplicationImage{image}, true)
	require.NotNil(observed)
	require.Equal(result, *observed)
}

func TestApplicationLifetimeRejectsStaleCollectionsAndCompletions(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	image := ApplicationImage{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:old"}
	targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
	status := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}

	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "old-spec"}))
	oldLifetime := store.ApplicationLifetime("web")
	require.NotEmpty(oldLifetime)
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {targetID: "old-target"}}, true))
	require.NoError(store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: "web", Lifetime: oldLifetime, SpecKey: "old-spec", TargetID: targetID, TargetKey: "old-target"}, image, status))

	// An ordinary desired update keeps previously observed content associated.
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "new-spec"}))
	require.Equal(oldLifetime, store.ApplicationLifetime("web"))
	require.Equal(status, *store.ObservedApplicationResultForLifetime("web", oldLifetime, []ApplicationImage{image}, true))

	// Removal followed by the same name and spec creates a distinct lifetime.
	require.NoError(store.ReconcileApplicationSpecs(nil))
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "new-spec"}))
	newLifetime := store.ApplicationLifetime("web")
	require.NotEmpty(newLifetime)
	require.NotEqual(oldLifetime, newLifetime)
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {targetID: "new-target"}}, true))
	require.NoError(store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: "web", Lifetime: oldLifetime, SpecKey: "new-spec", TargetID: targetID, TargetKey: "new-target"}, image, status))
	require.Nil(store.ObservedApplicationResultForLifetime("web", oldLifetime, []ApplicationImage{image}, true))
	require.Nil(store.ObservedApplicationResultForLifetime("web", newLifetime, []ApplicationImage{image}, true))

	// A persisted lifetime remains stable over restart, and the old token remains rejected.
	restarted := New(rw, "/state", logger)
	require.NoError(restarted.ReconcileApplicationSpecs(map[string]string{"web": "new-spec"}))
	require.Equal(newLifetime, restarted.ApplicationLifetime("web"))
	require.NoError(restarted.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: "web", Lifetime: oldLifetime, SpecKey: "new-spec", TargetID: targetID, TargetKey: "new-target"}, image, status))
	require.Nil(restarted.ObservedApplicationResultForLifetime("web", oldLifetime, []ApplicationImage{image}, true))
}

func TestApplicationVolumeImageEvidencePersistsAndIsLifetimeScoped(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	lifetime := store.ApplicationLifetime("web")
	image := ApplicationImage{Image: "quay.io/acme/data:release", Owner: "root", OCIType: "PodmanArtifact", Digest: "sha256:artifact-manifest"}
	require.NoError(store.RecordApplicationVolumeImage("web", lifetime, "web_data", "2026-10-12T12:00:00Z", image))

	restarted := New(rw, "/state", logger)
	require.NoError(restarted.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	evidence, ok := restarted.ApplicationVolumeImage("web", lifetime, "web_data", "2026-10-12T12:00:00Z")
	require.True(ok)
	require.Equal(image, evidence)
	_, ok = restarted.ApplicationVolumeImage("web", lifetime, "web_data", "2026-10-12T12:00:01Z")
	require.False(ok)

	require.NoError(restarted.ReconcileApplicationSpecs(nil))
	require.NoError(restarted.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	newLifetime := restarted.ApplicationLifetime("web")
	_, ok = restarted.ApplicationVolumeImage("web", newLifetime, "web_data", "2026-10-12T12:00:00Z")
	require.False(ok)
	require.NoError(restarted.RecordApplicationVolumeImage("web", lifetime, "web_data", "2026-10-12T12:00:00Z", image))
	_, ok = restarted.ApplicationVolumeImage("web", newLifetime, "web_data", "2026-10-12T12:00:00Z")
	require.False(ok)
}

func TestWhenApplicationHasManyScopedImagesItShouldScaleHistoryByDistinctObservedCount(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := New(rw, "/state", log.NewPrefixLogger("test"))
	const application = "large-app"
	const spec = "spec"
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: spec}))
	lifetime := store.ApplicationLifetime(application)

	images := make([]ApplicationImage, 30)
	allTargets := make(map[string]string, len(images))
	for i := range images {
		image := ApplicationImage{
			Image:   fmt.Sprintf("quay.io/acme/workload-%02d:latest", i),
			Owner:   "root",
			OCIType: "PodmanImage",
			Digest:  fmt.Sprintf("sha256:observed-%02d", i),
		}
		images[i] = image
		targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
		targetKey := fmt.Sprintf("target-%02d", i)
		allTargets[targetID] = targetKey
		require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: {targetID: targetKey}}, false))
		require.NoError(t, store.RecordApplicationImageResultForAttempt(
			ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetID, TargetKey: targetKey}, image,
			v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		))
		require.NotNil(t, store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{image}, true))
	}

	activeTargets := make(map[string]string, 12)
	for _, image := range images[:12] {
		targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
		activeTargets[targetID] = allTargets[targetID]
	}
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: activeTargets}, true))
	observedWithReplicas := append([]ApplicationImage(nil), images[:12]...)
	observedWithReplicas = append(observedWithReplicas, images[3], images[3])
	selected := store.ObservedApplicationResultForLifetime(application, lifetime, observedWithReplicas, true)
	require.NotNil(t, selected)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, selected.Outcome)

	// With 12 distinct observed and active images the limit is 24 records.
	// The 12 current images plus the 12 most recently collected old images fit;
	// duplicate replicas do not consume extra capacity.
	require.NotNil(t, store.ObservedApplicationResultForLifetime(application, lifetime, images[18:30], true))
	require.Nil(t, store.ObservedApplicationResultForLifetime(application, lifetime, images[14:18], true))
	require.Nil(t, store.ObservedApplicationResultForLifetime(application, lifetime, images[12:18], true))
}

func TestWhenCollectionIsIncompleteItShouldKeepObservedAndActivePendingResults(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	const application = "web"
	const spec = "spec"
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: spec}))
	lifetime := store.ApplicationLifetime(application)
	images := []ApplicationImage{
		{Image: "quay.io/acme/web:a", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:a"},
		{Image: "quay.io/acme/web:b", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:b"},
		{Image: "quay.io/acme/web:c", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:c"},
	}
	targetIDs := make([]string, len(images))
	targetKeys := []string{"target-a", "target-b", "target-c"}
	for i, image := range images {
		targetIDs[i] = Fingerprint(image.Owner, image.Image, image.OCIType)
	}
	result := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: {targetIDs[0]: targetKeys[0]}}, true))
	require.NoError(t, store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetIDs[0], TargetKey: targetKeys[0]}, images[0], result))
	require.Equal(t, result, *store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[0]}, true))

	// This normal state write persists the prior complete observation before B
	// completes. B is pending until a complete observation includes it.
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: {targetIDs[1]: targetKeys[1]}}, true))
	require.NoError(t, store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetIDs[1], TargetKey: targetKeys[1]}, images[1], result))
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: {targetIDs[2]: targetKeys[2]}}, true))
	require.NoError(t, store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetIDs[2], TargetKey: targetKeys[2]}, images[2], result))

	restarted := New(rw, "/state", logger)
	require.Nil(t, restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[2]}, false))
	// The incomplete collection did not replace A as the protected observed set.
	require.Equal(t, result, *restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[0]}, true))
	// A complete collection of C keeps the active result. Superseded pending B
	// is eligible for LRU eviction, while the recent A result fits within capacity.
	require.Equal(t, result, *restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[2]}, true))
	require.Equal(t, result, *restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[0]}, true))
	require.Nil(t, restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[1]}, true))

	// Selection updates retention only in memory. The next normal state write
	// persists the eviction for a new process.
	require.NoError(t, restarted.SetOSTarget("flush-application-lru"))
	postWriteRestart := New(rw, "/state", logger)
	require.Nil(t, postWriteRestart.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[1]}, true))
	require.Equal(t, result, *postWriteRestart.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[0]}, true))
	require.Equal(t, result, *postWriteRestart.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[2]}, true))
}

func TestWhenSupersededPendingResultsExceedCapacityItShouldKeepOnlyObservedAndActiveTargets(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	const application = "web"
	const spec = "spec"
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{application: spec}))
	lifetime := store.ApplicationLifetime(application)
	images := []ApplicationImage{
		{Image: "quay.io/acme/web:a", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:a"},
		{Image: "quay.io/acme/web:b", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:b"},
		{Image: "quay.io/acme/web:c", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:c"},
		{Image: "quay.io/acme/web:d", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:d"},
	}
	result := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	var activeTarget map[string]string
	for i, image := range images {
		targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
		targetKey := fmt.Sprintf("target-%s", image.Digest)
		activeTarget = map[string]string{targetID: targetKey}
		require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{application: activeTarget}, true))
		require.NoError(store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetID, TargetKey: targetKey}, image, result))

		observed := images[0]
		if i == 0 {
			observed = image
		}
		require.Equal(result, *store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{observed}, true))
	}

	// Persist the latest observations before checking the retained history.
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{application: activeTarget}, true))
	restarted := New(rw, "/state", logger)
	for _, superseded := range images[1:3] {
		require.Nil(restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{superseded}, true))
	}
	require.Equal(result, *restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[0]}, true))
	require.Equal(result, *restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[3]}, true))
}

func TestWhenProtectedHistoryOverflowsItShouldKeepUnknownObservedKeysSafe(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := New(rw, "/state", log.NewPrefixLogger("test"))
	const application = "web"
	const spec = "spec"
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: spec}))
	lifetime := store.ApplicationLifetime(application)
	images := []ApplicationImage{
		{Image: "quay.io/acme/web:b", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:b"},
		{Image: "quay.io/acme/web:c", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:c"},
		{Image: "quay.io/acme/web:d", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:d"},
	}
	for _, image := range images {
		targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
		targetKey := fmt.Sprintf("target-%s", image.Digest)
		require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: {targetID: targetKey}}, true))
		require.NoError(t, store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetID, TargetKey: targetKey}, image,
			v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	}
	unknown := ApplicationImage{Image: "quay.io/acme/web:a", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:a"}
	require.Nil(t, store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{unknown}, true))
	// The unknown observed key protects no history entry. Pruning keeps the
	// active target available while removing superseded pending history as needed.
	require.NotNil(t, store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{images[2]}, true))
	require.Len(t, store.state.Applications[application].History, 2)
}

func TestWhenCompleteStatusUsesHistoryItShouldPersistRecencyWithNextWrite(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	const application = "web"
	const spec = "spec"
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: spec}))
	lifetime := store.ApplicationLifetime(application)

	first := ApplicationImage{Image: "quay.io/acme/cache-a:v1", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:a"}
	second := ApplicationImage{Image: "quay.io/acme/cache-b:v1", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:b"}
	if applicationHistoryKey(first) > applicationHistoryKey(second) {
		first, second = second, first
	}
	third := ApplicationImage{Image: "quay.io/acme/cache-c:v1", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:c"}

	record := func(image ApplicationImage, targetKey string) {
		t.Helper()
		targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
		require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: {targetID: targetKey}}, true))
		require.NoError(t, store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetID, TargetKey: targetKey}, image,
			v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
		require.NotNil(t, store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{image}, true))
	}
	record(first, "target-first")
	record(second, "target-second")

	statePath := filepath.Join("/state", FileName)
	before, err := rw.ReadFile(statePath)
	require.NoError(t, err)
	require.NotNil(t, store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{first}, true))
	after, err := rw.ReadFile(statePath)
	require.NoError(t, err)
	require.Equal(t, before, after, "status collection must not write retained history")

	// A normal no-op reconciliation is still the next state write and persists
	// the newer recency value even though status delivery is outside the store.
	secondID := Fingerprint(second.Owner, second.Image, second.OCIType)
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: {secondID: "target-second"}}, true))
	restarted := New(rw, "/state", logger)
	thirdID := Fingerprint(third.Owner, third.Image, third.OCIType)
	require.NoError(t, restarted.ReconcileApplicationTargets(map[string]map[string]string{application: {thirdID: "target-third"}}, true))
	require.NoError(t, restarted.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: thirdID, TargetKey: "target-third"}, third,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	require.NotNil(t, restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{third}, true))
	require.Nil(t, restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{second}, true))
	require.NotNil(t, restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{first}, true))
}

func TestWhenRecencyWriteFailsItShouldRetryBeforeEvictingByLRU(t *testing.T) {
	failWrite := false
	var durable []byte
	writeErr := errors.New("disk full")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	rw := newFailingReadWriter(t, ctrl, &failWrite, &durable, writeErr)
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	const application = "web"
	const spec = "spec"
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: spec}))
	lifetime := store.ApplicationLifetime(application)
	first := ApplicationImage{Image: "quay.io/acme/cache-a:v1", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:a"}
	second := ApplicationImage{Image: "quay.io/acme/cache-b:v1", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:b"}
	if applicationHistoryKey(first) > applicationHistoryKey(second) {
		first, second = second, first
	}
	third := ApplicationImage{Image: "quay.io/acme/cache-c:v1", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:c"}

	record := func(target *Store, image ApplicationImage, targetKey string) {
		t.Helper()
		targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
		require.NoError(t, target.ReconcileApplicationTargets(map[string]map[string]string{application: {targetID: targetKey}}, true))
		require.NoError(t, target.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetID, TargetKey: targetKey}, image,
			v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
		require.NotNil(t, target.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{image}, true))
	}
	record(store, first, "target-first")
	record(store, second, "target-second")
	require.NotNil(t, store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{first}, true))
	secondID := Fingerprint(second.Owner, second.Image, second.OCIType)
	activeTargets := map[string]map[string]string{application: {secondID: "target-second"}}

	failWrite = true
	require.ErrorIs(t, store.ReconcileApplicationTargets(activeTargets, true), writeErr)
	failWrite = false
	require.NoError(t, store.ReconcileApplicationTargets(activeTargets, true))

	restarted := New(rw, "/state", logger)
	thirdID := Fingerprint(third.Owner, third.Image, third.OCIType)
	require.NoError(t, restarted.ReconcileApplicationTargets(map[string]map[string]string{application: {thirdID: "target-third"}}, true))
	require.NoError(t, restarted.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: thirdID, TargetKey: "target-third"}, third,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	require.NotNil(t, restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{third}, true))
	require.Nil(t, restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{second}, true))
	require.NotNil(t, restarted.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{first}, true))
}

func TestWhenBootedAndActiveOSResultsExceedCapacityItShouldEvictSupersededResults(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	result := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	require.NoError(t, store.SetOSTarget("attempt-a"))
	require.NoError(t, store.RecordOSImageResult("attempt-a", "os:a", "sha256:a", result))
	require.Equal(t, result, *store.ObservedOSResult("os:a", "sha256:a"))
	require.NoError(t, store.SetOSTarget("attempt-b"))
	require.NoError(t, store.RecordOSImageResult("attempt-b", "os:b", "sha256:b", result))
	require.NoError(t, store.SetOSTarget("attempt-c"))
	require.NoError(t, store.RecordOSImageResult("attempt-c", "os:c", "sha256:c", result))

	// Restart with A still booted. A and active C are protected; completed B is
	// superseded and must be eligible for eviction.
	restarted := New(rw, "/state", logger)
	require.Equal(t, result, *restarted.ObservedOSResult("os:a", "sha256:a"))
	require.Equal(t, result, *restarted.ObservedOSResult("os:c", "sha256:c"))
	require.Nil(t, restarted.ObservedOSResult("os:b", "sha256:b"))
	require.Equal(t, result, *restarted.ObservedOSResult("os:a", "sha256:a"))
}

func TestWhenMutableOSTargetChangesDigestItShouldEvictSupersededHistory(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := New(rw, "/state", log.NewPrefixLogger("test"))
	const targetKey = "mutable-os-target"
	const image = "quay.io/acme/os:stable"
	result := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}

	require.NoError(t, store.SetOSTarget(targetKey))
	require.NoError(t, store.RecordOSImageResult(targetKey, image, "sha256:a", result))
	require.Equal(t, result, *store.ObservedOSResult(image, "sha256:a"))
	require.NoError(t, store.RecordOSImageResult(targetKey, image, "sha256:b", result))
	require.Equal(t, result, *store.ObservedOSResult(image, "sha256:b"))
	require.NoError(t, store.RecordOSImageResult(targetKey, image, "sha256:c", result))

	// B is still booted and C is pending. A has been superseded even though all
	// three results share the same mutable OS target reference.
	require.Equal(t, result, *store.ObservedOSResult(image, "sha256:b"))
	require.Len(t, store.state.OS.History, 2)
	require.Nil(t, store.ObservedOSResult(image, "sha256:a"))
	require.Equal(t, result, *store.ObservedOSResult(image, "sha256:c"))
}

func TestWhenAMutableApplicationTargetChangesDigestItShouldEvictSupersededHistory(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := New(rw, "/state", log.NewPrefixLogger("test"))
	const application, spec, targetKey = "web", "spec", "mutable-target"
	image := ApplicationImage{Image: "quay.io/acme/web:stable", Owner: "root", OCIType: "PodmanImage"}
	targetID := Fingerprint(image.Owner, image.Image, image.OCIType)
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{application: spec}))
	lifetime := store.ApplicationLifetime(application)
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{application: {targetID: targetKey}}, true))
	result := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	for _, value := range []string{"sha256:a", "sha256:b"} {
		image.Digest = value
		require.NoError(store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetID, TargetKey: targetKey}, image, result))
		require.Equal(result, *store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{image}, true))
	}
	image.Digest = "sha256:c"
	require.NoError(store.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: spec, TargetID: targetID, TargetKey: targetKey}, image, result))
	image.Digest = "sha256:b"
	require.Equal(result, *store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{image}, true))
	image.Digest = "sha256:a"
	require.Nil(store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{image}, true), "old observed content must become evictable even when its mutable target key stays active")
	image.Digest = "sha256:c"
	require.Equal(result, *store.ObservedApplicationResultForLifetime(application, lifetime, []ApplicationImage{image}, true), "the active unobserved result must remain protected")
}

func TestWhenVersionOneStateIsReloadedItShouldPreserveVerifiedOutcomes(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	applied := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	require.NoError(store.SetOSTarget("attempt"))
	require.NoError(store.RecordOSImageResult("attempt", "quay.io/acme/os:v1", "sha256:verified", applied))
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	lifetime := store.ApplicationLifetime("web")
	require.NoError(store.RecordApplicationVolumeImage("web", lifetime, "data", "created", ApplicationImage{
		Image: "quay.io/acme/data:v1", Owner: "root", OCIType: "PodmanArtifact", Digest: "sha256:artifact",
	}))
	data, err := rw.ReadFile("/state/" + FileName)
	require.NoError(err)
	var document map[string]json.RawMessage
	require.NoError(json.Unmarshal(data, &document))
	require.JSONEq("1", string(document["version"]))
	restarted := New(rw, "/state", logger)
	require.Equal(applied, *restarted.ObservedOSResult("quay.io/acme/os:v1", "sha256:verified"))
	require.Equal(lifetime, restarted.ApplicationLifetime("web"))
	_, found := restarted.ApplicationVolumeImage("web", lifetime, "data", "created")
	require.True(found)
}

func TestWhenMutableApplicationAttemptsAreSupersededItShouldKeepOnlyObservedAndActiveEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sameSpec bool
	}{
		{name: "When the desired attempt changes it should evict superseded mutable content"},
		{name: "When mutable content is replaced during the same attempt it should evict superseded content", sameSpec: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			store := New(rw, "/state", logger)
			outcome := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
			observed := ApplicationImage{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:observed"}
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "initial"}))
			require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {"image": "mutable-target"}}, true))
			require.NoError(store.RecordApplicationImageResult("web", "initial", "image", "mutable-target", observed, outcome))
			require.NotNil(store.ObservedApplicationResult("web", []ApplicationImage{observed}, true))
			var first, latest ApplicationImage
			for i := 1; i <= 8; i++ {
				spec := fmt.Sprintf("update-%d", i)
				if tc.sameSpec {
					spec = "initial"
				}
				require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": spec}))
				require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {"image": "mutable-target"}}, true))
				latest = observed
				latest.Digest = fmt.Sprintf("sha256:pending-%d", i)
				if i == 1 {
					first = latest
				}
				require.NoError(store.RecordApplicationImageResult("web", spec, "image", "mutable-target", latest, outcome))
				require.Nil(store.ObservedApplicationResult("web", nil, false))
			}
			require.Nil(New(rw, "/state", logger).ObservedApplicationResult("web", []ApplicationImage{first}, true), "superseded evidence should be evicted while collection remains incomplete")
			restarted := New(rw, "/state", logger)
			require.NotNil(restarted.ObservedApplicationResult("web", []ApplicationImage{observed}, true), "incomplete collection must preserve previous observed evidence")
			require.NotNil(restarted.ObservedApplicationResult("web", []ApplicationImage{latest}, true), "the active update must survive restart before its first report")
			require.Nil(restarted.ObservedApplicationResult("web", []ApplicationImage{first}, true), "superseded unobserved attempts must become evictable even when the target key is unchanged")
		})
	}
}
func TestWhenMutableOSStagesAreReplacedItShouldKeepTheBootedAndLatestStagedEvidence(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := New(rw, "/state", logger)
	const image = "quay.io/acme/os:latest"
	outcome := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	require.NoError(store.SetOSTarget("mutable-target"))
	require.NoError(store.RecordOSImageResult("mutable-target", image, "sha256:booted", outcome))
	require.NotNil(store.ObservedOSResult(image, "sha256:booted"))
	for i := 1; i <= 8; i++ {
		require.NoError(store.RecordOSImageResult("mutable-target", image, fmt.Sprintf("sha256:stage-%d", i), outcome))
		require.NotNil(store.ObservedOSResult(image, "sha256:booted"))
	}
	require.NoError(store.RecordOSResult("mutable-target", outcome))
	restarted := New(rw, "/state", logger)
	require.NotNil(restarted.ObservedOSResult(image, "sha256:booted"))
	require.NotNil(restarted.ObservedOSResult(image, "sha256:stage-8"), "the most recently accepted stage must survive restart")
	require.Nil(restarted.ObservedOSResult(image, "sha256:stage-1"), "replaced stages are no longer active even if the mutable target key is unchanged")
}

func TestWhenAnUnobservedCachedImageIsReusedItShouldKeepItsEvidenceAcrossSpecChanges(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := New(rw, "/state", log.NewPrefixLogger("test"))
	outcome := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	booted := ApplicationImage{Image: "quay.io/acme/old:v1", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:observed"}
	cached := ApplicationImage{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: "PodmanImage", Digest: "sha256:pending"}
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "initial"}))
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {"old": "old-target", "cached": "cached-target"}}, true))
	require.NoError(store.RecordApplicationImageResult("web", "initial", "old", "old-target", booted, outcome))
	require.NotNil(store.ObservedApplicationResult("web", []ApplicationImage{booted}, true))
	require.NoError(store.RecordApplicationImageResult("web", "initial", "cached", "cached-target", cached, outcome))
	// An environment-only change can reuse cached content without generating a
	// new outcome when a mutable desired digest cannot be verified.
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "changed-environment"}))
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {"cached": "cached-target", "other": "other-target"}}, true))
	for i := 0; i < 32; i++ {
		image := ApplicationImage{Image: "quay.io/acme/other:latest", Owner: "root", OCIType: "PodmanImage", Digest: fmt.Sprintf("sha256:replacement-%d", i)}
		require.NoError(store.RecordApplicationImageResult("web", "changed-environment", "other", "other-target", image, outcome))
		require.Nil(store.ObservedApplicationResult("web", nil, false))
	}
	require.NotNil(New(rw, "/state", log.NewPrefixLogger("test")).ObservedApplicationResult("web", []ApplicationImage{cached}, true), "the latest accepted content of an unchanged active target must remain protected")
}
