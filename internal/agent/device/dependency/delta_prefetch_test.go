package dependency

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestApplicationDeltaPrefetch(t *testing.T) {
	const image = "quay.io/acme/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const candidate = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	tests := []struct {
		name               string
		delta              *OCIDeltaTarget
		setup              func(*executer.MockExecuter)
		retryAfterFallback bool
		wantFallback       bool
		wantOutcome        v1beta1.DeviceDeltaApplyOutcomeType
		fallbackContains   string
		disableDeltaClient bool
		deltaFactory       client.OCIDeltaFactory
	}{
		{
			name:               "When no delta client exists it should skip discovery and full-pull with NotUsed",
			delta:              &OCIDeltaTarget{SourceDigest: "sha256:source", Application: "app"},
			disableDeltaClient: true,
			wantOutcome:        v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
		},
		{
			name:        "hinted image imports into container storage",
			delta:       &OCIDeltaTarget{Hint: candidate, Application: "app"},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
			setup: func(exec *executer.MockExecuter) {
				copyDelta := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", 0)
				importDelta := exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "", 0)
				refreshRegistryReference := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
				gomock.InOrder(copyDelta, importDelta, refreshRegistryReference)
			},
		},
		{
			name:  "registry reference refresh failure falls back to a full image pull",
			delta: &OCIDeltaTarget{Hint: candidate, Application: "app"},
			setup: func(exec *executer.MockExecuter) {
				copyDelta := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", 0)
				importDelta := exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "", 0)
				refreshRegistryReference := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "registry refresh failed", 1)
				fullPull := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
				gomock.InOrder(copyDelta, importDelta, refreshRegistryReference, fullPull)
			},
			wantFallback:     true,
			wantOutcome:      v1beta1.DeviceDeltaApplyOutcomeFallback,
			fallbackContains: "registry refresh failed",
		},
		{
			name:               "missing candidate full-pulls without initializing the delta client",
			delta:              &OCIDeltaTarget{Application: "app"},
			disableDeltaClient: true,
			deltaFactory: func(v1beta1.Username) (*client.OCIDelta, error) {
				return nil, fmt.Errorf("factory should not be called without a candidate")
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
		},
		{
			name:               "hinted candidate full-pulls without delta when the delta client is unavailable",
			delta:              &OCIDeltaTarget{Hint: candidate, Application: "app"},
			disableDeltaClient: true,
			wantOutcome:        v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
		},
		{
			name:               "hinted candidate falls back when delta client creation fails",
			delta:              &OCIDeltaTarget{Hint: candidate, Application: "app"},
			disableDeltaClient: true,
			deltaFactory: func(v1beta1.Username) (*client.OCIDelta, error) {
				return nil, fmt.Errorf("delta client creation failed")
			},
			wantFallback:     true,
			wantOutcome:      v1beta1.DeviceDeltaApplyOutcomeFallback,
			fallbackContains: "delta client creation failed",
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
		},
		{
			name: "When the source digest matches but the image is absent it should full-pull and report NotUsed",
			delta: &OCIDeltaTarget{
				SourceDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Application:  "app",
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			setup: func(exec *executer.MockExecuter) {
				inspectTarget := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image,
				).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", 0)
				fullPull := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
				gomock.InOrder(inspectTarget, fullPull)
			},
		},
		{
			name:  "failed delta falls back to full pull",
			delta: &OCIDeltaTarget{Hint: candidate, Application: "app"},
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "import failed", 1)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
			wantFallback:     true,
			wantOutcome:      v1beta1.DeviceDeltaApplyOutcomeFallback,
			fallbackContains: "import failed",
		},
		{
			name:               "successful retry delta import clears previous fallback",
			delta:              &OCIDeltaTarget{Hint: candidate, Application: "app"},
			retryAfterFallback: true,
			wantOutcome:        v1beta1.DeviceDeltaApplyOutcomeApplied,
			setup: func(exec *executer.MockExecuter) {
				firstCopy := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", 0)
				firstImport := exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "import failed", 1)
				firstFullPull := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "pull failed", 1)
				secondCopy := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", 0)
				secondImport := exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "", 0)
				secondRegistryReferenceRefresh := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
				gomock.InOrder(firstCopy, firstImport, firstFullPull, secondCopy, secondImport, secondRegistryReferenceRefresh)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			tt.setup(exec)

			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			var ociDelta *client.OCIDelta
			if !tt.disableDeltaClient {
				ociDelta = client.NewOCIDelta(logger, exec, time.Minute)
			}
			target := imageRef{image: image}
			task := &prefetchTask{delta: tt.delta, deltaGeneration: 1, applicationTargetKey: applicationImageTargetKeyFor(target, "", OCITypePodmanImage)}
			manager := &prefetchManager{
				log:                   logger,
				readWriter:            rw,
				pullTimeout:           time.Minute,
				ociDelta:              ociDelta,
				ociDeltaFactory:       tt.deltaFactory,
				tasks:                 map[imageRef]*prefetchTask{target: task},
				deltaGeneration:       1,
				deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
				deltaTargetsScheduled: true,
				deltaTargetRefs:       map[string]imageRef{deltastatus.Fingerprint(string(target.owner), target.image): target},
				deltaAppTargetKeys: map[string]map[string]string{
					"app": {deltastatus.Fingerprint(string(target.owner), target.image): task.applicationTargetKey},
				},
			}

			pull := func() error {
				return manager.pullApplicationImage(context.Background(), target, task, podman, skopeo, client.Timeout(time.Minute))
			}
			if tt.retryAfterFallback {
				require.Error(t, pull())
			}
			err := pull()
			task.done = true
			deviceStatus := &v1beta1.DeviceStatus{
				Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}},
			}
			require.NoError(t, manager.Status(context.Background(), deviceStatus))
			if tt.wantOutcome != "" {
				require.NotNil(t, deviceStatus.Applications[0].LastDelta)
				require.Equal(t, tt.wantOutcome, deviceStatus.Applications[0].LastDelta.Outcome)
			} else {
				require.Nil(t, deviceStatus.Applications[0].LastDelta)
			}
			if tt.wantFallback {
				require.NotNil(t, deviceStatus.Applications[0].LastDelta)
				require.NotNil(t, deviceStatus.Applications[0].LastDelta.FallbackReason)
				require.NotEmpty(t, *deviceStatus.Applications[0].LastDelta.FallbackReason)
				if tt.fallbackContains != "" {
					require.Contains(t, *deviceStatus.Applications[0].LastDelta.FallbackReason, tt.fallbackContains)
				}
			}
			require.NoError(t, err)
			matches, globErr := filepath.Glob(filepath.Join(root, "tmp", "application-delta*"))
			require.NoError(t, globErr)
			require.Empty(t, matches)
		})
	}
}

