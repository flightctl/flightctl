package dependency

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestApplicationDeltaPrefetchCRIRefreshesRegistryReference(t *testing.T) {
	const (
		repository      = "quay.io/acme/app"
		targetDigest    = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		artifactSource  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		otherCurrent    = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		candidateDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		candidate       = repository + "@" + candidateDigest
	)

	testCases := []struct {
		name              string
		image             string
		discover          bool
		currentUnknown    bool
		currentDiffers    bool
		baseMissing       bool
		annotationMissing bool
		inspectFailure    bool
		applyFailure      bool
		refreshExitCode   int
		refreshStderr     string
		imageMissing      bool
		wantFallback      bool
		wantNotUsed       bool
		fallbackReasonHas string
	}{
		{
			name:  "When a tagged image is reconstructed it should refresh the canonical registry reference",
			image: repository + ":target",
		},
		{
			name:  "When a digest-pinned image has a hint it should reconstruct before pulling the canonical digest",
			image: repository + "@" + targetDigest,
		},
		{
			name:     "When a digest-pinned image has no hint it should discover a delta with a locally cached source",
			image:    repository + "@" + targetDigest,
			discover: true,
		},
		{
			name:           "When the current digest is unknown and a hint is supplied it should use the artifact base available locally",
			image:          repository + "@" + targetDigest,
			currentUnknown: true,
		},
		{
			name:           "When the current digest is unknown and no hint is supplied it should discover a delta with a local base",
			image:          repository + "@" + targetDigest,
			discover:       true,
			currentUnknown: true,
		},
		{
			name:           "When a hinted delta requires a different cached image it should use that base and preserve the current digest",
			image:          repository + "@" + targetDigest,
			currentDiffers: true,
		},
		{
			name:           "When the current image has no delta it should discover a delta from another cached image",
			image:          repository + "@" + targetDigest,
			discover:       true,
			currentDiffers: true,
		},
		{
			name:              "When a hinted delta base is absent it should full-pull without attempting reconstruction",
			image:             repository + "@" + targetDigest,
			currentDiffers:    true,
			baseMissing:       true,
			wantFallback:      true,
			fallbackReasonHas: "unavailable locally",
		},
		{
			name:              "When the current digest is unknown and a hinted base is absent it should full-pull",
			image:             repository + "@" + targetDigest,
			currentUnknown:    true,
			baseMissing:       true,
			wantFallback:      true,
			fallbackReasonHas: "unavailable locally",
		},
		{
			name:              "When a hinted artifact has no source annotation it should full-pull without using the current digest as its base",
			image:             repository + "@" + targetDigest,
			annotationMissing: true,
			wantFallback:      true,
			fallbackReasonHas: "does not identify its source image digest",
		},
		{
			name:              "When inspecting a hinted artifact fails it should full-pull",
			image:             repository + "@" + targetDigest,
			inspectFailure:    true,
			wantFallback:      true,
			fallbackReasonHas: "inspect application delta manifest",
		},
		{
			name:              "When the cached base disappears before reconstruction it should full-pull",
			image:             repository + "@" + targetDigest,
			applyFailure:      true,
			wantFallback:      true,
			fallbackReasonHas: "source disappeared",
		},
		{
			name:        "When the current image matches a discovered artifact source but that base is absent it should report NotUsed",
			image:       repository + "@" + targetDigest,
			discover:    true,
			baseMissing: true,
			wantNotUsed: true,
		},
		{
			name:              "When the canonical registry reference refresh fails it should full-pull and report the fallback",
			image:             repository + "@" + targetDigest,
			refreshExitCode:   1,
			refreshStderr:     "registry manifest refresh failed",
			wantFallback:      true,
			fallbackReasonHas: "refresh registry manifest after delta import",
		},
		{
			name:              "When the requested digest is unavailable after refresh it should full-pull and report the fallback",
			image:             repository + "@" + targetDigest,
			imageMissing:      true,
			wantFallback:      true,
			fallbackReasonHas: "did not make",
		},
	}

	for _, runtime := range []string{client.CRIRuntimeCRIO, client.CRIRuntimeContainerd} {
		for _, tc := range testCases {
			t.Run(runtime+"/"+tc.name, func(t *testing.T) {
				require := require.New(t)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				exec := executer.NewMockExecuter(ctrl)
				var calls []any
				if tc.discover {
					if !tc.currentUnknown {
						calls = append(calls, exec.EXPECT().ExecuteWithContext(
							gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+tc.image,
						).Return(targetDigest, "", 0))
					}
					calls = append(calls, exec.EXPECT().ExecuteWithContext(
						gomock.Any(), "skopeo", "list-referrers", "docker://"+tc.image,
					).Return(`{"manifests":[{"artifactType":"`+ociDeltaArtifactType+`","digest":"`+candidateDigest+`","annotations":{"`+ociDeltaSourceAnnotation+`":"`+artifactSource+`"}}]}`, "", 0))
				}
				{
					manifest := deltaCandidateManifest(artifactSource, targetDigest, 100)
					if tc.annotationMissing {
						manifest = deltaCandidateManifest("", targetDigest, 100)
					}
					exitCode := 0
					if tc.inspectFailure {
						exitCode = 1
					}
					calls = append(calls, exec.EXPECT().ExecuteWithContext(
						gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+candidate,
					).Return(manifest, "delta manifest unavailable", exitCode))
				}
				if !tc.annotationMissing && !tc.inspectFailure {
					if tc.image == repository+":target" {
						calls = append(calls, exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+tc.image).Return(targetDigest, "", 0))
					}
					images := "IMAGE TAG ID\napp old image-id"
					if tc.baseMissing {
						images = "IMAGE TAG ID\n"
					}
					calls = append(calls, exec.EXPECT().ExecuteWithContext(
						gomock.Any(), "crictl", "images", repository+"@"+artifactSource,
					).Return(images, "", 0))
				}
				reconstruct := !tc.baseMissing && !tc.annotationMissing && !tc.inspectFailure
				refreshEnv := make([]any, len(os.Environ()))
				for i := range refreshEnv {
					refreshEnv[i] = gomock.Any()
				}
				if reconstruct {
					calls = append(calls,
						exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "version").Return("RuntimeName: "+runtime+"\n", "", 0),
						exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", "docker://"+candidate, gomock.Any()).Return("", "", 0),
					)
					if runtime == client.CRIRuntimeCRIO {
						exitCode := 0
						if tc.applyFailure {
							exitCode = 1
						}
						calls = append(calls, exec.EXPECT().ExecuteWithContext(
							gomock.Any(), "oci-delta", "import", "--tag", tc.image, gomock.Any(),
						).Return("", "source disappeared", exitCode))
					} else {
						exitCode := 0
						if tc.applyFailure {
							exitCode = 1
						}
						calls = append(calls, exec.EXPECT().ExecuteWithContext(
							gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io", "images", "mount", repository+"@"+artifactSource, gomock.Any(),
						).Return("", "source disappeared", exitCode))
						if !tc.applyFailure {
							calls = append(calls,
								exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "apply", "--directory", gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", 0),
								exec.EXPECT().ExecuteWithContext(gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io", "images", "import", "--base-name", gomock.Any(), "--index-name", gomock.Any(), gomock.Any()).Return("", "", 0),
								exec.EXPECT().ExecuteWithContext(gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io", "images", "tag", "--force", gomock.Any(), tc.image).Return("", "", 0),
								exec.EXPECT().ExecuteWithContext(gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io", "images", "rm", gomock.Any()).Return("", "", 0),
								exec.EXPECT().ExecuteWithContext(gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io", "images", "unmount", gomock.Any()).Return("", "", 0),
							)
						}
					}
					if !tc.applyFailure {
						calls = append(calls, exec.EXPECT().ExecuteWithContextFromDir(
							gomock.Any(), "", "crictl", []string{"pull", tc.image}, refreshEnv...,
						).Return("Image is up to date", tc.refreshStderr, tc.refreshExitCode))
						if tc.refreshExitCode == 0 {
							images := "IMAGE TAG IMAGE ID SIZE\nquay.io/acme/app target sha256:cccc 1MB\n"
							if tc.imageMissing {
								images = "IMAGE TAG IMAGE ID SIZE\n"
							}
							calls = append(calls, exec.EXPECT().ExecuteWithContext(
								gomock.Any(), "crictl", "images", tc.image,
							).Return(images, "", 0))
						}
					}
				}
				if tc.wantFallback || tc.wantNotUsed {
					calls = append(calls, exec.EXPECT().ExecuteWithContextFromDir(
						gomock.Any(), "", "crictl", []string{"pull", tc.image}, refreshEnv...,
					).Return("Image pulled", "", 0))
				}
				gomock.InOrder(calls...)

				root := t.TempDir()
				rw := fileio.NewReadWriter(
					fileio.NewReader(fileio.WithReaderRootDir(root)),
					fileio.NewWriter(fileio.WithWriterRootDir(root)),
				)
				logger := log.NewPrefixLogger("test")
				cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
				skopeo := client.NewSkopeo(logger, exec, rw)
				target := imageRef{image: tc.image}
				currentDigest := artifactSource
				if tc.currentUnknown {
					currentDigest = ""
				} else if tc.currentDiffers {
					currentDigest = otherCurrent
				}
				delta := &OCIDeltaTarget{Hint: candidate, CurrentDigest: currentDigest, Application: "app"}
				if tc.discover {
					delta.Hint = ""
				}
				task := &prefetchTask{
					ociType:              OCITypeCRIImage,
					delta:                delta,
					deltaGeneration:      1,
					applicationTargetKey: applicationImageTargetKeyFor(target, "", OCITypeCRIImage),
				}
				manager := &prefetchManager{
					skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) {
						return skopeo, nil
					},
					log:                   logger,
					cliClients:            client.NewCLIClients(client.WithCRIClient(cri)),
					readWriter:            rw,
					pullTimeout:           time.Minute,
					ociDelta:              client.NewOCIDelta(logger, exec, time.Minute),
					tasks:                 map[imageRef]*prefetchTask{target: task},
					deltaGeneration:       1,
					deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
					deltaTargetsScheduled: true,
					deltaTargetRefs:       map[string]imageRef{deltastatus.Fingerprint(string(target.owner), target.image): target},
					deltaAppTargetKeys: map[string]map[string]string{
						"app": {deltastatus.Fingerprint(string(target.owner), target.image): task.applicationTargetKey},
					},
				}

				require.NoError(manager.pull(context.Background(), target, task))
				require.Equal(currentDigest, delta.CurrentDigest)
				task.done = true
				deviceStatus := &v1beta1.DeviceStatus{
					Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}},
				}
				require.NoError(manager.Status(context.Background(), deviceStatus))
				require.NotNil(deviceStatus.Applications[0].LastDelta)
				switch {
				case tc.wantFallback:
					require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, deviceStatus.Applications[0].LastDelta.Outcome)
					require.NotNil(deviceStatus.Applications[0].LastDelta.FallbackReason)
					require.Contains(*deviceStatus.Applications[0].LastDelta.FallbackReason, tc.fallbackReasonHas)
				case tc.wantNotUsed:
					require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotUsed, deviceStatus.Applications[0].LastDelta.Outcome)
				default:
					require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
				}
			})
		}
	}
}

