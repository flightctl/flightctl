package tasks

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/quadlet"
)

const (
	vmToQuadletBinary                   = "vm-to-quadlet"
	vmWorkloadTypeAnnotationKey         = "flightctl.io/workload-type"
	vmWorkloadTypeAnnotationValue       = "vm"
	vmYamlFileName                      = "vm.yaml"
	maxStderrLen                        = 512
	maxStdoutBytes                      = 16 * 1024 * 1024 // 16 MiB
	maxStderrBytes                      = 64 * 1024        // 64 KiB
	maxTarEntryBytes              int64 = 1 * 1024 * 1024  // 1 MiB per TAR entry
)

// limitedWriter wraps a bytes.Buffer and returns an error once more than limit
// bytes have been written. This causes cmd.Run() to fail fast rather than
// allowing unbounded memory growth from a misbehaving subprocess.
type limitedWriter struct {
	buf   bytes.Buffer
	limit int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	remaining := lw.limit - lw.buf.Len()
	if remaining <= 0 {
		return 0, fmt.Errorf("subprocess output exceeded limit of %d bytes", lw.limit)
	}
	if len(p) > remaining {
		p = p[:remaining]
		n, _ := lw.buf.Write(p)
		return n, fmt.Errorf("subprocess output exceeded limit of %d bytes", lw.limit)
	}
	return lw.buf.Write(p)
}

// truncateStderr trims and caps subprocess stderr to avoid embedding large or
// sensitive tool output directly in error messages.
func truncateStderr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxStderrLen {
		return s[:maxStderrLen] + "... (truncated)"
	}
	return s
}

// VmConverterFn accepts KubeVirt VM YAML on stdin and returns a map of Quadlet
// unit filename → content, read from the TAR stream that vm-to-quadlet writes
// to stdout. Passed explicitly to renderVmApplication — no package-level
// variable is used so tests can inject stubs without sharing state.
type VmConverterFn func(ctx context.Context, vmYAML []byte) (files map[string]string, stderr string, err error)

// NewVmConverter returns a VmConverterFn that invokes the vm-to-quadlet binary
// at binaryPath. The binary reads VM YAML from stdin and writes a TAR archive
// of Quadlet unit files to stdout. binaryPath may be an absolute path or a
// bare binary name resolved via PATH. Exported so integration tests can point
// the converter at a binary extracted to a temporary directory.
func NewVmConverter(binaryPath string) VmConverterFn {
	return func(ctx context.Context, vmYAML []byte) (map[string]string, string, error) {
		cmd := exec.CommandContext(ctx, binaryPath)
		cmd.Stdin = bytes.NewReader(vmYAML)

		stdoutBuf := &limitedWriter{limit: maxStdoutBytes}
		stderrBuf := &limitedWriter{limit: maxStderrBytes}
		cmd.Stdout = stdoutBuf
		cmd.Stderr = stderrBuf

		if err := cmd.Run(); err != nil {
			return nil, stderrBuf.buf.String(), err
		}

		files, err := parseTarFiles(stdoutBuf.buf.Bytes())
		if err != nil {
			return nil, stderrBuf.buf.String(), fmt.Errorf("parsing vm-to-quadlet output: %w", err)
		}
		return files, stderrBuf.buf.String(), nil
	}
}

// parseTarFiles reads a TAR archive and returns a filename → content map.
func parseTarFiles(data []byte) (map[string]string, error) {
	tr := tar.NewReader(bytes.NewReader(data))
	files := make(map[string]string)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading TAR entry: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		clean := strings.TrimPrefix(hdr.Name, "./")
		if !filepath.IsLocal(clean) {
			return nil, fmt.Errorf("vm-to-quadlet produced TAR entry with unsafe path %q", hdr.Name)
		}
		limited := io.LimitReader(tr, maxTarEntryBytes+1)
		content, err := io.ReadAll(limited)
		if err != nil {
			return nil, fmt.Errorf("reading TAR entry %q: %w", hdr.Name, err)
		}
		if int64(len(content)) > maxTarEntryBytes {
			return nil, fmt.Errorf("TAR entry %q exceeds maximum size of %d bytes", hdr.Name, maxTarEntryBytes)
		}
		name := clean
		files[name] = string(content)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("vm-to-quadlet produced no output files")
	}
	return files, nil
}

