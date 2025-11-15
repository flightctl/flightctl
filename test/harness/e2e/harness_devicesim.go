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
	if f1, ok1 := cmd.Stdout.(*os.File); ok1 {
		if f2, ok2 := cmd.Stderr.(*os.File); ok2 {
			if f1 == f2 {
				_ = f1.Close()
			} else {
				_ = f1.Close()
				_ = f2.Close()
			}
		} else {
			_ = f1.Close()
		}
	} else if c, ok := cmd.Stdout.(interface{ Close() error }); ok { // fallback for custom closers
		_ = c.Close()
	}
	if _, ok := cmd.Stdout.(*os.File); !ok { // only attempt stderr fallback if stdout wasn't handled as file
		if c, ok := cmd.Stderr.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}

	return stopErr
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

	logrus.Infof("device simulator agent config prepared at %s; certs in %s", destConfigPath, destCertsDir)
	return destConfigPath, nil
}

// DeleteAllResourcesFound deletes all fleets first and then all devices.
// Returns the names of deleted fleets and IDs of deleted devices, in order processed.
func (h *Harness) DeleteAllResourcesFound() ([]string, []string, error) {
	// Delete fleets first so they stop selecting devices
	fleetsOut, err := h.CLI("get", "fleets", "-o", "name")
	if err != nil {
		return nil, nil, err
	}
	var deletedFleets []string
	for _, line := range strings.Split(strings.TrimSpace(fleetsOut), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		name = strings.TrimPrefix(name, "fleet/")
		if _, err := h.ManageResource("delete", "fleet/"+name); err != nil {
			return deletedFleets, nil, err
		}
		deletedFleets = append(deletedFleets, name)
	}

	// Then delete devices
	devicesOut, err := h.CLI("get", "devices", "-o", "name")
	if err != nil {
		return deletedFleets, nil, err
	}
	var deletedDevices []string
	for _, line := range strings.Split(strings.TrimSpace(devicesOut), "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		id = strings.TrimPrefix(id, "device/")
		if _, err := h.ManageResource("delete", "device/"+id); err != nil {
			return deletedFleets, deletedDevices, err
		}
		deletedDevices = append(deletedDevices, id)
	}
	return deletedFleets, deletedDevices, nil
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
func (h *Harness) ValidateFleetYAMLDevicesPerFleet(yamlContent string, expectedDevicesPerFleet int, expectedFleetCount int) error {
	if yamlContent == "" {
		return fmt.Errorf("yaml content is empty")
	}
	// Split K8s multi-document YAMLs (leading doc counts as one)
	docs := 1
	for _, line := range strings.Split(yamlContent, "\n") {
		if strings.TrimSpace(line) == "---" {
			docs++
		}
	}
	if docs != expectedFleetCount {
		return fmt.Errorf("expected %d fleet docs, found %d", expectedFleetCount, docs)
	}
	expectedAnno := fmt.Sprintf("devices-per-fleet: \"%d\"", expectedDevicesPerFleet)
	if !strings.Contains(yamlContent, expectedAnno) {
		return fmt.Errorf("expected annotation %q not found", expectedAnno)
	}
	return nil
}
