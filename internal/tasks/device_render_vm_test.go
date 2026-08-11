package tasks

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Test helpers ────────────────────────────────────────────────────────────

// newTestVmInlineApp builds a VmApplication with inline: provider from a
// map[path]content, wrapped in an ApplicationProviderSpec.
func newTestVmInlineApp(t *testing.T, name string, files map[string]string, publishPorts []string) domain.ApplicationProviderSpec {
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
	if len(publishPorts) > 0 {
		vm.PublishPorts = &publishPorts
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

// makeTar returns a TAR archive containing the given filename→content map.
func makeTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// stubbedConverter returns a VmConverterFn that always emits a copy of the given Quadlet files.
// A copy is returned on each call to prevent parallel subtests from racing on the shared map.
func stubbedConverter(files map[string]string) VmConverterFn {
	return func(_ context.Context, _ []byte) (map[string]string, string, error) {
		out := make(map[string]string, len(files))
		for k, v := range files {
			out[k] = v
		}
		return out, "", nil
	}
}

// failingConverter returns a VmConverterFn that always returns an error with the given stderr.
func failingConverter(stderr string) VmConverterFn {
	return func(_ context.Context, _ []byte) (map[string]string, string, error) {
		return nil, stderr, errors.New("exit status 1")
	}
}

// emptyConverter returns a VmConverterFn that returns an empty map without an error.
func emptyConverter() VmConverterFn {
	return func(_ context.Context, _ []byte) (map[string]string, string, error) {
		return map[string]string{}, "", nil
	}
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

const fakePodUnit = "[Pod]\nNetwork=podman\n\n[Install]\nWantedBy=default.target\n"

// fakeQuadletFiles is a minimal set of Quadlet files that vm-to-quadlet would
// produce. The pod unit is deliberately named after KubeVirt's virt-launcher
// pod convention (not the raw VM name) to mirror real vm-to-quadlet output.
var fakeQuadletFiles = map[string]string{
	"virt-launcher-my-vm.pod":               fakePodUnit,
	"virt-launcher-my-vm-compute.container": "[Container]\nImage=quay.io/kubevirt/virt-launcher:v1.9.0\n",
}

func TestRenderVmApplication(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name           string
		vmName         string
		files          map[string]string
		publishPorts   []string
		converter      VmConverterFn
		kvStore        kvstore.KVStore
		wantAnnotation bool
		wantErr        string
	}{
		{
			name:   "When inline set has valid vm.yaml it should produce Quadlet files",
			vmName: "my-vm",
			files: map[string]string{
				"vm.yaml": minimalVmYAML("my-vm"),
			},
			converter:      stubbedConverter(fakeQuadletFiles),
			kvStore:        newFakeKVStore(),
			wantAnnotation: true,
		},
		{
			name:   "When publishPorts is set it should inject PublishPort into the .pod unit",
			vmName: "my-vm",
			files: map[string]string{
				"vm.yaml": minimalVmYAML("my-vm"),
			},
			publishPorts:   []string{"8080:80", "8443:443/tcp"},
			converter:      stubbedConverter(fakeQuadletFiles),
			kvStore:        newFakeKVStore(),
			wantAnnotation: true,
		},
		{
			name:   "When KV cache has a hit it should skip the converter",
			vmName: "my-vm",
			files: map[string]string{
				"vm.yaml": minimalVmYAML("my-vm"),
			},
			converter: func(_ context.Context, _ []byte) (map[string]string, string, error) {
				t.Error("converter must not be called on a cache hit")
				return nil, "", nil
			},
			kvStore: func() kvstore.KVStore {
				kv := newFakeKVStore()
				opts := DefaultVmRenderOptions()
				key := (&kvstore.VmQuadletFilesKey{}).ComposeKey([]byte(minimalVmYAML("my-vm")), opts.LauncherImage, opts.PasstWorkarounds)
				encoded, _ := json.Marshal(fakeQuadletFiles)
				kv.data[key] = encoded
				return kv
			}(),
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
			name:      "When converter stderr has a leading Error prefix it should be stripped",
			vmName:    "my-vm",
			files:     map[string]string{"vm.yaml": minimalVmYAML("my-vm")},
			converter: failingConverter("Error: unable to parse quantity's suffix"),
			kvStore:   newFakeKVStore(),
			wantErr:   "vm-to-quadlet: unable to parse quantity's suffix",
		},
		{
			name:   "When converter fails with empty stderr it should fall back to the exec error",
			vmName: "my-vm",
			files:  map[string]string{"vm.yaml": minimalVmYAML("my-vm")},
			converter: func(_ context.Context, _ []byte) (map[string]string, string, error) {
				return nil, "", errors.New("exit status 1")
			},
			kvStore: newFakeKVStore(),
			wantErr: "vm-to-quadlet: exit status 1",
		},
		{
			name:      "When converter returns no files it should return an error",
			vmName:    "my-vm",
			files:     map[string]string{"vm.yaml": minimalVmYAML("my-vm")},
			converter: emptyConverter(),
			kvStore:   newFakeKVStore(),
			wantErr:   "no output files",
		},
		{
			name:      "When vm.yaml is absent from inline set it should return an error",
			vmName:    "my-vm",
			files:     map[string]string{"other.yaml": "some content"},
			converter: stubbedConverter(fakeQuadletFiles),
			kvStore:   newFakeKVStore(),
			wantErr:   "vm.yaml not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			appSpec := newTestVmInlineApp(t, tc.vmName, tc.files, tc.publishPorts)
			vmApp, err := appSpec.AsVmApplication()
			require.NoError(t, err)

			result, err := renderVmApplication(ctx, vmApp, tc.converter, DefaultVmRenderOptions(), tc.kvStore)

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

			// Verify publishPorts injection. The pod unit is looked up by
			// suffix since vm-to-quadlet does not name it after the raw VM name.
			if len(tc.publishPorts) > 0 {
				podFileName, err := findPodFile(fileMap)
				require.NoError(t, err)
				podContent := fileMap[podFileName]
				for _, port := range tc.publishPorts {
					assert.Contains(t, podContent, "PublishPort="+port)
				}
			}

			assert.NotContains(t, fileMap, vmYamlFileName, "vm.yaml must not appear in the rendered QuadletApplication")
		})
	}
}

// TestRenderVmApplication_PreservesLifecycleFields verifies that desiredState/
// restartGeneration set on the source VmApplication (by the device render task's
// application lifecycle annotation overlay, see domain.OverlayApplicationLifecycle)
// survive the VM-to-Quadlet conversion done here, since renderVmApplication builds a
// brand new QuadletApplication rather than mutating the original app in place.
func TestRenderVmApplication_PreservesLifecycleFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	appSpec := newTestVmInlineApp(t, "my-vm", map[string]string{"vm.yaml": minimalVmYAML("my-vm")}, nil)
	vmApp, err := appSpec.AsVmApplication()
	require.NoError(t, err)
	vmApp.DesiredState = lo.ToPtr(domain.ApplicationDesiredStateStopped)
	vmApp.RestartGeneration = lo.ToPtr(3)

	result, err := renderVmApplication(ctx, vmApp, stubbedConverter(fakeQuadletFiles), DefaultVmRenderOptions(), newFakeKVStore())
	require.NoError(t, err)
	require.NotNil(t, result)

	quadlet, err := result.AsQuadletApplication()
	require.NoError(t, err)
	require.NotNil(t, quadlet.DesiredState)
	assert.Equal(t, domain.ApplicationDesiredStateStopped, *quadlet.DesiredState)
	require.NotNil(t, quadlet.RestartGeneration)
	assert.Equal(t, 3, *quadlet.RestartGeneration)
}

