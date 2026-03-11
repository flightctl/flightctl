package e2e

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

func (h *Harness) GetDeviceWithStatusSystem(enrollmentID string) (*apiclient.GetDeviceResponse, error) {
	device, err := h.Client.GetDeviceWithResponse(h.Context, enrollmentID)
	if err != nil {
		return nil, err
	}
	// we keep waiting for a 200 response, with filled in Status.SystemInfo
	if device.JSON200 == nil || device.JSON200.Status == nil || device.JSON200.Status.SystemInfo.IsEmpty() {
		return nil, nil
	}
	return device, nil
}

// GetDeviceSystemInfo returns the device system info with proper error handling
func (h *Harness) GetDeviceSystemInfo(deviceID string) *v1beta1.DeviceSystemInfo {
	resp, err := h.GetDeviceWithStatusSystem(deviceID)
	if err != nil || resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil {
		return nil
	}
	return &resp.JSON200.Status.SystemInfo
}

func (h *Harness) GetDeviceWithStatusSummary(enrollmentID string) (v1beta1.DeviceSummaryStatusType, error) {
	device, err := h.Client.GetDeviceWithResponse(h.Context, enrollmentID)
	if err != nil {
		return "", err
	}
	// we keep waiting for a 200 response, with filled in Status.SystemInfo
	if device == nil || device.JSON200 == nil || device.JSON200.Status == nil || device.JSON200.Status.Summary.Status == "" {
		return "", nil
	}
	return device.JSON200.Status.Summary.Status, nil
}

func (h *Harness) GetDeviceWithUpdateStatus(enrollmentID string) (v1beta1.DeviceUpdatedStatusType, error) {
	device, err := h.Client.GetDeviceWithResponse(h.Context, enrollmentID)
	if err != nil {
		return "", err
	}
	// we keep waiting for a 200 response, with filled in Status.SystemInfo
	if device == nil || device.JSON200 == nil || device.JSON200.Status == nil {
		return "", nil
	}
	return device.JSON200.Status.Updated.Status, nil
}

func (h *Harness) UpdateDeviceWithRetries(deviceId string, updateFunction func(*v1beta1.Device)) error {
	// this needs to be changed so that we don't use Eventually as our polling mechanism so that we can simply return
	// the underlying error if one exists
	updateResourceWithRetries(func() error {
		return h.UpdateDevice(deviceId, updateFunction)
	})
	return nil
}

func (h *Harness) UpdateDevice(deviceId string, updateFunction func(*v1beta1.Device)) error {
	response, err := h.Client.GetDeviceWithResponse(h.Context, deviceId)
	if err != nil {
		return err
	}
	if response.JSON200 == nil {
		logrus.Errorf("An error happened retrieving device: %+v", response)
		return fmt.Errorf("device %s not found: %v", deviceId, response.Status())
	}
	device := response.JSON200

	updateFunction(device)

	resp, err := h.Client.ReplaceDeviceWithResponse(h.Context, deviceId, *device)
	if err != nil {
		logrus.Errorf("Unexpected error updating device %s: %v", deviceId, err)
		return err
	}

	// if a conflict happens (the device updated status or object since we read it) we retry
	if resp.JSON409 != nil {
		logrus.Warningf("Conflict updating device %s: %+v", deviceId, resp.JSON409)
	}

	// response code 200 = updated, we are expecting to update... something else is unexpected
	if resp.StatusCode() != 200 {
		logrus.Errorf("Unexpected http status code received: %d", resp.StatusCode())
		logrus.Errorf("Unexpected http response: %s", string(resp.Body))
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode(), string(resp.Body))
	}

	return nil
}

// IsDeviceUpdateObserved returns true if the device is updating or has already updated to the expected version.
func IsDeviceUpdateObserved(device *v1beta1.Device, expectedVersion int) bool {
	version, err := GetRenderedVersion(device)
	if err != nil {
		rendered := "<nil>"
		if device != nil && device.Status != nil && device.Status.Config != (v1beta1.DeviceConfigStatus{}) {
			rendered = device.Status.Config.RenderedVersion
		}
		GinkgoWriter.Printf("Failed to parse rendered version '%s': %v\n", rendered, err)
		return false
	}

	if device == nil || device.Status == nil {
		return false
	}

	// The update has already applied
	if version == expectedVersion {
		return true
	}
	cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
	if cond == nil {
		return false
	}
	// send another update if we're in this state
	validReasons := []v1beta1.UpdateState{
		v1beta1.UpdateStatePreparing,
		v1beta1.UpdateStateReadyToUpdate,
		v1beta1.UpdateStateApplyingUpdate,
	}
	return slices.Contains(validReasons, v1beta1.UpdateState(cond.Reason))
}

