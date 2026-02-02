package agent_test

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VM Agent behavior during updates", func() {
	var (
		deviceId string
	)

	BeforeEach(func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		// The harness is already set up with VM from pool and agent started
		// We just need to enroll the device
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	Context("updates", func() {
		It("should update to the requested image", Label("75523"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Verifying update to agent with requested image")
			device, newImageReference, err := harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V2)
			Expect(err).ToNot(HaveOccurred())

			currentImage := device.Status.Os.Image
			GinkgoWriter.Printf("Current image is: %s\n", currentImage)
			GinkgoWriter.Printf("New image is: %s\n", newImageReference)

			harness.WaitForDeviceContents(deviceId, "The device is preparing an update to renderedVersion: 2",
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusTrue, string(v1beta1.UpdateStateApplyingUpdate))
				}, LONGTIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusOnline))

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusTrue, string(v1beta1.UpdateStateRebooting))
				}, LONGTIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusRebooting))

			harness.WaitForDeviceContents(deviceId, "Updated to desired renderedVersion: 2",
				func(device *v1beta1.Device) bool {
					for _, condition := range device.Status.Conditions {
						if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
							condition.Message == UpdateRenderedVersionSuccess.String() {
							return true
						}
					}
					return false
				}, TIMEOUT)
			GinkgoWriter.Printf("Device updated to new image 🎉\n")
		})

		It("Should update to v4 with embedded application", Label("77671", "sanity", "agent"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Verifying update to agent with embedded application")

			device, newImageReference, err := harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V4)
			Expect(err).ToNot(HaveOccurred())

			currentImage := device.Status.Os.Image
			GinkgoWriter.Printf("Current image is: %s\n", currentImage)
			GinkgoWriter.Printf("New image is: %s\n", newImageReference)

			harness.WaitForDeviceContents(deviceId, "The device is preparing an update to renderedVersion: 2",
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusTrue, string(v1beta1.UpdateStateApplyingUpdate))
				}, LONGTIMEOUT)

			Expect(device.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusOnline))

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusTrue, string(v1beta1.UpdateStateRebooting))
				}, LONGTIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusRebooting))

			harness.WaitForDeviceContents(deviceId, "Updated to desired renderedVersion: 2",
				func(device *v1beta1.Device) bool {
					for _, condition := range device.Status.Conditions {
						if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
							condition.Message == UpdateRenderedVersionSuccess.String() {
							return true
						}
					}
					return false
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusOnline))

			GinkgoWriter.Printf("Device updated to new image %s 🎉\n", util.NewDeviceImageReference(util.DeviceTags.V4).String())
			GinkgoWriter.Printf("We expect containers with sleep infinity process to be present but not running\n")
			stdout, err := harness.VM.RunSSH([]string{"sudo", "podman", "ps"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("sleep infinity"))

			GinkgoWriter.Printf("We expect podman containers with sleep infinity process to be present but not running 👌\n")

			device, newImageReference, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.Base)
			Expect(err).ToNot(HaveOccurred())

			currentImage = device.Status.Os.Image
			GinkgoWriter.Printf("Current image is: %s\n", currentImage)
			GinkgoWriter.Printf("New image is: %s\n", newImageReference)

			harness.WaitForDeviceContents(deviceId, "The device is preparing an update to renderedVersion: 3",
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusTrue, string(v1beta1.UpdateStateApplyingUpdate))
				}, TIMEOUT)

			Expect(device.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusType("Online")))

			harness.WaitForDeviceContents(deviceId, "the device is rebooting",
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusTrue, string(v1beta1.UpdateStateRebooting))
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusType("Rebooting")))

			harness.WaitForDeviceContents(deviceId, "Updated to desired renderedVersion: 3",
				func(device *v1beta1.Device) bool {
					for _, condition := range device.Status.Conditions {
						if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
							condition.Message == "Updated to desired renderedVersion: 3" {
							return true
						}
					}
					return false
				}, TIMEOUT)

			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusType("Online")))

			GinkgoWriter.Printf("Device updated to new image %s 🎉\n", util.NewDeviceImageReference(util.DeviceTags.Base).String())
			Expect(device.Spec.Applications).To(BeNil())
			GinkgoWriter.Printf("Application demo_embedded_app is not present in new image 🌞\n")

			stdout1, err1 := harness.VM.RunSSH([]string{"sudo", "podman", "ps"}, nil)
			Expect(err1).NotTo(HaveOccurred())
			Expect(stdout1.String()).NotTo(ContainSubstring("sleep infinity"))

			GinkgoWriter.Printf("Went back to base image and checked that there is no application now👌\n")

			By("The agent executable should have the proper SELinux domain after the upgrade")
			stdout, err = harness.VM.RunSSH([]string{"sudo", "ls", "-Z", "/usr/bin/flightctl-agent"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("flightctl_agent_exec_t"))
		})

		It("Should resolve to the latest version when multiple updates are applied", Label("77672"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			initialVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())

			By("Setting up extra dependencies for future device spec applications")
			err = harness.ReplaceRepository(spec, repoMetadata)
			Expect(err).NotTo(HaveOccurred())
			// clean up the repository when we're done with it
			defer func() {
				err := harness.DeleteRepository(*repoMetadata.Name)
				if err != nil {
					GinkgoWriter.Printf("Failed to delete repository %s: %v\n", *repoMetadata.Name, err)
				}
			}()

			// Add more factories here if desired. The first spec applied will add a repo spec
			// and the second a simple inline spec.
			type providerFactory = func(providerSpec *v1beta1.ConfigProviderSpec) error
			configFactories := []providerFactory{
				func(providerSpec *v1beta1.ConfigProviderSpec) error {
					return providerSpec.FromHttpConfigProviderSpec(flightDemosHttpRepoConfig)
				},
				func(providerSpec *v1beta1.ConfigProviderSpec) error {
					return providerSpec.FromInlineConfigProviderSpec(validInlineConfig)
				},
			}

			// Apply each spec in quick succession, just waiting for the device to register that it
			// has acknowledged it should update
			currentVersion := initialVersion
			for i, factory := range configFactories {
				specVersion := i + 1
				By(fmt.Sprintf("Applying spec: %d", specVersion))
				var configProviderSpec v1beta1.ConfigProviderSpec
				err := factory(&configProviderSpec)
				Expect(err).ToNot(HaveOccurred())
				err = harness.AddConfigToDeviceWithRetries(deviceId, configProviderSpec)
				Expect(err).ToNot(HaveOccurred())
				expectedVersion := currentVersion + 1
				desc := fmt.Sprintf("Updating to desired renderedVersion: %d", expectedVersion)
				By(fmt.Sprintf("Waiting for update %d to be picked up", specVersion))
				harness.WaitForDeviceContents(deviceId, desc, func(device *v1beta1.Device) bool {
					return e2e.IsDeviceUpdateObserved(device, expectedVersion)
				}, TIMEOUT)
				currentVersion = expectedVersion
			}

			By(fmt.Sprintf("applying all defined specs, the end version should indicate %d updates were applied", len(configFactories)))
			expectedVersion := initialVersion + len(configFactories)
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, expectedVersion)
			Expect(err).NotTo(HaveOccurred())
		})
		It("Should rollback when updating to a broken image", Label("82481", "sanity", "agent"), func() {
			Skip("Test temporarily disabled, re-enable after EDM-3264 is fixed")
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			expectedVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())
			dev, err := harness.GetDevice(deviceId)
			Expect(err).NotTo(HaveOccurred())
			initialImage := dev.Status.Os.Image
			// The v8 image should contain a bad compose file
			_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V8)
			Expect(err).ToNot(HaveOccurred())

			harness.WaitForDeviceContents(deviceId, "device image should be updated to the new image", func(device *v1beta1.Device) bool {
				return device.Spec.Os.Image != initialImage
			}, TIMEOUT)

			// There is currently a bug https://issues.redhat.com/browse/EDM-1365
			// that prevents the device from rolling back to the initial image
			// When that bug is fixed, the following assertions will need to change.

			harness.WaitForDeviceContents(deviceId, "device status should indicate updating failure", func(device *v1beta1.Device) bool {
				return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError))
			}, LONGTIMEOUT)

			// Verify that the flightctl-agent logs indicate that a rollback was attempted
			dur, err := time.ParseDuration(TIMEOUT)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() string {
				GinkgoWriter.Printf("Checking console output for rollback logs\n")
				logs, err := harness.ReadPrimaryVMAgentLogs("", util.FLIGHTCTL_AGENT_SERVICE)
				Expect(err).NotTo(HaveOccurred())
				return logs
			}).
				WithContext(harness.GetTestContext()).
				WithTimeout(dur).
				WithPolling(time.Second * 10).
				Should(ContainSubstring(fmt.Sprintf("Attempting to rollback to previous renderedVersion: %d", expectedVersion)))

			harness.WaitForDeviceContents(deviceId, "device should become out of date but be online", func(device *v1beta1.Device) bool {
				return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate &&
					device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline
			}, TIMEOUT)

			// validate that the error message contains an indication of why the update failed
			dev, err = harness.GetDevice(deviceId)
			Expect(err).NotTo(HaveOccurred())
			cond := v1beta1.FindStatusCondition(dev.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
			Expect(cond).ToNot(BeNil())
			Expect(cond.Message).To(And(
				ContainSubstring("applications"),
				SatisfyAny(
					ContainSubstring("fake-image"),
					ContainSubstring("Invalid value"),
					ContainSubstring("invalid configuration or input"),
					ContainSubstring("service unavailable"),
					ContainSubstring("pulling oci target"),
					ContainSubstring("internal error occurred"),
				),
			)) /*
				** Add this assertion back when the bug referenced above is fixed **
				harness.WaitForDeviceContents(deviceId, "device image should be reverted to the old image", func(device *v1beta1.Device) bool {
					return device.Spec.Os.Image == initialImage
				}, TIMEOUT)
			*/
		})
		It("Should trigger greenboot rollback when agent fails to start", Label("greenboot-rollback", "87279", "sanity", "agent"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Getting initial device state")
			dev, err := harness.GetDevice(deviceId)
			Expect(err).NotTo(HaveOccurred())
			initialStatusImage := dev.Status.Os.Image
			initialBootID := dev.Status.SystemInfo.BootID
			GinkgoWriter.Printf("Initial image: %s\n", initialStatusImage)

			By("Updating device to v11 image (contains systemd drop-in that breaks flightctl-agent)")
			// The v11 image contains a systemd drop-in that causes flightctl-agent to fail
			// This should trigger greenboot rollback after the health check fails
			_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V11)
			Expect(err).ToNot(HaveOccurred())

			harness.WaitForDeviceContents(deviceId, "device spec should be updated to v11", func(device *v1beta1.Device) bool {
				return device.Spec.Os != nil && strings.Contains(device.Spec.Os.Image, "v11")
			}, TIMEOUT)

			By("Waiting for device to start rebooting into v11")
			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusRebooting))

			By("Waiting for greenboot to detect agent failure and trigger OS rollback")
			// The device will reboot into v11, agent fails to start, greenboot detects failure,
			// and triggers rollback. With GREENBOOT_MAX_BOOT_ATTEMPTS=1 (set in BASE image),
			// this should complete in a single boot cycle (~3-5 minutes).
			harness.WaitForDeviceContents(deviceId, "device should rollback to initial OS image and come online", func(device *v1beta1.Device) bool {
				return device.Status.Os.Image == initialStatusImage &&
					device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline
			}, LONGTIMEOUT)

			By("Verifying greenboot triggered an OS rollback (not just a reboot)")
			// "FALLBACK BOOT DETECTED" only appears when greenboot's rollback mechanism was triggered.
			fallbackOutput, err := harness.VM.RunSSH([]string{
				"sudo", "journalctl", "-u", "greenboot-rpm-ostree-grub2-check-fallback.service", "--no-pager",
			}, nil)
			Expect(err).NotTo(HaveOccurred(), "Failed to read greenboot fallback check logs")
			Expect(fallbackOutput.String()).To(ContainSubstring("FALLBACK BOOT DETECTED"),
				"Expected greenboot to detect fallback boot - this confirms OS rollback occurred, not just a reboot")
			GinkgoWriter.Println("Confirmed: greenboot detected 'FALLBACK BOOT DETECTED' - OS rollback verified")

			By("Verifying device reports as OutOfDate after rollback")
			harness.WaitForDeviceContents(deviceId, "device should be out of date after rollback", func(device *v1beta1.Device) bool {
				return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate
			}, TIMEOUT)

			By("Verifying boot ID changed (confirming reboot occurred)")
			harness.WaitForDeviceContents(deviceId, "boot ID should have changed after reboot", func(device *v1beta1.Device) bool {
				return device.Status.SystemInfo.BootID != initialBootID
			}, TIMEOUT)

			GinkgoWriter.Printf("Device successfully rolled back from v11 to %s via greenboot\n", initialStatusImage)
		})
		It("Should respect the spec's update schedule", Label("79220", "sanity", "agent", "slow"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			const everyMinuteExpression = "* * * * *"
			const startGracePeriod v1beta1.Duration = "1m"

			// function for generating a cron expression to execute in a specified number of minutes from the current time
			inNMinutes := func(minutes int) string {

				stdout, err := harness.VM.RunSSH([]string{"date", "-Iseconds"}, nil)
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("Current device time: %s\n", stdout.String())
				// convert the current time to a time.Time object
				timeStr := strings.TrimSpace(stdout.String())
				currentDeviceTime, err := time.Parse(time.RFC3339, timeStr)
				Expect(err).NotTo(HaveOccurred())
				// add minutes to the current time
				minutesFromNow := currentDeviceTime.Add(time.Duration(minutes) * time.Minute)
				// format the time as a cron expression
				inMinutes := fmt.Sprintf("%d * * * *", minutesFromNow.Minute())
				return inMinutes
			}
			// cron is time based and since we can't control when this specific test will run, we do our best to ensure
			// that this test will always succeed whenever it is run.
			yesterday := time.Now().AddDate(0, 0, -1)
			// To ensure an update won't ever occur, we set the update time to midnight yesterday.
			wontUpdatePolicy := fmt.Sprintf("0 0 %d %d *", yesterday.Day(), yesterday.Month())
			currentVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())
			expectedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())

			By("Updating the device with a policy that won't trigger should prevent an update from occurring")
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{newInlineConfigVersion(expectedVersion)}
				device.Spec.UpdatePolicy = &v1beta1.DeviceUpdatePolicySpec{
					UpdateSchedule: &v1beta1.UpdateSchedule{
						At:                 wontUpdatePolicy,
						StartGraceDuration: startGracePeriod,
					},
					DownloadSchedule: &v1beta1.UpdateSchedule{
						At:                 wontUpdatePolicy,
						StartGraceDuration: startGracePeriod,
					},
				}
			})
			Expect(err).ToNot(HaveOccurred())
			harness.WaitForDeviceContents(deviceId, "status should indicate that we are OutOfDate but not updating", func(device *v1beta1.Device) bool {
				if device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusOutOfDate {
					return false
				}
				if device.Status.Config.RenderedVersion != strconv.Itoa(currentVersion) {
					return false
				}
				return true
			}, TIMEOUT)
			// A reasonable amount of time spent polling to ensure the spec doesn't change
			harness.EnsureDeviceContents(deviceId, "the spec contents should not apply", func(device *v1beta1.Device) bool {
				return device.Status.Config.RenderedVersion == strconv.Itoa(currentVersion)
			}, "1m30s")

			By("Reducing the policies, the spec should be applied")
			// pick a time two minutes in the future so that we can confirm that we wait at least some time before applying the update
			inTwoMinutes := inNMinutes(2)
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.UpdatePolicy.UpdateSchedule.At = inTwoMinutes
				device.Spec.UpdatePolicy.DownloadSchedule.At = inTwoMinutes
			})
			Expect(err).ToNot(HaveOccurred())
			expectedVersion++
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, expectedVersion)
			Expect(err).NotTo(HaveOccurred())

			currentVersion, err = harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())
			Expect(currentVersion).To(Equal(expectedVersion))
			expectedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())

			By("applying another spec, the update should be applied quickly")
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				// change the spec to update every minute so that we don't have to wait as long
				device.Spec.UpdatePolicy.UpdateSchedule.At = everyMinuteExpression
				device.Spec.UpdatePolicy.DownloadSchedule.At = everyMinuteExpression
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{newInlineConfigVersion(expectedVersion)}
			})
			Expect(err).ToNot(HaveOccurred())
			// eventually the next update should be applied
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, expectedVersion)
			Expect(err).NotTo(HaveOccurred())

			expectedVersion++
			inTwoMinutes = inNMinutes(2)
			By("applying an immediate download policy and an eventual update policy, the process should stall at updating")
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.UpdatePolicy.UpdateSchedule.At = inTwoMinutes
				device.Spec.UpdatePolicy.DownloadSchedule.At = everyMinuteExpression
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{newInlineConfigVersion(expectedVersion)}
			})
			Expect(err).ToNot(HaveOccurred())
			harness.WaitForDeviceContents(deviceId, "status should indicate that we are blocked by updating", func(device *v1beta1.Device) bool {
				cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
				if cond == nil || cond.Status != v1beta1.ConditionStatusTrue {
					return false
				}
				return cond.Reason == string(v1beta1.UpdateStatePreparing) ||
					cond.Reason == string(v1beta1.UpdateStateApplyingUpdate)
			}, TIMEOUT)
		})
		It("Should not crash in case of unexpected services configs", Label("78711", "sanity", "agent"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			stdout, err := harness.VM.RunSSH([]string{"sudo", "cat", "/var/lib/flightctl/current.json"}, nil)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("before updateCurrent.json: %s\n", stdout.String())

			stdout, err = harness.VM.RunSSH([]string{"sudo", "cat", "/var/lib/flightctl/desired.json"}, nil)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("before update desired.json: %s\n", stdout.String())

			const (
				rapidFilesCount  = 10
				firewallZonesDir = "/etc/firewalld/zones"
				badZoneFile      = firewallZonesDir + "/bad-zone.xml"
				conflictFile     = firewallZonesDir + "/conflict.json"
			)

			// ------------------------------------------------------------------
			// Malformed XML — should cause firewall reload hook to fail
			// ------------------------------------------------------------------
			By(fmt.Sprintf("Applying malformed XML to %s", badZoneFile))
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{newInlineConfigForPath("bad-zone", badZoneFile, "<invalid")}
			})
			Expect(err).NotTo(HaveOccurred())
			stdout, err = harness.VM.RunSSH([]string{"sudo", "cat", "/var/lib/flightctl/current.json"}, nil)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("after update current.json: %s\n", stdout.String())

			stdout, err = harness.VM.RunSSH([]string{"sudo", "cat", "/var/lib/flightctl/desired.json"}, nil)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("after update desired.json: %s\n", stdout.String())

			harness.WaitForDeviceContents(deviceId, "device status should indicate updating failure", func(device *v1beta1.Device) bool {
				return e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError))
			}, "10m")

			// ------------------------------------------------------------------
			// Rapidly add, remove, or update multiple files
			// ------------------------------------------------------------------
			By("Rapidly applying multiple config changes in quick succession")
			for i := 1; i <= rapidFilesCount; i++ {
				GinkgoWriter.Printf("Applying update %d\n", i)
				path := fmt.Sprintf("%s/rapid-%d.json", firewallZonesDir, i)
				name := fmt.Sprintf("rapid-%d", i)
				err := harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
					device.Spec.Config = &[]v1beta1.ConfigProviderSpec{newInlineConfigForPath(name, path, name)}
				})
				Expect(err).NotTo(HaveOccurred())
			}
			By("Verifying the last file remains after rapid updates")
			listFiles := func() ([]string, error) {
				stdout, err := harness.VM.RunSSH([]string{"sudo", "ls", firewallZonesDir}, nil)
				if err != nil {
					return nil, err
				}
				return strings.Fields(stdout.String()), nil
			}
			lastFile := fmt.Sprintf("rapid-%d.json", rapidFilesCount)
			// Can take a few seconds to process all updates
			Eventually(listFiles, TIMEOUT, POLLING).
				Should(ConsistOf(lastFile))

			// ------------------------------------------------------------------
			// Conflicting inline configs targeting the same path
			// ------------------------------------------------------------------
			By("Applying two inline configs that write to the same file path - through UpdateDevice")
			cfg1 := newInlineConfigForPath("c1", conflictFile, "cfg1")
			cfg2 := newInlineConfigForPath("c2", conflictFile, "cfg2")

			Expect(harness.UpdateDevice(deviceId, func(device *v1beta1.Device) {
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{cfg1, cfg2}
			})).To(MatchError(ContainSubstring("must be unique")))

		})

	})
})

var flightDemosHttpRepoConfig = v1beta1.HttpConfigProviderSpec{
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

func newInlineConfigVersion(version int) v1beta1.ConfigProviderSpec {
	configCopy := inlineConfig
	configCopy.Content = fmt.Sprintf("%s %d", configCopy.Content, version)
	cfg := v1beta1.InlineConfigProviderSpec{
		Inline: []v1beta1.FileSpec{configCopy},
		Name:   validInlineConfig.Name,
	}
	var provider v1beta1.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(cfg)
	Expect(err).NotTo(HaveOccurred())
	return provider
}

// newInlineConfigForPath creates a ConfigProviderSpec with inline configuration for the specified path
func newInlineConfigForPath(name string, path string, content string) v1beta1.ConfigProviderSpec {
	var inlineConfig = v1beta1.FileSpec{
		Content: content,
		Mode:    modePointer,
		Path:    path,
	}
	cfg := v1beta1.InlineConfigProviderSpec{
		Inline: []v1beta1.FileSpec{inlineConfig},
		Name:   name,
	}
	var provider v1beta1.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(cfg)
	Expect(err).NotTo(HaveOccurred())
	return provider
}
