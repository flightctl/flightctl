package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/vm"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type buildWorkflowService struct {
	imagebuilderapi.Service
	builds  imagebuilderapi.ImageBuildService
	exports imagebuilderapi.ImageExportService
}

func (s *buildWorkflowService) ImageBuild() imagebuilderapi.ImageBuildService   { return s.builds }
func (s *buildWorkflowService) ImageExport() imagebuilderapi.ImageExportService { return s.exports }

type workflowQueue struct {
	queues.QueueConsumer
	processingErr error
	completed     bool
}

func (q *workflowQueue) Complete(_ context.Context, _ string, _ []byte, err error) error {
	q.completed = true
	q.processingErr = err
	return nil
}

type workflowBuildService struct {
	*mockImageBuildServiceForStatusUpdater
	logs string
}

func (s *workflowBuildService) UpdateLogs(_ context.Context, _ uuid.UUID, _ string, logs string) error {
	s.logs = logs
	return nil
}
func (s *workflowBuildService) GetLogs(_ context.Context, _ uuid.UUID, _ string, _ bool) (imagebuilderapi.LogStreamReader, string, coredomain.Status) {
	return nil, s.logs, coredomain.StatusOK()
}

func (s *workflowBuildService) Get(_ context.Context, _ uuid.UUID, _ string, _ bool) (*domain.ImageBuild, coredomain.Status) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result domain.ImageBuild
	deepCopyJSON(s.imageBuild, &result)
	return &result, coredomain.StatusOK()
}
func (s *workflowBuildService) Cancel(ctx context.Context, org uuid.UUID, name string) (*domain.ImageBuild, error) {
	build, _ := s.Get(ctx, org, name, false)
	domain.SetImageBuildStatusCondition(build.Status.Conditions, domain.ImageBuildCondition{Type: domain.ImageBuildConditionTypeReady, Status: domain.ConditionStatusFalse, Reason: string(domain.ImageBuildConditionReasonCanceling)})
	return s.UpdateStatus(ctx, org, build)
}

type workflowKV struct {
	*mockKVStore
	cancelService *workflowBuildService
	readyFile     string
}