func TestApplicationDeltaCRICachedImageReportsNotRequiredWhenDigestMatches(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/app:v2"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "images", image).Return("IMAGE TAG ID\napp v2 image-id", "", 0)
	exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "crictl", "inspecti", "--output", "json", image,
	).Return(`{"status":{"repoDigests":["quay.io/acme/app@`+digest+`"]}}`, "", 0)
	exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image,
	).Return(digest, "", 0)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	target := imageRef{image: image}
	targetID := deltastatus.Fingerprint(string(target.owner), target.image)
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

	manager.mu.Lock()
	needsQueue := manager.checkCachedApplicationTask(context.Background(), target, task)
	manager.mu.Unlock()
	require.False(t, needsQueue)
	require.True(t, task.done)

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotRequired, deviceStatus.Applications[0].LastDelta.Outcome)
}

func TestApplicationDeltaCRIHintedCandidateReportsNotUsedWhenDeltaClientIsUnavailable(t *testing.T) {
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
	target := imageRef{image: image}
	targetID := deltastatus.Fingerprint(string(target.owner), target.image)
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

	require.NoError(t, manager.pullCRIImage(context.Background(), target, task, skopeo, client.Timeout(time.Minute)))
	task.done = true

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotUsed, deviceStatus.Applications[0].LastDelta.Outcome)
	require.Nil(t, deviceStatus.Applications[0].LastDelta.FallbackReason)
}

