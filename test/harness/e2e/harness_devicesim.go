package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/sirupsen/logrus"

	"github.com/flightctl/flightctl/test/util"
)

// EnsureDeviceSimulatorBinary returns the path to the devicesimulator binary, building it if missing.
func (h *Harness) EnsureDeviceSimulatorBinary() (string, error) {
	// Compute the expected path under the repo top-level bin directory
	expectedPath := filepath.Join(util.GetTopLevelDir(), "bin", "devicesimulator")

	if st, err := os.Stat(expectedPath); err == nil && !st.IsDir() {
		return expectedPath, nil
	}

	// Build the devicesimulator using the project's Makefile target
	build := exec.Command("make", "build-devicesimulator")
	build.Dir = util.GetTopLevelDir()
	build.Stdout = ginkgo.GinkgoWriter
	build.Stderr = ginkgo.GinkgoWriter
	logrus.Infof("devicesimulator binary not found; building via 'make build-devicesimulator' in %s", build.Dir)
	if err := build.Run(); err != nil {
		return "", fmt.Errorf("building devicesimulator: %w", err)
	}

	if st, err := os.Stat(expectedPath); err == nil && !st.IsDir() {
		return expectedPath, nil
	}
	return "", fmt.Errorf("devicesimulator binary not found after build at %s", expectedPath)
}

// RunDeviceSimulator starts the devicesimulator in the background with the provided args and returns the running command.
// The process is detached from the provided context and will continue running until explicitly stopped by the caller.
func (h *Harness) RunDeviceSimulator(ctx context.Context, args ...string) (*exec.Cmd, error) {
	// Ensure the simulator exists (build if necessary)
	simPath, err := h.EnsureDeviceSimulatorBinary()
	if err != nil {
		return nil, err
	}
	// Detach from context to run in background
	cmd := exec.Command(simPath)
	// Start in its own process group so it is not tied to the test process signals
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	h.setArgsInCmd(cmd, args...)
	// Stream simulator output to GinkgoWriter for CI visibility
	cmd.Stdout = ginkgo.GinkgoWriter
	cmd.Stderr = ginkgo.GinkgoWriter

	logrus.Infof("starting devicesimulator: %s", strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting devicesimulator: %w", err)
	}
	return cmd, nil
}

// StopDeviceSimulator attempts to gracefully stop a running devicesimulator process.
// It sends SIGTERM to the process group and waits up to the provided timeout before force-killing.
func (h *Harness) StopDeviceSimulator(cmd *exec.Cmd, timeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("invalid devicesimulator command")
	}

	// Try to signal the whole process group first
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil && pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		// Timed out; force kill the process group (or process)
		if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil && pgid > 0 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = cmd.Process.Kill()
		}
		return <-done
	}
}

func copyFile(from, to string) error {
	if _, err := os.Stat(from); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	r, err := os.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(to)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, r)
	return err
}

// SetupDeviceSimulatorAgentConfig generates an agent configuration file for the device simulator
// and copies required certificates into the expected locations under ~/.flightctl.
// If server is non-empty, it overrides the enrollment and management server endpoints in the template.
// If logLevel is non-empty, it overrides the log level. Non-zero durations override fetch/update intervals.
// Returns the path to the generated agent config file.
func (h *Harness) SetupDeviceSimulatorAgentConfig(server string, logLevel string, specFetch time.Duration, statusUpdate time.Duration) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting user home: %w", err)
	}

	// Destination paths used by the devicesimulator by default
	simBaseDir := filepath.Join(homeDir, ".flightctl")
	destCertsDir := filepath.Join(simBaseDir, "certs")
	destConfigPath := filepath.Join(simBaseDir, "agent.yaml")

	// Ensure destination directories exist
	if err := os.MkdirAll(destCertsDir, 0755); err != nil {
		return "", fmt.Errorf("creating simulator certs directory: %w", err)
	}

	// Run the helper script that requests certs and generates bin/agent/etc/flightctl/config.yaml
	scriptPath := filepath.Join(util.GetTopLevelDir(), "test", "scripts", "agent-images", "prepare_agent_config.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		return "", fmt.Errorf("missing prepare_agent_config.sh at %s: %w", scriptPath, err)
	}

	// Always pass intervals; default to 0m2s when zero
	effectiveStatusUpdate := statusUpdate
	effectiveSpecFetch := specFetch
	if effectiveStatusUpdate == 0 {
		effectiveStatusUpdate = 2 * time.Second
	}
	if effectiveSpecFetch == 0 {
		effectiveSpecFetch = 2 * time.Second
	}
	args := []string{
		"--status-update-interval", effectiveStatusUpdate.String(),
		"--spec-fetch-interval", effectiveSpecFetch.String(),
	}

	cmd := exec.Command(scriptPath, args...)
	cmd.Dir = util.GetTopLevelDir()
	cmd.Stdout = ginkgo.GinkgoWriter
	cmd.Stderr = ginkgo.GinkgoWriter

	logrus.Infof("preparing agent config via: %s %s", scriptPath, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("running prepare_agent_config.sh: %w", err)
	}

	// Copy the generated config and certs to ~/.flightctl for the devicesimulator to use
	srcBase := filepath.Join(util.GetTopLevelDir(), "bin", "agent", "etc", "flightctl")
	srcCertsDir := filepath.Join(srcBase, "certs")
	srcConfigPath := filepath.Join(srcBase, "config.yaml")

	if err := copyFile(srcConfigPath, destConfigPath); err != nil {
		return "", fmt.Errorf("copying generated agent config: %w", err)
	}

	entries, err := os.ReadDir(srcCertsDir)
	if err != nil {
		return "", fmt.Errorf("reading generated certs: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(srcCertsDir, entry.Name()), filepath.Join(destCertsDir, entry.Name())); err != nil {
			return "", fmt.Errorf("copying cert %s: %w", entry.Name(), err)
		}
	}

	// Do not modify the generated/copied config file content here.

	logrus.Infof("device simulator agent config prepared at %s; certs in %s", destConfigPath, destCertsDir)
	return destConfigPath, nil
}