func (k *workflowKV) StreamRead(ctx context.Context, _ string, _ string, _ time.Duration, _ int64) ([]kvstore.StreamEntry, error) {
	if k.cancelService != nil {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(k.readyFile); err == nil {
				_, err := k.cancelService.Cancel(ctx, uuid.Nil, "vm-build")
				return []kvstore.StreamEntry{{ID: "1", Value: []byte("cancel")}}, err
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
			}
		}
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestConsumeVMBuild(t *testing.T) {
	t.Run("When VM configuration is missing it should report a failed build without container fallback", func(t *testing.T) {
		ctx := context.Background()
		cfg := config.NewDefault()
		require.NoError(t, json.Unmarshal([]byte(`{"imageBuilderWorker":{"vm":null}}`), cfg))
		build := newTestImageBuild("vm-build", "late")
		svc := newMockImageBuildServiceForStatusUpdater(gomock.NewController(t), build)
		repos := newMockRepositoryStore()
		repos.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)
		logger := log.InitLogs()
		consumer := NewConsumer(nil, nil, repos, nil, nil, nil, &buildWorkflowService{builds: svc}, nil, cfg, logger)
		payload, err := json.Marshal(worker_client.EventWithOrgId{
			OrgId: uuid.New(),
			Event: coredomain.Event{InvolvedObject: coredomain.ObjectReference{Kind: string(domain.ResourceKindImageBuild), Name: "vm-build"}, Reason: coredomain.EventReasonResourceCreated},
		})
		require.NoError(t, err)
		queue := &workflowQueue{}
		err = consumer.Consume(ctx, payload, "entry-1", queue, logger)
		require.ErrorContains(t, err, "VM configuration is required")
		require.True(t, queue.completed)
		require.ErrorContains(t, queue.processingErr, "VM configuration is required")
		result, status := svc.Get(ctx, uuid.Nil, "vm-build", false)
		require.EqualValues(t, 200, status.Code)
		require.NotNil(t, result.Status.Conditions)
		ready := domain.FindImageBuildStatusCondition(*result.Status.Conditions, domain.ImageBuildConditionTypeReady)
		require.Equal(t, string(domain.ImageBuildConditionReasonFailed), ready.Reason)
	})
	for _, tc := range []struct {
		name         string
		sbom         bool
		serviceAuth  bool
		unsafeDigest bool
		cancelScan   bool
	}{
		{"When a guest builds and pushes an image it should publish its digest and complete the build", false, false, false, false},
		{"When SBOM is enabled it should stream the guest scan and complete despite publication failure", true, false, false, false},
		{"When service images require authentication it should deliver scoped credentials only to the guest", true, true, false, false},
		{"When the guest digest file points outside its output it should fail without exposing worker data", false, false, true, false},
		{"When a running guest scan is canceled it should finish the build as Canceled", true, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cfg := config.NewDefault()
			cfg.ImageBuilderWorker.VM = workflowRuntime(t)
			cfg.ImageBuilderWorker.SBOM.Enabled = tc.sbom
			if tc.unsafeDigest {
				t.Setenv("WORKFLOW_DIGEST_LINK", "1")
				secret := filepath.Join(t.TempDir(), "worker-secret")
				require.NoError(t, os.WriteFile(secret, []byte("private worker digest fixture"), 0600))
				t.Setenv("WORKFLOW_SECRET", secret)
			}
			if tc.serviceAuth {
				path := filepath.Join(os.Getenv("HOME"), ".config/containers/auth.json")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
				require.NoError(t, os.WriteFile(path, []byte(`{"auths":{"docker.io":{"auth":"c2VydmljZTp0ZXN0"},"docker.io/unrelated":{"auth":"b3RoZXI6c2VjcmV0"},"unrelated.example.com":{"auth":"b3RoZXI6c2VjcmV0"}}}`), 0600))
				t.Setenv("WORKFLOW_SERVICE_AUTH", "1")
			}
			build := newTestImageBuild("vm-build", "late")
			svc := &workflowBuildService{mockImageBuildServiceForStatusUpdater: newMockImageBuildServiceForStatusUpdater(gomock.NewController(t), build)}
			repos := newMockRepositoryStore()
			repos.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)
			repos.repositories["output-repo"] = createTestRepository("output-repo", "127.0.0.1:1", lo.ToPtr(coredomain.OciRepoSchemeHttp))
			logger := log.InitLogs()
			kv := &workflowKV{mockKVStore: newMockKVStore()}
			if tc.cancelScan {
				t.Setenv("WORKFLOW_CANCEL_SCAN", "1")
				ready := filepath.Join(t.TempDir(), "scan-ready")
				t.Setenv("WORKFLOW_SCAN_READY", ready)
				kv.cancelService = svc
				kv.readyFile = ready
			}
			consumer := NewConsumer(nil, nil, repos, nil, kv, nil, &buildWorkflowService{builds: svc}, nil, cfg, logger)
			payload, err := json.Marshal(worker_client.EventWithOrgId{
				OrgId: uuid.New(), Event: coredomain.Event{InvolvedObject: coredomain.ObjectReference{Kind: string(domain.ResourceKindImageBuild), Name: "vm-build"}, Reason: coredomain.EventReasonResourceCreated},
			})
			require.NoError(t, err)
			queue := &workflowQueue{}
			consumeErr := consumer.Consume(ctx, payload, "entry-1", queue, logger)
			if tc.unsafeDigest {
				require.ErrorContains(t, consumeErr, "manifest digest")
				result, _ := svc.Get(ctx, uuid.Nil, "vm-build", false)
				require.Nil(t, result.Status.ManifestDigest)
				ready := domain.FindImageBuildStatusCondition(*result.Status.Conditions, domain.ImageBuildConditionTypeReady)
				require.Equal(t, string(domain.ImageBuildConditionReasonFailed), ready.Reason)
				return
			}
			require.NoError(t, consumeErr)
			result, status := svc.Get(ctx, uuid.Nil, "vm-build", false)
			require.EqualValues(t, 200, status.Code)
			require.Equal(t, "127.0.0.1:1/output-image:v1.0.0", *result.Status.ImageReference)
			require.Equal(t, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", *result.Status.ManifestDigest)
			ready := domain.FindImageBuildStatusCondition(*result.Status.Conditions, domain.ImageBuildConditionTypeReady)
			if tc.cancelScan {
				require.Equal(t, string(domain.ImageBuildConditionReasonCanceled), ready.Reason)
				return
			}
			require.Equal(t, string(domain.ImageBuildConditionReasonCompleted), ready.Reason)
			require.True(t, queue.completed)
			require.NoError(t, queue.processingErr)
			if tc.sbom {
				_, logs, status := svc.GetLogs(ctx, uuid.Nil, "vm-build", false)
				require.EqualValues(t, 200, status.Code)
				require.Contains(t, logs, "guest syft scan complete")
			}
		})
	}
}