func TestCachedApplicationImageReportsNotRequiredWhenDigestMatches(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/app:v2"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		staleDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	tests := []struct {
		name            string
		localDigest     string
		repoDigests     []string
		deltaHint       string
		sourceDigest    string
		targetDigest    string
		previousOutcome v1beta1.DeviceDeltaApplyOutcomeType
		wantFullPull    bool
		wantOutcome     v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{
			name:         "When a cached image matches and the source digest is known without a delta image it should report NotRequired",
			sourceDigest: digest,
			wantOutcome:  v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name:        "When a cached image matches despite a delta hint it should report NotRequired",
			deltaHint:   "quay.io/acme/delta:target",
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name:        "When a cached image matches without delta metadata it should report NotRequired",
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name:        "When a cached image repository digest matches despite a different content digest it should report NotRequired",
			localDigest: staleDigest,
			repoDigests: []string{"quay.io/acme/app@" + digest},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name:            "When a cached image matches without delta metadata it should upgrade NotUsed to NotRequired",
			previousOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			wantOutcome:     v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name:         "When a cached image does not match the desired digest without delta metadata it should full-pull and report NotUsed",
			targetDigest: staleDigest,
			wantFullPull: true,
			wantOutcome:  v1beta1.DeviceDeltaApplyOutcomeNotUsed,
		},
		{
			name:            "When a cached image matches after delta application it should preserve Applied",
			sourceDigest:    digest,
			previousOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
			wantOutcome:     v1beta1.DeviceDeltaApplyOutcomeApplied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			exec := executer.NewMockExecuter(ctrl)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0)
			requestedDigestValue := tt.targetDigest
			if requestedDigestValue == "" {
				requestedDigestValue = digest
			}
			localDigestValue := tt.localDigest
			if localDigestValue == "" {
				localDigestValue = digest
			}
			inspection, err := json.Marshal(map[string]any{"Digest": localDigestValue, "RepoDigests": tt.repoDigests})
			require.NoError(t, err)
			localDigest := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image,
			).Return(string(inspection), "", 0)
			requestedDigest := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image,
			).Return(requestedDigestValue, "", 0)
			if tt.wantFullPull {
				fullPull := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "pull", image,
				).Return("", "", 0)
				gomock.InOrder(localDigest, requestedDigest, fullPull)
			} else {
				gomock.InOrder(localDigest, requestedDigest)
			}

			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			target := imageRef{image: image}
			targetID := deltastatus.Fingerprint(string(target.owner), target.image)
			targetKey := applicationImageTargetKeyFor(target, requestedDigestValue, OCITypePodmanImage)
			task := &prefetchTask{
				ociType:              OCITypePodmanImage,
				targetDigest:         requestedDigestValue,
				applicationTargetKey: targetKey,
				targetPresent:        true,
				delta:                &OCIDeltaTarget{Hint: tt.deltaHint, SourceDigest: tt.sourceDigest, Application: application},
				deltaGeneration:      1,
			}
			manager := &prefetchManager{
				log: logger,
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
					return podman, nil
				},
				skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) {
					return skopeo, nil
				},
				pullTimeout:           time.Minute,
				tasks:                 map[imageRef]*prefetchTask{target: task},
				deltaGeneration:       1,
				deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
				deltaTargetsScheduled: true,
				deltaTargetRefs:       map[string]imageRef{targetID: target},
				deltaAppTargetKeys: map[string]map[string]string{
					application: {targetID: targetKey},
				},
			}
			if tt.previousOutcome != "" {
				manager.deltaApplyResults[application] = map[imageRef]applicationDeltaApplyResult{
					target: {outcome: tt.previousOutcome, targetKey: targetKey},
				}
			}

			manager.mu.Lock()
			needsQueue := manager.checkCachedApplicationTask(context.Background(), target, task)
			manager.mu.Unlock()
			require.Equal(t, tt.wantFullPull, needsQueue)
			if needsQueue {
				require.NoError(t, manager.pull(context.Background(), target, task))
				task.done = true
			}

			deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
			require.NoError(t, manager.Status(context.Background(), deviceStatus))
			require.NotNil(t, deviceStatus.Applications[0].LastDelta)
			require.Equal(t, tt.wantOutcome, deviceStatus.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestCachedApplicationImageWithStaleDigestAttemptsHintedDelta(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/app:target"
		candidate   = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		source      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		target      = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0)
	localDigest := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image,
	).Return(fmt.Sprintf(`{"Digest": %q}`, source), "", 0)
	requestedDigest := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image,
	).Return(target, "", 0)
	copyDelta := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", gomock.Any(), gomock.Any(), gomock.Any(),
	).Return("", "", 0)
	importDelta := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any(),
	).Return("", "", 0)
	refreshRegistryReference := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", "pull", image,
	).Return("", "", 0)
	gomock.InOrder(localDigest, requestedDigest, copyDelta, importDelta, refreshRegistryReference)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	imageTarget := imageRef{image: image}
	targetID := deltastatus.Fingerprint(string(imageTarget.owner), imageTarget.image)
	targetKey := applicationImageTargetKeyFor(imageTarget, target, OCITypePodmanImage)
	task := &prefetchTask{
		ociType:              OCITypePodmanImage,
		targetDigest:         target,
		applicationTargetKey: targetKey,
		targetPresent:        true,
		delta: &OCIDeltaTarget{
			Hint:         candidate,
			SourceDigest: source,
			Application:  application,
		},
		deltaGeneration: 1,
	}
	manager := &prefetchManager{
		log:        logger,
		readWriter: rw,
		podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
			return podman, nil
		},
		skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) {
			return skopeo, nil
		},
		pullTimeout:           time.Minute,
		ociDelta:              client.NewOCIDelta(logger, exec, time.Minute),
		tasks:                 map[imageRef]*prefetchTask{imageTarget: task},
		deltaGeneration:       1,
		deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
		deltaTargetsScheduled: true,
		deltaTargetRefs:       map[string]imageRef{targetID: imageTarget},
		deltaAppTargetKeys: map[string]map[string]string{
			application: {targetID: targetKey},
		},
	}

	manager.mu.Lock()
	needsQueue := manager.checkCachedApplicationTask(context.Background(), imageTarget, task)
	manager.mu.Unlock()
	require.True(t, needsQueue)
	require.NoError(t, manager.pull(context.Background(), imageTarget, task))
	task.done = true

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
}