// SetDeviceConfig returns a device update callback that replaces the device config with the given specs.
func SetDeviceConfig(configs ...v1beta1.ConfigProviderSpec) func(*v1beta1.Device) {
	return func(device *v1beta1.Device) {
		device.Spec.Config = &configs
	}
}

// IsDeviceUpToDate returns true if the device status indicates it is up to date.
func IsDeviceUpToDate(device *v1beta1.Device) bool {
	return device != nil && device.Status != nil &&
		device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
}

// IsUpdatingConditionCleared returns true if the device has no Updating condition in error state (Reason != "Error").
func IsUpdatingConditionCleared(device *v1beta1.Device) bool {
	if device == nil || device.Status == nil {
		return false
	}
	cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
	return cond == nil || cond.Reason != "Error"
}

func (h *Harness) UpdateDeviceAndWait(deviceID string, updateFunc func(device *v1beta1.Device)) error {
	GinkgoWriter.Printf("Preparing device update (device=%s)\n", deviceID)
	newRenderedVersion, err := h.PrepareNextDeviceVersion(deviceID)
	if err != nil {
		return err
	}

	err = h.UpdateDeviceWithRetries(deviceID, updateFunc)
	if err != nil {
		return err
	}

	GinkgoWriter.Printf("Waiting for device to pick config (device=%s)\n", deviceID)
	return h.WaitForDeviceNewRenderedVersion(deviceID, newRenderedVersion)
}

func (h *Harness) ExtractSingleContainerNameFromVM() (string, error) {
	GinkgoWriter.Printf("Extracting container name from VM\n")
	cmd := "sudo podman ps --format \"{{.Names}}\" | head -n 1"
	containerName, err := h.VM.RunSSH(vmShellCommandArgs(cmd), nil)
	if err != nil {
		return "", err
	}
	containerNameString := ""
	if containerName == nil {
		GinkgoWriter.Printf("Container name output is nil for command: %s\n", cmd)
	} else {
		containerNameString = strings.Trim(containerName.String(), "\n")
	}
	if strings.TrimSpace(containerNameString) == "" {
		_, _ = h.VM.RunSSH(vmShellCommandArgs("sudo podman ps -a --format '{{.Names}} {{.Status}}'"), nil)
		return "", fmt.Errorf("no container name found (command=%q)", cmd)
	}
	GinkgoWriter.Printf("Found container name: %s\n", containerNameString)
	return containerNameString, nil
}

func (h *Harness) VerifyContainerCount(count int) error {
	GinkgoWriter.Printf("Verifying container count (expected=%d)\n", count)
	out, err := h.CheckRunningContainers()
	if err != nil {
		return err
	}
	actualCount, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return fmt.Errorf("parsing container count from %q: %w", out, err)
	}
	if actualCount != count {
		return fmt.Errorf("container count mismatch: expected %d, got %d", count, actualCount)
	}
	return nil
}

func (h *Harness) VerifyCommandOutputsSubstring(args []string, s string) error {
	GinkgoWriter.Printf("Verifying command output contains substring: %s\n", s)
	stdout, err := h.VM.RunSSH(args, nil)
	if err != nil {
		return err
	}
	if stdout == nil {
		return errors.New("command output is nil")
	}
	if !strings.Contains(stdout.String(), s) {
		return fmt.Errorf("command output missing substring %q: %s", s, stdout.String())
	}
	return nil
}

func (h *Harness) VerifyCommandLacksSubstring(args []string, s string) error {
	GinkgoWriter.Printf("Verifying command output lacks substring: %s\n", s)
	stdout, err := h.VM.RunSSH(args, nil)
	if err != nil {
		return err
	}
	if stdout == nil {
		return errors.New("command output is nil")
	}
	if strings.Contains(stdout.String(), s) {
		return fmt.Errorf("command output unexpectedly contains substring %q: %s", s, stdout.String())
	}
	return nil
}

type InlineContent struct {
	Path    string
	Content string
}

func BuildInlineAppSpec(appName string, appType v1beta1.AppType, contents []InlineContent) (v1beta1.ApplicationProviderSpec, error) {
	inline := make([]v1beta1.ApplicationContent, 0, len(contents))
	for _, c := range contents {
		content := c.Content
		inline = append(inline, v1beta1.ApplicationContent{
			Path:    c.Path,
			Content: &content,
		})
	}
	return BuildAppSpec(appName, appType, v1beta1.InlineApplicationProviderSpec{Inline: inline})
}

