package agent_test

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VM Agent behavior during updates", Label("agent-update"), func() {
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
		It("should update to the requested image", Label("75523", "agent"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Verifying update to agent with requested image")
			device, newImageReference, err := harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V2)
			Expect(err).ToNot(HaveOccurred())

			currentImage := device.Status.Os.Image
			GinkgoWriter.Printf("Current image is: %s\n", currentImage)
			GinkgoWriter.Printf("New image is: %s\n", newImageReference)

			harness.WaitForDeviceContents(deviceId, "The device is preparing an update to renderedVersion: 2",
				deviceInOSTUpdateProgress, LONGTIMEOUT)

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
				deviceInOSTUpdateProgress, LONGTIMEOUT)

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
				deviceInOSTUpdateProgress, TIMEOUT)

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

		It("Should resolve to the latest version when multiple updates are applied", Label("77672", "agent"), func() {
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

			repositoryAccessible := false
			By("Checking whether the shared HTTP repository is accessible")
			Eventually(func(g Gomega) {
				repo, err := harness.GetRepository(validRepoName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(repo.Status).NotTo(BeNil())

				cond := v1beta1.FindStatusCondition(repo.Status.Conditions, v1beta1.ConditionTypeRepositoryAccessible)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Or(Equal(v1beta1.ConditionStatusTrue), Equal(v1beta1.ConditionStatusFalse)))
				repositoryAccessible = cond.Status == v1beta1.ConditionStatusTrue
			}, "1m", POLLING).Should(Succeed())

			// Add more factories here if desired. In disconnected environments the public
			// repository-backed config is not reachable, so use a local inline config instead.
			type providerFactory = func(providerSpec *v1beta1.ConfigProviderSpec) error
			configFactories := []providerFactory{}
			if repositoryAccessible {
				configFactories = append(configFactories, func(providerSpec *v1beta1.ConfigProviderSpec) error {
					return providerSpec.FromHttpConfigProviderSpec(flightDemosHttpRepoConfig)
				})
			} else {
				GinkgoWriter.Printf("Repository %s is not accessible; using inline-only config updates for disconnected execution\n", validRepoName)
				disconnectedInlineConfig := v1beta1.InlineConfigProviderSpec{
					Name: "disconnected-inline-config",
					Inline: []v1beta1.FileSpec{{
						Path:    "/etc/motd.disconnected",
						Content: "disconnected fallback config",
					}},
				}
				configFactories = append(configFactories, func(providerSpec *v1beta1.ConfigProviderSpec) error {
					return providerSpec.FromInlineConfigProviderSpec(disconnectedInlineConfig)
				})
			}
			configFactories = append(configFactories, func(providerSpec *v1beta1.ConfigProviderSpec) error {
				return providerSpec.FromInlineConfigProviderSpec(validInlineConfig)
			})

			// Build the device spec progressively and update through harness device-spec helpers,
			// matching automation behavior.
			deviceSpecConfig := make([]v1beta1.ConfigProviderSpec, 0, len(configFactories))
			for i, factory := range configFactories {
				specVersion := i + 1
				By(fmt.Sprintf("Applying spec update: %d", specVersion))
				var configProviderSpec v1beta1.ConfigProviderSpec
				err := factory(&configProviderSpec)
				Expect(err).ToNot(HaveOccurred())
				deviceSpecConfig = append(deviceSpecConfig, configProviderSpec)

				nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
				Expect(err).NotTo(HaveOccurred())
				err = harness.UpdateDeviceConfigWithRetries(deviceId, deviceSpecConfig, nextRenderedVersion)
				Expect(err).ToNot(HaveOccurred())
			}

			By(fmt.Sprintf("applying all defined specs, the end version should indicate %d updates were applied", len(configFactories)))
			expectedVersion := initialVersion + len(configFactories)
			finalVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())
			Expect(finalVersion).To(Equal(expectedVersion))
		})
		It("Should rollback when updating to a broken image", Label("82481", "sanity", "agent"), func() {
			harness := e2e.GetWorkerHarness()

			expectedVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())
			dev, err := harness.GetDevice(deviceId)
			Expect(err).NotTo(HaveOccurred())
			initialImage := dev.Status.Os.Image
			GinkgoWriter.Printf("Initial OS image: %s\n", initialImage)

			By("Updating device to v8 image (contains bad compose file)")
			_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V8)
			Expect(err).ToNot(HaveOccurred())

			harness.WaitForDeviceContents(deviceId, "device spec should be updated to the new image", func(device *v1beta1.Device) bool {
				return device.Spec.Os != nil && device.Spec.Os.Image != initialImage
			}, TIMEOUT)

			By("Waiting for device to start rebooting (rollback triggered after prefetch failure)")
			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusRebooting))

			By("Waiting for device to come back online after rollback reboot")
			harness.WaitForDeviceContents(deviceId, "device should be online after rollback reboot", func(device *v1beta1.Device) bool {
				return device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline
			}, LONGTIMEOUT)

			By("Verifying agent logs show rollback attempt")
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

			By("Verifying device booted OS image reverted to initial image")
			harness.WaitForDeviceContents(deviceId, "device status OS image should be reverted to initial", func(device *v1beta1.Device) bool {
				return device.Status.Os.Image == initialImage
			}, TIMEOUT)

			By("Verifying device is OutOfDate (spec still wants v8, but running initial)")
			harness.WaitForDeviceContents(deviceId, "device should be out of date", func(device *v1beta1.Device) bool {
				return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate
			}, TIMEOUT)
		})
		It("Should trigger greenboot rollback when agent fails to start", Label("greenboot-rollback", "87279", "greenboot-rollback-recovery", "sanity", "agent"), func() {
			harness := e2e.GetWorkerHarness()
			initialStatusImage, postRollbackBootID := waitForGreenbootOSRollbackFromV11BrokenAgent(harness, deviceId, false)

			By("Verifying device does NOT retry the failed v11 image")
			// Wait for the device to be Online on the original image. With
			// greenboot-rs (0.16.x) the device may do extra boot cycles after
			// rollback, so we don't pin a boot ID — just wait for Online +
			// correct image, then capture the stable boot ID.
			harness.WaitForDeviceContents(deviceId, "device should be online on original image after rollback", func(device *v1beta1.Device) bool {
				return device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline &&
					device.Status.Os.Image == initialStatusImage
			}, TIMEOUT)

			stableDev, err := harness.GetDevice(deviceId)
			Expect(err).NotTo(HaveOccurred())
			stableBootID := stableDev.Status.SystemInfo.BootID
			GinkgoWriter.Printf("Stable boot ID after rollback: %s (initially captured: %s)\n", stableBootID, postRollbackBootID)

			// Don't check Summary.Status here: after a long rollback cycle (~6 min
			// offline), the periodic healthcheck may briefly flip the device to
			// Unknown before the first heartbeat lands. The key invariant is that
			// the device stays on the original image and doesn't reboot (retry v11).
			harness.EnsureDeviceContents(deviceId, "device should remain stable and not retry failed image", func(device *v1beta1.Device) bool {
				return device.Status.Os.Image == initialStatusImage &&
					device.Status.SystemInfo.BootID == stableBootID
			}, "2m")
			GinkgoWriter.Println("Confirmed: device did not retry the failed v11 image after rollback")

			By("Recovering with a new good OS image after rollback (operator pushes a fix)")
			nextRendered, err := harness.PrepareNextDeviceVersionFromCurrentStatus(deviceId)
			Expect(err).NotTo(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				cur, perr := util.NewImageReferenceFromString(device.Status.Os.Image)
				Expect(perr).NotTo(HaveOccurred())
				device.Spec.Os = &v1beta1.DeviceOsSpec{Image: cur.WithTag(util.DeviceTags.V2).String()}
			})
			Expect(err).NotTo(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersionWithReboot(deviceId, nextRendered)
			Expect(err).NotTo(HaveOccurred())

			harness.WaitForDeviceContents(deviceId, "device should be up to date on good image after recovery update", func(device *v1beta1.Device) bool {
				return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate &&
					device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline &&
					strings.Contains(device.Status.Os.Image, util.DeviceTags.V2) &&
					device.Status.Os.Image != initialStatusImage
			}, LONGTIMEOUT)
			GinkgoWriter.Println("Confirmed: device accepted new image and recovered after greenboot rollback")
		})

		It("Should retain pre-rollback script output in journal when Storage=persistent", Label("88425", "sanity", "agent"), func() {
			harness := e2e.GetWorkerHarness()
			configureJournaldForGreenbootE2E(harness, true)
			waitForGreenbootOSRollbackFromV11BrokenAgent(harness, deviceId, false)

			By("Verifying pre-rollback diagnostics are still visible after rollback reboot (persistent journal)")
			Eventually(func() bool {
				diagOut, err := journalGrepPreRollbackMarkerFromVM(harness)
				Expect(err).NotTo(HaveOccurred())
				return hasPreRollbackJournalEvidence(diagOut)
			}, "45s", "3s").Should(BeTrue(),
				"Expected 40_flightctl_agent_pre_rollback.sh output in persistent journal after rollback reboot")
			GinkgoWriter.Println("Confirmed: pre-rollback script output persisted across rollback reboot")
		})

		It("Should not retain pre-rollback script output across rollback when journal is volatile", Label("88426", "agent"), func() {
			harness := e2e.GetWorkerHarness()
			configureJournaldForGreenbootE2E(harness, false)
			waitForGreenbootOSRollbackFromV11BrokenAgent(harness, deviceId, true)

			By("Verifying pre-rollback diagnostics from the failed boot are not in retained journal")
			diagOut, err := journalGrepPreRollbackMarkerFromVMCurrentBootOnly(harness)
			Expect(err).NotTo(HaveOccurred())
			Expect(hasPreRollbackJournalEvidence(diagOut)).To(BeFalse(),
				"Pre-rollback collection runs before rollback reboot; with volatile journal those logs should not survive (current boot only)")
			GinkgoWriter.Println("Confirmed: pre-rollback script output not present after reboot with volatile journal")
		})
		It("Should NOT rollback when third-party health check (MicroShift) fails", Label("greenboot-third-party", "88229", "agent"), func() {
			harness := e2e.GetWorkerHarness()

			By("Getting initial device state")
			dev, err := harness.GetDevice(deviceId)
			Expect(err).NotTo(HaveOccurred())
			initialBootID := dev.Status.SystemInfo.BootID

			By("Updating device to v7 (MicroShift image)")
			// v7 includes MicroShift which installs 40_microshift_running_check.sh
			// in /usr/lib/greenboot/check/required.d/. MicroShift will fail its health
			// check (insufficient VM resources), but flightctl-configure-greenboot.service
			// should disable it via DISABLED_HEALTHCHECKS before greenboot runs.
			_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V7)
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for device to reboot into v7 and come online")
			harness.WaitForDeviceContents(deviceId, "device should come online on v7", func(device *v1beta1.Device) bool {
				return strings.Contains(device.Status.Os.Image, "v7") &&
					device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline &&
					device.Status.SystemInfo.BootID != initialBootID
			}, LONGTIMEOUT)

			By("Verifying NO greenboot rollback was triggered")
			fallbackOutput, err := harness.VM.RunSSH([]string{
				"sudo", "journalctl", "-b", "-u", "greenboot-healthcheck.service", "--no-pager",
			}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(fallbackOutput.String()).NotTo(ContainSubstring("FALLBACK BOOT DETECTED"),
				"Third-party health check failure must NOT trigger OS rollback")

			By("Verifying configure-greenboot disabled the MicroShift health check")
			configureOutput, err := harness.VM.RunSSH([]string{
				"sudo", "journalctl", "-b", "-u", "flightctl-configure-greenboot.service", "--no-pager",
			}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(configureOutput.String()).To(ContainSubstring("Disabling third-party greenboot health checks"),
				"Expected configure-greenboot to disable third-party health checks")

			GinkgoWriter.Println("Confirmed: third-party MicroShift health check did not trigger rollback")
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
			inlineCfg, cfgErr := newInlineConfigVersion(expectedVersion)
			Expect(cfgErr).NotTo(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{inlineCfg}
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
			inlineCfg, cfgErr = newInlineConfigVersion(expectedVersion)
			Expect(cfgErr).NotTo(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				// change the spec to update every minute so that we don't have to wait as long
				device.Spec.UpdatePolicy.UpdateSchedule.At = everyMinuteExpression
				device.Spec.UpdatePolicy.DownloadSchedule.At = everyMinuteExpression
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{inlineCfg}
			})
			Expect(err).ToNot(HaveOccurred())
			// eventually the next update should be applied
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, expectedVersion)
			Expect(err).NotTo(HaveOccurred())

			expectedVersion++
			inTwoMinutes = inNMinutes(2)
			By("applying an immediate download policy and an eventual update policy, the process should stall at updating")
			inlineCfg, cfgErr = newInlineConfigVersion(expectedVersion)
			Expect(cfgErr).NotTo(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.UpdatePolicy.UpdateSchedule.At = inTwoMinutes
				device.Spec.UpdatePolicy.DownloadSchedule.At = everyMinuteExpression
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{inlineCfg}
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
			badZoneCfg, cfgErr := util.BuildInlineConfigSpec("bad-zone", badZoneFile, "<invalid", "")
			Expect(cfgErr).NotTo(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, e2e.SetDeviceConfig(badZoneCfg))
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
				rapidCfg, cfgErr := util.BuildInlineConfigSpec(name, path, name, "")
				Expect(cfgErr).NotTo(HaveOccurred())
				err := harness.UpdateDeviceWithRetries(deviceId, e2e.SetDeviceConfig(rapidCfg))
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
			cfg1, cfgErr := util.BuildInlineConfigSpec("c1", conflictFile, "cfg1", "")
			Expect(cfgErr).NotTo(HaveOccurred())
			cfg2, cfgErr := util.BuildInlineConfigSpec("c2", conflictFile, "cfg2", "")
			Expect(cfgErr).NotTo(HaveOccurred())

			err = harness.UpdateDevice(deviceId, e2e.SetDeviceConfig(cfg1, cfg2))
			Expect(err).To(MatchError(ContainSubstring("must be unique")))

		})

	})
})