func workflowRuntime(t *testing.T) *vm.Config {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("WORKFLOW_STATE", t.TempDir())
	t.Setenv("WORKFLOW_OUTPUT_FILE", filepath.Join(t.TempDir(), "output-path"))
	scripts := map[string]string{
		"qemu-img":    "exit 0",
		"genisoimage": "exit 0",
		"ssh-keygen":  `while [ $# -gt 0 ]; do if [ "$1" = -f ]; then shift; printf 'ssh-ed25519 test\n' > "$1.pub"; fi; shift; done`,
		"qemu-system-x86_64": `for arg do case "$arg" in local,path=*,mount_tag=output,*) arg=${arg#local,path=}; printf '%s' "${arg%%,mount_tag=*}" > "$WORKFLOW_OUTPUT_FILE";; esac; done
exec sleep 300`,
		"ssh": `test -s "$WORKFLOW_OUTPUT_FILE" || exit 1
for arg do cmd=$arg; done
case "$cmd" in *"'write-file'"*) path=${cmd##* }; path=${path#\'}; path=${path%\'}; cat > "$WORKFLOW_STATE/$(basename "$path")"; exit 0;; esac
case "$cmd" in *"'sh' '-s'"*) cat >/dev/null; exit 0;; *"'mkdir'"*|*"'cp'"*) exit 0;; esac
WORKFLOW_IN_GUEST=1
WORKFLOW_OUTPUT=$(cat "$WORKFLOW_OUTPUT_FILE")
export WORKFLOW_IN_GUEST WORKFLOW_OUTPUT
exec /bin/sh -c "$cmd"`,
		"sha256sum": `if [ "${WORKFLOW_CORRUPT:-}" = 1 ];then printf '%064d file\n' 0;exit 0;fi
 for arg do case "$arg" in /output/*) set -- "${arg#/output/}";; esac; done
/usr/bin/sha256sum -b "$WORKFLOW_OUTPUT/$1"
if [ "${WORKFLOW_REPLACE:-}" = 1 ];then rm "$WORKFLOW_OUTPUT/$1";ln -s "$WORKFLOW_SECRET" "$WORKFLOW_OUTPUT/$1";fi`,
		"podman": `test "${WORKFLOW_IN_GUEST:-}" = 1 || { echo 'outer container execution is forbidden' >&2; exit 1; }
if [ "${WORKFLOW_BIB_ARTIFACT:-}" = 1 ];then case "$*" in *bootc-image-builder*build*) mkdir -p "$WORKFLOW_OUTPUT/qcow2";printf 'vm-export-bytes' > "$WORKFLOW_OUTPUT/qcow2/disk.qcow2";if [ "${WORKFLOW_CONTAINERFILE_LINK:-}" = 1 ];then ln -s "$WORKFLOW_SECRET" "$WORKFLOW_OUTPUT/qcow2/Containerfile";fi;; esac;fi
if [ "${WORKFLOW_BIB_FAIL:-}" = 1 ]; then case "$*" in *bootc-image-builder*build*) echo 'guest conversion failed' >&2; exit 42;; esac; fi
if [ "${WORKFLOW_SERVICE_AUTH:-}" = 1 ]; then case "$*" in *syft*) test -f "$WORKFLOW_STATE/service-auth.json" || exit 23; grep -q docker.io "$WORKFLOW_STATE/service-auth.json" || exit 24; if grep -q unrelated "$WORKFLOW_STATE/service-auth.json";then exit 25;fi;; esac; fi
case "$*" in *syft*scan*) if [ "${WORKFLOW_CANCEL_SCAN:-}" = 1 ];then touch "$WORKFLOW_SCAN_READY";exec sleep 300;fi;printf '{"bomFormat":"CycloneDX","specVersion":"1.5","components":[]}' > "$WORKFLOW_OUTPUT/sbom.json";echo 'guest syft scan complete';; esac
case "$1" in push) if [ "${WORKFLOW_DIGEST_LINK:-}" = 1 ];then ln -s "$WORKFLOW_SECRET" "$WORKFLOW_OUTPUT/push-digest.txt";exit 0;fi;printf 'sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n' > "$WORKFLOW_OUTPUT/push-digest.txt";; esac
echo 'guest progress'`,
	}
	for name, body := range scripts {
		//nolint:gosec // G306: The temporary executable fixture is private to this test.
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700))
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	base := filepath.Join(t.TempDir(), "guest.qcow2")
	require.NoError(t, os.WriteFile(base, []byte("base disk"), 0600))
	return &vm.Config{BaseImage: base, Accelerator: "tcg", MemoryMiB: 1024, CPUs: 1, DiskSizeGiB: 4, BootTimeout: util.Duration(30 * time.Second)}
}

