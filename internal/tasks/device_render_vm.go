package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
)

const (
	kubevirtVmToPodBinary         = "kubevirt-vm-to-pod"
	vmWorkloadTypeAnnotationKey   = "flightctl.io/workload-type"
	vmWorkloadTypeAnnotationValue = "vm"
	vmYamlFileName                = "vm.yaml"
	defaultKubeUnit               = "[Kube]\nYaml=pod.yaml\n"
	maxStderrLen                  = 512
)

// truncateStderr trims and caps subprocess stderr to avoid embedding large or
// sensitive tool output directly in error messages.
func truncateStderr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxStderrLen {
		return s[:maxStderrLen] + "... (truncated)"
	}
	return s
}

// VmConverterFn accepts KubeVirt VM YAML on stdin and returns pod YAML and
// stderr output. Passed explicitly to renderVmApplication — no package-level
// variable is used so tests can inject stubs without sharing state.
type VmConverterFn func(ctx context.Context, vmYAML []byte) (podYAML []byte, stderr string, err error)

// NewVmConverter returns a VmConverterFn that invokes the kubevirt-vm-to-pod
// binary at binaryPath. binaryPath may be an absolute path or a bare binary
// name resolved via PATH. Exported so integration tests can point the converter
// at a binary extracted to a temporary directory.
func NewVmConverter(binaryPath string) VmConverterFn {
	return func(ctx context.Context, vmYAML []byte) ([]byte, string, error) {
		cmd := exec.CommandContext(ctx, binaryPath)
		cmd.Stdin = bytes.NewReader(vmYAML)

		var stdoutBuf, stderrBuf bytes.Buffer
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf

		if err := cmd.Run(); err != nil {
			return nil, stderrBuf.String(), err
		}
		return stdoutBuf.Bytes(), stderrBuf.String(), nil
	}
}

// defaultVmConverter is the production converter backed by the
// kubevirt-vm-to-pod binary that must be present in PATH at runtime.
var defaultVmConverter = NewVmConverter(kubevirtVmToPodBinary)

// buildKubeUnit returns the Quadlet .kube unit file content.
//
//   - If kubeContent is nil, the minimal default unit ("[Kube]\nYaml=pod.yaml\n") is used.
//   - If kubeContent is provided but omits a Yaml= directive, "Yaml=pod.yaml" is injected
//     immediately after the [Kube] section header so that Podman/Quadlet always has a
//     valid pod manifest reference.
//   - If kubeContent already contains a Yaml= directive it is returned verbatim.
func buildKubeUnit(kubeContent *string) string {
	if kubeContent == nil {
		return defaultKubeUnit
	}
	content := *kubeContent
	if !strings.Contains(content, "Yaml=") {
		content = strings.Replace(content, "[Kube]", "[Kube]\nYaml=pod.yaml", 1)
	}
	return content
}

