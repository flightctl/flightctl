package dependency

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/platforms"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/delta"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type storedPodmanSource struct {
	image          string
	digest         string
	inspectFailure bool
}

func expectPodmanStoredSources(t *testing.T, exec *executer.MockExecuter, storedSources []storedPodmanSource) {
	t.Helper()
	require := require.New(t)
	const contentDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	images := make([]string, 0, len(storedSources))
	hasInspectFailure := false
	for _, stored := range storedSources {
		images = append(images, stored.image)
		hasInspectFailure = hasInspectFailure || stored.inspectFailure
	}
	if !hasInspectFailure {
		rows := make([]string, 0, len(storedSources))
		for _, stored := range storedSources {
			repoDigests, err := json.Marshal([]string{"quay.io/local/base@" + stored.digest})
			require.NoError(err)
			rows = append(rows, contentDigest+"\t"+string(repoDigests))
		}
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "ls", "--format", client.PodmanImageDigestsListFormat).Return(strings.Join(rows, "\n"), "", 0)
		return
	}
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "ls", "--format", client.PodmanImageDigestsListFormat).Return("", "image disappeared", 1)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "ls", "--format", gomock.Any()).Return(strings.Join(images, "\n"), "", 0)
	for _, stored := range storedSources {
		if stored.inspectFailure {
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", stored.image).Return("", "image disappeared", 1)
			continue
		}
		inspect, err := json.Marshal(map[string]any{
			"Digest":      contentDigest,
			"RepoDigests": []string{"quay.io/local/base@" + stored.digest},
		})
		require.NoError(err)
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", stored.image).Return(string(inspect), "", 0)
	}
}

