package auxiliary

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
)

// testAppVersions defines the versions and messages for the test-app chart.
var testAppVersions = map[string]string{
	"0.1.0": "hello v1",
	"0.2.0": "hello v2",
}

// UploadCharts uploads helm charts to the registry.
func (s *Services) UploadCharts() error {
	// Check if helm is available on the test runner (not in the agent VM)
	if _, err := exec.LookPath("helm"); err != nil {
		logrus.Warn("helm not found in PATH, attempting to install...")
		projectRoot, err := getProjectRoot()
		if err != nil {
			logrus.Warnf("failed to get project root for helm installation: %v, skipping chart upload", err)
			return nil
		}
		installScript := filepath.Join(projectRoot, "test", "scripts", "install_helm.sh")
		if !fileExists(installScript) {
			logrus.Warnf("install_helm.sh not found at %s, skipping chart upload", installScript)
			return nil
		}

		logrus.Infof("Running %s to install helm...", installScript)
		installCmd := exec.Command("bash", installScript)
		if out, err := installCmd.CombinedOutput(); err != nil {
			logrus.Warnf("failed to install helm: %v, output: %s, skipping chart upload", err, string(out))
			return nil
		}

		// Verify helm is now available
		if _, err := exec.LookPath("helm"); err != nil {
			logrus.Warn("helm still not found after installation attempt, skipping chart upload")
			return nil
		}
		logrus.Info("helm installed successfully")
	}

	projectRoot, err := getProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}
	chartsDir := filepath.Join(projectRoot, "test", "scripts", "agent-images", "charts")
	if !fileExists(chartsDir) {
		logrus.Warn("Charts directory not found, skipping chart upload")
		return nil
	}

	entries, err := os.ReadDir(chartsDir)
	if err != nil {
		return fmt.Errorf("failed to read charts directory: %w", err)
	}

	chartCount := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		chartDir := filepath.Join(chartsDir, entry.Name())
		if !fileExists(filepath.Join(chartDir, "Chart.yaml")) {
			continue
		}

		chartName := entry.Name()
		if chartName == "test-app" {
			for version, message := range testAppVersions {
				if err := s.uploadTestAppChart(chartDir, version, message); err != nil {
					return err
				}
				chartCount++
			}
		} else {
			if err := s.uploadChart(chartDir); err != nil {
				return err
			}
			chartCount++
		}
	}

	logrus.Infof("Uploaded %d helm chart(s) to registry %s", chartCount, s.Registry.URL)
	return nil
}

func (s *Services) uploadTestAppChart(chartDir, version, message string) error {
	tmpDir, err := os.MkdirTemp("", "chart-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	workDir := filepath.Join(tmpDir, "test-app")
	if err := copyDir(chartDir, workDir); err != nil {
		return fmt.Errorf("failed to copy chart: %w", err)
	}

	chartYaml := filepath.Join(workDir, "Chart.yaml")
	if err := replaceLineWithPrefix(chartYaml, "version:", fmt.Sprintf("version: %s", version)); err != nil {
		return fmt.Errorf("failed to update Chart.yaml: %w", err)
	}
	valuesYaml := filepath.Join(workDir, "values.yaml")
	if err := replaceLineWithPrefix(valuesYaml, "message:", fmt.Sprintf("message: \"%s\"", message)); err != nil {
		return fmt.Errorf("failed to update values.yaml: %w", err)
	}

	logrus.Infof("Packaging chart: test-app (version: %s, message: %s)", version, message)
	pkgCmd := exec.Command("helm", "package", workDir, "--destination", tmpDir)
	if out, err := pkgCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm package failed: %w, output: %s", err, string(out))
	}

	pkgFile := filepath.Join(tmpDir, fmt.Sprintf("test-app-%s.tgz", version))
	ociRef := fmt.Sprintf("oci://%s/flightctl/charts", s.Registry.URL)
	logrus.Infof("Pushing test-app:%s to %s", version, ociRef)
	pushCmd := exec.Command("helm", "push", pkgFile, ociRef, "--insecure-skip-tls-verify")
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm push failed: %w, output: %s", err, string(out))
	}

	return nil
}

func (s *Services) uploadChart(chartDir string) error {
	chartYamlPath := filepath.Join(chartDir, "Chart.yaml")
	content, err := os.ReadFile(chartYamlPath)
	if err != nil {
		return fmt.Errorf("failed to read Chart.yaml: %w", err)
	}

	var version string
	for _, line := range strings.Split(string(content), "\n") {
		if val, found := strings.CutPrefix(line, "version:"); found {
			version = strings.TrimSpace(val)
			break
		}
	}
	if version == "" {
		return fmt.Errorf("version not found in Chart.yaml")
	}

	chartName := filepath.Base(chartDir)
	logrus.Infof("Packaging chart: %s (version: %s)", chartName, version)

	tmpDir, err := os.MkdirTemp("", "chart-pkg-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	pkgCmd := exec.Command("helm", "package", chartDir, "--destination", tmpDir)
	if out, err := pkgCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm package failed: %w, output: %s", err, string(out))
	}

	pkgFile := filepath.Join(tmpDir, fmt.Sprintf("%s-%s.tgz", chartName, version))
	ociRef := fmt.Sprintf("oci://%s/flightctl/charts", s.Registry.URL)
	logrus.Infof("Pushing %s:%s to %s", chartName, version, ociRef)
	pushCmd := exec.Command("helm", "push", pkgFile, ociRef, "--insecure-skip-tls-verify")
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm push failed: %w, output: %s", err, string(out))
	}

	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, data, info.Mode())
	})
}

func replaceLineWithPrefix(filePath, prefix, newLine string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = newLine
			break
		}
	}

	return os.WriteFile(filePath, []byte(strings.Join(lines, "\n")), 0600)
}
