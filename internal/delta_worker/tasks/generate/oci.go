package generate

import (
	"bufio"
	"context"
	_ "crypto/sha256"
	"encoding/json"
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
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

const layoutTag = "img"

const maxCommandOutputBytes = 10 * 1024 * 1024

type commandOutput struct {
	strings.Builder
	truncated bool
}

func (o *commandOutput) writeLine(line string) {
	if o.Len() >= maxCommandOutputBytes {
		o.truncated = true
		return
	}
	remaining := maxCommandOutputBytes - o.Len()
	if len(line) >= remaining {
		o.WriteString(line[:remaining])
		o.truncated = true
		return
	}
	o.WriteString(line)
	o.WriteByte('\n')
}

func (o *commandOutput) String() string {
	output := o.Builder.String()
	if o.truncated {
		output += "\n[command output truncated]"
	}
	return output
}

type runner interface {
	Run(ctx context.Context, name string, args []string) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string) error {
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
		out commandOutput
		wg  sync.WaitGroup
	)
	scanErr := make(chan error, 2)
	scan := func(r io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 64*1024), 10*1024*1024)
		for s.Scan() {
			line := s.Text()
			mu.Lock()
			out.writeLine(line)
			mu.Unlock()
		}
		if err := s.Err(); err != nil {
			_ = cmd.Process.Kill()
			scanErr <- fmt.Errorf("read command output: %w", err)
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	wg.Wait()
	waitErr := cmd.Wait()
	select {
	case err := <-scanErr:
		return fmt.Errorf("%s: %w", name, err)
	default:
	}
	if waitErr != nil {
		return fmt.Errorf("%s: %w: %s", name, waitErr, out.String())
	}
	return nil
}

type generator struct {
	run                runner
	writeSpec          *domain.OciRepoSpec
	pullImage          func(ctx context.Context, imageRef, layoutDir string) error
	pushLayoutWithSize func(ctx context.Context, layoutDir, destRef, sourceRef, targetRef string) (deltaRef string, sizeBytes int64, err error)
	pushLayout         func(ctx context.Context, layoutDir, destRef, sourceRef, targetRef string) (deltaRef string, err error)
	layoutPayloadSize  func(layoutDir string) (int64, error)
	workDir            string
	log                logrus.FieldLogger
	phaseUpdate        func(domain.DeltaGenerationPhase) error
}

func (g generator) info(format string, args ...any) {
	if g.log == nil {
		return
	}
	g.log.Infof(format, args...)
}

func (g generator) pull(ctx context.Context, imageRef, layoutDir string) error {
	if g.pullImage != nil {
		return g.pullImage(ctx, imageRef, layoutDir)
	}
	return pullImageToLayout(ctx, g.writeSpec, imageRef, layoutDir)
}

func (g generator) push(ctx context.Context, layoutDir, destRef, sourceRef, targetRef string) (string, int64, error) {
	if g.pushLayoutWithSize != nil {
		return g.pushLayoutWithSize(ctx, layoutDir, destRef, sourceRef, targetRef)
	}
	if g.pushLayout != nil {
		deltaRef, err := g.pushLayout(ctx, layoutDir, destRef, sourceRef, targetRef)
		if err != nil {
			return "", 0, err
		}
		sizeBytes, err := g.payloadSize(layoutDir)
		if err != nil {
			return "", 0, err
		}
		return deltaRef, sizeBytes, nil
	}
	return pushOCILayoutWithSize(ctx, g.writeSpec, layoutDir, destRef, sourceRef, targetRef)
}

