package dependency

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/delta"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type criDeltaTestCandidate struct {
	digest, source          string
	targetDigest            string
	manifestSize, layerSize int64
	local                   bool
	localIndex              bool
	localRepositoryDigests  []string
	invalid                 bool
}

type criDeltaTestCase struct {
	name         string
	discover     bool
	candidates   []criDeltaTestCandidate
	selected     string
	sourceRef    string
	containerd   bool
	localIndex   bool
	remoteBudget bool
	tagged       bool
	failStep     string
	fullFailure  bool
	reason       string
	outcome      v1beta1.DeviceDeltaApplyOutcomeType
}

func TestCRIDeltaCandidatePrefetch(t *testing.T) {
	const hint = "quay.io/deltas/workload:target"
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	repositoryDigestA := "sha256:" + strings.Repeat("e", 64)
	repositoryDigestB := "sha256:" + strings.Repeat("f", 64)
	remoteRepositoryRefs := map[string]bool{
		"docker://quay.io/local/base@" + repositoryDigestA: true,
		"docker://quay.io/local/base@" + repositoryDigestB: true,
	}
	platformTargetDigest := "sha256:" + strings.Repeat("9", 64)
	image := "quay.io/acme/workload@" + platformTargetDigest
	localIndexDigest := "sha256:" + strings.Repeat("1", 64)
	targetIndex, targetIndexDigest := testApplicationTargetIndex(t, platformTargetDigest)
	tests := []criDeltaTestCase{
		{name: "When a digest-pinned CRI-O target has a local hinted source it should import and refresh the exact reference", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint},
		{name: "When containerd records an image-list digest it should resolve the local platform source", containerd: true, localIndex: true, candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true, localIndex: true}}, selected: hint, sourceRef: localIndexDigest},
		{name: "When discovery has no previous application digest it should rank locally usable artifacts by total download size", discover: true, candidates: []criDeltaTestCandidate{
			{digest: digestA, source: podmanDeltaSource, manifestSize: 10, layerSize: 80, local: true},
			{digest: digestB, source: digestB, manifestSize: 90, layerSize: 1, local: true},
		}, selected: "quay.io/acme/workload@" + digestA},
		{name: "When a referrer subject does not match the resolved platform digest it should full-pull with NotUsed", discover: true, candidates: []criDeltaTestCandidate{{digest: digestA, source: podmanDeltaSource, layerSize: 10, local: true, targetDigest: targetIndexDigest}}, outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When a digest-pinned containerd target has a local hinted source it should import through its configured endpoint and workload namespace", containerd: true, candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint},
		{name: "When a tagged CRI target has a usable hint it should refresh the requested tag", tagged: true, candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint},
		{name: "When equal sized discovered deltas arrive in reverse order it should choose the lexical reference", discover: true, candidates: []criDeltaTestCandidate{
			{digest: digestB, source: podmanDeltaSource, layerSize: 80, local: true},
			{digest: digestA, source: digestB, layerSize: 80, local: true},
		}, selected: "quay.io/acme/workload@" + digestA},
		{name: "When a smaller discovered delta has no local source it should select the usable delta", discover: true, candidates: []criDeltaTestCandidate{
			{digest: digestA, source: podmanDeltaSource, layerSize: 1},
			{digest: digestB, source: digestB, layerSize: 80, local: true},
		}, selected: "quay.io/acme/workload@" + digestB},
		{name: "When discovery has no local source it should full-pull with NotUsed", discover: true, candidates: []criDeltaTestCandidate{{digest: digestA, source: podmanDeltaSource, layerSize: 1}}, outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When discovery has invalid metadata it should skip the candidate", discover: true, candidates: []criDeltaTestCandidate{{digest: digestA, invalid: true}}, outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When a hinted source is missing from CRI it should full-pull with Fallback", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100}}, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "source image is unavailable"},
		{name: "When remote CRI source inspection exceeds its budget it should stop checking remaining images", remoteBudget: true, candidates: []criDeltaTestCandidate{
			{source: digestB, layerSize: 100, local: true, localRepositoryDigests: []string{repositoryDigestA, repositoryDigestB}},
		}, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "source image is unavailable"},
		{name: "When a hinted delta targets the index instead of the desired platform manifest it should full-pull with Fallback", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true, targetDigest: targetIndexDigest}}, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "target digest does not match"},
		{name: "When a runtime ID equals the source digest it should require a matching repository digest", failStep: "opaque-id", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100}}, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "source image is unavailable"},
		{name: "When a hinted artifact is malformed it should full-pull with Fallback", candidates: []criDeltaTestCandidate{{invalid: true}}, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "not an OCI image delta"},
		{name: "When a CRI source probe fails it should full-pull with Fallback", failStep: "source", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100}}, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "source failed"},
		{name: "When a delta download fails it should recover with a full pull", failStep: "download", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "download failed"},
		{name: "When a CRI-O import fails it should recover with a full pull", failStep: "import", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "import failed"},
		{name: "When a containerd source mount fails it should recover with a full pull", containerd: true, failStep: "mount", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "mount failed"},
		{name: "When containerd reconstruction fails it should unmount and recover with a full pull", containerd: true, failStep: "reconstruct", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "reconstruct failed"},
		{name: "When a containerd import fails it should clean up and recover with a full pull", containerd: true, failStep: "import", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "import failed"},
		{name: "When containerd target tagging fails it should clean up and recover with a full pull", containerd: true, failStep: "tag", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "tag failed"},
		{name: "When runtime visibility fails it should recover with a full pull", failStep: "visibility", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "did not make"},
		{name: "When canonical refresh fails it should retry the exact reference through a full pull", failStep: "refresh", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "refresh registry manifest"},
		{name: "When full recovery fails it should return an error and preserve Fallback", fullFailure: true, candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100}}, outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, reason: "source image is unavailable"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			image := image
			if tc.tagged {
				image = "quay.io/acme/workload:target"
			}
			outcome := tc.outcome
			if outcome == "" {
				outcome = v1beta1.DeviceDeltaApplyOutcomeApplied
			}
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 1))
			skopeo := client.NewSkopeo(logger, exec, rw)
			require.NoError(rw.WriteFile("/etc/workload-cri.yaml", []byte("runtime-endpoint: unix:///run/workload/containerd.sock\n"), fileio.DefaultFilePermissions))
			criArgs := func(args ...string) []any {
				result := []any{gomock.Any(), "crictl", "--config", "/etc/workload-cri.yaml"}
				for _, arg := range args {
					result = append(result, arg)
				}
				return result
			}
			target := imageRef{image: image, ociType: OCITypeCRIImage}
			id := target.targetID()
			key := applicationImageTargetKeyFor(target, "", OCITypeCRIImage)
			task := &prefetchTask{ociType: OCITypeCRIImage, delta: &OCIDeltaTarget{Hint: hint, SourceDigest: "sha256:previous-app", Application: "helm-app"}, deltaGeneration: 1, applicationTargetKey: key, clientOptsFn: func() []client.ClientOption {
				return []client.ClientOption{client.WithCRIConfig("/etc/workload-cri.yaml")}
			}}
			if tc.discover {
				task.delta.Hint = ""
				task.delta.SourceDigest = ""
			}
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			specKeys := map[string]string{"helm-app": "unchanged-spec"}
			keys := map[string]map[string]string{"helm-app": {id: key}}
			require.NoError(store.ReconcileApplicationSpecs(specKeys))
			lifetime := store.ApplicationLifetime("helm-app")
			task.applicationAttempts = map[string]deltastatus.ApplicationAttempt{"helm-app": {Application: "helm-app", Lifetime: lifetime, SpecKey: "unchanged-spec", TargetID: id, TargetKey: key}}
			require.NoError(store.ReconcileApplicationTargets(keys, true))
			manager := &prefetchManager{
				log: logger, readWriter: rw, pullTimeout: time.Minute,
				cliClients:    client.NewCLIClients(client.WithCRIClient(cri)),
				skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				ociDelta:      client.NewOCIDelta(logger, exec, time.Minute),
				tasks:         map[imageRef]*prefetchTask{target: task}, deltaGeneration: 1, deltaTargetsScheduled: true,
				deltaTargetRefs: map[string]imageRef{id: target}, deltaAppTargetKeys: keys,
				deltaAppLifetimes: map[string]string{"helm-app": lifetime},
				deltaStatusStore:  store, deltaAppSpecKeys: specKeys,
			}
			if tc.remoteBudget {
				manager.pullTimeout = time.Millisecond
			}
			var descriptors []client.OCIReferrer
			eligibleCandidates := 0
			targetNeedsInspection := tc.discover
			if tc.remoteBudget {
				expectRemoteCRISourceInspectionBudget(exec, remoteRepositoryRefs)
			}
			for _, c := range tc.candidates {
				ref := hint
				if tc.discover {
					ref = "quay.io/acme/workload@" + c.digest
				}
				targetDigest := c.targetDigest
				if targetDigest == "" {
					targetDigest = platformTargetDigest
				}
				manifestSize := c.manifestSize
				if tc.discover && manifestSize == 0 {
					manifestSize = 1
				}
				manifest := testApplicationDeltaManifest(t, c.source, targetDigest, c.layerSize)
				if c.invalid {
					manifest = `{"artifactType":"application/example"}`
				}
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+ref).Return(manifest, "", 0)
				if !c.invalid {
					if !tc.discover {
						targetNeedsInspection = true
					}
					if targetDigest == platformTargetDigest {
						eligibleCandidates++
					}
				}
				descriptors = append(descriptors, client.OCIReferrer{ArtifactType: delta.ArtifactType, Digest: c.digest, Size: manifestSize})
			}
			if targetNeedsInspection && !tc.discover {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image).Return(targetIndex, "", 0)
			}
			if eligibleCandidates > 0 {
				localImages := criDeltaLocalImages(t, exec, tc, localIndexDigest)
				if tc.failStep == "opaque-id" {
					localImages = []any{map[string]any{"id": podmanDeltaSource, "repoDigests": []string{}}}
				}
				imageList, err := json.Marshal(map[string]any{"images": localImages})
				require.NoError(err)
				exit, stderr := 0, ""
				if tc.failStep == "source" {
					exit, stderr = 1, "source failed"
				}
				args := criArgs("images", "--output", "json")
				exec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return(string(imageList), stderr, exit)
			}
			if tc.discover {
				index, err := json.Marshal(client.OCIIndex{Manifests: descriptors})
				require.NoError(err)
				expectPlatformReferrers(t, exec, image, platformTargetDigest, string(index))
			}

			expectCRIDeltaOperations(exec, image, "quay.io/acme/workload@"+platformTargetDigest, targetIndex, tc, outcome)

			if tc.containerd && tc.failStep == "" {
				const chart = "oci://quay.io/acme/charts/workload:1.0.0"
				chartDir := "/var/lib/flightctl/helm/charts/" + client.SanitizeChartRef(chart)
				require.NoError(rw.MkdirAll(chartDir, fileio.DefaultDirectoryPermissions))
				require.NoError(rw.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: workload\nversion: 1.0.0\n"), fileio.DefaultFilePermissions))
				helm := client.NewHelm(logger, exec, rw, "/var/lib/flightctl", poll.NewConfig(time.Millisecond, 1))
				manager.cliClients = client.NewCLIClients(client.WithCRIClient(cri), client.WithHelmClient(helm))
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{"dependency", "update", chartDir}).Return("", "", 0)
				require.NoError(manager.pull(context.Background(), imageRef{image: chart, ociType: OCITypeHelmChart}, &prefetchTask{ociType: OCITypeHelmChart, delta: &OCIDeltaTarget{Hint: "quay.io/deltas/chart:unsupported", Application: "helm-app"}}))
				resolved, err := helm.IsResolved(chart)
				require.NoError(err)
				require.True(resolved)
			}
			err := manager.pull(context.Background(), target, task)
			if tc.fullFailure {
				require.ErrorContains(err, "full pull failed")
			} else {
				require.NoError(err)
			}

			task.done = true
			status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "helm-app"}}}
			require.NoError(collectPrefetchTaskOutcomes(t, manager, context.Background(), status))
			require.NotNil(status.Applications[0].LastDelta)
			require.Equal(outcome, status.Applications[0].LastDelta.Outcome)
			require.Equal(outcome, deltastatus.New(rw, "/var/lib/flightctl", logger).ApplicationResults("helm-app")[id].Status.Outcome)
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

