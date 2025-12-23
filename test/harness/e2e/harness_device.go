package e2e

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
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

func (h *Harness) UpdateApplication(withRetries bool, deviceId string, appName string, appProvider any, envVars map[string]string) error {
	logrus.Infof("UpdateApplication called with deviceId=%s, appName=%s, withRetries=%v", deviceId, appName, withRetries)

	updateFunc := func(device *v1beta1.Device) {
		logrus.Infof("Starting update for device: %s", *device.Metadata.Name)
		var appSpec v1beta1.ApplicationProviderSpec
		var err error

		switch spec := appProvider.(type) {
		case v1beta1.InlineApplicationProviderSpec:
			logrus.Infof("Processing InlineApplicationProviderSpec for %s", appName)
			err = appSpec.FromInlineApplicationProviderSpec(spec)
		case v1beta1.ImageApplicationProviderSpec:
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
		appSpec.AppType = v1beta1.AppTypeCompose

		if envVars != nil {
			logrus.Infof("Setting environment variables for app %s: %v", appName, envVars)
			appSpec.EnvVars = &envVars
		}

		if device.Spec.Applications == nil {
			logrus.Infof("device.Spec.Applications is nil, initializing with app %s", appName)
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
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
			if device == nil || device.Status == nil {
				return false
			}
			if device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate {
				if device.Metadata.Generation != nil {
					deviceGeneration = *device.Metadata.Generation
				}
				return true
			}
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate {
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
			if device == nil || device.Status == nil {
				return false
			}
			if device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate {
				deviceRenderedVersion, renderedVersionError = GetRenderedVersion(device)
				return !errors.Is(renderedVersionError, InvalidRenderedVersionErr)
			}
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate {
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
	Eventually(func() v1beta1.DeviceSummaryStatusType {
		res, err := h.GetDeviceWithStatusSummary(deviceId)
		Expect(err).ToNot(HaveOccurred())
		return res
	}, LONGTIMEOUT, POLLING).ShouldNot(BeEmpty())
	logrus.Infof("The device %s was approved", deviceId)

	// Wait for the device to pickup the new config and report measurements on device status.
	logrus.Infof("Waiting for the device to pick the config")
	UpdateRenderedVersionSuccessMessage := fmt.Sprintf("%s %d", util.UpdateRenderedVersionSuccess.String(), newRenderedVersionInt)
	h.WaitForDeviceContents(deviceId, UpdateRenderedVersionSuccessMessage,
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil {
				logrus.Warnf("Device %s or device status is nil, cannot check conditions", deviceId)
				return false
			}
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate {
					// Accept jumps where multiple renders happen quickly (e.g., concurrent fleet/device updates)
					if v, err := strconv.Atoi(device.Status.Config.RenderedVersion); err == nil && v >= newRenderedVersionInt {
						if v > newRenderedVersionInt {
							logrus.Warnf("Device %s has rendered version %d, which is greater than %d", deviceId, v, newRenderedVersionInt)
						}
						return true
					}
				}
			}
			return false
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
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate &&
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
		return nil, fmt.Errorf("device not found")
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
		// Check if device has been updated
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
			"deviceId": deviceId,
			"config":   device.Spec.Config,
		}).Info("Updating device with new config")
	})
}

// UpdateDeviceConfigWithRetries updates the configuration of a device with retries using the provided harness and config specs.
// It applies the provided configuration and waits for the device to reach the specified rendered version.
func (h *Harness) UpdateDeviceConfigWithRetries(deviceId string, configs []v1beta1.ConfigProviderSpec, nextRenderedVersion int) error {
	err := h.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
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

// WaitForTPMInitialization waits for TPM hardware to be ready in the VM.
// This should be called after VM setup but before agent configuration.
func (h *Harness) WaitForTPMInitialization() error {
	logrus.Info("Waiting for TPM hardware initialization...")
	time.Sleep(20 * time.Second)
	return nil
}

// VerifyTPMFunctionality checks that TPM device is accessible and functional.
func (h *Harness) VerifyTPMFunctionality() error {
	// Check TPM device presence
	stdout, err := h.VM.RunSSH([]string{"sh", "-lc", "ls -la /dev/tpm*"}, nil)
	if err != nil || !strings.Contains(stdout.String(), "/dev/tpm0") {
		// Check if we're expecting TPM hardware to be available
		realTPM := strings.ToLower(os.Getenv("FLIGHTCTL_REAL_TPM")) == "true"
		skipTPM := strings.ToLower(os.Getenv("FLIGHTCTL_SKIP_TPM")) == "true"

		if realTPM && !skipTPM {
			// Real TPM was expected but not found
			return fmt.Errorf("TPM device /dev/tpm0 not available (FLIGHTCTL_REAL_TPM=true but no TPM device found)")
		}

		// No TPM device found but not in real TPM mode - this is expected for CI/virtual environments
		logrus.Info("No TPM device found (/dev/tpm0), skipping TPM hardware verification (expected in CI environments)")
		return nil
	}

	// Test TPM functionality
	_, _ = h.VM.RunSSH([]string{"sh", "-lc", "sudo tpm2_startup -c || true"}, nil)

	_, err = h.VM.RunSSH([]string{"sudo", "tpm2_getrandom", "8"}, nil)
	if err != nil {
		return fmt.Errorf("TPM getrandom test failed: %w", err)
	}

	logrus.Info("TPM functionality verified successfully")
	return nil
}

// EnableTPMForDevice configures the agent to use TPM for device identity.
// This reads existing agent config, updates TPM settings, and writes it back.
func (h *Harness) EnableTPMForDevice() error {
	// Get existing agent config or create default
	stdout, err := h.VM.RunSSH([]string{"cat", "/etc/flightctl/config.yaml"}, nil)
	var agentConfig *agentcfg.Config

	if err == nil && stdout.Len() > 0 {
		// Parse existing config
		agentConfig = &agentcfg.Config{}
		err = yaml.Unmarshal(stdout.Bytes(), agentConfig)
		if err != nil {
			logrus.Warnf("Failed to parse existing config, using default: %v", err)
			agentConfig = &agentcfg.Config{}
		}
	} else {
		// No existing config, create new
		agentConfig = &agentcfg.Config{}
	}

	// Configure TPM settings
	agentConfig.TPM = agentcfg.TPM{
		Enabled:         true,
		DevicePath:      "/dev/tpm0",
		StorageFilePath: filepath.Join(agentcfg.DefaultDataDir, agentcfg.DefaultTPMKeyFile),
		AuthEnabled:     true,
	}

	// Write the updated config
	err = h.SetAgentConfig(agentConfig)
	if err != nil {
		return fmt.Errorf("failed to set TPM agent config: %w", err)
	}

	logrus.Info("TPM configuration enabled for device")
	return nil
}

// SetupDeviceWithTPM prepares a device VM with TPM functionality enabled.
// This handles the complete TPM setup process in the correct order.
func (h *Harness) SetupDeviceWithTPM(workerID int) error {
	// 1. Setup VM from pool (includes agent start)
	err := h.SetupVMFromPoolAndStartAgent(workerID)
	if err != nil {
		return fmt.Errorf("failed to setup VM: %w", err)
	}

	// 2. Stop agent immediately to configure TPM first
	err = h.StopFlightCtlAgent()
	if err != nil {
		return fmt.Errorf("failed to stop agent: %w", err)
	}

	// Clean CSR from non-TPM agent start to avoid device ID mismatch
	_, err = h.VM.RunSSH([]string{"sudo", "rm", "-f", "/var/lib/flightctl/certs/agent.csr"}, nil)
	if err != nil {
		logrus.Warnf("Failed to clean stale CSR: %v", err)
	}

	// 3. Wait for TPM hardware initialization
	err = h.WaitForTPMInitialization()
	if err != nil {
		return fmt.Errorf("TPM initialization failed: %w", err)
	}

	// 4. Verify TPM is functional
	err = h.VerifyTPMFunctionality()
	if err != nil {
		return fmt.Errorf("TPM verification failed: %w", err)
	}

	// 5. Configure agent for TPM
	err = h.EnableTPMForDevice()
	if err != nil {
		return fmt.Errorf("TPM configuration failed: %w", err)
	}

	// 6. Start agent with TPM configuration
	err = h.StartFlightCtlAgent()
	if err != nil {
		return fmt.Errorf("failed to start agent with TPM: %w", err)
	}

	// 7. Brief wait for agent to initialize with TPM
	time.Sleep(10 * time.Second)

	logrus.Info("Device TPM setup completed successfully")
	return nil
}

// VerifyEnrollmentTPMAttestationData checks for TPM attestation data in enrollment request SystemInfo
// Returns error if no TPM attestation data is found
func (h *Harness) VerifyEnrollmentTPMAttestationData(systemInfo v1beta1.DeviceSystemInfo) error {
	// Look for TPM attestation data - check for either key name that might be used
	_, hasAttestation := systemInfo.Get("attestation")
	if !hasAttestation {
		logrus.Infof("No 'attestation' key found, checking for other TPM-related keys...")
		tpmVendorInfo, hasTmpVendorInfo := systemInfo.Get("tpmVendorInfo")
		if hasTmpVendorInfo {
			logrus.Infof("Found 'tpmVendorInfo' key: %s", tpmVendorInfo)
			logrus.Infof("TPM attestation data found in enrollment request: %s...", tpmVendorInfo)
			return nil
		}
		return errors.New("no TPM attestation data found in enrollment request")
	}

	logrus.Infof("TPM attestation data found in enrollment request")
	return nil
}

// VerifyDeviceTPMAttestationData checks for TPM attestation data in device SystemInfo
// Virtual TPM provides "tpmVendorInfo", real TPM provides "attestation"
// Returns error if attestation data is missing or empty
func (h *Harness) VerifyDeviceTPMAttestationData(device *v1beta1.Device) error {
	// Check for TPM vendor info in system info (virtual TPM provides tpmVendorInfo instead of full attestation)
	tpmVendorInfo, hasTmpVendorInfo := device.Status.SystemInfo.Get("tpmVendorInfo")
	if hasTmpVendorInfo {
		if tpmVendorInfo == "" {
			return fmt.Errorf("tpmVendorInfo is empty in device system info")
		}
		logrus.Infof("TPM vendor info found in device system info: %s", tpmVendorInfo)
		return nil
	}

	// For real TPM devices, check for full attestation data
	deviceAttestation, hasAttestation := device.Status.SystemInfo.Get("attestation")
	if !hasAttestation {
		return fmt.Errorf("no TPM attestation data found in device system info")
	}
	if deviceAttestation == "" {
		return fmt.Errorf("attestation data is empty in device system info")
	}
	logrus.Infof("TPM attestation data found in device system info: %.50s...", deviceAttestation)
	return nil
}
