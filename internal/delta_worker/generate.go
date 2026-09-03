package delta_worker

import (
	"bufio"
	"bytes"
	"context"
	_ "crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/oci"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	ocistore "oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

const layoutTag = "img"

type runner interface {
	Run(ctx context.Context, name string, args []string, onLine func(string)) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var (
		mu  sync.Mutex
		out strings.Builder
		wg  sync.WaitGroup
	)
	scan := func(r io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		for s.Scan() {
			line := s.Text()
			mu.Lock()
			out.WriteString(line)
			out.WriteByte('\n')
			mu.Unlock()
			if onLine != nil {
				onLine(line)
			}
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, out.String())
	}
	return nil
}

type generator struct {
	run               runner
	writeSpec         *domain.OciRepoSpec
	pullImage         func(ctx context.Context, imageRef, layoutDir string) error
	pushLayout        func(ctx context.Context, layoutDir, destRef, sourceRef, targetRef string) (deltaRef string, err error)
	layoutPayloadSize func(layoutDir string) (int64, error)
	workDir           string
	log               logrus.FieldLogger
}

func (g generator) info(format string, args ...any) {
	if g.log == nil {
		return
	}
	g.log.Infof(format, args...)
}

func (g generator) createAndPushDelta(ctx context.Context, sourceRef, targetRef, pushPath string) (deltaRef string, sizeBytes int64, err error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	ctx = withCopyLog(ctx, g.log)
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

	pull := g.pullImage
	if pull == nil {
		pull = func(ctx context.Context, imageRef, layoutDir string) error {
			return pullImageToLayout(ctx, g.writeSpec, imageRef, layoutDir)
		}
	}
	g.info("pulling source %s tls=%s", sourceRef, tlsSummaryForImage(sourceRef, g.writeSpec))
	emitGenerationProgress(ctx, GenerationProgress{Phase: domain.DeltaGenerationPhasePullSource})
	if err := pull(withCopyOp(ctx, "pull source"), sourceRef, sourceDir); err != nil {
		return "", 0, fmt.Errorf("pull source: %w", err)
	}
	g.info("pulled source %s", sourceRef)
	g.info("pulling target %s tls=%s", targetRef, tlsSummaryForImage(targetRef, g.writeSpec))
	emitGenerationProgress(ctx, GenerationProgress{Phase: domain.DeltaGenerationPhasePullTarget})
	if err := pull(withCopyOp(ctx, "pull target"), targetRef, targetDir); err != nil {
		return "", 0, fmt.Errorf("pull target: %w", err)
	}
	g.info("pulled target %s", targetRef)
	g.info("creating oci-delta")
	emitGenerationProgress(ctx, GenerationProgress{Phase: domain.DeltaGenerationPhaseCreateDelta})
	onLine := func(line string) {
		p, ok := parseOciDeltaCreateLine(line)
		if !ok {
			return
		}
		emitGenerationProgress(ctx, p)
	}
	if err := run.Run(ctx, "oci-delta", []string{"create", "--debug", sourceOCI, targetOCI, deltaOCI}, onLine); err != nil {
		return "", 0, fmt.Errorf("create delta: %w", err)
	}
	g.info("created oci-delta")

	push := g.pushLayout
	if push == nil {
		push = func(ctx context.Context, layoutDir, destRef, sourceRef, targetRef string) (string, error) {
			return pushOCILayout(ctx, g.writeSpec, layoutDir, destRef, sourceRef, targetRef)
		}
	}
	g.info("pushing delta to %s tls=%s", pushPath, tlsSummary(g.writeSpec))
	emitGenerationProgress(ctx, GenerationProgress{Phase: domain.DeltaGenerationPhasePush})
	deltaRef, err = push(withCopyOp(ctx, "push"), deltaDir, pushPath, sourceRef, targetRef)
	if err != nil {
		return "", 0, fmt.Errorf("push delta: %w", err)
	}
	g.info("pushed delta %s", deltaRef)

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

func pushOCILayout(ctx context.Context, spec *domain.OciRepoSpec, layoutDir, destRef, sourceRef, targetRef string) (string, error) {
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

	layout, err := loadDeltaLayout(ctx, layoutDir)
	if err != nil {
		return "", err
	}
	sourceDigest, err := referenceForResolve(sourceRef)
	if err != nil {
		return "", fmt.Errorf("source image: %w", err)
	}
	targetDigest, err := referenceForResolve(targetRef)
	if err != nil {
		return "", fmt.Errorf("target image: %w", err)
	}
	if err := layout.matchesPair(sourceDigest, targetDigest); err != nil {
		return "", err
	}
	subject, err := resolveExactDigest(ctx, dst, destRef, layout.subject.Digest.String())
	if err != nil {
		return "", err
	}
	desc, err := pushLayoutAsReferrer(ctx, layout, dst, subject)
	if err != nil {
		return "", err
	}
	return destRef + "@" + desc.Digest.String(), nil
}

type deltaLayout struct {
	store       *ocistore.Store
	manifest    ocispec.Manifest
	subject     ocispec.Descriptor
	annotations map[string]string
}

func loadDeltaLayout(ctx context.Context, layoutDir string) (*deltaLayout, error) {
	store, err := ocistore.NewWithContext(ctx, layoutDir)
	if err != nil {
		return nil, fmt.Errorf("open oci layout: %w", err)
	}
	root, err := store.Resolve(ctx, layoutTag)
	if err != nil {
		return nil, fmt.Errorf("resolve oci layout tag %s: %w", layoutTag, err)
	}
	manifest, err := fetchOCIManifest(ctx, store, root)
	if err != nil {
		return nil, err
	}
	if manifest.Subject == nil {
		return nil, fmt.Errorf("delta layout missing subject")
	}
	source := ""
	if manifest.Annotations != nil {
		source = manifest.Annotations[ociDeltaSourceAnnotation]
	}
	if source == "" {
		return nil, fmt.Errorf("delta layout missing %s annotation", ociDeltaSourceAnnotation)
	}
	return &deltaLayout{
		store:       store,
		manifest:    manifest,
		subject:     *manifest.Subject,
		annotations: manifest.Annotations,
	}, nil
}

func (l *deltaLayout) matchesPair(sourceDigest, targetDigest string) error {
	if l.subject.Digest.String() != targetDigest {
		return fmt.Errorf("delta subject %s does not match target %s", l.subject.Digest, targetDigest)
	}
	got := l.annotations[ociDeltaSourceAnnotation]
	if got != sourceDigest {
		return fmt.Errorf("delta source annotation %s does not match source %s", got, sourceDigest)
	}
	return nil
}

func resolveExactDigest(ctx context.Context, repo *remote.Repository, destRef, dgst string) (ocispec.Descriptor, error) {
	desc, err := repo.Resolve(ctx, dgst)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("resolve subject %s on %s: %w", dgst, destRef, err)
	}
	if desc.Digest.String() != dgst {
		return ocispec.Descriptor{}, fmt.Errorf("resolved subject %s, want %s", desc.Digest, dgst)
	}
	return desc, nil
}

func referenceForResolve(imageRef string) (string, error) {
	imageRef, err := oci.RewriteImageRef(imageRef)
	if err != nil {
		return "", err
	}
	parsed, err := registry.ParseReference(strings.TrimPrefix(imageRef, "docker://"))
	if err != nil {
		return "", fmt.Errorf("parse subject reference: %w", err)
	}
	if parsed.Reference == "" {
		return "", fmt.Errorf("subject image reference %q has no tag or digest", imageRef)
	}
	return parsed.Reference, nil
}

func pushLayoutAsReferrer(ctx context.Context, layout *deltaLayout, dst content.Pusher, subject ocispec.Descriptor) (ocispec.Descriptor, error) {
	obs := copyObserverFrom(ctx)
	if err := pushStoredBlob(ctx, layout.store, dst, layout.manifest.Config, obs); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push config: %w", err)
	}
	for i, layer := range layout.manifest.Layers {
		if err := pushStoredBlob(ctx, layout.store, dst, layer, obs); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("push layer %d: %w", i, err)
		}
	}
	if err := ensureSubjectBlob(ctx, dst, subject); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("ensure subject blob: %w", err)
	}
	config := layout.manifest.Config
	layers := layout.manifest.Layers
	desc, err := oras.PackManifest(ctx, dst, oras.PackManifestVersion1_1, ociDeltaArtifactType, oras.PackManifestOptions{
		Subject:             &subject,
		Layers:              layers,
		ConfigDescriptor:    &config,
		ManifestAnnotations: layout.annotations,
	})
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("pack delta referrer: %w", err)
	}
	return desc, nil
}