func expectRemoteCRISourceInspectionBudget(exec *executer.MockExecuter, remoteRepositoryRefs map[string]bool) {
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", gomock.Cond(func(value any) bool {
		reference, ok := value.(string)
		return ok && remoteRepositoryRefs[reference]
	})).Times(1).DoAndReturn(
		func(ctx context.Context, _ string, _ ...string) (string, string, int) {
			<-ctx.Done()
			return "", "remote inspection timed out", 1
		},
	)
}

func criDeltaLocalImages(t *testing.T, exec *executer.MockExecuter, tc criDeltaTestCase, localIndexDigest string) []any {
	localImages := make([]any, 0, len(tc.candidates))
	for _, candidate := range tc.candidates {
		if !candidate.local || candidate.invalid {
			continue
		}
		repoDigests := []string{"quay.io/local/base@" + candidate.source}
		if len(candidate.localRepositoryDigests) > 0 {
			repoDigests = make([]string, 0, len(candidate.localRepositoryDigests))
			for _, repositoryDigest := range candidate.localRepositoryDigests {
				repoDigests = append(repoDigests, "quay.io/local/base@"+repositoryDigest)
			}
		}
		if candidate.localIndex {
			repoDigests = []string{"quay.io/local/base@" + localIndexDigest}
			sourceIndex, _ := testApplicationTargetIndex(t, candidate.source)
			exec.EXPECT().ExecuteWithContext(
				gomock.Any(),
				"ctr",
				"--address", "/run/workload/containerd.sock",
				"--namespace", "k8s.io",
				"content", "get", localIndexDigest,
			).Return(sourceIndex, "", 0)
		}
		localImages = append(localImages, map[string]any{
			"id":          "opaque-id",
			"repoDigests": repoDigests,
		})
	}
	return localImages
}

