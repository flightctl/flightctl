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

	"github.com/flightctl/flightctl/test/util"
	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/sirupsen/logrus"
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
// The context parameter is currently unused but reserved for future use (e.g., cancellation during setup).
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
	// Always log only to a file under the repo root .output/devicesim folder (collected by CI)
	repoRoot := util.GetTopLevelDir()
	logDir := filepath.Join(repoRoot, ".output", "devicesim")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	// Timestamped filename to avoid collisions between parallel tests
	ts := time.Now().Format("20060102-150405.000000000")
	logPath := filepath.Join(logDir, fmt.Sprintf("devicesimulator-%s.log", ts))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log file %s: %w", logPath, err)
	}
	// Write only to the log file (no mirroring to Ginkgo)
	cmd.Stdout = f
	cmd.Stderr = f
	logrus.Infof("starting devicesimulator (logging only to %s)", logPath)

	logrus.Infof("starting devicesimulator: %s", strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		// Close the log file if we fail to start
		_ = f.Close()
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

	var stopErr error
	select {
	case err := <-done:
		stopErr = err
	case <-time.After(timeout):
		// Timed out; force kill the process group (or process)
		if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil && pgid > 0 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = cmd.Process.Kill()
		}
		stopErr = <-done
	}

	// After stopping the simulator, delete resources created by it (best-effort)
	if _, _, delErr := h.DeleteAllResourcesFound(); delErr != nil {
		logrus.Errorf("error deleting resources after stopping simulator: %v", delErr)
	}

	// Close any attached log file writers, if used
	closeFileIfNeeded(cmd.Stdout)
	// Only close stderr if it's different from stdout
	if cmd.Stderr != cmd.Stdout {
		closeFileIfNeeded(cmd.Stderr)
	}

	return stopErr
}

// closeFileIfNeeded attempts to close a file-like object if it implements the Close method.
// This is a helper to safely close log files attached to command stdout/stderr.
func closeFileIfNeeded(writer io.Writer) {
	if closer, ok := writer.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			logrus.Warnf("error closing log file: %v", err)
		}
	}
}

// copyFile copies a file from source to destination, creating the destination directory if needed.
func copyFile(from, to string) error {
	srcInfo, err := os.Stat(from)
	if err != nil {
		return fmt.Errorf("stat source file %s: %w", from, err)
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("source %s is a directory, not a file", from)
	}

	if err := os.MkdirAll(filepath.Dir(to), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	r, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer r.Close()

	w, err := os.Create(to)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer w.Close()

	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("copying file content: %w", err)
	}

	// Preserve file permissions
	if err := os.Chmod(to, srcInfo.Mode()); err != nil {
		return fmt.Errorf("setting destination file permissions: %w", err)
	}

	return nil
}

// SetupDeviceSimulatorAgentConfig generates an agent configuration file for the device simulator
// and copies required certificates into the expected locations under ~/.flightctl.
// The server and logLevel parameters are reserved for future use.
// Non-zero durations override fetch/update intervals; zero values default to 2 seconds.
// Returns the path to the generated agent config file.
func (h *Harness) SetupDeviceSimulatorAgentConfig(server string, logLevel string, specFetch time.Duration, statusUpdate time.Duration) (string, error) {
	_ = server   // Reserved for future use
	_ = logLevel // Reserved for future use
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

	logrus.Infof("device simulator agent config prepared at %s; certs in %s", destConfigPath, destCertsDir)
	return destConfigPath, nil
}

// DeleteAllResourcesFound deletes only fleets and devices created by the current test (identified by test-id label).
// Returns the names of deleted fleets and IDs of deleted devices, in order processed.
// Partial results are returned even if an error occurs during deletion.
func (h *Harness) DeleteAllResourcesFound() ([]string, []string, error) {
	testID := h.GetTestIDFromContext()
	logrus.Infof("Deleting test resources with test-id: %s", testID)

	var deletedFleets []string
	var deletedDevices []string
	var deleteErr error

	// Delete fleets first so they stop selecting devices
	// Only get fleets with the test-id label
	fleetsOut, err := h.CLI("get", util.Fleet, "-l", fmt.Sprintf("test-id=%s", testID), "-o", "name")
	if err != nil {
		// If no fleets found, that's fine
		logrus.Debugf("No fleets found with test-id %s: %v", testID, err)
	} else {
		fleetsOut = strings.TrimSpace(fleetsOut)
		if fleetsOut != "" {
			// Parse fleet names from the output
			// Output format: "fleet/name1\nfleet/name2\n..."
			for _, line := range strings.Split(fleetsOut, "\n") {
				name := strings.TrimSpace(line)
				if name == "" {
					continue
				}
				// Extract just the name part from "fleet/name"
				name = strings.TrimPrefix(name, "fleet/")
				if _, err := h.ManageResource("delete", "fleet/"+name); err != nil {
					if deleteErr == nil {
						deleteErr = fmt.Errorf("deleting fleet %s: %w", name, err)
					} else {
						deleteErr = fmt.Errorf("%v; deleting fleet %s: %w", deleteErr, name, err)
					}
					logrus.Warnf("failed to delete fleet %s: %v", name, err)
					continue // Continue with remaining fleets
				}
				deletedFleets = append(deletedFleets, name)
			}
		}
	}

	// Then delete devices with the test-id label
	devicesOut, err := h.CLI("get", util.Device, "-l", fmt.Sprintf("test-id=%s", testID), "-o", "name")
	if err != nil {
		if deleteErr != nil {
			return deletedFleets, deletedDevices, fmt.Errorf("fleet deletion errors: %w; listing devices: %w", deleteErr, err)
		}
		// If no devices found, that's fine
		logrus.Debugf("No devices found with test-id %s: %v", testID, err)
		return deletedFleets, deletedDevices, deleteErr
	}

	devicesOut = strings.TrimSpace(devicesOut)
	if devicesOut != "" {
		// Parse device names from the output
		// Output format: "device/name1\ndevice/name2\n..."
		for _, line := range strings.Split(devicesOut, "\n") {
			id := strings.TrimSpace(line)
			if id == "" {
				continue
			}
			// Extract just the name part from "device/name"
			id = strings.TrimPrefix(id, "device/")
			if _, err := h.ManageResource("delete", "device/"+id); err != nil {
				if deleteErr == nil {
					deleteErr = fmt.Errorf("deleting device %s: %w", id, err)
				} else {
					deleteErr = fmt.Errorf("%v; deleting device %s: %w", deleteErr, id, err)
				}
				logrus.Warnf("failed to delete device %s: %v", id, err)
				continue // Continue with remaining devices
			}
			deletedDevices = append(deletedDevices, id)
		}
	}

	if len(deletedFleets) > 0 || len(deletedDevices) > 0 {
		logrus.Infof("Deleted %d fleets and %d devices with test-id %s", len(deletedFleets), len(deletedDevices), testID)
	} else {
		logrus.Debugf("No test resources found to delete with test-id %s", testID)
	}

	return deletedFleets, deletedDevices, deleteErr
}

