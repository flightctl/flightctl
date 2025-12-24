package agent_test

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/api/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VM Agent behavior", func() {
	Context("vm", func() {
		It("Verify VM agent", Label("80455", "rpm-sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("should print QR output to console")
			// Wait for the top-most part of the QR output to appear
			Eventually(harness.GetFlightctlAgentLogs, TIMEOUT, POLLING).Should(ContainSubstring("████████████████████████████████"))

			By("should have flightctl-agent running")
			var stdout *bytes.Buffer
			var err error
			stdout, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("Active: active (running)"))

			By("The agent executable should have the proper SELinux domain")
			stdout, err = harness.VM.RunSSH([]string{"sudo", "ls", "-Z", "/usr/bin/flightctl-agent"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("flightctl_agent_exec_t"))
		})

		It("Verifying generation of enrollment request link", Label("75518"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("should be reporting device status on enrollment request")
			// Get the enrollment Request ID from the service logs
			enrollmentID := harness.GetEnrollmentIDFromServiceLogs("flightctl-agent")
			GinkgoWriter.Printf("Enrollment ID found in flightctl-agent service logs: %s\n", enrollmentID)

			// Wait for the device to create the enrollment request, and check the TPM details
			enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
			Expect(enrollmentRequest.Spec).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus.SystemInfo.IsEmpty()).NotTo(BeTrue())

			// Approve the enrollment and wait for the device details to be populated by the agent
			harness.ApproveEnrollment(enrollmentID, harness.TestEnrollmentApproval())
			GinkgoWriter.Printf("Waiting for device %s to report status\n", enrollmentID)

			// wait for the device to pickup enrollment and report measurements on device status
			Eventually(func() *apiclient.GetDeviceResponse {
				resp, err := harness.GetDeviceWithStatusSystem(enrollmentID)
				Expect(err).ToNot(HaveOccurred())
				return resp
			}, TIMEOUT, POLLING).ShouldNot(BeNil())
		})
		It("Should report a message when a device is assigned to multiple fleets", Label("75992"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			const (
				fleet1Name  = "fleet1"
				fleet2Name  = "fleet2"
				fleet1Label = "region"
				fleet2Label = "environment"
				fleet1Value = "world"
				fleet2Value = "prod"
			)
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			currentVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			// ensure we start with an empty template
			err = harness.SetLabelsForDevice(deviceId, nil)
			Expect(err).ToNot(HaveOccurred())

			By("creating the first fleet")
			var configProviderSpec v1beta1.ConfigProviderSpec
			err = configProviderSpec.FromInlineConfigProviderSpec(validInlineConfig)
			Expect(err).ToNot(HaveOccurred())
			err = harness.CreateTestFleetWithConfig(fleet1Name, v1beta1.LabelSelector{
				MatchLabels: &map[string]string{
					fleet1Label: fleet1Value,
				},
			}, configProviderSpec)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = harness.DeleteFleet(fleet1Name) }()

			By("creating a second fleet")
			configCopy := inlineConfig
			configCopy.Content = fmt.Sprintf("%s %s", configCopy.Content, fleet2Name)
			err = configProviderSpec.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{
				Inline: []v1beta1.FileSpec{configCopy},
				Name:   "second-fleet-config",
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.CreateTestFleetWithConfig(fleet2Name, v1beta1.LabelSelector{
				MatchLabels: &map[string]string{
					fleet2Label: fleet2Value,
				},
			}, configProviderSpec)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = harness.DeleteFleet(fleet2Name) }()

			By("setting multiple labels for a device with conflicting fleets a condition should be applied")
			err = harness.SetLabelsForDevice(deviceId, map[string]string{
				fleet1Label: fleet1Value,
				fleet2Label: fleet2Value,
			})
			Expect(err).ToNot(HaveOccurred())
			harness.WaitForDeviceContents(deviceId, "multiple owners condition should be applied", func(device *v1beta1.Device) bool {
				return e2e.ConditionStatusExists(device.Status.Conditions, v1beta1.ConditionTypeDeviceMultipleOwners, v1beta1.ConditionStatusTrue)
			}, TIMEOUT)

			// verify that the conflicting fleets are applied to the error message
			device, err := harness.GetDevice(deviceId)
			Expect(err).ToNot(HaveOccurred())
			cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceMultipleOwners)
			Expect(cond).ToNot(BeNil())
			Expect(cond.Message).Should(And(ContainSubstring(fleet1Name), ContainSubstring(fleet2Name)))
			// No updates from the fleet should have been applied
			Expect(device.Status.Config.RenderedVersion).To(Equal(strconv.Itoa(currentVersion)))
			Expect(device.Metadata.Owner).To(BeNil(), fmt.Sprintf("%+v", *device))

			By("resetting the labels should remove the condition from the device")
			err = harness.SetLabelsForDevice(deviceId, nil)
			Expect(err).ToNot(HaveOccurred())
			harness.WaitForDeviceContents(deviceId, "multiple owners condition should be removed", func(device *v1beta1.Device) bool {
				return e2e.ConditionStatusExists(device.Status.Conditions, v1beta1.ConditionTypeDeviceMultipleOwners, v1beta1.ConditionStatusFalse)
			}, TIMEOUT)

			By("adding a label to a matching fleet, the device should update its rendered version")
			expectedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.SetLabelsForDevice(deviceId, map[string]string{
				fleet1Label: fleet1Value,
			})
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, expectedVersion)
			Expect(err).ToNot(HaveOccurred())
			device, err = harness.GetDevice(deviceId)
			Expect(err).ToNot(HaveOccurred())
			cond = v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceMultipleOwners)
			Expect(cond).ToNot(BeNil())
			Expect(cond.Status).To(Equal(v1beta1.ConditionStatusFalse))
			Expect(device.Metadata.Owner).ToNot(BeNil())
			Expect(*device.Metadata.Owner).To(Equal(fmt.Sprintf("Fleet/%s", fleet1Name)))

			By("readding both labels should add the multiple owners condition back")
			err = harness.SetLabelsForDevice(deviceId, map[string]string{
				fleet1Label: fleet1Value,
				fleet2Label: fleet2Value,
			})
			Expect(err).ToNot(HaveOccurred())
			harness.WaitForDeviceContents(deviceId, "multiple owners condition should be reapplied", func(device *v1beta1.Device) bool {
				return e2e.ConditionStatusExists(device.Status.Conditions, v1beta1.ConditionTypeDeviceMultipleOwners, v1beta1.ConditionStatusTrue)
			}, TIMEOUT)

			// verify that the conflicting fleets are applied to the error message
			device, err = harness.GetDevice(deviceId)
			Expect(err).ToNot(HaveOccurred())
			cond = v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceMultipleOwners)
			Expect(cond).ToNot(BeNil())
			Expect(cond.Message).Should(And(ContainSubstring(fleet1Name), ContainSubstring(fleet2Name)))
		})
	})

	Context("status", func() {
		It("Device status tests", Label("75991", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			deviceId, device := harness.EnrollAndWaitForOnlineStatus()
			// Get the next expected rendered version
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("should report the correct device status after an inline config is added")

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {

				// Create ConfigProviderSpec.
				var configProviderSpec v1beta1.ConfigProviderSpec
				err := configProviderSpec.FromInlineConfigProviderSpec(validInlineConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{configProviderSpec}
				GinkgoWriter.Printf("Updating %s with config %s\n", deviceId, device.Spec.Config)
			})
			Expect(err).ToNot(HaveOccurred())

			GinkgoWriter.Printf("Waiting for the device to pick the config\n")
			harness.WaitForDeviceContents(deviceId, fmt.Sprintf("the device is updated to renderedVersion: %s", strconv.Itoa(newRenderedVersion)),
				func(device *v1beta1.Device) bool {
					for _, condition := range device.Status.Conditions {
						if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
							condition.Message == UpdateRenderedVersionSuccess.String() {
							return true
						}
					}
					return false
				}, TIMEOUT)

			// The device should have the online config.
			stdout, err := harness.VM.RunSSH([]string{"cat", "/etc/motd"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("This system is managed by flightctl."))

			// The status should be "Online"
			GinkgoWriter.Printf("The device has the config %s\n", device.Spec.Config)
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusOnline))

			By("should report the correct device status when trying to upgrade to a not existing image")
			previousRenderedVersion := newRenderedVersion
			newRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			var newImageReference string
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				currentImage := device.Status.Os.Image
				GinkgoWriter.Printf("Current image for %s is %s\n", deviceId, currentImage)
				repo, _ := parseImageReference(currentImage)
				newImageReference = repo + ":not-existing"
				device.Spec.Os = &v1beta1.DeviceOsSpec{Image: newImageReference}
				GinkgoWriter.Printf("Updating %s to image %s\n", deviceId, device.Spec.Os.Image)
			})
			Expect(err).ToNot(HaveOccurred())

			harness.WaitForDeviceContents(deviceId, fmt.Sprintf("Failed to update to renderedVersion: %s. Error", strconv.Itoa(newRenderedVersion)),
				func(device *v1beta1.Device) bool {
					// returning true if it is reported an error status or if the device is rolled back to the previous version
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError)) ||
						(e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateUpdated)) && (device.Status.Config.RenderedVersion == strconv.Itoa(previousRenderedVersion)))
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithUpdateStatus, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))

			By(`should show an error when trying to update a device with
				"a reference to a not existing git repo, and report 'Online' status`)

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				GinkgoWriter.Printf("Current device is %s\n", deviceId)

				// Create ConfigProviderSpec.
				var configProviderSpec v1beta1.ConfigProviderSpec
				err := configProviderSpec.FromGitConfigProviderSpec(gitConfigInvalidRepo)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{configProviderSpec}
				GinkgoWriter.Printf("Updating %s with config %s\n", deviceId, device.Spec.Config)
			})
			Expect(err).ToNot(HaveOccurred())

			// Check the http config error is detected.
			harness.WaitForDeviceContents(deviceId, `Error: failed fetching specified Repository definition`,
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceSpecValid, v1beta1.ConditionStatusFalse, "Invalid")
				}, TIMEOUT)

			harness.WaitForDeviceContents(deviceId, fmt.Sprintf("Failed to update to renderedVersion: %s", strconv.Itoa(newRenderedVersion)),
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError))
				}, TIMEOUT)
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusOnline))

			By(`should show an error when trying to update a device with a httpConfigProviderSpec
			with invalid Path, and report 'Online' status`)
			// Create the http repository.
			_, err = model.NewRepositoryFromApiResource(&httpRepo)
			Expect(err).ToNot(HaveOccurred())

			// Update the device with the http invalid config.
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				GinkgoWriter.Printf("current device is %s\n", deviceId)
				// Create ConfigProviderSpec.
				var configProviderSpec v1beta1.ConfigProviderSpec
				err := configProviderSpec.FromHttpConfigProviderSpec(httpConfigInvalidPath)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{configProviderSpec}
				GinkgoWriter.Printf("updating %s with config %s\n", deviceId, device.Spec.Config)
			})
			Expect(err).ToNot(HaveOccurred())

			// Check the http config error is detected.
			harness.WaitForDeviceContents(deviceId, "Error: sending HTTP Request",
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceSpecValid, v1beta1.ConditionStatusFalse, "Invalid")
				}, TIMEOUT)

			harness.WaitForDeviceContents(deviceId, fmt.Sprintf("Failed to update to renderedVersion: %s", strconv.Itoa(newRenderedVersion)),
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError))
				}, TIMEOUT)
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusOnline))

			By("should report 'Unknown' after the device vm is powered-off")
			//find a better way to test this without waiting for 10 min
			// Shutdown the vm.
			//err = harness.VM.Shutdown()
			//Expect(err).ToNot(HaveOccurred())
			//Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
			//deviceId).Should(Equal(v1beta1.DeviceSummaryStatusUnknown))

		})

		It("K8s secret config source", Label("76687"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

			// Get the next expected rendered version
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("should report the correct device status after an inline config is added")

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {

				// Create ConfigProviderSpec.
				var configProviderSpec v1beta1.ConfigProviderSpec
				err := configProviderSpec.FromKubernetesSecretProviderSpec(k8sSecretConfig)
				Expect(err).ToNot(HaveOccurred())

				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{configProviderSpec}
				GinkgoWriter.Printf("Updating %s with config %s\n", deviceId, device.Spec.Config)
			})
			Expect(err).ToNot(HaveOccurred())

			GinkgoWriter.Printf("Waiting for the device to pick the config\n")
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			// The device should have the online config.
			stdout, err := harness.VM.RunSSH([]string{"cat", "/etc/testfile.txt"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("This is used to test k8s secret config."))
		})

		It("System Info Timeout Tests", Label("81864"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Enroll and wait for image v9 to become online")
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V9)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Reload the flight agent")
			_, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "reload", "flightctl-agent"}, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Custom system-info infinite is empty due to timeout")
			// wait for the device to pickup enrollment and report measurements on device status
			Eventually(harness.GetDeviceWithStatusSystem, TIMEOUT, POLLING).WithArguments(deviceId).ShouldNot(BeNil())

			response, err := harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			device := response.JSON200

			// Ensure the infinite key exists in CustomInfo
			Expect(device.Status.SystemInfo.CustomInfo).ToNot(BeNil())
			Expect((*device.Status.SystemInfo.CustomInfo)).To(HaveKey("infinite"))
			Expect((*device.Status.SystemInfo.CustomInfo)["infinite"]).To(Equal(""))

		})
	})
	Context("Resources", func() {
		It("Alert Validation Rules", Label("78853"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			const fleet1Name = "fleet1"
			const fleet1Value = "US"
			const fleet1Label = "region"

			By("creating the fleet for the test")
			var configProviderSpec v1beta1.ConfigProviderSpec
			err := configProviderSpec.FromInlineConfigProviderSpec(validInlineConfig)
			Expect(err).ToNot(HaveOccurred())
			err = harness.CreateTestFleetWithConfig(fleet1Name, v1beta1.LabelSelector{
				MatchLabels: &map[string]string{
					fleet1Label: fleet1Value,
				},
			}, configProviderSpec)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = harness.DeleteFleet(fleet1Name) }()

			By("assigning the label to the device")
			expectedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.SetLabelsForDevice(deviceId, map[string]string{
				fleet1Label: fleet1Value,
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, expectedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Should failed with invalid sampling interval")
			err = harness.UpdateFleet(fleet1Name, func(fleet *v1beta1.Fleet) {
				// Create a ResourceMonitorSpec with an invalid sampling interval
				resourceMonitorSpec := v1beta1.DiskResourceMonitorSpec{
					SamplingInterval: "5x",
					Path:             "/run/usr/1000",

					AlertRules: []v1beta1.ResourceAlertRule{
						{
							Severity:    "Info",
							Duration:    "10s",
							Percentage:  5,
							Description: "some metric",
						},
					},
				}

				// Create a ResourceMonitor using the spec
				resourceMonitor := v1beta1.ResourceMonitor{}
				err := resourceMonitor.FromDiskResourceMonitorSpec(resourceMonitorSpec)
				Expect(err).ToNot(HaveOccurred())
				// Set fleet.Spec.Resources to a slice containing only this resource monitor
				fleet.Spec.Template.Spec.Resources = &[]v1beta1.ResourceMonitor{resourceMonitor}
				GinkgoWriter.Printf("Updating %s with resources %v\n", fleet1Name, fleet.Spec.Template.Spec.Resources)
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`/spec/template/spec/resources/0": Error at "/samplingInterval": string doesn't match the regular expression "^[1-9]\d*[smh]$`))

			By("Should fail with invalid percentage")
			err = harness.UpdateFleet(fleet1Name, func(fleet *v1beta1.Fleet) {
				resourceMonitorSpec := v1beta1.DiskResourceMonitorSpec{
					SamplingInterval: "5s",
					Path:             "/run/user/1000",
					AlertRules: []v1beta1.ResourceAlertRule{
						{
							Severity:    "Critical",
							Duration:    "10s",
							Percentage:  -5, // Invalid percentage
							Description: "Invalid percentage test",
						},
					},
				}
				resourceMonitor := v1beta1.ResourceMonitor{}
				err := resourceMonitor.FromDiskResourceMonitorSpec(resourceMonitorSpec)
				Expect(err).ToNot(HaveOccurred())
				fleet.Spec.Template.Spec.Resources = &[]v1beta1.ResourceMonitor{resourceMonitor}
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`percentage must be between 0 and 100`))

			By("Should fail with invalid duration")
			err = harness.UpdateFleet(fleet1Name, func(fleet *v1beta1.Fleet) {
				resourceMonitorSpec := v1beta1.DiskResourceMonitorSpec{
					SamplingInterval: "5s",
					Path:             "/run/user/1000",
					AlertRules: []v1beta1.ResourceAlertRule{
						{
							Severity:    "Critical",
							Duration:    "10x", // Invalid duration
							Percentage:  50,
							Description: "Invalid duration test",
						},
					},
				}
				resourceMonitor := v1beta1.ResourceMonitor{}
				err := resourceMonitor.FromDiskResourceMonitorSpec(resourceMonitorSpec)
				Expect(err).ToNot(HaveOccurred())
				fleet.Spec.Template.Spec.Resources = &[]v1beta1.ResourceMonitor{resourceMonitor}
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`Error at "/alertRules/0/duration": string doesn't match the regular expression "^\d+[smh]$"`))

			By("Should fail when sampling interval is longer than duration")
			err = harness.UpdateFleet(fleet1Name, func(fleet *v1beta1.Fleet) {
				resourceMonitorSpec := v1beta1.DiskResourceMonitorSpec{
					SamplingInterval: "5s", // longer than duration
					Path:             "/run/user/1000",
					AlertRules: []v1beta1.ResourceAlertRule{
						{
							Severity:    "Critical",
							Duration:    "1s",
							Percentage:  50,
							Description: "Sampling interval longer than duration",
						},
					},
				}
				resourceMonitor := v1beta1.ResourceMonitor{}
				err := resourceMonitor.FromDiskResourceMonitorSpec(resourceMonitorSpec)
				Expect(err).ToNot(HaveOccurred())
				fleet.Spec.Template.Spec.Resources = &[]v1beta1.ResourceMonitor{resourceMonitor}
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`sampling interval 5s must be less than the duration: 1s`))

			By("Should fail with invalid severity")
			err = harness.UpdateFleet(fleet1Name, func(fleet *v1beta1.Fleet) {
				resourceMonitorSpec := v1beta1.DiskResourceMonitorSpec{
					SamplingInterval: "5s",
					Path:             "/run/user/1000",
					AlertRules: []v1beta1.ResourceAlertRule{
						{
							Severity:    "Severe", // Invalid severity
							Duration:    "10s",
							Percentage:  50,
							Description: "Invalid severity test",
						},
					},
				}
				resourceMonitor := v1beta1.ResourceMonitor{}
				err := resourceMonitor.FromDiskResourceMonitorSpec(resourceMonitorSpec)
				Expect(err).ToNot(HaveOccurred())
				fleet.Spec.Template.Spec.Resources = &[]v1beta1.ResourceMonitor{resourceMonitor}
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`Error at "/alertRules/0/severity": value is not one of the allowed values ["Warning","Critical","Info"]`))

			By("Should fail with duplicate severity")
			err = harness.UpdateFleet(fleet1Name, func(fleet *v1beta1.Fleet) {
				resourceMonitorSpec := v1beta1.DiskResourceMonitorSpec{
					SamplingInterval: "5s",
					Path:             "/run/user/1000",
					AlertRules: []v1beta1.ResourceAlertRule{
						{
							Severity:    "Critical",
							Duration:    "55s",
							Percentage:  1,
							Description: "Disk space for application data is >1% full for over 55s.",
						},
						{
							Severity:    "Critical",
							Duration:    "10m",
							Percentage:  90,
							Description: "Disk space for application data is >90% full over 10m.",
						},
					},
				}
				resourceMonitor := v1beta1.ResourceMonitor{}
				err := resourceMonitor.FromDiskResourceMonitorSpec(resourceMonitorSpec)
				Expect(err).ToNot(HaveOccurred())
				fleet.Spec.Template.Spec.Resources = &[]v1beta1.ResourceMonitor{resourceMonitor}
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`duplicate alertRule severity: Critical`))
		})
	})
})

