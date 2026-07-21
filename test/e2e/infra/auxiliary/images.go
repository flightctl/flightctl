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
	// TEMPORARY DIAGNOSTIC marker, see registry_diagnostics.go: brackets this phase in
	// registry-health.log so periodic probe results can be correlated against it.
	logDiagnostic("MirrorExternalTestImages starting (%d images, concurrency=%d)", len(externalTestImages), uploadConcurrency)

	sem := make(chan struct{}, uploadConcurrency)
	errCh := make(chan error, len(externalTestImages))
	var wg sync.WaitGroup
	for _, ref := range externalTestImages {
		wg.Add(1)
		go func(ref string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			err := s.copyExternalImage(ctx, ref)
			if err != nil {
				logDiagnostic("MirrorExternalTestImages: %s FAILED: %v", ref, err)
			} else {
				logDiagnostic("MirrorExternalTestImages: %s done", ref)
			}
			errCh <- err
		}(ref)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			logDiagnostic("MirrorExternalTestImages failed: %v", err)
			return err
		}
	}
	logrus.Info("External test image mirroring completed")
	logDiagnostic("MirrorExternalTestImages completed successfully")
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
	logDiagnostic("UploadImages starting (%d bundles)", len(bundles))
	for _, bundle := range bundles {
		logrus.Infof("Uploading bundle: %s", filepath.Base(bundle))
		if err := s.uploadBundle(ctx, bundle); err != nil {
			logDiagnostic("UploadImages failed on bundle %s: %v", filepath.Base(bundle), err)
			return fmt.Errorf("failed to upload bundle %s: %w", bundle, err)
		}
	}
	logrus.Info("Image bundle upload completed")
	logDiagnostic("UploadImages completed")
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

// uploadBundle copies every image in the bundle straight from the archive to the
// registry via "skopeo copy docker-archive:...", in parallel across images. This is
// only called when the registry container was just created (see StartServices), so
// the registry is always empty here - there's no digest check to skip redundant
// pushes because every image needs pushing.
//
// This intentionally skips "podman load": loading a bundle of bootc images means
// extracting a full OS root filesystem (many small files) into local containers
// storage just to immediately read it back out for push. skopeo streams the already
// packaged layer blobs directly from the tar to the registry without ever touching
// local storage.
func (s *Services) uploadBundle(ctx context.Context, bundlePath string) error {
	refs, err := extractImageRefs(bundlePath)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}

	sem := make(chan struct{}, uploadConcurrency)
	errCh := make(chan error, len(refs))
	var wg sync.WaitGroup
	for _, ref := range refs {
		wg.Add(1)
		go func(ref string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errCh <- s.copyImageFromBundle(ctx, bundlePath, ref)
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

// copyImageFromBundle copies a single image reference out of a multi-image
// docker-archive bundle directly to the registry. Bounded by perCopyTimeout so a
// hung skopeo process can't block the uploadConcurrency semaphore indefinitely.
func (s *Services) copyImageFromBundle(ctx context.Context, bundlePath, ref string) error {
	path := ref
	if idx := strings.Index(ref, "/"); idx != -1 {
		path = ref[idx+1:]
	}
	src := fmt.Sprintf("docker-archive:%s:%s", bundlePath, ref)
	dst := fmt.Sprintf("docker://%s/%s", s.Registry.URL, path)

	copyCtx, cancel := context.WithTimeout(ctx, perCopyTimeout)
	defer cancel()
	copyCmd := exec.CommandContext(copyCtx, "skopeo", "copy", "--dest-tls-verify=false", src, dst)
	output, err := copyCmd.CombinedOutput()
	if copyCtx.Err() != nil {
		return fmt.Errorf("skopeo copy for %s did not complete within %s: %w", ref, perCopyTimeout, copyCtx.Err())
	}
	if err != nil {
		return fmt.Errorf("skopeo copy failed for %s: %w, output: %s", ref, err, string(output))
	}
	return nil
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