// renderVmApplication converts a VmApplication (inline: provider) into a
// QuadletApplication by:
//  1. Extracting vm.yaml from the VmApplication inline set.
//  2. Checking the KV-store cache (SHA-256 of vm.yaml content); returning
//     the cached pod.yaml directly on a hit.
//  3. On a cache miss: invoking the kubevirt-vm-to-pod subprocess via the
//     provided converter, then storing the result in the cache.
//  4. Extracting the .kube file from the inline set (if present) or
//     generating a minimal default unit.
//  5. Emitting a QuadletApplication with the flightctl.io/workload-type: vm
//     annotation so the agent can identify the workload without inspecting
//     image names.
func renderVmApplication(ctx context.Context, vmApp domain.VmApplication, converter VmConverterFn, kvStore kvstore.KVStore) (*domain.ApplicationProviderSpec, error) {
	providerType, err := vmApp.Type()
	if err != nil {
		return nil, fmt.Errorf("invalid vm application provider type: %w", err)
	}
	if providerType != domain.InlineApplicationProviderType {
		return nil, fmt.Errorf("VmApplication with %q provider is not yet supported for server-side rendering; use inline: with a vm.yaml file", providerType)
	}

	inlineSpec, err := vmApp.AsInlineApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("extracting inline spec from vm application: %w", err)
	}

	// Locate vm.yaml and optional .kube file in the inline set.
	var vmYAMLContent *string
	var kubeContent *string
	for i := range inlineSpec.Inline {
		f := &inlineSpec.Inline[i]
		switch {
		case f.Path == vmYamlFileName:
			vmYAMLContent = f.Content
		case strings.HasSuffix(f.Path, ".kube"):
			if kubeContent != nil {
				return nil, fmt.Errorf("VmApplication inline set contains multiple .kube files; only one is allowed")
			}
			kubeContent = f.Content
		}
	}
	if vmYAMLContent == nil {
		return nil, fmt.Errorf("vm.yaml not found in VmApplication inline set")
	}
	if strings.TrimSpace(*vmYAMLContent) == "" {
		return nil, fmt.Errorf("vm.yaml content is empty in VmApplication inline set")
	}

	podYAMLBytes, err := convertVmYAML(ctx, []byte(*vmYAMLContent), converter, kvStore)
	if err != nil {
		return nil, err
	}

	podYAMLStr := string(podYAMLBytes)
	kubeUnit := buildKubeUnit(kubeContent)

	name := ""
	if vmApp.Name != nil {
		name = *vmApp.Name
	}
	if name == "" {
		return nil, fmt.Errorf("VmApplication must have a non-empty name")
	}

	outInlineSpec := domain.InlineApplicationProviderSpec{
		Inline: []domain.ApplicationContent{
			{Path: "pod.yaml", Content: &podYAMLStr},
			{Path: name + ".kube", Content: &kubeUnit},
		},
	}

	annotations := map[string]string{vmWorkloadTypeAnnotationKey: vmWorkloadTypeAnnotationValue}
	quadlet := domain.QuadletApplication{
		AppType:     domain.AppTypeQuadlet,
		Name:        vmApp.Name,
		Annotations: &annotations,
	}
	if err := quadlet.FromInlineApplicationProviderSpec(outInlineSpec); err != nil {
		return nil, fmt.Errorf("building QuadletApplication: %w", err)
	}

	var appSpec domain.ApplicationProviderSpec
	if err := appSpec.FromQuadletApplication(quadlet); err != nil {
		return nil, fmt.Errorf("wrapping QuadletApplication: %w", err)
	}

	return &appSpec, nil
}

// convertVmYAML converts vm.yaml to pod.yaml using the KV cache and the
// converter subprocess. On a cache hit the subprocess is skipped entirely.
func convertVmYAML(ctx context.Context, vmYAML []byte, converter VmConverterFn, kvStore kvstore.KVStore) ([]byte, error) {
	sum := sha256.Sum256(vmYAML)
	sha256hex := fmt.Sprintf("%x", sum)
	cacheKey := (&kvstore.VmPodYamlKey{Sha256: sha256hex}).ComposeKey()

	if kvStore != nil {
		cached, err := kvStore.Get(ctx, cacheKey)
		if err != nil {
			return nil, fmt.Errorf("checking vm pod YAML cache: %w", err)
		}
		if len(cached) > 0 {
			return cached, nil
		}
	}

	if converter == nil {
		return nil, fmt.Errorf("vm converter function is required but was not provided")
	}

	podYAML, stderr, err := converter(ctx, vmYAML)
	if err != nil {
		return nil, fmt.Errorf("kubevirt-vm-to-pod: %s: %w", truncateStderr(stderr), err)
	}
	if len(podYAML) == 0 {
		return nil, fmt.Errorf("kubevirt-vm-to-pod produced empty output")
	}

	if kvStore != nil {
		if _, err := kvStore.SetNX(ctx, cacheKey, podYAML); err != nil {
			return nil, fmt.Errorf("storing vm pod YAML in cache: %w", err)
		}
	}

	return podYAML, nil
}