// mode defines the file permission bits, commonly used in Unix systems for files and directories.
var mode = 0644
var modePointer = &mode

// inlineConfig defines a file specification with content, mode, and path for provisioning system files.
var inlineConfig = v1beta1.FileSpec{
	Content: "This system is managed by flightctl.",
	Mode:    modePointer,
	Path:    "/etc/motd",
}

// validInlineConfig defines a valid inline configuration provider spec with pre-defined file specs and a name.
var validInlineConfig = v1beta1.InlineConfigProviderSpec{
	Inline: []v1beta1.FileSpec{inlineConfig},
	Name:   "valid-inline-config",
}

var validRepoName = "flightctl-demos"
var repoMetadata = v1beta1.ObjectMeta{
	Name: &validRepoName,
}

// httpRepoSpec initializes an HttpRepoSpec with an HTTP repository type and URL for clone or access operations.
var httpRepoSpec = v1beta1.HttpRepoSpec{
	Type: v1beta1.RepoSpecType("http"),
	Url:  "https://github.com/flightctl/flightctl-demos.git",
}

// spec is a variable of type RepositorySpec used to describe configuration for a repository.
var spec v1beta1.RepositorySpec
var _ = spec.FromHttpRepoSpec(httpRepoSpec)

// httpRepo represents a v1beta1.Repository with predefined ApiVersion, Kind, Metadata, and Spec values.
var httpRepo = v1beta1.Repository{
	ApiVersion: "v1beta1",
	Kind:       "Repository",
	Metadata:   repoMetadata,
	Spec:       spec,
}

