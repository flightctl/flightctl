package auxiliary

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

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
)

// UploadImages uploads all image bundles to the registry.
func (s *Services) UploadImages() error {
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
		if err := s.uploadBundle(bundle); err != nil {
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
func (s *Services) uploadBundle(bundlePath string) error {
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
			errCh <- s.copyImageFromBundle(bundlePath, ref)
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
// docker-archive bundle directly to the registry.
func (s *Services) copyImageFromBundle(bundlePath, ref string) error {
	path := ref
	if idx := strings.Index(ref, "/"); idx != -1 {
		path = ref[idx+1:]
	}
	src := fmt.Sprintf("docker-archive:%s:%s", bundlePath, ref)
	dst := fmt.Sprintf("docker://%s/%s", s.Registry.URL, path)
	copyCmd := exec.Command("skopeo", "copy", "--dest-tls-verify=false", src, dst)
	if output, err := copyCmd.CombinedOutput(); err != nil {
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

// ResolveAgentDeviceImageTag returns the exact "base" image tag (e.g.
// "base-cs10-bootc-v1.3.0-main-332-g250be75c") that was actually bundled for a container-backed
// device to pull, by reading it back out of the same agent-images-bundle-*.tar UploadImages just
// pushed from (see uploadBundle/copyImageFromBundle above).
//
// This exists because build.sh tags every image with several local aliases
// (${IMAGE_REPO}:base-${OS_ID}, :base-${TAG}, :base-${OS_ID}-${TAG}, :base), but
// build_and_qcow2.sh's bundle.sh --filter "reference=${IMAGE_REPO}:*-${OS_ID}-*" only bundles (and
// therefore only pushes to the registry) the aliases matching that pattern, i.e. just
// base-${OS_ID}-${TAG} - the bare base-${OS_ID} alias container_pool.go used to assume is never
// actually pushed, and ${TAG} (the git-describe version string) isn't otherwise propagated to the
// test binary's env. Reading it out of the bundle instead of guessing keeps this self-consistent
// with whatever UploadImages actually pushed.
//
// osIDHint, if non-empty, is used to pick the right bundle file when more than one exists on disk
// (e.g. a local dev machine that built both cs9-bootc and cs10-bootc); CI only ever stages the one
// bundle matching the current shard's os_id input, so it's optional there.
func ResolveAgentDeviceImageTag(osIDHint string) (string, error) {
	if strings.ContainsAny(osIDHint, `/\*?[]`) {
		return "", fmt.Errorf("invalid os ID hint %q: must not contain path separators or glob metacharacters", osIDHint)
	}
	projectRoot, err := getProjectRoot()
	if err != nil {
		return "", fmt.Errorf("failed to get project root: %w", err)
	}
	pattern := agentBundlePattern
	if osIDHint != "" {
		pattern = fmt.Sprintf("agent-images-bundle-%s.tar", osIDHint)
	}
	agentArtifactsDir := filepath.Join(projectRoot, "bin", "agent-artifacts")
	matches, err := filepath.Glob(filepath.Join(agentArtifactsDir, pattern))
	if err != nil {
		return "", fmt.Errorf("failed to glob agent image bundles: %w", err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one agent image bundle matching %s/%s, found %v", agentArtifactsDir, pattern, matches)
	}

	refs, err := extractImageRefs(matches[0])
	if err != nil {
		return "", fmt.Errorf("failed to read image refs from bundle %s: %w", matches[0], err)
	}
	for _, ref := range refs {
		if _, tag, ok := strings.Cut(ref, ":"); ok && strings.HasPrefix(tag, "base-") {
			return tag, nil
		}
	}
	return "", fmt.Errorf("no base-tagged image found in bundle %s (refs: %v)", matches[0], refs)
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