// deviceInOSTUpdateProgress is true while the OS image update is being pulled or applied (Preparing / ReadyToUpdate / ApplyingUpdate).
// Slow image pulls can keep the device in Preparing for a long time; ApplyingUpdate alone is too narrow and flakes in CI.
// Logging is throttled: WaitForDeviceContents polls this frequently.
func deviceInOSTUpdateProgress(device *v1beta1.Device) bool {
	for _, reason := range []string{
		string(v1beta1.UpdateStatePreparing),
		string(v1beta1.UpdateStateReadyToUpdate),
		string(v1beta1.UpdateStateApplyingUpdate),
	} {
		if e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusTrue, reason) {
			deviceInOSProgressLogThrottled(reason)
			return true
		}
	}
	return false
}

var (
	deviceInOSProgressLogMu       sync.Mutex
	deviceInOSProgressLastReason  string
	deviceInOSProgressLastLogTime time.Time
)

func deviceInOSProgressLogThrottled(reason string) {
	const minInterval = 8 * time.Second
	now := time.Now()
	deviceInOSProgressLogMu.Lock()
	defer deviceInOSProgressLogMu.Unlock()
	if reason == deviceInOSProgressLastReason && now.Sub(deviceInOSProgressLastLogTime) < minInterval {
		return
	}
	deviceInOSProgressLastReason = reason
	deviceInOSProgressLastLogTime = now
	GinkgoWriter.Printf("[deviceInOSTUpdateProgress] Updating=True reason=%s\n", reason)
}