func TestPodmanDeltaCandidatePrefetch(t *testing.T) {
	const image = "quay.io/acme/app:target"
	const hint = "quay.io/another-repo/delta:target"
	const owner v1beta1.Username = "workload-user"
	type candidate struct {
		digest         string
		source         string
		manifestSize   int64
		layerSize      int64
		targetDigest   string
		local          bool
		invalid        bool
		untagged       bool
		inspectFailure bool
	}
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	digestC := "sha256:" + strings.Repeat("c", 64)
	digestD := "sha256:" + strings.Repeat("d", 64)
	digestE := "sha256:" + strings.Repeat("e", 64)
	platformTargetDigest := "sha256:" + strings.Repeat("9", 64)
	targetIndex, targetIndexDigest := testApplicationTargetIndex(t, platformTargetDigest)
	tests := []struct {
		name        string
		hint        string
		candidates  []candidate
		selected    string
		copyFailure bool
		fullFailure bool
		reason      string
		outcome     v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{name: "When discovery has differently sized candidates it should choose the smallest total download in the owner store", candidates: []candidate{
			{digest: digestA, source: podmanDeltaSource, manifestSize: 10, layerSize: 80, local: true},
			{digest: digestB, source: digestB, manifestSize: 90, layerSize: 1, local: true},
			{digest: digestC, source: digestE, layerSize: 1},
		}, selected: "quay.io/acme/app@" + digestA, outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When a referrer subject does not match the resolved platform digest it should use a full pull", candidates: []candidate{{digest: digestA, source: podmanDeltaSource, layerSize: 10, targetDigest: targetIndexDigest, local: true}}, outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When equal sized candidates arrive in reverse order it should break ties lexically", candidates: []candidate{
			{digest: digestB, source: podmanDeltaSource, layerSize: 80, local: true},
			{digest: digestA, source: digestB, layerSize: 80, local: true},
		}, selected: "quay.io/acme/app@" + digestA, outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When discovery has no local source it should use a full pull with NotUsed", candidates: []candidate{{digest: digestA, source: podmanDeltaSource, layerSize: 1}}, outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When an unrelated Podman image inspection fails it should still select a usable delta", candidates: []candidate{
			{digest: digestA, source: podmanDeltaSource, layerSize: 20, local: true},
			{digest: digestB, source: digestD, layerSize: 10, inspectFailure: true},
		}, selected: "quay.io/acme/app@" + digestA, outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When discovery has malformed metadata it should skip the candidate", candidates: []candidate{{digest: digestA, invalid: true}}, outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When a hint declares a different source than application metadata it should check the declared source", hint: hint, candidates: []candidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When a source was pulled by digest without a tag it should inspect its image ID and apply the delta", hint: hint, candidates: []candidate{{source: podmanDeltaSource, layerSize: 100, local: true, untagged: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When a hinted source is absent from its owner store it should recover through a full pull", hint: hint, candidates: []candidate{{source: podmanDeltaSource, layerSize: 100}}, reason: "source image is unavailable", outcome: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When a hinted artifact has invalid metadata it should recover through a full pull", hint: hint, candidates: []candidate{{invalid: true}}, reason: "not an OCI image delta", outcome: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When a hinted delta targets the index instead of the desired platform manifest it should recover through a full pull", hint: hint, candidates: []candidate{{source: podmanDeltaSource, layerSize: 100, local: true, targetDigest: targetIndexDigest}}, reason: "target digest does not match", outcome: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When a delta download fails it should clean its workspace and recover through a full pull", hint: hint, candidates: []candidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, copyFailure: true, reason: "download failed", outcome: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When full recovery also fails it should return the pull error", hint: hint, candidates: []candidate{{source: podmanDeltaSource, layerSize: 100}}, fullFailure: true, reason: "source image is unavailable", outcome: v1beta1.DeviceDeltaApplyOutcomeFallback},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			target := imageRef{image: image, owner: owner, ociType: OCITypePodmanImage}
			targetID := target.targetID()
			targetKey := applicationImageTargetKeyFor(target, "", OCITypePodmanImage)
			task := &prefetchTask{ociType: OCITypePodmanImage, delta: &OCIDeltaTarget{Hint: tc.hint, Application: "app"}, deltaGeneration: 1, applicationTargetKey: targetKey}
			// SourceDigest is application metadata, not proof of a usable delta base.
			if tc.hint != "" {
				task.delta.SourceDigest = digestA
			}
			manager := &prefetchManager{
				log: logger, readWriter: rw, pullTimeout: time.Minute,
				podmanFactory:     func(user v1beta1.Username) (*client.Podman, error) { require.Equal(owner, user); return podman, nil },
				skopeoFactory:     func(user v1beta1.Username) (*client.Skopeo, error) { require.Equal(owner, user); return skopeo, nil },
				readWriterFactory: func(user v1beta1.Username) (fileio.ReadWriter, error) { require.Equal(owner, user); return rw, nil },
				ociDeltaFactory: func(user v1beta1.Username) (*client.OCIDelta, error) {
					require.Equal(owner, user)
					return client.NewOCIDelta(logger, exec, time.Minute), nil
				},
				tasks: map[imageRef]*prefetchTask{target: task}, deltaGeneration: 1, deltaTargetsScheduled: true,
				deltaTargetRefs:    map[string]imageRef{targetID: target},
				deltaAppTargetKeys: map[string]map[string]string{"app": {targetID: targetKey}},
			}
			descriptors := make([]client.OCIReferrer, 0, len(tc.candidates))
			storedSources := make([]storedPodmanSource, 0, len(tc.candidates))
			eligibleCandidates := 0
			targetNeedsInspection := false
			for i, c := range tc.candidates {
				ref := tc.hint
				if ref == "" {
					ref = "quay.io/acme/app@" + c.digest
				}
				manifestSize := c.manifestSize
				if tc.hint == "" && manifestSize == 0 {
					manifestSize = 1
				}
				targetDigest := c.targetDigest
				if targetDigest == "" {
					targetDigest = platformTargetDigest
				}
				manifest := testApplicationDeltaManifest(t, c.source, targetDigest, c.layerSize)
				if c.invalid {
					manifest = `{"artifactType":"application/example"}`
				}
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+ref).Return(manifest, "", 0)
				if !c.invalid {
					if tc.hint != "" {
						targetNeedsInspection = true
					}
					if targetDigest == platformTargetDigest {
						eligibleCandidates++
					}
					if c.local || c.inspectFailure {
						image := fmt.Sprintf("quay.io/local/base:cached-%d", i)
						if c.untagged {
							image = "0123456789ab"
						}
						storedSources = append(storedSources, storedPodmanSource{image: image, digest: c.source, inspectFailure: c.inspectFailure})
					}
				}
				descriptors = append(descriptors, client.OCIReferrer{ArtifactType: delta.ArtifactType, Digest: c.digest, Size: manifestSize})
			}
			if targetNeedsInspection {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image).Return(targetIndex, "", 0)
			}
			if eligibleCandidates > 0 {
				expectPodmanStoredSources(t, exec, storedSources)
			}
			if tc.hint == "" {
				index, err := json.Marshal(client.OCIIndex{Manifests: descriptors})
				require.NoError(err)
				expectPlatformReferrers(t, exec, image, platformTargetDigest, string(index))
			}
			if tc.selected != "" {
				exit, stderr := 0, ""
				if tc.copyFailure {
					exit, stderr = 1, "download failed"
				}
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", "docker://"+tc.selected, gomock.Any()).Return("", stderr, exit)
				if !tc.copyFailure {
					exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "", 0)
					if tc.outcome == v1beta1.DeviceDeltaApplyOutcomeApplied {
						exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", "quay.io/acme/app@"+platformTargetDigest).Return("", "", 0)
					}
				}
			}
			exit, stderr := 0, ""
			if tc.fullFailure {
				exit, stderr = 1, "full pull failed"
			}
			if tc.outcome != v1beta1.DeviceDeltaApplyOutcomeApplied || tc.fullFailure {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", stderr, exit)
			}
			err := manager.pull(context.Background(), target, task)
			if tc.fullFailure {
				require.ErrorContains(err, "full pull failed")
			} else {
				require.NoError(err)
			}
			task.done = true
			status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
			require.NoError(collectPrefetchTaskOutcomes(t, manager, context.Background(), status))
			require.NotNil(status.Applications[0].LastDelta)
			require.Equal(tc.outcome, status.Applications[0].LastDelta.Outcome)
			if tc.reason != "" {
				require.NotNil(status.Applications[0].LastDelta.FallbackReason)
				require.Contains(*status.Applications[0].LastDelta.FallbackReason, tc.reason)
			}
			paths, err := filepath.Glob(filepath.Join(root, "tmp", "application-delta*"))
			require.NoError(err)
			require.Empty(paths)
		})
	}
}