func BuildImageAppSpec(appName string, appType v1beta1.AppType, image string) (v1beta1.ApplicationProviderSpec, error) {
	return BuildAppSpec(appName, appType, v1beta1.ImageApplicationProviderSpec{Image: image})
}

func BuildAppSpec(appName string, appType v1beta1.AppType, provider any) (v1beta1.ApplicationProviderSpec, error) {
	switch appType {
	case v1beta1.AppTypeCompose:
		app := v1beta1.ComposeApplication{
			AppType: appType,
			Name:    &appName,
		}
		if err := applyAppProvider(&app, provider); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		var appSpec v1beta1.ApplicationProviderSpec
		if err := appSpec.FromComposeApplication(app); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		return appSpec, nil
	case v1beta1.AppTypeQuadlet:
		app := v1beta1.QuadletApplication{
			AppType: appType,
			Name:    &appName,
		}
		if err := applyAppProvider(&app, provider); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		var appSpec v1beta1.ApplicationProviderSpec
		if err := appSpec.FromQuadletApplication(app); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		return appSpec, nil
	case v1beta1.AppTypeContainer:
		imageSpec, ok := provider.(v1beta1.ImageApplicationProviderSpec)
		if !ok {
			return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("container app requires ImageApplicationProviderSpec")
		}
		app := v1beta1.ContainerApplication{
			AppType: appType,
			Name:    &appName,
			Image:   imageSpec.Image,
		}
		var appSpec v1beta1.ApplicationProviderSpec
		if err := appSpec.FromContainerApplication(app); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		return appSpec, nil
	case v1beta1.AppTypeHelm:
		imageSpec, ok := provider.(v1beta1.ImageApplicationProviderSpec)
		if !ok {
			return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("helm app requires ImageApplicationProviderSpec")
		}
		app := v1beta1.HelmApplication{
			AppType: appType,
			Name:    &appName,
			Image:   imageSpec.Image,
		}
		var appSpec v1beta1.ApplicationProviderSpec
		if err := appSpec.FromHelmApplication(app); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		return appSpec, nil
	default:
		return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("unsupported app type %s", appType)
	}
}

func applyAppProvider(target any, provider any) error {
	switch app := target.(type) {
	case *v1beta1.ComposeApplication:
		switch spec := provider.(type) {
		case v1beta1.InlineApplicationProviderSpec:
			return app.FromInlineApplicationProviderSpec(spec)
		case v1beta1.ImageApplicationProviderSpec:
			return app.FromImageApplicationProviderSpec(spec)
		default:
			return fmt.Errorf("unsupported application provider type %T", provider)
		}
	case *v1beta1.QuadletApplication:
		switch spec := provider.(type) {
		case v1beta1.InlineApplicationProviderSpec:
			return app.FromInlineApplicationProviderSpec(spec)
		case v1beta1.ImageApplicationProviderSpec:
			return app.FromImageApplicationProviderSpec(spec)
		default:
			return fmt.Errorf("unsupported application provider type %T", provider)
		}
	default:
		return fmt.Errorf("unsupported application target type %T", target)
	}
}

func (h *Harness) fetchDeviceContents(deviceId string) (*v1beta1.Device, error) {
	response, err := h.Client.GetDeviceWithResponse(h.Context, deviceId)
	if err != nil {
		return nil, err
	}
	if response.JSON200 == nil {
		logrus.Errorf("An error happened retrieving device: %+v", response)
		return nil, errors.New("device not found???")
	}
	return response.JSON200, nil
}

func (h *Harness) WaitForDeviceContents(deviceId string, description string, condition func(*v1beta1.Device) bool, timeout string) {
	waitForResourceContents(deviceId, description, func(id string) (*v1beta1.Device, error) {
		return h.fetchDeviceContents(id)
	}, condition, timeout)
}

// EnsureDeviceContents ensures that the contents of the device match the specified condition for the entire timeout
func (h *Harness) EnsureDeviceContents(deviceId string, description string, condition func(*v1beta1.Device) bool, timeout string) {
	ensureResourceContents(deviceId, description, func(id string) (*v1beta1.Device, error) {
		return h.fetchDeviceContents(id)
	}, condition, timeout)
}

