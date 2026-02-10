package e2e

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	// Quadlet unit path on device (rootful)
	QuadletUnitPath = "/etc/containers/systemd"
)

// QuadletPathForUser returns the quadlet systemd path for the given user.
// Empty or "root" returns the root path; any other user returns the user's config path.
func QuadletPathForUser(user string) string {
	if user == "" || user == "root" {
		return QuadletUnitPath
	}
	return fmt.Sprintf("/home/%s/.config/containers/systemd", user)
}

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

// VerifyQuadletApplicationFolderExists checks that the application folder exists at the root quadlet path.
func (h *Harness) VerifyQuadletApplicationFolderExists(appName string) {
	h.VerifyQuadletApplicationFolderExistsAt(appName, QuadletUnitPath)
}

// VerifyQuadletApplicationFolderExistsAt checks that the application folder exists at the given base path.
func (h *Harness) VerifyQuadletApplicationFolderExistsAt(appName, basePath string) {
	appPath := fmt.Sprintf("%s/%s", basePath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"sudo", "test", "-d", appPath}, nil)
		return err
	}, TIMEOUT, POLLING).Should(Succeed())
}

// VerifyQuadletApplicationFolderDeleted checks that the application folder was removed from the root quadlet path.
func (h *Harness) VerifyQuadletApplicationFolderDeleted(appName string) {
	h.VerifyQuadletApplicationFolderDeletedAt(appName, QuadletUnitPath)
}

// VerifyQuadletApplicationFolderDeletedAt checks that the application folder does not exist at the given base path.
func (h *Harness) VerifyQuadletApplicationFolderDeletedAt(appName, basePath string) {
	appPath := fmt.Sprintf("%s/%s", basePath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"sudo", "test", "!", "-d", appPath}, nil)
		return err
	}, TIMEOUT, POLLING).Should(Succeed())
}

// PathExistsOnDevice checks that the given path exists on the device (file or directory).
// Callers should assert the returned error (e.g. Expect(err).ToNot(HaveOccurred())).
func (h *Harness) PathExistsOnDevice(path string) error {
	_, err := h.VM.RunSSH([]string{"sudo", "test", "-e", path}, nil)
	return err
}

// PathDoesNotExistOnDevice checks that the given path does not exist on the device.
// Callers should assert the returned error (e.g. Expect(err).ToNot(HaveOccurred())).
func (h *Harness) PathDoesNotExistOnDevice(path string) error {
	_, err := h.VM.RunSSH([]string{"sudo", "test", "!", "-e", path}, nil)
	return err
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

// buildInlineContent builds ApplicationContent slice from paths and contents (contents may be shorter; missing get "").
func buildInlineContent(paths, contents []string) []v1beta1.ApplicationContent {
	inline := make([]v1beta1.ApplicationContent, len(paths))
	for i := range paths {
		c := ""
		if i < len(contents) {
			c = contents[i]
		}
		inline[i] = v1beta1.ApplicationContent{Path: paths[i], Content: &c}
	}
	return inline
}

// newQuadletSpecFromInline builds an ApplicationProviderSpec for a quadlet app from inline content.
// envVars and volumes are optional (nil means not set).
func newQuadletSpecFromInline(name, runAs string, inline []v1beta1.ApplicationContent, envVars *map[string]string, volumes *[]v1beta1.ApplicationVolume) (v1beta1.ApplicationProviderSpec, error) {
	app := v1beta1.QuadletApplication{
		Name:    lo.ToPtr(name),
		AppType: v1beta1.AppTypeQuadlet,
		EnvVars: envVars,
		Volumes: volumes,
	}
	if runAs != "" {
		app.RunAs = v1beta1.Username(runAs)
	}
	if err := app.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{Inline: inline}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromQuadletApplication(app); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}

// NewQuadletInlineSpec builds an ApplicationProviderSpec for a quadlet app from inline paths and contents.
// If runAs is non-empty, the application runs as that user (rootless); otherwise as root.
func NewQuadletInlineSpec(name, runAs string, paths, contents []string) (v1beta1.ApplicationProviderSpec, error) {
	return newQuadletSpecFromInline(name, runAs, buildInlineContent(paths, contents), nil, nil)
}

// NewQuadletInlineSpecWithEnvs builds an ApplicationProviderSpec for a quadlet app with inline content and env vars.
func NewQuadletInlineSpecWithEnvs(name, runAs string, envVars map[string]string, paths, contents []string) (v1beta1.ApplicationProviderSpec, error) {
	return newQuadletSpecFromInline(name, runAs, buildInlineContent(paths, contents), &envVars, nil)
}

// NewQuadletInlineSpecWithVolumes builds an ApplicationProviderSpec for a quadlet app with inline content and optional volumes.
func NewQuadletInlineSpecWithVolumes(name, runAs string, volumes *[]v1beta1.ApplicationVolume, paths, contents []string) (v1beta1.ApplicationProviderSpec, error) {
	return newQuadletSpecFromInline(name, runAs, buildInlineContent(paths, contents), nil, volumes)
}

// NewImageVolume returns an ApplicationVolume backed by an image (for use in quadlet/compose apps).
func NewImageVolume(name, imageRef string) (v1beta1.ApplicationVolume, error) {
	var vol v1beta1.ApplicationVolume
	vol.Name = name
	err := vol.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{Reference: imageRef, PullPolicy: lo.ToPtr(v1beta1.PullIfNotPresent)},
	})
	return vol, err
}

