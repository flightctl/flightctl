package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
<<<<<<< HEAD
	testutil "github.com/flightctl/flightctl/test/util"
=======
>>>>>>> 33a1cb77 (fix)
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m *mockDirEntry) Name() string               { return m.name }
func (m *mockDirEntry) IsDir() bool                { return m.isDir }
func (m *mockDirEntry) Type() fs.FileMode          { return 0 }
func (m *mockDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

<<<<<<< HEAD
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

=======
>>>>>>> 33a1cb77 (fix)
func TestQuadlet_Execute(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name          string
		action        *Action
		setupMocks    func(*executer.MockExecuter, *fileio.MockReadWriter)
		setupServices func(*Quadlet)
		wantErr       bool
	}{
		{
			name: "ActionAdd success",
			action: &Action{
				Type: ActionAdd,
				Name: "test-app",
				Path: "/test/path",
				ID:   "test-id",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{}, nil)
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
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
<<<<<<< HEAD
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0).Times(1)

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).Times(1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).Times(1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).Times(1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).Times(1)
=======
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
>>>>>>> 33a1cb77 (fix)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["test-id"] = []string{}
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
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{}, nil)
<<<<<<< HEAD

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
=======
>>>>>>> 33a1cb77 (fix)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["test-id"] = []string{}
			},
			wantErr: false,
		},
		{
			name: "unsupported action type",
			action: &Action{
				Type: "invalid",
				Name: "test-app",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockExec := executer.NewMockExecuter(ctrl)
			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMocks(mockExec, mockRW)

			systemd := client.NewSystemd(mockExec)
<<<<<<< HEAD
			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			q := NewQuadlet(logger, mockRW, systemd, podman)
=======
			logger := log.NewPrefixLogger("test")
			q := NewQuadlet(logger, mockRW, systemd)
>>>>>>> 33a1cb77 (fix)

			if tc.setupServices != nil {
				tc.setupServices(q)
			}

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
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name       string
		action     *Action
		setupMocks func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr    bool
	}{
		{
			name: "add container file",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "app.container", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/app.container").Return([]byte("[Container]\nImage=nginx\n"), nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "start", "app.service").Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "add pod file with ServiceName",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "app.pod", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/app.pod").Return([]byte("[Pod]\nServiceName=custom.service\n"), nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "start", "custom.service").Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "add pod file without ServiceName",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "mypod.pod", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/mypod.pod").Return([]byte("[Pod]\nName=mypod\n"), nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "start", "mypod-pod.service").Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "add target file",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "app.target", isDir: false},
				}, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "start", "app.target").Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "add mixed files with correct ordering",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "app1.container", isDir: false},
					&mockDirEntry{name: "app.target", isDir: false},
					&mockDirEntry{name: "app2.container", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/app1.container").Return([]byte("[Container]\nImage=nginx\n"), nil)
				mockRW.EXPECT().ReadFile("/test/path/app2.container").Return([]byte("[Container]\nImage=redis\n"), nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "start", "app.target", "app1.service", "app2.service").Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "skip directories and unknown files",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "subdir", isDir: true},
					&mockDirEntry{name: "readme.txt", isDir: false},
					&mockDirEntry{name: "app.container", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/app.container").Return([]byte("[Container]\nImage=nginx\n"), nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "start", "app.service").Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name: "daemon reload fails",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "reload failed", 1)
			},
			wantErr: true,
		},
		{
			name: "ReadDir fails",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return(nil, fmt.Errorf("directory not found"))
			},
			wantErr: true,
		},
		{
			name: "StartUnits fails",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "app.container", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/app.container").Return([]byte("[Container]\nImage=nginx\n"), nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "start", "app.service").Return("", "start failed", 1)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockExec := executer.NewMockExecuter(ctrl)
			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMocks(mockExec, mockRW)

			systemd := client.NewSystemd(mockExec)
<<<<<<< HEAD
			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			q := NewQuadlet(logger, mockRW, systemd, podman)
=======
			logger := log.NewPrefixLogger("test")
			q := NewQuadlet(logger, mockRW, systemd)
>>>>>>> 33a1cb77 (fix)

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
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name          string
		action        *Action
		setupMocks    func(*executer.MockExecuter, *fileio.MockReadWriter)
		setupServices func(*Quadlet)
		wantErr       bool
	}{
		{
			name: "remove with matching units, no failed services",
			action: &Action{
				Name: "test-app",
				ID:   "app-123",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "stop", "app-123-web.service").Return("", "", 0)
				units := []client.SystemDUnitListEntry{
					{Unit: "app-123-web.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateInactive), SubState: "dead", Description: "Test service"},
				}
				unitBytes, _ := json.Marshal(units)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-units", "--all", "--output", "json", "--", "app-123-web.service").Return(string(unitBytes), "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
<<<<<<< HEAD

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
=======
>>>>>>> 33a1cb77 (fix)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-123"] = []string{"app-123-web.service"}
			},
			wantErr: false,
		},
		{
			name: "remove with no matching units",
			action: &Action{
				Name: "test-app",
				ID:   "app-456",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
			},
			wantErr: false,
		},
		{
			name: "StopUnits fails",
			action: &Action{
				Name: "test-app",
				ID:   "app-999",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "stop", "app-999-web.service").Return("", "stop failed", 1)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-999"] = []string{"app-999-web.service"}
			},
			wantErr: true,
		},
		{
			name: "stop succeeds, one failed service, reset succeeds",
			action: &Action{
				Name: "test-app",
				ID:   "app-failed-1",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "stop", "app-failed-1-web.service").Return("", "", 0)
				units := []client.SystemDUnitListEntry{
					{Unit: "app-failed-1-web.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateFailed), SubState: "failed", Description: "Test service"},
				}
				unitBytes, _ := json.Marshal(units)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-units", "--all", "--output", "json", "--", "app-failed-1-web.service").Return(string(unitBytes), "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "reset-failed", "app-failed-1-web.service").Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
<<<<<<< HEAD

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
=======
>>>>>>> 33a1cb77 (fix)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-failed-1"] = []string{"app-failed-1-web.service"}
			},
			wantErr: false,
		},
		{
			name: "stop succeeds, multiple services, some failed, reset succeeds",
			action: &Action{
				Name: "test-app",
				ID:   "app-multi",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "stop", "app-multi-web.service", "app-multi-db.service").Return("", "", 0)
				units := []client.SystemDUnitListEntry{
					{Unit: "app-multi-web.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateFailed), SubState: "failed", Description: "Web service"},
					{Unit: "app-multi-db.service", LoadState: "loaded", ActiveState: "inactive", SubState: "dead", Description: "DB service"},
				}
				unitBytes, _ := json.Marshal(units)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-units", "--all", "--output", "json", "--", "app-multi-web.service", "app-multi-db.service").Return(string(unitBytes), "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "reset-failed", "app-multi-web.service").Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
<<<<<<< HEAD

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
=======
>>>>>>> 33a1cb77 (fix)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-multi"] = []string{"app-multi-web.service", "app-multi-db.service"}
			},
			wantErr: false,
		},
		{
			name: "stop succeeds, failed services found, reset-failed fails",
			action: &Action{
				Name: "test-app",
				ID:   "app-reset-fail",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "stop", "app-reset-fail-web.service").Return("", "", 0)
				units := []client.SystemDUnitListEntry{
					{Unit: "app-reset-fail-web.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateFailed), SubState: "failed", Description: "Test service"},
				}
				unitBytes, _ := json.Marshal(units)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-units", "--all", "--output", "json", "--", "app-reset-fail-web.service").Return(string(unitBytes), "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "reset-failed", "app-reset-fail-web.service").Return("", "reset failed", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
<<<<<<< HEAD

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
=======
>>>>>>> 33a1cb77 (fix)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-reset-fail"] = []string{"app-reset-fail-web.service"}
			},
			wantErr: false,
		},
		{
			name: "stop succeeds, list-units fails",
			action: &Action{
				Name: "test-app",
				ID:   "app-list-fail",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "stop", "app-list-fail-web.service").Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-units", "--all", "--output", "json", "--", "app-list-fail-web.service").Return("", "list failed", 1)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
<<<<<<< HEAD

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
=======
>>>>>>> 33a1cb77 (fix)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-list-fail"] = []string{"app-list-fail-web.service"}
			},
			wantErr: false,
		},
		{
			name: "daemon reload fails after stop",
			action: &Action{
				Name: "test-app",
				ID:   "app-000",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "stop", "app-000-db.service").Return("", "", 0)
				units := []client.SystemDUnitListEntry{
					{Unit: "app-000-db.service", LoadState: "loaded", ActiveState: string(api.SystemdActiveStateInactive), SubState: "dead", Description: "Test service"},
				}
				unitBytes, _ := json.Marshal(units)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-units", "--all", "--output", "json", "--", "app-000-db.service").Return(string(unitBytes), "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "reload failed", 1)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-000"] = []string{"app-000-db.service"}
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockExec := executer.NewMockExecuter(ctrl)
			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMocks(mockExec, mockRW)

			systemd := client.NewSystemd(mockExec)