func (h *Harness) WaitForBootstrapAndUpdateToVersion(deviceId string, version string) (*v1beta1.Device, util.ImageReference, error) {
	var imageReference = util.ImageReference{}
	// Check the device status right after bootstrap
	response, err := h.GetDeviceWithStatusSystem(deviceId)
	if err != nil {
		return nil, imageReference, err
	}
	device := response.JSON200
	if device.Status.Summary.Status != v1beta1.DeviceSummaryStatusOnline {
		return nil, imageReference, fmt.Errorf("device: %q is not online", deviceId)
	}

	err = h.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
		currentImage := device.Status.Os.Image
		logrus.Infof("current image for %s is %s", deviceId, currentImage)
		imageReference, err = util.NewImageReferenceFromString(currentImage)
		if err != nil {
			logrus.Errorf("failed to parse image reference %s: %v", currentImage, err)
			return
		}
		imageReference = imageReference.WithTag(version)
		device.Spec.Os = &v1beta1.DeviceOsSpec{Image: imageReference.String()}
		logrus.Infof("updating %s to image %s", deviceId, device.Spec.Os.Image)
	})
	if err != nil {
		return nil, imageReference, err
	}

	return device, imageReference, nil
}

func (h *Harness) GetCurrentDeviceGeneration(deviceId string) (deviceRenderedVersionInt int64, err error) {
	var deviceGeneration int64 = -1
	logrus.Infof("Waiting for the device to be UpToDate")
	h.WaitForDeviceContents(deviceId, "The device is UpToDate",
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil || device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusUpToDate {
				return false
			}
			if device.Metadata.Generation != nil {
				deviceGeneration = *device.Metadata.Generation
			}
			return true
		}, TIMEOUT)

	if deviceGeneration <= 0 {
		return deviceGeneration, fmt.Errorf("invalid generation: %d", deviceGeneration)

	}
	logrus.Infof("The device current generation is %d", deviceGeneration)

	return deviceGeneration, nil
}

func (h *Harness) PrepareNextDeviceGeneration(deviceId string) (int64, error) {
	currentGeneration, err := h.GetCurrentDeviceGeneration(deviceId)
	if err != nil {
		return -1, err
	}
	return currentGeneration + 1, nil
}

var (
	InvalidRenderedVersionErr = fmt.Errorf("invalid rendered version")
)

func GetRenderedVersion(device *v1beta1.Device) (int, error) {
	if device == nil || device.Status == nil {
		return -1, fmt.Errorf("invalid device: %+v", device)
	}
	version, err := strconv.Atoi(device.Status.Config.RenderedVersion)
	if err != nil {
		return -1, fmt.Errorf("failed to convert current rendered version '%s': %w", device.Status.Config.RenderedVersion, err)
	}
	if version <= 0 {
		return -1, fmt.Errorf("version: %d: %w", version, InvalidRenderedVersionErr)
	}
	return version, nil
}

func (h *Harness) GetCurrentDeviceRenderedVersion(deviceId string) (int, error) {
	deviceRenderedVersion := -1
	var renderedVersionError error

	logrus.Infof("Waiting for the device to be UpToDate")
	h.WaitForDeviceContents(deviceId, "The device is UpToDate",
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil || device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusUpToDate {
				return false
			}
			deviceRenderedVersion, renderedVersionError = GetRenderedVersion(device)
			return !errors.Is(renderedVersionError, InvalidRenderedVersionErr)
		}, TIMEOUT)
	if renderedVersionError != nil {
		return -1, renderedVersionError
	}
	logrus.Infof("The device current renderedVersion is %d", deviceRenderedVersion)
	return deviceRenderedVersion, nil
}

func (h *Harness) PrepareNextDeviceVersion(deviceId string) (int, error) {
	currentVersion, err := h.GetCurrentDeviceRenderedVersion(deviceId)
	if err != nil {
		return -1, err
	}
	return currentVersion + 1, nil
}

func (h *Harness) WaitForDeviceNewRenderedVersion(deviceId string, newRenderedVersionInt int) (err error) {
	// Check that the device was already approved
	Eventually(func() v1beta1.DeviceSummaryStatusType {
		res, err := h.GetDeviceWithStatusSummary(deviceId)
		Expect(err).ToNot(HaveOccurred())
		return res
	}, LONGTIMEOUT, POLLING).ShouldNot(BeEmpty())
	logrus.Infof("The device %s was approved", deviceId)

	// Wait for the device to pickup the new config and report measurements on device status.
	logrus.Infof("Waiting for the device to pick the config")
	UpdateRenderedVersionSuccessMessage := fmt.Sprintf("%s %d", util.UpdateRenderedVersionSuccess.String(), newRenderedVersionInt)
	seenUpdating := false
	h.WaitForDeviceContents(deviceId, UpdateRenderedVersionSuccessMessage,
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil {
				logrus.Warnf("Device %s or device status is nil, cannot check conditions", deviceId)
				return false
			}

			if device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpdating {
				seenUpdating = true
			}

			// Primary success signal: desired rendered version is applied, regardless of summary state.
			// Fleet-owned devices can momentarily report OutOfDate even after applying the target version.
			currentRenderedVersion, parseErr := strconv.Atoi(device.Status.Config.RenderedVersion)
			if parseErr == nil && currentRenderedVersion >= newRenderedVersionInt {
				if currentRenderedVersion > newRenderedVersionInt {
					logrus.Warnf("Device %s has rendered version %d, which is greater than %d", deviceId, currentRenderedVersion, newRenderedVersionInt)
				}
				return true
			}

			// Fail fast on terminal update failures only after we've seen active updating,
			// to avoid tripping on initial OutOfDate before the update begins.
			if seenUpdating && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate {
				updatedInfo := ""
				if device.Status.Updated.Info != nil {
					updatedInfo = *device.Status.Updated.Info
				}
				Fail(fmt.Sprintf(
					"Device %s failed to update to renderedVersion %d (current=%s): %s",
					deviceId,
					newRenderedVersionInt,
					device.Status.Config.RenderedVersion,
					updatedInfo,
				))
			}

			return false
		}, LONGTIMEOUT)

	return nil
}

