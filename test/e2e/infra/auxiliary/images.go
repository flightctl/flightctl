package auxiliary

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	agentBundlePattern = "agent-images-bundle-*.tar"
	appBundleName      = "app-images-bundle.tar"

	// uploadConcurrency bounds how many images are copied out of a bundle at once.
	// This is I/O-bound work (reading tar offsets, pushing to a local registry), so
	// running several in parallel overlaps their I/O wait without oversubscribing the
	// runner's CPU.
	uploadConcurrency = 4

	// perCopyTimeout bounds a single "skopeo copy" invocation. Without it, a hung
	// copy would pin an uploadConcurrency semaphore slot indefinitely and block aux
	// startup forever; the largest observed single-image copy in CI is well under
	// this budget.
	perCopyTimeout = 5 * time.Minute

	// externalCopyRetries bounds retries for copyExternalImage only: unlike the
	// bundle copies (copyImageFromBundle), it pulls from the real quay.io over the
	// internet, so it's exposed to transient upstream blips (seen in CI: a one-off
	// EOF reading a config blob from quay's CDN). A single such blip previously
	// failed the whole aux-service startup (see MirrorExternalTestImages), taking
	// down every e2e shard sharing it.
	externalCopyRetries   = 3
	externalCopyRetryWait = 5 * time.Second

	// bundleCopyRetries bounds retries for copying images out of the local app/agent
	// bundles into the local registry. CI has seen one-off local-registry 500s while
	// checking or writing blobs; retrying keeps those transient startup blips from
	// failing an entire shard before any spec code runs.
	bundleCopyRetries   = 3
	bundleCopyRetryWait = 5 * time.Second
)

// externalTestImages are quay.io/flightctl-tests fixture images that e2e specs
// reference directly (not built locally, so they never appear in an app/agent
// bundle - see UploadImages). Without mirroring, every fresh VM pulls each of these
// straight from the real quay.io the first time a spec needs it, which is slow and
// adds a hard external dependency to the test run. Mirroring them into the local
// registry once here lets the device-side registry remap
// (quay.io/flightctl-tests -> local registry, see inject_agent_files_into_qcow.sh)
// serve them locally instead. Keep this list in sync with the literal
// "quay.io/flightctl-tests/..." refs used under test/. Deliberately excludes
// quay.io/flightctl-tests/does-not-exist:never, which tests rely on staying absent.
var externalTestImages = []string{
	"quay.io/flightctl-tests/alpine:v1",
	"quay.io/flightctl-tests/nginx:v1",
	"quay.io/flightctl-tests/nginx:1.28-alpine-slim",
	"quay.io/flightctl-tests/nginx-config-artifact:latest",
	"quay.io/flightctl-tests/nginx-html-artifact-image:latest",
	"quay.io/flightctl-tests/quadlet-app-artifact:latest",
	"quay.io/flightctl-tests/quadlet-app-artifact:with-image-ref",
	"quay.io/flightctl-tests/quadlet-test/quadlet-app-artifact:with-image-ref",
	"quay.io/flightctl-tests/model-artifact:latest",
	"quay.io/flightctl-tests/busybox-dummy-artifact:latest",
}

// MirrorExternalTestImages copies each image in externalTestImages from the real
// quay.io straight into the local registry, in parallel across images. Only called
// when the registry container was just created (see StartServices) - a reused
// registry already has these from a previous run.
func (s *Services) MirrorExternalTestImages(ctx context.Context) error {
	logrus.Infof("Mirroring %d external test image(s) into registry %s", len(externalTestImages), s.Registry.URL)

	sem := make(chan struct{}, uploadConcurrency)
	errCh := make(chan error, len(externalTestImages))
	var wg sync.WaitGroup
	for _, ref := range externalTestImages {
		wg.Add(1)
		go func(ref string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errCh <- s.copyExternalImage(ctx, ref)
		}(ref)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}
	logrus.Info("External test image mirroring completed")
	return nil
}