// defaultVmConverter is the production converter backed by the vm-to-quadlet
// binary that must be present in PATH at runtime.
var defaultVmConverter = NewVmConverter(vmToQuadletBinary)

// renderVmApplication converts a VmApplication (inline: provider) into a
// QuadletApplication by:
//  1. Extracting vm.yaml from the VmApplication inline set.
//  2. Checking the KV-store cache (SHA-256 of vm.yaml content); returning
//     the cached Quadlet files directly on a hit.
//  3. On a cache miss: invoking the vm-to-quadlet subprocess via the
//     provided converter, then storing the result in the cache.
//  4. Injecting PublishPort= directives from VmApplication.publishPorts into
//     the generated .pod unit file.
//  5. Emitting a QuadletApplication with the flightctl.io/workload-type: vm
//     annotation so the agent can identify the workload without inspecting
//     image names.
func renderVmApplication(ctx context.Context, vmApp domain.VmApplication, converter VmConverterFn, kvStore kvstore.KVStore) (*domain.ApplicationProviderSpec, error) {
	providerType := vmApp.Type()
	if providerType != domain.InlineApplicationProviderType {
		return nil, fmt.Errorf("VmApplication with %q provider is not yet supported for server-side rendering; use inline: with a vm.yaml file", providerType)
	}

	inlineSpec, err := vmApp.AsInlineApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("extracting inline spec from vm application: %w", err)
	}

	// Locate vm.yaml in the inline set.
	var vmYAMLContent *string
	for i := range inlineSpec.Inline {
		f := &inlineSpec.Inline[i]
		if f.Path == vmYamlFileName {
			vmYAMLContent = f.Content
			break
		}
	}
	if vmYAMLContent == nil {
		return nil, fmt.Errorf("vm.yaml not found in VmApplication inline set")
	}
	if strings.TrimSpace(*vmYAMLContent) == "" {
		return nil, fmt.Errorf("vm.yaml content is empty in VmApplication inline set")
	}

	quadletFiles, err := convertVmYAML(ctx, []byte(*vmYAMLContent), converter, kvStore)
	if err != nil {
		return nil, err
	}

	name := ""
	if vmApp.Name != nil {
		name = *vmApp.Name
	}
	if name == "" {
		return nil, fmt.Errorf("VmApplication must have a non-empty name")
	}

	var publishPorts []string
	if vmApp.PublishPorts != nil {
		publishPorts = *vmApp.PublishPorts
	}
	if err := injectPublishPorts(quadletFiles, publishPorts); err != nil {
		return nil, err
	}

	inline := make([]domain.ApplicationContent, 0, len(quadletFiles))
	for filename, content := range quadletFiles {
		c := content
		inline = append(inline, domain.ApplicationContent{Path: filename, Content: &c})
	}
	sort.Slice(inline, func(i, j int) bool { return inline[i].Path < inline[j].Path })

	annotations := map[string]string{vmWorkloadTypeAnnotationKey: vmWorkloadTypeAnnotationValue}
	quadlet := domain.QuadletApplication{
		AppType:           domain.AppTypeQuadlet,
		Name:              vmApp.Name,
		Annotations:       &annotations,
		DesiredState:      vmApp.DesiredState,
		RestartGeneration: vmApp.RestartGeneration,
	}
	if err := quadlet.FromInlineApplicationProviderSpec(domain.InlineApplicationProviderSpec{Inline: inline}); err != nil {
		return nil, fmt.Errorf("building QuadletApplication: %w", err)
	}

	var appSpec domain.ApplicationProviderSpec
	if err := appSpec.FromQuadletApplication(quadlet); err != nil {
		return nil, fmt.Errorf("wrapping QuadletApplication: %w", err)
	}

	return &appSpec, nil
}