func TestApplicationDeltaCRIPinnedImageReportsNotUsedWhenDeltaSourceIsMissing(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	discover := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "list-referrers", "docker://"+image,
	).Return(`{"manifests":[{"artifactType":"`+ociDeltaArtifactType+`","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","annotations":{"`+ociDeltaSourceAnnotation+`":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}]}`, "", 0)
	inspect := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/acme/app@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb").Return(deltaCandidateManifest("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", digest, 100), "", 0)
	sourceExists := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "crictl", "images", "quay.io/acme/app@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	).Return("IMAGE TAG ID\n", "", 0)
	pullEnv := make([]any, len(os.Environ()))
	for i := range pullEnv {
		pullEnv[i] = gomock.Any()
	}
	fullPull := exec.EXPECT().ExecuteWithContextFromDir(
		gomock.Any(), "", "crictl", []string{"pull", image}, pullEnv...,
	).Return("Image pulled", "", 0)
	gomock.InOrder(discover, inspect, sourceExists, fullPull)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	target := imageRef{image: image}
	targetID := deltastatus.Fingerprint(string(target.owner), target.image)
	targetKey := applicationImageTargetKeyFor(target, digest, OCITypeCRIImage)
	task := &prefetchTask{
		ociType:              OCITypeCRIImage,
		targetDigest:         digest,
		applicationTargetKey: targetKey,
		delta:                &OCIDeltaTarget{Application: application},
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
	require.NoError(t, manager.Status(context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotUsed, deviceStatus.Applications[0].LastDelta.Outcome)
}

func deltaCandidateManifest(source, target string, payload int64) string {
	manifest := client.OCIManifest{
		ArtifactType: ociDeltaArtifactType,
		Annotations:  map[string]string{ociDeltaSourceAnnotation: source},
		Subject:      &client.OCIDescriptor{Digest: target},
		Config:       &client.OCIDescriptor{Digest: "sha256:config", Size: 2},
		Layers:       []client.OCIDescriptor{{Digest: "sha256:payload", Size: payload}},
	}
	data, _ := json.Marshal(manifest)
	return string(data)
}

func TestSelectApplicationDeltaCandidateChoosesSmallestAvailablePayload(t *testing.T) {
	const (
		repository      = "quay.io/acme/app"
		target          = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		firstSource     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		secondSource    = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		firstCandidate  = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		secondCandidate = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)
	tests := []struct {
		name                            string
		firstPayload, secondPayload     int64
		firstAvailable, secondAvailable bool
		firstTarget, firstManifest      string
		wantCandidate, wantSource       string
	}{
		{name: "When the first manifest is smaller but its payload is larger it should choose the second delta", firstPayload: 500, secondPayload: 10, firstAvailable: true, secondAvailable: true, wantCandidate: secondCandidate, wantSource: secondSource},
		{name: "When a later payload is larger it should keep the first delta", firstPayload: 10, secondPayload: 500, firstAvailable: true, secondAvailable: true, wantCandidate: firstCandidate, wantSource: firstSource},
		{name: "When the smallest delta source is unavailable it should use the available source", firstPayload: 1, secondPayload: 500, secondAvailable: true, wantCandidate: secondCandidate, wantSource: secondSource},
		{name: "When no source exists in the selected store it should choose no delta", firstPayload: 1, secondPayload: 2},
		{name: "When the delta subject differs from the desired target it should ignore it", firstTarget: firstSource, secondAvailable: true, wantCandidate: secondCandidate, wantSource: secondSource},
		{name: "When the artifact source metadata is absent it should ignore the descriptor source", firstManifest: deltaCandidateManifest("", target, 1), secondAvailable: true, wantCandidate: secondCandidate, wantSource: secondSource},
		{name: "When a delta has a negative blob size it should ignore it", firstPayload: -1, firstAvailable: true, secondAvailable: true, wantCandidate: secondCandidate, wantSource: secondSource},
		{name: "When payload sizes tie it should choose deterministically", firstPayload: 10, secondPayload: 10, firstAvailable: true, secondAvailable: true, wantCandidate: firstCandidate, wantSource: firstSource},
	}
	for _, ociType := range []OCIType{OCITypeCRIImage, OCITypePodmanImage} {
		for _, tc := range tests {
			t.Run(string(ociType)+"/"+tc.name, func(t *testing.T) {
				require := require.New(t)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				exec := executer.NewMockExecuter(ctrl)
				logger := log.NewPrefixLogger("test")
				root := t.TempDir()
				rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
				require.NoError(rw.WriteFile("/cri-config.yaml", []byte("runtime-endpoint: unix:///run/custom.sock"), fileio.DefaultFilePermissions))
				cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
				podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
				skopeo := client.NewSkopeo(logger, exec, rw)
				manager := &prefetchManager{log: logger, cliClients: client.NewCLIClients(client.WithCRIClient(cri))}
				firstTarget := target
				if tc.firstTarget != "" {
					firstTarget = tc.firstTarget
				}
				firstManifest := deltaCandidateManifest(firstSource, firstTarget, tc.firstPayload)
				if tc.firstManifest != "" {
					firstManifest = tc.firstManifest
				}
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+repository+"@"+firstCandidate).Return(firstManifest, "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+repository+"@"+secondCandidate).Return(deltaCandidateManifest(secondSource, target, tc.secondPayload), "", 0)
				checkSource := func(source string, exists bool) {
					if ociType == OCITypeCRIImage {
						images := "IMAGE TAG ID\n"
						if exists {
							images += "app cached image-id\n"
						}
						exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "--config", "/cri-config.yaml", "images", repository+"@"+source).Return(images, "", 0)
					} else {
						exit := 1
						if exists {
							exit = 0
						}
						exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", repository+"@"+source).Return("", "", exit)
					}
				}
				if tc.firstTarget == "" && tc.firstManifest == "" {
					checkSource(firstSource, tc.firstAvailable)
				}
				checkSource(secondSource, tc.secondAvailable)
				index := &client.OCIIndex{Manifests: []client.OCIReferrer{
					{ArtifactType: ociDeltaArtifactType, Digest: firstCandidate, Size: 1, Annotations: map[string]string{ociDeltaSourceAnnotation: secondSource}},
					{ArtifactType: ociDeltaArtifactType, Digest: secondCandidate, Size: 999999},
					{ArtifactType: "other", Digest: firstCandidate},
					{ArtifactType: ociDeltaArtifactType},
				}}
				candidate, source := manager.selectApplicationDeltaCandidate(context.Background(), repository+"@"+target, ociType, podman, skopeo, index, client.WithDefaultAuth(), client.WithCRIConfig("/cri-config.yaml"))
				if tc.wantCandidate != "" {
					require.Equal(repository+"@"+tc.wantCandidate, candidate)
				} else {
					require.Empty(candidate)
				}
				require.Equal(tc.wantSource, source)
			})
		}
	}
}

