package hook

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

type command struct {
	command string
	args    []string
}

func TestHookManager(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name             string
		hooks            map[string]string
		current          *api.DeviceSpec
		desired          *api.DeviceSpec
		rebooted         bool
		expectedCommands []command
	}{
		{
			name:             "creating a file outside the default hooks' paths should trigger no action",
			hooks:            map[string]string{},
			current:          createDeviceSpec(require, map[string]string{}),
			desired:          createDeviceSpec(require, map[string]string{"/etc/systemd/user/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{},
		},
		{
			name:             "creating a file inside a default hook's path should trigger its default action",
			hooks:            map[string]string{},
			current:          createDeviceSpec(require, map[string]string{}),
			desired:          createDeviceSpec(require, map[string]string{"/etc/systemd/system/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{{"systemctl", []string{"daemon-reload"}}},
		},
		{
			name:             "creating a file whose path is being watched should trigger the action once",
			hooks:            map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookPathToFile},
			current:          createDeviceSpec(require, map[string]string{}),
			desired:          createDeviceSpec(require, map[string]string{"/etc/someservice/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{{"systemctl", []string{"restart", "someservice"}}},
		},
		{
			name:             "creating a file whose parent directory's path is being watched should trigger the action once",
			hooks:            map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookPathToDir},
			current:          createDeviceSpec(require, map[string]string{}),
			desired:          createDeviceSpec(require, map[string]string{"/etc/someservice/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{{"systemctl", []string{"restart", "someservice"}}},
		},
		{
			name:    "creating multiple files whose parent directory's path is being watched should trigger the action once",
			hooks:   map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookPathToDir},
			current: createDeviceSpec(require, map[string]string{}),
			desired: createDeviceSpec(require, map[string]string{
				"/etc/someservice/some.config":      "data:,content",
				"/etc/someservice/someother.config": "data:,content",
			}),
			rebooted:         false,
			expectedCommands: []command{{"systemctl", []string{"restart", "someservice"}}},
		},
		{
			name:             "actions with rebooted condition should run if the system rebooted during the update",
			hooks:            map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookRebootedCondition},
			current:          createDeviceSpec(require, map[string]string{}),
			desired:          createDeviceSpec(require, map[string]string{"/etc/someservice/some.config": "data:,content"}),
			rebooted:         true,
			expectedCommands: []command{{"echo", []string{"\"System was rebooted.\""}}},
		},
		{
			name:             "actions with rebooted condition should run if the system rebooted during the update",
			hooks:            map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookRebootedCondition},
			current:          createDeviceSpec(require, map[string]string{}),
			desired:          createDeviceSpec(require, map[string]string{"/etc/someservice/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{{"echo", []string{"\"System was not rebooted.\""}}},
		},
	}

	for i := range testCases {
		tc := testCases[i]
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			readWriter := createTempHooksDir(t, tc.hooks)
			mockExecuter := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			logger.SetLevel(logrus.DebugLevel)
			hookManager := NewManager(readWriter, mockExecuter, logger)
			expectExecCalls(mockExecuter, tc.expectedCommands)

			ctx, cancel := context.WithCancel(context.TODO())
			defer cancel()
			require.NoError(hookManager.OnAfterUpdating(ctx, tc.current, tc.desired, tc.rebooted))
		})
	}
}

const testHookPathToFile = `
- if:
  - path: /etc/someservice/some.config
    op: [created]
  run: systemctl restart someservice
`

const testHookPathToDir = `
- if:
  - path: /etc/someservice/
    op: [created]
  run: systemctl restart someservice
`

const testHookRebootedCondition = `
- if:
  - rebooted == true
  run: echo "System was rebooted."
- if:
  - rebooted == false
  run: echo "System was not rebooted."
`

func createTempHooksDir(t *testing.T, hooks map[string]string) fileio.ReadWriter {
	readerWriter := fileio.NewReadWriter(fileio.WithTestRootDir(t.TempDir()))

	util.Must(readerWriter.MkdirAll("/usr/lib/flightctl/hooks.d/afterupdating", 0755))
	util.Must(readerWriter.MkdirAll("/etc/flightctl/hooks.d/afterupdating", 0755))

	_, thisFile, _, _ := runtime.Caller(0)
	srcHooksFile := filepath.Join(filepath.Dir(thisFile), "../../../../packaging/hooks.d/afterupdating/00-default.yaml")
	dstHooksFile := readerWriter.PathFor("/usr/lib/flightctl/hooks.d/afterupdating/00-default.yaml")
	data, err := os.ReadFile(srcHooksFile)
	util.Must(err)
	util.Must(os.WriteFile(dstHooksFile, data, 0600))

	for filepath, content := range hooks {
		util.Must(readerWriter.WriteFile(filepath, []byte(content), 0600))
	}

	return readerWriter
}

func createDeviceSpec(require *require.Assertions, fileMap map[string]string) *api.DeviceSpec {
	files := []v1alpha1.FileSpec{}
	for path, data := range fileMap {
		files = append(files, v1alpha1.FileSpec{
			Path:    path,
			Content: data,
		})
	}

	config, err := config.FilesToProviderSpec(files)
	require.NoError(err)

	return &api.DeviceSpec{
		Config: config,
	}
}

func expectExecCalls(mockExecuter *executer.MockExecuter, expectedCommands []command) {
	if len(expectedCommands) > 0 {
		calls := make([]any, len(expectedCommands))
		for i, e := range expectedCommands {
			calls[i] = mockExecuter.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", e.command, e.args).DoAndReturn(
				func(ctx context.Context, workingDir, command string, args []string, env ...string) (string, string, int) {
					return "", "", 0
				}).Return("", "", 0).Times(1)
		}
		gomock.InOrder(calls...)
	} else {
		mockExecuter.EXPECT().ExecuteWithContextFromDir(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, workingDir, command string, args []string, env ...string) (string, string, int) {
				return strings.Join(append([]string{command}, args...), " "), "", 0
			}).Times(0)
	}
}
