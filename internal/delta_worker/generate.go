package delta_worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/oci"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	ocistore "oras.land/oras-go/v2/content/oci"
)

const layoutTag = "img"

type runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, out)
	}
	return nil
}

type generator struct {
	run               runner
	writeSpec         *domain.OciRepoSpec
	pushLayout        func(ctx context.Context, layoutDir, destRef string) (deltaRef string, err error)
	layoutPayloadSize func(layoutDir string) (int64, error)
	workDir           string
}

func (g generator) createAndPushDelta(ctx context.Context, sourceRef, targetRef, pushPath string) (deltaRef string, sizeBytes int64, err error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	run := g.run
	if run == nil {
		run = execRunner{}
	}
	workDir := g.workDir
	if workDir == "" {
		dir, err := os.MkdirTemp("", "delta-gen-")
		if err != nil {
			return "", 0, fmt.Errorf("create work dir: %w", err)
		}
		defer os.RemoveAll(dir)
		workDir = dir
	}

	sourceDir := filepath.Join(workDir, "source")
	targetDir := filepath.Join(workDir, "target")
	deltaDir := filepath.Join(workDir, "delta")
	sourceOCI := "oci:" + sourceDir + ":" + layoutTag
	targetOCI := "oci:" + targetDir + ":" + layoutTag
	deltaOCI := "oci:" + deltaDir + ":" + layoutTag

	if err := run.Run(ctx, "skopeo", "copy", "docker://"+sourceRef, sourceOCI); err != nil {
		return "", 0, fmt.Errorf("pull source: %w", err)
	}
	if err := run.Run(ctx, "skopeo", "copy", "docker://"+targetRef, targetOCI); err != nil {
		return "", 0, fmt.Errorf("pull target: %w", err)
	}
	if err := run.Run(ctx, "oci-delta", "create", sourceOCI, targetOCI, deltaOCI); err != nil {
		return "", 0, fmt.Errorf("create delta: %w", err)
	}

	push := g.pushLayout
	if push == nil {
		push = func(ctx context.Context, layoutDir, destRef string) (string, error) {
			return pushOCILayout(ctx, g.writeSpec, layoutDir, destRef)
		}
	}
	deltaRef, err = push(ctx, deltaDir, pushPath)
	if err != nil {
		return "", 0, fmt.Errorf("push delta: %w", err)
	}

	sizeFn := g.layoutPayloadSize
	if sizeFn == nil {
		sizeFn = readLayoutPayloadSize
	}
	sizeBytes, err = sizeFn(deltaDir)
	if err != nil {
		return "", 0, err
	}
	return deltaRef, sizeBytes, nil
}

func pushOCILayout(ctx context.Context, spec *domain.OciRepoSpec, layoutDir, destRef string) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("OCI write target is required to push")
	}
	if destRef == "" {
		return "", fmt.Errorf("push destination is required")
	}
	dst, err := oci.BuildOciRepoRef(ctx, spec, destRef)
	if err != nil {
		return "", fmt.Errorf("configure destination repository: %w", err)
	}
	dst.SkipReferrersGC = true
	desc, err := copyOCILayout(ctx, layoutDir, dst)
	if err != nil {
		return "", err
	}
	return destRef + "@" + desc.Digest.String(), nil
}

func copyOCILayout(ctx context.Context, layoutDir string, dst oras.Target) (ocispec.Descriptor, error) {
	src, err := ocistore.NewWithContext(ctx, layoutDir)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("open oci layout: %w", err)
	}
	desc, err := oras.Copy(ctx, src, layoutTag, dst, "", oras.DefaultCopyOptions)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("copy oci layout: %w", err)
	}
	return desc, nil
}

func readLayoutPayloadSize(layoutDir string) (int64, error) {
	indexBytes, err := os.ReadFile(filepath.Join(layoutDir, "index.json"))
	if err != nil {
		return 0, fmt.Errorf("read oci layout index: %w", err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return 0, fmt.Errorf("parse oci layout index: %w", err)
	}
	if len(index.Manifests) == 0 {
		return 0, fmt.Errorf("oci layout has no manifests")
	}
	d := index.Manifests[0].Digest
	manifestBytes, err := os.ReadFile(filepath.Join(layoutDir, "blobs", d.Algorithm().String(), d.Encoded()))
	if err != nil {
		return 0, fmt.Errorf("read oci layout manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return 0, fmt.Errorf("parse oci layout manifest: %w", err)
	}
	size := manifest.Config.Size
	for _, layer := range manifest.Layers {
		size += layer.Size
	}
	return size, nil
}
