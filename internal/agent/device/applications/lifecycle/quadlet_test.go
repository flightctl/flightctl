package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/opencontainers/go-digest"
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

func expectLoadedTarget(m *systemd.MockManager, target string) *gomock.Call {
	return m.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{target}).Return(
		[]client.SystemDUnitListEntry{{Unit: target, LoadState: string(api.SystemdLoadStateLoaded)}}, nil)
}

func expectNotLoadedTarget(m *systemd.MockManager, target string) *gomock.Call {
	return m.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{target}).Return([]client.SystemDUnitListEntry{}, nil)
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
		action     Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "ActionAdd success",
			action: Action{
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
			action: Action{
				Type: ActionRemove,
				Name: "test-app",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "test-id-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-app.service").Return(nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "test-id-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), "test-id-app.service").Return(nil)
				mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil)

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
			action: Action{
				Type: ActionUpdate,
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				// Remove phase
				expectLoadedTarget(mockSystemdMgr, "test-id-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil).Times(2)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "test-id-app.service").Return(nil)
				mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil)
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
				mockSystemdMgr.EXPECT().Start(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "unsupported action type",
			action: Action{
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

			var systemdFactory systemd.ManagerFactory = func(user api.Username) (systemd.Manager, error) {
				return mockSystemdMgr, nil
			}

			var podmanFactory client.PodmanFactory = func(user api.Username) (*client.Podman, error) {
				return podman, nil
			}

			var rwFactory fileio.ReadWriterFactory = func(username api.Username) (fileio.ReadWriter, error) {
				return mockRW, nil
			}

			q := NewQuadlet(logger, rwFactory, systemdFactory, podmanFactory)

			err := q.Execute(context.Background(), Actions{tc.action})
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestWhenQuadletAddsTheSameArtifactVolumeItShouldPreserveItsContents(t *testing.T) {
	const (
		application = "test-app"
		appID       = "test-id"
		volumeID    = "test-volume"
		reference   = "quay.io/acme/data:v1"
		createdAt   = "2026-10-12T12:00:00Z"
		digestValue = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	logger := log.NewPrefixLogger("test")
	storeRoot := t.TempDir()
	storeRW := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(storeRoot)), fileio.NewWriter(fileio.WithWriterRootDir(storeRoot)))
	store := deltastatus.New(storeRW, "/var/lib/flightctl", logger)
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: "spec"}))
	lifetime := store.ApplicationLifetime(application)
	require.NoError(t, store.RecordApplicationVolumeImage(application, lifetime, volumeID, createdAt, deltastatus.ApplicationImage{
		Image: reference, Owner: string(api.RootUsername), OCIType: string(dependency.OCITypePodmanArtifact), Digest: digestValue,
	}))

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockSystemd := systemd.NewMockManager(ctrl)
	mockSystemd.EXPECT().DaemonReload(gomock.Any()).Return(nil)
	mockSystemd.EXPECT().ListDependencies(gomock.Any(), appID+"-flightctl-quadlet-app.target").Return([]string{appID + "-app.service"}, nil)
	mockSystemd.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{appID + "-app.service"}).Return(
		[]client.SystemDUnitListEntry{{Unit: appID + "-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil,
	)
	mockSystemd.EXPECT().Start(gomock.Any(), appID+"-flightctl-quadlet-app.target").Return(nil)
	mockSystemd.EXPECT().AddExclusions(appID+"-app.service", appID+"-flightctl-quadlet-app.target")
	mockExec := executer.NewMockExecuter(ctrl)
	inspection, err := json.Marshal(client.PodmanVolumeInspect{Name: volumeID, Driver: "local", CreatedAt: createdAt})
	require.NoError(t, err)
	var commands [][]string
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, args ...string) (string, string, int) {
			commands = append(commands, append([]string(nil), args...))
			if len(args) >= 4 && args[0] == "volume" && args[1] == "inspect" && args[len(args)-1] == "{{json .}}" {
				return string(inspection), "", 0
			}
			if len(args) >= 4 && args[0] == "volume" && args[1] == "inspect" && args[len(args)-1] == "{{.Mountpoint}}" {
				return "/volume-data", "", 0
			}
			if len(args) == 1 && args[0] == "--version" {
				return "podman version 5.5.0", "", 0
			}
			if len(args) >= 2 && args[0] == "artifact" && args[1] == "inspect" {
				return fmt.Sprintf(`{"Digest":%q}`, digestValue), "", 0
			}
			if len(args) >= 2 && args[0] == "volume" && args[1] == "exists" {
				return "", "", 0
			}
			return "", "", 0
		},
	).AnyTimes()
	mockRW := fileio.NewMockReadWriter(ctrl)
	countingRW := &removeContentsCountingReadWriter{ReadWriter: mockRW}
	podman := client.NewPodman(logger, mockExec, mockRW, testutil.NewPollConfig())
	q := NewQuadlet(
		logger,
		func(api.Username) (fileio.ReadWriter, error) { return countingRW, nil },
		func(api.Username) (systemd.Manager, error) { return mockSystemd, nil },
		func(api.Username) (*client.Podman, error) { return podman, nil },
		WithDeltaStatusStore(store),
	)

	err = q.Execute(context.Background(), Actions{{
		Type: ActionAdd, Name: application, ID: appID, Path: "/test/path", User: api.RootUsername, Lifetime: lifetime,
		Volumes: []Volume{{ID: volumeID, Reference: reference, OCIType: string(dependency.OCITypePodmanArtifact)}},
	}})
	require.NoError(t, err)
	require.Zero(t, countingRW.removeContentsCalls)
	for _, args := range commands {
		require.False(t, len(args) >= 2 && args[0] == "artifact" && args[1] == "extract", "unexpected extraction: %v", args)
	}
}