func TestOCIArtifactWithDeltaHintFallsBackToFullArtifactPull(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/artifact:v2"
		candidate   = "quay.io/acme/delta:target"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	version := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0)
	pullArtifact := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", "artifact", "pull", image,
	).Return("", "", 0)
	gomock.InOrder(version, pullArtifact)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	target := imageRef{image: image}
	targetID := deltastatus.Fingerprint(string(target.owner), target.image)
	targetKey := applicationImageTargetKeyFor(target, "sha256:target", OCITypePodmanArtifact)
	task := &prefetchTask{
		ociType:              OCITypePodmanArtifact,
		targetDigest:         "sha256:target",
		applicationTargetKey: targetKey,
		delta:                &OCIDeltaTarget{Hint: candidate, Application: application},
		deltaGeneration:      1,
	}
	manager := &prefetchManager{
		log: logger,
		podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
			return podman, nil
		},
		skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) {
			return skopeo, nil
		},
		pullTimeout:           time.Minute,
		tasks:                 map[imageRef]*prefetchTask{target: task},
		deltaGeneration:       1,
		deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
		deltaTargetsScheduled: true,
		deltaTargetRefs:       map[string]imageRef{targetID: target},
		deltaAppTargetKeys: map[string]map[string]string{
			application: {targetID: targetKey},
		},
	}

	require.NoError(t, manager.pull(context.Background(), target, task))
	task.done = true

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeFallback, deviceStatus.Applications[0].LastDelta.Outcome)
	require.NotNil(t, deviceStatus.Applications[0].LastDelta.FallbackReason)
	require.Contains(t, *deviceStatus.Applications[0].LastDelta.FallbackReason, "not supported for OCI artifacts")
}

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