// NewComposeInlineSpec builds an ApplicationProviderSpec for a compose app from a single inline file.
// runAs is accepted for API compatibility but ComposeApplication has no RunAs field; compose agent runs as root.
func NewComposeInlineSpec(name, path, content, runAs string) (v1beta1.ApplicationProviderSpec, error) {
	_ = runAs // reserved for when ComposeApplication supports RunAs
	inline := []v1beta1.ApplicationContent{{Path: path, Content: &content}}
	app := v1beta1.ComposeApplication{
		Name:    lo.ToPtr(name),
		AppType: v1beta1.AppTypeCompose,
	}
	if err := app.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{Inline: inline}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromComposeApplication(app); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}

// NewContainerApplicationSpec creates a ContainerApplication spec with the given parameters (runs as root).
func NewContainerApplicationSpec(
	name string,
	image string,
	ports []v1beta1.ApplicationPort,
	cpu, memory *string,
	volumes *[]v1beta1.ApplicationVolume,
) (v1beta1.ApplicationProviderSpec, error) {
	return NewContainerApplicationSpecWithRunAs(name, image, ports, cpu, memory, volumes, "")
}

// NewContainerApplicationSpecWithRunAs creates a ContainerApplication spec with the given parameters and optional runAs user.
// If runAs is empty, the application runs as root (same as NewContainerApplicationSpec).
func NewContainerApplicationSpecWithRunAs(
	name string,
	image string,
	ports []v1beta1.ApplicationPort,
	cpu, memory *string,
	volumes *[]v1beta1.ApplicationVolume,
	runAs string,
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
	if runAs != "" {
		containerApp.RunAs = v1beta1.Username(runAs)
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

// InlineComposeVolume describes a volume for CreateFleetWithInlineComposeAndVolumes.
type InlineComposeVolume struct {
	Name       string
	Reference  string
	PullPolicy v1beta1.ImagePullPolicy
}

// CreateFleetWithInlineComposeAndVolumes creates or updates a fleet with a single inline compose application and optional image volumes.
func (h *Harness) CreateFleetWithInlineComposeAndVolumes(
	fleetName string,
	selectorValue string,
	appName string,
	composePath string,
	composeContent string,
	volumes []InlineComposeVolume,
) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if fleetName == "" || selectorValue == "" || appName == "" || composePath == "" || composeContent == "" {
		return fmt.Errorf("invalid fleet inputs: fleetName=%q selectorValue=%q appName=%q composePath=%q composeContentEmpty=%t",
			fleetName, selectorValue, appName, composePath, composeContent == "")
	}

	selector := v1beta1.LabelSelector{
		MatchLabels: &map[string]string{"fleet": selectorValue},
	}

	var vols []v1beta1.ApplicationVolume
	for _, v := range volumes {
		vol := v1beta1.ApplicationVolume{Name: v.Name}
		if err := vol.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
			Image: v1beta1.ImageVolumeSource{
				Reference:  v.Reference,
				PullPolicy: lo.ToPtr(v.PullPolicy),
			},
		}); err != nil {
			return err
		}
		vols = append(vols, vol)
	}

	inline := v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Path:    composePath,
				Content: lo.ToPtr(composeContent),
			},
		},
	}

	compose := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    lo.ToPtr(appName),
	}
	if len(vols) > 0 {
		compose.Volumes = &vols
	}
	if err := compose.FromInlineApplicationProviderSpec(inline); err != nil {
		return err
	}

	app := v1beta1.ApplicationProviderSpec{}
	if err := app.FromComposeApplication(compose); err != nil {
		return err
	}

	deviceSpec := v1beta1.DeviceSpec{
		Applications: &[]v1beta1.ApplicationProviderSpec{app},
	}

	return h.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
}

// RunPodmanPsContainerNames runs podman ps (or podman ps -a) on the VM as root and returns container names.
func (h *Harness) RunPodmanPsContainerNames(allContainers bool) (string, error) {
	return h.RunPodmanPsContainerNamesAsUser("root", allContainers)
}