func TestWhenQuadletAddsAnArtifactVolumeWithANewLocalDigestItShouldReextract(t *testing.T) {
	const (
		application = "test-app"
		appID       = "test-id"
		volumeID    = "test-volume"
		reference   = "quay.io/acme/data:v1"
		createdAt   = "2026-10-12T12:00:00Z"
		oldDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		newDigest   = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	logger := log.NewPrefixLogger("test")
	newDigestValue, err := digest.Parse(newDigest)
	require.NoError(t, err)
	storeRoot := t.TempDir()
	storeRW := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(storeRoot)), fileio.NewWriter(fileio.WithWriterRootDir(storeRoot)))
	store := deltastatus.New(storeRW, "/var/lib/flightctl", logger)
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: "spec"}))
	lifetime := store.ApplicationLifetime(application)
	require.NoError(t, store.RecordApplicationVolumeImage(application, lifetime, volumeID, createdAt, deltastatus.ApplicationImage{
		Image: reference, Owner: string(api.RootUsername), OCIType: string(dependency.OCITypePodmanArtifact), Digest: oldDigest,
	}))

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockSystemd := systemd.NewMockManager(ctrl)
	mockSystemd.EXPECT().DaemonReload(gomock.Any()).Return(nil)
	mockSystemd.EXPECT().ListDependencies(gomock.Any(), appID+"-flightctl-quadlet-app.target").Return([]string{appID + "-app.service"}, nil)
	mockSystemd.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{appID + "-app.service"}).Return(
		[]client.SystemDUnitListEntry{{Unit: appID + "-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil,
	)
	mockSystemd.EXPECT().Start(gomock.Any(), appID+"-flightctl-quadlet-app.target").Return(nil)
	mockSystemd.EXPECT().AddExclusions(appID+"-app.service", appID+"-flightctl-quadlet-app.target")
	mockExec := executer.NewMockExecuter(ctrl)
	inspection, err := json.Marshal(client.PodmanVolumeInspect{Name: volumeID, Driver: "local", CreatedAt: createdAt})
	require.NoError(t, err)
	var commands [][]string
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, args ...string) (string, string, int) {
			commands = append(commands, append([]string(nil), args...))
			if len(args) >= 4 && args[0] == "volume" && args[1] == "inspect" && args[len(args)-1] == "{{json .}}" {
				return string(inspection), "", 0
			}
			if len(args) >= 4 && args[0] == "volume" && args[1] == "inspect" && args[len(args)-1] == "{{.Mountpoint}}" {
				return "/volume-data", "", 0
			}
			if len(args) == 1 && args[0] == "--version" {
				return "podman version 5.5.0", "", 0
			}
			if len(args) >= 3 && args[0] == "artifact" && args[1] == "inspect" {
				return fmt.Sprintf(`{"Digest":%q}`, newDigest), "", 0
			}
			return "", "", 0
		},
	).AnyTimes()
	mockRW := fileio.NewMockReadWriter(ctrl)
	countingRW := &removeContentsCountingReadWriter{ReadWriter: mockRW}
	podman := client.NewPodman(logger, mockExec, mockRW, testutil.NewPollConfig())
	q := NewQuadlet(
		logger,
		func(api.Username) (fileio.ReadWriter, error) { return countingRW, nil },
		func(api.Username) (systemd.Manager, error) { return mockSystemd, nil },
		func(api.Username) (*client.Podman, error) { return podman, nil },
		WithDeltaStatusStore(store),
	)

	err = q.Execute(context.Background(), Actions{{
		Type: ActionAdd, Name: application, ID: appID, Path: "/test/path", User: api.RootUsername, Lifetime: lifetime,
		Volumes: []Volume{{ID: volumeID, Reference: reference, OCIType: string(dependency.OCITypePodmanArtifact)}},
	}})
	require.NoError(t, err)
	require.Equal(t, 1, countingRW.removeContentsCalls)
	var extracted bool
	for _, args := range commands {
		if len(args) == 4 && args[0] == "artifact" && args[1] == "extract" {
			require.Equal(t, newDigestValue.Encoded(), args[2])
			extracted = true
		}
	}
	require.True(t, extracted)
	binding, found := store.ApplicationVolumeBinding(application, lifetime, volumeID)
	require.True(t, found)
	require.Equal(t, newDigest, binding.Image.Digest)
}

type removeContentsCountingReadWriter struct {
	fileio.ReadWriter
	removeContentsCalls int
}

