package e2e

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/sirupsen/logrus"
)

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