// TestRenderVmApplication_CachePopulatedOnMiss verifies that after a cache miss
// the Quadlet files are stored, and a second identical call returns the cached
// value without invoking the converter again.
func TestRenderVmApplication_CachePopulatedOnMiss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv := newFakeKVStore()
	callCount := 0
	converter := func(_ context.Context, _ []byte) (map[string]string, string, error) {
		callCount++
		out := make(map[string]string, len(fakeQuadletFiles))
		for k, v := range fakeQuadletFiles {
			out[k] = v
		}
		return out, "", nil
	}

	vmYAML := minimalVmYAML("my-vm")
	appSpec := newTestVmInlineApp(t, "my-vm", map[string]string{"vm.yaml": vmYAML}, nil)
	vmApp, err := appSpec.AsVmApplication()
	require.NoError(t, err)

	_, err = renderVmApplication(ctx, vmApp, converter, DefaultVmRenderOptions(), kv)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "converter should be called on the first (cache miss) call")

	_, err = renderVmApplication(ctx, vmApp, converter, DefaultVmRenderOptions(), kv)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "converter must not be called again on the second (cache hit) call")

	opts := DefaultVmRenderOptions()
	opts.PasstWorkarounds = true
	_, err = renderVmApplication(ctx, vmApp, converter, opts, kv)
	require.NoError(t, err)
	assert.Equal(t, 2, callCount, "converter should run again when passtWorkarounds changes")

	opts = DefaultVmRenderOptions()
	opts.LauncherImage = "registry.example.com/kubevirt/virt-launcher:custom"
	_, err = renderVmApplication(ctx, vmApp, converter, opts, kv)
	require.NoError(t, err)
	assert.Equal(t, 3, callCount, "converter should run again when launcherImage changes")
}

