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
	manifestSize, layerSize int64
	local                   bool
	invalid                 bool
}

type criDeltaTestCase struct {
	name        string
	discover    bool
	candidates  []criDeltaTestCandidate
	selected    string
	containerd  bool
	tagged      bool
	failStep    string
	fullFailure bool
	reason      string
	outcome     v1beta1.DeviceDeltaApplyOutcomeType
}

func TestCRIDeltaCandidatePrefetch(t *testing.T) {
	const hint = "quay.io/deltas/workload:target"
	const image = "quay.io/acme/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	tests := []criDeltaTestCase{
		{name: "When a digest-pinned CRI-O target has a local hinted source it should import and refresh the exact reference", candidates: []criDeltaTestCandidate{{source: podmanDeltaSource, layerSize: 100, local: true}}, selected: hint},
		{name: "When discovery has no previous application digest it should rank locally usable artifacts by total download size", discover: true, candidates: []criDeltaTestCandidate{
			{digest: digestA, source: podmanDeltaSource, manifestSize: 10, layerSize: 80, local: true},
			{digest: digestB, source: digestB, manifestSize: 90, layerSize: 1, local: true},
		}, selected: "quay.io/acme/workload@" + digestA},
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
			task.applicationLifetimes = map[string]string{"helm-app": lifetime}
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
			var descriptors []client.OCIReferrer
			for _, c := range tc.candidates {
				ref := hint
				if tc.discover {
					ref = "quay.io/acme/workload@" + c.digest
				}
				manifest := strings.Replace(podmanDeltaManifest, podmanDeltaSource, c.source, 1)
				manifest = strings.Replace(manifest, `"size":100`, fmt.Sprintf(`"size":%d`, c.layerSize), 1)
				if c.invalid {
					manifest = `{"artifactType":"application/example"}`
				}
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+ref).Return(manifest, "", 0)
				localImages := `{"images":[]}`
				if c.local {
					localImages = `{"images":[{"id":"opaque-id","repoDigests":["quay.io/local/base@` + c.source + `"]}]}`
				}
				if tc.failStep == "opaque-id" {
					localImages = `{"images":[{"id":"` + podmanDeltaSource + `","repoDigests":[]}]}`
				}
				if !c.invalid {
					exit, stderr := 0, ""
					if tc.failStep == "source" {
						exit, stderr = 1, "source failed"
					}
					args := criArgs("images", "--output", "json")
					exec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return(localImages, stderr, exit)
				}
				descriptors = append(descriptors, client.OCIReferrer{ArtifactType: ociDeltaArtifactType, Digest: c.digest, Size: c.manifestSize})
			}
			if tc.discover {
				index, err := json.Marshal(client.OCIIndex{Manifests: descriptors})
				require.NoError(err)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "list-referrers", "docker://"+image).Return(string(index), "", 0)
			}

			expectCRIDeltaOperations(exec, image, tc, outcome)
			// These task-processing fixtures have no verified runtime observation.
			args := criArgs("inspecti", "--output", "json", image)
			exec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return(`{"status":{"repoDigests":[]}}`, "", 0).AnyTimes()

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

func expectCRIDeltaOperations(exec *executer.MockExecuter, image string, tc criDeltaTestCase, outcome v1beta1.DeviceDeltaApplyOutcomeType) {
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
	if tc.selected != "" {
		runtime := "cri-o"
		if tc.containerd {
			runtime = "containerd"
		}
		args := criArgs("version")
		steps = append(steps, exec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return("RuntimeName: "+runtime+"\n", "", 0))
		expect("skopeo", "copy", "docker://"+tc.selected, gomock.Any())
		if tc.failStep != "download" {
			if !tc.containerd {
				expect("oci-delta", "import", "--tag", image, gomock.Any())
			} else {
				ctrArgs := []any{"--address", "/run/workload/containerd.sock", "--namespace", "k8s.io", "images"}
				mountArgs := append(append([]any{}, ctrArgs...), "mount", "quay.io/local/base@"+podmanDeltaSource, gomock.Any())
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
				output := "IMAGE TAG ID\nworkload target image-id"
				if tc.failStep == "visibility" {
					output = "IMAGE TAG ID"
				}
				args := criArgs("images", image)
				steps = append(steps, exec.EXPECT().ExecuteWithContext(args[0], args[1], args[2:]...).Return(output, "", 0))
				if tc.failStep != "visibility" {
					exit, stderr := 0, ""
					if tc.failStep == "refresh" {
						exit, stderr = 1, "refresh failed"
					}
					steps = append(steps, exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"--config", "/etc/workload-cri.yaml", "pull", image}, gomock.Any()).Return("", stderr, exit))
				}
			}
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