func TestConsumeVMExport(t *testing.T) {
	cases := []struct{ name, accelerator, wantError string }{
		{"When export uses software emulation it should fail with the KVM requirement", "tcg", "ImageExport requires KVM"},
		{"When guest conversion fails it should report the failed export", "kvm", "failed to run bootc-image-builder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := config.NewDefault()
			cfg.ImageBuilderWorker.VM = workflowRuntime(t)
			cfg.ImageBuilderWorker.VM.Accelerator = tc.accelerator
			t.Setenv("WORKFLOW_BIB_FAIL", "1")
			build := makeCompletedBuild("build-1", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
			build.Spec.Destination.Repository = "output-repo"
			export := makeExportWithCondition("vm-export", "build-1", domain.ExportFormatTypeQCOW2, domain.ImageExportConditionReasonPending)
			buildSvc := newMockImageBuildServiceForStatusUpdater(gomock.NewController(t), build)
			exportSvc := newMockImageExportServiceForStatusUpdater(gomock.NewController(t), export)
			repos := newMockRepositoryStore()
			repos.repositories["output-repo"] = createTestRepository("output-repo", "registry.example.com", nil)
			logger := log.InitLogs()
			consumer := NewConsumer(nil, nil, repos, nil, nil, nil, &buildWorkflowService{builds: buildSvc, exports: exportSvc}, nil, cfg, logger)
			payload, err := json.Marshal(worker_client.EventWithOrgId{OrgId: uuid.New(), Event: coredomain.Event{InvolvedObject: coredomain.ObjectReference{Kind: string(domain.ResourceKindImageExport), Name: "vm-export"}, Reason: coredomain.EventReasonResourceCreated}})
			require.NoError(t, err)
			queue := &workflowQueue{}
			err = consumer.Consume(ctx, payload, "entry-1", queue, logger)
			require.ErrorContains(t, err, tc.wantError)
			result, status := exportSvc.Get(ctx, uuid.Nil, "vm-export")
			require.EqualValues(t, 200, status.Code)
			ready := domain.FindImageExportStatusCondition(*result.Status.Conditions, domain.ImageExportConditionTypeReady)
			require.Equal(t, string(domain.ImageExportConditionReasonFailed), ready.Reason)
			require.True(t, queue.completed)
			require.ErrorContains(t, queue.processingErr, tc.wantError)
		})
	}
}

