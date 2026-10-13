package dependency

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCachedTaskInspectionAllowsWorkerCompletion(t *testing.T) {
	const image = "quay.io/acme/app:target"
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testCases := []struct {
		name   string
		change func(*prefetchManager, imageRef)
		checks int
	}{
		{
			name:   "When inspection blocks it should allow another worker task to complete",
			checks: 1,
		},
		{
			name: "When a task is replaced during inspection it should discard the stale result",
			change: func(manager *prefetchManager, target imageRef) {
				manager.tasks[target] = &prefetchTask{ociType: OCITypePodmanImage}
			},
			checks: 1,
		},
		{
			name: "When a task completes during inspection it should preserve its result",
			change: func(manager *prefetchManager, target imageRef) {
				manager.tasks[target].done = true
				manager.tasks[target].resolvedDigest = "completed-digest"
			},
			checks: 1,
		},
		{
			name: "When the generation changes during inspection it should recheck the current task",
			change: func(manager *prefetchManager, target imageRef) {
				manager.deltaGeneration++
				manager.tasks[target].deltaGeneration = manager.deltaGeneration
			},
			checks: 2,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0).Times(testCase.checks + 1)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).
				Return(fmt.Sprintf(`{"Digest": %q}`, digest), "", 0).Times(testCase.checks)
			inspecting := make(chan struct{}, testCase.checks)
			release := make(chan struct{}, testCase.checks)
			defer close(release)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image).
				DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
					inspecting <- struct{}{}
					<-release
					return digest, "", 0
				}).Times(testCase.checks)
			logger := log.NewPrefixLogger("test")
			rw := fileio.NewMockReadWriter(ctrl)
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			target := imageRef{image: image}
			workerTarget := imageRef{image: "quay.io/acme/other:target"}
			manager := &prefetchManager{
				log:             logger,
				tasks:           map[imageRef]*prefetchTask{workerTarget: {}},
				deltaGeneration: 1,
				podmanFactory:   func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				skopeoFactory:   func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
			}
			type preparationResult struct {
				queue bool
				err   error
			}
			prepared := make(chan preparationResult, 1)
			go func() {
				queue, err := manager.prepareTask(context.Background(), target, OCITypePodmanImage, "", nil, &OCIDeltaTarget{Application: "app"})
				prepared <- preparationResult{queue: queue, err: err}
			}()
			select {
			case <-inspecting:
			case <-time.After(5 * time.Second):
				t.Fatal("cache inspection did not start")
			}
			workerCompleted := make(chan struct{})
			go func() {
				manager.setResult(workerTarget, nil)
				close(workerCompleted)
			}()
			select {
			case <-workerCompleted:
			case <-time.After(5 * time.Second):
				t.Fatal("cache inspection blocked worker completion")
			}
			manager.mu.Lock()
			original := manager.tasks[target]
			if testCase.change != nil {
				testCase.change(manager, target)
			}
			current := manager.tasks[target]
			manager.mu.Unlock()
			for check := 0; check < testCase.checks; check++ {
				release <- struct{}{}
			}
			select {
			case result := <-prepared:
				require.NoError(result.err)
				require.False(result.queue)
			case <-time.After(5 * time.Second):
				t.Fatal("cache preparation did not finish")
			}
			manager.mu.Lock()
			defer manager.mu.Unlock()
			require.True(manager.tasks[workerTarget].done)
			switch {
			case current != original:
				require.False(current.done)
				require.False(original.done)
				require.Empty(original.resolvedDigest)
			case current.resolvedDigest == "completed-digest":
				require.True(current.done)
				require.Empty(manager.deltaApplyResults)
			default:
				require.True(current.done)
				require.True(current.targetPresent)
				require.Equal(digest, current.resolvedDigest)
				require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, manager.deltaApplyResults["app"][target].outcome)
			}
		})
	}
}