func TestApplicationDeltaStatusWaitsForTargetsToBeScheduled(t *testing.T) {
	const application = "app"
	target := imageRef{image: "quay.io/acme/workload:target"}
	const targetKey = "target-key"
	manager := &prefetchManager{
		tasks: make(map[imageRef]*prefetchTask),
		deltaApplyResults: map[string]map[imageRef]applicationDeltaApplyResult{
			application: {target: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied, targetKey: targetKey}},
		},
		deltaAppTargetKeys: map[string]map[string]string{
			application: {deltastatus.Fingerprint(string(target.owner), target.image): targetKey},
		},
		deltaTargetRefs: map[string]imageRef{deltastatus.Fingerprint(string(target.owner), target.image): target},
	}

	for _, tt := range []struct {
		name      string
		scheduled bool
	}{
		{name: "When targets are still being scheduled it should withhold LastDelta"},
		{name: "When all targets are scheduled it should publish LastDelta", scheduled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager.deltaTargetsScheduled = tt.scheduled
			deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
			require.NoError(t, manager.Status(context.Background(), deviceStatus))
			if !tt.scheduled {
				require.Nil(t, deviceStatus.Applications[0].LastDelta)
				return
			}
			require.NotNil(t, deviceStatus.Applications[0].LastDelta)
			require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestApplicationDeltaStatusWaitsForAllTargetsToHaveResults(t *testing.T) {
	const application = "app"
	manager := &prefetchManager{
		deltaTargetsScheduled: true,
		deltaTargetRefs:       make(map[string]imageRef),
		tasks:                 make(map[imageRef]*prefetchTask),
		deltaApplyResults: map[string]map[imageRef]applicationDeltaApplyResult{
			application: make(map[imageRef]applicationDeltaApplyResult),
		},
		deltaAppTargetKeys: map[string]map[string]string{
			application: make(map[string]string),
		},
	}

	for _, image := range []string{
		"quay.io/acme/workload-1:target",
		"quay.io/acme/workload-2:target",
		"quay.io/acme/workload-3:target",
		"quay.io/acme/workload-4:target",
		"quay.io/acme/workload-5:target",
	} {
		target := imageRef{image: image}
		targetKey := "target-key:" + image
		manager.tasks[target] = &prefetchTask{
			delta:                &OCIDeltaTarget{Application: application},
			applicationTargetKey: targetKey,
			done:                 true,
		}
		manager.deltaAppTargetKeys[application][deltastatus.Fingerprint(string(target.owner), target.image)] = targetKey
		manager.deltaTargetRefs[deltastatus.Fingerprint(string(target.owner), target.image)] = target
		manager.deltaApplyResults[application][target] = applicationDeltaApplyResult{
			outcome:   v1beta1.DeviceDeltaApplyOutcomeApplied,
			targetKey: targetKey,
		}
	}

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	lastTarget := imageRef{image: "quay.io/acme/workload-6:target"}
	lastTargetKey := "target-key:" + lastTarget.image
	lastTask := &prefetchTask{
		delta:                &OCIDeltaTarget{Application: application},
		applicationTargetKey: lastTargetKey,
	}
	manager.tasks[lastTarget] = lastTask
	manager.deltaAppTargetKeys[application][deltastatus.Fingerprint(string(lastTarget.owner), lastTarget.image)] = lastTargetKey
	manager.deltaTargetRefs[deltastatus.Fingerprint(string(lastTarget.owner), lastTarget.image)] = lastTarget
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.Nil(t, deviceStatus.Applications[0].LastDelta)

	manager.deltaApplyResults[application][lastTarget] = applicationDeltaApplyResult{
		outcome:        v1beta1.DeviceDeltaApplyOutcomeFallback,
		fallbackReason: "delta import failed",
		targetKey:      lastTargetKey,
	}
	lastTask.done = true
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomePartial, deviceStatus.Applications[0].LastDelta.Outcome)

	// A later collection can remove a target while leaving its prior result in
	// memory. Its result must not affect the current application's outcome.
	delete(manager.deltaAppTargetKeys[application], deltastatus.Fingerprint(string(lastTarget.owner), lastTarget.image))
	delete(manager.deltaTargetRefs, deltastatus.Fingerprint(string(lastTarget.owner), lastTarget.image))
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	manager.tasks = make(map[imageRef]*prefetchTask)
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
}

func TestApplicationDeltaStatusUsesCurrentTargetReferences(t *testing.T) {
	const application = "app"
	const targetKey = "current-target"
	target := imageRef{image: "quay.io/acme/workload:target", owner: "app-user"}
	targetID := deltastatus.Fingerprint(string(target.owner), target.image)

	for _, tt := range []struct {
		name        string
		missingRef  bool
		otherOwner  bool
		staleResult bool
		staleTask   bool
		withoutTask bool
		wantStatus  bool
	}{
		{name: "When the reference is missing it should withhold LastDelta", missingRef: true},
		{name: "When another owner has the same image it should withhold LastDelta", otherOwner: true},
		{name: "When the result belongs to an older target it should withhold LastDelta", staleResult: true},
		{name: "When the task belongs to an older target it should withhold LastDelta", staleTask: true},
		{name: "When the completed task is cleaned up it should retain LastDelta", withoutTask: true, wantStatus: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager := &prefetchManager{
				deltaTargetsScheduled: true,
				deltaAppTargetKeys:    map[string]map[string]string{application: {targetID: targetKey}},
				deltaTargetRefs:       map[string]imageRef{targetID: target},
				deltaApplyResults: map[string]map[imageRef]applicationDeltaApplyResult{
					application: {target: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied, targetKey: targetKey}},
				},
				tasks: map[imageRef]*prefetchTask{target: {applicationTargetKey: targetKey, done: true}},
			}
			if tt.missingRef {
				delete(manager.deltaTargetRefs, targetID)
			}
			if tt.otherOwner {
				otherTarget := imageRef{image: target.image, owner: "other-user"}
				manager.deltaApplyResults[application][otherTarget] = manager.deltaApplyResults[application][target]
				delete(manager.deltaApplyResults[application], target)
			}
			if tt.staleResult {
				manager.deltaApplyResults[application][target] = applicationDeltaApplyResult{
					outcome: v1beta1.DeviceDeltaApplyOutcomeApplied, targetKey: "previous-target",
				}
			}
			if tt.staleTask {
				manager.tasks[target].applicationTargetKey = "previous-target"
			}
			if tt.withoutTask {
				delete(manager.tasks, target)
			}

			deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
			require.NoError(t, manager.Status(context.Background(), deviceStatus))
			if !tt.wantStatus {
				require.Nil(t, deviceStatus.Applications[0].LastDelta)
				return
			}
			require.NotNil(t, deviceStatus.Applications[0].LastDelta)
			require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestApplicationTargetIdentitiesTrackTargetsWithoutDeltaHintOrSourceDigest(t *testing.T) {
	target := imageRef{image: "quay.io/acme/app:latest"}
	targetKey := applicationImageTargetKeyFor(target, "", OCITypePodmanImage)
	appTargets, targetRefs := applicationTargetIdentities(map[imageRef]OCIPullTarget{
		target: {
			Type:      OCITypePodmanImage,
			Reference: target.image,
			Delta:     &OCIDeltaTarget{Application: "app"},
		},
	})

	targetID := deltastatus.Fingerprint(string(target.owner), target.image)
	require.Equal(t, map[string]map[string]string{"app": {targetID: targetKey}}, appTargets)
	require.Equal(t, map[string]imageRef{targetID: target}, targetRefs)
}

func TestPrepareTaskReusedCompletedTargetKeepsCacheWhenDigestCheckIsUnavailable(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/workload:target"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	target := imageRef{image: image}
	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0)
	logger := log.NewPrefixLogger("test")
	podman := client.NewPodman(logger, exec, fileio.NewMockReadWriter(ctrl), poll.NewConfig(time.Millisecond, 2))
	delta := &OCIDeltaTarget{Hint: "quay.io/acme/delta:target", Application: application}
	task := &prefetchTask{
		ociType:              OCITypePodmanImage,
		targetDigest:         digest,
		applicationTargetKey: applicationImageTargetKeyFor(target, digest, OCITypePodmanImage),
		delta:                delta,
		deltaGeneration:      1,
		done:                 true,
	}
	manager := &prefetchManager{
		log:               logger,
		podmanFactory:     func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		tasks:             map[imageRef]*prefetchTask{target: task},
		deltaGeneration:   2,
		deltaApplyResults: make(map[string]map[imageRef]applicationDeltaApplyResult),
	}

	needsQueue, err := manager.prepareTask(context.Background(), target, OCITypePodmanImage, digest, nil, delta)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.Equal(t, uint64(2), task.deltaGeneration)
	require.True(t, task.targetPresent)
	require.True(t, task.done)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotUsed, manager.deltaApplyResults[application][target].outcome)
}

func TestPrepareTaskCachedDigestChecks(t *testing.T) {
	const image = "quay.io/acme/app:target"
	const currentDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const differentDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testCases := []struct {
		name           string
		localExit      int
		remoteExit     int
		remoteDigest   string
		reused         bool
		sameGeneration bool
		missing        bool
		wantQueue      bool
		wantOutcome    v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{name: "When the cached digest matches it should complete synchronously", remoteDigest: currentDigest, wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		{name: "When local inspection fails it should queue a pull", localExit: 1, remoteDigest: differentDigest, wantQueue: true},
		{name: "When remote inspection fails it should keep the cache synchronously", remoteExit: 1, wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When the remote digest is empty it should keep the cache synchronously", wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When the cached digest mismatches it should queue and reuse the resolved digest", remoteDigest: differentDigest, wantQueue: true},
		{name: "When a reused task changes generation it should recheck its cached digest", remoteDigest: differentDigest, reused: true, wantQueue: true},
		{name: "When a reused image was pruned it should queue a pull", reused: true, missing: true, remoteDigest: differentDigest, wantQueue: true},
		{name: "When a reused image was pruned in the same generation it should queue a pull", reused: true, sameGeneration: true, missing: true, remoteDigest: differentDigest, wantQueue: true},
		{name: "When local inspection of a reused image fails it should queue a pull", reused: true, localExit: 1, remoteDigest: differentDigest, wantQueue: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			existsExit := 0
			if testCase.missing {
				existsExit = 1
			}
			existsCalls := 1
			if !testCase.reused {
				existsCalls++
			}
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", existsExit).Times(existsCalls)
			if testCase.missing {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("", "", 1)
			}
			if !testCase.missing {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).Return(fmt.Sprintf(`{"Digest": %q}`, currentDigest), "inspect failed", testCase.localExit)
				if testCase.localExit == 0 {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image).Return(testCase.remoteDigest, "inspect failed", testCase.remoteExit)
				}
			}
			logger := log.NewPrefixLogger("test")
			readWriter := fileio.NewMockReadWriter(ctrl)
			podman := client.NewPodman(logger, mockExec, readWriter, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, mockExec, readWriter)
			manager := &prefetchManager{
				log: logger, tasks: make(map[imageRef]*prefetchTask), deltaGeneration: 2,
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				ociDelta:      client.NewOCIDelta(logger, mockExec, time.Minute),
			}
			target := imageRef{image: image}
			delta := &OCIDeltaTarget{Application: "app", SourceDigest: currentDigest}
			if testCase.reused {
				manager.tasks[target] = &prefetchTask{ociType: OCITypePodmanImage, done: true, deltaGeneration: 1, resolvedDigest: currentDigest}
				if testCase.sameGeneration {
					manager.tasks[target].deltaGeneration = 2
					manager.tasks[target].delta = delta
					manager.tasks[target].resolvedDigest = ""
				}
			}
			needsQueue, err := manager.prepareTask(context.Background(), target, OCITypePodmanImage, "", nil, delta)
			require.NoError(err)
			require.Equal(testCase.wantQueue, needsQueue)
			task := manager.tasks[target]
			require.Equal(!testCase.wantQueue, task.done)
			if !testCase.wantQueue {
				require.NoError(manager.checkReady(context.Background()))
				require.Equal(testCase.wantOutcome, manager.deltaApplyResults["app"][target].outcome)
				return
			}
			require.False(task.targetPresent)
			if testCase.localExit != 0 || testCase.missing {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image).Return(differentDigest, "", 0)
			} else {
				require.Equal(differentDigest, task.resolvedDigest)
			}
			require.ErrorIs(manager.checkReady(context.Background()), errors.ErrPrefetchNotReady)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "list-referrers", "docker://"+image).Return(`{"manifests": []}`, "", 0)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			require.NoError(manager.pull(context.Background(), target, task))
		})
	}
}

