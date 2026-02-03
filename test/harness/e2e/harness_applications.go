package e2e

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	// Quadlet unit path on device
	QuadletUnitPath = "/etc/containers/systemd"
)

// WaitForApplicationRunningStatus waits for an application to reach Running status with ready replicas
func (h *Harness) WaitForApplicationRunningStatus(deviceId string, applicationName string, timeout string) {
	GinkgoWriter.Printf("Waiting for application %s to reach Running status\n", applicationName)
	h.WaitForDeviceContents(deviceId, "Application running",
		func(device *v1beta1.Device) bool {
			for _, application := range device.Status.Applications {
				if application.Name == applicationName && application.Status == v1beta1.ApplicationStatusRunning {
					parts := strings.Split(application.Ready, "/")
					if len(parts) != 2 {
						return false
					}
					return parts[0] == parts[1]
				}
			}
			return false
		}, timeout)
}

// VerifyContainerRunning checks that podman shows containers running with the given image substring
func (h *Harness) VerifyContainerRunning(imageSubstring string, timeout string) {
	Eventually(func() string {
		stdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "{{.Image}}"}, nil)
		Expect(err).ToNot(HaveOccurred())
		return stdout.String()
	}, timeout).Should(ContainSubstring(imageSubstring))
}

// VerifyNoContainersRunning checks that no containers are running
func (h *Harness) VerifyNoContainersRunning(timeout string) {
	Eventually(func() string {
		stdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "-q"}, nil)
		Expect(err).ToNot(HaveOccurred())
		return strings.TrimSpace(stdout.String())
	}, timeout).Should(BeEmpty())
}

// VerifyQuadletApplicationFolderExists checks that the application folder exists
func (h *Harness) VerifyQuadletApplicationFolderExists(appName string, timeout string) {
	appPath := fmt.Sprintf("%s/%s", QuadletUnitPath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"test", "-d", appPath}, nil)
		return err
	}, timeout).Should(Succeed())
}

// VerifyQuadletApplicationFolderDeleted checks that the application folder was removed
func (h *Harness) VerifyQuadletApplicationFolderDeleted(appName string, timeout string) {
	appPath := fmt.Sprintf("%s/%s", QuadletUnitPath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"test", "!", "-d", appPath}, nil)
		return err
	}, timeout).Should(Succeed())
}

// VerifyContainerCPULimitApplied checks that the CPU limit is properly applied to the container
func (h *Harness) VerifyContainerCPULimitApplied(timeout string) bool {
	var result bool
	Eventually(func() bool {
		// Get the container ID first
		idStdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "-q", "-l"}, nil)
		if err != nil {
			GinkgoWriter.Printf("Error getting container ID: %v\n", err)
			return false
		}
		containerID := strings.TrimSpace(idStdout.String())
		if containerID == "" {
			GinkgoWriter.Println("No running container found")
			return false
		}

		GinkgoWriter.Printf("Checking CPU limit for container %s\n", containerID)

		// Get CpuQuota
		quotaStdout, err := h.VM.RunSSH([]string{"sudo", "podman", "inspect", "--format",
			"{{.HostConfig.CpuQuota}}", containerID}, nil)
		if err != nil {
			GinkgoWriter.Printf("Error getting CPU quota: %v\n", err)
			return false
		}
		cpuQuota := strings.TrimSpace(quotaStdout.String())

		// Get CpuPeriod
		periodStdout, err := h.VM.RunSSH([]string{"sudo", "podman", "inspect", "--format",
			"{{.HostConfig.CpuPeriod}}", containerID}, nil)
		if err != nil {
			GinkgoWriter.Printf("Error getting CPU period: %v\n", err)
			return false
		}
		cpuPeriod := strings.TrimSpace(periodStdout.String())

		GinkgoWriter.Printf("Container CPU quota: %s, period: %s\n", cpuQuota, cpuPeriod)

		// Verify that quota is set (non-zero) to confirm limit is applied
		if cpuQuota != "0" && cpuQuota != "" {
			GinkgoWriter.Printf("CPU limit is applied: quota=%s, period=%s\n", cpuQuota, cpuPeriod)
			result = true
			return true
		}

		return false
	}, timeout, "5s").Should(BeTrue(), "Expected CPU limit to be applied to container")
	return result
}

// GetContainerPorts returns the port mappings for running containers
func (h *Harness) GetContainerPorts() (string, error) {
	stdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "{{.Ports}}"}, nil)
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// GetRunningContainerCount returns the number of running containers
func (h *Harness) GetRunningContainerCount() (int, error) {
	stdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "-q"}, nil)
	if err != nil {
		return 0, err
	}
	containers := strings.TrimSpace(stdout.String())
	if containers == "" {
		return 0, nil
	}
	return len(strings.Split(containers, "\n")), nil
}

// NewContainerApplicationSpec creates a ContainerApplication spec with the given parameters
func NewContainerApplicationSpec(
	name string,
	image string,
	ports []v1beta1.ApplicationPort,
	cpu, memory *string,
	volumes *[]v1beta1.ApplicationVolume,
) (v1beta1.ApplicationProviderSpec, error) {
	var resources *v1beta1.ApplicationResources
	if cpu != nil || memory != nil {
		resources = &v1beta1.ApplicationResources{
			Limits: &v1beta1.ApplicationResourceLimits{
				Cpu:    cpu,
				Memory: memory,
			},
		}
	}

	containerApp := v1beta1.ContainerApplication{
		Name:      lo.ToPtr(name),
		AppType:   v1beta1.AppTypeContainer,
		Image:     image,
		Ports:     &ports,
		Resources: resources,
		Volumes:   volumes,
	}

	var appSpec v1beta1.ApplicationProviderSpec
	err := appSpec.FromContainerApplication(containerApp)
	return appSpec, err
}

// NewMountVolume creates a named volume mount for container apps
func NewMountVolume(name, mountPath string) (v1beta1.ApplicationVolume, error) {
	var volume v1beta1.ApplicationVolume
	volume.Name = name

	mountVolumeProvider := v1beta1.MountVolumeProviderSpec{
		Mount: v1beta1.VolumeMount{
			Path: mountPath,
		},
	}

	err := volume.FromMountVolumeProviderSpec(mountVolumeProvider)
	return volume, err
}

// UpdateDeviceAndWaitForVersion updates the device and waits for the new rendered version
func (h *Harness) UpdateDeviceAndWaitForVersion(deviceID string, updateFunc func(device *v1beta1.Device)) error {
	newRenderedVersion, err := h.PrepareNextDeviceVersion(deviceID)
	if err != nil {
		return fmt.Errorf("failed to prepare next device version: %w", err)
	}

	err = h.UpdateDeviceWithRetries(deviceID, updateFunc)
	if err != nil {
		return fmt.Errorf("failed to update device: %w", err)
	}

	GinkgoWriter.Printf("Waiting for device to pick up config version %d\n", newRenderedVersion)
	err = h.WaitForDeviceNewRenderedVersion(deviceID, newRenderedVersion)
	if err != nil {
		return fmt.Errorf("failed to wait for new rendered version: %w", err)
	}

	return nil
}