// GetUserHomeOnVM returns the home directory of the given user on the VM (e.g. /var/lib/flightctl).
// Use it when running rootless podman over SSH so HOME points to the user's container store.
func (h *Harness) GetUserHomeOnVM(user string) (string, error) {
	return h.getUserHomeOnVM(user)
}

// QuadletPathForUserOnVM returns the quadlet systemd path for the given user using the user's
// actual home on the VM (from getent). Use this for verification when the agent writes to
// the user's home (e.g. /var/home/flightctl/.config/containers/systemd) so it works on distros
// that use /var/home/<user> instead of /home/<user>.
func (h *Harness) QuadletPathForUserOnVM(user string) (string, error) {
	home, err := h.getUserHomeOnVM(user)
	if err != nil {
		return "", err
	}
	return home + "/.config/containers/systemd", nil
}

func (h *Harness) getUserHomeOnVM(user string) (string, error) {
	out, err := h.VM.RunSSH([]string{"sudo", "getent", "passwd", user}, nil)
	if err != nil {
		return "", fmt.Errorf("getent passwd %s: %w", user, err)
	}
	line := strings.TrimSpace(out.String())
	fields := strings.Split(line, ":")
	// passwd format: name:passwd:uid:gid:gecos:home:shell; gecos may contain colons
	if len(fields) < 2 {
		return "", fmt.Errorf("getent passwd %s: unexpected output", user)
	}
	return fields[len(fields)-2], nil
}

// RunPodmanPsContainerNamesAsUser runs podman ps (or podman ps -a) on the VM as the given user and returns container names.
// Use "root" for rootful podman (same as RunPodmanPsContainerNames).
// For non-root users, runs with cwd /tmp and HOME set to the user's home so rootless podman finds the container store.
// No manual chown; the VM/agent is expected to set up the user's home with correct ownership.
func (h *Harness) RunPodmanPsContainerNamesAsUser(user string, allContainers bool) (string, error) {
	var args []string
	if user == "root" {
		args = []string{"sudo", "-u", user, "podman", "ps", "--format", "{{.Names}}"}
		if allContainers {
			args = []string{"sudo", "-u", user, "podman", "ps", "-a", "--format", "{{.Names}}"}
		}
	} else {
		home, err := h.getUserHomeOnVM(user)
		if err != nil {
			return "", err
		}
		cmd := fmt.Sprintf("cd /tmp && env HOME=%q podman ps --format '{{.Names}}'", home)
		if allContainers {
			cmd = fmt.Sprintf("cd /tmp && env HOME=%q podman ps -a --format '{{.Names}}'", home)
		}
		args = []string{"sudo", "-u", user, "sh", "-c", cmd}
	}
	out, err := h.VM.RunSSH(args, nil)
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// RunSystemctlUserStatus runs systemctl --user status for the given user on the VM.
// unitPattern is the unit name or glob pattern (e.g. "*new-rootful*"); it is passed
// quoted to systemctl so the shell does not expand it—systemctl does the matching.
// Uses cd /tmp so the target user does not inherit the SSH session cwd; XDG_RUNTIME_DIR for user systemd.
func (h *Harness) RunSystemctlUserStatus(user, unitPattern string) (string, error) {
	cmd := fmt.Sprintf("sudo -u %s sh -c 'cd /tmp && XDG_RUNTIME_DIR=/run/user/$(id -u %s) systemctl --user status %s'", user, user, unitPattern)
	out, err := h.VM.RunSSH([]string{"sh", "-c", cmd}, nil)
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// RebootVMAndWaitForSSH triggers a reboot on the VM and waits for SSH to become ready again.
func (h *Harness) RebootVMAndWaitForSSH(waitInterval time.Duration, maxAttempts int) error {
	_, _ = h.VM.RunSSH([]string{"sudo", "reboot"}, nil)
	var sshErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(waitInterval)
		sshErr = h.VM.WaitForSSHToBeReady()
		if sshErr == nil {
			return nil
		}
	}
	return fmt.Errorf("SSH did not become ready after reboot within %d attempts: %w", maxAttempts, sshErr)
}

// UpdateDeviceAndWaitForRenderedVersion updates the device with the given function and waits for the new rendered version.
func (h *Harness) UpdateDeviceAndWaitForRenderedVersion(deviceID string, updateFunc func(*v1beta1.Device)) error {
	newRenderedVersion, err := h.PrepareNextDeviceVersion(deviceID)
	if err != nil {
		return err
	}
	if err := h.UpdateDeviceWithRetries(deviceID, updateFunc); err != nil {
		return err
	}
	return h.WaitForDeviceNewRenderedVersion(deviceID, newRenderedVersion)
}

// ClearDeviceApplications is a device update callback that removes all applications from the device spec.
func ClearDeviceApplications(device *v1beta1.Device) {
	device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
}

// SetDeviceApplications sets device.Spec.Applications and waits for the new rendered version.
func (h *Harness) SetDeviceApplications(deviceID string, apps *[]v1beta1.ApplicationProviderSpec) error {
	return h.UpdateDeviceAndWaitForRenderedVersion(deviceID, func(device *v1beta1.Device) {
		device.Spec.Applications = apps
	})
}

// UpdateDeviceWithQuadletInline updates the device with an inline quadlet application (no env vars).
func (h *Harness) UpdateDeviceWithQuadletInline(deviceID, appName string, paths, contents []string) error {
	return h.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, appName, "", paths, contents)
}