func TestPrepareTaskCompletesExistingImageWhenDigestCheckIsUnavailable(t *testing.T) {
	const (
		image     = "quay.io/acme/workload:target"
		candidate = "quay.io/acme/delta:target"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0).Times(2)

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

	needsQueue, err := manager.prepareTask(
		context.Background(),
		target,
		OCITypePodmanImage,
		"sha256:target",
		nil,
		&OCIDeltaTarget{Hint: candidate, Application: "app"},
	)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.True(t, manager.tasks[target].targetPresent)
	require.True(t, manager.tasks[target].done)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotUsed, manager.deltaApplyResults["app"][target].outcome)
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
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotUsed, manager.deltaApplyResults["app"][target].outcome)

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
			delta := &OCIDeltaTarget{SourceDigest: digest, Application: application}
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

func TestApplicationDeltaStatusPersistsAcrossRestartAndClearsForChangedSpec(t *testing.T) {
	const (
		imageV2       = "quay.io/acme/app:v2"
		imageV3       = "quay.io/acme/app:v3"
		imageDigestV2 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		imageDigestV3 = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		deltaV2       = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		deltaV3       = "quay.io/acme/delta@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)

	makeApplication := func(image string) v1beta1.ApplicationProviderSpec {
		application := v1beta1.ContainerApplication{
			Name:    lo.ToPtr("app"),
			AppType: v1beta1.AppTypeContainer,
		}
		require.NoError(t, application.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
		var spec v1beta1.ApplicationProviderSpec
		require.NoError(t, spec.FromContainerApplication(application))
		return spec
	}

	makeTarget := func(image, digest, deltaImage, sourceDigest string) (imageRef, OCIPullTarget) {
		ref := imageRef{image: image}
		return ref, OCIPullTarget{
			Type:      OCITypePodmanImage,
			Reference: image,
			Digest:    digest,
			Delta: &OCIDeltaTarget{
				Hint:         deltaImage,
				SourceDigest: sourceDigest,
				Application:  "app",
			},
		}
	}

	refV2, targetV2 := makeTarget(imageV2, imageDigestV2, deltaV2, "sha256:source-v1")
	desiredV2 := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{makeApplication(imageV2)}}
	desiredV2WithUnrelatedOSUpdate := &v1beta1.DeviceSpec{
		Os:           &v1beta1.DeviceOsSpec{Image: "quay.io/acme/os:v2"},
		Applications: desiredV2.Applications,
	}
	specKeys, err := applicationDeltaSpecKeys(desiredV2, nil)
	require.NoError(t, err)
	targetID := deltastatus.Fingerprint(string(refV2.owner), refV2.image)
	targetKeyV2 := applicationImageTargetKey(refV2, targetV2)
	require.NoError(t, store.ReconcileApplicationSpecs(specKeys))
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{
		"app": {targetID: targetKeyV2},
	}, true))
	require.NoError(t, store.RecordApplicationResult(
		"app", specKeys["app"], targetID, targetKeyV2,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
	))

	// Re-read the state through a new Store to simulate an agent restart.
	restartedStore := deltastatus.New(rw, "/var/lib/flightctl", logger)
	ctrl := gomock.NewController(t)
	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", imageV2).Return("", "", 0).Times(6)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", imageV3).Return("", "", 0).Times(3)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", imageV2).Return(fmt.Sprintf(`{"Digest": %q}`, imageDigestV2), "", 0).Times(2)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+imageV2).Return(imageDigestV2, "", 0).Times(2)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", imageV3).Return(fmt.Sprintf(`{"Digest": %q}`, imageDigestV3), "", 0)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+imageV3).Return(imageDigestV3, "", 0)
	mockResources := resource.NewMockManager(ctrl)
	mockResources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(6)
	podman := client.NewPodman(logger, mockExec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, mockExec, rw)
	manager := NewPrefetchManager(
		logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
		client.NewCLIClients(),
		rw,
		util.Duration(time.Minute),
		mockResources,
		poll.Config{},
		WithDeltaStatusStore(restartedStore),
	)
	var target OCIPullTarget
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: {target}}}, nil
	}))
	target = targetV2
	ctx := context.Background()
	completePrefetch := func(target imageRef, current, desired *v1beta1.DeviceSpec) {
		require.NoError(t, manager.BeforeUpdate(ctx, current, desired))
		require.True(t, manager.tasks[target].done)
		require.NoError(t, manager.BeforeUpdate(ctx, current, desired))
	}
	completePrefetch(refV2, nil, desiredV2)

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, manager.Status(ctx, deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	// An unrelated OS update and a changed delta hint do not erase the fact that
	// the current application image was reconstructed with a delta before restart.
	_, targetWithNewHint := makeTarget(imageV2, imageDigestV2, deltaV3, "sha256:source-v2")
	target = targetWithNewHint
	completePrefetch(refV2, desiredV2, desiredV2WithUnrelatedOSUpdate)
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, manager.Status(ctx, deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	// The last outcome remains reportable after transient prefetch tasks are
	// discarded at the end of reconciliation.
	manager.mu.Lock()
	manager.tasks = make(map[imageRef]*prefetchTask)
	manager.mu.Unlock()
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, manager.Status(ctx, deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	// A new application target clears the old result. Its already-cached image
	// is reported as NotRequired, rather than inheriting v2's Applied outcome.
	refV3, targetV3 := makeTarget(imageV3, imageDigestV3, deltaV3, "sha256:source-v2")
	target = targetV3
	desiredV3 := &v1beta1.DeviceSpec{
		Os:           desiredV2WithUnrelatedOSUpdate.Os,
		Applications: &[]v1beta1.ApplicationProviderSpec{makeApplication(imageV3)},
	}
	completePrefetch(refV3, desiredV2WithUnrelatedOSUpdate, desiredV3)
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, manager.Status(ctx, deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotRequired, deviceStatus.Applications[0].LastDelta.Outcome)
	newTargetID := deltastatus.Fingerprint(string(refV3.owner), refV3.image)
	require.Equal(t, map[string]imageRef{newTargetID: refV3}, manager.deltaTargetRefs)
	appResults := restartedStore.ApplicationResults("app")
	require.NotContains(t, appResults, targetID)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotRequired, appResults[newTargetID].Status.Outcome)
}

func TestBeforeUpdatePreservesCurrentDeltaResultsAcrossRetries(t *testing.T) {
	const (
		image        = "quay.io/acme/app:target"
		targetDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		candidate    = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		sourceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)

	tests := []struct {
		name                  string
		withStatusStore       bool
		withoutApplicationKey bool
	}{
		{
			name: "When persistence is disabled it should preserve the in-memory result",
		},
		{
			name:                  "When no persisted target key is available it should preserve the in-memory result",
			withStatusStore:       true,
			withoutApplicationKey: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			logger := log.NewPrefixLogger("test")
			ctrl := gomock.NewController(t)
			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 1)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0).Times(2)
			mockResources := resource.NewMockManager(ctrl)
			mockResources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(3)
			podman := client.NewPodman(logger, mockExec, rw, poll.NewConfig(time.Millisecond, 2))

			var options []PrefetchManagerOption
			if tt.withStatusStore {
				options = append(options, WithDeltaStatusStore(deltastatus.New(rw, "/var/lib/flightctl", logger)))
			}
			manager := NewPrefetchManager(
				logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				func(v1beta1.Username) (*client.Skopeo, error) { return nil, nil },
				client.NewCLIClients(),
				rw,
				util.Duration(time.Minute),
				mockResources,
				poll.Config{},
				options...,
			)
			defer manager.Cleanup()

			application := v1beta1.ContainerApplication{Name: lo.ToPtr("app"), AppType: v1beta1.AppTypeContainer}
			require.NoError(t, application.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
			var applicationSpec v1beta1.ApplicationProviderSpec
			require.NoError(t, applicationSpec.FromContainerApplication(application))
			desired := &v1beta1.DeviceSpec{Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{applicationSpec})}

			target := OCIPullTarget{
				Type:      OCITypePodmanImage,
				Reference: image,
				Digest:    targetDigest,
				Delta: &OCIDeltaTarget{
					Hint:         candidate,
					SourceDigest: sourceDigest,
					Application:  "app",
				},
			}
			manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
				return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: {target}}}, nil
			}))

			ctx := context.Background()
			require.ErrorIs(t, manager.BeforeUpdate(ctx, nil, desired), errors.ErrPrefetchNotReady)
			ref := imageRef{image: image, owner: v1beta1.CurrentProcessUsername}
			task := manager.tasks[ref]
			require.NotNil(t, task)
			if tt.withoutApplicationKey {
				manager.mu.Lock()
				manager.deltaAppTargetKeys = nil
				manager.mu.Unlock()
			}
			manager.recordDeltaApplied(ref, task)
			manager.setResult(ref, nil)

			require.NoError(t, manager.BeforeUpdate(ctx, desired, desired))
			generation := manager.deltaGeneration
			changed := *desired
			changed.Os = &v1beta1.DeviceOsSpec{Image: "quay.io/acme/os:changed"}
			require.NoError(t, manager.BeforeUpdate(ctx, desired, &changed))
			require.Equal(t, generation, manager.deltaGeneration)
			deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
			require.NoError(t, manager.Status(ctx, deviceStatus))
			require.NotNil(t, deviceStatus.Applications[0].LastDelta)
			require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestApplicationDeltaPrefetchRunsAsRunAsUser(t *testing.T) {
	const image = "quay.io/acme/app:target"
	const candidate = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const runAsUser v1beta1.Username = "delta-user"

	ctrl := gomock.NewController(t)
	userExec := executer.NewMockExecuter(ctrl)

	root := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	userSkopeo := client.NewSkopeo(logger, userExec, readWriter)
	userPodman := client.NewPodman(logger, userExec, readWriter, poll.NewConfig(time.Millisecond, 2))
	manager := &prefetchManager{
		log:        logger,
		readWriter: readWriter,
		readWriterFactory: func(username v1beta1.Username) (fileio.ReadWriter, error) {
			require.Equal(t, runAsUser, username)
			return readWriter, nil
		},
		ociDeltaFactory: func(username v1beta1.Username) (*client.OCIDelta, error) {
			require.Equal(t, runAsUser, username)
			return client.NewOCIDelta(logger, userExec, time.Minute), nil
		},
		pullTimeout:           time.Minute,
		tasks:                 make(map[imageRef]*prefetchTask),
		deltaGeneration:       1,
		deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
		deltaTargetsScheduled: true,
	}
	target := imageRef{image: image, owner: runAsUser}
	task := &prefetchTask{delta: &OCIDeltaTarget{Hint: candidate, Application: "app"}, deltaGeneration: 1}
	task.applicationTargetKey = applicationImageTargetKeyFor(target, "", OCITypePodmanImage)
	manager.deltaAppTargetKeys = map[string]map[string]string{
		"app": {deltastatus.Fingerprint(string(target.owner), target.image): task.applicationTargetKey},
	}
	manager.deltaTargetRefs = map[string]imageRef{deltastatus.Fingerprint(string(target.owner), target.image): target}
	manager.tasks[target] = task

	deltaFetch := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "copy", "docker://"+candidate, gomock.Any(),
	).Return("", "", 0)
	deltaImport := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any(),
	).Return("", "", 0)
	canonicalPull := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", "pull", image,
	).Return("", "", 0)
	gomock.InOrder(deltaFetch, deltaImport, canonicalPull)

	err := manager.pullApplicationImage(context.Background(), target, task, userPodman, userSkopeo, client.Timeout(time.Minute))
	require.NoError(t, err)
	task.done = true

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	matches, err := filepath.Glob(filepath.Join(root, "tmp", "application-delta*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestApplicationDeltaPrefetchRunAsDeltaImportFailureFallsBackToFullPull(t *testing.T) {
	const image = "quay.io/acme/app:target"
	const candidate = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const runAsUser v1beta1.Username = "delta-user"

	ctrl := gomock.NewController(t)
	userExec := executer.NewMockExecuter(ctrl)
	root := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	userSkopeo := client.NewSkopeo(logger, userExec, readWriter)
	userPodman := client.NewPodman(logger, userExec, readWriter, poll.NewConfig(time.Millisecond, 2))
	manager := &prefetchManager{
		log:        logger,
		readWriter: readWriter,
		readWriterFactory: func(username v1beta1.Username) (fileio.ReadWriter, error) {
			require.Equal(t, runAsUser, username)
			return readWriter, nil
		},
		ociDeltaFactory: func(username v1beta1.Username) (*client.OCIDelta, error) {
			require.Equal(t, runAsUser, username)
			return client.NewOCIDelta(logger, userExec, time.Minute), nil
		},
		pullTimeout:           time.Minute,
		tasks:                 make(map[imageRef]*prefetchTask),
		deltaGeneration:       1,
		deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
		deltaTargetsScheduled: true,
	}
	target := imageRef{image: image, owner: runAsUser}
	task := &prefetchTask{delta: &OCIDeltaTarget{Hint: candidate, Application: "app"}, deltaGeneration: 1}
	task.applicationTargetKey = applicationImageTargetKeyFor(target, "", OCITypePodmanImage)
	manager.deltaAppTargetKeys = map[string]map[string]string{
		"app": {deltastatus.Fingerprint(string(target.owner), target.image): task.applicationTargetKey},
	}
	manager.deltaTargetRefs = map[string]imageRef{deltastatus.Fingerprint(string(target.owner), target.image): target}
	manager.tasks[target] = task

	deltaFetch := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "copy", "docker://"+candidate, gomock.Any(),
	).Return("", "", 0)
	deltaImport := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any(),
	).Return("", "import into RunAs storage failed", 1)
	fullPull := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", "pull", image,
	).Return("", "", 0)
	gomock.InOrder(deltaFetch, deltaImport, fullPull)

	err := manager.pullApplicationImage(context.Background(), target, task, userPodman, userSkopeo, client.Timeout(time.Minute))
	require.NoError(t, err)
	task.done = true

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeFallback, deviceStatus.Applications[0].LastDelta.Outcome)
	require.NotNil(t, deviceStatus.Applications[0].LastDelta.FallbackReason)
	require.Contains(t, *deviceStatus.Applications[0].LastDelta.FallbackReason, "import into RunAs storage failed")

	matches, err := filepath.Glob(filepath.Join(root, "tmp", "application-delta*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestMovedApplicationTagClearsPersistedDeltaOutcome(t *testing.T) {
	const image = "quay.io/acme/app:v2"
	const oldDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const newDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cases := []struct {
		name         string
		ociType      OCIType
		reused       bool
		absent       bool
		previous     v1beta1.DeviceDeltaApplyOutcomeType
		remoteDigest string
		remoteExit   int
		localExit    int
		wantPull     bool
		want         v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{name: "When a Podman image is absent after restart and its tag moves it should replace NotRequired", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When a CRI image is absent after restart and its tag moves it should replace NotRequired", ociType: OCITypeCRIImage, absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When an absent image tag moves after Applied it should replace the persisted outcome", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeApplied, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When an absent image tag moves after Fallback it should replace the persisted outcome", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeFallback, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When an absent image digest is unchanged it should preserve Applied", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeApplied, remoteDigest: oldDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When registry inspection fails for an absent image it should preserve NotRequired", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, remoteExit: 1, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		{name: "When a CRI tag moves it should replace the persisted outcome", ociType: OCITypeCRIImage, previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When a tag moves between prepare generations it should replace Applied", reused: true, previous: v1beta1.DeviceDeltaApplyOutcomeApplied, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When a tag moves after NotRequired it should full-pull and report NotUsed", previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When a tag moves after Applied it should full-pull and report NotUsed", previous: v1beta1.DeviceDeltaApplyOutcomeApplied, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When a tag moves after Fallback it should full-pull and report NotUsed", previous: v1beta1.DeviceDeltaApplyOutcomeFallback, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When the digest is unchanged it should preserve Applied", previous: v1beta1.DeviceDeltaApplyOutcomeApplied, remoteDigest: oldDigest, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When registry inspection fails it should preserve NotRequired", previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, remoteExit: 1, want: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		{name: "When local inspection fails it should preserve Applied", previous: v1beta1.DeviceDeltaApplyOutcomeApplied, localExit: 1, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			target := imageRef{image: image}
			targetID := deltastatus.Fingerprint(string(target.owner), image)
			ociType := tt.ociType
			if ociType == "" {
				ociType = OCITypePodmanImage
			}
			targetKey := applicationImageTargetKeyFor(target, "", ociType)
			specKeys := map[string]string{"app": "unchanged-app-spec"}
			targetKeys := map[string]map[string]string{"app": {targetID: targetKey}}
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			require.NoError(store.ReconcileApplicationSpecs(specKeys))
			require.NoError(store.ReconcileApplicationTargets(targetKeys, true))
			require.NoError(store.RecordApplicationResult("app", specKeys["app"], targetID, targetKey, v1beta1.DeviceDeltaApplyStatus{Outcome: tt.previous}))
			restarted := deltastatus.New(rw, "/var/lib/flightctl", logger)
			podman := client.NewPodman(logger, mockExec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, mockExec, rw)
			m := &prefetchManager{
				log: logger, tasks: make(map[imageRef]*prefetchTask), deltaGeneration: 1,
				cliClients:       client.NewCLIClients(client.WithCRIClient(client.NewCRI(logger, mockExec, rw, poll.NewConfig(time.Millisecond, 2)))),
				podmanFactory:    func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				skopeoFactory:    func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				deltaStatusStore: restarted, deltaAppSpecKeys: specKeys, deltaAppTargetKeys: targetKeys,
				deltaTargetRefs: map[string]imageRef{targetID: target}, deltaTargetsScheduled: true, pullTimeout: time.Minute,
			}
			m.deltaApplyResults = m.restoreApplicationDeltaResults(targetKeys, m.deltaTargetRefs)
			require.Equal(tt.previous, m.deltaApplyResults["app"][target].outcome)
			delta := &OCIDeltaTarget{Application: "app"}
			existsCalls := 2
			existsExit := 0
			criImages := "IMAGE TAG ID\napp v2 abc"
			if tt.absent {
				m.ociDelta = client.NewOCIDelta(logger, mockExec, time.Minute)
				delta.SourceDigest = oldDigest
				existsCalls = 1
				existsExit = 1
				criImages = ""
			}
			if tt.reused {
				m.tasks[target] = &prefetchTask{ociType: ociType, applicationTargetKey: targetKey, done: true, deltaGeneration: 0, resolvedDigest: oldDigest}
				existsCalls = 1
			}
			if ociType == OCITypeCRIImage {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "images", image).Return(criImages, "", 0).Times(existsCalls)
				if !tt.absent {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", image).Return(fmt.Sprintf(`{"status":{"repoDigests":[%q]}}`, oldDigest), "inspect failed", tt.localExit)
				}
			} else {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", existsExit).Times(existsCalls)
				if !tt.absent {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).Return(fmt.Sprintf(`{"Digest": %q}`, oldDigest), "inspect failed", tt.localExit)
				}
			}
			if tt.localExit == 0 {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image).Return(tt.remoteDigest, "inspect failed", tt.remoteExit)
			}
			ctx := context.Background()
			needsQueue, err := m.prepareTask(ctx, target, ociType, "", nil, delta)
			require.NoError(err)
			require.Equal(tt.wantPull, needsQueue)
			if tt.wantPull {
				if !tt.absent && tt.remoteDigest == newDigest {
					// The old result must already be gone, including if the agent crashes before pulling.
					require.Empty(m.deltaApplyResults["app"])
					require.Empty(deltastatus.New(rw, "/var/lib/flightctl", logger).ApplicationResults("app"))
				}
				if tt.absent && tt.remoteDigest == newDigest {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "list-referrers", "docker://"+image).Return(`{"manifests": []}`, "", 0)
				}
				assertInvalidated := func() {
					if tt.absent && tt.remoteDigest == newDigest {
						// Persist invalidation before pulling, even if the agent crashes mid-pull.
						require.Empty(m.deltaApplyResults["app"])
						require.Empty(deltastatus.New(rw, "/var/lib/flightctl", logger).ApplicationResults("app"))
					}
				}
				if ociType == OCITypeCRIImage {
					mockExec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"pull", image}, gomock.Any()).DoAndReturn(func(context.Context, string, string, []string, ...string) (string, string, int) {
						assertInvalidated()
						return "", "", 0
					})
				} else {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
						assertInvalidated()
						return "", "", 0
					})
				}
				require.NoError(m.pull(ctx, target, m.tasks[target]))
				m.tasks[target].done = true
			}
			status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
			require.NoError(m.Status(ctx, status))
			require.NotNil(status.Applications[0].LastDelta)
			require.Equal(tt.want, status.Applications[0].LastDelta.Outcome)
			require.Equal(tt.want, deltastatus.New(rw, "/var/lib/flightctl", logger).ApplicationResults("app")[targetID].Status.Outcome)
		})
	}
}