func TestApplicationDeltaInvalidHintFullPullsWithoutDiscovery(t *testing.T) {
	const (
		repository   = "quay.io/acme/app"
		targetDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		sourceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		hint         = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	image := repository + "@" + targetDigest
	tests := []struct {
		name, candidate, manifest, reason string
		checkSource                       bool
	}{
		{name: "When the hint reference is invalid it should full-pull without discovery", candidate: "invalid image reference", reason: "invalid application delta image reference"},
		{name: "When the hinted artifact has no source metadata it should full-pull without discovery", manifest: deltaCandidateManifest("", targetDigest, 1), reason: "does not identify its source image digest"},
		{name: "When the hinted artifact source is unavailable it should full-pull without discovery", manifest: deltaCandidateManifest(sourceDigest, targetDigest, 1), checkSource: true, reason: "unavailable locally"},
		{name: "When the hinted artifact targets another image it should full-pull without discovery", manifest: deltaCandidateManifest(sourceDigest, sourceDigest, 1), reason: "does not match desired image"},
		{name: "When the hinted artifact type is invalid it should full-pull without discovery", manifest: `{ "artifactType": "other" }`, reason: "unexpected delta artifact type"},
	}
	for _, ociType := range []OCIType{OCITypeCRIImage, OCITypePodmanImage} {
		for _, tc := range tests {
			t.Run(string(ociType)+"/"+tc.name, func(t *testing.T) {
				require := require.New(t)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				exec := executer.NewMockExecuter(ctrl)
				root := t.TempDir()
				rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
				logger := log.NewPrefixLogger("test")
				cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
				podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
				skopeo := client.NewSkopeo(logger, exec, rw)
				candidate := hint
				if tc.candidate != "" {
					candidate = tc.candidate
				} else {
					exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+hint).Return(tc.manifest, "", 0)
				}
				if ociType == OCITypeCRIImage {
					if tc.checkSource {
						exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "images", repository+"@"+sourceDigest).Return("IMAGE TAG ID\n", "", 0)
					}
					exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"pull", image}, gomock.Any()).Return("Image pulled", "", 0)
				} else {
					if tc.checkSource {
						exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", repository+"@"+sourceDigest).Return("", "", 1)
					}
					exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
				}
				target := imageRef{image: image}
				task := &prefetchTask{ociType: ociType, delta: &OCIDeltaTarget{Hint: candidate, CurrentDigest: sourceDigest, Application: "app"}, deltaGeneration: 1}
				manager := &prefetchManager{log: logger, cliClients: client.NewCLIClients(client.WithCRIClient(cri)), ociDelta: client.NewOCIDelta(logger, exec, time.Minute), deltaGeneration: 1, tasks: map[imageRef]*prefetchTask{target: task}, deltaApplyResults: make(map[string]map[imageRef]applicationDeltaApplyResult)}
				// No referrers expectation: any discovery attempt fails this test.
				if ociType == OCITypeCRIImage {
					require.NoError(manager.pullCRIImage(context.Background(), target, task, skopeo))
				} else {
					require.NoError(manager.pullApplicationImage(context.Background(), target, task, podman, skopeo))
				}
				result := manager.deltaApplyResults["app"][target]
				require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, result.outcome)
				require.Contains(result.fallbackReason, tc.reason)
			})
		}
	}
}
