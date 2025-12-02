package agent_test

import (
	"fmt"
	"slices"
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

			By("Verifying update to agent  with requested image")
			device, newImageReference, err := harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":v2")
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

			device, newImageReference, err := harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":v4")
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

			GinkgoWriter.Printf("Device updated to new image %s 🎉\n", "flightctl-device:v4")
			GinkgoWriter.Printf("We expect containers with sleep infinity process to be present but not running\n")
			stdout, err := harness.VM.RunSSH([]string{"sudo", "podman", "ps"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("sleep infinity"))

			GinkgoWriter.Printf("We expect podman containers with sleep infinity process to be present but not running 👌\n")

			device, newImageReference, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":base")
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

			GinkgoWriter.Printf("Device updated to new image %s 🎉\n", "flightctl-device:base")
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
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			expectedVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceId)
			Expect(err).NotTo(HaveOccurred())
			dev, err := harness.GetDevice(deviceId)
			Expect(err).NotTo(HaveOccurred())
			initialImage := dev.Status.Os.Image
			// The v8 image should contain a bad compose file
			_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":v8")
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
		It("Should respect the spec's update schedule", Label("79220", "sanity"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			const everyMinuteExpression = "* * * * *"
			startGracePeriod := "1m"

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
						StartGraceDuration: &startGracePeriod,
					},
					DownloadSchedule: &v1beta1.UpdateSchedule{
						At:                 wontUpdatePolicy,
						StartGraceDuration: &startGracePeriod,
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
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			GinkgoWriter.Printf("Enrolled device %s\n", deviceId)

			By("configuring device.spec.systemd.matchPatterns to track chronyd.service")
			patchDeviceJSON(harness, deviceId, `[{
			"op":"add",
			"path":"/spec/systemd",
			"value":{"matchPatterns":["chronyd.service"]}
		}]`)

			By("configuring device.spec.applications with a simple compose application")
			patchDeviceJSON(harness, deviceId, `[{
			"op":"add",
			"path":"/spec/applications",
			"value":[
				{
					"image":"quay.io/rh_ee_camadorg/oci-app",
					"appType":"compose",
					"envVars":{},
					"volumes":[],
					"name":"my-compose-app"
				}
			]
		}]`)

			By("waiting for device status to report applications and systemd sections")
			var dev *v1beta1.Device
			Eventually(func() *v1beta1.Device {
				resp, err := harness.GetDeviceWithStatusSystem(deviceId)
				if err != nil || resp == nil || resp.JSON200 == nil {
					return nil
				}
				return resp.JSON200
			}, 10*time.Minute, 10*time.Second).ShouldNot(BeNil())

			resp, err := harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).ToNot(BeNil())
			Expect(resp.JSON200).ToNot(BeNil())
			dev = resp.JSON200

			Expect(dev.Status).ToNot(BeNil())
			Expect(dev.Status.Applications).ToNot(BeNil())
			Expect(len(dev.Status.Applications)).To(BeNumerically(">=", 1))
			Expect(dev.Status.Systemd).ToNot(BeNil())
			Expect(len(*dev.Status.Systemd)).To(BeNumerically(">=", 1))

			var chronydSeen bool
			for _, unit := range *dev.Status.Systemd {
				if unit.Unit == "chronyd.service" {
					chronydSeen = true
					By("verifying chronyd.service is reported as active/loaded")
					Expect(string(unit.ActiveState)).To(Equal("active"))
					Expect(string(unit.LoadState)).To(Equal("loaded"))
				}
			}
			Expect(chronydSeen).To(BeTrue(), "chronyd.service should be reported in status.systemd")

			By("Scenario 2: failing tracked service degrades summary and exposes failed systemd unit")

			// 1. Make sure we start from Online summary
			Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).
				WithArguments(deviceId).
				Should(Equal(v1beta1.DeviceSummaryStatusOnline))

			// 2. Stop the tracked service on the worker VM
			_, err = harness.VM.RunSSH("sudo systemctl stop chronyd.service")
			Expect(err).NotTo(HaveOccurred(), "failed to stop chronyd.service on device VM")

			// 3. Wait until device summary becomes Degraded
			Eventually(harness.GetDeviceWithStatusSummary, LONGTIMEOUT, POLLING).
				WithArguments(deviceId).
				Should(Equal(v1beta1.DeviceSummaryStatusDegraded),
					"summary.status should be Degraded when tracked systemd service fails")

			// 4. Verify systemd status entry for chronyd shows failure
			Eventually(func() (v1beta1.SystemdUnitStatus, error) {
				resp, err := harness.GetDeviceWithStatusSystem(deviceId)
				if err != nil {
					return v1beta1.SystemdUnitStatus{}, err
				}
				if resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Systemd == nil {
					return v1beta1.SystemdUnitStatus{}, fmt.Errorf("systemd status not populated yet")
				}

				for _, u := range *resp.JSON200.Status.Systemd {
					if u.Unit == "chronyd.service" {
						// Found the unit we care about
						return u, nil
					}
				}
				return v1beta1.SystemdUnitStatus{}, fmt.Errorf("chronyd.service not found in status.systemd")
			}, LONGTIMEOUT, POLLING).Should(
				SatisfyAll(
					// Unit name is correct
					WithTransform(func(u v1beta1.SystemdUnitStatus) string {
						return u.Unit
					}, Equal("chronyd.service")),

					// Active state is Failed
					WithTransform(func(u v1beta1.SystemdUnitStatus) v1beta1.SystemdActiveStateType {
						return u.ActiveState
					}, Equal(v1beta1.SystemdActiveStateFailed)),
				),
				"chronyd.service should appear in status.systemd as failed when the service is stopped",
			)

			// 5. Cleanup: restore chronyd so later scenarios start from healthy state
			_, err = harness.VM.RunSSH("sudo systemctl start chronyd.service")
			Expect(err).NotTo(HaveOccurred(), "failed to restart chronyd.service on device VM")

			// Scenario 3: track a deliberately failing systemd service
			const failingServiceName = "edge-test-fail.service"

			By("creating a dummy failing systemd unit on the device")
			createFailingServiceOnDevice(harness, failingServiceName)

			By("updating matchPatterns to include chronyd.service and the failing service")
			patchDeviceJSON(harness, deviceId, fmt.Sprintf(`[
			{
				"op":"add",
				"path":"/spec/systemd",
				"value":{"matchPatterns":["chronyd.service","%s"]}
			}
		]`, failingServiceName))

			By("waiting for device status.systemd to include the failing unit (with loaded or not-found state)")
			Eventually(func() bool {
				resp, err := harness.GetDeviceWithStatusSystem(deviceId)
				if err != nil || resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Systemd == nil {
					return false
				}
				for _, unit := range *resp.JSON200.Status.Systemd {
					if unit.Unit == failingServiceName {
						loadState := string(unit.LoadState)
						return loadState == "loaded" || loadState == "not-found"
					}
				}
				return false
			}, 10*time.Minute, 10*time.Second).Should(BeTrue())

			By("ensuring the failing service is present in status.systemd")
			resp, err = harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).ToNot(BeNil())
			Expect(resp.JSON200).ToNot(BeNil())
			dev = resp.JSON200
			Expect(dev.Status.Systemd).ToNot(BeNil())

			var failUnitFound bool
			for _, unit := range *dev.Status.Systemd {
				if unit.Unit == failingServiceName {
					failUnitFound = true
					GinkgoWriter.Printf("Failing unit status: active=%s, sub=%s, load=%s\n",
						string(unit.ActiveState), unit.SubState, string(unit.LoadState))
				}
			}
			Expect(failUnitFound).To(BeTrue(), "failing service should be tracked in status.systemd")

			// Scenario 4: unmonitored failing service does not appear in systemd list
			const untrackedService = "rsyslog.service"

			By("stopping an untracked service on the device (rsyslog.service)")
			stopServiceOnDevice(harness, untrackedService)

			By("verifying rsyslog.service does not appear in status.systemd when it is not in matchPatterns")
			resp, err = harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).ToNot(BeNil())
			Expect(resp.JSON200).ToNot(BeNil())
			dev = resp.JSON200

			if dev.Status.Systemd != nil {
				for _, unit := range *dev.Status.Systemd {
					if unit.Unit == untrackedService {
						Fail("untracked rsyslog.service should not appear in status.systemd")
					}
				}
			}

			//
			// Scenario 6: clearing matchPatterns removes systemd tracking
			//

			By("clearing device.spec.systemd.matchPatterns")
			patchDeviceJSON(harness, deviceId, `[{
			"op":"add",
			"path":"/spec/systemd",
			"value":{"matchPatterns":[]}
		}]`)

			By("waiting for status.systemd to be empty after clearing matchPatterns (expected behavior)")
			Eventually(func() int {
				resp, err := harness.GetDeviceWithStatusSystem(deviceId)
				if err != nil || resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Systemd == nil {
					return 0
				}
				return len(*resp.JSON200.Status.Systemd)
			}, 10*time.Minute, 10*time.Second).Should(Equal(0))

			//
			// Cleanup: restore services and clear test configuration
			//

			By("cleaning up: removing applications and systemd from spec and restoring services")
			// Remove apps and systemd spec (ignore errors if paths don't exist)
			patchDeviceJSON(harness, deviceId, `[{"op":"remove","path":"/spec/applications"}]`)
			patchDeviceJSON(harness, deviceId, `[{"op":"remove","path":"/spec/systemd"}]`)

			restoreServiceOnDevice(harness, "chronyd.service")
			restoreServiceOnDevice(harness, failingServiceName)
			restoreServiceOnDevice(harness, untrackedService)
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

