package agent_test

import (
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VM Agent systemd status", func() {
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

	Context("systemd", func() {
		It("reports systemd services and applications based on device spec", Label("sanity", "agent", "86238"), func() {
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