func TestConsumeExportArtifact(t *testing.T) {
	for _, tc := range []struct {
		name         string
		corrupt      bool
		replace      bool
		templateLink bool
	}{
		{"When artifact checksums disagree it should fail without publishing an export", true, false, false},
		{"When a verified export is ready it should publish its bytes and referrer then clean up", false, false, false},
		{"When a guest replaces a verified artifact with a symlink it should reject the final output", false, true, false},
		{"When a containerdisk guest plants a template symlink it should leave worker files intact", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.ImageBuilderWorker.VM = workflowRuntime(t)
			cfg.ImageBuilderWorker.VM.Accelerator = "kvm"
			t.Setenv("WORKFLOW_BIB_ARTIFACT", "1")
			if tc.corrupt {
				t.Setenv("WORKFLOW_CORRUPT", "1")
			} else {
				t.Setenv("WORKFLOW_CORRUPT", "0")
			}
			if tc.replace {
				t.Setenv("WORKFLOW_REPLACE", "1")
				secret := filepath.Join(t.TempDir(), "worker-secret")
				require.NoError(t, os.WriteFile(secret, []byte("private worker bytes"), 0600))
				t.Setenv("WORKFLOW_SECRET", secret)
			}
			var workerFile string
			if tc.templateLink {
				t.Setenv("WORKFLOW_CONTAINERFILE_LINK", "1")
				workerFile = filepath.Join(t.TempDir(), "worker-config")
				require.NoError(t, os.WriteFile(workerFile, []byte("worker config fixture"), 0600))
				t.Setenv("WORKFLOW_SECRET", workerFile)
			}
			registry := newWorkflowRegistry(t)
			build := makeCompletedBuild("build-1", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
			build.Spec.Destination.Repository = "output-repo"
			export := makeExportWithCondition("vm-export", "build-1", domain.ExportFormatTypeQCOW2, domain.ImageExportConditionReasonPending)
			if tc.templateLink {
				export.Spec.Format = domain.ExportFormatTypeQCOW2DiskContainer
			}
			buildSvc := newMockImageBuildServiceForStatusUpdater(gomock.NewController(t), build)
			exportSvc := newMockImageExportServiceForStatusUpdater(gomock.NewController(t), export)
			repos := newMockRepositoryStore()
			repos.repositories["output-repo"] = createTestRepository("output-repo", strings.TrimPrefix(registry.server.URL, "http://"), lo.ToPtr(coredomain.OciRepoSchemeHttp))
			logger := log.InitLogs()
			consumer := NewConsumer(nil, nil, repos, nil, nil, nil, &buildWorkflowService{builds: buildSvc, exports: exportSvc}, nil, cfg, logger)
			payload, err := json.Marshal(worker_client.EventWithOrgId{OrgId: uuid.New(), Event: coredomain.Event{InvolvedObject: coredomain.ObjectReference{Kind: string(domain.ResourceKindImageExport), Name: "vm-export"}, Reason: coredomain.EventReasonResourceCreated}})
			require.NoError(t, err)
			err = consumer.Consume(t.Context(), payload, "entry-1", &workflowQueue{}, logger)
			if tc.templateLink {
				require.ErrorContains(t, err, "failed to build container disk image")
				data, readErr := os.ReadFile(workerFile)
				require.NoError(t, readErr)
				require.Equal(t, []byte("worker config fixture"), data)
				return
			}
			if tc.corrupt {
				require.ErrorContains(t, err, "checksum mismatch")
			} else if tc.replace {
				require.ErrorContains(t, err, "symlink")
			} else {
				require.NoError(t, err)
			}
			result, _ := exportSvc.Get(t.Context(), uuid.Nil, "vm-export")
			ready := domain.FindImageExportStatusCondition(*result.Status.Conditions, domain.ImageExportConditionTypeReady)
			registry.mu.Lock()
			defer registry.mu.Unlock()
			if tc.corrupt || tc.replace {
				require.Equal(t, string(domain.ImageExportConditionReasonFailed), ready.Reason)
				require.Empty(t, registry.manifests)
			} else {
				require.Equal(t, string(domain.ImageExportConditionReasonCompleted), ready.Reason)
				require.NotNil(t, result.Status.ManifestDigest)
				var manifest ocispec.Manifest
				require.NoError(t, json.Unmarshal(registry.manifests[*result.Status.ManifestDigest], &manifest))
				require.NotNil(t, manifest.Subject)
				require.Equal(t, "sha256:f20c43161d73848408ef247f0ec7111b19fe58ffebc0cbcaa0d2c8bda4967268", manifest.Subject.Digest.String())
				require.Len(t, manifest.Layers, 1)
				require.Equal(t, []byte("vm-export-bytes"), registry.blobs[manifest.Layers[0].Digest.String()])
			}
			path, readErr := os.ReadFile(os.Getenv("WORKFLOW_OUTPUT_FILE"))
			require.NoError(t, readErr)
			require.NoDirExists(t, string(path))
		})
	}
}