// GenerateFleetYAMLsForSimulator returns a multi-document Fleet YAML string with
// fleetCount Fleet objects, each selecting devices labeled with its fleet name,
// and annotates each Fleet with the desired devices-per-fleet count for clarity.
// fleetBaseName controls the base of the generated Fleet names (formatted as
// "<fleetBaseName>-%02d"). It validates inputs are positive and returns an error otherwise.
func (h *Harness) GenerateFleetYAMLsForSimulator(fleetCount, devicesPerFleet int, fleetBaseName string) (string, error) {
	if fleetCount <= 0 {
		return "", fmt.Errorf("fleetCount must be > 0")
	}
	if devicesPerFleet <= 0 {
		return "", fmt.Errorf("devicesPerFleet must be > 0")
	}

	if strings.TrimSpace(fleetBaseName) == "" {
		fleetBaseName = "sim-fleet"
	}

	var b strings.Builder
	for i := 0; i < fleetCount; i++ {
		if i > 0 {
			b.WriteString("\n---\n")
		}
		fleetName := fmt.Sprintf("%s-%02d", fleetBaseName, i)
		// Minimal valid Fleet spec mirroring example, with selector/labels and an annotation to record devices-per-fleet
		b.WriteString("apiVersion: flightctl.io/v1alpha1\n")
		b.WriteString("kind: Fleet\n")
		b.WriteString("metadata:\n")
		b.WriteString(fmt.Sprintf("  name: %s\n", fleetName))
		b.WriteString("  annotations:\n")
		b.WriteString(fmt.Sprintf("    devices-per-fleet: \"%d\"\n", devicesPerFleet))
		b.WriteString("spec:\n")
		b.WriteString("  selector:\n")
		b.WriteString("    matchLabels:\n")
		b.WriteString(fmt.Sprintf("      fleet: %s\n", fleetName))
		b.WriteString("  template:\n")
		b.WriteString("    metadata:\n")
		b.WriteString("      labels:\n")
		b.WriteString(fmt.Sprintf("        fleet: %s\n", fleetName))
		b.WriteString("    spec:\n")
		b.WriteString("      os:\n")
		b.WriteString("        image: quay.io/redhat/rhde:9.2\n")
		b.WriteString("      config:\n")
		b.WriteString("        - name: base\n")
		b.WriteString("          gitRef:\n")
		b.WriteString("            repository: flightctl-demos\n")
		b.WriteString("            targetRevision: main\n")
		b.WriteString("            path: /demos/basic-nginx-demo/configuration/\n")
	}
	return b.String(), nil
}

// ValidateFleetYAMLDevicesPerFleet performs a basic consistency check on the generated YAML,
// ensuring it contains the expected number of Fleet documents and the devices-per-fleet annotation.
// Note: This uses simple string matching for YAML parsing and may not catch all malformed YAML.
func (h *Harness) ValidateFleetYAMLDevicesPerFleet(yamlContent string, expectedDevicesPerFleet int, expectedFleetCount int) error {
	if yamlContent == "" {
		return fmt.Errorf("yaml content is empty")
	}

	// Count K8s multi-document YAML separators (leading doc counts as one)
	docs := 1
	for _, line := range strings.Split(yamlContent, "\n") {
		if strings.TrimSpace(line) == "---" {
			docs++
		}
	}
	if docs != expectedFleetCount {
		return fmt.Errorf("expected %d fleet docs, found %d", expectedFleetCount, docs)
	}

	// Check for the expected annotation (must appear expectedFleetCount times)
	expectedAnno := fmt.Sprintf("devices-per-fleet: \"%d\"", expectedDevicesPerFleet)
	count := strings.Count(yamlContent, expectedAnno)
	if count != expectedFleetCount {
		return fmt.Errorf("expected annotation %q found %d times, expected %d", expectedAnno, count, expectedFleetCount)
	}

	return nil
}
