package dependency

import (
	"context"
	"fmt"
	"os"
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

const deltaActivationCandidate = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestWhenADeltaIsRefreshedItShouldActivateTheInstallationSelector(t *testing.T) {
	const refreshImage = "quay.io/acme/app@" + podmanDeltaTargetDigest
	tests := []struct {
		name              string
		runtime           string
		image             string
		setupExpectations func(*testing.T, *executer.MockExecuter, string)
	}{
		{
			name:    "When a tagged Podman delta is refreshed it should activate the installation selector",
			runtime: "podman",
			image:   "quay.io/acme/app:target",
			setupExpectations: func(t *testing.T, exec *executer.MockExecuter, image string) {
				t.Helper()
				const canonicalImageID = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
				targetIndex, _ := testApplicationTargetIndex(t, podmanDeltaTargetDigest)

				targetMissing := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "image", "exists", image,
				).Return("", "", 1)
				deltaManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+deltaActivationCandidate,
				).Return(podmanDeltaManifest, "", 0)
				targetManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image,
				).Return(targetIndex, "", 0)
				sourceListing := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "image", "ls", "--format", client.PodmanImageDigestsListFormat,
				).Return("sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\t[\"quay.io/local/base@"+podmanDeltaSource+"\"]", "", 0)
				copyDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "copy", "docker://"+deltaActivationCandidate, gomock.Any(),
				).Return("", "", 0)
				importDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "oci-delta", "import", gomock.Any(),
				).Return("", "", 0)
				refreshCanonical := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "pull", refreshImage,
				).Return(canonicalImageID, "", 0)
				activateSelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "tag", canonicalImageID, image,
				).Return("", "", 0)
				verifySelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "image", "inspect", "--format", "{{.Id}}", image,
				).Return(canonicalImageID, "", 0)
				observedDigest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", canonicalImageID,
				).Return(podmanDeltaTargetDigest, "", 0)
				gomock.InOrder(
					targetMissing, deltaManifest, targetManifest, sourceListing, copyDelta,
					importDelta, refreshCanonical, activateSelector, verifySelector, observedDigest,
				)
			},
		},
		{
			name:    "When a tagged CRI-O delta is refreshed it should activate the installation selector",
			runtime: client.CRIRuntimeCRIO,
			image:   "quay.io/acme/app:target",
			setupExpectations: func(t *testing.T, exec *executer.MockExecuter, image string) {
				t.Helper()
				const canonicalImageID = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
				targetIndex, _ := testApplicationTargetIndex(t, podmanDeltaTargetDigest)
				canonicalMetadata := fmt.Sprintf(
					`{"status":{"id":%q,"repoDigests":[%q]}}`,
					canonicalImageID, "quay.io/acme/app@"+podmanDeltaTargetDigest,
				)

				targetMissing := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", image,
				).Return("IMAGE TAG IMAGE ID SIZE\n", "", 0)
				deltaManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+deltaActivationCandidate,
				).Return(podmanDeltaManifest, "", 0)
				targetManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image,
				).Return(targetIndex, "", 0)
				localSources := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", "--output", "json",
				).Return(`{"images":[{"repoDigests":["quay.io/local/base@`+podmanDeltaSource+`"]}]}`, "", 0)
				runtimeInfo := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "version",
				).Return("RuntimeName: cri-o\n", "", 0)
				copyDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "copy", "docker://"+deltaActivationCandidate, gomock.Any(),
				).Return("", "", 0)
				importDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "oci-delta", "import", gomock.Any(),
				).Return("", "", 0)

				pullEnv := make([]any, len(os.Environ()))
				for i := range pullEnv {
					pullEnv[i] = gomock.Any()
				}
				refreshCanonical := exec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), "", "crictl", []string{"pull", refreshImage}, pullEnv...,
				).Return("Image is up to date for "+refreshImage, "", 0)
				inspectCanonical := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", refreshImage,
				).Return(canonicalMetadata, "", 0)
				activateSelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "copy", "--preserve-digests", "docker://"+refreshImage, "containers-storage:"+image,
				).Return("", "", 0)
				verifySelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				observeSelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				verifyObservedDigest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/acme/app@"+podmanDeltaTargetDigest, "--no-creds",
				).Return(targetIndex, "", 0)
				gomock.InOrder(
					targetMissing, deltaManifest, targetManifest, localSources, runtimeInfo,
					copyDelta, importDelta, refreshCanonical,
					inspectCanonical, activateSelector, verifySelector,
					observeSelector, verifyObservedDigest,
				)
			},
		},
		{
			name:    "When a CRI-O target is digest-pinned it should refresh before verifying canonical visibility",
			runtime: client.CRIRuntimeCRIO,
			image:   refreshImage,
			setupExpectations: func(t *testing.T, exec *executer.MockExecuter, image string) {
				t.Helper()
				const canonicalImageID = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
				targetIndex, _ := testApplicationTargetIndex(t, podmanDeltaTargetDigest)
				canonicalMetadata := fmt.Sprintf(
					`{"status":{"id":%q,"repoDigests":[%q]}}`,
					canonicalImageID, image,
				)

				targetMissing := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", image,
				).Return("IMAGE TAG IMAGE ID SIZE\n", "", 0)
				deltaManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+deltaActivationCandidate,
				).Return(podmanDeltaManifest, "", 0)
				targetManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image,
				).Return(targetIndex, "", 0)
				localSources := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", "--output", "json",
				).Return(`{"images":[{"repoDigests":["quay.io/local/base@`+podmanDeltaSource+`"]}]}`, "", 0)
				runtimeInfo := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "version",
				).Return("RuntimeName: cri-o\n", "", 0)
				copyDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "copy", "docker://"+deltaActivationCandidate, gomock.Any(),
				).Return("", "", 0)
				importDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any(),
				).Return("", "", 0)
				pullEnv := make([]any, len(os.Environ()))
				for i := range pullEnv {
					pullEnv[i] = gomock.Any()
				}
				refreshCanonical := exec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), "", "crictl", []string{"pull", image}, pullEnv...,
				).Return("Image is up to date for "+image, "", 0)
				inspectCanonical := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				verifySelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				observeSelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				verifyObservedDigest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image, "--no-creds",
				).Return(targetIndex, "", 0)
				gomock.InOrder(
					targetMissing, deltaManifest, targetManifest, localSources, runtimeInfo,
					copyDelta, importDelta, refreshCanonical,
					inspectCanonical, verifySelector, observeSelector, verifyObservedDigest,
				)
			},
		},
		{
			name:    "When a tagged containerd delta is refreshed it should activate the installation selector",
			runtime: client.CRIRuntimeContainerd,
			image:   "quay.io/acme/app:target",
			setupExpectations: func(t *testing.T, exec *executer.MockExecuter, image string) {
				t.Helper()
				const canonicalImageID = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
				targetIndex, _ := testApplicationTargetIndex(t, podmanDeltaTargetDigest)
				canonicalMetadata := fmt.Sprintf(
					`{"status":{"id":%q,"repoDigests":[%q]}}`,
					canonicalImageID, "quay.io/acme/app@"+podmanDeltaTargetDigest,
				)

				targetMissing := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", image,
				).Return("IMAGE TAG IMAGE ID SIZE\n", "", 0)
				deltaManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+deltaActivationCandidate,
				).Return(podmanDeltaManifest, "", 0)
				targetManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image,
				).Return(targetIndex, "", 0)
				localSources := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", "--output", "json",
				).Return(`{"images":[{"repoDigests":["quay.io/local/base@`+podmanDeltaSource+`"]}]}`, "", 0)
				runtimeInfo := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "version",
				).Return("RuntimeName: containerd\n", "", 0)
				copyDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "copy", "docker://"+deltaActivationCandidate, gomock.Any(),
				).Return("", "", 0)
				mountSource := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "mount", "quay.io/local/base@"+podmanDeltaSource, gomock.Any(),
				).Return("", "", 0)
				reconstructDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "oci-delta", "apply", "--directory", gomock.Any(), gomock.Any(), gomock.Any(),
				).Return("", "", 0)
				importReconstructed := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "import", "--base-name", gomock.Any(), "--index-name", gomock.Any(), gomock.Any(),
				).Return("", "", 0)
				tagReconstructed := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "tag", "--force", gomock.Any(), image,
				).Return("", "", 0)
				removeTemporary := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "rm", gomock.Any(),
				).Return("", "", 0)
				unmountSource := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "unmount", gomock.Any(),
				).Return("", "", 0)
				reconstructedVisible := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", image,
				).Return("IMAGE TAG IMAGE ID SIZE\nquay.io/acme/app target sha256:5555555555555555555555555555555555555555555555555555555555555555 1MB\n", "", 0)

				pullEnv := make([]any, len(os.Environ()))
				for i := range pullEnv {
					pullEnv[i] = gomock.Any()
				}
				refreshCanonical := exec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), "", "crictl", []string{"pull", refreshImage}, pullEnv...,
				).Return("Image is up to date for "+refreshImage, "", 0)
				inspectCanonical := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", refreshImage,
				).Return(canonicalMetadata, "", 0)
				activateSelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "tag", "--force", refreshImage, image,
				).Return("", "", 0)
				verifySelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				observeSelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				verifyObservedDigest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/acme/app@"+podmanDeltaTargetDigest, "--no-creds",
				).Return(targetIndex, "", 0)
				gomock.InOrder(
					targetMissing, deltaManifest, targetManifest, localSources, runtimeInfo,
					copyDelta, mountSource, reconstructDelta, importReconstructed, tagReconstructed,
					removeTemporary, unmountSource, reconstructedVisible, refreshCanonical,
					inspectCanonical, activateSelector, verifySelector, observeSelector,
					verifyObservedDigest,
				)
			},
		},
		{
			name:    "When a containerd target is digest-pinned it should verify without retagging",
			runtime: client.CRIRuntimeContainerd,
			image:   refreshImage,
			setupExpectations: func(t *testing.T, exec *executer.MockExecuter, image string) {
				t.Helper()
				const canonicalImageID = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
				targetIndex, _ := testApplicationTargetIndex(t, podmanDeltaTargetDigest)
				canonicalMetadata := fmt.Sprintf(
					`{"status":{"id":%q,"repoDigests":[%q]}}`,
					canonicalImageID, image,
				)

				targetMissing := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", image,
				).Return("IMAGE TAG IMAGE ID SIZE\n", "", 0)
				deltaManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+deltaActivationCandidate,
				).Return(podmanDeltaManifest, "", 0)
				targetManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image,
				).Return(targetIndex, "", 0)
				localSources := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", "--output", "json",
				).Return(`{"images":[{"repoDigests":["quay.io/local/base@`+podmanDeltaSource+`"]}]}`, "", 0)
				runtimeInfo := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "version",
				).Return("RuntimeName: containerd\n", "", 0)
				copyDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "copy", "docker://"+deltaActivationCandidate, gomock.Any(),
				).Return("", "", 0)
				mountSource := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "mount", "quay.io/local/base@"+podmanDeltaSource, gomock.Any(),
				).Return("", "", 0)
				reconstructDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "oci-delta", "apply", "--directory", gomock.Any(), gomock.Any(), gomock.Any(),
				).Return("", "", 0)
				importReconstructed := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "import", "--base-name", gomock.Any(), "--index-name", gomock.Any(), gomock.Any(),
				).Return("", "", 0)
				tagReconstructed := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "tag", "--force", gomock.Any(), image,
				).Return("", "", 0)
				removeTemporary := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "rm", gomock.Any(),
				).Return("", "", 0)
				unmountSource := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io",
					"images", "unmount", gomock.Any(),
				).Return("", "", 0)
				reconstructedVisible := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "images", image,
				).Return("IMAGE TAG IMAGE ID SIZE\nquay.io/acme/app "+podmanDeltaTargetDigest+" sha256:5555555555555555555555555555555555555555555555555555555555 1MB\n", "", 0)

				pullEnv := make([]any, len(os.Environ()))
				for i := range pullEnv {
					pullEnv[i] = gomock.Any()
				}
				refreshCanonical := exec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), "", "crictl", []string{"pull", image}, pullEnv...,
				).Return("Image is up to date for "+image, "", 0)
				inspectCanonical := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				verifySelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				observeSelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "crictl", "inspecti", "--output", "json", image,
				).Return(canonicalMetadata, "", 0)
				verifyObservedDigest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image, "--no-creds",
				).Return(targetIndex, "", 0)
				gomock.InOrder(
					targetMissing, deltaManifest, targetManifest, localSources, runtimeInfo,
					copyDelta, mountSource, reconstructDelta, importReconstructed, tagReconstructed,
					removeTemporary, unmountSource, reconstructedVisible, refreshCanonical,
					inspectCanonical, verifySelector, observeSelector, verifyObservedDigest,
				)
			},
		},
		{
			name:    "When a Podman target is digest-pinned it should verify without retagging",
			runtime: "podman",
			image:   refreshImage,
			setupExpectations: func(t *testing.T, exec *executer.MockExecuter, image string) {
				t.Helper()
				const canonicalImageID = "sha256:6666666666666666666666666666666666666666666666666666666666666666"
				targetIndex, _ := testApplicationTargetIndex(t, podmanDeltaTargetDigest)

				targetMissing := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "image", "exists", image,
				).Return("", "", 1)
				deltaManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+deltaActivationCandidate,
				).Return(podmanDeltaManifest, "", 0)
				targetManifest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image,
				).Return(targetIndex, "", 0)
				sourceListing := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "image", "ls", "--format", client.PodmanImageDigestsListFormat,
				).Return("sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\t[\"quay.io/local/base@"+podmanDeltaSource+"\"]", "", 0)
				copyDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "copy", "docker://"+deltaActivationCandidate, gomock.Any(),
				).Return("", "", 0)
				importDelta := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any(),
				).Return("", "", 0)
				refreshCanonical := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "pull", image,
				).Return(canonicalImageID, "", 0)
				verifySelector := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "image", "inspect", "--format", "{{.Id}}", image,
				).Return(canonicalImageID, "", 0)
				observedDigest := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", canonicalImageID,
				).Return(podmanDeltaTargetDigest, "", 0)
				gomock.InOrder(
					targetMissing, deltaManifest, targetManifest, sourceListing, copyDelta,
					importDelta, refreshCanonical, verifySelector, observedDigest,
				)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runDeltaActivationTest(t, tc.runtime, tc.image, func(exec *executer.MockExecuter) {
				tc.setupExpectations(t, exec, tc.image)
			})
		})
	}
}

