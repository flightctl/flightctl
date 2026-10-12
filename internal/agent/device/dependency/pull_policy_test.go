package dependency

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCollectorsMergePullPolicies(t *testing.T) {
	tests := []struct {
		name       string
		policies   []v1beta1.ImagePullPolicy
		setupMocks func(*executer.MockExecuter, *resource.MockManager)
	}{
		{
			name:     "When PullAlways follows a cached image request it should require a fresh pull",
			policies: []v1beta1.ImagePullPolicy{v1beta1.PullIfNotPresent, v1beta1.PullAlways},
			setupMocks: func(exec *executer.MockExecuter, resources *resource.MockManager) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testOSImage).Return("", "", 0)
				resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false)
			},
		},
		{
			name:     "When PullAlways precedes a cached image request it should require a fresh pull",
			policies: []v1beta1.ImagePullPolicy{v1beta1.PullAlways, v1beta1.PullIfNotPresent},
			setupMocks: func(exec *executer.MockExecuter, resources *resource.MockManager) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testOSImage).Return("", "", 0)
				resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			resources := resource.NewMockManager(ctrl)
			tt.setupMocks(exec, resources)
			logger := log.NewPrefixLogger("test")
			rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			podman := client.NewPodman(logger, exec, rw, poll.Config{})
			manager := NewPrefetchManager(logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil }, nil,
				client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.Config{})
			defer manager.Cleanup()
			for _, policy := range tt.policies {
				manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
					return &OCICollection{Targets: OCIPullTargetsByUser{"": {{Type: OCITypePodmanImage, Reference: testOSImage, PullPolicy: policy}}}}, nil
				}))
			}
			require.ErrorIs(manager.BeforeUpdate(t.Context(), nil, &v1beta1.DeviceSpec{}), errors.ErrPrefetchNotReady)
			require.False(manager.IsReady(t.Context()))
		})
	}
}
