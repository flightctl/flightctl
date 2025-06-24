package agent_test

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var _ = Describe("VM Agent behavior during updates", func() {
	var (
		ctx      context.Context
		harness  *e2e.Harness
		deviceId string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		harness = e2e.NewTestHarness(ctx)
		deviceId = harness.StartVMAndEnroll()
	})

	AfterEach(func() {
		harness.Cleanup(true)
	})

	Context("updates", func() {
		It("should update to the requested image", Label("75523", "sanity"), func() {
			By("Verifying update to agent  with requested image")
			device, newImageReference := harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":v2")

			currentImage := device.Status.Os.Image
			logrus.Infof("Current image is: %s", currentImage)
			logrus.Infof("New image is: %s", newImageReference)

			harness.WaitForDeviceContents(deviceId, "The device is preparing an update to renderedVersion: 2",
				func(device *v1alpha1.Device) bool {
					return e2e.ConditionExists(device, v1alpha1.DeviceUpdating, v1alpha1.ConditionStatusTrue, string(v1alpha1.UpdateStateApplyingUpdate))
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusOnline))

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1alpha1.Device) bool {
					return e2e.ConditionExists(device, v1alpha1.DeviceUpdating, v1alpha1.ConditionStatusTrue, string(v1alpha1.UpdateStateRebooting))
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusRebooting))

			harness.WaitForDeviceContents(deviceId, "Updated to desired renderedVersion: 2",
				func(device *v1alpha1.Device) bool {
					for _, condition := range device.Status.Conditions {
						if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
							condition.Message == UpdateRenderedVersionSuccess.String() {
							return true
						}
					}
					return false
				}, TIMEOUT)
			logrus.Info("Device updated to new image 🎉")
		})

		It("Should update to v4 with embedded application", Label("77671", "sanity"), func() {
			By("Verifying update to agent  with embedded application")

			device, newImageReference := harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":v4")

			currentImage := device.Status.Os.Image
			logrus.Infof("Current image is: %s", currentImage)
			logrus.Infof("New image is: %s", newImageReference)

			harness.WaitForDeviceContents(deviceId, "The device is preparing an update to renderedVersion: 2",
				func(device *v1alpha1.Device) bool {
					return e2e.ConditionExists(device, v1alpha1.DeviceUpdating, v1alpha1.ConditionStatusTrue, string(v1alpha1.UpdateStateApplyingUpdate))
				}, TIMEOUT)

			Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusOnline))

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1alpha1.Device) bool {
					return e2e.ConditionExists(device, v1alpha1.DeviceUpdating, v1alpha1.ConditionStatusTrue, string(v1alpha1.UpdateStateRebooting))
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Rebooting")))

			harness.WaitForDeviceContents(deviceId, "Updated to desired renderedVersion: 2",
				func(device *v1alpha1.Device) bool {
					for _, condition := range device.Status.Conditions {
						if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
							condition.Message == UpdateRenderedVersionSuccess.String() {
							return true
						}
					}
					return false
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			logrus.Infof("Device updated to new image %s 🎉", "flightctl-device:v4")
			logrus.Info("We expect containers with sleep infinity process to be present but not running")
			stdout, err := harness.VM.RunSSH([]string{"sudo", "podman", "ps"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("sleep infinity"))

			logrus.Info("We expect podman containers with sleep infinity process to be present but not running 👌")

			device, newImageReference = harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":base")

			currentImage = device.Status.Os.Image
			logrus.Infof("Current image is: %s", currentImage)
			logrus.Infof("New image is: %s", newImageReference)

			harness.WaitForDeviceContents(deviceId, "The device is preparing an update to renderedVersion: 3",
				func(device *v1alpha1.Device) bool {
					return e2e.ConditionExists(device, v1alpha1.DeviceUpdating, v1alpha1.ConditionStatusTrue, string(v1alpha1.UpdateStateApplyingUpdate))
				}, TIMEOUT)

			Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1alpha1.Device) bool {
					return e2e.ConditionExists(device, v1alpha1.DeviceUpdating, v1alpha1.ConditionStatusTrue, string(v1alpha1.UpdateStateRebooting))
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Rebooting")))

			harness.WaitForDeviceContents(deviceId, "Updated to desired renderedVersion: 3",
				func(device *v1alpha1.Device) bool {
					for _, condition := range device.Status.Conditions {
						if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
							condition.Message == "Updated to desired renderedVersion: 3" {
							return true
						}
					}
					return false
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

			logrus.Infof("Device updated to new image %s 🎉", "flightctl-device:base")
			Expect(device.Spec.Applications).To(BeNil())
			logrus.Info("Application demo_embedded_app is not present in new image 🌞")

			stdout1, err1 := harness.VM.RunSSH([]string{"sudo", "podman", "ps"}, nil)
			Expect(err1).NotTo(HaveOccurred())
			Expect(stdout1.String()).NotTo(ContainSubstring("sleep infinity"))

			logrus.Info("Went back to base image and checked that there is no application now👌")
		})
		It("Should resolve to the latest version when multiple updates are applied", Label("77672", "sanity"), func() {
			initialVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())

			By("Setting up extra dependencies for future device spec applications")
			err = harness.ReplaceRepository(spec, repoMetadata)
			Expect(err).NotTo(HaveOccurred())
			// clean up the repository when we're done with it
			defer func() {
				err := harness.DeleteRepository(*repoMetadata.Name)
				if err != nil {
					logrus.Errorf("Failed to delete repository %s: %v", *repoMetadata.Name, err)
				}
			}()

			// Add more factories here if desired. The first spec applied will add a repo spec
			// and the second a simple inline spec.
			type providerFactory = func(providerSpec *v1alpha1.ConfigProviderSpec) error
			configFactories := []providerFactory{
				func(providerSpec *v1alpha1.ConfigProviderSpec) error {
					return providerSpec.FromHttpConfigProviderSpec(flightDemosHttpRepoConfig)
				},
				func(providerSpec *v1alpha1.ConfigProviderSpec) error {
					return providerSpec.FromInlineConfigProviderSpec(validInlineConfig)
				},
			}

			// Apply each spec in quick succession, just waiting for the device to register that it
			// has acknowledged it should update
			currentVersion := initialVersion
			for i, factory := range configFactories {
				specVersion := i + 1
				By(fmt.Sprintf("Applying spec: %d", specVersion))
				harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
					var configProviderSpec v1alpha1.ConfigProviderSpec
					err := factory(&configProviderSpec)
					Expect(err).ToNot(HaveOccurred())
					// append, not overwrite, new specs
					specs := lo.FromPtr(device.Spec.Config)
					specs = append(specs, configProviderSpec)
					device.Spec.Config = &specs
					logrus.Infof("Updating %s with config %+v", deviceId, *device.Spec.Config)
				})
				expectedVersion := currentVersion + 1
				desc := fmt.Sprintf("Updating to desired renderedVersion: %d", expectedVersion)
				By(fmt.Sprintf("Waiting for update %d to be picked up", specVersion))
				harness.WaitForDeviceContents(deviceId, desc, func(device *v1alpha1.Device) bool {
					return isDeviceUpdateObserved(device, expectedVersion)
				}, TIMEOUT)
				currentVersion = expectedVersion
			}

			By(fmt.Sprintf("applying all defined specs, the end version should indicate %d updates were applied", len(configFactories)))
			expectedVersion := initialVersion + len(configFactories)
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, expectedVersion)
			Expect(err).NotTo(HaveOccurred())
		})
		It("Should rollback when updating to a broken image", Label("82481", "sanity"), func() {
			expectedVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())
			dev, err := harness.GetDevice(deviceId)
			Expect(err).NotTo(HaveOccurred())
			initialImage := dev.Status.Os.Image
			// The v8 image should contain a bad compose file
			harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":v8")

			harness.WaitForDeviceContents(deviceId, "device image should be updated to the new image", func(device *v1alpha1.Device) bool {
				return device.Spec.Os.Image != initialImage
			}, TIMEOUT)

			// There is currently a bug https://issues.redhat.com/browse/EDM-1365
			// that prevents the device from rolling back to the initial image
			// When that bug is fixed, the following assertions will need to change.

			harness.WaitForDeviceContents(deviceId, "device status should indicate updating failure", func(device *v1alpha1.Device) bool {
				return e2e.ConditionExists(device, string(v1alpha1.DeviceUpdating), string(v1alpha1.ConditionStatusFalse), string(v1alpha1.UpdateStateError))
			}, LONGTIMEOUT)

			// Verify that the flightctl-agent logs indicate that a rollback was attempted
			dur, err := time.ParseDuration(TIMEOUT)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() string {
				logrus.Infof("Checking console output for rollback logs")
				return harness.AgentLogs(harness.VM)
			}).
				WithContext(harness.Context).
				WithTimeout(dur).
				WithPolling(time.Second * 10).
				Should(ContainSubstring(fmt.Sprintf("Attempting to rollback to previous renderedVersion: %d", expectedVersion)))

			harness.WaitForDeviceContents(deviceId, "device should become out of date but be online", func(device *v1alpha1.Device) bool {
				return device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusOutOfDate &&
					device.Status.Summary.Status == v1alpha1.DeviceSummaryStatusOnline
			}, TIMEOUT)

			// validate that the error message contains an indication of why the update failed
			dev, err = harness.GetDevice(deviceId)
			Expect(err).NotTo(HaveOccurred())
			cond := v1alpha1.FindStatusCondition(dev.Status.Conditions, v1alpha1.DeviceUpdating)
			Expect(cond).ToNot(BeNil())
			Expect(cond.Message).To(And(ContainSubstring("Failed to update to renderedVersion"), ContainSubstring("validating compose spec")))

			/*
				** Add this assertion back when the bug referenced above is fixed **
				harness.WaitForDeviceContents(deviceId, "device image should be reverted to the old image", func(device *v1alpha1.Device) bool {
					return device.Spec.Os.Image == initialImage
				}, TIMEOUT)
			*/
		})
	})
})