func testApplicationDeltaManifest(t *testing.T, sourceDigest, targetDigest string, layerSize int64) string {
	t.Helper()
	annotations := map[string]string{delta.SourceAnnotation: sourceDigest}
	manifest, err := json.Marshal(client.OCIManifest{
		ArtifactType: delta.ArtifactType,
		Subject:      &client.OCIDescriptor{Digest: targetDigest, MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 100},
		Annotations:  annotations,
		Config:       &client.OCIDescriptor{Digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", MediaType: "application/vnd.oci.image.config.v1+json", Size: 10},
		Layers:       []client.OCIDescriptor{{Digest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Size: layerSize}},
	})
	require.NoError(t, err)
	return string(manifest)
}

func testApplicationTargetIndex(t *testing.T, platformDigest string) (string, string) {
	t.Helper()
	platform := platforms.DefaultSpec()
	index, err := json.Marshal(ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    digest.Digest(platformDigest),
			Size:      100,
			Platform:  &platform,
		}},
	})
	require.NoError(t, err)
	return string(index), digest.FromBytes(index).String()
}

func expectPlatformReferrers(t *testing.T, exec *executer.MockExecuter, image, platformDigest, referrersJSON string) {
	t.Helper()
	targetIndex, _ := testApplicationTargetIndex(t, platformDigest)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image).Return(targetIndex, "", 0)
	referrerImage := delta.Repository(image) + "@" + platformDigest
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "list-referrers", "docker://"+referrerImage).Return(referrersJSON, "", 0)
}

func TestComposeArtifactAndNestedDeltaPrefetch(t *testing.T) {
	const parent = "quay.io/acme/compose:v2"
	const nested = "quay.io/acme/web:v2"
	const hint = "quay.io/acme/delta:web"
	const parentDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, parentHint := range []string{"", "quay.io/acme/delta:compose"} {
		t.Run("When a Compose artifact has hint "+parentHint+" it should apply its nested image delta independently", func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			tasks := make(map[imageRef]*prefetchTask)
			refs := make(map[string]imageRef)
			keys := make(map[string]string)
			for _, target := range []OCIPullTarget{
				{Reference: parent, Type: OCITypePodmanArtifact, Delta: &OCIDeltaTarget{Hint: parentHint, Application: "compose"}},
				{Reference: nested, Type: OCITypePodmanImage, Delta: &OCIDeltaTarget{Hint: hint, Application: "compose"}},
			} {
				ref := imageRef{image: target.Reference, ociType: target.Type}
				id := ref.targetID()
				key := applicationImageTargetKey(ref, target)
				tasks[ref] = &prefetchTask{ociType: target.Type, delta: target.Delta, deltaGeneration: 1, applicationTargetKey: key}
				refs[id] = ref
				keys[id] = key
			}
			manager := &prefetchManager{
				log: logger, readWriter: rw, pullTimeout: time.Minute, ociDelta: client.NewOCIDelta(logger, exec, time.Minute),
				podmanFactory: func(user v1beta1.Username) (*client.Podman, error) {
					require.True(user.IsCurrentProcessUser())
					return podman, nil
				},
				skopeoFactory: func(user v1beta1.Username) (*client.Skopeo, error) {
					require.True(user.IsCurrentProcessUser())
					return skopeo, nil
				},
				tasks: tasks, deltaGeneration: 1, deltaTargetsScheduled: true, deltaTargetRefs: refs, deltaAppTargetKeys: map[string]map[string]string{"compose": keys},
			}
			if parentHint != "" {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", parent).Return("", "artifact not found", 1)
			}
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0).Times(2)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "pull", parent).Return("", "", 0)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", parent).Return(`{"Digest":"`+parentDigest+`"}`, "", 0)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+hint).Return(podmanDeltaManifest, "", 0)
			expectPodmanDeltaTarget(t, exec, nested, 1)
			expectPodmanDeltaSource(exec, 1)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", "docker://"+hint, gomock.Any()).Return("", "", 0)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", nested, gomock.Any()).Return("", "", 0)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", "quay.io/acme/web@"+podmanDeltaTargetDigest).Return("", "", 0)
			for ref, task := range tasks {
				require.NoError(manager.pull(context.Background(), ref, task))
				task.done = true
			}
			status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "compose"}}}
			require.NoError(collectPrefetchTaskOutcomes(t, manager, context.Background(), status))
			require.NotNil(status.Applications[0].LastDelta)
			require.Equal(v1beta1.DeviceDeltaApplyOutcomePartial, status.Applications[0].LastDelta.Outcome)
			if parentHint != "" {
				require.NotNil(status.Applications[0].LastDelta.FallbackReason)
				require.Contains(*status.Applications[0].LastDelta.FallbackReason, "not supported for OCI artifacts")
			} else {
				require.Nil(status.Applications[0].LastDelta.FallbackReason)
			}
		})
	}
}
