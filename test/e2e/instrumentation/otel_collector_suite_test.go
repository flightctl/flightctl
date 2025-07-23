package agent_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const TIMEOUT = "5m"
const POLLING = "125ms"
const LONGTIMEOUT = "10m"

// Define a type for messages.
type Message string

const (
	UpdateRenderedVersionSuccess    Message = "Updated to desired renderedVersion: 2"
	ComposeFile                     string  = "podman-compose.yaml"
	ExpectedNumSleepAppV1Containers string  = "3"
	ExpectedNumSleepAppV2Containers string  = "1"
	ZeroContainers                  string  = "0"
)

// String returns the string representation of a message.
func (m Message) String() string {
	return string(m)
}

var (
	suiteCtx context.Context
	log      *logrus.Logger
)

func TestAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent E2E Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Agent E2E Suite")
	log = logrus.New()
	log.SetLevel(logrus.InfoLevel)

	// Set up OpenTelemetry collector certificates before running tests
	By("Setting up OpenTelemetry collector certificates")
	setupOtelCollectorCertificates()

	// Restart the otel-collector pod to pick up the new certificates
	By("Restarting otel-collector pod")
	restartOtelCollectorPod()

	// Wait for the otel-collector pod to be ready
	By("Waiting for otel-collector pod to be ready")
	waitForOtelCollectorPodReady()
})

// setupOtelCollectorCertificates runs the certificate setup script
func setupOtelCollectorCertificates() {
	// Find the project root directory
	projectRoot := findProjectRoot()
	if projectRoot == "" {
		Fail("Could not find project root directory")
	}

	scriptPath := filepath.Join(projectRoot, "scripts", "setup-otel-collector-certs.sh")

	// Check if the script exists
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		Fail("Certificate setup script not found at " + scriptPath)
	}

	// Make sure the script is executable
	if err := os.Chmod(scriptPath, 0755); err != nil {
		Fail("Failed to make certificate setup script executable: " + err.Error())
	}

	// Run the certificate setup script
	cmd := exec.Command(scriptPath)
	cmd.Dir = projectRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Infof("Running certificate setup script: %s", scriptPath)
	if err := cmd.Run(); err != nil {
		Fail("Failed to run certificate setup script: " + err.Error())
	}

	log.Info("Certificate setup completed successfully")
}

// restartOtelCollectorPod deletes the existing otel-collector pod to force a restart
func restartOtelCollectorPod() {
	cmd := exec.Command("kubectl", "delete", "pod", "-l", "flightctl.service=flightctl-otel-collector", "-n", "flightctl-external")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Info("Restarting otel-collector pod...")
	if err := cmd.Run(); err != nil {
		Fail("Failed to restart otel-collector pod: " + err.Error())
	}

	// Wait a moment for the pod to be deleted
	time.Sleep(5 * time.Second)
}

// waitForOtelCollectorPodReady waits for the otel-collector pod to be in Ready state
func waitForOtelCollectorPodReady() {
	timeout := 2 * time.Minute
	interval := 5 * time.Second
	startTime := time.Now()

	for time.Since(startTime) < timeout {
		cmd := exec.Command("kubectl", "get", "pods", "-l", "flightctl.service=flightctl-otel-collector", "-n", "flightctl-external", "-o", "jsonpath={.items[0].status.phase}")
		output, err := cmd.Output()

		if err == nil && string(output) == "Running" {
			// Check if the pod is ready
			readyCmd := exec.Command("kubectl", "get", "pods", "-l", "flightctl.service=flightctl-otel-collector", "-n", "flightctl-external", "-o", "jsonpath={.items[0].status.containerStatuses[0].ready}")
			readyOutput, readyErr := readyCmd.Output()

			if readyErr == nil && string(readyOutput) == "true" {
				log.Info("Otel-collector pod is ready")
				return
			}
		}

		log.Infof("Waiting for otel-collector pod to be ready... (elapsed: %v)", time.Since(startTime))
		time.Sleep(interval)
	}

	Fail("Timeout waiting for otel-collector pod to be ready")
}

// findProjectRoot finds the project root directory by looking for the go.mod file
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Walk up the directory tree looking for go.mod
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root directory
			break
		}
		dir = parent
	}

	return ""
}