// WaitForDeviceNewRenderedVersionWithReboot waits for the device to reach the target rendered version
// after an update that causes a reboot.
// Use only when the update triggers a device reboot. Otherwise, use WaitForDeviceNewRenderedVersion.
func (h *Harness) WaitForDeviceNewRenderedVersionWithReboot(deviceId string, newRenderedVersionInt int) (err error) {
	Eventually(func() v1beta1.DeviceSummaryStatusType {
		res, err := h.GetDeviceWithStatusSummary(deviceId)
		Expect(err).ToNot(HaveOccurred())
		return res
	}, LONGTIMEOUT, POLLING).ShouldNot(BeEmpty())
	logrus.Infof("The device %s was approved", deviceId)

	const specVisibleTimeout = "2m"
	logrus.Infof("Waiting for the device to acknowledge the new spec (up to %s)", specVisibleTimeout)
	h.WaitForDeviceContents(deviceId, "device has acknowledged the update (Updating or OutOfDate)",
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil {
				return false
			}
			currentRenderedVersion, parseErr := strconv.Atoi(device.Status.Config.RenderedVersion)
			if parseErr == nil && currentRenderedVersion >= newRenderedVersionInt {
				return true
			}
			if device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpdating ||
				device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate {
				return true
			}
			cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
			if cond != nil && cond.Status == v1beta1.ConditionStatusTrue {
				return true
			}
			return false
		}, specVisibleTimeout)

	logrus.Infof("Waiting for the device to reach renderedVersion %d (with reboot)", newRenderedVersionInt)
	successMsg := fmt.Sprintf("%s %d", util.UpdateRenderedVersionSuccess.String(), newRenderedVersionInt)
	seenUpdating := false
	h.WaitForDeviceContents(deviceId, successMsg,
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil {
				return false
			}
			// Transitional: rebooting or unknown — keep waiting, never fail.
			if device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUnknown {
				return false
			}
			if device.Status.Summary.Status == v1beta1.DeviceSummaryStatusRebooting ||
				device.Status.Summary.Status == v1beta1.DeviceSummaryStatusUnknown {
				return false
			}
			if device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpdating {
				seenUpdating = true
			}
			cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
			if cond != nil && cond.Status == v1beta1.ConditionStatusTrue {
				seenUpdating = true
			}
			currentRenderedVersion, parseErr := strconv.Atoi(device.Status.Config.RenderedVersion)
			if parseErr == nil && currentRenderedVersion >= newRenderedVersionInt {
				return true
			}
			// Fail only when device is OutOfDate and has not reached target version (terminal failure).
			if seenUpdating && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate {
				updatedInfo := ""
				if device.Status.Updated.Info != nil {
					updatedInfo = *device.Status.Updated.Info
				}
				Fail(fmt.Sprintf(
					"Device %s failed to update to renderedVersion %d (current=%s): %s",
					deviceId, newRenderedVersionInt, device.Status.Config.RenderedVersion, updatedInfo,
				))
			}
			return false
		}, LONGTIMEOUT)

	return nil
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