// preRollbackJournalMarker is emitted by packaging/greenboot/flightctl-agent-pre-rollback.sh (log_info).
const preRollbackJournalMarker = "flightctl-agent pre-rollback script started"

// hasPreRollbackJournalEvidence returns true if s looks like output from 40_flightctl_agent_pre_rollback.sh
// (journal MESSAGE formatting varies by systemd/journald version).
func hasPreRollbackJournalEvidence(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, sub := range []string{
		preRollbackJournalMarker,
		"40_flightctl_agent_pre_rollback",
		"rollback imminent - collecting debug info",
		"[pre-rollback]",
	} {
		if strings.Contains(s, sub) {
			GinkgoWriter.Printf("[hasPreRollbackJournalEvidence] matched pattern %q (snippet len=%d)\n", sub, len(s))
			return true
		}
	}
	return false
}

// journalPreRollbackGrepScript searches persistent journal for pre-rollback hook output. Tries several
// boots (-1..-3), full merge, and redboot-task-runner; uses grep -E with alternates because exact MESSAGE
// text can differ between OS versions.
const journalPreRollbackGrepScript = `
pick_pat='flightctl-agent pre-rollback script started|40_flightctl_agent_pre_rollback|rollback imminent - collecting debug info|\[pre-rollback\]'
pick() { grep -E "$pick_pat" 2>/dev/null | head -1 || true; }
out=""
for b in -1 -2 -3; do
  out=$(sudo journalctl -b "$b" --no-pager 2>/dev/null | pick)
  [ -n "$out" ] && break
done
if [ -z "$out" ]; then
  out=$(sudo journalctl --no-pager 2>/dev/null | pick)
fi
if [ -z "$out" ]; then
  for b in -1 -2; do
    out=$(sudo journalctl -b "$b" -o cat -u redboot-task-runner.service --no-pager 2>/dev/null | pick)
    [ -n "$out" ] && break
  done
fi
if [ -z "$out" ]; then
  out=$(sudo journalctl -o cat -u redboot-task-runner.service --no-pager 2>/dev/null | pick)
fi
printf '%s' "$out"
`

