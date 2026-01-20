package e2e

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func vmShellCommandArgs(command string) []string {
	return []string{"bash -lc " + strconv.Quote(command)}
}

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
		outputBuf, err := h.VM.RunSSH(vmShellCommandArgs(command), nil)
		output := ""
		if outputBuf != nil {
			output = outputBuf.String()
		} else if err != nil {
			output = ""
		}
		if trim {
			return strings.TrimSpace(output)
		}
		return output
	}
}

// WaitForPodmanImagePresence waits until a podman image is present/absent on the VM.
func (h *Harness) WaitForPodmanImagePresence(imageRef string, shouldExist bool) {
	GinkgoWriter.Printf("Waiting for podman image presence (image=%s shouldExist=%t)\n", imageRef, shouldExist)
	Expect(h).ToNot(BeNil())
	Expect(strings.TrimSpace(imageRef)).ToNot(BeEmpty())

	checkCmd := fmt.Sprintf("sudo podman image inspect %s >/dev/null 2>&1 && echo present || echo missing", imageRef)
	Eventually(h.VMCommandOutputFunc(checkCmd, false), TIMEOUT, POLLING).
		Should(Satisfy(func(raw string) bool {
			present := strings.TrimSpace(raw) == "present"
			if shouldExist {
				return present
			}
			return !present
		}))
}

const pruningArtifactsDir = "artifacts/pruning"

func WriteEvidence(dir, filename, command, output string, err error) error {
	var b strings.Builder
	b.WriteString("$ ")
	b.WriteString(command)
	b.WriteString("\n")
	if err != nil {
		b.WriteString("error: ")
		b.WriteString(err.Error())
		b.WriteString("\n")
	}
	b.WriteString(output)
	return os.WriteFile(filepath.Join(dir, filename), []byte(b.String()), 0o600)
}

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

// SetupScenario creates the artifacts dir for the scenario.
func (h *Harness) SetupScenario(deviceID, testCase string) (string, error) {
	artifactDir, err := h.EnsureArtifactDir(testCase)
	if err != nil {
		return "", err
	}
	GinkgoWriter.Printf("Preparing scenario (device=%s testCase=%s)\n", deviceID, testCase)
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
	outputBuf, err := h.VM.RunSSH(vmShellCommandArgs(command), nil)
	output := ""
	if outputBuf != nil {
		output = outputBuf.String()
	}
	if writeErr := WriteEvidence(artifactDir, filename, command, output, err); writeErr != nil {
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
	if writeErr := WriteEvidence(artifactDir, filename, cmd, output, err); writeErr != nil {
		if err == nil {
			return writeErr
		}
		return errors.Join(err, writeErr)
	}
	GinkgoWriter.Printf("Wrote host evidence file: %s\n", filepath.Join(artifactDir, filename))
	return err
}

// DropinCleanupFunc returns a cleanup func that removes a drop-in and records evidence.
func (h *Harness) DropinCleanupFunc(artifactDir, filename, command string) func() {
	GinkgoWriter.Printf("Registering drop-in cleanup (file=%s)\n", filename)
	return func() {
		_, _ = h.RunVMCommandWithEvidence(artifactDir, filename, command)
	}
}

// CreateInlineApplicationSpec builds an inline application provider spec from compose yaml content.
func CreateInlineApplicationSpec(inlineAppComposeYaml, filename string) v1beta1.InlineApplicationProviderSpec {
	return v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Content: &inlineAppComposeYaml,
				Path:    filename,
			},
		},
	}
}

// UpdateDeviceApplicationFromInline updates or appends an inline application spec on the device.
func UpdateDeviceApplicationFromInline(device *v1beta1.Device, appName string, inlineApp v1beta1.InlineApplicationProviderSpec) error {
	composeApp := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    &appName,
	}
	if err := composeApp.FromInlineApplicationProviderSpec(inlineApp); err != nil {
		return err
	}

	var appSpec v1beta1.ApplicationProviderSpec
	if err := appSpec.FromComposeApplication(composeApp); err != nil {
		return err
	}

	if device.Spec.Applications == nil {
		device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
		return nil
	}

	for i, existing := range *device.Spec.Applications {
		existingName, _ := existing.GetName()
		if existingName != nil && *existingName == appName {
			(*device.Spec.Applications)[i] = appSpec
			return nil
		}
	}

	*device.Spec.Applications = append(*device.Spec.Applications, appSpec)
	return nil
}
