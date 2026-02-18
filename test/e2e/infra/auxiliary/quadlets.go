package auxiliary

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

// UploadQuadlets uploads quadlet artifacts to the registry.
func (s *Services) UploadQuadlets() error {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}
	quadletsDir := filepath.Join(projectRoot, "test", "scripts", "agent-images", "quadlets")
	if !fileExists(quadletsDir) {
		logrus.Warn("Quadlets directory not found, skipping quadlet upload")
		return nil
	}

	artifactCount := 0

	appsDir := filepath.Join(quadletsDir, "apps")
	if fileExists(appsDir) {
		entries, err := os.ReadDir(appsDir)
		if err != nil {
			return fmt.Errorf("failed to read apps directory: %w", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			appDir := filepath.Join(appsDir, entry.Name())
			if err := s.uploadQuadletApp(appDir, entry.Name()); err != nil {
				return err
			}
			artifactCount++
		}
	}

	volumesDir := filepath.Join(quadletsDir, "volumes")
	if fileExists(volumesDir) {
		entries, err := os.ReadDir(volumesDir)
		if err != nil {
			return fmt.Errorf("failed to read volumes directory: %w", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			volumeDir := filepath.Join(volumesDir, entry.Name())
			if err := s.uploadQuadletVolume(volumeDir, entry.Name()); err != nil {
				return err
			}
			artifactCount++
		}
	}

	logrus.Infof("Uploaded %d quadlet artifact(s) to registry %s", artifactCount, s.RegistryURL)
	return nil
}

func (s *Services) uploadQuadletApp(appDir, appName string) error {
	tmpDir, err := os.MkdirTemp("", "quadlet-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tarball := filepath.Join(tmpDir, appName+".tar.gz")
	logrus.Infof("Bundling quadlet app: %s", appName)

	if err := createTarGz(tarball, appDir); err != nil {
		return fmt.Errorf("failed to create tarball: %w", err)
	}

	artifactRef := fmt.Sprintf("%s/flightctl/quadlets/%s:latest", s.RegistryURL, appName)
	logrus.Infof("Pushing %s to %s", appName, artifactRef)

	return s.pushArtifact(artifactRef, tarball)
}

func createTarGz(tarballPath, srcDir string) error {
	tarball, err := os.Create(tarballPath)
	if err != nil {
		return err
	}
	defer tarball.Close()

	gzWriter := gzip.NewWriter(tarball)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(tarWriter, file)
		return err
	})
}

func (s *Services) uploadQuadletVolume(volumeDir, volumeName string) error {
	entries, err := os.ReadDir(volumeDir)
	if err != nil {
		return fmt.Errorf("failed to read volume directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, filepath.Join(volumeDir, entry.Name()))
		}
	}

	if len(files) == 0 {
		logrus.Warnf("No files found in %s, skipping", volumeDir)
		return nil
	}

	artifactRef := fmt.Sprintf("%s/flightctl/quadlets/%s:latest", s.RegistryURL, volumeName)
	logrus.Infof("Pushing volume artifact: %s to %s", volumeName, artifactRef)

	return s.pushVolumeArtifact(artifactRef, volumeDir, files)
}

func (s *Services) pushArtifact(artifactRef, filePath string) error {
	if hasPodmanArtifact() {
		return s.pushArtifactViaPodman(artifactRef, filePath)
	}
	if hasOras() {
		return s.pushArtifactViaOras(artifactRef, filePath)
	}
	return fmt.Errorf("neither 'podman artifact' (podman 5.4+) nor 'oras' is available")
}

func (s *Services) pushVolumeArtifact(artifactRef, dir string, files []string) error {
	if hasPodmanArtifact() {
		return s.pushVolumeArtifactViaPodman(artifactRef, files)
	}
	if hasOras() {
		return s.pushVolumeArtifactViaOras(artifactRef, dir, files)
	}
	return fmt.Errorf("neither 'podman artifact' (podman 5.4+) nor 'oras' is available")
}

func (s *Services) pushVolumeArtifactViaPodman(artifactRef string, files []string) error {
	_ = exec.Command("podman", "artifact", "rm", artifactRef).Run()

	args := []string{"artifact", "add", artifactRef}
	args = append(args, files...)
	addCmd := exec.Command("podman", args...)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("podman artifact add failed: %w, output: %s", err, string(out))
	}

	pushCmd := exec.Command("podman", "artifact", "push", "--tls-verify=false", artifactRef)
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("podman artifact push failed: %w, output: %s", err, string(out))
	}

	_ = exec.Command("podman", "artifact", "rm", artifactRef).Run()
	return nil
}

func (s *Services) pushVolumeArtifactViaOras(artifactRef, dir string, files []string) error {
	var orasFiles []string
	for _, f := range files {
		orasFiles = append(orasFiles, fmt.Sprintf("%s:text/plain", filepath.Base(f)))
	}

	args := []string{"push", "--insecure", artifactRef}
	args = append(args, orasFiles...)
	cmd := exec.Command("oras", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("oras push failed: %w, output: %s", err, string(out))
	}
	return nil
}

func (s *Services) pushArtifactViaPodman(artifactRef, filePath string) error {
	_ = exec.Command("podman", "artifact", "rm", artifactRef).Run()

	addCmd := exec.Command("podman", "artifact", "add", artifactRef, filePath)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("podman artifact add failed: %w, output: %s", err, string(out))
	}

	pushCmd := exec.Command("podman", "artifact", "push", "--tls-verify=false", artifactRef)
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("podman artifact push failed: %w, output: %s", err, string(out))
	}

	_ = exec.Command("podman", "artifact", "rm", artifactRef).Run()
	return nil
}

func (s *Services) pushArtifactViaOras(artifactRef, filePath string) error {
	dir := filepath.Dir(filePath)
	filename := filepath.Base(filePath)
	fileArg := fmt.Sprintf("%s:application/x-gzip", filename)

	cmd := exec.Command("oras", "push", "--insecure", artifactRef, fileArg)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("oras push failed: %w, output: %s", err, string(out))
	}
	return nil
}

func hasPodmanArtifact() bool {
	out, err := exec.Command("podman", "--version").Output()
	if err != nil {
		return false
	}
	version := strings.TrimSpace(string(out))
	parts := strings.Fields(version)
	if len(parts) < 3 {
		return false
	}
	verStr := parts[2]
	verParts := strings.Split(verStr, ".")
	if len(verParts) < 2 {
		return false
	}
	major, err := strconv.Atoi(verParts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(verParts[1])
	if err != nil {
		return false
	}
	return major > 5 || (major == 5 && minor >= 4)
}

func hasOras() bool {
	_, err := exec.LookPath("oras")
	return err == nil
}
