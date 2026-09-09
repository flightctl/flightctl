package hook

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
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

// TestHookManager verifies the hook manager's AfterUpdating flow with various hook configurations.
func TestHookManager(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name             string
		hooks            map[string]string
		current          *v1beta1.DeviceSpec
		desired          *v1beta1.DeviceSpec
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
			expectedCommands: []command{{"echo", []string{"System was rebooted."}}},
		},
		{
			name:             "actions with rebooted condition should run if the system rebooted during the update",
			hooks:            map[string]string{"/etc/flightctl/hooks.d/afterupdating/01-test.yaml": testHookRebootedCondition},
			current:          createDeviceSpec(require, map[string]string{}),
			desired:          createDeviceSpec(require, map[string]string{"/etc/someservice/some.config": "data:,content"}),
			rebooted:         false,
			expectedCommands: []command{{"echo", []string{"System was not rebooted."}}},
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

// createTempHooksDir builds a temp directory with default and custom hook YAML for update tests.
func createTempHooksDir(t *testing.T, hooks map[string]string) fileio.ReadWriter {
	tempDir := t.TempDir()
	readerWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)

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

// createDeviceSpec builds a DeviceSpec with the given file paths and inline content.
func createDeviceSpec(require *require.Assertions, fileMap map[string]string) *v1beta1.DeviceSpec {
	files := []v1beta1.FileSpec{}
	for path, data := range fileMap {
		files = append(files, v1beta1.FileSpec{
			Path:    path,
			Content: data,
		})
	}

	config, err := config.FilesToProviderSpec(files)
	require.NoError(err)

	return &v1beta1.DeviceSpec{
		Config: config,
	}
}

const testEnrollmentHookYAML = `
- run: echo "enrollment hook executed"
`

// TestHookManagerEnrollment verifies enrollment hook discovery, merge rules, and execution.
func TestHookManagerEnrollment(t *testing.T) {
	tests := []struct {
		name             string
		hooks            map[string]string
		hookType         string
		expectedCommands []command
	}{
		{
			name: "When BeforeEnrolling has hooks in image dir it should execute them",
			hooks: map[string]string{
				"/usr/lib/flightctl/hooks.d/beforeenrolling/01-test.yaml": testEnrollmentHookYAML,
			},
			hookType:         "before",
			expectedCommands: []command{{"echo", []string{"enrollment hook executed"}}},
		},
		{
			name: "When BeforeEnrolling has hooks in both dirs it should merge and execute",
			hooks: map[string]string{
				"/usr/lib/flightctl/hooks.d/beforeenrolling/01-test.yaml": testEnrollmentHookYAML,
				"/etc/flightctl/hooks.d/beforeenrolling/02-test.yaml":     testEnrollmentHookYAML,
			},
			hookType:         "before",
			expectedCommands: []command{{"echo", []string{"enrollment hook executed"}}, {"echo", []string{"enrollment hook executed"}}},
		},
		{
			name: "When AfterEnrolling has hooks in /etc only it should not load them (image-only)",
			hooks: map[string]string{
				"/etc/flightctl/hooks.d/afterenrolling/01-test.yaml": testEnrollmentHookYAML,
			},
			hookType:         "after",
			expectedCommands: []command{},
		},
		{
			name: "When AfterEnrolling has hooks in /usr/lib only it should load and execute",
			hooks: map[string]string{
				"/usr/lib/flightctl/hooks.d/afterenrolling/01-test.yaml": testEnrollmentHookYAML,
			},
			hookType:         "after",
			expectedCommands: []command{{"echo", []string{"enrollment hook executed"}}},
		},
		{
			name:             "When no enrollment hooks exist it should not error",
			hooks:            map[string]string{},
			hookType:         "before",
			expectedCommands: []command{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			readWriter := createEnrollmentHooksDir(t, tt.hooks)
			mockExec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			logger.SetLevel(logrus.DebugLevel)
			hookManager := NewManager(readWriter, mockExec, logger)
			expectEnrollmentExecCalls(mockExec, tt.expectedCommands)

			ctx, cancel := context.WithCancel(context.TODO())
			defer cancel()

			enrollCtx := &EnrollmentContext{
				DeviceName: "test-device",
				SystemInfo: map[string]interface{}{"os": "linux"},
				Labels:     map[string]string{"env": "test"},
			}

			var err error
			if tt.hookType == "before" {
				err = hookManager.OnBeforeEnrolling(ctx, enrollCtx)
			} else {
				err = hookManager.OnAfterEnrolling(ctx, enrollCtx)
			}
			require.NoError(err)
		})
	}
}

func TestReadHookLabelsRejectsOversizedFile(t *testing.T) {
	require := require.New(t)
	readWriter := createEnrollmentHooksDir(t, map[string]string{})
	oversized := strings.Repeat("a", MaxHookLabelsFileSize+1)
	require.NoError(readWriter.WriteFile(HookLabelsPath, []byte(oversized), 0600))

	hookManager := NewManager(readWriter, executer.NewCommonExecuter(), log.NewPrefixLogger("test"))
	impl, ok := hookManager.(*manager)
	require.True(ok)
	require.Nil(impl.readHookLabels())
}

// TestHookManagerEnrollmentContext verifies FLIGHTCTL_HOOK_CONTEXT is injected during hook execution.
func TestHookManagerEnrollmentContext(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hooks := map[string]string{
		"/usr/lib/flightctl/hooks.d/beforeenrolling/01-test.yaml": testEnrollmentHookYAML,
	}
	readWriter := createEnrollmentHooksDir(t, hooks)
	mockExec := executer.NewMockExecuter(ctrl)
	logger := log.NewPrefixLogger("test")
	logger.SetLevel(logrus.DebugLevel)
	hookManager := NewManager(readWriter, mockExec, logger)

	// Capture env vars passed to executor to verify FLIGHTCTL_HOOK_CONTEXT
	var capturedEnv []string
	mockExec.EXPECT().ExecuteWithBoundedOutputFromDir(gomock.Any(), "", "echo", []string{"enrollment hook executed"}, MaxEnrollmentHookActionOutput, gomock.Any()).DoAndReturn(
		func(ctx context.Context, workingDir, command string, args []string, _ int, env ...string) (string, string, int) {
			capturedEnv = env
			return "", "", 0
		}).Times(1)

	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()

	enrollCtx := &EnrollmentContext{
		DeviceName: "test-device",
		SystemInfo: map[string]interface{}{},
		Labels:     map[string]string{},
	}

	require.NoError(hookManager.OnBeforeEnrolling(ctx, enrollCtx))

	// Verify FLIGHTCTL_HOOK_CONTEXT is in the captured env vars
	found := false
	for _, env := range capturedEnv {
		if strings.HasPrefix(env, "FLIGHTCTL_HOOK_CONTEXT=") {
			found = true
			// Verify it's valid JSON
			jsonStr := strings.TrimPrefix(env, "FLIGHTCTL_HOOK_CONTEXT=")
			require.True(len(jsonStr) > 0, "FLIGHTCTL_HOOK_CONTEXT value should not be empty")

			// Verify the JSON contains the expected hook field
			require.Contains(jsonStr, `"hook":"BeforeEnrolling"`)
			require.Contains(jsonStr, `"deviceName":"test-device"`)

			// Verify the context file was written and matches the env var
			contextFilePath := readWriter.PathFor(HookContextPath)
			fileData, err := os.ReadFile(contextFilePath)
			require.NoError(err)
			require.Equal(jsonStr, string(fileData), "env var content should match file content")
			break
		}
	}
	require.True(found, "FLIGHTCTL_HOOK_CONTEXT env var should be set")
}

// createEnrollmentHooksDir builds a temp hooks.d tree for enrollment hook tests.
func createEnrollmentHooksDir(t *testing.T, hooks map[string]string) fileio.ReadWriter {
	t.Helper()
	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)

	// Create enrollment hook directories
	for _, dir := range []string{
		"/usr/lib/flightctl/hooks.d/beforeenrolling",
		"/usr/lib/flightctl/hooks.d/afterenrolling",
		"/etc/flightctl/hooks.d/beforeenrolling",
		"/etc/flightctl/hooks.d/afterenrolling",
	} {
		require.NoError(t, readWriter.MkdirAll(dir, 0755))
	}

	for fpath, content := range hooks {
		require.NoError(t, readWriter.WriteFile(fpath, []byte(content), 0600))
	}

	return readWriter
}

