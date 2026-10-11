package dependency

import (
	"context"
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

func TestApplicationDeltaCRIPullsWithoutDiscovery(t *testing.T) {
	for _, hint := range []string{"", "not an image", "quay.io/acme/delta:target"} {
		t.Run("When hint is "+hint+" it should full pull without discovery", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 1))
			target := imageRef{image: "quay.io/acme/app:latest", runtime: OCITypeCRIImage}
			task := &prefetchTask{ociType: OCITypeCRIImage, delta: &OCIDeltaTarget{Application: "app", Hint: hint}, deltaGeneration: 1}
			m := &prefetchManager{log: logger, cliClients: client.NewCLIClients(client.WithCRIClient(cri)), deltaStatusStore: deltastatus.NewInMemory(logger), tasks: map[imageRef]*prefetchTask{target: task}, deltaGeneration: 1}
			exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"pull", target.image}, gomock.Any()).Return(strings.Repeat("f", 64), "", 0)
			require.NoError(t, m.pullCRIImage(context.Background(), target, task, nil))
			require.Empty(t, task.preparedDigest)
		})
	}
}

func TestApplicationDeltaCRIOReusesImportFlow(t *testing.T) {
	const repository = "quay.io/acme/app"
	targetDigest, sourceDigest := historyDigest(10), historyDigest(3)
	importedID := strings.TrimPrefix(historyDigest(20), "sha256:")
	canonical, candidate := repository+"@"+targetDigest, "quay.io/acme/delta:target"
	for _, tt := range []struct {
		name                                  string
		mutable, opaque, configured           bool
		baseMissing, copyFailed, importFailed bool
		pullFailed, aliasFailed, aliasMoved   bool
		importIdentityMissing                 bool
		runtimeImageMissing                   bool
		runtimeImageMismatch                  bool
		runtimeInspectFailed                  bool
		want                                  v1beta1.DeviceDeltaApplyOutcomeType
		wantError, wantReason                 string
	}{
		{name: "When a pinned CRI-O target has a valid hint it should reuse the original import and canonical pull", want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When a tagged CRI-O target is reconstructed it should make the desired reference available in the configured runtime", mutable: true, configured: true, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When CRI-O returns opaque identities it should preserve unknown digest after restoring the desired reference", mutable: true, opaque: true},
		{name: "When the CRI-O source is absent it should full pull without import", baseMissing: true, want: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When downloading the CRI-O delta fails it should retain full pull fallback", copyFailed: true, want: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When CRI-O import fails it should retain full pull fallback", importFailed: true, want: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When import returns no storage identity it should full pull without claiming the delta reached CRI-O", importIdentityMissing: true, want: v1beta1.DeviceDeltaApplyOutcomeFallback, wantReason: "did not identify its storage image"},
		{name: "When local import succeeds in a different store it should full pull into the configured CRI-O service and report fallback", configured: true, runtimeImageMissing: true, want: v1beta1.DeviceDeltaApplyOutcomeFallback, wantReason: "has no local identity"},
		{name: "When CRI-O reports a different storage identity it should report fallback after full pull", runtimeImageMismatch: true, want: v1beta1.DeviceDeltaApplyOutcomeFallback, wantReason: "absent from the configured CRI-O image service"},
		{name: "When CRI-O cannot inspect the imported identity it should retain full pull fallback", runtimeInspectFailed: true, want: v1beta1.DeviceDeltaApplyOutcomeFallback, wantReason: "verify imported CRI-O image"},
		{name: "When the final canonical CRI-O pull fails it should not record an applied delta", pullFailed: true, wantError: "crictl pull"},
		{name: "When restoring the desired CRI-O reference fails it should not record an applied delta", mutable: true, aliasFailed: true, wantError: "crictl pull"},
		{name: "When the desired CRI-O tag moves during preparation it should not attribute the delta to the different image", mutable: true, aliasMoved: true, wantError: "different image"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec, rw := executer.NewMockExecuter(ctrl), fileio.NewMockReadWriter(ctrl)
			logger := log.NewPrefixLogger("test")
			cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 1))
			skopeo := client.NewSkopeo(logger, exec, rw)
			target := imageRef{image: canonical, runtime: OCITypeCRIImage}
			if tt.mutable {
				target.image = repository + ":target"
			}
			task := &prefetchTask{ociType: OCITypeCRIImage, delta: &OCIDeltaTarget{Application: "app", Hint: candidate}, deltaGeneration: 1}
			store := deltastatus.NewInMemory(logger)
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{"app": "spec"}))
			m := &prefetchManager{log: logger, readWriter: rw, cliClients: client.NewCLIClients(client.WithCRIClient(cri)), ociDelta: client.NewOCIDelta(logger, exec, time.Minute), deltaStatusStore: store, tasks: map[imageRef]*prefetchTask{target: task}, deltaGeneration: 1}
			var opts []client.ClientOption
			var criArgs []string
			configPath := "/etc/crictl.yaml"
			if tt.configured {
				configPath = "/etc/flightctl/crio.yaml"
				opts = []client.ClientOption{client.WithCRIConfig(configPath)}
				criArgs = []string{"--config", configPath}
				rw.EXPECT().PathExists(configPath).Return(true, nil).AnyTimes()
			}
			commandArgs := func(args ...string) []any {
				values := make([]any, 0, len(criArgs)+len(args))
				for _, arg := range append(append([]string(nil), criArgs...), args...) {
					values = append(values, arg)
				}
				return values
			}
			var calls []any
			if tt.mutable {
				calls = append(calls, exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+target.image).Return(targetDigest, "", 0))
			}
			manifest := fmt.Sprintf(`{"artifactType":%q,"annotations":{%q:%q},"subject":{"digest":%q},"config":{"digest":%q,"size":1},"layers":[]}`, ociDeltaArtifactType, ociDeltaSourceAnnotation, sourceDigest, targetDigest, historyDigest(4))
			calls = append(calls, exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+candidate).Return(manifest, "", 0))
			images := "IMAGE TAG ID\napp old immutable-base"
			if tt.baseMissing {
				images = "IMAGE TAG ID\n"
			}
			calls = append(calls, exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", commandArgs("images", repository+"@"+sourceDigest)...).Return(images, "", 0))
			if !tt.baseMissing {
				calls = append(calls, exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", commandArgs("version")...).Return("RuntimeName: cri-o\n", "", 0))
				tmpDir := "/application-delta-h2"
				deltaFile := filepath.Join(tmpDir, "delta.oci")
				rw.EXPECT().MkdirTemp("application-delta").Return(tmpDir, nil)
				rw.EXPECT().RemoveAll(tmpDir).Return(nil)
				copyCode := 0
				if tt.copyFailed {
					copyCode = 1
				}
				calls = append(calls, exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", "docker://"+candidate, "oci-archive:"+deltaFile).Return("", "delta download failed", copyCode))
				if !tt.copyFailed {
					importCode := 0
					if tt.importFailed {
						importCode = 1
					}
					output := importedID + "\n"
					if tt.importIdentityMissing {
						output = ""
					}
					calls = append(calls, exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", target.image, deltaFile).Return(output, "delta import failed", importCode))
					if !tt.importFailed && !tt.importIdentityMissing {
						status, statusCode := fmt.Sprintf(`{"status":{"id":%q}}`, importedID), 0
						if tt.runtimeImageMissing {
							status = `{"status":null}`
						}
						if tt.runtimeImageMismatch {
							status = fmt.Sprintf(`{"status":{"id":%q}}`, strings.TrimPrefix(historyDigest(30), "sha256:"))
						}
						if tt.runtimeInspectFailed {
							statusCode = 1
						}
						calls = append(calls, exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", commandArgs("inspecti", "--output", "json", importedID)...).Return(status, "image inspection failed", statusCode))
					}
				}
			}
			id := canonical
			if tt.opaque {
				id = historyDigest(20)
			}
			pullRef := canonical
			if tt.want == v1beta1.DeviceDeltaApplyOutcomeFallback {
				pullRef = target.image
			}
			pullCode := 0
			if tt.pullFailed {
				pullCode = 1
			}
			calls = append(calls, exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", append(append([]string(nil), criArgs...), "pull", pullRef), gomock.Any()).Return(id, "final pull failed", pullCode))
			if tt.mutable && tt.want != v1beta1.DeviceDeltaApplyOutcomeFallback && !tt.pullFailed {
				aliasCode, aliasID := 0, id
				if tt.aliasFailed {
					aliasCode = 1
				}
				if tt.aliasMoved {
					aliasID = repository + "@" + historyDigest(30)
				}
				calls = append(calls, exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", append(append([]string(nil), criArgs...), "pull", target.image), gomock.Any()).Return(aliasID, "desired reference pull failed", aliasCode))
			}
			destination := "unix:///run/crio/crio.sock#k8s.io"
			if !tt.opaque && tt.wantError == "" {
				calls = append(calls, exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", commandArgs("inspecti", "--output", "json", canonical)...).Return(fmt.Sprintf(`{"status":{"repoDigests":[%q]}}`, canonical), "", 0))
				rw.EXPECT().ReadFile(configPath).Return([]byte("image-endpoint: unix:///run/crio/crio.sock\n"), nil)
			}
			gomock.InOrder(calls...)
			err := m.pullCRIImage(t.Context(), target, task, skopeo, opts...)
			if tt.wantError != "" {
				require.ErrorContains(err, tt.wantError)
			} else {
				require.NoError(err)
			}
			c, ok := store.Context("app", target.image, "cri", "root", destination)
			require.True(ok)
			results, err := store.LookupApplicationResults([]deltastatus.ApplicationLookup{{Context: c, Digest: targetDigest}, {Context: c, Digest: sourceDigest}, {Context: c, Digest: historyDigest(30)}, {Context: c, Digest: "sha256:" + importedID}})
			require.NoError(err)
			require.Nil(results[1].Status)
			require.Nil(results[2].Status)
			require.Nil(results[3].Status)
			if tt.want == "" {
				require.Nil(results[0].Status)
				require.Empty(task.preparedDigest)
				require.Empty(store.ApplicationSnapshot("app").Histories)
			} else {
				require.NotNil(results[0].Status)
				require.Equal(tt.want, results[0].Status.Outcome)
				require.Equal(destination, task.destination)
				if tt.wantReason != "" {
					require.NotNil(results[0].Status.FallbackReason)
					require.Contains(*results[0].Status.FallbackReason, tt.wantReason)
				}
			}
		})
	}
}

func TestCompleteCRIPullRetainsContainerdTagging(t *testing.T) {
	t.Run("When containerd applies a delta it should retain its native desired-reference tagging", func(t *testing.T) {
		require := require.New(t)
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		exec, rw := executer.NewMockExecuter(ctrl), fileio.NewMockReadWriter(ctrl)
		logger := log.NewPrefixLogger("test")
		cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 1))
		target := imageRef{image: "quay.io/acme/app:target", runtime: OCITypeCRIImage}
		canonical := "quay.io/acme/app@" + historyDigest(10)
		task := &prefetchTask{ociType: OCITypeCRIImage, executionReference: canonical, runtimeEndpoint: "/run/containerd/containerd.sock"}
		m := &prefetchManager{log: logger, cliClients: client.NewCLIClients(client.WithCRIClient(cri))}
		gomock.InOrder(
			exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"pull", canonical}, gomock.Any()).Return(canonical, "", 0),
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "ctr", "--address", task.runtimeEndpoint, "--namespace", "k8s.io", "images", "tag", "--force", canonical, target.image).Return("", "", 0),
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", canonical).Return(fmt.Sprintf(`{"status":{"repoDigests":[%q]}}`, canonical), "", 0),
		)
		rw.EXPECT().ReadFile("/etc/crictl.yaml").Return([]byte("image-endpoint: unix:///run/containerd/containerd.sock\n"), nil)
		require.NoError(m.completeCRIPull(t.Context(), target, task, v1beta1.DeviceDeltaApplyOutcomeApplied, ""))
		require.Equal(historyDigest(10), task.preparedDigest)
	})
}