// journalPreRollbackGrepCurrentBootScript only reads the current boot (-b). Used when journald is volatile:
// a full `journalctl` merge can still pick up stale lines from disk if /var/log/journal was not fully empty,
// which would flake 88426 (expect no pre-rollback output retained).
const journalPreRollbackGrepCurrentBootScript = `
pick_pat='flightctl-agent pre-rollback script started|40_flightctl_agent_pre_rollback|rollback imminent - collecting debug info|\[pre-rollback\]'
pick() { grep -E "$pick_pat" 2>/dev/null | head -1 || true; }
out=$(sudo journalctl -b --no-pager 2>/dev/null | pick)
if [ -z "$out" ]; then
  out=$(sudo journalctl -b -o cat -u redboot-task-runner.service --no-pager 2>/dev/null | pick)
fi
printf '%s' "$out"
`

// journalGrepPreRollbackMarkerFromVM returns the first journal line matching pre-rollback diagnostics, or "".
func journalGrepPreRollbackMarkerFromVM(harness *e2e.Harness) (string, error) {
	GinkgoWriter.Println("[journalGrepPreRollbackMarkerFromVM] running full journal grep (multi-boot + merge + redboot-task-runner)")
	stdout, err := harness.VM.RunSSH([]string{"bash", "-lc", journalPreRollbackGrepScript}, nil)
	if err != nil {
		return "", err
	}
	out := stdout.String()
	logJournalGrepResult("journalGrepPreRollbackMarkerFromVM", out)
	return out, nil
}