func TestOnBeforeEnrollingRecordsPerActionResults(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	multiActionYAML := `
- run: echo first
- run: echo second
`
	hooks := map[string]string{
		"/usr/lib/flightctl/hooks.d/beforeenrolling/01-test.yaml": multiActionYAML,
	}
	readWriter := createEnrollmentHooksDir(t, hooks)
	mockExec := executer.NewMockExecuter(ctrl)
	logger := log.NewPrefixLogger("test")
	logger.SetLevel(logrus.ErrorLevel)
	hookManager := NewManager(readWriter, mockExec, logger)

	mockExec.EXPECT().ExecuteWithBoundedOutputFromDir(gomock.Any(), "", "echo", []string{"first"}, MaxEnrollmentHookActionOutput, gomock.Any()).
		Return("ip=10.0.0.1", "", 0).Times(1)
	mockExec.EXPECT().ExecuteWithBoundedOutputFromDir(gomock.Any(), "", "echo", []string{"second"}, MaxEnrollmentHookActionOutput, gomock.Any()).
		Return("", "serial=ABC", 0).Times(1)

	enrollCtx := &EnrollmentContext{DeviceName: "test-device"}
	require.NoError(hookManager.OnBeforeEnrolling(context.Background(), enrollCtx))
	require.True(enrollCtx.Success)
	require.Len(enrollCtx.Actions, 2)
	require.Equal("/usr/lib/flightctl/hooks.d/beforeenrolling/01-test.yaml", enrollCtx.Actions[0].Source)
	require.Equal("echo first", enrollCtx.Actions[0].Command)
	require.Equal(0, enrollCtx.Actions[0].ExitCode)
	require.Equal("ip=10.0.0.1", enrollCtx.Actions[0].Output)
	require.Equal("/usr/lib/flightctl/hooks.d/beforeenrolling/01-test.yaml", enrollCtx.Actions[1].Source)
	require.Equal("echo second", enrollCtx.Actions[1].Command)
	require.Equal("serial=ABC", enrollCtx.Actions[1].Output)
}

