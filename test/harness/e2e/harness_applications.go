package e2e

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	// Quadlet unit path on device
	QuadletUnitPath = "/etc/containers/systemd"
)

func (h *Harness) UpdateApplication(withRetries bool, deviceId string, appName string, appProvider any, envVars map[string]string) error {
	logrus.Infof("UpdateApplication called with deviceId=%s, appName=%s, withRetries=%v", deviceId, appName, withRetries)

	updateFunc := func(device *v1beta1.Device) {
		logrus.Infof("Starting update for device: %s", *device.Metadata.Name)

		// Build the ComposeApplication with name and envVars
		composeApp := v1beta1.ComposeApplication{
			AppType: v1beta1.AppTypeCompose,
			Name:    &appName,
		}

		if envVars != nil {
			logrus.Infof("Setting environment variables for app %s: %v", appName, envVars)
			composeApp.EnvVars = &envVars
		}

		// Set the image/inline union on the ComposeApplication
		var err error
		switch spec := appProvider.(type) {
		case v1beta1.InlineApplicationProviderSpec:
			logrus.Infof("Processing InlineApplicationProviderSpec for %s", appName)
			err = composeApp.FromInlineApplicationProviderSpec(spec)
		case v1beta1.ImageApplicationProviderSpec:
			logrus.Infof("Processing ImageApplicationProviderSpec for %s", appName)
			err = composeApp.FromImageApplicationProviderSpec(spec)
		default:
			logrus.Errorf("Unsupported application provider type: %T for %s", appProvider, appName)
			return
		}

		if err != nil {
			logrus.Errorf("Error converting application provider spec: %v", err)
			return
		}

		// Create the ApplicationProviderSpec from the ComposeApplication
		var appSpec v1beta1.ApplicationProviderSpec
		if err := appSpec.FromComposeApplication(composeApp); err != nil {
			logrus.Errorf("Error creating ApplicationProviderSpec: %v", err)
			return
		}

		if device.Spec.Applications == nil {
			logrus.Infof("device.Spec.Applications is nil, initializing with app %s", appName)
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
			return
		}

		for i, a := range *device.Spec.Applications {
			existingName, _ := a.GetName()
			if existingName != nil && *existingName == appName {
				logrus.Infof("Updating existing application %s at index %d", appName, i)
				(*device.Spec.Applications)[i] = appSpec
				return
			}
		}

		logrus.Infof("Appending new application %s to device %s", appName, *device.Metadata.Name)
		*device.Spec.Applications = append(*device.Spec.Applications, appSpec)
	}

	if withRetries {
		logrus.Info("Updating device with retries...")
		return h.UpdateDeviceWithRetries(deviceId, updateFunc)
	}

	logrus.Info("Updating device without retries...")
	return h.UpdateDevice(deviceId, updateFunc)
}

// WaitForApplicationRunningStatus waits for a specific application on a device to reach the "Running" status with all
// expected workloads running within a timeout.
func (h *Harness) WaitForApplicationRunningStatus(deviceId string, applicationImage string) {
	h.WaitForApplicationStatusByName(deviceId, applicationImage, v1beta1.ApplicationStatusRunning)
}

// waitForApplicationsSummaryCondition is an internal helper that waits for the ApplicationsSummary status
// to satisfy the given condition.
func (h *Harness) waitForApplicationsSummaryCondition(deviceID string, description string, condition func(v1beta1.ApplicationsSummaryStatusType) bool) {
	GinkgoWriter.Printf("Waiting for applications summary: %s (device=%s)\n", description, deviceID)
	Eventually(func() bool {
		response, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil || response == nil || response.JSON200 == nil {
			return false
		}
		device := response.JSON200
		if device.Status == nil {
			return false
		}
		if device.Status.ApplicationsSummary.Status == "" {
			return false
		}
		return condition(device.Status.ApplicationsSummary.Status)
	}, TIMEOUT, POLLING).Should(BeTrue())
}

