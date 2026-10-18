package dependency

import (
	"context"
	"fmt"
	"os"
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

func TestApplicationDeltaPrefetchCRIRefreshesRegistryReference(t *testing.T) {
	const (
		image        = "quay.io/acme/app:target"
		candidate    = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		refreshImage = "quay.io/acme/app@" + podmanDeltaTargetDigest
	)

	testCases := []struct {
		name              string
		image             string
		refreshImage      string
		refreshExitCode   int
		refreshStderr     string
		wantFallback      bool
		fallbackReasonHas string
	}{
		{
			name: "When the canonical registry reference refresh succeeds it should clear delta fallback status",
		},
		{
			name:         "When the target uses a short name it should preserve runtime registry resolution during refresh",
			image:        "nginx:latest",
			refreshImage: "nginx@" + podmanDeltaTargetDigest,
		},
		{
			name:              "When the canonical registry reference refresh fails it should full-pull and report the fallback",
			refreshExitCode:   1,
			refreshStderr:     "registry manifest refresh failed",
			wantFallback:      true,
			fallbackReasonHas: "refresh registry manifest after delta import",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			image, refreshImage := image, refreshImage
			if tc.image != "" {
				image, refreshImage = tc.image, tc.refreshImage
			}
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)

			exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+candidate).Return(podmanDeltaManifest, "", 0)
			expectPodmanDeltaTarget(t, exec, image, 1)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "images", "--output", "json").Return(`{"images":[{"repoDigests":["quay.io/local/base@`+podmanDeltaSource+`"]}]}`, "", 0)
			runtimeInfo := exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "version").Return("RuntimeName: cri-o\n", "", 0)
			copyDelta := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "skopeo", "copy", "docker://"+candidate, gomock.Any(),
			).Return("", "", 0)
			importDelta := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any(),
			).Return("", "", 0)
			imageExists := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "crictl", "images", image,
			).Return("IMAGE TAG IMAGE ID SIZE\nquay.io/acme/app target sha256:cccc 1MB\n", "", 0)

			refreshEnv := make([]any, len(os.Environ()))
			for i := range refreshEnv {
				refreshEnv[i] = gomock.Any()
			}
			refreshRegistryReference := exec.EXPECT().ExecuteWithContextFromDir(
				gomock.Any(), "", "crictl", []string{"pull", refreshImage}, refreshEnv...,
			).Return("Image is up to date", tc.refreshStderr, tc.refreshExitCode)

			if tc.wantFallback {
				fullPullEnv := make([]any, len(os.Environ()))
				for i := range fullPullEnv {
					fullPullEnv[i] = gomock.Any()
				}
				fullPull := exec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), "", "crictl", []string{"pull", image}, fullPullEnv...,
				).Return("Image pulled", "", 0)
				gomock.InOrder(runtimeInfo, copyDelta, importDelta, imageExists, refreshRegistryReference, fullPull)
			} else {
				activation := expectCRIDeltaActivation(exec, image, refreshImage)
				gomock.InOrder(append([]any{runtimeInfo, copyDelta, importDelta, imageExists, refreshRegistryReference}, activation...)...)
			}

			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			logger := log.NewPrefixLogger("test")
			cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			target := imageRef{image: image, ociType: OCITypeCRIImage}
			task := &prefetchTask{
				delta:                &OCIDeltaTarget{Hint: candidate, Application: "app"},
				deltaGeneration:      1,
				applicationTargetKey: applicationImageTargetKeyFor(target, "", OCITypeCRIImage),
			}
			manager := &prefetchManager{
				log:                   logger,
				cliClients:            client.NewCLIClients(client.WithCRIClient(cri)),
				readWriter:            rw,
				pullTimeout:           time.Minute,
				ociDelta:              client.NewOCIDelta(logger, exec, time.Minute),
				tasks:                 map[imageRef]*prefetchTask{target: task},
				deltaGeneration:       1,
				deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
				deltaTargetsScheduled: true,
				deltaTargetRefs:       map[string]imageRef{target.targetID(): target},
				deltaAppTargetKeys: map[string]map[string]string{
					"app": {target.targetID(): task.applicationTargetKey},
				},
			}

			err := manager.pullCRIImage(context.Background(), target, task, skopeo, client.Timeout(time.Minute))
			require.NoError(t, err)
			task.done = true

			deviceStatus := &v1beta1.DeviceStatus{
				Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}},
			}
			require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
			if tc.wantFallback {
				require.NotNil(t, deviceStatus.Applications[0].LastDelta)
				require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeFallback, deviceStatus.Applications[0].LastDelta.Outcome)
				require.NotNil(t, deviceStatus.Applications[0].LastDelta.FallbackReason)
				require.Contains(t, *deviceStatus.Applications[0].LastDelta.FallbackReason, tc.fallbackReasonHas)
			} else {
				require.NotNil(t, deviceStatus.Applications[0].LastDelta)
				require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
			}
		})
	}
}