func TestOnBeforeEnrollingRecordsFailedActionResult(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hooks := map[string]string{
		"/usr/lib/flightctl/hooks.d/beforeenrolling/01-test.yaml": `- run: /bin/false`,
	}
	readWriter := createEnrollmentHooksDir(t, hooks)
	mockExec := executer.NewMockExecuter(ctrl)
	logger := log.NewPrefixLogger("test")
	logger.SetLevel(logrus.ErrorLevel)
	hookManager := NewManager(readWriter, mockExec, logger)

	mockExec.EXPECT().ExecuteWithBoundedOutputFromDir(gomock.Any(), "", "/bin/false", []string{}, MaxEnrollmentHookActionOutput, gomock.Any()).
		Return("", "failed", 1).Times(1)

	enrollCtx := &EnrollmentContext{DeviceName: "test-device"}
	require.Error(hookManager.OnBeforeEnrolling(context.Background(), enrollCtx))
	require.False(enrollCtx.Success)
	require.Len(enrollCtx.Actions, 1)
	require.Equal("/usr/lib/flightctl/hooks.d/beforeenrolling/01-test.yaml", enrollCtx.Actions[0].Source)
	require.Equal("/bin/false", enrollCtx.Actions[0].Command)
	require.Equal(1, enrollCtx.Actions[0].ExitCode)
	require.Equal("failed", enrollCtx.Actions[0].Output)
}

// expectEnrollmentExecCalls configures bounded-output executor expectations for enrollment hooks.
func expectEnrollmentExecCalls(mockExecuter *executer.MockExecuter, expectedCommands []command) {
	if len(expectedCommands) > 0 {
		calls := make([]any, len(expectedCommands))
		for i, e := range expectedCommands {
			calls[i] = mockExecuter.EXPECT().ExecuteWithBoundedOutputFromDir(gomock.Any(), "", e.command, e.args, MaxEnrollmentHookActionOutput, gomock.Any()).DoAndReturn(
				func(ctx context.Context, workingDir, command string, args []string, _ int, env ...string) (string, string, int) {
					return "", "", 0
				}).Return("", "", 0).Times(1)
		}
		gomock.InOrder(calls...)
	} else {
		mockExecuter.EXPECT().ExecuteWithBoundedOutputFromDir(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, workingDir, command string, args []string, _ int, env ...string) (string, string, int) {
				return strings.Join(append([]string{command}, args...), " "), "", 0
			}).Times(0)
	}
}

// expectExecCalls configures the mock executor to expect the given commands in order.
func expectExecCalls(mockExecuter *executer.MockExecuter, expectedCommands []command) {
	if len(expectedCommands) > 0 {
		calls := make([]any, len(expectedCommands))
		for i, e := range expectedCommands {
			calls[i] = mockExecuter.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", e.command, e.args, gomock.Any()).DoAndReturn(
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