func (rw *removeContentsCountingReadWriter) RemoveContents(string) error {
	rw.removeContentsCalls++
	return nil
}

func TestQuadlet_add(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name       string
		action     Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "add container file",
			action: Action{
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
			action: Action{
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
			action: Action{
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
			action: Action{
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
			action: Action{
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
			action: Action{
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
				expectLoadedTarget(mockSystemdMgr, "test-id-flightctl-quadlet-app.target")
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
			action: Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "test-id-flightctl-quadlet-app.target").Return([]string{"test-id-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).
					Return([]client.SystemDUnitListEntry{}, nil)
				mockSystemdMgr.EXPECT().Logs(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

				mockRW.EXPECT().ReadDir(gomock.Any()).Return(nil, fmt.Errorf("dir not found")).AnyTimes()
				mockRW.EXPECT().RemoveFile("/etc/systemd/system/test-id-flightctl-quadlet-app.target").Return(nil).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("[]", "", 0).AnyTimes()
			},
			wantErr: true,
		},
		{
			name: "add with artifact volumes",
			action: Action{
				ID:   "app-with-vols",
				Name: "test-app",
				Path: "/test/path",
				Volumes: []Volume{
					{ID: "data", Reference: "artifact:mydata", OCIType: "PodmanArtifact"},
				},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-with-vols-flightctl-quadlet-app.target").Return([]string{"app-with-vols-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-with-vols-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "app-with-vols-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)

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
			action: Action{
				ID:   "app-mixed-vols",
				Name: "test-app",
				Path: "/test/path",
				Volumes: []Volume{
					{ID: "image-vol", Reference: "docker.io/nginx:latest", OCIType: "PodmanImage"},
					{ID: "artifact-vol", Reference: "artifact:config", OCIType: "PodmanArtifact"},
				},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-mixed-vols-flightctl-quadlet-app.target").Return([]string{"app-mixed-vols-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-mixed-vols-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "app-mixed-vols-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)

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
			action: Action{
				ID:   "app-vol-fail",
				Name: "test-app",
				Path: "/test/path",
				Volumes: []Volume{
					{ID: "data", Reference: "artifact:badartifact", OCIType: "PodmanArtifact"},
				},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				// First call during add
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-vol-fail-flightctl-quadlet-app.target").Return([]string{"app-vol-fail-app.service"}, nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-vol-fail-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "app-vol-fail-app.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "data")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "data")).Return("", "Error: creation failed", 1)

				// Second call during cleanup (remove is called by defer)
				expectLoadedTarget(mockSystemdMgr, "app-vol-fail-flightctl-quadlet-app.target")
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

			var systemdFactory systemd.ManagerFactory = func(user api.Username) (systemd.Manager, error) {
				return mockSystemdMgr, nil
			}

			var podmanFactory client.PodmanFactory = func(user api.Username) (*client.Podman, error) {
				return podman, nil
			}

			var rwFactory fileio.ReadWriterFactory = func(username api.Username) (fileio.ReadWriter, error) {
				return mockRW, nil
			}
			q := NewQuadlet(logger, rwFactory, systemdFactory, podmanFactory)

			err := q.add(context.Background(), tc.action, mockSystemdMgr)
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
		action     Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "remove with matching units",
			action: Action{
				Name: "test-app",
				ID:   "app-123",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-123-flightctl-quadlet-app.target")
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
			action: Action{
				Name: "test-app",
				ID:   "app-456",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-456-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-456-flightctl-quadlet-app.target").Return(nil, fmt.Errorf("list dependencies failed"))
			},
			wantErr: true,
		},
		{
			name: "StopTarget fails",
			action: Action{
				Name: "test-app",
				ID:   "app-999",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-999-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-999-flightctl-quadlet-app.target").Return([]string{"app-999-web.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-999-flightctl-quadlet-app.target").Return(fmt.Errorf("stop failed"))
			},
			wantErr: true,
		},
		{
			name: "stop succeeds, one failed service, reset succeeds",
			action: Action{
				Name: "test-app",
				ID:   "app-failed-1",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-failed-1-flightctl-quadlet-app.target")
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
			action: Action{
				Name: "test-app",
				ID:   "app-multi",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-multi-flightctl-quadlet-app.target")
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
			action: Action{
				Name: "test-app",
				ID:   "app-reset-fail",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-reset-fail-flightctl-quadlet-app.target")
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
			action: Action{
				Name: "test-app",
				ID:   "app-list-fail",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-list-fail-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-list-fail-flightctl-quadlet-app.target").Return([]string{"app-list-fail-web.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-list-fail-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-list-fail-web.service"}).Return(nil, fmt.Errorf("list failed"))
			},
			wantErr: true,
		},
		{
			name: "removes image-backed volumes on app removal",
			action: Action{
				Name: "test-app",
				ID:   "app-img-vol",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-img-vol-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-img-vol-flightctl-quadlet-app.target").Return([]string{"app-img-vol-web.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-img-vol-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-img-vol-web.service").Return(nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-img-vol-web.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "app-img-vol-web.service", LoadState: "loaded"}}, nil)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), "app-img-vol-web.service").Return(nil)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "ls")).
					Return(`[{"Name":"app-img-vol-html"},{"Name":"app-img-vol-data"}]`, "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-img-vol-html", "--format", "{{.Driver}}")).
					Return("image", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-img-vol-data", "--format", "{{.Driver}}")).
					Return("local", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-img-vol-data", "--format", "{{.Options.type}}")).
					Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "rm", "app-img-vol-html")).
					Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "no image-backed volumes to remove",
			action: Action{
				Name: "test-app",
				ID:   "app-no-vol",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectNotLoadedTarget(mockSystemdMgr, "app-no-vol-flightctl-quadlet-app.target")

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "ls")).
					Return("[]", "", 0)
			},
			wantErr: false,
		},
		{
			name: "skips non-image volumes during cleanup",
			action: Action{
				Name: "test-app",
				ID:   "app-local-vol",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectNotLoadedTarget(mockSystemdMgr, "app-local-vol-flightctl-quadlet-app.target")

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "ls")).
					Return(`[{"Name":"app-local-vol-data"}]`, "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-local-vol-data", "--format", "{{.Driver}}")).
					Return("local", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-local-vol-data", "--format", "{{.Options.type}}")).
					Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "volume inspect failure is non-fatal",
			action: Action{
				Name: "test-app",
				ID:   "app-inspect-fail",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectNotLoadedTarget(mockSystemdMgr, "app-inspect-fail-flightctl-quadlet-app.target")

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "ls")).
					Return(`[{"Name":"app-inspect-fail-gone"}]`, "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-inspect-fail-gone", "--format", "{{.Driver}}")).
					Return("", "Error: no such volume", 1)
			},
			wantErr: false,
		},
		{
			name: "When target is not loaded it should still clear the target exclusion",
			action: Action{
				Name: "test-app",
				ID:   "app-excl-unloaded",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectNotLoadedTarget(mockSystemdMgr, "app-excl-unloaded-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().RemoveExclusions([]string{"app-excl-unloaded-flightctl-quadlet-app.target"})

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
			},
			wantErr: false,
		},
		{
			name: "When target is loaded with no dependencies it should clear the target exclusion",
			action: Action{
				Name: "test-app",
				ID:   "app-excl-empty",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-excl-empty-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-excl-empty-flightctl-quadlet-app.target").Return([]string{}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-excl-empty-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().RemoveExclusions([]string{"app-excl-empty-flightctl-quadlet-app.target"})

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
			},
			wantErr: false,
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
			var systemdFactory systemd.ManagerFactory = func(user api.Username) (systemd.Manager, error) {
				return mockSystemdMgr, nil
			}

			var podmanFactory client.PodmanFactory = func(user api.Username) (*client.Podman, error) {
				return podman, nil
			}

			var rwFactory fileio.ReadWriterFactory = func(username api.Username) (fileio.ReadWriter, error) {
				return mockRW, nil
			}

			q := NewQuadlet(logger, rwFactory, systemdFactory, podmanFactory)

			err := q.remove(context.Background(), tc.action, mockSystemdMgr)
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
		action     Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "update success",
			action: Action{
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

				expectLoadedTarget(mockSystemdMgr, "app-123-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-123-flightctl-quadlet-app.target").Return([]string{"app-123-app.service"}, nil).Times(2)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-123-flightctl-quadlet-app.target").Return(nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-123-app.service").Return(nil)
				mockSystemdMgr.EXPECT().DaemonReload(gomock.Any()).Return(nil)
				mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app-123-app.service"}).
					Return([]client.SystemDUnitListEntry{{Unit: "app-123-app.service", ActiveState: string(api.SystemdActiveStateInactive), LoadState: string(api.SystemdLoadStateLoaded)}}, nil).Times(2)
				mockSystemdMgr.EXPECT().ResetFailed(gomock.Any(), "app-123-app.service").Return(nil)

				mockSystemdMgr.EXPECT().Start(gomock.Any(), "app-123-flightctl-quadlet-app.target").Return(nil)
			},
			wantErr: false,
		},
		{
			name: "update fails on remove",
			action: Action{
				Type: ActionUpdate,
				Name: "test-app",
				Path: "/test/path",
				ID:   "app-456",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app-456-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app-456-flightctl-quadlet-app.target").Return([]string{"app-456-app.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app-456-flightctl-quadlet-app.target").Return(fmt.Errorf("stop failed"))
			},
			wantErr: true,
		},
		{
			name: "update fails on daemon reload",
			action: Action{
				Type: ActionUpdate,
				Name: "test-app",
				Path: "/test/path",
				ID:   "app-789",
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				// When the target is not loaded, we skip Stop and go directly
				// to cleanResources for idempotent removal
				expectNotLoadedTarget(mockSystemdMgr, "app-789-flightctl-quadlet-app.target")

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
			var systemdFactory systemd.ManagerFactory = func(user api.Username) (systemd.Manager, error) {
				return mockSystemdMgr, nil
			}

			var podmanFactory client.PodmanFactory = func(user api.Username) (*client.Podman, error) {
				return podman, nil
			}

			var rwFactory fileio.ReadWriterFactory = func(username api.Username) (fileio.ReadWriter, error) {
				return mockRW, nil
			}

			q := NewQuadlet(logger, rwFactory, systemdFactory, podmanFactory)

			err := q.Execute(context.Background(), Actions{tc.action})
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestQuadletUpdateRemovesImageVolumeBeforeStartingUpdatedUnit(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockSystemd := systemd.NewMockManager(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)
	mockExec := executer.NewMockExecuter(ctrl)
	logger := log.NewPrefixLogger("test")
	const target = "app-id-flightctl-quadlet-app.target"
	const workload = "app-id-web.service"
	const volume = "app-id-data"

	expectLoadedTarget(mockSystemd, target)
	removeDependencies := mockSystemd.EXPECT().ListDependencies(gomock.Any(), target).Return([]string{workload}, nil)
	stopTarget := mockSystemd.EXPECT().Stop(gomock.Any(), target).Return(nil)
	listStoppedWorkload := mockSystemd.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{workload}).Return(
		[]client.SystemDUnitListEntry{{Unit: workload, LoadState: string(api.SystemdLoadStateLoaded)}}, nil,
	)
	stopWorkload := mockSystemd.EXPECT().Stop(gomock.Any(), workload).Return(nil)
	resetWorkload := mockSystemd.EXPECT().ResetFailed(gomock.Any(), workload).Return(nil)
	removeExclusions := mockSystemd.EXPECT().RemoveExclusions(gomock.Any())
	imageVolumeList := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "ls")).Return(
		`[{"Name":"app-id-data"}]`, "", 0,
	)
	imageVolumeDriver := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", volume, "--format", "{{.Driver}}")).Return("image", "", 0).After(imageVolumeList)
	removeOldImageVolume := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "rm", volume).Return("", "", 0).After(imageVolumeDriver)
	reload := mockSystemd.EXPECT().DaemonReload(gomock.Any()).Return(nil)
	addDependencies := mockSystemd.EXPECT().ListDependencies(gomock.Any(), target).Return([]string{workload}, nil)
	loadUpdatedWorkload := mockSystemd.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{workload}).Return(
		[]client.SystemDUnitListEntry{{Unit: workload, LoadState: string(api.SystemdLoadStateLoaded)}}, nil,
	)
	startUpdatedApplication := mockSystemd.EXPECT().Start(gomock.Any(), target).Return(nil)
	addExclusions := mockSystemd.EXPECT().AddExclusions(gomock.Any())

	gomock.InOrder(
		removeDependencies,
		stopTarget,
		listStoppedWorkload,
		stopWorkload,
		resetWorkload,
		removeExclusions,
		imageVolumeList,
		imageVolumeDriver,
		removeOldImageVolume,
		reload,
		addDependencies,
		loadUpdatedWorkload,
		startUpdatedApplication,
		addExclusions,
	)

	for _, command := range [][]string{{"network"}, {"pod"}, {"ps"}, {"stop"}, {"rm", "--filter"}} {
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher(command...)).Return("[]", "", 0).AnyTimes()
	}
	q := NewQuadlet(
		logger,
		func(api.Username) (fileio.ReadWriter, error) { return mockRW, nil },
		func(api.Username) (systemd.Manager, error) { return mockSystemd, nil },
		func(api.Username) (*client.Podman, error) {
			return client.NewPodman(logger, mockExec, mockRW, testutil.NewPollConfig()), nil
		},
	)
	err := q.Execute(context.Background(), Actions{{
		Type: ActionUpdate,
		Name: "app",
		ID:   "app-id",
		Path: "/path/app",
		Volumes: []Volume{{
			ID:        volume,
			Reference: "quay.io/acme/data:v2",
			OCIType:   "PodmanImage",
		}},
	}})
	require.NoError(t, err)
}

func TestQuadlet_ExecuteMultipleActions(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name       string
		actions    []Action
		setupMocks func(*systemd.MockManager, *fileio.MockReadWriter, *executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "mixed add, remove, update - correct ordering",
			actions: []Action{
				{Type: ActionAdd, Name: "app1", ID: "app1-id", Path: "/path/app1"},
				{Type: ActionRemove, Name: "app2", ID: "app2-id"},
				{Type: ActionUpdate, Name: "app3", ID: "app3-id", Path: "/path/app3"},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("[]", "", 0).AnyTimes()

				gomock.InOrder(
					expectLoadedTarget(mockSystemdMgr, "app2-id-flightctl-quadlet-app.target"),
					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app2-id-flightctl-quadlet-app.target").Return([]string{"app2-id-web.service"}, nil),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app2-id-flightctl-quadlet-app.target").Return(nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"app2-id-web.service"}).Return([]client.SystemDUnitListEntry{}, nil),

					expectLoadedTarget(mockSystemdMgr, "app3-id-flightctl-quadlet-app.target"),
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
			actions: []Action{
				{Type: ActionAdd, Name: "new-app1", ID: "new1-id", Path: "/path/new1"},
				{Type: ActionAdd, Name: "new-app2", ID: "new2-id", Path: "/path/new2"},
				{Type: ActionRemove, Name: "old-app1", ID: "old1-id"},
				{Type: ActionRemove, Name: "old-app2", ID: "old2-id"},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("[]", "", 0).AnyTimes()

				gomock.InOrder(
					expectLoadedTarget(mockSystemdMgr, "old1-id-flightctl-quadlet-app.target"),
					mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "old1-id-flightctl-quadlet-app.target").Return([]string{"old1-id-old1.service"}, nil),
					mockSystemdMgr.EXPECT().Stop(gomock.Any(), "old1-id-flightctl-quadlet-app.target").Return(nil),
					mockSystemdMgr.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"old1-id-old1.service"}).Return([]client.SystemDUnitListEntry{}, nil),

					expectLoadedTarget(mockSystemdMgr, "old2-id-flightctl-quadlet-app.target"),
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
			actions: []Action{
				{Type: ActionRemove, Name: "app1", ID: "app1-id"},
				{Type: ActionAdd, Name: "app2", ID: "app2-id", Path: "/path/app2"},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				expectLoadedTarget(mockSystemdMgr, "app1-id-flightctl-quadlet-app.target")
				mockSystemdMgr.EXPECT().ListDependencies(gomock.Any(), "app1-id-flightctl-quadlet-app.target").Return([]string{"app1-id-app1.service"}, nil)
				mockSystemdMgr.EXPECT().Stop(gomock.Any(), "app1-id-flightctl-quadlet-app.target").Return(fmt.Errorf("stop failed"))
			},
			wantErr: true,
		},
		{
			name: "daemon reload failure stops add phase",
			actions: []Action{
				{Type: ActionRemove, Name: "app1", ID: "app1-id"},
				{Type: ActionAdd, Name: "app2", ID: "app2-id", Path: "/path/app2"},
			},
			setupMocks: func(mockSystemdMgr *systemd.MockManager, mockRW *fileio.MockReadWriter, mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("[]", "", 0).AnyTimes()

				gomock.InOrder(
					expectLoadedTarget(mockSystemdMgr, "app1-id-flightctl-quadlet-app.target"),
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
			var systemdFactory systemd.ManagerFactory = func(user api.Username) (systemd.Manager, error) {
				return mockSystemdMgr, nil
			}

			var podmanFactory client.PodmanFactory = func(user api.Username) (*client.Podman, error) {
				return podman, nil
			}

			var rwFactory fileio.ReadWriterFactory = func(username api.Username) (fileio.ReadWriter, error) {
				return mockRW, nil
			}

			q := NewQuadlet(logger, rwFactory, systemdFactory, podmanFactory)

			err := q.Execute(context.Background(), tc.actions)
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
		action    Action
		setupExec func(*executer.MockExecuter)
		setupRW   func(*fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name: "no volumes",
			action: Action{
				ID:      "app-123",
				Name:    "test-app",
				Volumes: []Volume{},
			},
			setupExec: nil,
			wantErr:   false,
		},
		{
			name: "single artifact volume",
			action: Action{
				ID:   "app-123",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:myartifact", OCIType: "PodmanArtifact", ReclaimPolicy: api.Retain},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
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
			action: Action{
				ID:   "app-123",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:myartifact", OCIType: "PodmanArtifact", ReclaimPolicy: api.Retain},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
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
			action: Action{
				ID:   "app-keep",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:seed-data", OCIType: "PodmanArtifact", ReclaimPolicy: api.Retain},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol1")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "vol1", "--format", "{{json .}}").Return(`{"Name":"vol1","Driver":"local"}`, "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "vol1", "--format", "{{.Mountpoint}}").Return("/var/lib/containers/storage/volumes/app-keep-vol1/_data", "", 0)
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
			action: Action{
				ID:   "app-456",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:art1", OCIType: "PodmanArtifact", ReclaimPolicy: api.Retain},
					{ID: "vol2", Reference: "artifact:art2", OCIType: "PodmanArtifact", ReclaimPolicy: api.Retain},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol1")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol1")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol1")).Return("/var/lib/containers/storage/volumes/app-456-vol1/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:art1")).Return("", "", 0)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol2")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol2")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol2")).Return("/var/lib/containers/storage/volumes/app-456-vol2/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:art2")).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "image volume should be skipped",
			action: Action{
				ID:   "app-789",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "docker.io/nginx:latest", OCIType: "PodmanImage"},
				},
			},
			setupExec: nil,
			wantErr:   false,
		},
		{
			name: "mixed image and artifact volumes",
			action: Action{
				ID:   "app-mixed",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "docker.io/nginx:latest", OCIType: "PodmanImage"},
					{ID: "vol2", Reference: "artifact:mydata", OCIType: "PodmanArtifact", ReclaimPolicy: api.Retain},
					{ID: "vol3", Reference: "quay.io/myimage:v1", OCIType: "PodmanImage"},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol2")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol2")).Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol2")).Return("/var/lib/containers/storage/volumes/app-mixed-vol2/_data", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:mydata")).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "CreateVolume fails",
			action: Action{
				ID:   "app-fail",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:badartifact", OCIType: "PodmanArtifact"},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "exists", "vol1")).Return("", "", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "create", "vol1")).Return("", "Error: volume creation failed", 1)
			},
			wantErr: true,
		},
		{
			name: "ExtractArtifact fails",
			action: Action{
				ID:   "app-extract-fail",
				Name: "test-app",
				Volumes: []Volume{
					{ID: "vol1", Reference: "artifact:badextract", OCIType: "PodmanArtifact"},
				},
			},
			setupExec: func(mockExec *executer.MockExecuter) {
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

			var podmanFactory client.PodmanFactory = func(user api.Username) (*client.Podman, error) {
				return podman, nil
			}

			var rwFactory fileio.ReadWriterFactory = func(username api.Username) (fileio.ReadWriter, error) {
				return mockRW, nil
			}

			q := NewQuadlet(logger, rwFactory, nil, podmanFactory)

			err := q.ensureArtifactVolumes(context.Background(), tc.action)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestQuadlet_LifecycleHandler(t *testing.T) {
	const testAppID = "test-id"
	const testTarget = "test-id-flightctl-quadlet-app.target"

	testAction := Action{
		ID:   testAppID,
		Name: "test-app",
	}

	testCases := []struct {
		name       string
		operation  func(q *Quadlet, action Action) error
		setupMocks func(*systemd.MockManager)
		wantErr    bool
	}{
		{
			name: "When Stop is called it should stop the target and reset failed dependent services",
			operation: func(q *Quadlet, action Action) error {
				return q.Stop(context.Background(), action)
			},
			setupMocks: func(m *systemd.MockManager) {
				expectLoadedTarget(m, testTarget)
				m.EXPECT().ListDependencies(gomock.Any(), testTarget).Return([]string{"test-id-app.service"}, nil)
				m.EXPECT().Stop(gomock.Any(), testTarget).Return(nil)
				units := []client.SystemDUnitListEntry{
					{Unit: "test-id-app.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateInactive), SubState: "dead", Description: "Test service"},
				}
				m.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).Return(units, nil)
				m.EXPECT().Stop(gomock.Any(), "test-id-app.service").Return(nil)
				m.EXPECT().ResetFailed(gomock.Any(), "test-id-app.service").Return(nil)
			},
		},
		{
			name: "When Stop fails it should propagate the error",
			operation: func(q *Quadlet, action Action) error {
				return q.Stop(context.Background(), action)
			},
			setupMocks: func(m *systemd.MockManager) {
				expectLoadedTarget(m, testTarget)
				m.EXPECT().ListDependencies(gomock.Any(), testTarget).Return([]string{"test-id-app.service"}, nil)
				m.EXPECT().Stop(gomock.Any(), testTarget).Return(fmt.Errorf("unit not found"))
			},
			wantErr: true,
		},
		{
			name: "When Stop finds the target not loaded it should succeed without stopping",
			operation: func(q *Quadlet, action Action) error {
				return q.Stop(context.Background(), action)
			},
			setupMocks: func(m *systemd.MockManager) {
				expectNotLoadedTarget(m, testTarget)
			},
		},
		{
			name: "When Stop finds a loaded target with no dependencies it should stop only the target",
			operation: func(q *Quadlet, action Action) error {
				return q.Stop(context.Background(), action)
			},
			setupMocks: func(m *systemd.MockManager) {
				expectLoadedTarget(m, testTarget)
				m.EXPECT().ListDependencies(gomock.Any(), testTarget).Return(nil, nil)
				m.EXPECT().Stop(gomock.Any(), testTarget).Return(nil)
			},
		},
		{
			name: "When Stop reset-failed fails it should propagate the error",
			operation: func(q *Quadlet, action Action) error {
				return q.Stop(context.Background(), action)
			},
			setupMocks: func(m *systemd.MockManager) {
				expectLoadedTarget(m, testTarget)
				m.EXPECT().ListDependencies(gomock.Any(), testTarget).Return([]string{"test-id-app.service"}, nil)
				m.EXPECT().Stop(gomock.Any(), testTarget).Return(nil)
				units := []client.SystemDUnitListEntry{
					{Unit: "test-id-app.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateFailed), SubState: "failed", Description: "Test service"},
				}
				m.EXPECT().ListUnitsByMatchPattern(gomock.Any(), []string{"test-id-app.service"}).Return(units, nil)
				m.EXPECT().Stop(gomock.Any(), "test-id-app.service").Return(nil)
				m.EXPECT().ResetFailed(gomock.Any(), "test-id-app.service").Return(fmt.Errorf("reset failed"))
			},
			wantErr: true,
		},
		{
			name: "When Start is called it should start the target unit without daemon reload",
			operation: func(q *Quadlet, action Action) error {
				return q.Start(context.Background(), action)
			},
			setupMocks: func(m *systemd.MockManager) {
				m.EXPECT().Start(gomock.Any(), testTarget).Return(nil)
			},
		},
		{
			name: "When Start fails it should propagate the error",
			operation: func(q *Quadlet, action Action) error {
				return q.Start(context.Background(), action)
			},
			setupMocks: func(m *systemd.MockManager) {
				m.EXPECT().Start(gomock.Any(), testTarget).Return(fmt.Errorf("unit not found"))
			},
			wantErr: true,
		},
		{
			name: "When Restart is called it should restart the target unit",
			operation: func(q *Quadlet, action Action) error {
				return q.Restart(context.Background(), action)
			},
			setupMocks: func(m *systemd.MockManager) {
				m.EXPECT().Restart(gomock.Any(), testTarget).Return(nil)
			},
		},
		{
			name: "When Restart fails it should propagate the error",
			operation: func(q *Quadlet, action Action) error {
				return q.Restart(context.Background(), action)
			},
			setupMocks: func(m *systemd.MockManager) {
				m.EXPECT().Restart(gomock.Any(), testTarget).Return(fmt.Errorf("unit restart failed"))
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockSystemdMgr := systemd.NewMockManager(ctrl)
			tc.setupMocks(mockSystemdMgr)

			var systemdFactory systemd.ManagerFactory = func(user api.Username) (systemd.Manager, error) {
				return mockSystemdMgr, nil
			}

			q := NewQuadlet(log.NewPrefixLogger("test"), nil, systemdFactory, nil)
			err := tc.operation(q, testAction)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}

	t.Run("When systemd factory fails Stop should return error", func(t *testing.T) {
		require := require.New(t)
		var systemdFactory systemd.ManagerFactory = func(user api.Username) (systemd.Manager, error) {
			return nil, fmt.Errorf("factory error")
		}
		q := NewQuadlet(log.NewPrefixLogger("test"), nil, systemdFactory, nil)
		err := q.Stop(context.Background(), testAction)
		require.Error(err)
	})
}

func TestWhenArtifactBindingPersistenceFailsItShouldKeepThePreparedVolume(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	logger := log.NewPrefixLogger("test")
	stateRW := fileio.NewMockReadWriter(ctrl)
	stateRW.EXPECT().ReadFile(gomock.Any()).Return(nil, os.ErrNotExist)
	stateRW.EXPECT().MkdirAll(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	failWrites := false
	stateRW.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(string, []byte, fs.FileMode, ...fileio.FileOption) error {
			if failWrites {
				return fmt.Errorf("state filesystem is full")
			}
			return nil
		}).AnyTimes()
	store := deltastatus.New(stateRW, "/state", logger)
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"app": "spec"}))
	lifetime := store.ApplicationLifetime("app")
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	const reference = "quay.io/acme/data:v1"
	const digestValue = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observedDigest, err := digest.Parse(digestValue)
	require.NoError(err)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", reference).Return(`{"Digest":"`+digestValue+`"}`, "", 0)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "exists", "data").Return("", "", 1)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "create", "data", "--label", client.QuadletProjectLabelKey+"=app-id").Return("data", "", 0)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "data", "--format", "{{.Mountpoint}}").Return("/volume-data", "", 0)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "extract", observedDigest.Encoded(), "/volume-data").DoAndReturn(
		func(context.Context, string, ...string) (string, string, int) {
			require.NoError(rw.WriteFile("/volume-data/payload", []byte("installed contents"), 0o600))
			failWrites = true
			return "", "", 0
		})
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "data", "--format", "{{json .}}").Return(`{"Name":"data","Driver":"local","CreatedAt":"created"}`, "", 0)
	// Removing the prepared volume would discard successfully installed content.
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "rm", "data").DoAndReturn(
		func(context.Context, string, ...string) (string, string, int) {
			require.NoError(rw.RemoveAll("/volume-data"))
			return "", "", 0
		}).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("", "", 0).AnyTimes()
	systemdClient := systemd.NewMockManager(ctrl)
	systemdClient.EXPECT().RemoveExclusions(gomock.Any()).AnyTimes()
	systemdClient.EXPECT().DaemonReload(gomock.Any()).Return(nil).AnyTimes()
	systemdClient.EXPECT().ListDependencies(gomock.Any(), "app-id-flightctl-quadlet-app.target").Return([]string{"app-id-web.service"}, nil).AnyTimes()
	systemdClient.EXPECT().ListUnitsByMatchPattern(gomock.Any(), gomock.Any()).Return([]client.SystemDUnitListEntry{{Unit: "app-id-web.service", LoadState: string(api.SystemdLoadStateLoaded)}}, nil).AnyTimes()
	systemdClient.EXPECT().Start(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	systemdClient.EXPECT().AddExclusions(gomock.Any(), gomock.Any()).AnyTimes()
	podman := client.NewPodman(logger, exec, rw, testutil.NewPollConfig())
	q := NewQuadlet(logger, func(api.Username) (fileio.ReadWriter, error) { return rw, nil },
		func(api.Username) (systemd.Manager, error) { return systemdClient, nil },
		func(api.Username) (*client.Podman, error) { return podman, nil }, WithDeltaStatusStore(store))
	require.NoError(q.Execute(t.Context(), Actions{{Type: ActionAdd, Name: "app", ID: "app-id", Path: "/app", User: api.RootUsername, Lifetime: lifetime,
		Volumes: []Volume{{ID: "data", Reference: reference, OCIType: string(dependency.OCITypePodmanArtifact)}}}}))
	contents, err := rw.ReadFile("/volume-data/payload")
	require.NoError(err)
	require.Equal("installed contents", string(contents))
}
