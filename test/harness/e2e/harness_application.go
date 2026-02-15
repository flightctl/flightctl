package e2e

import (
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/sirupsen/logrus"
)

// RunPodmanPsContainerNames runs podman ps (or podman ps -a) on the VM and returns container names.
func (h *Harness) RunPodmanPsContainerNames(allContainers bool) (string, error) {
	args := []string{"sudo", "podman", "ps", "--format", "{{.Names}}"}
	if allContainers {
		args = []string{"sudo", "podman", "ps", "-a", "--format", "{{.Names}}"}
	}
	out, err := h.VM.RunSSH(args, nil)
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
	if len(paths) == 0 && len(contents) == 0 {
		return h.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{})
	}
	inline := make([]v1beta1.ApplicationContent, len(paths))
	for i := range paths {
		c := ""
		if i < len(contents) {
			c = contents[i]
		}
		inline[i] = v1beta1.ApplicationContent{Path: paths[i], Content: &c}
	}
	quadletApp := v1beta1.QuadletApplication{
		Name:    &appName,
		AppType: v1beta1.AppTypeQuadlet,
	}
	if err := quadletApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{Inline: inline}); err != nil {
		return err
	}
	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromQuadletApplication(quadletApp); err != nil {
		return err
	}
	return h.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{spec})
}

// UpdateDeviceWithQuadletInlineAndEnvs updates the device with an inline quadlet application and env vars.
func (h *Harness) UpdateDeviceWithQuadletInlineAndEnvs(deviceID, appName string, envVars map[string]string, paths, contents []string) error {
	inline := make([]v1beta1.ApplicationContent, len(paths))
	for i := range paths {
		c := ""
		if i < len(contents) {
			c = contents[i]
		}
		inline[i] = v1beta1.ApplicationContent{Path: paths[i], Content: &c}
	}
	quadletApp := v1beta1.QuadletApplication{
		Name:    &appName,
		AppType: v1beta1.AppTypeQuadlet,
		EnvVars: &envVars,
	}
	if err := quadletApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{Inline: inline}); err != nil {
		return err
	}
	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromQuadletApplication(quadletApp); err != nil {
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
