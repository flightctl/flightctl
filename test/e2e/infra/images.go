package infra

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	agentBundlePattern = "agent-images-bundle-*.tar"
	appBundleName      = "app-images-bundle.tar"
)

// UploadImages uploads all image bundles to the registry.
// This is called automatically after the registry starts.
func (s *SatelliteServices) UploadImages() error {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}

	// Find image bundles
	bundles := s.findImageBundles(projectRoot)
	if len(bundles) == 0 {
		logrus.Warn("No image bundles found - skipping image upload")
		return nil
	}

	logrus.Infof("Found %d image bundle(s) to upload", len(bundles))

	// Upload each bundle
	for _, bundle := range bundles {
		if err := s.uploadBundle(bundle); err != nil {
			return fmt.Errorf("failed to upload bundle %s: %w", bundle, err)
		}
	}

	return nil
}

// findImageBundles locates all image bundle tar files in the project.
func (s *SatelliteServices) findImageBundles(projectRoot string) []string {
	var bundles []string

	// Look for agent bundle in bin/agent-artifacts/
	agentArtifactsDir := filepath.Join(projectRoot, "bin", "agent-artifacts")
	matches, _ := filepath.Glob(filepath.Join(agentArtifactsDir, agentBundlePattern))
	bundles = append(bundles, matches...)

	// Look for app bundle in bin/
	appBundle := filepath.Join(projectRoot, "bin", appBundleName)
	if fileExists(appBundle) {
		bundles = append(bundles, appBundle)
	}

	return bundles
}

// getImageDigest returns the digest of an image using skopeo inspect.
// Returns empty string if image doesn't exist or inspection fails.
func getImageDigest(imageRef string, tlsVerify bool) string {
	tlsArg := fmt.Sprintf("--tls-verify=%v", tlsVerify)
	cmd := exec.Command("skopeo", "inspect", tlsArg, "--format", "{{.Digest}}", imageRef)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// imageNeedsPush checks if an image needs to be pushed by comparing digests.
// Returns true if image doesn't exist in registry or has different digest.
func (s *SatelliteServices) imageNeedsPush(localRef, registryRef string) bool {
	// Get digest from local podman storage
	localDigest := getImageDigest("containers-storage:"+localRef, false)
	if localDigest == "" {
		// Can't get local digest, assume needs push
		logrus.Debugf("Could not get local digest for %s", localRef)
		return true
	}

	// Get digest from registry
	registryDigest := getImageDigest("docker://"+registryRef, false)
	if registryDigest == "" {
		// Image doesn't exist in registry
		logrus.Debugf("Image %s not found in registry", registryRef)
		return true
	}

	// Compare digests
	if localDigest == registryDigest {
		logrus.Debugf("Digests match for %s: %s", localRef, localDigest)
		return false
	}

	logrus.Debugf("Digest mismatch for %s: local=%s registry=%s", localRef, localDigest, registryDigest)
	return true
}

// uploadBundle uploads all images from a tar bundle to the registry.
// Uses podman load + push for better memory efficiency than direct skopeo from tar.
// Compares digests to skip images that already exist with same content.
func (s *SatelliteServices) uploadBundle(bundlePath string) error {
	logrus.Infof("Uploading images from bundle: %s", filepath.Base(bundlePath))

	// Extract image references from manifest.json
	refs, err := extractImageRefs(bundlePath)
	if err != nil {
		return fmt.Errorf("failed to extract image refs: %w", err)
	}

	if len(refs) == 0 {
		logrus.Warnf("No images found in bundle %s", bundlePath)
		return nil
	}

	logrus.Infof("Found %d image(s) in bundle", len(refs))

	// Step 1: Load bundle into podman storage first (needed for digest comparison)
	logrus.Infof("Loading bundle into podman storage...")
	loadCmd := exec.Command("podman", "load", "-i", bundlePath)
	if output, err := loadCmd.CombinedOutput(); err != nil {
		logrus.Warnf("podman load failed (falling back to skopeo): %v, output: %s", err, string(output))
		return s.uploadBundleViaSkopeo(bundlePath, refs)
	}
	logrus.Info("Bundle loaded into podman storage")

	// Step 2: Check which images need to be pushed (by comparing digests)
	var needsPush []string
	for _, ref := range refs {
		path := ref
		if idx := strings.Index(ref, "/"); idx != -1 {
			path = ref[idx+1:]
		}
		registryRef := fmt.Sprintf("%s/%s", s.RegistryURL, path)

		if s.imageNeedsPush(ref, registryRef) {
			needsPush = append(needsPush, ref)
		} else {
			logrus.Infof("[skip %s] Same digest already in registry", ref)
		}
	}

	if len(needsPush) == 0 {
		logrus.Info("All images already exist in registry with matching digests, skipping upload")
		return nil
	}

	logrus.Infof("%d image(s) need to be pushed", len(needsPush))

	// Step 3: Push images that need updating
	for _, ref := range needsPush {
		path := ref
		if idx := strings.Index(ref, "/"); idx != -1 {
			path = ref[idx+1:]
		}

		dst := fmt.Sprintf("%s/%s", s.RegistryURL, path)
		logrus.Infof("[push %s] -> %s", ref, dst)

		pushCmd := exec.Command("podman", "push", "--tls-verify=false", ref, dst)
		if output, err := pushCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("podman push failed for %s: %w, output: %s", ref, err, string(output))
		}
		logrus.Infof("[push %s] Success", ref)
	}

	logrus.Infof("Successfully pushed %d image(s) to registry", len(needsPush))
	return nil
}