func runDeltaActivationTest(t *testing.T, runtime, image string, setupExpectations func(*executer.MockExecuter)) {
	t.Helper()
	const application = "app"
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	setupExpectations(exec)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	clients := client.NewCLIClients()
	targetType := OCITypePodmanImage
	if runtime != "podman" {
		clients = client.NewCLIClients(client.WithCRIClient(client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))))
		targetType = OCITypeCRIImage
	}
	resources := resource.NewMockManager(ctrl)
	resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
	manager := NewPrefetchManager(logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
		clients, rw, util.Duration(time.Second), resources, poll.NewConfig(time.Millisecond, 2),
		WithOCIDelta(client.NewOCIDelta(logger, exec, time.Minute)), WithDeltaStatusStore(store))
	defer manager.Cleanup()
	containerApplication := v1beta1.ContainerApplication{Name: lo.ToPtr(application), AppType: v1beta1.AppTypeContainer}
	require.NoError(t, containerApplication.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
	var applicationSpec v1beta1.ApplicationProviderSpec
	require.NoError(t, applicationSpec.FromContainerApplication(containerApplication))
	desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{applicationSpec}}
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: {{
			Type: targetType, Reference: image, Digest: podmanDeltaTargetDigest, PullPolicy: v1beta1.PullIfNotPresent,
			Delta: &OCIDeltaTarget{Hint: deltaActivationCandidate, SourceDigest: podmanDeltaSource, Application: application},
		}}}}, nil
	}))
	stopWorker := startPrefetchWorker(t, manager)
	defer stopWorker()
	require.ErrorIs(t, manager.BeforeUpdate(t.Context(), nil, desired), errors.ErrPrefetchNotReady)
	require.Eventually(t, func() bool { return manager.IsReady(t.Context()) }, time.Second, time.Millisecond)
	result := store.ObservedApplicationResult(application, []deltastatus.ApplicationImage{{
		Image: image, Owner: string(v1beta1.RootUsername), OCIType: string(targetType), Digest: podmanDeltaTargetDigest,
	}}, true)
	require.NotNil(t, result)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, result.Outcome)
	require.NotEmpty(t, store.ApplicationLifetime(application))
}