// UpdateDeviceAndWaitForFailure updates a device and waits for the update to fail with an error.
// If expectedMessageSubstrings are provided, it verifies that the error message contains at least one of them.
func (h *Harness) UpdateDeviceAndWaitForFailure(deviceID string, updateFunc func(device *v1beta1.Device), expectedMessageSubstrings ...string) error {
	err := h.UpdateDeviceWithRetries(deviceID, updateFunc)
	if err != nil {
		return fmt.Errorf("failed to update device: %w", err)
	}

	description := "update should fail with error"
	if len(expectedMessageSubstrings) > 0 {
		description = fmt.Sprintf("update should fail with error containing one of: %v", expectedMessageSubstrings)
	}

	h.WaitForDeviceContents(deviceID, description,
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil {
				return false
			}
			if !ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating,
				v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError)) {
				return false
			}
			if len(expectedMessageSubstrings) == 0 {
				return true
			}
			cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
			if cond == nil {
				return false
			}
			for _, substring := range expectedMessageSubstrings {
				if strings.Contains(cond.Message, substring) {
					return true
				}
			}
			return false
		}, LONGTIMEOUT)

	h.WaitForDeviceContents(deviceID, "device should be out of date but online after failed update",
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil {
				return false
			}
			return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate &&
				device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline
		}, LONGTIMEOUT)

	return nil
}

func (h *Harness) WaitForDeviceNewGeneration(deviceId string, newGeneration int64) (err error) {
	// Check that the device was already approved
	Eventually(func() v1beta1.DeviceSummaryStatusType {
		res, err := h.GetDeviceWithStatusSummary(deviceId)
		Expect(err).ToNot(HaveOccurred())
		return res
	}, LONGTIMEOUT, POLLING).ShouldNot(BeEmpty())
	logrus.Infof("The device %s was approved", deviceId)

	// Wait for the device to pickup the new config and report measurements on device status.
	logrus.Infof("Waiting for the device to pick the config")
	h.WaitForDeviceContents(deviceId, fmt.Sprintf("Waiting fot the device generation %d", newGeneration),
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil || device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusUpToDate {
				return false
			}
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					newGeneration == *device.Metadata.Generation {
					return true
				}
			}
			return false
		}, LONGTIMEOUT)

	return nil
}

// GetDeviceConfig is a generic helper function to retrieve device configurations
func GetDeviceConfig[T any](device *v1beta1.Device, configType v1beta1.ConfigProviderType,
	asConfig func(v1beta1.ConfigProviderSpec) (T, error)) (T, error) {

	var config T
	if device.Spec == nil || device.Spec.Config == nil {
		return config, fmt.Errorf("device spec or config is nil")
	}

	if len(*device.Spec.Config) > 0 {
		for _, configItem := range *device.Spec.Config {
			// Check config type
			itemType, err := configItem.Type()
			if err != nil {
				return config, fmt.Errorf("failed to get config type: %w", err)
			}
			if itemType == configType {
				// Convert to the expected config type
				config, err := asConfig(configItem)
				if err != nil {
					return config, fmt.Errorf("failed to convert config: %w", err)
				}

				return config, nil
			}
		}
	}

	// If we don't find the config, return an error
	return config, fmt.Errorf("%s config not found in the device", configType)
}

// Get InlineConfig
func (h *Harness) GetDeviceInlineConfig(device *v1beta1.Device, configName string) (v1beta1.InlineConfigProviderSpec, error) {
	return GetDeviceConfig(device, v1beta1.InlineConfigProviderType,
		func(c v1beta1.ConfigProviderSpec) (v1beta1.InlineConfigProviderSpec, error) {
			inlineConfig, err := c.AsInlineConfigProviderSpec()
			if err != nil {
				return inlineConfig, fmt.Errorf("failed to cast config type: %w", err)
			}
			if inlineConfig.Name == configName {
				logrus.Infof("Inline configuration found %s", configName)
				return inlineConfig, nil
			}
			return v1beta1.InlineConfigProviderSpec{}, fmt.Errorf("inline config not found")
		})
}

// Get GitConfig
func (h *Harness) GetDeviceGitConfig(device *v1beta1.Device, configName string) (v1beta1.GitConfigProviderSpec, error) {
	return GetDeviceConfig(device, v1beta1.GitConfigProviderType,
		func(c v1beta1.ConfigProviderSpec) (v1beta1.GitConfigProviderSpec, error) {
			gitConfig, err := c.AsGitConfigProviderSpec()
			if err != nil {
				return gitConfig, fmt.Errorf("failed to cast config type: %w", err)
			}
			if gitConfig.Name == configName {
				logrus.Infof("Git configuration found %s", configName)
				return gitConfig, nil
			}
			return v1beta1.GitConfigProviderSpec{}, fmt.Errorf("git config not found")
		})
}

// Get HttpConfig
func (h *Harness) GetDeviceHttpConfig(device *v1beta1.Device, configName string) (v1beta1.HttpConfigProviderSpec, error) {
	return GetDeviceConfig(device, v1beta1.HttpConfigProviderType,
		func(c v1beta1.ConfigProviderSpec) (v1beta1.HttpConfigProviderSpec, error) {
			httpConfig, err := c.AsHttpConfigProviderSpec()
			if err != nil {
				return httpConfig, fmt.Errorf("failed to cast config type: %w", err)
			}
			if httpConfig.Name == configName {
				logrus.Infof("Http configuration found %s", configName)
				return httpConfig, nil
			}
			return v1beta1.HttpConfigProviderSpec{}, fmt.Errorf("http config not found")
		})
}

