package satellite

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
		len(bundles), s.RegistryURL)
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

func getImageDigest(imageRef string, tlsVerify bool) string {
	tlsArg := fmt.Sprintf("--tls-verify=%v", tlsVerify)
	cmd := exec.Command("skopeo", "inspect", tlsArg, "--format", "{{.Digest}}", imageRef)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (s *Services) imageNeedsPush(localRef, registryRef string) bool {
	localDigest := getImageDigest("containers-storage:"+localRef, false)
	if localDigest == "" {
		return true
	}
	registryDigest := getImageDigest("docker://"+registryRef, false)
	if registryDigest == "" {
		return true
	}
	return localDigest != registryDigest
}

func (s *Services) uploadBundle(bundlePath string) error {
	refs, err := extractImageRefs(bundlePath)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	loadCmd := exec.Command("podman", "load", "-i", bundlePath)
	if _, err := loadCmd.CombinedOutput(); err != nil {
		logrus.Warnf("podman load failed (falling back to skopeo): %v", err)
		return s.uploadBundleViaSkopeo(bundlePath, refs)
	}
	var needsPush []string
	for _, ref := range refs {
		path := ref
		if idx := strings.Index(ref, "/"); idx != -1 {
			path = ref[idx+1:]
		}
		registryRef := fmt.Sprintf("%s/%s", s.RegistryURL, path)
		if s.imageNeedsPush(ref, registryRef) {
			needsPush = append(needsPush, ref)
		}
	}
	if len(needsPush) == 0 {
		return nil
	}
	for _, ref := range needsPush {
		path := ref
		if idx := strings.Index(ref, "/"); idx != -1 {
			path = ref[idx+1:]
		}
		dst := fmt.Sprintf("%s/%s", s.RegistryURL, path)
		pushCmd := exec.Command("podman", "push", "--tls-verify=false", ref, dst)
		if output, err := pushCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("podman push failed for %s: %w, output: %s", ref, err, string(output))
		}
	}
	return nil
}

func (s *Services) uploadBundleViaSkopeo(bundlePath string, refs []string) error {
	var needsPush []string
	for _, ref := range refs {
		path := ref
		if idx := strings.Index(ref, "/"); idx != -1 {
			path = ref[idx+1:]
		}
		registryRef := fmt.Sprintf("%s/%s", s.RegistryURL, path)
		if !imageConfigMatches(bundlePath, ref, registryRef) {
			needsPush = append(needsPush, ref)
		}
	}
	if len(needsPush) == 0 {
		return nil
	}
	for _, ref := range needsPush {
		path := ref
		if idx := strings.Index(ref, "/"); idx != -1 {
			path = ref[idx+1:]
		}
		src := fmt.Sprintf("docker-archive:%s:%s", bundlePath, ref)
		dst := fmt.Sprintf("docker://%s/%s", s.RegistryURL, path)
		cmd := exec.Command("skopeo", "copy", "--dest-tls-verify=false", src, dst)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("skopeo copy failed for %s: %w, output: %s", ref, err, string(out))
		}
	}
	return nil
}

func getImageConfigDigest(imageRef string, tlsVerify bool) string {
	tlsArg := fmt.Sprintf("--tls-verify=%v", tlsVerify)
	cmd := exec.Command("skopeo", "inspect", "--config", tlsArg, imageRef)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(output)
	return fmt.Sprintf("sha256:%x", hash)
}

func imageConfigMatches(bundlePath, imageRef, registryRef string) bool {
	archiveRef := fmt.Sprintf("docker-archive:%s:%s", bundlePath, imageRef)
	a := getImageConfigDigest(archiveRef, false)
	b := getImageConfigDigest("docker://"+registryRef, false)
	return a != "" && b != "" && a == b
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
