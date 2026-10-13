package os

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
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
		{name: "When the booted OS digest is unknown it should still report NotRequired", booted: true},
		{name: "When the desired OS tag is cached it should reuse it without registry access"},
		{name: "When a booted OS image was applied from a delta it should preserve Applied", booted: true, previous: lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeApplied)},
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
			bootc.EXPECT().Status(gomock.Any()).Return(bootcStatus(bootedImage, tt.digest), nil).Times(3)
			if !tt.booted {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testDesiredImage).Return("", "", 0).Times(2)
			}
			m := newTestManager(t, bootc, exec, resolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr(testHintedDelta)}}
			require.NoError(m.BeforeUpdate(t.Context(), nil, desired))
			if tt.previous != nil {
				reason := ""
				if *tt.previous == v1beta1.DeviceDeltaApplyOutcomeFallback {
					reason = "delta reconstruction failed"
				}
				m.recordDeltaResult(*tt.previous, reason, false)
			}
			for range 2 {
				collection, err := m.CollectOCITargets(t.Context(), nil, desired)
				require.NoError(err)
				require.Empty(collection.Targets)
			}
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(t.Context(), status))
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