// UpdateDeviceWithQuadletInlineAndRunAs updates the device with an inline quadlet application and optional runAs user.
// If runAs is empty, the application runs as root (default). Otherwise it runs as the given user (e.g. "flightctl").
func (h *Harness) UpdateDeviceWithQuadletInlineAndRunAs(deviceID, appName, runAs string, paths, contents []string) error {
	if len(paths) == 0 && len(contents) == 0 {
		return h.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{})
	}
	spec, err := NewQuadletInlineSpec(appName, runAs, paths, contents)
	if err != nil {
		return err
	}
	return h.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{spec})
}

// UpdateDeviceWithQuadletInlineAndEnvs updates the device with an inline quadlet application and env vars.
func (h *Harness) UpdateDeviceWithQuadletInlineAndEnvs(deviceID, appName string, envVars map[string]string, paths, contents []string) error {
	return h.UpdateDeviceWithQuadletInlineAndEnvsAndRunAs(deviceID, appName, "", envVars, paths, contents)
}

// UpdateDeviceWithQuadletInlineAndEnvsAndRunAs updates the device with an inline quadlet application, env vars, and optional runAs user.
func (h *Harness) UpdateDeviceWithQuadletInlineAndEnvsAndRunAs(deviceID, appName, runAs string, envVars map[string]string, paths, contents []string) error {
	spec, err := NewQuadletInlineSpecWithEnvs(appName, runAs, envVars, paths, contents)
	if err != nil {
		return err
	}
	return h.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{spec})
}

// QuadletImageAppSpec builds an ApplicationProviderSpec for a quadlet app from an image and env vars.
func (h *Harness) QuadletImageAppSpec(name, image string, envVars map[string]string) (v1beta1.ApplicationProviderSpec, error) {
	quadletApp := v1beta1.QuadletApplication{
		Name:    &name,
		AppType: v1beta1.AppTypeQuadlet,
		EnvVars: &envVars,
	}
	if err := quadletApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromQuadletApplication(quadletApp); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}

func deviceHasApplicationWithStatus(device *v1beta1.Device, appName string, status v1beta1.ApplicationStatusType) bool {
	if device == nil || device.Status == nil {
		return false
	}
	for _, app := range device.Status.Applications {
		if app.Name == appName && app.Status == status {
			return true
		}
	}
	return false
}

func deviceApplicationsSummaryStatus(device *v1beta1.Device) string {
	if device == nil || device.Status == nil {
		return ""
	}
	return string(device.Status.ApplicationsSummary.Status)
}

// WaitForApplicationStatus polls until the device reports the given application with the given status, or timeout.
func (h *Harness) WaitForApplicationStatus(deviceID, appName string, status v1beta1.ApplicationStatusType, timeout, polling time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil {
			logrus.Debugf("WaitForApplicationStatus GetDeviceWithStatusSystem: %v", err)
			time.Sleep(polling)
			continue
		}
		if resp.JSON200 != nil && deviceHasApplicationWithStatus(resp.JSON200, appName, status) {
			return nil
		}
		time.Sleep(polling)
	}
	return fmt.Errorf("timed out after %s waiting for application %s to have status %s", timeout, appName, status)
}

// WaitForApplicationSummaryDegradedOrError polls until the device's applications summary is Degraded or Error, or timeout.
func (h *Harness) WaitForApplicationSummaryDegradedOrError(deviceID string, timeout, polling time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil {
			logrus.Debugf("WaitForApplicationSummaryDegradedOrError GetDeviceWithStatusSystem: %v", err)
			time.Sleep(polling)
			continue
		}
		if resp.JSON200 != nil {
			s := deviceApplicationsSummaryStatus(resp.JSON200)
			if s == string(v1beta1.ApplicationsSummaryStatusDegraded) || s == string(v1beta1.ApplicationsSummaryStatusError) {
				return nil
			}
		}
		time.Sleep(polling)
	}
	return fmt.Errorf("timed out after %s waiting for applications summary to be Degraded or Error", timeout)
}
