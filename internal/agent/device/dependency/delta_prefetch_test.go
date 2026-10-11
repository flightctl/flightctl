package dependency

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestAggregateApplicationDeltaApplyResults(t *testing.T) {
	targetA := "target-a"
	targetB := "target-b"
	tests := []struct {
		name         string
		results      map[string]applicationDeltaApplyResult
		wantOutcome  v1beta1.DeviceDeltaApplyOutcomeType
		wantFallback *string
	}{
		{
			name: "When all image deltas apply it should report Applied",
			results: map[string]applicationDeltaApplyResult{
				targetA: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
				targetB: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
		},
		{
			name: "When one image delta applies and another is not required it should report Applied",
			results: map[string]applicationDeltaApplyResult{
				targetA: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
				targetB: {outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
		},
		{
			name: "When every image target is already at its desired digest it should report NotRequired",
			results: map[string]applicationDeltaApplyResult{
				targetA: {outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
				targetB: {outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name: "When all image deltas fall back it should report Fallback",
			results: map[string]applicationDeltaApplyResult{
				targetA: {outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, fallbackReason: "import failed"},
			},
			wantOutcome:  v1beta1.DeviceDeltaApplyOutcomeFallback,
			wantFallback: lo.ToPtr("import failed"),
		},
		{
			name: "When image targets have mixed delta results it should report Partial",
			results: map[string]applicationDeltaApplyResult{
				targetA: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
				targetB: {outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, fallbackReason: "import failed"},
			},
			wantOutcome:  v1beta1.DeviceDeltaApplyOutcomePartial,
			wantFallback: lo.ToPtr("import failed"),
		},
		{
			name: "When no image target uses a delta it should report NotUsed",
			results: map[string]applicationDeltaApplyResult{
				targetA: {outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
				targetB: {outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := aggregateApplicationDeltaApplyResults(tt.results)
			require.NotNil(t, status)
			require.Equal(t, tt.wantOutcome, status.Outcome)
			require.Equal(t, tt.wantFallback, status.FallbackReason)
		})
	}
}

func TestPrepareTaskSkipsExistingArtifactWhenDeltaHintExists(t *testing.T) {
	const (
		image     = "quay.io/acme/artifact:v2"
		candidate = "quay.io/acme/delta:target"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	target := imageRef{image: image}
	manager := &prefetchManager{
		log: logger,
		podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
			return podman, nil
		},
		tasks:           make(map[imageRef]*prefetchTask),
		deltaGeneration: 1,
	}

	store := deltastatus.NewInMemory(logger)
	manager.deltaStatusStore = store
	seedHistory(t, store, "app", target.image, 1, v1beta1.DeviceDeltaApplyOutcomeFallback)

	needsQueue, err := manager.prepareTask(
		context.Background(),
		target,
		OCITypePodmanArtifact,
		"sha256:target",
		nil,
		&OCIDeltaTarget{Hint: candidate, Application: "app"},
	)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.True(t, manager.tasks[target].done)
	c, ok := store.Context("app", target.image, "podman", "root", "containers-storage")
	require.True(t, ok)
	results, err := store.LookupApplicationResults([]deltastatus.ApplicationLookup{{Context: c, Digest: historyDigest(1)}})
	require.NoError(t, err)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeFallback, results[0].Status.Outcome)

	manager.tasks[target].done = true
	needsQueue, err = manager.prepareTask(
		context.Background(),
		target,
		OCITypePodmanArtifact,
		"sha256:target",
		nil,
		&OCIDeltaTarget{Hint: candidate, Application: "app"},
	)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.True(t, manager.tasks[target].done)

	manager.deltaGeneration++
	needsQueue, err = manager.prepareTask(
		context.Background(),
		target,
		OCITypePodmanArtifact,
		"sha256:target",
		nil,
		&OCIDeltaTarget{Hint: candidate, Application: "app"},
	)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.True(t, manager.tasks[target].done)
}

func TestBeforeUpdateKeepsGenerationForEquivalentDesiredSpecs(t *testing.T) {
	manager := &prefetchManager{log: log.NewPrefixLogger("test")}
	ctx := context.Background()

	require.NoError(t, manager.BeforeUpdate(ctx, nil, &v1beta1.DeviceSpec{}))
	firstGeneration := manager.deltaGeneration
	require.NoError(t, manager.BeforeUpdate(ctx, nil, &v1beta1.DeviceSpec{}))

	require.Equal(t, firstGeneration, manager.deltaGeneration)
}

func TestPrepareTaskReusedCompletedNoHintTargetOnlyRechecksOnNewGeneration(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/workload:target"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	tests := []struct {
		name           string
		taskGeneration uint64
		wantQueue      bool
	}{
		{
			name:           "When the generation is unchanged it should reuse the completed digest check",
			taskGeneration: 2,
		},
		{
			name:           "When the generation changes and inspection is unavailable it should keep the cache",
			taskGeneration: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			exec := executer.NewMockExecuter(ctrl)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0)
			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, exec, fileio.NewMockReadWriter(ctrl), poll.NewConfig(time.Millisecond, 2))
			target := imageRef{image: image}
			delta := &OCIDeltaTarget{Application: application}
			task := &prefetchTask{
				ociType:              OCITypePodmanImage,
				targetDigest:         digest,
				applicationTargetKey: applicationImageTargetKeyFor(target, digest, OCITypePodmanImage),
				targetPresent:        true,
				delta:                delta,
				deltaGeneration:      tt.taskGeneration,
				done:                 true,
			}
			manager := &prefetchManager{
				log:             logger,
				podmanFactory:   func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				tasks:           map[imageRef]*prefetchTask{target: task},
				deltaGeneration: 2,
			}

			needsQueue, err := manager.prepareTask(context.Background(), target, OCITypePodmanImage, digest, nil, delta)
			require.NoError(t, err)
			require.Equal(t, tt.wantQueue, needsQueue)
			require.True(t, task.targetPresent)
			require.True(t, task.done)
		})
	}
}

func TestApplicationDeltaSpecKeyIgnoresOnlyRenderedStatusAndDeltaHints(t *testing.T) {
	makeDesired := func(desiredState v1beta1.ApplicationDesiredState, restartGeneration int, deltaImage, nestedDelta string, nestedValue string) *v1beta1.DeviceSpec {
		values := map[string]interface{}{"desiredState": nestedValue, "restartGeneration": 7, "deltaImage": "user-chart-value"}
		application := v1beta1.HelmApplication{
			Name:              lo.ToPtr("app"),
			AppType:           v1beta1.AppTypeHelm,
			DesiredState:      lo.ToPtr(desiredState),
			RestartGeneration: lo.ToPtr(restartGeneration),
			Values:            &values,
		}
		require.NoError(t, application.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{
			Image:      "quay.io/acme/chart:v1",
			DeltaImage: lo.ToPtr(deltaImage),
			DeltaImages: &[]v1beta1.ImageDeltaHint{{
				TargetImage:  "quay.io/acme/workload:v1",
				TargetDigest: "sha256:target",
				DeltaImage:   nestedDelta,
			}},
		}))
		var spec v1beta1.ApplicationProviderSpec
		require.NoError(t, spec.FromHelmApplication(application))
		return &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{spec}}
	}

	first, err := applicationDeltaSpecKeys(makeDesired(v1beta1.ApplicationDesiredStateRunning, 1, "quay.io/acme/delta:v1", "quay.io/acme/nested-delta:v1", "chart-value"), nil)
	require.NoError(t, err)
	second, err := applicationDeltaSpecKeys(makeDesired(v1beta1.ApplicationDesiredStateStopped, 2, "quay.io/acme/delta:v2", "quay.io/acme/nested-delta:v2", "chart-value"), nil)
	require.NoError(t, err)
	require.Equal(t, first["app"], second["app"])

	changedChartValue, err := applicationDeltaSpecKeys(makeDesired(v1beta1.ApplicationDesiredStateRunning, 1, "quay.io/acme/delta:v1", "quay.io/acme/nested-delta:v1", "changed-chart-value"), nil)
	require.NoError(t, err)
	require.NotEqual(t, first["app"], changedChartValue["app"])
}

func TestApplicationDeltaSpecKeyUsesResolvedNameWhenNameIsOmitted(t *testing.T) {
	application := v1beta1.ContainerApplication{AppType: v1beta1.AppTypeContainer}
	require.NoError(t, application.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/acme/app:v1"}))
	var spec v1beta1.ApplicationProviderSpec
	require.NoError(t, spec.FromContainerApplication(application))
	desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{spec}}

	keys, err := applicationDeltaSpecKeys(desired, func(*v1beta1.ApplicationProviderSpec) (string, error) {
		return "quay.io/acme/app:v1", nil
	})
	require.NoError(t, err)
	require.Contains(t, keys, "quay.io/acme/app:v1")
}

func TestApplicationDeltaPrefetchRecordsCanonicalPreparedDigest(t *testing.T) {
	for _, tt := range []struct {
		name, hint                             string
		failImport, failPull, unknown, mutable bool
		want                                   v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{name: "When a mutable target applies it should alias the final canonical image", hint: "quay.io/acme/delta:v2", mutable: true, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When a hint applies it should key the canonical final digest", hint: "quay.io/acme/delta:v2", want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When a hint fails it should record only after successful full pull", hint: "quay.io/acme/delta:v2", failImport: true, want: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When final pull fails it should retain earlier history", hint: "quay.io/acme/delta:v2", failPull: true},
		{name: "When no hint is provided it should full pull without discovery", want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When an explicit hint is invalid it should directly fall back", hint: "not an image", want: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When final identity is unknown it should skip insertion", unknown: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			backoff := poll.NewConfig(time.Millisecond, 1)
			podman := client.NewPodman(logger, exec, rw, backoff)
			skopeo := client.NewSkopeo(logger, exec, rw)
			target := imageRef{image: "quay.io/acme/app@" + historyDigest(10)}
			if tt.mutable {
				target.image = "quay.io/acme/app:latest"
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+target.image).Return(historyDigest(10), "", 0)
			}
			task := &prefetchTask{ociType: OCITypePodmanImage, delta: &OCIDeltaTarget{Application: "app", Hint: tt.hint}, deltaGeneration: 1}
			store := deltastatus.NewInMemory(logger)
			seedHistory(t, store, "app", target.image, 1, v1beta1.DeviceDeltaApplyOutcomeApplied)
			m := &prefetchManager{log: logger, readWriter: rw, ociDelta: client.NewOCIDelta(logger, exec, time.Minute), deltaStatusStore: store, tasks: map[imageRef]*prefetchTask{target: task}, deltaGeneration: 1}
			source := "quay.io/acme/app@" + historyDigest(3)
			finalID := strings.Repeat("f", 64)
			finalDigest := historyDigest(20)
			if tt.hint != "" && tt.hint != "not an image" {
				artifact := fmt.Sprintf(`{"artifactType":%q,"annotations":{%q:%q},"subject":{"digest":%q},"config":{"digest":%q,"size":1},"layers":[]}`, ociDeltaArtifactType, ociDeltaSourceAnnotation, historyDigest(3), historyDigest(10), historyDigest(4))
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+tt.hint).Return(artifact, "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", source).Return("", "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", "docker://"+tt.hint, gomock.Any()).Return("", "", 0)
				code := 0
				if tt.failImport {
					code = 1
				}
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", target.image, gomock.Any()).Return("", "failed", code)
			}
			code := 0
			if tt.failPull {
				code = 1
			}
			id := finalID
			if tt.unknown {
				id = "opaque"
			}
			pullRef := target.image
			if tt.mutable {
				pullRef = "quay.io/acme/app@" + historyDigest(10)
			}
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", pullRef).Return(id, "failed", code)
			if tt.mutable {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "tag", finalID, target.image).Return("", "", 0)
			}
			if !tt.failPull && !tt.unknown {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", finalID).Return(finalDigest, "", 0)
			}
			err := m.pullApplicationImage(context.Background(), target, task, podman, skopeo)
			if tt.failPull {
				require.Error(err)
			} else {
				require.NoError(err)
			}
			c, ok := store.Context("app", target.image, "podman", "root", "containers-storage")
			require.True(ok)
			results, lookupErr := store.LookupApplicationResults([]deltastatus.ApplicationLookup{{Context: c, Digest: finalDigest}, {Context: c, Digest: historyDigest(1)}, {Context: c, Digest: historyDigest(3)}})
			require.NoError(lookupErr)
			require.NotNil(results[1].Status)
			require.Nil(results[2].Status)
			if tt.want == "" {
				require.Nil(results[0].Status)
			} else {
				require.NotNil(results[0].Status)
				require.Equal(tt.want, results[0].Status.Outcome)
			}
		})
	}
}
