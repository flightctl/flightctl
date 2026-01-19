package agent_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// ApplicationRunningStatus represents the status string used to signify that an application is currently running.
	ApplicationRunningStatus = "status: Running"
)

// WaitForApplicationRunningStatus waits for a specific application on a device to reach the "Running" status with all
// expected workloads running within a timeout.
func WaitForApplicationRunningStatus(h *e2e.Harness, deviceId string, applicationImage string) {
	GinkgoWriter.Printf("Waiting for the application ready status\n")
	h.WaitForDeviceContents(deviceId, ApplicationRunningStatus,
		func(device *v1beta1.Device) bool {
			for _, application := range device.Status.Applications {
				if application.Name == applicationImage && application.Status == v1beta1.ApplicationStatusRunning {
					// ready indicates the number of workloads that are currently running compared to the number of expected
					// workloads. Checks to see if "1/1" or "2/3" containers are ready
					parts := strings.Split(application.Ready, "/")
					if len(parts) != 2 {
						return false
					}
					return parts[0] == parts[1]
				}
			}
			return false
		}, TIMEOUT)
}

func createInlineApplicationSpec(content string, path string) v1beta1.InlineApplicationProviderSpec {
	return v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Content: &content,
				Path:    path,
			},
		},
	}
}

func updateDeviceApplicationFromInline(device *v1beta1.Device, inlineAppName string, inlineApp v1beta1.InlineApplicationProviderSpec) error {
	for i, app := range *device.Spec.Applications {
		existingName, _ := app.GetName()
		if existingName != nil && *existingName == inlineAppName {
			// Get the existing ComposeApplication to preserve Name and other fields
			composeApp, err := app.AsComposeApplication()
			if err != nil {
				return fmt.Errorf("failed to get compose application: %w", err)
			}

			// Update the inline spec
			if err := composeApp.FromInlineApplicationProviderSpec(inlineApp); err != nil {
				return fmt.Errorf("failed to update inline spec: %w", err)
			}

			// Convert back to ApplicationProviderSpec
			var newAppSpec v1beta1.ApplicationProviderSpec
			if err := newAppSpec.FromComposeApplication(composeApp); err != nil {
				return fmt.Errorf("failed to create application spec: %w", err)
			}

			(*device.Spec.Applications)[i] = newAppSpec
			return nil
		}
	}
	return fmt.Errorf("application %s not found in device spec", inlineAppName)
}

func updateDevice(harness *e2e.Harness, deviceID string, updateFunc func(device *v1beta1.Device)) {
	// Get the next expected rendered version
	newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceID)
	Expect(err).ToNot(HaveOccurred())

	// Update the device with the application config
	err = harness.UpdateDeviceWithRetries(deviceID, updateFunc)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("Waiting for the device to pick the config\n")
	err = harness.WaitForDeviceNewRenderedVersion(deviceID, newRenderedVersion)
	Expect(err).ToNot(HaveOccurred())
}

func extractSingleContainerNameFromVM(harness *e2e.Harness) string {
	containerName, err := harness.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "\"{{.Names}} {{.Names}}\"", "|", "head", "-n", "1", "|", "awk", "'{print $1}'"}, nil)
	containerNameString := strings.Trim(containerName.String(), "\n")
	Expect(err).ToNot(HaveOccurred())
	return containerNameString
}

func verifyContainerCount(harness *e2e.Harness, count int) {
	out, err := harness.CheckRunningContainers()
	Expect(err).ToNot(HaveOccurred())
	Expect(out).To(ContainSubstring(fmt.Sprintf("%d", count)))
}

func verifyCommandOutputsSubstring(harness *e2e.Harness, args []string, s string) {
	stdout, err := harness.VM.RunSSH(args, nil)
	Expect(err).ToNot(HaveOccurred())
	Expect(stdout.String()).To(ContainSubstring(s))
}

func verifyCommandLacksSubstring(harness *e2e.Harness, args []string, s string) {
	stdout, err := harness.VM.RunSSH(args, nil)
	Expect(err).ToNot(HaveOccurred())
	Expect(stdout.String()).To(Not(ContainSubstring(s)))
}