// copyExternalImage copies a single image reference directly from quay.io to the
// registry, retrying on transient failures since it depends on the real, external
// quay.io rather than resources local to the CI run. Bounded by perCopyTimeout per
// attempt so a hung skopeo process can't block the uploadConcurrency semaphore
// indefinitely.
func (s *Services) copyExternalImage(ctx context.Context, ref string) error {
	path := ref
	if idx := strings.Index(ref, "/"); idx != -1 {
		path = ref[idx+1:]
	}
	src := fmt.Sprintf("docker://%s", ref)
	dst := fmt.Sprintf("docker://%s/%s", s.Registry.URL, path)

	var lastErr error
	for attempt := 1; attempt <= externalCopyRetries; attempt++ {
		copyCtx, cancel := context.WithTimeout(ctx, perCopyTimeout)
		copyCmd := exec.CommandContext(copyCtx, "skopeo", "copy", "--dest-tls-verify=false", src, dst)
		output, err := copyCmd.CombinedOutput()
		timedOut := copyCtx.Err() != nil
		cancel()

		if timedOut {
			lastErr = fmt.Errorf("skopeo copy for %s did not complete within %s: %w", ref, perCopyTimeout, copyCtx.Err())
		} else if err != nil {
			lastErr = fmt.Errorf("skopeo copy failed for %s: %w, output: %s", ref, err, string(output))
		} else {
			return nil
		}

		if attempt < externalCopyRetries {
			logrus.Warnf("Retrying external image mirror for %s (attempt %d/%d): %v", ref, attempt, externalCopyRetries, lastErr)
			select {
			case <-ctx.Done():
				return lastErr
			case <-time.After(externalCopyRetryWait):
			}
		}
	}
	return lastErr
}

// UploadImages uploads all image bundles to the registry.
func (s *Services) UploadImages(ctx context.Context) error {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}
	bundles := s.findImageBundles(projectRoot)
	if len(bundles) == 0 {
		logrus.Warnf("No image bundles found (bin/agent-artifacts/%s or bin/%s) - skipping image upload",
			agentBundlePattern, appBundleName)
		return nil
	}
	// Each bundle is a .tar file (agent-images-bundle-*.tar and/or app-images-bundle.tar) containing many images.
	logrus.Infof("Uploading %d bundle file(s) to registry %s (each bundle can contain many images)",
		len(bundles), s.Registry.URL)
	for _, bundle := range bundles {
		logrus.Infof("Uploading bundle: %s", filepath.Base(bundle))
		if err := s.uploadBundle(ctx, bundle); err != nil {
			return fmt.Errorf("failed to upload bundle %s: %w", bundle, err)
		}
	}
	logrus.Info("Image bundle upload completed")
	return nil
}

func (s *Services) findImageBundles(projectRoot string) []string {
	var bundles []string
	agentArtifactsDir := filepath.Join(projectRoot, "bin", "agent-artifacts")
	matches, _ := filepath.Glob(filepath.Join(agentArtifactsDir, agentBundlePattern))
	bundles = append(bundles, matches...)
	appBundle := filepath.Join(projectRoot, "bin", appBundleName)
	if fileExists(appBundle) {
		bundles = append(bundles, appBundle)
	}
	return bundles
}

func (s *Services) uploadBundle(ctx context.Context, bundlePath string) error {
	oci, err := tarContains(bundlePath, "oci/oci-layout")
	if err != nil {
		return err
	}
	if oci {
		return s.uploadOCIBundle(ctx, bundlePath)
	}
	return s.uploadDockerArchiveBundle(ctx, bundlePath)
}

func (s *Services) uploadOCIBundle(ctx context.Context, bundlePath string) error {
	dir, err := os.MkdirTemp("", "e2e-oci-bundle-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := extractTar(bundlePath, dir); err != nil {
		return err
	}
	refs, err := parseE2ERefs(filepath.Join(dir, "e2e-refs.tsv"))
	if err != nil {
		return err
	}
	layout := filepath.Join(dir, "oci")
	return s.copyRefsParallel(ctx, refs, func(ctx context.Context, rec e2eRef) error {
		src := fmt.Sprintf("oci:%s:%s", layout, rec.tag)
		return s.copyPreserveDigest(ctx, src, rec.ref)
	})
}

func (s *Services) uploadDockerArchiveBundle(ctx context.Context, bundlePath string) error {
	names, err := extractImageRefs(bundlePath)
	if err != nil {
		return err
	}
	refs := make([]e2eRef, 0, len(names))
	for _, ref := range names {
		refs = append(refs, e2eRef{ref: ref})
	}
	return s.copyRefsParallel(ctx, refs, func(ctx context.Context, rec e2eRef) error {
		src := fmt.Sprintf("docker-archive:%s:%s", bundlePath, rec.ref)
		return s.skopeoCopy(ctx, rec.ref, src, destDockerRef(s.Registry.URL, rec.ref), false)
	})
}

type e2eRef struct {
	tag string
	ref string
}