func expectCRIDeltaOperations(exec *executer.MockExecuter, image, refreshImage, targetIndex string, tc criDeltaTestCase, outcome v1beta1.DeviceDeltaApplyOutcomeType) {
	criArgs := func(args ...string) []any {
		result := []any{gomock.Any(), "crictl", "--config", "/etc/workload-cri.yaml"}
		for _, arg := range args {
			result = append(result, arg)
		}
		return result
	}
	var steps []any
	expect := func(cmd string, args ...any) {
		step := ""
		switch cmd {
		case "skopeo":
			step = "download"
		case "oci-delta":
			step = "import"
			if tc.containerd {
				step = "reconstruct"
			}
		case "ctr":
			step = fmt.Sprint(args[5])
		}
		exit, stderr := 0, ""
		if tc.failStep == step {
			exit, stderr = 1, step+" failed"
		}
		steps = append(steps, exec.EXPECT().ExecuteWithContext(gomock.Any(), cmd, args...).Return("", stderr, exit))
	}
	if tc.selected != "" || tc.remoteBudget {
		sourceRef := tc.sourceRef
		if sourceRef == "" {
			sourceRef = podmanDeltaSource
		}
		runtime := "cri-o"
		if tc.containerd {
			runtime = "containerd"
		}
		args := criArgs("version")
		versionCalls := 1
		if tc.localIndex {
			versionCalls = 2
		}
		steps = append(steps, exec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return("RuntimeName: "+runtime+"\n", "", 0).Times(versionCalls))
		if tc.selected != "" {
			expect("skopeo", "copy", "docker://"+tc.selected, gomock.Any())
			if tc.failStep != "download" {
				digestPinned, err := imageReferenceIsDigestPinned(image)
				if err != nil {
					panic(err)
				}
				if !tc.containerd {
					if digestPinned {
						expect("oci-delta", "import", "--tag", image, gomock.Any())
					} else {
						expect("oci-delta", "import", gomock.Any())
					}
				} else {
					ctrArgs := []any{"--address", "/run/workload/containerd.sock", "--namespace", "k8s.io", "images"}
					mountArgs := append(append([]any{}, ctrArgs...), "mount", "quay.io/local/base@"+sourceRef, gomock.Any())
					expect("ctr", mountArgs...)
					if tc.failStep != "mount" {
						expect("oci-delta", "apply", "--directory", gomock.Any(), gomock.Any(), gomock.Any())
						if tc.failStep != "reconstruct" {
							expect("ctr", append(append([]any{}, ctrArgs...), "import", "--base-name", gomock.Any(), "--index-name", gomock.Any(), gomock.Any())...)
							if tc.failStep != "import" {
								expect("ctr", append(append([]any{}, ctrArgs...), "tag", "--force", gomock.Any(), image)...)
							}
							expect("ctr", append(append([]any{}, ctrArgs...), "rm", gomock.Any())...)
						}
						expect("ctr", append(append([]any{}, ctrArgs...), "unmount", gomock.Any())...)
					}
				}
				if tc.failStep != "import" && tc.failStep != "reconstruct" && tc.failStep != "mount" && tc.failStep != "tag" {
					steps = append(steps, expectCRIDeltaVisibilityAndRefresh(exec, image, refreshImage, tc, criArgs, digestPinned)...)
				}
			}
		}
		if tc.selected != "" && tc.failStep == "" {
			steps = append(steps, expectCRIDeltaCanonicalRefresh(exec, image, refreshImage, targetIndex, tc, criArgs)...)
		}
	}
	if outcome != v1beta1.DeviceDeltaApplyOutcomeApplied {
		exit, stderr := 0, ""
		if tc.fullFailure {
			exit, stderr = 1, "full pull failed"
		}
		steps = append(steps, exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"--config", "/etc/workload-cri.yaml", "pull", image}, gomock.Any()).Return("", stderr, exit))
	}
	gomock.InOrder(steps...)
}