func patchDeviceJSON(h *e2e.Harness, deviceId string, jsonPatch string) {
	GinkgoWriter.Printf("patching device %s with: %s\n", deviceId, jsonPatch)
	stdout, err := h.CLI("patch", "device/"+deviceId, "--type=json", "-p", jsonPatch)
	if err != nil {
		GinkgoWriter.Printf("flightctl patch stdout: %s\n", stdout)
	}
	Expect(err).ToNot(HaveOccurred())
}

func createFailingServiceOnDevice(h *e2e.Harness, serviceName string) {
	GinkgoWriter.Printf("creating failing systemd service %s on device VM\n", serviceName)

	unitPath := fmt.Sprintf("/etc/systemd/system/%s", serviceName)
	commands := []string{
		fmt.Sprintf(`sudo sh -c 'echo "[Unit]" > %s'`, unitPath),
		fmt.Sprintf(`sudo sh -c 'echo "Description=Edge test failing service" >> %s'`, unitPath),
		fmt.Sprintf(`sudo sh -c 'echo "" >> %s'`, unitPath),
		fmt.Sprintf(`sudo sh -c 'echo "[Service]" >> %s'`, unitPath),
		fmt.Sprintf(`sudo sh -c 'echo "Type=simple" >> %s'`, unitPath),
		fmt.Sprintf(`sudo sh -c 'echo "ExecStart=/bin/false" >> %s'`, unitPath),
		fmt.Sprintf(`sudo sh -c 'echo "Restart=no" >> %s'`, unitPath),
		fmt.Sprintf(`sudo sh -c 'echo "" >> %s'`, unitPath),
		fmt.Sprintf(`sudo sh -c 'echo "[Install]" >> %s'`, unitPath),
		fmt.Sprintf(`sudo sh -c 'echo "WantedBy=multi-user.target" >> %s'`, unitPath),
		"sudo systemctl daemon-reload",
		fmt.Sprintf("sudo systemctl enable %s", serviceName),
		fmt.Sprintf("sudo systemctl start %s || true", serviceName),
	}

	for _, cmd := range commands {
		stdout, err := h.VM.RunSSH([]string{"bash", "-lc", cmd}, nil)
		if err != nil {
			GinkgoWriter.Printf("command %q failed, stdout: %s\n", cmd, stdout)
		}
		Expect(err).ToNot(HaveOccurred())
	}
}

func stopServiceOnDevice(h *e2e.Harness, serviceName string) {
	GinkgoWriter.Printf("stopping service %s on device VM\n", serviceName)
	stdout, err := h.VM.RunSSH([]string{"sudo", "systemctl", "stop", serviceName}, nil)
	if err != nil {
		GinkgoWriter.Printf("systemctl stop stdout: %s\n", stdout)
	}
	Expect(err).ToNot(HaveOccurred())
}

func restoreServiceOnDevice(h *e2e.Harness, serviceName string) {
	GinkgoWriter.Printf("restoring service %s on device VM\n", serviceName)
	stdout, err := h.VM.RunSSH([]string{"sudo", "systemctl", "restart", serviceName}, nil)
	if err != nil {
		GinkgoWriter.Printf("systemctl restart %s failed: %v, stdout: %s\n", serviceName, err, stdout)
	}
}

// returns true if the device is updating or has already updated to the expected version
func isDeviceUpdateObserved(device *v1beta1.Device, expectedVersion int) bool {
	version, err := e2e.GetRenderedVersion(device)
	if err != nil {
		GinkgoWriter.Printf("Failed to parse rendered version '%s': %v\n", device.Status.Config.RenderedVersion, err)
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