// fakeVmToQuadletBinary writes a shell script that records argv and emits a
// minimal Quadlet TAR on stdout. Returns the script path and the args file path.
func fakeVmToQuadletBinary(t *testing.T) (scriptPath, argsPath string) {
	t.Helper()
	dir := t.TempDir()
	argsPath = filepath.Join(dir, "args")
	tarPath := filepath.Join(dir, "out.tar")
	require.NoError(t, os.WriteFile(tarPath, makeTar(t, map[string]string{"my-vm.pod": "[Pod]\n"}), 0o600))

	scriptPath = filepath.Join(dir, "vm-to-quadlet")
	scriptBody := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\ncat %q\n", argsPath, tarPath)
	require.NoError(t, os.WriteFile(scriptPath, []byte(scriptBody), 0o600))
	require.NoError(t, os.Chmod(scriptPath, 0o700))
	return scriptPath, argsPath
}

// TestNewVmConverter_PassesRenderFlags verifies the converter invokes the binary
// with --launcher-image and --passt-workarounds from VmRenderOptions.
func TestNewVmConverter_PassesRenderFlags(t *testing.T) {
	t.Parallel()
	script, argsPath := fakeVmToQuadletBinary(t)

	opts := VmRenderOptions{
		LauncherImage:    "registry.example.com/kubevirt/virt-launcher:custom",
		PasstWorkarounds: false,
	}
	files, _, err := NewVmConverter(script, opts)(context.Background(), []byte("apiVersion: v1\n"))
	require.NoError(t, err)
	assert.Contains(t, files, "my-vm.pod")

	argsBytes, err := os.ReadFile(argsPath)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--launcher-image=registry.example.com/kubevirt/virt-launcher:custom",
		"--passt-workarounds=false",
	}, strings.Split(strings.TrimSpace(string(argsBytes)), "\n"))
}

// TestNewVmConverter_EmptyLauncherImageUsesDefault verifies an empty launcher
// image falls back to the built-in default before argv is built.
func TestNewVmConverter_EmptyLauncherImageUsesDefault(t *testing.T) {
	t.Parallel()
	script, argsPath := fakeVmToQuadletBinary(t)

	_, _, err := NewVmConverter(script, VmRenderOptions{PasstWorkarounds: true})(context.Background(), []byte("apiVersion: v1\n"))
	require.NoError(t, err)

	argsBytes, err := os.ReadFile(argsPath)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--launcher-image=" + config.DefaultVirtLauncherImage,
		"--passt-workarounds=true",
	}, strings.Split(strings.TrimSpace(string(argsBytes)), "\n"))
}

// TestVmRenderOptionsFromConfig verifies NewDeviceRenderLogic wires Worker.VmRender
// into the options used for conversion.
func TestVmRenderOptionsFromConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"worker": {
			"vmRender": {
				"launcherImage": "registry.example.com/kubevirt/virt-launcher:custom",
				"passtWorkarounds": false
			}
		}
	}`), cfg))

	logic := NewDeviceRenderLogic(logrus.New(), nil, nil, nil, nil, nil, cfg, [16]byte{}, domain.Event{})
	assert.Equal(t, "registry.example.com/kubevirt/virt-launcher:custom", logic.vmRenderOptions.LauncherImage)
	assert.False(t, logic.vmRenderOptions.PasstWorkarounds)

	defaults := NewDeviceRenderLogic(logrus.New(), nil, nil, nil, nil, nil, &config.Config{}, [16]byte{}, domain.Event{})
	assert.Equal(t, config.DefaultVirtLauncherImage, defaults.vmRenderOptions.LauncherImage)
	assert.False(t, defaults.vmRenderOptions.PasstWorkarounds)
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

	_, err = renderVmApplication(ctx, vmApp, stubbedConverter(fakeQuadletFiles), DefaultVmRenderOptions(), newFakeKVStore())
	require.ErrorContains(t, err, "not yet supported")
}

// ─── truncateStderr ───────────────────────────────────────────────────────────

func TestTruncateStderr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "When stderr has no Error prefix it should be returned unchanged",
			input: "failed to unmarshal VM: bad quantity",
			want:  "failed to unmarshal VM: bad quantity",
		},
		{
			name:  "When stderr has a leading Error prefix it should be stripped",
			input: "Error: failed to unmarshal VM: bad quantity",
			want:  "failed to unmarshal VM: bad quantity",
		},
		{
			name:  "When stderr exceeds the max length it should be truncated with a marker",
			input: "Error: " + strings.Repeat("x", maxStderrLen+10),
			want:  strings.Repeat("x", maxStderrLen) + "... (truncated)",
		},
		{
			name:  "When stderr is empty it should remain empty",
			input: "  ",
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, truncateStderr(tc.input))
		})
	}
}

// TestRenderVmApplication_StderrErrorPrefixNotDoubled verifies vm-to-quadlet's
// own "Error: " stderr prefix is stripped rather than doubled up with the
// wrapper's own "Error:" text added one level up in renderApplications, and
// that the redundant exec exit-status text is dropped when stderr already
// explains the failure.
func TestRenderVmApplication_StderrErrorPrefixNotDoubled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	appSpec := newTestVmInlineApp(t, "my-vm", map[string]string{"vm.yaml": minimalVmYAML("my-vm")}, nil)
	vmApp, err := appSpec.AsVmApplication()
	require.NoError(t, err)

	_, err = renderVmApplication(ctx, vmApp, failingConverter("Error: unable to parse quantity's suffix"), DefaultVmRenderOptions(), newFakeKVStore())
	require.Error(t, err)
	assert.Equal(t, `vm-to-quadlet: unable to parse quantity's suffix`, err.Error())
	assert.NotContains(t, err.Error(), "exit status")
}

