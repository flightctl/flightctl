package os

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestOSFullTargetPrefetchRefreshesCachedImage(t *testing.T) {
	tests := []struct {
		name        string
		hintFailed  bool
		pullFailure bool
	}{
		{name: "When the cached OS tag is outdated and no delta is usable it should refresh before staging"},
		{name: "When a delta hint is unusable it should refresh the outdated cached OS tag before staging", hintFailed: true},
		{name: "When refreshing the outdated OS tag fails it should fail prefetch and prevent staging", pullFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			bootc := NewMockClient(ctrl)
			resolver := dependency.NewMockPullConfigResolver(ctrl)
			collections := 1
			if tt.pullFailure {
				collections++
			}
			bootc.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(collections + 1)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testDesiredImage).Return("", "", 0).Times(collections + 1)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", testDesiredImage).Return(fmt.Sprintf(`{"Digest":%q}`, testSourceDigest), "", 0).Times(collections)
			for i := 0; i < collections; i++ {
				expectTargetDigest(exec, testDesiredImage, testDeltaDigest)
				expectPlatformIndex(exec, testDesiredImage, testDeltaDigest)
				if tt.hintFailed {
					exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+testHintedDelta, "--no-creds").Return(`{"artifactType":"application/example"}`, "", 0)
				} else {
					expectListReferrers(exec, testDesiredImage, `{"manifests":[]}`, "", 0)
				}
			}
			expectPullConfig(t, resolver)
			m := newTestManager(t, bootc, exec, resolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage}}
			if tt.hintFailed {
				hint := testHintedDelta
				desired.Os.DeltaImage = &hint
			}
			collection, err := m.CollectOCITargets(t.Context(), nil, desired)
			require.NoError(err)
			resources := resource.NewMockManager(ctrl)
			if tt.pullFailure {
				resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false)
			}
			prefetch := dependency.NewPrefetchManager(m.log,
				func(v1beta1.Username) (*client.Podman, error) { return m.podmanClient, nil },
				func(v1beta1.Username) (*client.Skopeo, error) { return m.skopeo, nil },
				client.NewCLIClients(), m.readWriter, util.Duration(time.Second), resources, poll.Config{})
			defer prefetch.Cleanup()
			require.NoError(prefetch.Schedule(t.Context(), collection.Targets))
			require.False(prefetch.IsReady(t.Context()), "an outdated cached OS image still needs a full pull")
			var pulled atomic.Bool
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", testDesiredImage).DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
				pulled.Store(true)
				if tt.pullFailure {
					return "", "full target unavailable", 1
				}
				return "", "", 0
			})
			ctx, cancel := context.WithCancel(t.Context())
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				prefetch.Run(ctx)
			}()
			defer func() { cancel(); <-stopped }()
			if tt.pullFailure {
				require.Eventually(pulled.Load, time.Second, time.Millisecond)
				cancel()
				<-stopped
				prefetch.RegisterOCICollector(m)
				require.ErrorContains(prefetch.BeforeUpdate(t.Context(), nil, desired), "full target unavailable")
				require.False(prefetch.IsReady(t.Context()))
			} else {
				require.Eventually(func() bool { return prefetch.IsReady(ctx) }, time.Second, time.Millisecond)
				// A retry of the same collection keeps the completed pull ready.
				require.NoError(prefetch.Schedule(ctx, collection.Targets))
				require.True(prefetch.IsReady(ctx))
				bootc.EXPECT().Switch(gomock.Any(), testDesiredImage).DoAndReturn(func(context.Context, string) error {
					require.True(pulled.Load(), "staging must use the refreshed image")
					return nil
				})
				require.NoError(m.AfterUpdate(ctx, desired))
			}
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(t.Context(), status))
			outcome := v1beta1.DeviceDeltaApplyOutcomeNotUsed
			if tt.hintFailed {
				outcome = v1beta1.DeviceDeltaApplyOutcomeFallback
			}
			require.Equal(outcome, status.Os.LastDelta.Outcome)
		})
	}
}