<<<<<<< HEAD
			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			q := NewQuadlet(logger, mockRW, systemd, podman)
=======
			logger := log.NewPrefixLogger("test")
			q := NewQuadlet(logger, mockRW, systemd)
>>>>>>> 33a1cb77 (fix)

			if tc.setupServices != nil {
				tc.setupServices(q)
			}

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
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name          string
		action        *Action
		setupMocks    func(*executer.MockExecuter, *fileio.MockReadWriter)
		setupServices func(*Quadlet)
		wantErr       bool
	}{
		{
			name: "update success",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "app-123",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "app.container", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/app.container").Return([]byte("[Container]\nImage=nginx\n"), nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "start", "app.service").Return("", "", 0)
<<<<<<< HEAD

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
=======
>>>>>>> 33a1cb77 (fix)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-123"] = []string{}
			},
			wantErr: false,
		},
		{
			name: "update fails on remove",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "app-456",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "stop", "app-456.service").Return("", "stop failed", 1)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-456"] = []string{"app-456.service"}
			},
			wantErr: true,
		},
		{
			name: "update fails on add",
			action: &Action{
				Name: "test-app",
				Path: "/test/path",
				ID:   "app-789",
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "daemon-reload").Return("", "reload failed", 1)
<<<<<<< HEAD

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("", "", 0).AnyTimes()
=======
>>>>>>> 33a1cb77 (fix)
			},
			setupServices: func(q *Quadlet) {
				q.actionServices["app-789"] = []string{}
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockExec := executer.NewMockExecuter(ctrl)
			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMocks(mockExec, mockRW)

			systemd := client.NewSystemd(mockExec)
<<<<<<< HEAD
			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, mockRW, testutil.NewPollConfig())
			logger := log.NewPrefixLogger("test")
			q := NewQuadlet(logger, mockRW, systemd, podman)
