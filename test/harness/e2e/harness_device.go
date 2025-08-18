package e2e

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
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
func (h *Harness) GetDeviceSystemInfo(deviceID string) *v1alpha1.DeviceSystemInfo {
	resp, err := h.GetDeviceWithStatusSystem(deviceID)
	if err != nil || resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil {
		return nil
	}
	return &resp.JSON200.Status.SystemInfo
}

func (h *Harness) GetDeviceWithStatusSummary(enrollmentID string) (v1alpha1.DeviceSummaryStatusType, error) {
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

func (h *Harness) GetDeviceWithUpdateStatus(enrollmentID string) (v1alpha1.DeviceUpdatedStatusType, error) {
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

func (h *Harness) UpdateDeviceWithRetries(deviceId string, updateFunction func(*v1alpha1.Device)) error {
	// this needs to be changed so that we don't use Eventually as our polling mechanism so that we can simply return
	// the underlying error if one exists
	updateResourceWithRetries(func() error {
		return h.UpdateDevice(deviceId, updateFunction)
	})
	return nil
}

func (h *Harness) UpdateDevice(deviceId string, updateFunction func(*v1alpha1.Device)) error {
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

func (h *Harness) UpdateApplication(withRetries bool, deviceId string, appName string, appProvider any, envVars map[string]string) error {
	logrus.Infof("UpdateApplication called with deviceId=%s, appName=%s, withRetries=%v", deviceId, appName, withRetries)

	updateFunc := func(device *v1alpha1.Device) {
		logrus.Infof("Starting update for device: %s", *device.Metadata.Name)
		var appSpec v1alpha1.ApplicationProviderSpec
		var err error

		switch spec := appProvider.(type) {
		case v1alpha1.InlineApplicationProviderSpec:
			logrus.Infof("Processing InlineApplicationProviderSpec for %s", appName)
			err = appSpec.FromInlineApplicationProviderSpec(spec)
		case v1alpha1.ImageApplicationProviderSpec:
			logrus.Infof("Processing ImageApplicationProviderSpec for %s", appName)
			err = appSpec.FromImageApplicationProviderSpec(spec)
		default:
			logrus.Errorf("Unsupported application provider type: %T for %s", appProvider, appName)
			return
		}

		if err != nil {
			logrus.Errorf("Error converting application provider spec: %v", err)
			return
		}

		appSpec.Name = &appName
		appType := v1alpha1.AppTypeCompose
		appSpec.AppType = &appType

		if envVars != nil {
			logrus.Infof("Setting environment variables for app %s: %v", appName, envVars)
			appSpec.EnvVars = &envVars
		}

		if device.Spec.Applications == nil {
			logrus.Infof("device.Spec.Applications is nil, initializing with app %s", appName)
			device.Spec.Applications = &[]v1alpha1.ApplicationProviderSpec{appSpec}
			return
		}

		for i, a := range *device.Spec.Applications {
			if a.Name != nil && *a.Name == appName {
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

func (h *Harness) fetchDeviceContents(deviceId string) (*v1alpha1.Device, error) {
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

func (h *Harness) WaitForDeviceContents(deviceId string, description string, condition func(*v1alpha1.Device) bool, timeout string) {
	waitForResourceContents(deviceId, description, func(id string) (*v1alpha1.Device, error) {
		return h.fetchDeviceContents(id)
	}, condition, timeout)
}

// EnsureDeviceContents ensures that the contents of the device match the specified condition for the entire timeout
func (h *Harness) EnsureDeviceContents(deviceId string, description string, condition func(*v1alpha1.Device) bool, timeout string) {
	ensureResourceContents(deviceId, description, func(id string) (*v1alpha1.Device, error) {
		return h.fetchDeviceContents(id)
	}, condition, timeout)
}

func (h *Harness) WaitForBootstrapAndUpdateToVersion(deviceId string, version string) (*v1alpha1.Device, string, error) {
	// Check the device status right after bootstrap
	response, err := h.GetDeviceWithStatusSystem(deviceId)
	if err != nil {
		return nil, "", err
	}
	device := response.JSON200
	if device.Status.Summary.Status != v1alpha1.DeviceSummaryStatusOnline {
		return nil, "", fmt.Errorf("device: %q is not online", deviceId)
	}

	var newImageReference string

	err = h.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
		currentImage := device.Status.Os.Image
		logrus.Infof("current image for %s is %s", deviceId, currentImage)
		repo, _ := h.parseImageReference(currentImage)
		newImageReference = repo + version
		device.Spec.Os = &v1alpha1.DeviceOsSpec{Image: newImageReference}
		logrus.Infof("updating %s to image %s", deviceId, device.Spec.Os.Image)
	})
	if err != nil {
		return nil, "", err
	}

	return device, newImageReference, nil
}

func (h *Harness) GetCurrentDeviceGeneration(deviceId string) (deviceRenderedVersionInt int64, err error) {
	var deviceGeneration int64 = -1
	logrus.Infof("Waiting for the device to be UpToDate")
	h.WaitForDeviceContents(deviceId, "The device is UpToDate",
		func(device *v1alpha1.Device) bool {
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpToDate {
					deviceGeneration = *device.Metadata.Generation

					return true
				}
			}
			return false
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

func GetRenderedVersion(device *v1alpha1.Device) (int, error) {
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
		func(device *v1alpha1.Device) bool {
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpToDate {
					deviceRenderedVersion, renderedVersionError = GetRenderedVersion(device)
					// try until we get a valid rendered version
					return !errors.Is(renderedVersionError, InvalidRenderedVersionErr)
				}
			}
			return false
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
	Eventually(func() v1alpha1.DeviceSummaryStatusType {
		res, err := h.GetDeviceWithStatusSummary(deviceId)
		Expect(err).ToNot(HaveOccurred())
		return res
	}, LONGTIMEOUT, POLLING).ShouldNot(BeEmpty())
	logrus.Infof("The device %s was approved", deviceId)

	// Wait for the device to pickup the new config and report measurements on device status.
	logrus.Infof("Waiting for the device to pick the config")
	UpdateRenderedVersionSuccessMessage := fmt.Sprintf("%s %d", util.UpdateRenderedVersionSuccess.String(), newRenderedVersionInt)
	h.WaitForDeviceContents(deviceId, UpdateRenderedVersionSuccessMessage,
		func(device *v1alpha1.Device) bool {
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpToDate &&
					device.Status.Config.RenderedVersion == strconv.Itoa(newRenderedVersionInt) {
					return true
				}
			}
			return false
		}, LONGTIMEOUT)

	return nil
}

func (h *Harness) WaitForDeviceNewGeneration(deviceId string, newGeneration int64) (err error) {
	// Check that the device was already approved
	Eventually(func() v1alpha1.DeviceSummaryStatusType {
		res, err := h.GetDeviceWithStatusSummary(deviceId)
		Expect(err).ToNot(HaveOccurred())
		return res
	}, LONGTIMEOUT, POLLING).ShouldNot(BeEmpty())
	logrus.Infof("The device %s was approved", deviceId)

	// Wait for the device to pickup the new config and report measurements on device status.
	logrus.Infof("Waiting for the device to pick the config")
	h.WaitForDeviceContents(deviceId, fmt.Sprintf("Waiting fot the device generation %d", newGeneration),
		func(device *v1alpha1.Device) bool {
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpToDate &&
					newGeneration == *device.Metadata.Generation {
					return true
				}
			}
			return false
		}, LONGTIMEOUT)

	return nil
}

// GetDeviceConfig is a generic helper function to retrieve device configurations
func GetDeviceConfig[T any](device *v1alpha1.Device, configType v1alpha1.ConfigProviderType,
	asConfig func(v1alpha1.ConfigProviderSpec) (T, error)) (T, error) {

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
func (h *Harness) GetDeviceInlineConfig(device *v1alpha1.Device, configName string) (v1alpha1.InlineConfigProviderSpec, error) {
	return GetDeviceConfig(device, v1alpha1.InlineConfigProviderType,
		func(c v1alpha1.ConfigProviderSpec) (v1alpha1.InlineConfigProviderSpec, error) {
			inlineConfig, err := c.AsInlineConfigProviderSpec()
			if err != nil {
				return inlineConfig, fmt.Errorf("failed to cast config type: %w", err)
			}
			if inlineConfig.Name == configName {
				logrus.Infof("Inline configuration found %s", configName)
				return inlineConfig, nil
			}
			return v1alpha1.InlineConfigProviderSpec{}, fmt.Errorf("inline config not found")
		})
}

// Get GitConfig
func (h *Harness) GetDeviceGitConfig(device *v1alpha1.Device, configName string) (v1alpha1.GitConfigProviderSpec, error) {
	return GetDeviceConfig(device, v1alpha1.GitConfigProviderType,
		func(c v1alpha1.ConfigProviderSpec) (v1alpha1.GitConfigProviderSpec, error) {
			gitConfig, err := c.AsGitConfigProviderSpec()
			if err != nil {
				return gitConfig, fmt.Errorf("failed to cast config type: %w", err)
			}
			if gitConfig.Name == configName {
				logrus.Infof("Git configuration found %s", configName)
				return gitConfig, nil
			}
			return v1alpha1.GitConfigProviderSpec{}, fmt.Errorf("git config not found")
		})
}

// Get HttpConfig
func (h *Harness) GetDeviceHttpConfig(device *v1alpha1.Device, configName string) (v1alpha1.HttpConfigProviderSpec, error) {
	return GetDeviceConfig(device, v1alpha1.HttpConfigProviderType,
		func(c v1alpha1.ConfigProviderSpec) (v1alpha1.HttpConfigProviderSpec, error) {
			httpConfig, err := c.AsHttpConfigProviderSpec()
			if err != nil {
				return httpConfig, fmt.Errorf("failed to cast config type: %w", err)
			}
			if httpConfig.Name == configName {
				logrus.Infof("Http configuration found %s", configName)
				return httpConfig, nil
			}
			return v1alpha1.HttpConfigProviderSpec{}, fmt.Errorf("http config not found")
		})
}

// Get an http config of a device resource
func (h *Harness) GetDeviceOsImage(device *v1alpha1.Device) (image string, err error) {
	if device.Spec == nil {
		return "", fmt.Errorf("device spec is nil")
	}
	if device.Spec.Os == nil {
		return "", fmt.Errorf("device os spec is nil")
	}

	return device.Spec.Os.Image, nil
}

// Check that the device summary status is equal to the status input
func (h *Harness) CheckDeviceStatus(deviceId string, status v1alpha1.DeviceSummaryStatusType) (*v1alpha1.Device, error) {
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
func (h *Harness) GetDevice(deviceId string) (*v1alpha1.Device, error) {
	response, err := h.Client.GetDeviceWithResponse(h.Context, deviceId)
	if err != nil {
		return nil, fmt.Errorf("failed to get device with response: %s", err)
	}
	if response == nil {
		return nil, fmt.Errorf("device response is nil")
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("device not found")
	}
	device := response.JSON200
	return device, nil
}

func (h *Harness) SetLabelsForDevice(deviceId string, labels map[string]string) error {
	return h.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
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

func (h *Harness) GetSelectedDevicesForBatch(fleetName string) ([]*v1alpha1.Device, error) {
	labelSelector := fmt.Sprintf("fleet=%s", fleetName)
	listDeviceParams := &v1alpha1.ListDevicesParams{
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

	var result []*v1alpha1.Device

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

func (h *Harness) GetUnavailableDevicesPerGroup(fleetName string, groupBy []string) (map[string][]*v1alpha1.Device, error) {
	labelSelector := fmt.Sprintf("fleet=%s", fleetName)
	listDeviceParams := &v1alpha1.ListDevicesParams{
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
	result := make(map[string][]*v1alpha1.Device)

	for _, device := range devices {
		// Check if device is unavailable
		if device.Status != nil && (device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpdating ||
			device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUnknown) {
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
				result[groupKey] = []*v1alpha1.Device{}
			}
			deviceCopy := device
			result[groupKey] = append(result[groupKey], &deviceCopy)
		}
	}

	return result, nil
}

func (h *Harness) GetUpdatedDevices(fleetName string) ([]*v1alpha1.Device, error) {
	labelSelector := fmt.Sprintf("fleet=%s", fleetName)
	listDeviceParams := &v1alpha1.ListDevicesParams{
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
	var result []*v1alpha1.Device

	for _, device := range devices {
		// Check if device has been updated
		if device.Status != nil && device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpToDate {
			deviceCopy := device
			result = append(result, &deviceCopy)
		}
	}

	return result, nil
}

func (h *Harness) AddConfigToDeviceWithRetries(deviceId string, config v1alpha1.ConfigProviderSpec) error {
	return h.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
		specs := lo.FromPtr(device.Spec.Config)
		specs = append(specs, config)
		device.Spec.Config = &specs
		logrus.WithFields(logrus.Fields{
			"deviceId": deviceId,
			"config":   device.Spec.Config,
		}).Info("Updating device with new config")
	})
}

// UpdateDeviceConfigWithRetries updates the configuration of a device with retries using the provided harness and config specs.
// It applies the provided configuration and waits for the device to reach the specified rendered version.
func (h *Harness) UpdateDeviceConfigWithRetries(deviceId string, configs []v1alpha1.ConfigProviderSpec, nextRenderedVersion int) error {
	err := h.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
		device.Spec.Config = &configs
		logrus.WithFields(logrus.Fields{
			"deviceId": deviceId,
			"config":   device.Spec.Config,
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