// WaitForApplicationsSummaryNotHealthy waits until applications summary status is set and not Healthy.
func (h *Harness) WaitForApplicationsSummaryNotHealthy(deviceID string) {
	h.waitForApplicationsSummaryCondition(deviceID, "not healthy", func(status v1beta1.ApplicationsSummaryStatusType) bool {
		return status != v1beta1.ApplicationsSummaryStatusHealthy
	})
}

// WaitForApplicationStatusByName waits for an application to reach the specified status.
// For Running status, it also verifies ready replicas match.
func (h *Harness) WaitForApplicationStatusByName(deviceId string, applicationName string, expectedStatus v1beta1.ApplicationStatusType) {
	GinkgoWriter.Printf("Waiting for application %s to reach %s status\n", applicationName, expectedStatus)
	h.WaitForDeviceContents(deviceId, fmt.Sprintf("Application %s", expectedStatus),
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil || device.Status.Applications == nil {
				return false
			}
			for _, application := range device.Status.Applications {
				if application.Name == applicationName && application.Status == expectedStatus {
					// For Running status, also verify ready replicas
					if expectedStatus == v1beta1.ApplicationStatusRunning {
						parts := strings.Split(application.Ready, "/")
						if len(parts) != 2 {
							return false
						}
						return parts[0] == parts[1]
					}
					return true
				}
			}
			return false
		}, TIMEOUT)
}

// WaitForApplicationsCount waits for the total number of applications to match the expected count
func (h *Harness) WaitForApplicationsCount(deviceId string, expectedCount int) {
	GinkgoWriter.Printf("Waiting for %d total applications\n", expectedCount)
	Eventually(func() int {
		response, err := h.GetDeviceWithStatusSystem(deviceId)
		if err != nil {
			return -1
		}
		if response.JSON200 == nil || response.JSON200.Status == nil {
			return -1
		}
		return len(response.JSON200.Status.Applications)
	}, TIMEOUT, POLLING).Should(Equal(expectedCount))
}

// WaitForRunningApplicationsCount waits for a specific number of applications to reach Running status
func (h *Harness) WaitForRunningApplicationsCount(deviceId string, expectedCount int) {
	GinkgoWriter.Printf("Waiting for %d applications to reach Running status\n", expectedCount)
	Eventually(func() int {
		response, err := h.GetDeviceWithStatusSystem(deviceId)
		if err != nil {
			return -1
		}
		if response.JSON200 == nil || response.JSON200.Status == nil {
			return -1
		}
		runningCount := 0
		for _, app := range response.JSON200.Status.Applications {
			if app.Status == v1beta1.ApplicationStatusRunning {
				runningCount++
			}
		}
		return runningCount
	}, TIMEOUT, POLLING).Should(Equal(expectedCount))
}

// WaitForApplicationsSummaryStatus waits for the device's ApplicationsSummary status to match the expected status
func (h *Harness) WaitForApplicationsSummaryStatus(deviceId string, expectedStatus v1beta1.ApplicationsSummaryStatusType) {
	h.waitForApplicationsSummaryCondition(deviceId, string(expectedStatus), func(status v1beta1.ApplicationsSummaryStatusType) bool {
		return status == expectedStatus
	})
}

// WaitForNoApplications waits for the device to have no applications in status
func (h *Harness) WaitForNoApplications(deviceId string) {
	h.WaitForApplicationsCount(deviceId, 0)
}

// VerifyContainerRunning checks that podman shows containers running with the given image substring
func (h *Harness) VerifyContainerRunning(imageSubstring string) {
	Eventually(func() error {
		stdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "{{.Image}}"}, nil)
		if err != nil {
			return err
		}
		if !strings.Contains(stdout.String(), imageSubstring) {
			return fmt.Errorf("container with image %q not found in running containers: %s", imageSubstring, stdout.String())
		}
		return nil
	}, TIMEOUT, POLLING).Should(Succeed())
}