func journalGrepPreRollbackMarkerFromVMCurrentBootOnly(harness *e2e.Harness) (string, error) {
	GinkgoWriter.Println("[journalGrepPreRollbackMarkerFromVMCurrentBootOnly] running current-boot journal grep only")
	stdout, err := harness.VM.RunSSH([]string{"bash", "-lc", journalPreRollbackGrepCurrentBootScript}, nil)
	if err != nil {
		return "", err
	}
	out := stdout.String()
	logJournalGrepResult("journalGrepPreRollbackMarkerFromVMCurrentBootOnly", out)
	return out, nil
}

func logJournalGrepResult(label, out string) {
	out = strings.TrimSpace(out)
	if out == "" {
		GinkgoWriter.Printf("[%s] no pre-rollback marker line matched\n", label)
		return
	}
	snip := out
	if len(snip) > 240 {
		snip = snip[:240] + "...(truncated)"
	}
	GinkgoWriter.Printf("[%s] matched (len=%d): %s\n", label, len(out), snip)
}

// waitForGreenbootOSRollbackFromV11BrokenAgent updates the device to the v11 image (broken flightctl-agent),
// waits for greenboot to roll the OS back to the initial image, and verifies OutOfDate.
// When skipFallbackJournalAssert is false, also asserts FALLBACK in greenboot-healthcheck logs (greenboot-rs).
// With journald Storage=volatile, prior-boot logs are lost and greenboot-rs will not emit FALLBACK — pass true.
// It returns the initial status OS image reference and the boot ID after rollback completes.
func waitForGreenbootOSRollbackFromV11BrokenAgent(harness *e2e.Harness, deviceId string, skipFallbackJournalAssert bool) (initialStatusImage, postRollbackBootID string) {
	GinkgoWriter.Printf("[waitForGreenbootOSRollbackFromV11BrokenAgent] deviceId=%s skipFallbackJournalAssert=%v\n", deviceId, skipFallbackJournalAssert)
	By("Getting initial device state")
	dev, err := harness.GetDevice(deviceId)
	Expect(err).NotTo(HaveOccurred())
	initialStatusImage = dev.Status.Os.Image
	initialBootID := dev.Status.SystemInfo.BootID
	GinkgoWriter.Printf("Initial image: %s initialBootID=%s\n", initialStatusImage, initialBootID)

	By("Updating device to v11 image (contains systemd drop-in that breaks flightctl-agent)")
	// The v11 image contains a systemd drop-in that causes flightctl-agent to fail.
	// Greenboot health checks fail and the OS rolls back to the previous deployment.
	_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V11)
	Expect(err).ToNot(HaveOccurred())

	harness.WaitForDeviceContents(deviceId, "device spec should be updated to v11", func(device *v1beta1.Device) bool {
		return device.Spec.Os != nil && strings.Contains(device.Spec.Os.Image, "v11")
	}, TIMEOUT)

	By("Waiting for device to start rebooting into v11")
	Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).WithArguments(
		deviceId).Should(Equal(v1beta1.DeviceSummaryStatusRebooting))

	By("Waiting for greenboot to detect agent failure and trigger OS rollback")
	postRollbackBootID = ""
	harness.WaitForDeviceContents(deviceId, "device should rollback to initial OS image and come online", func(device *v1beta1.Device) bool {
		if device.Status.Os.Image != initialStatusImage ||
			device.Status.Summary.Status != v1beta1.DeviceSummaryStatusOnline ||
			device.Status.SystemInfo.BootID == initialBootID {
			return false
		}
		postRollbackBootID = device.Status.SystemInfo.BootID
		return true
	}, LONGTIMEOUT)
	GinkgoWriter.Printf("[waitForGreenbootOSRollbackFromV11BrokenAgent] rollback observed postRollbackBootID=%s\n", postRollbackBootID)

	By("Verifying greenboot triggered an OS rollback (not just a reboot)")
	if skipFallbackJournalAssert {
		GinkgoWriter.Println("Skipping FALLBACK journal assertion: volatile journal drops prior-boot healthcheck logs required for greenboot-rs to log FALLBACK")
	} else {
		fallbackOutput, err := harness.VM.RunSSH([]string{
			"sudo", "journalctl", "-b", "-u", "greenboot-healthcheck.service", "--no-pager", "-n", "300",
		}, nil)
		Expect(err).NotTo(HaveOccurred(), "Failed to read greenboot-healthcheck logs")
		GinkgoWriter.Printf("[waitForGreenbootOSRollbackFromV11BrokenAgent] greenboot-healthcheck journal bytes=%d\n", len(fallbackOutput.String()))
		Expect(fallbackOutput.String()).To(ContainSubstring("FALLBACK BOOT DETECTED"),
			"Expected greenboot-rs healthcheck to log FALLBACK after OS rollback (rollback already confirmed via device status)")
		GinkgoWriter.Println("Confirmed: greenboot-rs logged 'FALLBACK BOOT DETECTED' - OS rollback verified")
	}

	By("Verifying device reports as OutOfDate after rollback")
	harness.WaitForDeviceContents(deviceId, "device should be out of date after rollback", func(device *v1beta1.Device) bool {
		return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate
	}, TIMEOUT)

	GinkgoWriter.Printf("Device successfully rolled back from v11 to %s via greenboot\n", initialStatusImage)
	return initialStatusImage, postRollbackBootID
}

