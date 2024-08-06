package hook

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"go.uber.org/mock/gomock"
)

func TestHookManager(t *testing.T) {
	testCases := []struct {
		name     string
		expected int32
		before   bool
		after    bool
		ops      []v1alpha1.FileOperation
	}{
		{
			name: "unwatched path",
		},
		{
			name:     "file is on default compose watched directory [create] - before only",
			before:   true,
			after:    false,
			ops:      []v1alpha1.FileOperation{v1alpha1.FileOperationCreate},
			expected: 1,
		},
		{
			name:     "file is on default compose watched directory [create] - after",
			before:   false,
			after:    true,
			ops:      []v1alpha1.FileOperation{v1alpha1.FileOperationCreate},
			expected: 1,
		},
		{
			name:     "file is on default compose watched directory [create + remove] - before only",
			before:   true,
			after:    false,
			ops:      []v1alpha1.FileOperation{v1alpha1.FileOperationCreate, v1alpha1.FileOperationRemove},
			expected: 2,
		},
		{
			name:     "file is on default compose watched directory [create + remove] - before and after",
			before:   true,
			after:    true,
			ops:      []v1alpha1.FileOperation{v1alpha1.FileOperationCreate, v1alpha1.FileOperationRemove},
			expected: 4,
		},
	}

	// Run the test cases
	for i := range testCases {
		tc := testCases[i]
		t.Run(tc.name, func(t *testing.T) {
			var (
				ctx          context.Context
				cancel       context.CancelFunc
				mockExecuter *executer.MockExecuter
				ctrl         *gomock.Controller
				logger       *log.PrefixLogger
				hookManager  Manager
				callCount    atomic.Int32
				wg           sync.WaitGroup
			)
			setup := func() {
				fmt.Println("starting test")
				ctx, cancel = context.WithCancel(context.TODO())
				ctrl = gomock.NewController(t)
				mockExecuter = executer.NewMockExecuter(ctrl)
				logger = log.NewPrefixLogger("test")
				hookManager = NewManager(mockExecuter, logger)
				callCount.Store(0)
				wg.Add(1)
				go func() {
					defer wg.Done()
					hookManager.Run(ctx)
				}()
			}

			teardown := func() {
				cancel()
				ctrl.Finish()
				wg.Wait()
			}

			addWatch := func(before, after bool, ops ...v1alpha1.FileOperation) {
				hooks := []v1alpha1.DeviceUpdateHookSpec{
					{
						Actions: []v1alpha1.HookAction{
							marshalExecutable("run-action", &[]string{"VAR=VAL"},
								"/tmp", "1m"),
						},
						OnFile: &ops,
						Name:   util.StrToPtr("test"),
						Path:   util.StrToPtr("/tmp"),
					},
				}
				var beforeHooks, afterHooks []v1alpha1.DeviceUpdateHookSpec
				if before {
					beforeHooks = hooks
				}
				if after {
					afterHooks = hooks
				}
				var current, desired *v1alpha1.RenderedDeviceSpec
				desired = &v1alpha1.RenderedDeviceSpec{
					Hooks: &v1alpha1.DeviceHooksSpec{
						BeforeUpdating: &beforeHooks,
						AfterUpdating:  &afterHooks,
					},
				}
				if err := hookManager.Sync(current, desired); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			expectCalls := func(times int) {
				mockExecuter.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "/tmp", "bash", []string{"-c", "run-action"}, "VAR=VAL").DoAndReturn(
					func(ctx context.Context, workingDir, command string, args []string, env ...string) (string, string, int) {
						callCount.Add(1)
						return "", "", 0
					}).Times(times)
			}

			waitForCalls := func(times int32) {
				for start := time.Now(); time.Since(start) < time.Second; {
					if callCount.Load() == times {
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
				if callCount.Load() != times {
					t.Fatalf("expected %d calls, but got %d", times, callCount.Load())
				}
			}
			setup()
			defer teardown()
			expectCalls(int(tc.expected))
			addWatch(tc.before, tc.after, tc.ops...)
			hookManager.OnBeforeCreate(ctx, "/tmp/compose.yaml")
			hookManager.OnAfterCreate(ctx, "/tmp/compose.yaml")
			hookManager.OnBeforeRemove(ctx, "/tmp/compose.yaml")
			hookManager.OnAfterRemove(ctx, "/tmp/compose.yaml")
			waitForCalls(tc.expected)
		})
	}
}