// gitConfigInvalidRepo defines a GitConfigProviderSpec with an invalid repository name ("not-existing-repo") for test purposes.
var gitConfigInvalidRepo = v1beta1.GitConfigProviderSpec{
	GitRef: struct {
		Path           string `json:"path"`
		Repository     string `json:"repository"`
		TargetRevision string `json:"targetRevision"`
	}{
		Path:           "/configs/repo/config.yaml",
		Repository:     "not-existing-repo",
		TargetRevision: "main",
	},
	Name: "example-git-config-provider",
}

var k8sSecretConfig = v1beta1.KubernetesSecretProviderSpec{
	Name: "example-k8s-secret-config-provider",
	SecretRef: struct {
		MountPath string "json:\"mountPath\""
		Name      string "json:\"name\""
		Namespace string "json:\"namespace\""
	}{
		MountPath: "/etc",
		Name:      "test-config",
		Namespace: "flightctl-e2e",
	},
}

// suffix specifies a default path segment or query parameter that can be appended to a URL in HTTP configuration.
var suffix = "/some/suffix"

// httpConfigInvalidPath defines an invalid HTTP configuration with a non-existent file path for testing scenarios.
var httpConfigInvalidPath = v1beta1.HttpConfigProviderSpec{
	HttpRef: struct {
		FilePath   string  `json:"filePath"`
		Repository string  `json:"repository"`
		Suffix     *string `json:"suffix,omitempty"`
	}{
		FilePath:   "/invalid/path",
		Repository: "flightctl-demos",
		Suffix:     &suffix,
	},
	Name: "example-http-config-provider",
}

// parseImageReference splits an image reference string into the repository and tag components.
// It returns the repository and tag as separate strings.
// If no tag is present, the returned tag string will be empty.
func parseImageReference(image string) (string, string) {
	// Split the image string by the colon to separate the repository and the tag.
	parts := strings.Split(image, ":")

	tag := ""
	repo := ""

	// The tag is the last part after the last colon.
	if len(parts) > 1 {
		tag = parts[len(parts)-1]
		// The repository is composed of all parts before the last colon, joined back together with colons.
		repo = strings.Join(parts[:len(parts)-1], ":")
	}

	return repo, tag
}