// uploadBundleViaSkopeo is the fallback method using skopeo directly from tar.
// Compares image config digests to skip images that already exist with same content.
// Config digests are stable across storage formats (docker-archive vs registry)
// unlike manifest digests which differ due to compression.
func (s *SatelliteServices) uploadBundleViaSkopeo(bundlePath string, refs []string) error {
	logrus.Info("Using skopeo fallback for bundle upload")

	// Filter refs that need pushing (compare config digests)
	var needsPush []string
	for _, ref := range refs {
		path := ref
		if idx := strings.Index(ref, "/"); idx != -1 {
			path = ref[idx+1:]
		}
		registryRef := fmt.Sprintf("%s/%s", s.RegistryURL, path)

		// Check if image config matches (stable comparison across formats)
		if imageConfigMatches(bundlePath, ref, registryRef) {
			logrus.Infof("[skip %s] Same config already in registry", ref)
			continue
		}
		needsPush = append(needsPush, ref)
	}

	if len(needsPush) == 0 {
		logrus.Info("All images already exist in registry with matching configs, skipping upload")
		return nil
	}

	logrus.Infof("%d image(s) need to be pushed", len(needsPush))

	// Push each image
	for _, ref := range needsPush {
		path := ref
		if idx := strings.Index(ref, "/"); idx != -1 {
			path = ref[idx+1:]
		}

		src := fmt.Sprintf("docker-archive:%s:%s", bundlePath, ref)
		dst := fmt.Sprintf("docker://%s/%s", s.RegistryURL, path)

		logrus.Infof("[push %s] -> %s", ref, dst)

		cmd := exec.Command("skopeo", "copy", "--dest-tls-verify=false", src, dst)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("skopeo copy failed for %s: %w, output: %s", ref, err, string(output))
		}
		logrus.Infof("[push %s] Success", ref)
	}

	logrus.Infof("Successfully pushed %d image(s) to registry", len(needsPush))
	return nil
}

// getImageConfigDigest returns the SHA256 hash of the image config JSON.
// The config digest is stable across different storage formats (docker-archive vs registry)
// because it represents the actual image configuration, not the compressed manifest.
// Returns empty string if image doesn't exist or inspection fails.
func getImageConfigDigest(imageRef string, tlsVerify bool) string {
	tlsArg := fmt.Sprintf("--tls-verify=%v", tlsVerify)
	cmd := exec.Command("skopeo", "inspect", "--config", tlsArg, imageRef)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	// Hash the config JSON to get a stable identifier
	hash := sha256.Sum256(output)
	return fmt.Sprintf("sha256:%x", hash)
}

// imageConfigMatches checks if an image in docker-archive has the same config as one in registry.
// This is reliable because the config digest is format-independent (unlike manifest digests
// which differ due to compression differences between docker-archive and registry).
func imageConfigMatches(bundlePath, imageRef, registryRef string) bool {
	archiveRef := fmt.Sprintf("docker-archive:%s:%s", bundlePath, imageRef)
	archiveConfigDigest := getImageConfigDigest(archiveRef, false)
	if archiveConfigDigest == "" {
		logrus.Debugf("Could not get config digest from archive for %s", imageRef)
		return false
	}

	registryConfigDigest := getImageConfigDigest("docker://"+registryRef, false)
	if registryConfigDigest == "" {
		logrus.Debugf("Image %s not found in registry", registryRef)
		return false
	}

	if archiveConfigDigest == registryConfigDigest {
		logrus.Debugf("Config digests match for %s: %s", imageRef, archiveConfigDigest)
		return true
	}

	logrus.Debugf("Config digest mismatch for %s: archive=%s registry=%s", imageRef, archiveConfigDigest, registryConfigDigest)
	return false
}

// extractImageRefs reads the manifest.json from a docker-archive tar and returns image references.
func extractImageRefs(bundlePath string) ([]string, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Try to read as gzipped tar first, fall back to plain tar
	var reader io.Reader = f
	if strings.HasSuffix(bundlePath, ".tar.gz") || strings.HasSuffix(bundlePath, ".tgz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			// Fall back to plain tar
			if _, seekErr := f.Seek(0, 0); seekErr != nil {
				return nil, fmt.Errorf("failed to seek to start of file: %w", seekErr)
			}
		} else {
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

// manifestEntry represents an entry in the docker-archive manifest.json.
type manifestEntry struct {
	RepoTags []string `json:"RepoTags"`
}

// parseManifestJSON parses manifest.json and extracts all RepoTags.
func parseManifestJSON(r io.Reader) ([]string, error) {
	// Read all content - manifest.json is small
	content, err := io.ReadAll(bufio.NewReader(r))
	if err != nil {
		return nil, err
	}

	var entries []manifestEntry
	if err := json.Unmarshal(content, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse manifest.json: %w", err)
	}

	var refs []string
	for _, entry := range entries {
		refs = append(refs, entry.RepoTags...)
	}

	return refs, nil
}
