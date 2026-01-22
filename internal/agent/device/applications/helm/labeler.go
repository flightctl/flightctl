package helm

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"gopkg.in/yaml.v3"
)

const (
	// AppLabelKey is the label key used to identify which application
	// owns a Kubernetes resource. This enables querying all resources
	// belonging to a specific application.
	AppLabelKey = "agent.flightctl.io/app"

	manifestsFileName     = "manifests.yaml"
	kustomizationFileName = "kustomization.yaml"
)

// Labeler injects labels into Kubernetes manifests using kubectl kustomize.
// It leverages kustomize's built-in label transformer which automatically
// handles all Kubernetes resource types and their pod templates.
type Labeler struct {
	kube       *client.Kube
	readWriter fileio.ReadWriter
}

// NewLabeler creates a new Labeler that uses kubectl kustomize to inject labels.
func NewLabeler(kube *client.Kube, readWriter fileio.ReadWriter) *Labeler {
	return &Labeler{
		kube:       kube,
		readWriter: readWriter,
	}
}

// InjectLabels reads multi-document YAML from input, injects the provided labels
// into each Kubernetes resource using kustomize, and writes the modified YAML to output.
//
// This function is designed to be used as a Helm post-renderer. Helm pipes
// rendered templates through the post-renderer before applying them to the cluster.
//
// Labels are injected using kustomize's label transformer with:
//   - includeSelectors: false (avoids modifying immutable selectors)
//   - includeTemplates: true (ensures pods inherit labels from their controllers)
//
// This approach automatically handles all Kubernetes resource types including
// Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, and any future types.
func (l *Labeler) InjectLabels(ctx context.Context, input io.Reader, output io.Writer, labels map[string]string) error {
	if !l.kube.IsAvailable() {
		return fmt.Errorf("kubectl or oc not available")
	}

	tempDir, err := l.readWriter.MkdirTemp("kustomize-labels-*")
	if err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}
	defer func() { _ = l.readWriter.RemoveAll(tempDir) }()

	manifestsPath := filepath.Join(tempDir, manifestsFileName)
	kustomizationPath := filepath.Join(tempDir, kustomizationFileName)

	manifests, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("read input manifests: %w", err)
	}

	if err := l.readWriter.WriteFile(manifestsPath, manifests, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("write manifests file: %w", err)
	}

	kustomization := buildKustomization(labels)
	kustomizationBytes, err := yaml.Marshal(kustomization)
	if err != nil {
		return fmt.Errorf("marshal kustomization: %w", err)
	}

	if err := l.readWriter.WriteFile(kustomizationPath, kustomizationBytes, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("write kustomization file: %w", err)
	}

	stdout, stderr, exitCode := l.kube.Kustomize(ctx, tempDir)
	if exitCode != 0 {
		return fmt.Errorf("kubectl kustomize failed (exit %d): %s", exitCode, stderr)
	}

	if _, err := output.Write([]byte(stdout)); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

// kustomization represents the structure of a kustomization.yaml file.
type kustomization struct {
	APIVersion string        `yaml:"apiVersion"`
	Kind       string        `yaml:"kind"`
	Resources  []string      `yaml:"resources"`
	Labels     []labelConfig `yaml:"labels"`
}

// labelConfig represents a label configuration in kustomization.yaml.
type labelConfig struct {
	Pairs            map[string]string `yaml:"pairs"`
	IncludeSelectors bool              `yaml:"includeSelectors"`
	IncludeTemplates bool              `yaml:"includeTemplates"`
}

func buildKustomization(labels map[string]string) kustomization {
	return kustomization{
		APIVersion: "kustomize.config.k8s.io/v1beta1",
		Kind:       "Kustomization",
		Resources:  []string{manifestsFileName},
		Labels: []labelConfig{
			{
				Pairs:            labels,
				IncludeSelectors: false,
				IncludeTemplates: true,
			},
		},
	}
}
