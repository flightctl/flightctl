package e2e

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
)

// StartFlightCtlAgent starts the flightctl-agent service
func (h *Harness) StartFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "start", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to start flightctl-agent: %w", err)
	}
	return nil
}

// StopFlightCtlAgent stops the flightctl-agent service
func (h *Harness) StopFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "stop", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to stop flightctl-agent: %w", err)
	}
	return nil
}

// RestartFlightCtlAgent restarts the flightctl-agent service
func (h *Harness) RestartFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "restart", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to restart flightctl-agent: %w", err)
	}
	return nil
}

// ReloadFlightCtlAgent reloads the flightctl-agent service
func (h *Harness) ReloadFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "reload", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to reload flightctl-agent: %w", err)
	}
	return nil
}

// VMCommandOutputFunc returns a polling function for Eventually/Consistently.
func (h *Harness) VMCommandOutputFunc(command string, trim bool) func() string {
	return func() string {
		outputBuf, _ := h.VM.RunSSH([]string{"bash", "-c", command}, nil)
		output := outputBuf.String()
		if trim {
			return strings.TrimSpace(output)
		}
		return output
	}
}

// DeviceNotHealthyFunc returns a polling function that checks the device health summary.
func (h *Harness) DeviceNotHealthyFunc(deviceID string) func() bool {
	return func() bool {
		resp, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil || resp.JSON200 == nil {
			return false
		}
		if resp.JSON200.Status == nil {
			return false
		}
		if resp.JSON200.Status.ApplicationsSummary.Status == "" {
			return false
		}
		return resp.JSON200.Status.ApplicationsSummary.Status != v1beta1.ApplicationsSummaryStatusHealthy
	}
}

const pruningArtifactsDir = "artifacts/pruning"