func expectCRIDeltaVisibilityAndRefresh(
	exec *executer.MockExecuter,
	image, refreshImage string,
	tc criDeltaTestCase,
	criArgs func(args ...string) []any,
	digestPinned bool,
) []any {
	var steps []any
	requiresVisibility := digestPinned || tc.containerd
	if requiresVisibility {
		output := "IMAGE TAG ID\nworkload target image-id"
		if tc.failStep == "visibility" {
			output = "IMAGE TAG ID"
		}
		args := criArgs("images", image)
		steps = append(steps, exec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return(output, "", 0))
	}
	if tc.failStep != "visibility" || !requiresVisibility {
		exit, stderr := 0, ""
		if tc.failStep == "refresh" {
			exit, stderr = 1, "refresh failed"
		}
		steps = append(steps, exec.EXPECT().ExecuteWithContextFromDir(
			gomock.Any(), "", "crictl", []string{"--config", "/etc/workload-cri.yaml", "pull", refreshImage}, gomock.Any(),
		).Return("", stderr, exit))
	}
	return steps
}

func expectCRIDeltaCanonicalRefresh(
	exec *executer.MockExecuter,
	image, refreshImage, targetIndex string,
	tc criDeltaTestCase,
	criArgs func(args ...string) []any,
) []any {
	repository, _, _, err := normalizedCRIImageReferences(image)
	if err != nil {
		panic(err)
	}
	targetDigest := applicationImageDigest(refreshImage)
	metadata := fmt.Sprintf(
		`{"status":{"id":%q,"repoDigests":[%q]}}`,
		podmanDeltaImageID, repository+"@"+targetDigest,
	)
	var steps []any
	canonicalArgs := criArgs("inspecti", "--output", "json", refreshImage)
	steps = append(steps, exec.EXPECT().ExecuteWithContext(canonicalArgs[0], canonicalArgs[1], canonicalArgs[2:]...).Return(metadata, "", 0))
	digestPinned, err := imageReferenceIsDigestPinned(image)
	if err != nil {
		panic(err)
	}
	if !digestPinned {
		if tc.containerd {
			steps = append(steps, exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "ctr", "--address", "/run/workload/containerd.sock", "--namespace", "k8s.io",
				"images", "tag", "--force", refreshImage, image,
			).Return("", "", 0))
		} else {
			steps = append(steps, exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "skopeo", "copy", "--preserve-digests", "docker://"+refreshImage, "containers-storage:"+image,
			).Return("", "", 0))
		}
	}
	verifyArgs := criArgs("inspecti", "--output", "json", image)
	steps = append(steps, exec.EXPECT().ExecuteWithContext(verifyArgs[0], verifyArgs[1], verifyArgs[2:]...).Return(metadata, "", 0))
	observeArgs := criArgs("inspecti", "--output", "json", image)
	steps = append(steps, exec.EXPECT().ExecuteWithContext(observeArgs[0], observeArgs[1], observeArgs[2:]...).Return(metadata, "", 0))
	steps = append(steps, exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+repository+"@"+targetDigest, "--no-creds",
	).Return(targetIndex, "", 0))
	return steps
}
