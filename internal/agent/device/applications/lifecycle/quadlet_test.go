package lifecycle

import (
	"context"
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// allows for matching the first N arguments in a variadic list
type variadicMatcher struct {
	expected []string
}

func (v *variadicMatcher) Matches(x interface{}) bool {
	args, ok := x.([]string)
	if !ok {
		return false
	}

	if len(v.expected) > len(args) {
		return false
	}
	for i, expected := range v.expected {
		if expected != args[i] {
			return false
		}
	}
	return true
}

func (v *variadicMatcher) String() string {
	return fmt.Sprintf("expected: %v", v.expected)
}

func newMatcher(expected ...string) gomock.Matcher {
	return &variadicMatcher{expected: expected}
}

// unorderedMatcher matches variadic string arguments in any order
type unorderedMatcher struct {
	expected []string
}

func (u *unorderedMatcher) Matches(x interface{}) bool {
	args, ok := x.([]string)
	if !ok {
		return false
	}
	if len(args) != len(u.expected) {
		return false
	}
	expectedSet := make(map[string]bool)
	for _, s := range u.expected {
		expectedSet[s] = true
	}
	for _, s := range args {
		if !expectedSet[s] {
			return false
		}
	}
	return true
}

func (u *unorderedMatcher) String() string {
	return fmt.Sprintf("unordered: %v", u.expected)
}

func newUnorderedMatcher(expected ...string) gomock.Matcher {
	return &unorderedMatcher{expected: expected}
}

func TestQuadlet_Execute(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name       string
		action     *Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "ActionAdd success",
			action: &Action{
				Type: ActionAdd,
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil)
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "test-id-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)
				mockSystemdMgr.EXPECT().Start(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "ActionRemove success",
			action: &Action{
				Type: ActionRemove,
				Name: "test-app",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil)
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-app.service").Return(nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "test-id-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), "test-id-app.service").Return(nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).Times(1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).Times(1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).Times(1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).Times(1)
			},
			wantErr: false,
		},
		{
			name: "ActionUpdate success",
			action: &Action{
				Type: ActionUpdate,
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				// Remove phase
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil).Times(2)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-app.service").Return(nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "test-id-app.service", ActiveState: string(api.SystemdActiveStateInactive), LoadState: string(api.SystemdLoadStateLoaded)}}, nil).Times(2)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), "test-id-app.service").Return(nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()

				// Add phase
				mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil)
				mockSystemdMgr.EXPECT().Start(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "unsupported action type",
			action: &Action{
				Type: "invalid",
				Name: "test-app",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRW := fileio.NewMockReadWriter(ctrl)
			mockSystemdMgr := systemd.NewMockManager(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			tc.setupMocks(mockSystemdMgr, mockRW, mockExec)

			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			mockSystemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
			q := NewQuadlet(logger, mockRW, mockSystemdMgr, podman)

			err := q.Execute(context.Background(), tc.action)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestQuadlet_add(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name       string
		action     *Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "add container file",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "test-id-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)
				mockSystemdMgr.EXPECT().Start(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "add pod file with custom ServiceName",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-custom.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-custom.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "test-id-custom.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)
				mockSystemdMgr.EXPECT().Start(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "add pod file with default service name",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-mypod-pod.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-mypod-pod.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "test-id-mypod-pod.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)
				mockSystemdMgr.EXPECT().Start(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "add multiple services",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app1.service", "test-id-app2.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app1.service", "test-id-app2.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "test-id-app1.service", LoadState: string(api.SystemdLoadStateLoaded)}, {Unit: "test-id-app2.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)
				mockSystemdMgr.EXPECT().Start(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "list dependencies fails",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil, fmt.Errorf("list dependencies failed"))

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("[]", "", 0).AnyTimes()
			},
			wantErr: true,
		},
		{
			name: "start target fails",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				// First call during add
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "test-id-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)
				mockSystemdMgr.EXPECT().Start(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(fmt.Errorf("start failed"))
				mockSystemdMgr.EXPECT().Logs(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

				// Second call during cleanup (remove is called by defer)
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil).AnyTimes()
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-app.service").Return(nil).AnyTimes()
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).Return([]client.SystemDUnitListEntry{}, nil).AnyTimes()
				mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
			},
			wantErr: true,
		},
		{
			name: "service not loaded as target",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).
					Return([]client.SystemDUnitListEntry{}, nil)
				mockSystemdMgr.EXPECT().Logs(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

				mockRW.EXPECT().RemoveFile("/etc/systemd/system/test-id-flightctl-quadlet-app.target").Return(nil).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("[]", "", 0).AnyTimes()
			},
			wantErr: true,
		},
		{
			name: "add with artifact volumes",
			action: &Action{
				ID:   "app-with-vols",
				Name: "test-app",
				Path: "/test/path",
				Volumes: []Volume{
					{ID: "data", Reference: "artifact:mydata"},
				},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-with-vols-flightctl-quadlet-app.target").Return([]string{"app-with-vols-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-with-vols-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "app-with-vols-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:mydata")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "data")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "data")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "data")).Return("/var/lib/containers/storage/volumes/app-with-vols-data/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:mydata")).Return("", "", 0)

				mockSystemdMgr.EXPECT().Start(gomock.Any(), "app-with-vols-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "add with mixed image and artifact volumes",
			action: &Action{
				ID:   "app-mixed-vols",
				Name: "test-app",
				Path: "/test/path",
				Volumes: []Volume{
					{ID: "image-vol", Reference: "docker.io/nginx:latest"},
					{ID: "artifact-vol", Reference: "artifact:config"},
				},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-mixed-vols-flightctl-quadlet-app.target").Return([]string{"app-mixed-vols-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-mixed-vols-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "app-mixed-vols-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "docker.io/nginx:latest")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:config")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "artifact-vol")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "artifact-vol")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "artifact-vol")).Return("/var/lib/containers/storage/volumes/app-mixed-vols-artifact-vol/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:config")).Return("", "", 0)

				mockSystemdMgr.EXPECT().Start(gomock.Any(), "app-mixed-vols-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "add fails when artifact volume creation fails",
			action: &Action{
				ID:   "app-vol-fail",
				Name: "test-app",
				Path: "/test/path",
				Volumes: []Volume{
					{ID: "data", Reference: "artifact:badartifact"},
				},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				// First call during add
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-vol-fail-flightctl-quadlet-app.target").Return([]string{"app-vol-fail-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-vol-fail-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "app-vol-fail-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:badartifact")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "data")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "data")).Return("", "Error: creation failed", 1)

				// Second call during cleanup (remove is called by defer)
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-vol-fail-flightctl-quadlet-app.target").Return([]string{"app-vol-fail-app.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-vol-fail-flightctl-quadlet-app.target").Return(nil).AnyTimes()
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-vol-fail-app.service").Return(nil).AnyTimes()
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-vol-fail-app.service"}).Return([]client.SystemDUnitListEntry{}, nil).AnyTimes()
				mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRW := fileio.NewMockReadWriter(ctrl)
			mockSystemdMgr := systemd.NewMockManager(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			tc.setupMocks(mockSystemdMgr, mockRW, mockExec)

			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			mockSystemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			q := NewQuadlet(logger, mockRW, mockSystemdMgr, podman)

			err := q.add(context.Background(), tc.action)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestQuadlet_remove(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name       string
		action     *Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "remove with matching units",
			action: &Action{
				Name: "test-app",
				ID:   "app-123",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-123-flightctl-quadlet-app.target").Return([]string{"app-123-web.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-123-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-123-web.service").Return(nil)
				units := []client.SystemDUnitListEntry{
					{Unit: "app-123-web.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateInactive), SubState: "dead", Description: "Test service"},
				}
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-123-web.service"}).Return(units, nil)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), "app-123-web.service").Return(nil)
				// Verify that RemoveExclusions is called with both the service and target
				mockSystemdMgr.EXPECT().RemoveExclusions([]string{"app-123-web.service", "app-123-flightctl-quadlet-app.target"})

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
			},
			wantErr: false,
		},
		{
			name: "list dependencies fails on remove",
			action: &Action{
				Name: "test-app",
				ID:   "app-456",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-456-flightctl-quadlet-app.target").Return(nil, fmt.Errorf("list dependencies failed"))
			},
			wantErr: true,
		},
		{
			name: "StopTarget fails",
			action: &Action{
				Name: "test-app",
				ID:   "app-999",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-999-flightctl-quadlet-app.target").Return([]string{"app-999-web.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-999-flightctl-quadlet-app.target").Return(fmt.Errorf("stop failed"))
			},
			wantErr: true,
		},
		{
			name: "stop succeeds, one failed service, reset succeeds",
			action: &Action{
				Name: "test-app",
				ID:   "app-failed-1",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-failed-1-flightctl-quadlet-app.target").Return([]string{"app-failed-1-web.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-failed-1-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-failed-1-web.service").Return(nil)
				units := []client.SystemDUnitListEntry{
					{Unit: "app-failed-1-web.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateFailed), SubState: "failed", Description: "Test service"},
				}
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-failed-1-web.service"}).Return(units, nil)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), "app-failed-1-web.service").Return(nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
			},
			wantErr: false,
		},
		{
			name: "stop succeeds, multiple services, reset succeeds",
			action: &Action{
				Name: "test-app",
				ID:   "app-multi",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-multi-flightctl-quadlet-app.target").Return([]string{"app-multi-web.service", "app-multi-db.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-multi-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), newUnorderedMatcher("app-multi-web.service", "app-multi-db.service")).Return(nil)
				units := []client.SystemDUnitListEntry{
					{Unit: "app-multi-web.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateFailed), SubState: "failed", Description: "Web service"},
					{Unit: "app-multi-db.service", LoadState: "loaded", ActiveState: "inactive", SubState: "dead", Description: "DB service"},
				}
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), newUnorderedMatcher("app-multi-web.service", "app-multi-db.service")).Return(units, nil)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), newUnorderedMatcher("app-multi-web.service", "app-multi-db.service")).Return(nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
			},
			wantErr: false,
		},
		{
			name: "stop succeeds, reset-failed fails",
			action: &Action{
				Name: "test-app",
				ID:   "app-reset-fail",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-reset-fail-flightctl-quadlet-app.target").Return([]string{"app-reset-fail-web.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-reset-fail-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-reset-fail-web.service").Return(nil)
				units := []client.SystemDUnitListEntry{
					{Unit: "app-reset-fail-web.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateFailed), SubState: "failed", Description: "Test service"},
				}
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-reset-fail-web.service"}).Return(units, nil)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), "app-reset-fail-web.service").Return(fmt.Errorf("reset failed"))
			},
			wantErr: true,
		},
		{
			name: "stop succeeds, list-units fails",
			action: &Action{
				Name: "test-app",
				ID:   "app-list-fail",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-list-fail-flightctl-quadlet-app.target").Return([]string{"app-list-fail-web.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-list-fail-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-list-fail-web.service"}).Return(nil, fmt.Errorf("list failed"))
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRW := fileio.NewMockReadWriter(ctrl)
			mockSystemdMgr := systemd.NewMockManager(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			tc.setupMocks(mockSystemdMgr, mockRW, mockExec)

			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
			q := NewQuadlet(logger, mockRW, mockSystemdMgr, podman)

			err := q.remove(context.Background(), tc.action)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestQuadlet_update(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name       string
		action     *Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "update success",
			action: &Action{
				Type: ActionUpdate,
				Name: "test-app",
				Path: "/test/path",
				ID:   "app-123",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()

				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-123-flightctl-quadlet-app.target").Return([]string{"app-123-app.service"}, nil).Times(2)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-123-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-123-app.service").Return(nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-123-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "app-123-app.service", ActiveState: string(api.SystemdActiveStateInactive), LoadState: string(api.SystemdLoadStateLoaded)}}, nil).Times(2)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), "app-123-app.service").Return(nil)

				mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil)
				mockSystemdMgr.EXPECT().Start(gomock.Any(), "app-123-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "update fails on remove",
			action: &Action{
				Type: ActionUpdate,
				Name: "test-app",
				Path: "/test/path",
				ID:   "app-456",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-456-flightctl-quadlet-app.target").Return([]string{"app-456-app.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-456-flightctl-quadlet-app.target").Return(fmt.Errorf("stop failed"))
			},
			wantErr: true,
		},
		{
			name: "update fails on daemon reload",
			action: &Action{
				Type: ActionUpdate,
				Name: "test-app",
				Path: "/test/path",
				ID:   "app-789",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-789-flightctl-quadlet-app.target").Return([]string{}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-789-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{}).Return([]client.SystemDUnitListEntry{}, nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()

				mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(fmt.Errorf("reload failed"))
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRW := fileio.NewMockReadWriter(ctrl)
			mockSystemdMgr := systemd.NewMockManager(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			tc.setupMocks(mockSystemdMgr, mockRW, mockExec)

			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			mockSystemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
			q := NewQuadlet(logger, mockRW, mockSystemdMgr, podman)

			err := q.Execute(context.Background(), tc.action)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestQuadlet_ExecuteMultipleActions(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name       string
		actions    []*Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "mixed add, remove, update - correct ordering",
			actions: []*Action{
				{Type: ActionAdd, Name: "app1", ID: "app1-id", Path: "/path/app1"},
				{Type: ActionRemove, Name: "app2", ID: "app2-id"},
				{Type: ActionUpdate, Name: "app3", ID: "app3-id", Path: "/path/app3"},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("[]", "", 0).AnyTimes()

				gomock.InOrder(
					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app2-id-flightctl-quadlet-app.target").Return([]string{"app2-id-web.service"}, nil),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app2-id-flightctl-quadlet-app.target").Return(nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app2-id-web.service"}).Return([]client.SystemDUnitListEntry{}, nil),

					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app3-id-flightctl-quadlet-app.target").Return([]string{"app3-id-web.service"}, nil),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app3-id-flightctl-quadlet-app.target").Return(nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app3-id-web.service"}).Return([]client.SystemDUnitListEntry{}, nil),

					mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil),

					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app1-id-flightctl-quadlet-app.target").Return([]string{"app1-id-app1.service"}, nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app1-id-app1.service"}).
						Return([]client.SystemDUnitListEntry{{Unit: "app1-id-app1.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil),
					mockSystemdMgr.EXPECT().Start(gomock.Any(), "app1-id-flightctl-quadlet-app.target").Return(nil),

					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app3-id-flightctl-quadlet-app.target").Return([]string{"app3-id-app3.service"}, nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app3-id-app3.service"}).
						Return([]client.SystemDUnitListEntry{{Unit: "app3-id-app3.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil),
					mockSystemdMgr.EXPECT().Start(gomock.Any(), "app3-id-flightctl-quadlet-app.target").Return(nil),
				)
			},
			wantErr: false,
		},
		{
			name: "multiple removes then multiple adds",
			actions: []*Action{
				{Type: ActionAdd, Name: "new-app1", ID: "new1-id", Path: "/path/new1"},
				{Type: ActionAdd, Name: "new-app2", ID: "new2-id", Path: "/path/new2"},
				{Type: ActionRemove, Name: "old-app1", ID: "old1-id"},
				{Type: ActionRemove, Name: "old-app2", ID: "old2-id"},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("[]", "", 0).AnyTimes()

				gomock.InOrder(
					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "old1-id-flightctl-quadlet-app.target").Return([]string{"old1-id-old1.service"}, nil),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), "old1-id-flightctl-quadlet-app.target").Return(nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"old1-id-old1.service"}).Return([]client.SystemDUnitListEntry{}, nil),

					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "old2-id-flightctl-quadlet-app.target").Return([]string{"old2-id-old2.service"}, nil),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), "old2-id-flightctl-quadlet-app.target").Return(nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"old2-id-old2.service"}).Return([]client.SystemDUnitListEntry{}, nil),

					mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil),

					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "new1-id-flightctl-quadlet-app.target").Return([]string{"new1-id-new1.service"}, nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"new1-id-new1.service"}).
						Return([]client.SystemDUnitListEntry{{Unit: "new1-id-new1.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil),
					mockSystemdMgr.EXPECT().Start(gomock.Any(), "new1-id-flightctl-quadlet-app.target").Return(nil),

					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "new2-id-flightctl-quadlet-app.target").Return([]string{"new2-id-new2.service"}, nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"new2-id-new2.service"}).
						Return([]client.SystemDUnitListEntry{{Unit: "new2-id-new2.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil),
					mockSystemdMgr.EXPECT().Start(gomock.Any(), "new2-id-flightctl-quadlet-app.target").Return(nil),
				)
			},
			wantErr: false,
		},
		{
			name: "remove fails stops execution",
			actions: []*Action{
				{Type: ActionRemove, Name: "app1", ID: "app1-id"},
				{Type: ActionAdd, Name: "app2", ID: "app2-id", Path: "/path/app2"},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app1-id-flightctl-quadlet-app.target").Return([]string{"app1-id-app1.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app1-id-flightctl-quadlet-app.target").Return(fmt.Errorf("stop failed"))
			},
			wantErr: true,
		},
		{
			name: "daemon reload failure stops add phase",
			actions: []*Action{
				{Type: ActionRemove, Name: "app1", ID: "app1-id"},
				{Type: ActionAdd, Name: "app2", ID: "app2-id", Path: "/path/app2"},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("[]", "", 0).AnyTimes()

				gomock.InOrder(
					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app1-id-flightctl-quadlet-app.target").Return([]string{"app1-id-app1.service"}, nil),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app1-id-flightctl-quadlet-app.target").Return(nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app1-id-app1.service"}).Return([]client.SystemDUnitListEntry{}, nil),
					mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(fmt.Errorf("reload failed")),
				)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRW := fileio.NewMockReadWriter(ctrl)
			mockSystemdMgr := systemd.NewMockManager(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			tc.setupMocks(mockSystemdMgr, mockRW, mockExec)

			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			mockSystemdMgr.EXPECT().AddExclusions(gomock.Any()).AnyTimes()
			mockSystemdMgr.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
			q := NewQuadlet(logger, mockRW, mockSystemdMgr, podman)

			err := q.Execute(context.Background(), tc.actions...)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestQuadlet_ensureArtifactVolumes(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name      string
		action    *Action
		setupExec func(*executer.MockExecuter)
		setupRW   func(*fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name: "no volumes",
			action: &Action{
				ID:      "app-123",
				Name:    "test-app",
				Volumes: []Volume{},
			},
			setupExec: nil,
			wantErr:   false,
		},
		{
			name: "single artifact volume",
			action: &Action{
				ID:   "app-123",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:myartifact", ReclaimPolicy: api.Retain},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:myartifact")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol1")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol1")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol1")).Return("/var/lib/containers/storage/volumes/app-123-vol1/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:myartifact")).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "artifact volume with retain policy is not tracked",
			action: &Action{
				ID:   "app-123",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:myartifact", ReclaimPolicy: api.Retain},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:myartifact")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol1")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol1")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol1")).Return("/var/lib/containers/storage/volumes/app-123-vol1/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:myartifact")).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "existing retain artifact volume is reseeded",
			action: &Action{
				ID:   "app-keep",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:seed-data", ReclaimPolicy: api.Retain},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:seed-data")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol1")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol1")).Return("/var/lib/containers/storage/volumes/app-keep-vol1/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:seed-data")).Return("", "", 0)
			},
			setupRW: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().RemoveContents("/var/lib/containers/storage/volumes/app-keep-vol1/_data").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "multiple artifact volumes",
			action: &Action{
				ID:   "app-456",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:art1", ReclaimPolicy: api.Retain},
					{ID: "vol2", Reference: "artifact:art2", ReclaimPolicy: api.Retain},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:art1")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol1")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol1")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol1")).Return("/var/lib/containers/storage/volumes/app-456-vol1/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:art1")).Return("", "", 0)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:art2")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol2")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol2")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol2")).Return("/var/lib/containers/storage/volumes/app-456-vol2/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:art2")).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "image volume should be skipped",
			action: &Action{
				ID:   "app-789",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "docker.io/nginx:latest"},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "docker.io/nginx:latest")).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "mixed image and artifact volumes",
			action: &Action{
				ID:   "app-mixed",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "docker.io/nginx:latest"},
					{ID: "vol2", Reference: "artifact:mydata", ReclaimPolicy: api.Retain},
					{ID: "vol3", Reference: "quay.io/myimage:v1"},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "docker.io/nginx:latest")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:mydata")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol2")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol2")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol2")).Return("/var/lib/containers/storage/volumes/app-mixed-vol2/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:mydata")).Return("", "", 0)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "quay.io/myimage:v1")).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "CreateVolume fails",
			action: &Action{
				ID:   "app-fail",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:badartifact"},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:badartifact")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol1")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol1")).Return("", "Error: volume creation failed", 1)
			},
			wantErr: true,
		},
		{
			name: "ExtractArtifact fails",
			action: &Action{
				ID:   "app-extract-fail",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:badextract"},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("image", "exists", "artifact:badextract")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol1")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol1")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol1")).Return("/var/lib/containers/storage/volumes/app-extract-fail-vol1/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:badextract")).Return("", "Error: extraction failed", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "rm", "vol1")).Return("", "", 0)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockRW := fileio.NewMockReadWriter(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			if tc.setupExec != nil {
				tc.setupExec(mockExec)
			}
			if tc.setupRW != nil {
				tc.setupRW(mockRW)
			}

			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			q := NewQuadlet(logger, mockRW, nil, podman)

			err := q.ensureArtifactVolumes(context.Background(), tc.action)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}
