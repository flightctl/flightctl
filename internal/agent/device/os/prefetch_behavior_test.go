package os

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestOSLocalTargetSkipsFetching(t *testing.T) {
	tests := []struct {
		name     string
		booted   bool
		digest   string
		previous *v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{name: "When the desired OS tag is booted it should reuse it without registry access", booted: true, digest: testSourceDigest},
		{name: "When the booted OS digest is unknown it should skip pulling and omit an unverified outcome", booted: true},
		{name: "When the desired OS tag is cached it should reuse it without registry access"},
		{name: "When a booted OS image was applied from a delta it should preserve Applied", booted: true, digest: testSourceDigest, previous: lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeApplied)},
		{name: "When a cached OS image followed delta recovery it should preserve Fallback", previous: lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeFallback)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			bootc := NewMockClient(ctrl)
			resolver := dependency.NewMockPullConfigResolver(ctrl)
			bootedImage := testBootedImage
			if tt.booted {
				bootedImage = testDesiredImage
			}
			bootc.EXPECT().Status(gomock.Any()).Return(bootcStatus(bootedImage, tt.digest), nil).Times(2)
			if !tt.booted {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testDesiredImage).Return("", "", 0).Times(2)
				expectFullOSStage(bootc, testDesiredImage, testDeltaDigest)
				bootc.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
			} else {
				bootc.EXPECT().Status(gomock.Any()).Return(bootcStatus(bootedImage, tt.digest), nil)
			}

			m := newTestManager(t, bootc, exec, resolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr(testHintedDelta)}}
			require.NoError(m.BeforeUpdate(t.Context(), nil, desired))
			if tt.previous != nil {
				reason := ""
				if *tt.previous == v1beta1.DeviceDeltaApplyOutcomeFallback {
					reason = "delta reconstruction failed"
				}
				m.recordDeltaResult(m.activeDeltaTargetKey, *tt.previous, reason, false)
				if tt.booted {
					m.recordVerifiedOSResult(m.activeDeltaTargetKey, bootedImage, tt.digest)
				}
			}
			for range 2 {
				collection, err := m.CollectOCITargets(t.Context(), nil, desired)
				require.NoError(err)
				require.Empty(collection.Targets)
			}
			if !tt.booted {
				require.NoError(m.AfterUpdate(t.Context(), desired))
			}

			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(t.Context(), status))
			if tt.booted && tt.digest == "" {
				require.Nil(status.Os.LastDelta)
				want := v1beta1.DeviceDeltaApplyOutcomeNotRequired
				if tt.previous != nil {
					want = *tt.previous
				}
				require.Equal(want, *m.deltaOutcome)
				return
			}

			require.NotNil(status.Os.LastDelta)
			outcome := v1beta1.DeviceDeltaApplyOutcomeNotRequired
			if tt.previous != nil {
				outcome = *tt.previous
			}
			require.Equal(outcome, status.Os.LastDelta.Outcome)
			if outcome == v1beta1.DeviceDeltaApplyOutcomeFallback {
				require.Equal("delta reconstruction failed", *status.Os.LastDelta.FallbackReason)
			}
		})
	}
}