// ensureSubjectBlob stores the subject manifest bytes in the blob CAS.
func ensureSubjectBlob(ctx context.Context, dst content.Pusher, subject ocispec.Descriptor) error {
	repo, ok := dst.(*remote.Repository)
	if !ok {
		return nil
	}
	blobDesc := ocispec.Descriptor{
		MediaType: "application/octet-stream",
		Digest:    subject.Digest,
		Size:      subject.Size,
	}
	exists, err := repo.Blobs().Exists(ctx, blobDesc)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	rc, err := repo.Fetch(ctx, subject)
	if err != nil {
		return fmt.Errorf("fetch subject %s: %w", subject.Digest, err)
	}
	payload, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("read subject %s: %w", subject.Digest, err)
	}
	blobDesc.Size = int64(len(payload))
	if err := repo.Blobs().Push(ctx, blobDesc, bytes.NewReader(payload)); err != nil {
		if errors.Is(err, errdef.ErrAlreadyExists) {
			return nil
		}
		return err
	}
	return nil
}

func fetchOCIManifest(ctx context.Context, src content.Fetcher, desc ocispec.Descriptor) (ocispec.Manifest, error) {
	rc, err := src.Fetch(ctx, desc)
	if err != nil {
		return ocispec.Manifest{}, fmt.Errorf("fetch layout manifest: %w", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return ocispec.Manifest{}, fmt.Errorf("read layout manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return ocispec.Manifest{}, fmt.Errorf("parse layout manifest: %w", err)
	}
	return manifest, nil
}

func pushStoredBlob(ctx context.Context, src content.Fetcher, dst content.Pusher, desc ocispec.Descriptor, obs *copyObserver) error {
	rc, err := src.Fetch(ctx, desc)
	if err != nil {
		return fmt.Errorf("fetch blob %s: %w", desc.Digest, err)
	}
	defer rc.Close()
	if obs != nil {
		obs.emit(blobProgress(obs.phase, 0, desc.Size), fmt.Sprintf("%s %s %s %s", obs.op, blobLabel(desc), formatBytes(desc.Size), desc.Digest), true)
	}
	if err := dst.Push(ctx, desc, rc); err != nil {
		if errors.Is(err, errdef.ErrAlreadyExists) {
			return nil
		}
		return err
	}
	if obs != nil {
		obs.emit(blobProgress(obs.phase, desc.Size, desc.Size), fmt.Sprintf("%s %s complete %s %s", obs.op, blobLabel(desc), formatBytes(desc.Size), desc.Digest), true)
	}
	return nil
}

func tlsSummaryForImage(imageRef string, spec *domain.OciRepoSpec) string {
	if rewritten, err := oci.RewriteImageRef(imageRef); err == nil {
		imageRef = rewritten
	}
	parsed, err := registry.ParseReference(strings.TrimPrefix(imageRef, "docker://"))
	if err != nil {
		return tlsSummary(specForRegistry("", spec))
	}
	return tlsSummary(specForRegistry(parsed.Registry, spec))
}

func pullImageToLayout(ctx context.Context, spec *domain.OciRepoSpec, imageRef, layoutDir string) error {
	src, srcRef, err := remoteImageRepository(ctx, spec, imageRef)
	if err != nil {
		return err
	}
	return copyImageToLayout(ctx, src, srcRef, layoutDir)
}

func remoteImageRepository(ctx context.Context, spec *domain.OciRepoSpec, imageRef string) (*remote.Repository, string, error) {
	return oci.RemoteRepository(ctx, spec, imageRef)
}

func copyImageToLayout(ctx context.Context, src oras.ReadOnlyGraphTarget, srcRef, layoutDir string) error {
	dst, err := ocistore.NewWithContext(ctx, layoutDir)
	if err != nil {
		return fmt.Errorf("open oci layout: %w", err)
	}
	obs := copyObserverFrom(ctx)
	if _, err := oras.Copy(ctx, wrapFetchProgress(src, obs), srcRef, dst, layoutTag, obs.copyOptions()); err != nil {
		return fmt.Errorf("copy image to oci layout: %w", err)
	}
	return nil
}

func copyOCILayout(ctx context.Context, layoutDir string, dst oras.Target) (ocispec.Descriptor, error) {
	src, err := ocistore.NewWithContext(ctx, layoutDir)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("open oci layout: %w", err)
	}
	obs := copyObserverFrom(ctx)
	desc, err := oras.Copy(ctx, wrapFetchProgress(src, obs), layoutTag, dst, "", obs.copyOptions())
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