type workflowRegistry struct {
	server    *httptest.Server
	mu        sync.Mutex
	upload    []byte
	blobs     map[string][]byte
	manifests map[string][]byte
}

func newWorkflowRegistry(t *testing.T) *workflowRegistry {
	t.Helper()
	r := &workflowRegistry{blobs: map[string][]byte{}, manifests: map[string][]byte{}}
	subject := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[]}`)
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		path := req.URL.Path
		switch {
		case req.Method == http.MethodHead && strings.Contains(path, "/manifests/v1.0"):
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Content-Length", strconv.Itoa(len(subject)))
			w.Header().Set("Docker-Content-Digest", digest.FromBytes(subject).String())
			w.WriteHeader(http.StatusOK)
		case req.Method == http.MethodPost && strings.Contains(path, "/blobs/uploads"):
			r.upload = nil
			w.Header().Set("Location", "/v2/build-1/blobs/uploads/attempt")
			w.WriteHeader(http.StatusAccepted)
		case (req.Method == http.MethodPut || req.Method == http.MethodPatch) && strings.Contains(path, "/blobs/uploads/"):
			data, _ := io.ReadAll(req.Body)
			r.upload = append(r.upload, data...)
			if req.Method == http.MethodPatch {
				w.Header().Set("Location", path)
				w.WriteHeader(http.StatusAccepted)
				return
			}
			dgst := req.URL.Query().Get("digest")
			r.blobs[dgst] = append([]byte(nil), r.upload...)
			w.Header().Set("Docker-Content-Digest", dgst)
			w.Header().Set("Location", "/v2/build-1/blobs/"+dgst)
			w.WriteHeader(http.StatusCreated)
		case req.Method == http.MethodHead && strings.Contains(path, "/blobs/"):
			dgst := path[strings.LastIndex(path, "/")+1:]
			if data, ok := r.blobs[dgst]; ok {
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				w.Header().Set("Docker-Content-Digest", dgst)
				w.WriteHeader(http.StatusOK)
			} else {
				http.NotFound(w, req)
			}
		case req.Method == http.MethodPut && strings.Contains(path, "/manifests/"):
			data, _ := io.ReadAll(req.Body)
			dgst := digest.FromBytes(data).String()
			r.manifests[dgst] = data
			w.Header().Set("Docker-Content-Digest", dgst)
			w.Header().Set("OCI-Subject", digest.FromBytes(subject).String())
			w.Header().Set("Location", path)
			w.WriteHeader(http.StatusCreated)
		case req.Method == http.MethodGet && strings.Contains(path, "/referrers/"):
			w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
			_, _ = fmt.Fprint(w, `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}