// Get an http config of a device resource
func (h *Harness) GetDeviceOsImage(device *v1beta1.Device) (image string, err error) {
	if device.Spec == nil {
		return "", fmt.Errorf("device spec is nil")
	}
	if device.Spec.Os == nil {
		return "", fmt.Errorf("device os spec is nil")
	}

	return device.Spec.Os.Image, nil
}

// Check that the device summary status is equal to the status input
func (h *Harness) CheckDeviceStatus(deviceId string, status v1beta1.DeviceSummaryStatusType) (*v1beta1.Device, error) {
	response, err := h.GetDeviceWithStatusSystem(deviceId)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("device response is nil")
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("device.JSON200 response is nil")
	}
	device := response.JSON200
	deviceStaus := device.Status.Summary.Status
	if deviceStaus != status {
		return nil, fmt.Errorf("the device status is notOnline but %s", deviceStaus)
	}
	return device, nil
}

// Get device with response
func (h *Harness) GetDevice(deviceId string) (*v1beta1.Device, error) {
	response, err := h.Client.GetDeviceWithResponse(h.Context, deviceId)
	if err != nil {
		return nil, fmt.Errorf("failed to get device with response: %s", err)
	}
	if response == nil {
		return nil, fmt.Errorf("device response is nil")
	}
	if response.JSON200 == nil {
		// Include HTTP status code in error for better debugging
		statusCode := response.StatusCode()
		if statusCode == util.HTTP_403_ERROR {
			return nil, fmt.Errorf("device %s: permission denied (%s) - RBAC permissions may not have propagated yet", deviceId, strconv.Itoa(util.HTTP_403_ERROR))
		}
		if statusCode == util.HTTP_404_ERROR {
			return nil, fmt.Errorf("device %s not found (%d)", deviceId, util.HTTP_404_ERROR)
		}
		return nil, fmt.Errorf("device %s not found (HTTP %d): %s", deviceId, statusCode, string(response.Body))
	}
	device := response.JSON200
	return device, nil
}

func (h *Harness) SetLabelsForDevice(deviceId string, labels map[string]string) error {
	return h.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
		if len(labels) == 0 {
			device.Metadata.Labels = nil
			return
		}
		devLabels := make(map[string]string, len(labels)+1)
		devLabels["test-id"] = h.GetTestIDFromContext()
		for key, value := range labels {
			devLabels[key] = value
		}
		device.Metadata.Labels = &devLabels
	})
}

func (h *Harness) SetLabelsForDevicesByIndex(deviceIDs []string, labelsList []map[string]string, fleetName string) error {
	if len(deviceIDs) != len(labelsList) {
		return fmt.Errorf("mismatched lengths: deviceIDs (%d) and labelsList (%d)", len(deviceIDs), len(labelsList))
	}

	for i, deviceID := range deviceIDs {
		labels := labelsList[i]
		if labels == nil {
			labels = make(map[string]string)
		}
		labels["fleet"] = fleetName
		err := h.SetLabelsForDevice(deviceID, labels)
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *Harness) GetSelectedDevicesForBatch(fleetName string) ([]*v1beta1.Device, error) {
	labelSelector := fmt.Sprintf("fleet=%s", fleetName)
	listDeviceParams := &v1beta1.ListDevicesParams{
		LabelSelector: &labelSelector,
	}
	response, err := h.Client.ListDevicesWithResponse(h.Context, listDeviceParams)
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %s", err)
	}
	if response == nil {
		return nil, fmt.Errorf("device response is nil")
	}
	devices := response.JSON200.Items

	var result []*v1beta1.Device

	for _, device := range devices {
		annotations := device.Metadata.Annotations
		if annotations == nil {
			continue
		}
		if _, ok := (*annotations)["fleet-controller/selectedForRollout"]; ok {
			deviceCopy := device
			result = append(result, &deviceCopy)
		}
	}

	return result, nil
}

