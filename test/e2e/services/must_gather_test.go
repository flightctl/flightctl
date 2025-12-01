package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("FlightCtl Services Must-Gather Script", Label("OCP-85998", "must-gather", "services"), func() {

	var scriptPath string
	var tempDir string
	var originalDir string

	BeforeEach(func() {
		// Try to find the script in common locations
		possiblePaths := []string{
			"/usr/bin/flightctl-services-must-gather",
			"/usr/local/bin/flightctl-services-must-gather",
			"./packaging/must-gather/flightctl-services-must-gather",
			"packaging/must-gather/flightctl-services-must-gather",
		}

		for _, path := range possiblePaths {
			if _, err := os.Stat(path); err == nil {
				scriptPath = path
				break
			}
		}

		if scriptPath == "" {
			// Try to find it relative to the project root
			cwd, _ := os.Getwd()
			GinkgoWriter.Printf("Current directory: %s\n", cwd)
			Skip("flightctl-services-must-gather script not found in expected locations")
		}

		GinkgoWriter.Printf("Using must-gather script at: %s\n", scriptPath)

		// Create temp directory for test execution
		var err error
		tempDir, err = os.MkdirTemp("", "must-gather-test-*")
		Expect(err).ToNot(HaveOccurred())

		originalDir, err = os.Getwd()
		Expect(err).ToNot(HaveOccurred())

		err = os.Chdir(tempDir)
		Expect(err).ToNot(HaveOccurred())

		GinkgoWriter.Printf("Testing in temporary directory: %s\n", tempDir)
	})

	AfterEach(func() {
		if originalDir != "" {
			os.Chdir(originalDir)
		}
		if tempDir != "" {
			os.RemoveAll(tempDir)
		}
	})

	It("should validate must-gather script existence, execution, and diagnostic collection", func() {
		// Step 1: Validate script file and permissions
		By("checking if the script file exists")
		_, err := os.Stat(scriptPath)
		Expect(err).ToNot(HaveOccurred(), "must-gather script should exist at "+scriptPath)

		By("verifying script has executable permissions")
		info, err := os.Stat(scriptPath)
		Expect(err).ToNot(HaveOccurred())
		mode := info.Mode()
		Expect(mode&0111).ToNot(BeZero(), "script should have execute permission")

		By("verifying script is a bash script")
		content, err := os.ReadFile(scriptPath)
		Expect(err).ToNot(HaveOccurred())
		scriptContent := string(content)
		firstLine := strings.Split(scriptContent, "\n")[0]
		Expect(firstLine).To(MatchRegexp("#!/.*bash"), "script should have bash shebang")

		// Step 2: Validate script content quality
		By("verifying script uses proper error handling")
		Expect(scriptContent).To(ContainSubstring("set -euo pipefail"),
			"script should use strict error handling")

		By("verifying script checks for sudo privileges")
		Expect(scriptContent).To(ContainSubstring("sudo"),
			"script should use sudo for privileged operations")

		By("verifying script prompts for storage space confirmation")
		Expect(scriptContent).To(ContainSubstring("Do you have enough storage space"))

		By("verifying script collects comprehensive diagnostic information")
		diagnosticChecks := map[string]string{
			"system info":       "uname",
			"OS release":        "/etc/os-release",
			"SELinux status":    "getenforce",
			"package version":   "dnf list installed",
			"systemd status":    "systemctl status",
			"journal logs":      "journalctl",
			"podman containers": "podman ps",
			"podman images":     "podman images",
			"podman networks":   "podman network",
		}
		for checkName, checkString := range diagnosticChecks {
			Expect(scriptContent).To(ContainSubstring(checkString),
				fmt.Sprintf("script should collect %s", checkName))
		}

		// Step 3: Execute the script
		By("running the must-gather script with automatic 'yes' response")
		cmd := exec.Command("bash", "-c", fmt.Sprintf("echo 'y' | %s", scriptPath))
		cmd.Dir = tempDir

		output, err := cmd.CombinedOutput()
		Expect(err).ToNot(HaveOccurred(), "script should execute without errors")

		outputStr := string(output)
		GinkgoWriter.Printf("Script output:\n%s\n", outputStr)

		By("verifying script completion messages")
		Expect(outputStr).To(ContainSubstring("Beginning Must Gather Collection"))
		Expect(outputStr).To(ContainSubstring("Collecting journal logs"))
		Expect(outputStr).To(ContainSubstring("created successfully"))

		// Step 4: Validate tarball creation
		By("verifying tarball was created")
		files, err := os.ReadDir(tempDir)
		Expect(err).ToNot(HaveOccurred())

		var tarballPath string
		for _, file := range files {
			if strings.HasPrefix(file.Name(), "flightctl-services-must-gather-") &&
				strings.HasSuffix(file.Name(), ".tgz") {
				tarballPath = filepath.Join(tempDir, file.Name())
				GinkgoWriter.Printf("Found tarball: %s\n", file.Name())
				break
			}
		}
		Expect(tarballPath).ToNot(BeEmpty(), "tarball should be created")

		By("verifying tarball is a valid gzip file and not empty")
		tarballInfo, err := os.Stat(tarballPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(tarballInfo.Size()).To(BeNumerically(">", 0), "tarball should not be empty")

		// Step 5: Validate tarball contents
		By("extracting and verifying tarball contents")
		contents, err := util.ExtractTarballContents(tarballPath)
		Expect(err).ToNot(HaveOccurred())

		GinkgoWriter.Printf("Tarball contains %d files\n", len(contents))
		for _, filename := range contents {
			GinkgoWriter.Printf("  - %s\n", filename)
		}

		By("verifying all expected diagnostic files are present")
		expectedFiles := []string{
			"uname-info.log",
			"os-release.log",
			"selinux-status.log",
			"services-version.log",
			"target-list-dependencies.log",
			"systemd-status.log",
			"systemd-list-units.log",
			"podman-ps.log",
			"podman-images.log",
			"podman-volumes.log",
			"podman-networks.log",
			"podman-secrets.log",
			"podman-network-flightctl.log",
			"flightctl-services-must-gather.txt",
		}
		for _, expectedFile := range expectedFiles {
			Expect(contents).To(ContainElement(expectedFile),
				fmt.Sprintf("tarball should contain %s", expectedFile))
		}

		// Step 6: Validate specific file contents
		By("extracting tarball to verify file contents")
		extractDir := filepath.Join(tempDir, "extracted")
		err = os.MkdirAll(extractDir, 0755)
		Expect(err).ToNot(HaveOccurred())

		err = util.ExtractTarball(tarballPath, extractDir)
		Expect(err).ToNot(HaveOccurred())

		By("verifying podman-ps.log contains container information or error message")
		podmanPsPath := filepath.Join(extractDir, "podman-ps.log")
		podmanPsContent, err := os.ReadFile(podmanPsPath)
		Expect(err).ToNot(HaveOccurred())

		podmanPsStr := string(podmanPsContent)
		GinkgoWriter.Printf("podman-ps.log content preview (first 200 chars):\n%s\n",
			util.TruncateString(podmanPsStr, 200))

		// Check if it contains container info or the expected error message
		if !strings.Contains(podmanPsStr, "podman ps failed") {
			// If podman is running, we should see at least some content
			Expect(podmanPsStr).ToNot(BeEmpty())
		}

		By("verifying uname-info.log contains system information")
		unameInfoPath := filepath.Join(extractDir, "uname-info.log")
		unameContent, err := os.ReadFile(unameInfoPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(unameContent)).ToNot(BeEmpty(), "uname-info.log should contain system information")

		By("verifying os-release.log contains OS information")
		osReleasePath := filepath.Join(extractDir, "os-release.log")
		osReleaseContent, err := os.ReadFile(osReleasePath)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(osReleaseContent)).ToNot(BeEmpty(), "os-release.log should contain OS information")
	})
})