func configureJournaldForGreenbootE2E(harness *e2e.Harness, persistent bool) {
	storage := "volatile"
	if persistent {
		storage = "persistent"
	}
	By(fmt.Sprintf("Configuring journald Storage=%s for pre-rollback visibility test", storage))
	script := fmt.Sprintf(`set -euo pipefail
sudo mkdir -p /etc/systemd/journald.conf.d
echo '[Journal]
Storage=%s' | sudo tee /etc/systemd/journald.conf.d/99-e2e-greenboot-journal.conf >/dev/null
`, storage)
	if persistent {
		script += `sudo mkdir -p /var/log/journal
sudo systemd-tmpfiles --create --prefix /var/log/journal 2>/dev/null || true
`
	} else {
		script += `sudo rm -rf /var/log/journal
`
	}
	script += `sudo systemctl restart systemd-journald
`
	_, err := harness.VM.RunSSH([]string{"bash", "-lc", script}, nil)
	Expect(err).NotTo(HaveOccurred())
	GinkgoWriter.Printf("[configureJournaldForGreenbootE2E] applied Storage=%s and restarted systemd-journald\n", storage)
}

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

func newInlineConfigVersion(version int) (v1beta1.ConfigProviderSpec, error) {
	content := fmt.Sprintf("%s %d", inlineConfig.Content, version)
	return util.BuildInlineConfigSpec(validInlineConfig.Name, inlineConfig.Path, content, "")
}
