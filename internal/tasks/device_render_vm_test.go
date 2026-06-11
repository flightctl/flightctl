package tasks

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Test helpers ────────────────────────────────────────────────────────────

// newTestVmInlineApp builds a VmApplication with inline: provider from a
// map[path]content, wrapped in an ApplicationProviderSpec.
func newTestVmInlineApp(t *testing.T, name string, files map[string]string) domain.ApplicationProviderSpec {
	t.Helper()
	contents := make([]domain.ApplicationContent, 0, len(files))
	for path, content := range files {
		c := content
		contents = append(contents, domain.ApplicationContent{Path: path, Content: &c})
	}
	inlineSpec := domain.InlineApplicationProviderSpec{Inline: contents}

	vm := domain.VmApplication{
		AppType: domain.AppTypeVm,
		Name:    lo.ToPtr(name),
	}
	require.NoError(t, vm.FromInlineApplicationProviderSpec(inlineSpec))

	var spec domain.ApplicationProviderSpec
	require.NoError(t, spec.FromVmApplication(vm))
	return spec
}

// minimalVmYAML returns a minimal valid KubeVirt VirtualMachine YAML for test input.
func minimalVmYAML(name string) string {
	return fmt.Sprintf(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: %s
spec:
  running: true
  template:
    spec:
      domain:
        cpu:
          cores: 1
        memory:
          guest: 1Gi
        devices: {}
      networks: []
      volumes: []
`, name)
}

// stubbedConverter returns a VmConverterFn that always emits a fixed pod YAML string.
func stubbedConverter(podYAML string) VmConverterFn {
	return func(_ context.Context, _ []byte) ([]byte, string, error) {
		return []byte(podYAML), "", nil
	}
}

// failingConverter returns a VmConverterFn that always returns an error with the given stderr.
func failingConverter(stderr string) VmConverterFn {
	return func(_ context.Context, _ []byte) ([]byte, string, error) {
		return nil, stderr, errors.New("exit status 1")
	}
}

// emptyConverter returns a VmConverterFn that returns empty stdout without an error.
func emptyConverter() VmConverterFn {
	return func(_ context.Context, _ []byte) ([]byte, string, error) {
		return []byte{}, "", nil
	}
}

// vmPodCacheKey returns the KV key for a given vm.yaml content, matching the
// production implementation so tests can pre-populate the cache.
func vmPodCacheKey(vmYAML string) string {
	sum := sha256.Sum256([]byte(vmYAML))
	return (&kvstore.VmPodYamlKey{Sha256: fmt.Sprintf("%x", sum)}).ComposeKey()
}

// ─── fakeKVStore ──────────────────────────────────────────────────────────────

// fakeKVStore is a minimal in-memory KVStore for unit tests.
type fakeKVStore struct {
	data map[string][]byte
}

func newFakeKVStore() *fakeKVStore {
	return &fakeKVStore{data: make(map[string][]byte)}
}

func (f *fakeKVStore) Close()                                                         {}
func (f *fakeKVStore) PrintAllKeys(_ context.Context)                                 {}
func (f *fakeKVStore) DeleteAllKeys(_ context.Context) error                          { return nil }
func (f *fakeKVStore) DeleteKeysForTemplateVersion(_ context.Context, _ string) error { return nil }
func (f *fakeKVStore) Delete(_ context.Context, key string) error                     { delete(f.data, key); return nil }
func (f *fakeKVStore) SetIfGreater(_ context.Context, _ string, _ int64) (bool, error) {
	return false, nil
}
func (f *fakeKVStore) SetExpire(_ context.Context, _ string, _ time.Duration) error { return nil }
func (f *fakeKVStore) StreamAdd(_ context.Context, _ string, _ []byte) (string, error) {
	return "", nil
}
func (f *fakeKVStore) StreamRange(_ context.Context, _ string, _, _ string) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (f *fakeKVStore) StreamRead(_ context.Context, _ string, _ string, _ time.Duration, _ int64) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (f *fakeKVStore) GetOrSetNX(_ context.Context, key string, value []byte) ([]byte, error) {
	if existing := f.data[key]; existing != nil {
		return existing, nil
	}
	f.data[key] = value
	return value, nil
}

func (f *fakeKVStore) SetNX(_ context.Context, key string, value []byte) (bool, error) {
	if _, ok := f.data[key]; ok {
		return false, nil
	}
	f.data[key] = value
	return true, nil
}

func (f *fakeKVStore) Get(_ context.Context, key string) ([]byte, error) {
	return f.data[key], nil
}

var _ kvstore.KVStore = (*fakeKVStore)(nil)

// ─── renderVmApplication ──────────────────────────────────────────────────────

const (
	fakePodYAML    = "apiVersion: v1\nkind: Pod\nmetadata:\n  name: my-vm\n"
	customKubeUnit = "[Kube]\nYaml=pod.yaml\nKubeDownForce=true\nPublishPort=8080:8080/tcp\n"
)

func TestRenderVmApplication(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name           string
		vmName         string
		files          map[string]string
		converter      VmConverterFn
		kvStore        kvstore.KVStore
		wantPodYAML    string
		wantKubeUnit   string
		wantAnnotation bool
		wantErr        string
	}{
		{
			name:   "When inline set has only vm.yaml it should generate the default .kube unit",
			vmName: "my-vm",
			files: map[string]string{
				"vm.yaml": minimalVmYAML("my-vm"),
			},
			converter:      stubbedConverter(fakePodYAML),
			kvStore:        newFakeKVStore(),
			wantPodYAML:    fakePodYAML,
			wantKubeUnit:   defaultKubeUnit,
			wantAnnotation: true,
		},
		{
			name:   "When inline set contains a .kube file it should pass it through unchanged",
			vmName: "my-vm",
			files: map[string]string{
				"vm.yaml":    minimalVmYAML("my-vm"),
				"my-vm.kube": customKubeUnit,
			},
			converter:      stubbedConverter(fakePodYAML),
			kvStore:        newFakeKVStore(),
			wantPodYAML:    fakePodYAML,
			wantKubeUnit:   customKubeUnit,
			wantAnnotation: true,
		},
		{
			name:   "When KV cache has a hit it should skip the converter",
			vmName: "my-vm",
			files: map[string]string{
				"vm.yaml": minimalVmYAML("my-vm"),
			},
			converter: func(_ context.Context, _ []byte) ([]byte, string, error) {
				t.Error("converter must not be called on a cache hit")
				return nil, "", nil
			},
			kvStore: func() kvstore.KVStore {
				kv := newFakeKVStore()
				kv.data[vmPodCacheKey(minimalVmYAML("my-vm"))] = []byte(fakePodYAML)
				return kv
			}(),
			wantPodYAML:    fakePodYAML,
			wantKubeUnit:   defaultKubeUnit,
			wantAnnotation: true,
		},
		{
			name:      "When converter exits non-zero it should surface the subprocess stderr as the error",
			vmName:    "my-vm",
			files:     map[string]string{"vm.yaml": minimalVmYAML("my-vm")},
			converter: failingConverter("unsupported vm feature"),
			kvStore:   newFakeKVStore(),
			wantErr:   "unsupported vm feature",
		},
		{
			name:      "When converter returns empty stdout it should return an error",
			vmName:    "my-vm",
			files:     map[string]string{"vm.yaml": minimalVmYAML("my-vm")},
			converter: emptyConverter(),
			kvStore:   newFakeKVStore(),
			wantErr:   "produced empty output",
		},
		{
			name:      "When vm.yaml is absent from inline set it should return an error",
			vmName:    "my-vm",
			files:     map[string]string{"other.yaml": "some content"},
			converter: stubbedConverter(fakePodYAML),
			kvStore:   newFakeKVStore(),
			wantErr:   "vm.yaml not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			appSpec := newTestVmInlineApp(t, tc.vmName, tc.files)
			vmApp, err := appSpec.AsVmApplication()
			require.NoError(t, err)

			result, err := renderVmApplication(ctx, vmApp, tc.converter, tc.kvStore)

			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			quadlet, err := result.AsQuadletApplication()
			require.NoError(t, err)

			if tc.wantAnnotation {
				require.NotNil(t, quadlet.Annotations)
				assert.Equal(t, vmWorkloadTypeAnnotationValue, (*quadlet.Annotations)[vmWorkloadTypeAnnotationKey])
			}

			inlineOut, err := quadlet.AsInlineApplicationProviderSpec()
			require.NoError(t, err)

			fileMap := make(map[string]string, len(inlineOut.Inline))
			for _, f := range inlineOut.Inline {
				if f.Content != nil {
					fileMap[f.Path] = *f.Content
				}
			}

			assert.Equal(t, tc.wantPodYAML, fileMap["pod.yaml"], "pod.yaml content")
			assert.Equal(t, tc.wantKubeUnit, fileMap[tc.vmName+".kube"], ".kube unit content")
			assert.NotContains(t, fileMap, vmYamlFileName, "vm.yaml must not appear in the rendered QuadletApplication")
		})
	}
}

// TestRenderVmApplication_CachePopulatedOnMiss verifies that after a cache miss
// the pod.yaml is stored, and a second identical call returns the cached value
// without invoking the converter again.
func TestRenderVmApplication_CachePopulatedOnMiss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv := newFakeKVStore()
	callCount := 0
	converter := func(_ context.Context, _ []byte) ([]byte, string, error) {
		callCount++
		return []byte(fakePodYAML), "", nil
	}

	vmYAML := minimalVmYAML("my-vm")
	appSpec := newTestVmInlineApp(t, "my-vm", map[string]string{"vm.yaml": vmYAML})
	vmApp, err := appSpec.AsVmApplication()
	require.NoError(t, err)

	_, err = renderVmApplication(ctx, vmApp, converter, kv)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "converter should be called on the first (cache miss) call")

	_, err = renderVmApplication(ctx, vmApp, converter, kv)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "converter must not be called again on the second (cache hit) call")
}

// TestRenderVmApplication_ImageProviderUnsupported verifies that a VmApplication
// using the image: provider returns a clear error.
func TestRenderVmApplication_ImageProviderUnsupported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	imageSpec := domain.ImageApplicationProviderSpec{Image: "quay.io/org/vm-disk:latest"}
	vm := domain.VmApplication{AppType: domain.AppTypeVm, Name: lo.ToPtr("my-vm")}
	require.NoError(t, vm.FromImageApplicationProviderSpec(imageSpec))
	var appSpec domain.ApplicationProviderSpec
	require.NoError(t, appSpec.FromVmApplication(vm))

	vmApp, err := appSpec.AsVmApplication()
	require.NoError(t, err)

	_, err = renderVmApplication(ctx, vmApp, stubbedConverter(fakePodYAML), newFakeKVStore())
	require.ErrorContains(t, err, "not yet supported")
}

// ─── buildKubeUnit ────────────────────────────────────────────────────────────

func TestBuildKubeUnit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		kubeContent *string
		want        string
	}{
		{
			name:        "When kubeContent is nil it should return the default unit",
			kubeContent: nil,
			want:        defaultKubeUnit,
		},
		{
			name:        "When kubeContent is provided it should return it verbatim",
			kubeContent: lo.ToPtr(customKubeUnit),
			want:        customKubeUnit,
		},
		{
			name:        "When kubeContent is an empty string it should return it verbatim",
			kubeContent: lo.ToPtr(""),
			want:        "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, buildKubeUnit(tc.kubeContent))
		})
	}
}
