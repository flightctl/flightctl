package agent_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const (
	// ApplicationRunningStatus represents the status string used to signify that an application is currently running.
	ApplicationRunningStatus = "status: Running"
)

// WaitForApplicationRunningStatus waits for a specific application on a device to reach the "Running" status within a timeout.
func WaitForApplicationRunningStatus(h *e2e.Harness, deviceId string, applicationImage string) {
	logrus.Infof("Waiting for the application ready status")
	h.WaitForDeviceContents(deviceId, ApplicationRunningStatus,
		func(device *v1alpha1.Device) bool {
			for _, application := range device.Status.Applications {
				if application.Name == applicationImage && application.Status == v1alpha1.ApplicationStatusRunning {
					return true
				}
			}
			return false
		}, TIMEOUT)
}

func createInlineApplicationSpec(content string, path string) v1alpha1.InlineApplicationProviderSpec {
	return v1alpha1.InlineApplicationProviderSpec{
		Inline: []v1alpha1.ApplicationContent{
			{
				Content: &content,
				Path:    path,
			},
		},
	}
}

func updateDeviceApplicationFromInline(device *v1alpha1.Device, inlineAppName string, inlineApp v1alpha1.InlineApplicationProviderSpec) error {
	for i, app := range *device.Spec.Applications {
		if app.Name != nil && *app.Name == inlineAppName {
			err := (*device.Spec.Applications)[i].FromInlineApplicationProviderSpec(inlineApp)
			if err != nil {
				return fmt.Errorf("failed to update application %s from inline spec: %w", inlineAppName, err)
			}
			return nil
		}
	}
	return fmt.Errorf("application %s not found in device spec", inlineAppName)
}

func updateDevice(harness *e2e.Harness, deviceID string, updateFunc func(device *v1alpha1.Device)) {
	// Get the next expected rendered version
	newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceID)
	Expect(err).ToNot(HaveOccurred())

	// Update the device with the application config
	err = harness.UpdateDeviceWithRetries(deviceID, updateFunc)
	Expect(err).ToNot(HaveOccurred())

	logrus.Infof("Waiting for the device to pick the config")
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