=======
			logger := log.NewPrefixLogger("test")
			q := NewQuadlet(logger, mockRW, systemd)
>>>>>>> 33a1cb77 (fix)

			if tc.setupServices != nil {
				tc.setupServices(q)
			}

			err := q.update(context.Background(), tc.action)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestQuadlet_collectTargets(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name       string
		path       string
		setupMocks func(*fileio.MockReadWriter)
		want       []string
		wantErr    bool
	}{
		{
			name: "container files generate service names",
			path: "/test/path",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "web.container", isDir: false},
					&mockDirEntry{name: "db.container", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/web.container").Return([]byte("[Container]\nImage=nginx\n"), nil)
				mockRW.EXPECT().ReadFile("/test/path/db.container").Return([]byte("[Container]\nImage=postgres\n"), nil)
			},
			want:    []string{"web.service", "db.service"},
			wantErr: false,
		},
		{
			name: "pod files with ServiceName",
			path: "/test/path",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "mypod.pod", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/mypod.pod").Return([]byte("[Pod]\nServiceName=custom-pod.service\n"), nil)
			},
			want:    []string{"custom-pod.service"},
			wantErr: false,
		},
		{
			name: "pod files without ServiceName",
			path: "/test/path",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "mypod.pod", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/mypod.pod").Return([]byte("[Pod]\nName=mypod\n"), nil)
			},
			want:    []string{"mypod-pod.service"},
			wantErr: false,
		},
		{
			name: "target files preserved",
			path: "/test/path",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "app.target", isDir: false},
				}, nil)
			},
			want:    []string{"app.target"},
			wantErr: false,
		},
		{
			name: "mixed files with correct ordering",
			path: "/test/path",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "web.container", isDir: false},
					&mockDirEntry{name: "app.target", isDir: false},
					&mockDirEntry{name: "db.container", isDir: false},
					&mockDirEntry{name: "other.target", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/web.container").Return([]byte("[Container]\nImage=nginx\n"), nil)
				mockRW.EXPECT().ReadFile("/test/path/db.container").Return([]byte("[Container]\nImage=postgres\n"), nil)
			},
			want:    []string{"app.target", "other.target", "web.service", "db.service"},
			wantErr: false,
		},
		{
			name: "skip directories and unknown extensions",
			path: "/test/path",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "subdir", isDir: true},
					&mockDirEntry{name: "readme.txt", isDir: false},
					&mockDirEntry{name: "config.yaml", isDir: false},
					&mockDirEntry{name: "app.container", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/app.container").Return([]byte("[Container]\nImage=nginx\n"), nil)
			},
			want:    []string{"app.service"},
			wantErr: false,
		},
		{
			name: "ReadDir fails",
			path: "/test/path",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadDir("/test/path").Return(nil, fmt.Errorf("directory not found"))
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "serviceName fails for pod",
			path: "/test/path",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadDir("/test/path").Return([]fs.DirEntry{
					&mockDirEntry{name: "mypod.pod", isDir: false},
				}, nil)
				mockRW.EXPECT().ReadFile("/test/path/mypod.pod").Return(nil, fmt.Errorf("read failed"))
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMocks(mockRW)

			logger := log.NewPrefixLogger("test")
			q := &Quadlet{
				rw:  mockRW,
				log: logger,
			}

			got, err := q.collectTargets(tc.path)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
				require.Equal(tc.want, got)
			}
		})
	}
}