func TestWhenPodmanDeltaActivationFailsItShouldFallBackWithoutApplied(t *testing.T) {
	const (
		application         = "app"
		image               = "quay.io/acme/app:target"
		candidate           = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		refreshImage        = "quay.io/acme/app@" + podmanDeltaTargetDigest
		canonicalImageID    = "sha256:8888888888888888888888888888888888888888888888888888888888888"
		reconstructedID     = "sha256:9999999999999999999999999999999999999999999999999999999999999"
		fallbackImageID     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		fallbackImageDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	for _, tc := range []struct {
		name         string
		failAt       string
		boundImageID string
		reasonHas    string
	}{
		{
			name:      "When selector activation fails it should full-pull and report Fallback",
			failAt:    "tag",
			reasonHas: "activate refreshed image",
		},
		{
			name:         "When selector verification fails it should full-pull and report Fallback",
			failAt:       "verify",
			boundImageID: reconstructedID,
			reasonHas:    "verify activated image",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			targetIndex, _ := testApplicationTargetIndex(t, podmanDeltaTargetDigest)

			targetMissing := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "podman", "image", "exists", image,
			).Return("", "", 1)
			deltaManifest := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+candidate,
			).Return(podmanDeltaManifest, "", 0)
			targetManifest := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image,
			).Return(targetIndex, "", 0)
			sourceListing := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "podman", "image", "ls", "--format", client.PodmanImageDigestsListFormat,
			).Return("sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\t[\"quay.io/local/base@"+podmanDeltaSource+"\"]", "", 0)
			copyDelta := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "skopeo", "copy", "docker://"+candidate, gomock.Any(),
			).Return("", "", 0)
			importDelta := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "oci-delta", "import", gomock.Any(),
			).Return("", "", 0)
			refreshCanonical := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "podman", "pull", refreshImage,
			).Return(canonicalImageID, "", 0)

			var activation *gomock.Call
			if tc.failAt == "tag" {
				activation = exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "tag", canonicalImageID, image,
				).Return("", "tag failed", 1)
			} else {
				activation = exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "tag", canonicalImageID, image,
				).Return("", "", 0)
			}
			ordered := []any{
				targetMissing, deltaManifest, targetManifest, sourceListing,
				copyDelta, importDelta, refreshCanonical, activation,
			}
			if tc.failAt == "verify" {
				verification := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", "image", "inspect", "--format", "{{.Id}}", image,
				).Return(tc.boundImageID, "", 0)
				ordered = append(ordered, verification)
			}
			fullPull := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "podman", "pull", image,
			).Return(fallbackImageID, "", 0)
			observedDigest := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", fallbackImageID,
			).Return(fallbackImageDigest, "", 0)
			ordered = append(ordered, fullPull, observedDigest)
			gomock.InOrder(ordered...)

			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			logger := log.NewPrefixLogger("test")
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			resources := resource.NewMockManager(ctrl)
			resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
			manager := NewPrefetchManager(
				logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.NewConfig(time.Millisecond, 2),
				WithOCIDelta(client.NewOCIDelta(logger, exec, time.Minute)),
				WithDeltaStatusStore(store),
			)
			defer manager.Cleanup()

			containerApplication := v1beta1.ContainerApplication{
				Name:    lo.ToPtr(application),
				AppType: v1beta1.AppTypeContainer,
			}
			require.NoError(t, containerApplication.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
			var applicationSpec v1beta1.ApplicationProviderSpec
			require.NoError(t, applicationSpec.FromContainerApplication(containerApplication))
			desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{applicationSpec}}

			manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
				return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: {{
					Type:       OCITypePodmanImage,
					Reference:  image,
					Digest:     podmanDeltaTargetDigest,
					PullPolicy: v1beta1.PullIfNotPresent,
					Delta:      &OCIDeltaTarget{Hint: candidate, SourceDigest: podmanDeltaSource, Application: application},
				}}}}, nil
			}))

			stopWorker := startPrefetchWorker(t, manager)
			defer stopWorker()
			require.ErrorIs(t, manager.BeforeUpdate(context.Background(), nil, desired), errors.ErrPrefetchNotReady)
			require.Eventually(t, func() bool { return manager.IsReady(context.Background()) }, time.Second, time.Millisecond)

			result := store.ObservedApplicationResult(application, []deltastatus.ApplicationImage{{
				Image: image, Owner: string(v1beta1.RootUsername), OCIType: string(OCITypePodmanImage), Digest: fallbackImageDigest,
			}}, true)
			require.NotNil(t, result)
			require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeFallback, result.Outcome)
			require.NotNil(t, result.FallbackReason)
			require.Contains(t, *result.FallbackReason, tc.reasonHas)
		})
	}
}
