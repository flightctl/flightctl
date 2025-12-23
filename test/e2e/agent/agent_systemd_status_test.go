package agent_test

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VM Agent behavior during updates", func() {
	var (
		deviceId       string
		chronydService = "chronyd.service"
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

			By("Verifying update to agent  with requested image")
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

		It("Should update to v4 with embedded application", Label("77671", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("Verifying update to agent  with embedded application")

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
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusType("Rebooting")))

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
				deviceId).Should(Equal(v1beta1.DeviceSummaryStatusType("Online")))

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
		It("Should rollback when updating to a broken image", Label("82481", "sanity"), func() {
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
			Expect(cond.Message).To(And(ContainSubstring("Failed to update to renderedVersion"), ContainSubstring("validating compose spec")))

			/*
				** Add this assertion back when the bug referenced above is fixed **
				harness.WaitForDeviceContents(deviceId, "device image should be reverted to the old image", func(device *v1beta1.Device) bool {
					return device.Spec.Os.Image == initialImage
				}, TIMEOUT)
			*/
		})
		It("Should respect the spec's update schedule", Label("79220", "sanity", "slow"), func() {
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
			harness.WaitForDeviceContents(deviceId, "the update should be registered but not applied", func(device *v1beta1.Device) bool {
				return device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusOutOfDate
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
				if cond == nil {
					return false
				}
				return cond.Reason == string(v1beta1.UpdateStateApplyingUpdate) &&
					strings.Contains(cond.Message, "update policy not ready")
			}, TIMEOUT)
		})
		It("Should not crash in case of unexpected services configs", Label("78711", "sanity"), func() {
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
		It("reports systemd services and applications based on device spec", Label("sanity", "86238"), func() {
			harness := e2e.GetWorkerHarness()

			By("enrolling a fresh device")
			GinkgoWriter.Printf("Enrolled device %s\n", deviceId)

			startVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("configuring device.spec.systemd.matchPatterns to track chronyd.service")
			err = harness.UpdateSystemdMatchPatterns(deviceId, []string{chronydService})
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.WaitForDeviceNewRenderedVersion(deviceId, startVersion+1)).To(Succeed())

			By("configuring device.spec.applications with a simple inline compose application")
			composeContent := `
version: "3.8"
services:
  sleep:
    image: quay.io/flightctl-tests/alpine:v1
    command: ["sleep", "infinity"]
`
			inlineApp := v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{
					{
						Content: &composeContent,
						Path:    "podman-compose.yaml",
					},
				},
			}
			err = harness.UpdateApplication(true, deviceId, "my-compose-app", inlineApp, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.WaitForDeviceNewRenderedVersion(deviceId, startVersion+2)).To(Succeed())

			By("waiting for device status to report applications and systemd sections")
			Eventually(func() bool {
				resp, err := harness.GetDeviceWithStatusSystem(deviceId)
				if err != nil || resp == nil || resp.JSON200 == nil {
					return false
				}

				dev := resp.JSON200
				if dev.Status == nil {
					return false
				}

				// Applications must be present and non-empty
				if len(dev.Status.Applications) == 0 {
					return false
				}

				// Systemd must be present and non-empty
				if dev.Status.Systemd == nil || len(*dev.Status.Systemd) == 0 {
					return false
				}

				return true
			}, TENMINTIMEOUT, TENSECTIMEOUT).Should(BeTrue())

			// Fetch a fresh copy after the Eventually, now that we know it's populated
			resp, err := harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).ToNot(BeNil())
			Expect(resp.JSON200).ToNot(BeNil())
			dev := resp.JSON200

			Expect(dev.Status).ToNot(BeNil())
			Expect(dev.Status.Applications).ToNot(BeNil())
			Expect(len(dev.Status.Applications)).To(BeNumerically(">=", 1))
			Expect(dev.Status.Systemd).ToNot(BeNil())
			Expect(len(*dev.Status.Systemd)).To(BeNumerically(">=", 1))

			var chronydSeen bool
			for _, unit := range *dev.Status.Systemd {
				if unit.Unit == chronydService {
					chronydSeen = true
					By("verifying chronyd.service is reported as active/loaded")
					Expect(unit.ActiveState).To(Equal(v1beta1.SystemdActiveStateActive))
					Expect(unit.LoadState).To(Equal(v1beta1.SystemdLoadStateLoaded))
				}
			}
			Expect(chronydSeen).To(BeTrue(), "chronyd.service should be reported in status.systemd")

			By("Scenario 2: failing tracked service degrades summary and exposes failed systemd unit")

			// 1. Make sure we start from Online summary
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).
				WithArguments(deviceId).
				Should(Equal(v1beta1.DeviceSummaryStatusOnline))

			// 2. Stop and mask the tracked service on the worker VM to prevent auto-restarts
			_, err = harness.VM.RunSSH(
				[]string{"sudo", "systemctl", "mask", "--now", "chronyd.service"},
				nil,
			)
			Expect(err).NotTo(HaveOccurred(), "failed to mask/stop chronyd.service on device VM")

			// 3. Confirm chronyd eventually reports a non-active state
			waitForChronydState := func() v1beta1.SystemdActiveStateType {
				resp, err := harness.GetDeviceWithStatusSystem(deviceId)
				if err != nil || resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Systemd == nil {
					return v1beta1.SystemdActiveStateUnknown
				}
				for _, u := range *resp.JSON200.Status.Systemd {
					if u.Unit == chronydService {
						return u.ActiveState
					}
				}
				return v1beta1.SystemdActiveStateUnknown
			}

			Eventually(waitForChronydState, LONGTIMEOUT, POLLING).
				Should(BeElementOf(
					[]v1beta1.SystemdActiveStateType{
						v1beta1.SystemdActiveStateFailed,
						v1beta1.SystemdActiveStateInactive,
					}),
					"chronyd.service should be reported as failed or inactive after stop",
				)

			// 4. Verify systemd status entry for chronyd shows failure or inactive state
			unitStatus := harness.WaitForSystemdUnitStatus(deviceId, chronydService, LONGTIMEOUT, POLLING)
			Expect(unitStatus.Unit).To(Equal(chronydService))
			Expect(unitStatus.ActiveState).To(BeElementOf(
				[]v1beta1.SystemdActiveStateType{
					v1beta1.SystemdActiveStateFailed,
					v1beta1.SystemdActiveStateInactive,
				}),
				"chronyd.service should appear in status.systemd as failed or inactive when the service is stopped")

			// 5. Cleanup: restore chronyd so later scenarios start from healthy state
			_, _ = harness.VM.RunSSH(
				[]string{"sudo", "systemctl", "unmask", "chronyd.service"},
				nil,
			)
			_, err = harness.VM.RunSSH(
				[]string{"sudo", "systemctl", "start", "chronyd.service"},
				nil,
			)
			Expect(err).NotTo(HaveOccurred(), "failed to restart chronyd.service on device VM")

			// Track a deliberately failing systemd service
			const failingServiceName = "edge-test-fail.service"

			By("creating a dummy failing systemd unit on the device")
			err = e2e.CreateFailingServiceOnDevice(harness, failingServiceName)
			Expect(err).NotTo(HaveOccurred())

			By("updating matchPatterns to include chronyd.service and the failing service")
			err = harness.UpdateSystemdMatchPatterns(deviceId, []string{chronydService, failingServiceName})
			Expect(err).ToNot(HaveOccurred())

			By("waiting for device status.systemd to include the failing unit (with loaded or not-found state)")
			failUnit := harness.WaitForSystemdUnitStatus(deviceId, failingServiceName, TENMINTIMEOUT, TENSECTIMEOUT)
			Expect(failUnit.LoadState).To(Or(Equal(v1beta1.SystemdLoadStateLoaded), Equal(v1beta1.SystemdLoadStateNotFound)))
			GinkgoWriter.Printf("Failing unit status: active=%s, sub=%s, load=%s\n",
				string(failUnit.ActiveState), failUnit.SubState, string(failUnit.LoadState))

			// Unmonitored failing service does not appear in systemd list
			const untrackedService = "rsyslog.service"

			By("stopping an untracked service on the device (rsyslog.service)")
			err = e2e.StopServiceOnDevice(harness, untrackedService)
			Expect(err).NotTo(HaveOccurred())

			By("verifying rsyslog.service does not appear in status.systemd when it is not in matchPatterns")
			Consistently(func() bool {
				resp, err := harness.GetDeviceWithStatusSystem(deviceId)
				if err != nil || resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Systemd == nil {
					return true // No systemd status means service is not tracked
				}
				for _, unit := range *resp.JSON200.Status.Systemd {
					if unit.Unit == untrackedService {
						return false
					}
				}
				return true
			}, 3*TENSECTIMEOUT, TENSECTIMEOUT).Should(BeTrue(), "untracked rsyslog.service should not appear in status.systemd")

			By("cleaning up: removing applications and systemd from spec and restoring services")
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = nil
				device.Spec.Systemd = nil
			})
			Expect(err).ToNot(HaveOccurred())

			err = e2e.RestoreServiceOnDevice(harness, "chronyd.service")
			Expect(err).NotTo(HaveOccurred())
			// Clean up the failing test service (don't try to restart it)
			err = e2e.RemoveSystemdService(harness, failingServiceName)
			Expect(err).NotTo(HaveOccurred())
			err = e2e.RestoreServiceOnDevice(harness, untrackedService)
			Expect(err).NotTo(HaveOccurred())
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