func (g generator) payloadSize(layoutDir string) (int64, error) {
	if g.layoutPayloadSize != nil {
		return g.layoutPayloadSize(layoutDir)
	}
	return readLayoutPayloadSize(layoutDir)
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

	g.info("pulling source %s tls=%s", sourceRef, tlsSummaryForImage(sourceRef, g.writeSpec))
	if g.phaseUpdate != nil {
		if err := g.phaseUpdate(domain.DeltaGenerationPhasePullSource); err != nil {
			return "", 0, err
		}
	}
	if err := g.pull(ctx, sourceRef, sourceDir); err != nil {
		return "", 0, fmt.Errorf("pull source: %w", err)
	}
	g.info("pulled source %s", sourceRef)
	g.info("pulling target %s tls=%s", targetRef, tlsSummaryForImage(targetRef, g.writeSpec))
	if g.phaseUpdate != nil {
		if err := g.phaseUpdate(domain.DeltaGenerationPhasePullTarget); err != nil {
			return "", 0, err
		}
	}
	if err := g.pull(ctx, targetRef, targetDir); err != nil {
		return "", 0, fmt.Errorf("pull target: %w", err)
	}
	g.info("pulled target %s", targetRef)
	g.info("creating oci-delta")
	if g.phaseUpdate != nil {
		if err := g.phaseUpdate(domain.DeltaGenerationPhaseCreateDelta); err != nil {
			return "", 0, err
		}
	}
	if err := run.Run(ctx, "oci-delta", []string{"create", "--debug", sourceOCI, targetOCI, deltaOCI}); err != nil {
		return "", 0, fmt.Errorf("create delta: %w", err)
	}
	g.info("created oci-delta")

	g.info("pushing delta to %s tls=%s", pushPath, tlsSummary(g.writeSpec))
	if g.phaseUpdate != nil {
		if err := g.phaseUpdate(domain.DeltaGenerationPhasePush); err != nil {
			return "", 0, err
		}
	}
	deltaRef, sizeBytes, err = g.push(ctx, deltaDir, pushPath, sourceRef, targetRef)
	if err != nil {
		return "", 0, fmt.Errorf("push delta: %w", err)
	}
	g.info("pushed delta %s", deltaRef)
	return deltaRef, sizeBytes, nil
}

func pushOCILayout(ctx context.Context, spec *domain.OciRepoSpec, layoutDir, destRef, sourceRef, targetRef string) (string, error) {
	deltaRef, _, err := pushOCILayoutWithSize(ctx, spec, layoutDir, destRef, sourceRef, targetRef)
	return deltaRef, err
}

func pushOCILayoutWithSize(ctx context.Context, spec *domain.OciRepoSpec, layoutDir, destRef, sourceRef, targetRef string) (string, int64, error) {
	if spec == nil {
		return "", 0, fmt.Errorf("OCI write target is required to push")
	}
	if destRef == "" {
		return "", 0, fmt.Errorf("push destination is required")
	}
	layout, err := loadDeltaLayout(ctx, layoutDir)
	if err != nil {
		return "", 0, err
	}
	sourceDigest, err := referenceForResolve(sourceRef)
	if err != nil {
		return "", 0, fmt.Errorf("source image: %w", err)
	}
	targetDigest, err := referenceForResolve(targetRef)
	if err != nil {
		return "", 0, fmt.Errorf("target image: %w", err)
	}
	if err := layout.matchesPair(sourceDigest, targetDigest); err != nil {
		return "", 0, err
	}
	dst, err := exactRepository(ctx, spec, destRef)
	if err != nil {
		return "", 0, fmt.Errorf("configure destination repository: %w", err)
	}
	dst.SkipReferrersGC = true
	if err := copyDeltaGraph(ctx, layout, dst, dst.Blobs()); err != nil {
		return "", 0, fmt.Errorf("copy delta layout: %w", err)
	}
	return destRef + "@" + layout.root.Digest.String(), deltaPayloadSize(layout.manifest), nil
}