func (s *Services) copyRefsParallel(ctx context.Context, refs []e2eRef, copyFn func(context.Context, e2eRef) error) error {
	if len(refs) == 0 {
		return nil
	}
	sem := make(chan struct{}, uploadConcurrency)
	errCh := make(chan error, len(refs))
	var wg sync.WaitGroup
	for _, ref := range refs {
		wg.Add(1)
		go func(ref e2eRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errCh <- copyFn(ctx, ref)
		}(ref)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Services) copyPreserveDigest(ctx context.Context, src, originalRef string) error {
	dst := destDockerRef(s.Registry.URL, originalRef)
	if err := s.skopeoCopy(ctx, originalRef, src, dst, true); err != nil {
		return err
	}
	want, err := skopeoDigest(ctx, src, false)
	if err != nil {
		return fmt.Errorf("inspect source %s: %w", src, err)
	}
	got, err := skopeoDigest(ctx, dst, true)
	if err != nil {
		return fmt.Errorf("inspect dest %s: %w", dst, err)
	}
	if want != got {
		return fmt.Errorf("manifest digest changed for %s: source %s dest %s", originalRef, want, got)
	}
	return nil
}

func destDockerRef(registryURL, ref string) string {
	path := ref
	if idx := strings.Index(ref, "/"); idx != -1 {
		path = ref[idx+1:]
	}
	return fmt.Sprintf("docker://%s/%s", registryURL, path)
}

func (s *Services) skopeoCopy(ctx context.Context, ref, src, dst string, preserveDigests bool) error {
	var lastErr error
	for attempt := 1; attempt <= bundleCopyRetries; attempt++ {
		copyCtx, cancel := context.WithTimeout(ctx, perCopyTimeout)
		args := []string{"copy", "--dest-tls-verify=false"}
		if preserveDigests {
			args = append(args, "--preserve-digests")
		}
		args = append(args, src, dst)
		copyCmd := exec.CommandContext(copyCtx, "skopeo", args...)
		output, err := copyCmd.CombinedOutput()
		timedOut := copyCtx.Err() != nil
		cancel()

		if timedOut {
			lastErr = fmt.Errorf("skopeo copy for %s did not complete within %s: %w", ref, perCopyTimeout, copyCtx.Err())
		} else if err != nil {
			lastErr = fmt.Errorf("skopeo copy failed for %s: %w, output: %s", ref, err, string(output))
		} else {
			return nil
		}

		if attempt < bundleCopyRetries {
			logrus.Warnf("Retrying bundle image upload for %s (attempt %d/%d): %v", ref, attempt, bundleCopyRetries, lastErr)
			select {
			case <-ctx.Done():
				return lastErr
			case <-time.After(bundleCopyRetryWait):
			}
		}
	}
	return lastErr
}

func skopeoDigest(ctx context.Context, image string, insecureTLS bool) (string, error) {
	args := []string{"inspect", "--format", "{{.Digest}}"}
	if insecureTLS {
		args = append(args, "--tls-verify=false")
	}
	args = append(args, image)
	cmd := exec.CommandContext(ctx, "skopeo", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func extractImageRefs(bundlePath string) ([]string, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var reader io.Reader = f
	if strings.HasSuffix(bundlePath, ".tar.gz") || strings.HasSuffix(bundlePath, ".tgz") {
		gz, err := gzip.NewReader(f)
		if err == nil {
			reader = gz
			defer gz.Close()
		}
	}
	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Name == "manifest.json" {
			return parseManifestJSON(tr)
		}
	}
	return nil, fmt.Errorf("manifest.json not found in bundle")
}

type manifestEntry struct {
	RepoTags []string `json:"RepoTags"`
}

func parseManifestJSON(r io.Reader) ([]string, error) {
	content, err := io.ReadAll(bufio.NewReader(r))
	if err != nil {
		return nil, err
	}
	var entries []manifestEntry
	if err := json.Unmarshal(content, &entries); err != nil {
		return nil, err
	}
	var refs []string
	for _, entry := range entries {
		refs = append(refs, entry.RepoTags...)
	}
	return refs, nil
}

func tarContains(bundlePath, name string) (bool, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return false, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if hdr.Name == name {
			return true, nil
		}
	}
}

func parseE2ERefs(path string) ([]e2eRef, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []e2eRef
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		tag, ref, ok := strings.Cut(line, "\t")
		if !ok || tag == "" || ref == "" {
			return nil, fmt.Errorf("invalid e2e-refs line %q", line)
		}
		out = append(out, e2eRef{tag: tag, ref: ref})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("e2e-refs.tsv has no entries")
	}
	return out, nil
}

func extractTar(bundlePath, dest string) error {
	f, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := writeTarHeader(dest, hdr, tr); err != nil {
			return err
		}
	}
}

func writeTarHeader(dest string, hdr *tar.Header, r io.Reader) error {
	name := filepath.Clean(hdr.Name)
	if strings.HasPrefix(name, "..") {
		return fmt.Errorf("invalid tar path %q", hdr.Name)
	}
	target := filepath.Join(dest, name)
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, r)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	default:
		return nil
	}
}