// VerifyQuadletApplicationFolderExists checks that the application folder exists
func (h *Harness) VerifyQuadletApplicationFolderExists(appName string) {
	appPath := fmt.Sprintf("%s/%s", QuadletUnitPath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"test", "-d", appPath}, nil)
		return err
	}, TIMEOUT, POLLING).Should(Succeed())
}

// VerifyQuadletApplicationFolderDeleted checks that the application folder was removed
func (h *Harness) VerifyQuadletApplicationFolderDeleted(appName string) {
	appPath := fmt.Sprintf("%s/%s", QuadletUnitPath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"test", "!", "-d", appPath}, nil)
		return err
	}, TIMEOUT, POLLING).Should(Succeed())
}

// VerifyQuadletPodmanArgs verifies that a quadlet file contains the expected PodmanArgs entry
func (h *Harness) VerifyQuadletPodmanArgs(appName, flag, expectedValue string) {
	appPath := fmt.Sprintf("%s/%s", QuadletUnitPath, appName)
	expectedArg := fmt.Sprintf("PodmanArgs=%s %s", flag, expectedValue)

	GinkgoWriter.Printf("Verifying quadlet file contains %q for app %s\n", expectedArg, appName)

	Eventually(func() bool {
		// Find the .container file in the app directory
		findStdout, err := h.VM.RunSSH([]string{"sudo", "find", appPath, "-name", "*.container", "-type", "f"}, nil)
		if err != nil {
			GinkgoWriter.Printf("Error finding container file: %v\n", err)
			return false
		}

		containerFile := strings.TrimSpace(findStdout.String())
		if containerFile == "" {
			GinkgoWriter.Printf("No .container file found in %s\n", appPath)
			return false
		}

		// Handle multiple files - take the first one
		files := strings.Split(containerFile, "\n")
		containerFile = strings.TrimSpace(files[0])

		GinkgoWriter.Printf("Reading quadlet file: %s\n", containerFile)

		// Read the container file contents
		catStdout, err := h.VM.RunSSH([]string{"sudo", "cat", containerFile}, nil)
		if err != nil {
			GinkgoWriter.Printf("Error reading container file: %v\n", err)
			return false
		}

		contents := catStdout.String()
		if strings.Contains(contents, expectedArg) {
			GinkgoWriter.Printf("Found expected PodmanArgs in quadlet file\n")
			return true
		}

		GinkgoWriter.Printf("Expected %q not found in quadlet file contents:\n%s\n", expectedArg, contents)
		return false
	}, TIMEOUT, POLLING).Should(BeTrue(), "Expected quadlet file to contain %q", expectedArg)
}

// GetContainerPorts returns the port mappings for running containers
func (h *Harness) GetContainerPorts() (string, error) {
	stdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "{{.Ports}}"}, nil)
	if err != nil {
		GinkgoWriter.Printf("Error getting container ports: %v\n", err)
		return "", err
	}
	return stdout.String(), nil
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

// BuildComposeWithImageVolumeSpec builds a compose ApplicationProviderSpec with inline compose content
// and a single image-backed application volume.
func BuildComposeWithImageVolumeSpec(appName, composePath, composeContent, volumeName, imageRef string) (v1beta1.ApplicationProviderSpec, error) {
	volume := v1beta1.ApplicationVolume{
		Name:          volumeName,
		ReclaimPolicy: lo.ToPtr(v1beta1.Retain),
	}
	if err := volume.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{
			Reference:  imageRef,
			PullPolicy: lo.ToPtr(v1beta1.PullIfNotPresent),
		},
	}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}

	compose := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    lo.ToPtr(appName),
		Volumes: &[]v1beta1.ApplicationVolume{volume},
	}
	if err := compose.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Path:    composePath,
				Content: lo.ToPtr(composeContent),
			},
		},
	}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}

	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromComposeApplication(compose); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}