func TestCachedTaskInspectionBoundsGenerationRetries(t *testing.T) {
	const image = "quay.io/acme/app:target"
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testCases := []struct {
		name         string
		cancelBefore bool
		cancelDuring bool
		checks       int
		wantDone     bool
	}{
		{name: "When generation churn persists it should accept the cache after three checks", checks: 3, wantDone: true},
		{name: "When context is canceled on entry it should skip inspection", cancelBefore: true},
		{name: "When context is canceled during generation churn it should stop retrying", cancelDuring: true, checks: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			rw := fileio.NewMockReadWriter(ctrl)
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			target := imageRef{image: image}
			task := &prefetchTask{ociType: OCITypePodmanImage, targetPresent: true, deltaGeneration: 1, delta: &OCIDeltaTarget{Application: "app"}}
			manager := &prefetchManager{
				log: logger, tasks: map[imageRef]*prefetchTask{target: task}, deltaGeneration: 1,
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if testCase.cancelBefore {
				cancel()
			}
			if testCase.checks > 0 {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0).Times(testCase.checks)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).
					Return(fmt.Sprintf(`{"Digest": %q}`, digest), "", 0).Times(testCase.checks)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image).
					DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
						manager.mu.Lock()
						manager.deltaGeneration++
						task.deltaGeneration = manager.deltaGeneration
						manager.mu.Unlock()
						if testCase.cancelDuring {
							cancel()
						}
						return "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "", 0
					}).Times(testCase.checks)
			}
			manager.mu.Lock()
			defer manager.mu.Unlock()
			require.Equal(testCase.cancelBefore || testCase.cancelDuring, manager.checkCachedApplicationTask(ctx, target, task))
			require.False(manager.mu.TryLock())
			require.Equal(testCase.wantDone, task.done)
			require.True(task.targetPresent)
			require.Empty(task.resolvedDigest)
			if testCase.wantDone {
				require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotUsed, manager.deltaApplyResults["app"][target].outcome)
			} else {
				require.Empty(manager.deltaApplyResults)
			}
		})
	}
}

func TestCachedAutoArtifactRemainsPresent(t *testing.T) {
	const image = "quay.io/acme/artifact:target"
	testCases := []struct {
		name      string
		completed bool
		reused    bool
		checkOnly bool
		checks    int
	}{
		{name: "When an auto artifact is cached it should not queue a new pull", checks: 3},
		{name: "When an auto artifact task is reused it should not queue a new pull", reused: true, checks: 2},
		{name: "When an auto artifact task is completed it should preserve the cache", reused: true, completed: true, checks: 1},
		{name: "When auto digest checking finds an artifact it should report unknown rather than mismatched", checkOnly: true, checks: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 1).Times(testCase.checks)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0).Times(testCase.checks)
			logger := log.NewPrefixLogger("test")
			rw := fileio.NewMockReadWriter(ctrl)
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			target := imageRef{image: image}
			delta := &OCIDeltaTarget{Application: "app", Hint: "quay.io/acme/delta:target"}
			manager := &prefetchManager{
				log: logger, tasks: make(map[imageRef]*prefetchTask), deltaGeneration: 1,
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
			}
			if testCase.checkOnly {
				require.Equal(digestUnknown, manager.applicationImageDigestMatchesTarget(context.Background(), target, OCITypeAuto, delta, true, podman, nil, nil))
				return
			}
			if !testCase.completed {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image).Return("", "inspection unavailable", 1)
			}
			if testCase.reused {
				manager.tasks[target] = &prefetchTask{ociType: OCITypeAuto, done: true, deltaGeneration: 1}
				if !testCase.completed {
					manager.tasks[target].deltaGeneration = 0
				}
			}
			needsQueue, err := manager.prepareTask(context.Background(), target, OCITypeAuto, "", nil, delta)
			require.NoError(err)
			require.False(needsQueue)
			require.True(manager.tasks[target].done)
			require.True(manager.tasks[target].targetPresent)
		})
	}
}
