package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	ignv3types "github.com/coreos/ignition/v2/config/v3_4/types"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

type command struct {
	command string
	args    []string
}

func TestHookManager(t *testing.T) {
	testCases := []struct {
		name             string
		hooks            map[string]string
		current          *api.RenderedDeviceSpec
		desired          *api.RenderedDeviceSpec
		rebooted         bool
		expectedCommands []command
	}{
		{
			name:             "creating a file outside the default hooks' paths should trigger no action",
			hooks:            map[string]string{},
			current:          createRenderedDeviceSpec(map[string]string{}),
			desired:          createRenderedDeviceSpec(map[string]string{"/etc/systemd/user/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{},
		},
		{
			name:             "creating a file inside a default hook's path should trigger its default action",
			hooks:            map[string]string{},
			current:          createRenderedDeviceSpec(map[string]string{}),
			desired:          createRenderedDeviceSpec(map[string]string{"/etc/systemd/system/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{{"systemctl", []string{"daemon-reload"}}},
		},
		{
			name:             "creating a file whose path is being watched should trigger the action once",
			hooks:            map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookPathToFile},
			current:          createRenderedDeviceSpec(map[string]string{}),
			desired:          createRenderedDeviceSpec(map[string]string{"/etc/someservice/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{{"systemctl", []string{"restart", "someservice"}}},
		},
		{
			name:             "creating a file whose parent directory's path is being watched should trigger the action once",
			hooks:            map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookPathToDir},
			current:          createRenderedDeviceSpec(map[string]string{}),
			desired:          createRenderedDeviceSpec(map[string]string{"/etc/someservice/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{{"systemctl", []string{"restart", "someservice"}}},
		},
		{
			name:    "creating multiple files whose parent directory's path is being watched should trigger the action once",
			hooks:   map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookPathToDir},
			current: createRenderedDeviceSpec(map[string]string{}),
			desired: createRenderedDeviceSpec(map[string]string{
				"/etc/someservice/some.config":      "data:,content",
				"/etc/someservice/someother.config": "data:,content",
			}),
			rebooted:         false,
			expectedCommands: []command{{"systemctl", []string{"restart", "someservice"}}},
		},
		{
			name:             "actions with rebooted condition should run if the system rebooted during the update",
			hooks:            map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookRebootedCondition},
			current:          createRenderedDeviceSpec(map[string]string{}),
			desired:          createRenderedDeviceSpec(map[string]string{"/etc/someservice/some.config": "data:,content"}),
			rebooted:         true,
			expectedCommands: []command{{"echo", []string{"\"System was rebooted.\""}}},
		},
		{
			name:             "actions with rebooted condition should run if the system rebooted during the update",
			hooks:            map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookRebootedCondition},
			current:          createRenderedDeviceSpec(map[string]string{}),
			desired:          createRenderedDeviceSpec(map[string]string{"/etc/someservice/some.config": "data:,content"}),
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
			hookManager := NewManager(readWriter, mockExecuter, logger)

			expectExecCalls(mockExecuter, tc.expectedCommands)

			ctx, cancel := context.WithCancel(context.TODO())
			defer cancel()
			require.NoError(t, hookManager.OnAfterUpdating(ctx, tc.current, tc.desired, tc.rebooted))
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

func createRenderedDeviceSpec(files map[string]string) *api.RenderedDeviceSpec {
	ignFiles := []ignv3types.File{}
	for path, data := range files {
		ignFiles = append(ignFiles, ignv3types.File{
			Node: ignv3types.Node{Path: path},
			FileEmbedded1: ignv3types.FileEmbedded1{
				Contents: ignv3types.Resource{
					Source: lo.ToPtr(data),
				},
			},
		})
	}
	ignitionConfig := &ignv3types.Config{
		Ignition: ignv3types.Ignition{
			Version: ignv3types.MaxVersion.String(),
		},
		Storage: ignv3types.Storage{
			Files: ignFiles,
		},
	}
	marshalledIgnitionConfig, _ := json.Marshal(ignitionConfig)
	return &api.RenderedDeviceSpec{
		Config: lo.ToPtr(string(marshalledIgnitionConfig)),
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