var flightDemosHttpRepoConfig = v1alpha1.HttpConfigProviderSpec{
	HttpRef: struct {
		FilePath   string  `json:"filePath"`
		Repository string  `json:"repository"`
		Suffix     *string `json:"suffix,omitempty"`
	}{
		FilePath:   "/etc/config",
		Repository: validRepoName,
		Suffix:     nil,
	},
	Name: "flightctl-demos-cfg",
}

// returns true if the device is updating or has already updated to the expected version
func isDeviceUpdateObserved(device *v1alpha1.Device, expectedVersion int) bool {
	version, err := e2e.GetRenderedVersion(device)
	if err != nil {
		logrus.Errorf("Failed to parse rendered version '%s': %v", device.Status.Config.RenderedVersion, err)
		return false
	}
	// The update has already applied
	if version == expectedVersion {
		return true
	}
	cond := v1alpha1.FindStatusCondition(device.Status.Conditions, v1alpha1.DeviceUpdating)
	if cond == nil {
		return false
	}
	// send another update if we're in this state
	validReasons := []v1alpha1.UpdateState{
		v1alpha1.UpdateStatePreparing,
		v1alpha1.UpdateStateReadyToUpdate,
		v1alpha1.UpdateStateApplyingUpdate,
	}
	return slices.Contains(validReasons, v1alpha1.UpdateState(cond.Reason))
}