func copyDeltaGraph(ctx context.Context, layout *deltaLayout, dst content.Storage, dstBlobs content.Storage) error {
	if err := pushSubjectLayerAsBlob(ctx, layout, dstBlobs); err != nil {
		return fmt.Errorf("push delta subject layer as blob: %w", err)
	}

	root := layout.root
	manifest := layout.manifest
	copyOptions := oras.DefaultCopyGraphOptions
	copyOptions.FindSuccessors = func(ctx context.Context, fetcher content.Fetcher, node ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		successors, err := content.Successors(ctx, fetcher, node)
		if err != nil || !content.Equal(node, root) || manifest.Subject == nil {
			return successors, err
		}
		filtered := successors[:0]
		for _, successor := range successors {
			if !content.Equal(successor, *manifest.Subject) {
				filtered = append(filtered, successor)
			}
		}
		return filtered, nil
	}
	return oras.CopyGraph(ctx, layout.store, dst, root, copyOptions)
}

// pushSubjectLayerAsBlob copies the embedded subject manifest into the
// destination blob store without copying its image graph. Delta artifacts
// reference the target image manifest both as their subject and as a layer.
// The main graph copy omits that descriptor to avoid recopying the target
// image graph, so it must be copied separately as a leaf.
func pushSubjectLayerAsBlob(ctx context.Context, layout *deltaLayout, dst content.Storage) error {
	if layout.manifest.Subject == nil {
		return nil
	}
	for _, layer := range layout.manifest.Layers {
		if !content.Equal(layer, *layout.manifest.Subject) {
			continue
		}
		copyOptions := oras.DefaultCopyGraphOptions
		copyOptions.FindSuccessors = func(context.Context, content.Fetcher, ocispec.Descriptor) ([]ocispec.Descriptor, error) {
			return nil, nil
		}
		if err := oras.CopyGraph(ctx, layout.store, dst, layer, copyOptions); err != nil {
			return fmt.Errorf("copy subject layer %s: %w", layer.Digest, err)
		}
		return nil
	}
	return nil
}

type deltaLayout struct {
	store       *ocistore.Store
	root        ocispec.Descriptor
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
	manifestBytes, err := content.FetchAll(ctx, store, root)
	if err != nil {
		return nil, fmt.Errorf("fetch delta manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("parse delta manifest: %w", err)
	}
	if err := validateDeltaManifest(manifest); err != nil {
		return nil, fmt.Errorf("delta layout: %w", err)
	}
	return &deltaLayout{
		store:       store,
		root:        root,
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

func referenceForResolve(imageRef string) (string, error) {
	dgst, err := oci.DigestFromImageRef(imageRef)
	if err != nil {
		return "", fmt.Errorf("parse subject reference: %w", err)
	}
	if dgst == "" {
		return "", fmt.Errorf("subject image reference %q has no digest", imageRef)
	}
	return dgst, nil
}

func tlsSummaryForImage(imageRef string, spec *domain.OciRepoSpec) string {
	if rewritten, err := oci.RewriteImageRef(imageRef); err == nil {
		imageRef = rewritten
	}
	parsed, err := registry.ParseReference(strings.TrimPrefix(imageRef, "docker://"))
	if err != nil {
		return tlsSummary(oci.SpecForRegistry("", spec))
	}
	return tlsSummary(oci.SpecForRegistry(parsed.Registry, spec))
}

func tlsSummary(spec *domain.OciRepoSpec) string {
	if spec == nil || spec.Registry == "" {
		return "default"
	}
	skip := spec.SkipServerVerification != nil && *spec.SkipServerVerification
	scheme := "https"
	if spec.Scheme != nil && *spec.Scheme != "" {
		scheme = string(*spec.Scheme)
	}
	return fmt.Sprintf("registry=%s scheme=%s skipTLS=%t ca=%t auth=%t", spec.Registry, scheme, skip, spec.CaCrt != nil, spec.OciAuth != nil)
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
	if _, err := oras.Copy(ctx, src, srcRef, dst, layoutTag, oras.DefaultCopyOptions); err != nil {
		return fmt.Errorf("copy image to oci layout: %w", err)
	}
	return nil
}

func readLayoutPayloadSize(layoutDir string) (int64, error) {
	layout, err := loadDeltaLayout(context.Background(), layoutDir)
	if err != nil {
		return 0, err
	}
	return deltaPayloadSize(layout.manifest), nil
}