func expectCRIDeltaActivation(exec *executer.MockExecuter, image, refreshImage string) []any {
	repository, _, _, err := normalizedCRIImageReferences(image)
	if err != nil {
		panic(err)
	}
	metadata := fmt.Sprintf(
		`{"status":{"id":%q,"repoDigests":[%q]}}`,
		podmanDeltaImageID, repository+"@"+podmanDeltaTargetDigest,
	)
	return []any{
		exec.EXPECT().ExecuteWithContext(
			gomock.Any(), "crictl", "inspecti", "--output", "json", refreshImage,
		).Return(metadata, "", 0),
		exec.EXPECT().ExecuteWithContext(
			gomock.Any(), "crictl", "rmi", image,
		).Return("", "", 0),
		exec.EXPECT().ExecuteWithContext(
			gomock.Any(), "skopeo", "copy", "--preserve-digests", "docker://"+refreshImage, "containers-storage:"+image,
		).Return("", "", 0),
		exec.EXPECT().ExecuteWithContext(
			gomock.Any(), "crictl", "inspecti", "--output", "json", image,
		).Return(metadata, "", 0),
	}
}

func TestWhenCRICacheIdentityIsUnavailableItShouldWithholdNotRequired(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/app:v2"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "images", image).Return("IMAGE TAG ID\napp v2 image-id", "", 0)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	target := imageRef{image: image, ociType: OCITypeCRIImage}
	targetID := target.targetID()
	targetKey := applicationImageTargetKeyFor(target, digest, OCITypeCRIImage)
	task := &prefetchTask{
		ociType:              OCITypeCRIImage,
		targetDigest:         digest,
		applicationTargetKey: targetKey,
		targetPresent:        true,
		delta:                &OCIDeltaTarget{Hint: "quay.io/acme/delta:target", Application: application},
		deltaGeneration:      1,
	}
	manager := &prefetchManager{
		log:                   logger,
		cliClients:            client.NewCLIClients(client.WithCRIClient(cri)),
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

	manager.mu.Lock()
	needsQueue := manager.checkCachedApplicationTask(context.Background(), target, task)
	manager.mu.Unlock()
	require.False(t, needsQueue)
	require.True(t, task.done)

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.Nil(t, deviceStatus.Applications[0].LastDelta)
}

func TestApplicationDeltaCRIHintedCandidateFallsBackWhenDeltaClientIsUnavailable(t *testing.T) {
	require := require.New(t)
	const (
		application = "app"
		image       = "quay.io/acme/app:target"
		candidate   = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	pullEnv := make([]any, len(os.Environ()))
	for i := range pullEnv {
		pullEnv[i] = gomock.Any()
	}
	exec.EXPECT().ExecuteWithContextFromDir(
		gomock.Any(), "", "crictl", []string{"pull", image}, pullEnv...,
	).Return("Image pulled", "", 0)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	target := imageRef{image: image, ociType: OCITypeCRIImage}
	targetID := target.targetID()
	targetKey := applicationImageTargetKeyFor(target, "sha256:target", OCITypeCRIImage)
	task := &prefetchTask{
		ociType:              OCITypeCRIImage,
		targetDigest:         "sha256:target",
		applicationTargetKey: targetKey,
		delta:                &OCIDeltaTarget{Hint: candidate, Application: application},
		deltaGeneration:      1,
	}
	manager := &prefetchManager{
		log: logger,
		skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) {
			return skopeo, nil
		},
		cliClients:            client.NewCLIClients(client.WithCRIClient(cri)),
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

	require.NoError(manager.pullCRIImage(context.Background(), target, task, skopeo, client.Timeout(time.Minute)))
	task.done = true

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.NotNil(deviceStatus.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, deviceStatus.Applications[0].LastDelta.Outcome)
	require.NotNil(deviceStatus.Applications[0].LastDelta.FallbackReason)
	require.Contains(*deviceStatus.Applications[0].LastDelta.FallbackReason, "OCI delta client is unavailable")
}

func TestApplicationDeltaCRIMissingImageReportsNotUsedWhenSourceDigestMatches(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	inspectTarget := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image,
	).Return(digest, "", 0)
	targetIndex, _ := testApplicationTargetIndex(t, digest)
	inspectPlatform := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image,
	).Return(targetIndex, "", 0)
	pullEnv := make([]any, len(os.Environ()))
	for i := range pullEnv {
		pullEnv[i] = gomock.Any()
	}
	referrers := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "list-referrers", "docker://quay.io/acme/app@"+digest).Return(`{"manifests":[]}`, "", 0)
	fullPull := exec.EXPECT().ExecuteWithContextFromDir(
		gomock.Any(), "", "crictl", []string{"pull", image}, pullEnv...,
	).Return("Image pulled", "", 0)
	gomock.InOrder(inspectTarget, inspectPlatform, referrers, fullPull)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	target := imageRef{image: image, ociType: OCITypeCRIImage}
	targetID := target.targetID()
	targetKey := applicationImageTargetKeyFor(target, digest, OCITypeCRIImage)
	task := &prefetchTask{
		ociType:              OCITypeCRIImage,
		targetDigest:         digest,
		applicationTargetKey: targetKey,
		delta:                &OCIDeltaTarget{SourceDigest: digest, Application: application},
		deltaGeneration:      1,
	}
	manager := &prefetchManager{
		log:      logger,
		ociDelta: client.NewOCIDelta(logger, exec, time.Minute),
		skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) {
			return skopeo, nil
		},
		cliClients:            client.NewCLIClients(client.WithCRIClient(cri)),
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

	require.NoError(t, manager.pullCRIImage(context.Background(), target, task, skopeo, client.Timeout(time.Minute)))
	task.done = true

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotUsed, deviceStatus.Applications[0].LastDelta.Outcome)
}
