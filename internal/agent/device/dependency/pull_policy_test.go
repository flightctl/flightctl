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

func TestPrefetchReusesCachedTargetsAcrossReconciliation(t *testing.T) {
	tests := []struct {
		name     string
		ociType  OCIType
		policies []v1beta1.ImagePullPolicy
	}{
		{name: "When PullAlways follows a cached image request it should reuse the image", ociType: OCITypePodmanImage, policies: []v1beta1.ImagePullPolicy{v1beta1.PullIfNotPresent, v1beta1.PullAlways}},
		{name: "When PullAlways precedes a cached image request it should reuse the image", ociType: OCITypePodmanImage, policies: []v1beta1.ImagePullPolicy{v1beta1.PullAlways, v1beta1.PullIfNotPresent}},
		{name: "When an image volume specifies PullAlways it should reuse the cached artifact", ociType: OCITypePodmanArtifact, policies: []v1beta1.ImagePullPolicy{v1beta1.PullAlways}},
		{name: "When an automatic image volume specifies PullAlways it should reuse the cached artifact", ociType: OCITypeAuto, policies: []v1beta1.ImagePullPolicy{v1beta1.PullAlways}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			resources := resource.NewMockManager(ctrl)
			resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(2)
			switch tt.ociType {
			case OCITypePodmanImage:
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testOSImage).Return("", "", 0).Times(2)
			case OCITypeAuto:
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", testOSImage).Return("", "", 1).Times(2)
				fallthrough
			case OCITypePodmanArtifact:
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", testOSImage).Return("", "", 0).Times(2)
			}
			logger := log.NewPrefixLogger("test")
			rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			podman := client.NewPodman(logger, exec, rw, poll.Config{})
			manager := NewPrefetchManager(logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil }, nil,
				client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.Config{})
			defer manager.Cleanup()
			for range 2 {
				for _, policy := range tt.policies {
					manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
						return &OCICollection{Targets: OCIPullTargetsByUser{"": {{Type: tt.ociType, Reference: testOSImage, PullPolicy: policy}}}}, nil
					}))
				}
				require.NoError(manager.BeforeUpdate(t.Context(), nil, &v1beta1.DeviceSpec{}))
				require.True(manager.IsReady(t.Context()))
				manager.Cleanup()
			}
		})
	}
}

func TestCleanupClearsCollectorsWithoutTargets(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	resources := resource.NewMockManager(ctrl)
	logger := log.NewPrefixLogger("test")
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	manager := NewPrefetchManager(logger, nil, nil, client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.Config{})
	defer manager.Cleanup()
	collected := 0
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		collected++
		return &OCICollection{}, nil
	}))
	require.NoError(manager.BeforeUpdate(t.Context(), nil, &v1beta1.DeviceSpec{}))
	manager.Cleanup()
	require.NoError(manager.BeforeUpdate(t.Context(), nil, &v1beta1.DeviceSpec{}))
	require.Equal(1, collected, "a completed collector must not run on the next reconciliation unless registered again")
}