func (h *Harness) GetUnavailableDevicesPerGroup(fleetName string, groupBy []string) (map[string][]*v1beta1.Device, error) {
	labelSelector := fmt.Sprintf("fleet=%s", fleetName)
	listDeviceParams := &v1beta1.ListDevicesParams{
		LabelSelector: &labelSelector,
	}

	response, err := h.Client.ListDevicesWithResponse(h.Context, listDeviceParams)
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %s", err)
	}
	if response == nil {
		return nil, fmt.Errorf("device response is nil")
	}

	devices := response.JSON200.Items
	result := make(map[string][]*v1beta1.Device)

	for _, device := range devices {
		// Check if device is unavailable
		if device.Status != nil && (device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpdating ||
			device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUnknown) {
			// Generate group key based on labels
			groupKey := ""

			if device.Metadata.Labels != nil {
				labelValues := []string{}
				for _, key := range groupBy {
					value, exists := (*device.Metadata.Labels)[key]
					if exists {
						labelValues = append(labelValues, value)
					} else {
						labelValues = append(labelValues, "")
					}
				}
				groupKey = strings.Join(labelValues, ":")
			}

			// Add device to the appropriate group
			if _, exists := result[groupKey]; !exists {
				result[groupKey] = []*v1beta1.Device{}
			}
			deviceCopy := device
			result[groupKey] = append(result[groupKey], &deviceCopy)
		}
	}

	return result, nil
}

func (h *Harness) GetUpdatedDevices(fleetName string) ([]*v1beta1.Device, error) {
	labelSelector := fmt.Sprintf("fleet=%s", fleetName)
	listDeviceParams := &v1beta1.ListDevicesParams{
		LabelSelector: &labelSelector,
	}

	response, err := h.Client.ListDevicesWithResponse(h.Context, listDeviceParams)
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %s", err)
	}
	if response == nil {
		return nil, fmt.Errorf("device response is nil")
	}

	devices := response.JSON200.Items
	var result []*v1beta1.Device

	for _, device := range devices {
		if device.Status != nil && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate {
			deviceCopy := device
			result = append(result, &deviceCopy)
		}
	}

	return result, nil
}

func (h *Harness) AddConfigToDeviceWithRetries(deviceId string, config v1beta1.ConfigProviderSpec) error {
	return h.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
		specs := lo.FromPtr(device.Spec.Config)
		specs = append(specs, config)
		device.Spec.Config = &specs
		logrus.WithFields(logrus.Fields{
			"deviceId":        deviceId,
			"configProviders": len(specs),
		}).Info("Updating device with new config")
	})
}

// UpdateDeviceConfigWithRetries updates the configuration of a device with retries using the provided harness and config specs.
// It applies the provided configuration and waits for the device to reach the specified rendered version.
func (h *Harness) UpdateDeviceConfigWithRetries(deviceId string, configs []v1beta1.ConfigProviderSpec, nextRenderedVersion int) error {
	err := h.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
		device.Spec.Config = &configs
		logrus.WithFields(logrus.Fields{
			"deviceId":        deviceId,
			"configProviders": len(configs),
		}).Info("Updating device with new config")
	})
	if err != nil {
		return err
	}
	return h.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
}

// reset agent
func (h *Harness) ResetAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "pkill", "-HUP", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to send SIGHUP to agent: %w", err)
	}
	return nil
}

// SetAgentConfig configures the agent by writing the configuration file.
// This method should be called before starting the agent.
func (h *Harness) SetAgentConfig(cfg *agentcfg.Config) error {
	// Marshal the config to YAML
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal agent config: %w", err)
	}

	// Write the config to the VM (using correct agent config filename)
	stdout, err := h.VM.RunSSH([]string{
		"sudo", "mkdir", "-p", "/etc/flightctl",
		"&&",
		"echo", fmt.Sprintf("'%s'", string(configBytes)),
		"|",
		"sudo", "tee", "/etc/flightctl/config.yaml",
	}, nil)
	if err != nil {
		logrus.Errorf("Failed to write agent config: %v, stdout: %s", err, stdout)
		return fmt.Errorf("failed to write agent config: %w", err)
	}

	return nil
}

// GetAgentConfig reads and parses the agent configuration from the VM.
func (h *Harness) GetAgentConfig() (*agentcfg.Config, error) {
	stdout, err := h.VM.RunSSH([]string{"sudo", "cat", "/etc/flightctl/config.yaml"}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent config: %w", err)
	}
	if stdout == nil {
		return nil, fmt.Errorf("agent config output is nil")
	}

	cfg := &agentcfg.Config{}
	if err := yaml.Unmarshal(stdout.Bytes(), cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal agent config: %w", err)
	}

	return cfg, nil
}

// DecommissionDevice runs the CLI decommission command for the given device name.
// Returns the CLI output and any error.
func (h *Harness) DecommissionDevice(deviceName string) (string, error) {
	if deviceName == "" {
		return "", fmt.Errorf("device name is empty")
	}
	return h.CLI("decommission", "devices/"+deviceName)
}
