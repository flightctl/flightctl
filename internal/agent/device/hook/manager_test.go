package hook

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"go.uber.org/mock/gomock"
)

func TestHookManager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Hooks manager")
}

var _ = Describe("Hook manager test", func() {
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
	BeforeEach(func() {
		fmt.Println("starting test")
		ctx, cancel = context.WithCancel(context.TODO())
		ctrl = gomock.NewController(GinkgoT())
		mockExecuter = executer.NewMockExecuter(ctrl)
		logger = log.NewPrefixLogger("test")
		hookManager = NewManager(mockExecuter, logger)
		callCount.Store(0)
		wg.Add(1)
		go func() {
			defer wg.Done()
			hookManager.Run(ctx)
		}()
	})

	AfterEach(func() {
		cancel()
		ctrl.Finish()
		wg.Wait()
	})
	addWatch := func(before, after bool, ops ...v1alpha1.FileOperation) {
		hooks := []v1alpha1.DeviceUpdateHookSpec{
			{
				Actions: []v1alpha1.HookAction{
					marshalExecutable("run-action", lo.ToPtr([]string{"VAR=VAL"}),
						"/tmp", "1m"),
				},
				OnFile: lo.ToPtr(ops),
				Name:   lo.ToPtr("test"),
				Path:   lo.ToPtr("/tmp"),
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
		Expect(hookManager.Sync(current, desired)).ToNot(HaveOccurred())
	}
	expectCall := func(times int) {
		mockExecuter.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "/tmp", "bash", []string{"-c", "run-action"}, "VAR=VAL").DoAndReturn(
			func(ctx context.Context, workingDir, command string, args []string, env ...string) (string, string, int) {
				callCount.Add(1)
				return "", "", 0
			}).Times(times)
	}
	waitForCalls := func(times int32) {
		Eventually(callCount.Load).WithPolling(100 * time.Millisecond).WithTimeout(time.Second).Should(Equal(times))
	}
	It("unwatched path", func() {
		hookManager.OnAfterCreate(ctx, "/tmp/compose.yaml")
	})
	It("file is on default compose watched directory [create] - before only", func() {
		expectCall(1)
		addWatch(true, false, v1alpha1.FileOperationCreate)
		hookManager.OnBeforeCreate(ctx, "/tmp/compose.yaml")
		hookManager.OnAfterCreate(ctx, "/tmp/compose.yaml")
		hookManager.OnBeforeRemove(ctx, "/tmp/compose.yaml")
		hookManager.OnAfterRemove(ctx, "/tmp/compose.yaml")
		waitForCalls(1)
	})
	It("file is on default compose watched directory [create] - after", func() {
		expectCall(1)
		addWatch(false, true, v1alpha1.FileOperationCreate)
		hookManager.OnBeforeCreate(ctx, "/tmp/compose.yaml")
		hookManager.OnAfterCreate(ctx, "/tmp/compose.yaml")
		hookManager.OnBeforeRemove(ctx, "/tmp/compose.yaml")
		hookManager.OnAfterRemove(ctx, "/tmp/compose.yaml")
		waitForCalls(1)
	})
	It("file is on default compose watched directory [create + remove] - before only", func() {
		expectCall(2)
		addWatch(true, false, v1alpha1.FileOperationCreate, v1alpha1.FileOperationRemove)
		hookManager.OnBeforeCreate(ctx, "/tmp/compose.yaml")
		hookManager.OnAfterCreate(ctx, "/tmp/compose.yaml")
		hookManager.OnBeforeRemove(ctx, "/tmp/compose.yaml")
		hookManager.OnAfterRemove(ctx, "/tmp/compose.yaml")
		waitForCalls(2)
	})
	It("file is on default compose watched directory [create + remove] - before and after", func() {
		expectCall(4)
		addWatch(true, true, v1alpha1.FileOperationCreate, v1alpha1.FileOperationRemove)
		hookManager.OnBeforeCreate(ctx, "/tmp/compose.yaml")
		hookManager.OnAfterCreate(ctx, "/tmp/compose.yaml")
		hookManager.OnBeforeRemove(ctx, "/tmp/compose.yaml")
		hookManager.OnAfterRemove(ctx, "/tmp/compose.yaml")
		waitForCalls(4)
	})
})
