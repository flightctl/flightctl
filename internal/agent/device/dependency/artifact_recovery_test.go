package dependency

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCachedArtifactHintKeepsCachedArtifact(t *testing.T) {
	const image = "quay.io/acme/compose:target"
	const hint = "quay.io/acme/delta:target"
	tests := []struct {
		name       string
		ociType    OCIType
		setupMocks func(*executer.MockExecuter)
	}{
		{
			name:    "When a cached artifact has a delta hint it should report NotRequired without pulling the artifact",
			ociType: OCITypePodmanArtifact,
			setupMocks: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0)
			},
		},
		{
			name:    "When a cached auto detected artifact has a delta hint it should report NotRequired without pulling the artifact",
			ociType: OCITypeAuto,
			setupMocks: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 1)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			tt.setupMocks(exec)
			resources := resource.NewMockManager(ctrl)
			resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false)
			logger := log.NewPrefixLogger("test")
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			manager := NewPrefetchManager(logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.Config{})
			defer manager.Cleanup()
			manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
				return &OCICollection{Targets: OCIPullTargetsByUser{"": {{Type: tt.ociType, Reference: image, PullPolicy: v1beta1.PullIfNotPresent, Delta: &OCIDeltaTarget{Hint: hint, Application: "compose"}}}}}, nil
			}))
			require.NoError(manager.BeforeUpdate(t.Context(), nil, &v1beta1.DeviceSpec{}))
			require.True(manager.IsReady(t.Context()))
			status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "compose"}}}
			require.NoError(manager.Status(t.Context(), status))
			require.NotNil(status.Applications[0].LastDelta)
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, status.Applications[0].LastDelta.Outcome)
			require.Nil(status.Applications[0].LastDelta.FallbackReason)
		})
	}
}