// injectPublishPorts adds PublishPort= entries to the [Pod] section of the
// generated .pod unit file using the quadlet unit parser. The pod unit is
// located by its .pod suffix rather than by an assumed {vmName}.pod name:
// vm-to-quadlet names the pod unit after KubeVirt's virt-launcher pod (e.g.
// "virt-launcher-{vmName}.pod"), not the raw VmApplication name.
func injectPublishPorts(files map[string]string, publishPorts []string) error {
	if len(publishPorts) == 0 {
		return nil
	}
	podFileName, err := findPodFile(files)
	if err != nil {
		return fmt.Errorf("injectPublishPorts: %w", err)
	}
	u, err := quadlet.NewUnit([]byte(files[podFileName]))
	if err != nil {
		return fmt.Errorf("injectPublishPorts: parsing %q: %w", podFileName, err)
	}
	for _, port := range publishPorts {
		u.Add(quadlet.PodGroup, quadlet.PublishPortKey, port)
	}
	updated, err := u.Write()
	if err != nil {
		return fmt.Errorf("injectPublishPorts: serializing %q: %w", podFileName, err)
	}
	files[podFileName] = string(updated)
	return nil
}

// findPodFile returns the name of the single .pod Quadlet unit file among
// files. vm-to-quadlet always emits exactly one .pod file per VM, but its
// naming scheme is an implementation detail we should not hardcode.
func findPodFile(files map[string]string) (string, error) {
	var podFiles []string
	for name := range files {
		if strings.HasSuffix(name, ".pod") {
			podFiles = append(podFiles, name)
		}
	}
	switch len(podFiles) {
	case 0:
		return "", fmt.Errorf("no .pod file found in vm-to-quadlet output")
	case 1:
		return podFiles[0], nil
	default:
		sort.Strings(podFiles)
		return "", fmt.Errorf("expected exactly one .pod file in vm-to-quadlet output, found %d: %v", len(podFiles), podFiles)
	}
}

// convertVmYAML converts vm.yaml to a set of Quadlet unit files using the KV
// cache and the converter subprocess. On a cache hit the subprocess is skipped
// entirely. The cached value is a JSON-encoded map[string]string.
func convertVmYAML(ctx context.Context, vmYAML []byte, converter VmConverterFn, kvStore kvstore.KVStore) (map[string]string, error) {
	cacheKey := (&kvstore.VmQuadletFilesKey{}).ComposeKey(vmYAML)

	if kvStore != nil {
		cached, err := kvStore.Get(ctx, cacheKey)
		if err != nil {
			return nil, fmt.Errorf("checking vm quadlet files cache: %w", err)
		}
		if len(cached) > 0 {
			var files map[string]string
			if err := json.Unmarshal(cached, &files); err != nil {
				return nil, fmt.Errorf("decoding cached vm quadlet files: %w", err)
			}
			return files, nil
		}
	}

	if converter == nil {
		return nil, fmt.Errorf("vm converter function is required but was not provided")
	}

	files, stderr, err := converter(ctx, vmYAML)
	if err != nil {
		return nil, fmt.Errorf("vm-to-quadlet: %s: %w", truncateStderr(stderr), err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("vm-to-quadlet produced no output files")
	}

	if kvStore != nil {
		encoded, err := json.Marshal(files)
		if err != nil {
			return nil, fmt.Errorf("encoding vm quadlet files for cache: %w", err)
		}
		if _, err := kvStore.SetNX(ctx, cacheKey, encoded); err != nil {
			return nil, fmt.Errorf("storing vm quadlet files in cache: %w", err)
		}
	}

	return files, nil
}