// EnsureArtifactDir builds and creates the per-test artifact directory.
func (h *Harness) EnsureArtifactDir(testCase string) (string, error) {
	testID := h.GetTestIDFromContext()
	dir := filepath.Join(pruningArtifactsDir, fmt.Sprintf("%s-%s", testCase, testID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	GinkgoWriter.Printf("Created artifact dir (testCase=%s dir=%s)\n", testCase, dir)
	return dir, nil
}

// SetupScenario creates the artifacts dir and prepares VM access.
func (h *Harness) SetupScenario(deviceID, testCase string) (string, error) {
	artifactDir, err := h.EnsureArtifactDir(testCase)
	if err != nil {
		return "", err
	}
	GinkgoWriter.Printf("Preparing scenario (device=%s testCase=%s)\n", deviceID, testCase)
	if err := h.PrepareVMAccess(deviceID, artifactDir); err != nil {
		return "", err
	}
	return artifactDir, nil
}

// CaptureStandardEvidence records common evidence files for pruning scenarios.
func (h *Harness) CaptureStandardEvidence(artifactDir, deviceID string) error {
	GinkgoWriter.Printf("Capturing standard evidence (device=%s dir=%s)\n", deviceID, artifactDir)
	if err := h.CaptureVMCommand(artifactDir, "vm_systemctl_status.txt", "sudo systemctl status flightctl-agent --no-pager", false); err != nil {
		return err
	}
	if err := h.CaptureVMCommand(artifactDir, "vm_desired_json.txt", "sudo cat /var/lib/flightctl/desired.json", false); err != nil {
		return err
	}
	if err := h.CaptureVMCommand(artifactDir, "vm_current_json.txt", "sudo cat /var/lib/flightctl/current.json", false); err != nil {
		return err
	}
	_ = h.CaptureVMCommand(artifactDir, "vm_refs_cat.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json", true)
	if err := h.CaptureVMCommand(artifactDir, "vm_podman_images_all.txt", "podman images --no-trunc", false); err != nil {
		return err
	}
	if err := h.CaptureHostCLI(artifactDir, "host_flightctl_get_device_yaml.txt", "get", "device", deviceID, "-o", "yaml"); err != nil {
		return err
	}
	if err := h.CaptureHostCLI(artifactDir, "host_flightctl_get_device_json.txt", "get", "device", deviceID, "-o", "json"); err != nil {
		return err
	}
	_ = h.CaptureVMCommand(artifactDir, "vm_journalctl_flightctl_agent.txt", "sudo journalctl -u flightctl-agent --no-pager -n 200", true)
	return nil
}

// RunVMCommandWithEvidence runs a VM command and captures its output.
func (h *Harness) RunVMCommandWithEvidence(artifactDir, filename, command string) (string, error) {
	GinkgoWriter.Printf("VM command (file=%s cmd=%s)\n", filename, command)
	outputBuf, err := h.VM.RunSSH([]string{"bash", "-c", command}, nil)
	output := outputBuf.String()
	if writeErr := util.WriteEvidence(artifactDir, filename, command, output, err); writeErr != nil {
		if err == nil {
			return output, writeErr
		}
		return output, errors.Join(err, writeErr)
	}
	GinkgoWriter.Printf("Wrote VM evidence file: %s\n", filepath.Join(artifactDir, filename))
	return output, err
}

// CaptureVMCommand runs a VM command and writes evidence.
func (h *Harness) CaptureVMCommand(artifactDir, filename, command string, optional bool) error {
	GinkgoWriter.Printf("Capture VM evidence (file=%s optional=%t)\n", filename, optional)
	_, err := h.RunVMCommandWithEvidence(artifactDir, filename, command)
	if optional {
		return nil
	}
	return err
}

// CaptureHostCLI runs flightctl on the host and writes evidence.
func (h *Harness) CaptureHostCLI(artifactDir, filename string, args ...string) error {
	GinkgoWriter.Printf("Host CLI evidence (file=%s args=%s)\n", filename, strings.Join(args, " "))
	output, err := h.CLI(args...)
	cmd := "flightctl " + strings.Join(args, " ")
	if writeErr := util.WriteEvidence(artifactDir, filename, cmd, output, err); writeErr != nil {
		if err == nil {
			return writeErr
		}
		return errors.Join(err, writeErr)
	}
	GinkgoWriter.Printf("Wrote host evidence file: %s\n", filepath.Join(artifactDir, filename))
	return err
}

// UpdateDeviceSpecWithEvidence updates a device and captures evidence.
func (h *Harness) UpdateDeviceSpecWithEvidence(deviceID, artifactDir, filename, description string, updateFunc func(*v1beta1.Device)) error {
	GinkgoWriter.Printf("Updating device spec (device=%s desc=%s)\n", deviceID, description)
	expectedRenderedVersion, err := h.PrepareNextDeviceVersion(deviceID)
	if err != nil {
		_ = util.WriteEvidence(artifactDir, filename, description, "prepare next rendered version failed", err)
		return err
	}
	if err := h.UpdateDeviceWithRetries(deviceID, updateFunc); err != nil {
		_ = util.WriteEvidence(artifactDir, filename, description, "update device failed", err)
		return err
	}
	if err := h.WaitForDeviceNewRenderedVersion(deviceID, expectedRenderedVersion); err != nil {
		_ = util.WriteEvidence(artifactDir, filename, description, "wait for rendered version failed", err)
		return err
	}
	output := fmt.Sprintf("updated to renderedVersion=%d", expectedRenderedVersion)
	if writeErr := util.WriteEvidence(artifactDir, filename, description, output, nil); writeErr != nil {
		return writeErr
	}
	GinkgoWriter.Printf("Wrote update evidence file: %s\n", filepath.Join(artifactDir, filename))
	return nil
}

// UpdateApplicationsWithEvidence updates device applications and captures evidence.
func (h *Harness) UpdateApplicationsWithEvidence(deviceID, artifactDir, filename, description string, apps ...v1beta1.ApplicationProviderSpec) error {
	GinkgoWriter.Printf("Updating applications (device=%s file=%s)\n", deviceID, filename)
	if len(apps) == 0 {
		apps = []v1beta1.ApplicationProviderSpec{}
	}
	return h.UpdateDeviceSpecWithEvidence(deviceID, artifactDir, filename, description, func(device *v1beta1.Device) {
		device.Spec.Applications = &apps
	})
}

// UpdateOSImageWithEvidence updates the device OS image and captures evidence.
func (h *Harness) UpdateOSImageWithEvidence(deviceID, artifactDir, filename, description, image string) error {
	GinkgoWriter.Printf("Updating OS image (device=%s image=%s file=%s)\n", deviceID, image, filename)
	return h.UpdateDeviceSpecWithEvidence(deviceID, artifactDir, filename, description, func(device *v1beta1.Device) {
		if device.Spec.Os == nil {
			device.Spec.Os = &v1beta1.DeviceOsSpec{}
		}
		device.Spec.Os.Image = image
	})
}

// DropinCleanupFunc returns a cleanup func that removes a drop-in and records evidence.
func (h *Harness) DropinCleanupFunc(artifactDir, filename, command string) func() {
	GinkgoWriter.Printf("Registering drop-in cleanup (file=%s)\n", filename)
	return func() {
		_, _ = h.RunVMCommandWithEvidence(artifactDir, filename, command)
	}
}

// PrepareVMAccess captures device info and ensures VM access for the scenario.
func (h *Harness) PrepareVMAccess(deviceID, artifactDir string) error {
	GinkgoWriter.Printf("Preparing VM access (device=%s dir=%s)\n", deviceID, artifactDir)
	consoleOut, err := h.RunConsoleCommand(deviceID, nil, "whoami")
	if err != nil {
		_ = util.WriteEvidence(artifactDir, "host_console_whoami.txt", "flightctl console device/"+deviceID+" -- whoami", consoleOut, err)
		return err
	}
	if err := util.WriteEvidence(artifactDir, "host_console_whoami.txt", "flightctl console device/"+deviceID+" -- whoami", consoleOut, nil); err != nil {
		return err
	}
	GinkgoWriter.Printf("Wrote console evidence file: %s\n", filepath.Join(artifactDir, "host_console_whoami.txt"))

	sshWhoamiOut, err := h.VM.RunSSH([]string{"whoami"}, nil)
	if err != nil {
		_ = util.WriteEvidence(artifactDir, "vm_ssh_whoami.txt", "whoami", sshWhoamiOut.String(), err)
		return err
	}
	if err := util.WriteEvidence(artifactDir, "vm_ssh_whoami.txt", "whoami", sshWhoamiOut.String(), nil); err != nil {
		return err
	}
	GinkgoWriter.Printf("Wrote SSH evidence file: %s\n", filepath.Join(artifactDir, "vm_ssh_whoami.txt"))
	return nil
}