// ─── parseTarFiles ────────────────────────────────────────────────────────────

func TestParseTarFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   map[string]string
		wantErr bool
	}{
		{
			name:  "When TAR has one file it should parse correctly",
			input: map[string]string{"my-vm.pod": "[Pod]\nNetwork=podman\n"},
		},
		{
			name: "When TAR has multiple files it should parse all correctly",
			input: map[string]string{
				"my-vm.pod":               "[Pod]\nNetwork=podman\n",
				"my-vm-compute.container": "[Container]\nImage=quay.io/test:latest\n",
			},
		},
		{
			name:    "When TAR is empty it should return an error",
			input:   map[string]string{},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := makeTar(t, tc.input)
			got, err := parseTarFiles(data)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.input, got)
		})
	}
}

// ─── injectPublishPorts ───────────────────────────────────────────────────────

func TestInjectPublishPorts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		files        map[string]string
		publishPorts []string
		wantPodFile  string
		wantPodHas   []string
		wantErr      string
	}{
		{
			name:         "When publishPorts is empty it should not modify any file",
			files:        map[string]string{"my-vm.pod": "[Pod]\n\n[Install]\nWantedBy=default.target\n"},
			publishPorts: nil,
		},
		{
			name:         "When publishPorts has one port it should inject into the Pod section",
			files:        map[string]string{"my-vm.pod": "[Pod]\nNetwork=podman\n\n[Install]\nWantedBy=default.target\n"},
			publishPorts: []string{"8080:80"},
			wantPodFile:  "my-vm.pod",
			wantPodHas:   []string{"PublishPort=8080:80"},
		},
		{
			name:         "When publishPorts has multiple ports it should inject all",
			files:        map[string]string{"my-vm.pod": "[Pod]\n\n[Install]\nWantedBy=default.target\n"},
			publishPorts: []string{"8080:80", "8443:443/tcp"},
			wantPodFile:  "my-vm.pod",
			wantPodHas:   []string{"PublishPort=8080:80", "PublishPort=8443:443/tcp"},
		},
		{
			name: "When the pod unit is not named after the VM it should still find and inject it",
			files: map[string]string{
				"virt-launcher-my-vm.pod":               "[Pod]\n\n[Install]\nWantedBy=default.target\n",
				"virt-launcher-my-vm-compute.container": "[Container]\n",
			},
			publishPorts: []string{"8080:80"},
			wantPodFile:  "virt-launcher-my-vm.pod",
			wantPodHas:   []string{"PublishPort=8080:80"},
		},
		{
			name:         "When .pod file is absent it should return an error",
			files:        map[string]string{"my-vm-compute.container": "[Container]\n"},
			publishPorts: []string{"8080:80"},
			wantErr:      "no .pod file found",
		},
		{
			name: "When multiple .pod files are present it should return an error",
			files: map[string]string{
				"my-vm.pod":   "[Pod]\n",
				"my-vm-2.pod": "[Pod]\n",
			},
			publishPorts: []string{"8080:80"},
			wantErr:      "expected exactly one .pod file",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Copy so we don't mutate the test table.
			files := make(map[string]string, len(tc.files))
			for k, v := range tc.files {
				files[k] = v
			}
			err := injectPublishPorts(files, tc.publishPorts)
			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			assert.NoError(t, err)
			if len(tc.wantPodHas) > 0 {
				podContent := files[tc.wantPodFile]
				for _, want := range tc.wantPodHas {
					assert.Contains(t, podContent, want)
				}
			}
		})
	}
}