func TestQuadlet_serviceName(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name        string
		file        string
		section     string
		defaultName string
		setupMocks  func(*fileio.MockReadWriter)
		want        string
		wantErr     bool
	}{
		{
			name:        "pod with ServiceName key",
			file:        "mypod.pod",
			section:     "Pod",
			defaultName: "mypod-pod.service",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadFile("mypod.pod").Return([]byte("[Pod]\nServiceName=custom.service\n"), nil)
			},
			want:    "custom.service",
			wantErr: false,
		},
		{
			name:        "pod without ServiceName key",
			file:        "mypod.pod",
			section:     "Pod",
			defaultName: "mypod-pod.service",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadFile("mypod.pod").Return([]byte("[Pod]\nName=mypod\n"), nil)
			},
			want:    "mypod-pod.service",
			wantErr: false,
		},
		{
			name:        "ReadFile fails",
			file:        "mypod.pod",
			section:     "Pod",
			defaultName: "mypod-pod.service",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadFile("mypod.pod").Return(nil, fmt.Errorf("file not found"))
			},
			want:    "",
			wantErr: true,
		},
		{
			name:        "invalid INI format",
			file:        "mypod.pod",
			section:     "Pod",
			defaultName: "mypod-pod.service",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadFile("mypod.pod").Return([]byte("invalid ini content\n[[["), nil)
			},
			want:    "",
			wantErr: true,
		},
		{
			name:        "missing section",
			file:        "mypod.pod",
			section:     "Pod",
			defaultName: "mypod-pod.service",
			setupMocks: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().ReadFile("mypod.pod").Return([]byte("[Container]\nImage=nginx\n"), nil)
			},
			want:    "",
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMocks(mockRW)

			logger := log.NewPrefixLogger("test")
			q := &Quadlet{
				rw:  mockRW,
				log: logger,
			}

			got, err := q.serviceName(tc.file, tc.section, tc.defaultName)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
				require.Equal(tc.want, got)
			}
		})
	}
}