func TestStagedOSOutcomeAfterRestart(t *testing.T) {
	tests := []struct {
		name         string
		cached       bool
		discovery    bool
		previous     v1beta1.DeviceDeltaApplyStatus
		stagedDigest string
		expected     v1beta1.DeviceDeltaApplyStatus
	}{
		{
			name:   "When a cached tag stages different content after restart it should not inherit Applied",
			cached: true, stagedDigest: testDeltaDigest,
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			expected: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		},
		{
			name:   "When a cached tag stages different content after restart it should not inherit Fallback",
			cached: true, stagedDigest: testDeltaDigest,
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: lo.ToPtr(fallbackReasonApply)},
			expected: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		},
		{
			name:   "When a cached image matches verified history after restart it should retain Applied",
			cached: true, stagedDigest: testSourceDigest,
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			expected: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		},
		{
			name:   "When a cached image matches verified history after restart it should retain Fallback and its reason",
			cached: true, stagedDigest: testSourceDigest,
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: lo.ToPtr(fallbackReasonApply)},
			expected: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: lo.ToPtr(fallbackReasonApply)},
		},
		{
			name:         "When a full fetch stages different content after restart it should not inherit Applied",
			stagedDigest: testDeltaDigest,
			previous:     v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			expected:     v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		},
		{
			name:         "When a full fetch stages different content after restart it should not inherit Fallback",
			stagedDigest: testDeltaDigest,
			previous:     v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: lo.ToPtr(fallbackReasonApply)},
			expected:     v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		},
		{
			name:      "When discovery finds no delta after restart it should record the new full fetch outcome",
			discovery: true, stagedDigest: testDeltaDigest,
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			expected: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			bootc := NewMockClient(ctrl)
			resolver := dependency.NewMockPullConfigResolver(ctrl)
			original := newTestManager(t, bootc, exec, resolver, Capabilities{})
			store := deltastatus.New(original.readWriter, "/state", original.log)
			targetKey := deltastatus.Fingerprint(testDesiredImage)
			require.NoError(store.SetOSTarget(targetKey))
			require.NoError(store.RecordOSResult(targetKey, tt.previous))
			require.NoError(store.RecordOSImageResult(targetKey, testDesiredImage, testSourceDigest, tt.previous))
			caps := Capabilities{DeltaEligible: tt.discovery, BootcVersion: "bootc 1.15.0"}
			restarted := NewManager(original.log, bootc, caps, original.readWriter, original.podmanClient,
				resolver, original.ociDelta, original.skopeo, original.pullTimeout,
				WithDeltaStatusStore(deltastatus.New(original.readWriter, "/state", original.log)))
			bootc.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
			expectImageExists(exec, testDesiredImage, tt.cached)
			if !tt.cached {
				expectPullConfig(t, resolver)
			}
			if tt.discovery {
				expectListReferrers(exec, testDesiredImage, `{"schemaVersion":2,"manifests":[]}`, "", 0)
			}
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage}}
			collection, err := restarted.CollectOCITargets(t.Context(), nil, desired)
			require.NoError(err)
			if tt.cached {
				require.Empty(collection.Targets)
			} else {
				require.Len(collection.Targets[v1beta1.CurrentProcessUsername], 1)
				require.Equal(testDesiredImage, collection.Targets[v1beta1.CurrentProcessUsername][0].Reference)
			}
			expectFullOSStage(bootc, testDesiredImage, tt.stagedDigest)
			require.NoError(restarted.AfterUpdate(t.Context(), desired))

			observed := bootcStatus(testDesiredImage, tt.stagedDigest)
			bootc.EXPECT().Status(gomock.Any()).DoAndReturn(func(context.Context) (*Status, error) { return observed, nil }).AnyTimes()
			for range 2 {
				status := &v1beta1.DeviceStatus{}
				require.NoError(restarted.Status(t.Context(), status))
				require.Equal(tt.stagedDigest, status.Os.ImageDigest)
				require.Equal(&tt.expected, status.Os.LastDelta)
				observed.Status.Booted.Image.ImageDigest = testSourceDigest
				require.NoError(restarted.Status(t.Context(), status))
				require.Equal(&tt.previous, status.Os.LastDelta)
				observed.Status.Booted.Image.ImageDigest = tt.stagedDigest
				restarted = NewManager(original.log, bootc, caps, original.readWriter, original.podmanClient,
					resolver, original.ociDelta, original.skopeo, original.pullTimeout,
					WithDeltaStatusStore(deltastatus.New(original.readWriter, "/state", original.log)))
			}
		})
	}
}

func TestOSFallbackSurvivesCachedRetry(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	bootc := NewMockClient(ctrl)
	resolver := dependency.NewMockPullConfigResolver(ctrl)
	m := newTestManager(t, bootc, exec, resolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
	m.deltaStatusStore = deltastatus.New(m.readWriter, "/state", m.log)
	desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr("not an image reference")}}
	bootc.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(2)
	expectImageExists(exec, testDesiredImage, false)
	expectPullConfig(t, resolver)
	collection, err := m.CollectOCITargets(t.Context(), nil, desired)
	require.NoError(err)
	require.Len(collection.Targets[v1beta1.CurrentProcessUsername], 1)

	expectImageExists(exec, testDesiredImage, true)
	collection, err = m.CollectOCITargets(t.Context(), nil, desired)
	require.NoError(err)
	require.Empty(collection.Targets)
	expectFullOSStage(bootc, testDesiredImage, testDeltaDigest)
	require.NoError(m.AfterUpdate(t.Context(), desired))
	bootc.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
	status := &v1beta1.DeviceStatus{}
	require.NoError(m.Status(t.Context(), status))
	require.NotNil(status.Os.LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, status.Os.LastDelta.Outcome)
	require.NotNil(status.Os.LastDelta.FallbackReason)
	require.Contains(*status.Os.LastDelta.FallbackReason, "invalid delta reference")
}
