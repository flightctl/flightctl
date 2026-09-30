package dependency

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
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
	}{
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
			name:        "missing candidate full-pulls without fallback",
			delta:       &OCIDeltaTarget{Application: "app"},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
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
			target := imageRef{image: image}
			task := &prefetchTask{delta: tt.delta, deltaGeneration: 1}
			manager := &prefetchManager{
				log:               logger,
				readWriter:        rw,
				pullTimeout:       time.Minute,
				ociDelta:          client.NewOCIDelta(logger, exec, time.Minute),
				tasks:             map[imageRef]*prefetchTask{target: task},
				deltaGeneration:   1,
				deltaApplyResults: make(map[string]map[imageRef]applicationDeltaApplyResult),
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

func TestAggregateApplicationDeltaApplyResults(t *testing.T) {
	imageA := imageRef{image: "quay.io/acme/app-a:target"}
	imageB := imageRef{image: "quay.io/acme/app-b:target"}
	tests := []struct {
		name         string
		results      map[imageRef]applicationDeltaApplyResult
		wantOutcome  v1beta1.DeviceDeltaApplyOutcomeType
		wantFallback *string
	}{
		{
			name: "When all image deltas apply it should report Applied",
			results: map[imageRef]applicationDeltaApplyResult{
				imageA: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
				imageB: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
		},
		{
			name: "When all image deltas fall back it should report Fallback",
			results: map[imageRef]applicationDeltaApplyResult{
				imageA: {outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, fallbackReason: "import failed"},
			},
			wantOutcome:  v1beta1.DeviceDeltaApplyOutcomeFallback,
			wantFallback: lo.ToPtr("import failed"),
		},
		{
			name: "When image targets have mixed delta results it should report Partial",
			results: map[imageRef]applicationDeltaApplyResult{
				imageA: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
				imageB: {outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, fallbackReason: "import failed"},
			},
			wantOutcome:  v1beta1.DeviceDeltaApplyOutcomePartial,
			wantFallback: lo.ToPtr("import failed"),
		},
		{
			name: "When no image target uses a delta it should report NotUsed",
			results: map[imageRef]applicationDeltaApplyResult{
				imageA: {outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
				imageB: {outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
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
		pullTimeout:       time.Minute,
		tasks:             make(map[imageRef]*prefetchTask),
		deltaGeneration:   1,
		deltaApplyResults: make(map[string]map[imageRef]applicationDeltaApplyResult),
	}
	target := imageRef{image: image, owner: runAsUser}
	task := &prefetchTask{delta: &OCIDeltaTarget{Hint: candidate, Application: "app"}, deltaGeneration: 1}
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
		pullTimeout:       time.Minute,
		tasks:             make(map[imageRef]*prefetchTask),
		deltaGeneration:   1,
		deltaApplyResults: make(map[string]map[imageRef]applicationDeltaApplyResult),
	}
	target := imageRef{image: image, owner: runAsUser}
	task := &prefetchTask{delta: &OCIDeltaTarget{Hint: candidate, Application: "app"}, deltaGeneration: 1}
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
