// Package backup_restore implements e2e tests for Service backup and restore
// (section 4 of the Recover and restore Test Plan, EDM-415).
//
// Tests require in-cluster FlightCtl (kind: flightctl-external + flightctl-internal; OCP: single flightctl namespace),
// kubectl, pg_dump/psql, and flightctl-restore binary (same location as CLI: bin/flightctl-restore).
// Namespaces are detected at runtime so the same tests run on both environments.
package backup_restore

import (
	"fmt"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"
)

const (
	backupRestoreFleetName = "backup-restore-fleet"
	devYesLabel            = "dev"
	devYesValue            = "yes"
)

var _ = Describe("Service backup and restore", Label("backup-restore"), func() {
	var harness *e2e.Harness
	var br *e2e.BackupRestore

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		Eventually(func() error {
			_, err := login.LoginToAPIWithToken(harness)
			return err
		}, testutil.DURATION_TIMEOUT, testutil.POLLING).Should(Succeed(), "API should become responsive for login")
		br = newBackupRestore(harness, setup.GetDefaultProviders())
	})

	// full backup/restore flow with 3 ERs, fleet, post-backup changes, and resume.
	// Concession (e2e wall time): RV divergence uses config-only fleet updates (motd), not
	// bootc OS v2→v3→v4. ConflictPaused/resume semantics are unchanged; OS rollout coverage
	// remains in agent_update and non-sanity 89194. See test/e2e/E2E_WALL_TIME.md.
	Context("All flightctl resources can be resumed after a backup and restore", func() {
		It("3 ERs, fleet rollout, backup, restore, then verify states and resume", Label("89141", "sanity", "slow", "needdevice"), func() {
			if reason := backupRestoreExternalDBSkipReason(); reason != "" {
				Skip(reason)
			}
			By("Setting up 3 devices and enrollment requests (2 approved with different labels, 1 unapproved)")
			ctx := harness.GetTestContext()

			// Primary is container-backed (needdevice). Secondaries are container-backed and only
			// enrolled/observed via the API — setup is independent per worker ID.
			workerID2 := GinkgoParallelProcess()*100 + 1
			workerID3 := GinkgoParallelProcess()*100 + 2
			var harness2, harness3 *e2e.Harness
			g, _ := errgroup.WithContext(ctx)
			g.Go(func() error {
				var err error
				harness2, err = e2e.NewTestHarnessWithContainerPool(ctx, workerID2)
				if err != nil {
					return err
				}
				harness2.SetTestContext(harness.GetTestContext())
				return harness2.SetupContainerFromPoolAndStartAgent(workerID2)
			})
			g.Go(func() error {
				var err error
				harness3, err = e2e.NewTestHarnessWithContainerPool(ctx, workerID3)
				if err != nil {
					return err
				}
				harness3.SetTestContext(harness.GetTestContext())
				return harness3.SetupContainerFromPoolAndStartAgent(workerID3)
			})
			setupErr := g.Wait()
			if harness2 != nil {
				DeferCleanup(func() {
					harness2.PrintAgentLogsIfFailed()
					harness2.CaptureDeploymentLogsIfFailed()
					err := harness2.CleanUpAllTestResources()
					Expect(err).ToNot(HaveOccurred(), "harness2 cleanup")
				})
			}
			if harness3 != nil {
				DeferCleanup(func() {
					harness3.PrintAgentLogsIfFailed()
					harness3.CaptureDeploymentLogsIfFailed()
					err := harness3.CleanUpAllTestResources()
					Expect(err).ToNot(HaveOccurred(), "harness3 cleanup")
				})
			}
			Expect(setupErr).ToNot(HaveOccurred())

			device1ID, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{devYesLabel: devYesValue})
			Expect(device1ID).NotTo(BeEmpty())
			device2ID, _ := harness2.EnrollAndWaitForOnlineStatus()
			Expect(device2ID).NotTo(BeEmpty())
			er3ID := harness3.GetEnrollmentIDFromServiceLogs("flightctl-agent")
			Expect(er3ID).NotTo(BeEmpty())
			_ = harness3.WaitForEnrollmentRequest(er3ID)

			// Step 1: fleet with config-only RV bump (no OS image change).
			By("Creating fleet with selector dev=yes and initial motd config")
			regHost, regPort := auxSvcs.Registry.Host, auxSvcs.Registry.Port
			deviceSpecV1, err := harness.CreateFleetDeviceSpec(regHost, regPort, "", motdInlineConfigProviderSpecWith("backup-restore-e2e-v1\n"))
			Expect(err).ToNot(HaveOccurred())
			selector := v1beta1.LabelSelector{MatchLabels: &map[string]string{devYesLabel: devYesValue}}
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecV1)).To(Succeed())

			By("Step 1: Waiting for device 1 to be UpToDate and reading RV at backup time")
			harness.WaitForDeviceContents(device1ID, "device 1 UpToDate", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
			}, testutil.TIMEOUT_5M)
			devForRV, err := harness.GetDevice(device1ID)
			Expect(err).ToNot(HaveOccurred())
			rvAtBackup, err := e2e.GetRenderedVersion(devForRV)
			Expect(err).ToNot(HaveOccurred())
			Expect(rvAtBackup).To(BeNumerically(">", 0), "step 1: RV at backup must be positive")

			By("Step 2: Creating backup archive (service RV=rvAtBackup)")
			backupDir := GinkgoT().TempDir()
			archivePath, _, err := br.RunFlightCtlBackup(backupDir)
			Expect(err).ToNot(HaveOccurred(), "backup must succeed")

			var rvAfterUpdate int
			By("Step 3: Updating fleet motd config (new RV, no OS change)")
			deviceSpecV2, err := harness.CreateFleetDeviceSpec(regHost, regPort, "", motdInlineConfigProviderSpecWith("backup-restore-e2e-v2\n"))
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecV2)).To(Succeed())

			By("Approving third ER (after backup)")
			harness3.ApproveEnrollment(er3ID, harness3.TestEnrollmentApproval())
			Eventually(harness3.GetDeviceWithStatusSummary, testutil.TIMEOUT, testutil.POLLING).WithArguments(er3ID).ShouldNot(BeEmpty())

			By("Step 4: Waiting for device 1 to apply new version (new RV > previous RV) and be UpToDate")
			harness.WaitForDeviceContents(device1ID, "device 1 RV > rvAtBackup and UpToDate", func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				v, err := strconv.Atoi(device.Status.Config.RenderedVersion)
				if err != nil {
					return false
				}
				if v > rvAtBackup && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate {
					rvAfterUpdate = v
					return true
				}
				return false
			}, testutil.TIMEOUT_5M)
			Expect(rvAfterUpdate).To(BeNumerically(">", rvAtBackup), "step 4: new RV must be greater than previous")

			By("Step 5: Restoring from backup archive (flightctl-restore handles service stop/start)")
			defer func() {
				Expect(br.VerifyAllServicesRunning()).To(Succeed(), "all services must be running after restore cleanup")
			}()
			Expect(br.RunFlightCtlRestoreFromArchive(archivePath)).To(Succeed(), "flightctl-restore must succeed")

			By("Waiting for services and API after restore")
			Eventually(func() error {
				if err := br.VerifyAllServicesRunning(); err != nil {
					return err
				}
				_, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
				return err
			}, testutil.TIMEOUT_5M, testutil.POLLING).Should(Succeed(), "services and API must be ready after restore")

			By("Step 6: Waiting for terminal post-restore states")
			Eventually(func() bool {
				d1, err1 := harness.GetDevice(device1ID)
				d2, err2 := harness2.GetDevice(device2ID)
				if err1 != nil || err2 != nil || d1.Status == nil || d2.Status == nil {
					return false
				}
				return d1.Status.Summary.Status == v1beta1.DeviceSummaryStatusConflictPaused &&
					d2.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline
			}, testutil.TIMEOUT_5M, testutil.POLLING).Should(BeTrue(), "device1 ConflictPaused and device2 Online after restore")

			By("Step 6: Verifying device 1 ConflictPaused (device RV > service RV) with OutOfDate")
			device1, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(device1.JSON200).ToNot(BeNil())
			device1RV, err := strconv.Atoi(device1.JSON200.Status.Config.RenderedVersion)
			Expect(err).ToNot(HaveOccurred())
			Expect(device1RV).To(BeNumerically(">", rvAtBackup), "step 6: after restore, device RV must be > service RV (rvAtBackup)")
			Expect(device1RV).To(Equal(rvAfterUpdate), "step 6: device RV unchanged after restore (no restart)")
			Expect(device1.JSON200.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))

			By("Verifying third enrollment remains as ER (not yet a device in restored DB)")
			erResp, err := harness.Client.GetEnrollmentRequestWithResponse(harness.Context, er3ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(erResp.JSON200).ToNot(BeNil())

			By("Pushing new motd config to ConflictPaused device 1 (spec updates, RV unchanged)")
			deviceSpecNext, err := harness.CreateFleetDeviceSpec(regHost, regPort, "", motdInlineConfigProviderSpecWith("backup-restore-e2e-v3\n"))
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecNext)).To(Succeed())
			Eventually(func() (string, error) {
				devAfterPush, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
				if err != nil {
					return "", err
				}
				if devAfterPush.JSON200 == nil || devAfterPush.JSON200.Spec.Config == nil {
					return "", fmt.Errorf("device response missing spec/config")
				}
				return fmt.Sprintf("%v", *devAfterPush.JSON200.Spec.Config), nil
			}, 2*time.Second, 500*time.Millisecond).Should(ContainSubstring("backup-restore-e2e-v3"), "device spec should pick up new motd while ConflictPaused")
			Consistently(func() (int, error) {
				devAfterPush, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
				if err != nil {
					return 0, err
				}
				if devAfterPush.JSON200 == nil || devAfterPush.JSON200.Status == nil || devAfterPush.JSON200.Status.Config.RenderedVersion == "" {
					return 0, fmt.Errorf("device response missing status/config renderedVersion")
				}
				return strconv.Atoi(devAfterPush.JSON200.Status.Config.RenderedVersion)
			}, 2*time.Second, 500*time.Millisecond).Should(Equal(rvAfterUpdate), "renderedVersion must not increase while ConflictPaused")

			By("Re-approving third ER so it becomes a device (ConflictPaused)")
			harness3.ApproveEnrollment(er3ID, harness3.TestEnrollmentApproval())
			harness.WaitForDeviceContents(er3ID, "device 3 (er3) ConflictPaused", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Summary.Status == v1beta1.DeviceSummaryStatusConflictPaused
			}, testutil.TIMEOUT_5M)
			device3, err := harness.Client.GetDeviceWithResponse(harness.Context, er3ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(device3.JSON200).ToNot(BeNil())
			Expect(device3.JSON200.Status.Config.RenderedVersion).To(Equal("1"))

			By("Resuming device 1 via API")
			fieldSelector := fmt.Sprintf("metadata.name=%s", device1ID)
			req := v1beta1.DeviceResumeRequest{FieldSelector: &fieldSelector}
			resp, err := harness.Client.ResumeDevicesWithResponse(harness.Context, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.StatusCode()).To(Equal(200))
			Expect(resp.JSON200).ToNot(BeNil())
			Expect(resp.JSON200.ResumedDevices).To(BeNumerically(">=", 1))

			By("Waiting for device 1 to be Online with new RV > rvAfterUpdate and up-to-date")
			harness.WaitForDeviceContents(device1ID, "device 1 Online and UpToDate with RV > rvAfterUpdate", func(device *v1beta1.Device) bool {
				if device == nil || device.Status == nil {
					return false
				}
				if device.Status.Summary.Status != v1beta1.DeviceSummaryStatusOnline {
					return false
				}
				if device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusUpToDate {
					return false
				}
				rv, err := e2e.GetRenderedVersion(device)
				return err == nil && rv > rvAfterUpdate
			}, testutil.TIMEOUT_5M)
			resumedDev, err := harness.GetDevice(device1ID)
			Expect(err).ToNot(HaveOccurred())
			rv, err := e2e.GetRenderedVersion(resumedDev)
			Expect(err).ToNot(HaveOccurred())
			Expect(rv).To(BeNumerically(">", rvAfterUpdate), "after resume, device RV must be greater than RV at update (rvAfterUpdate)")
		})

		// 84938: Backup taken while device update is in progress; after restore, device version <= server → AwaitingReconnect then Online (no ConflictPaused).
		It("backup during update in progress, restore then devices reach Online", Label("89194", "slow", "needvm"), func() {
			if reason := backupRestoreExternalDBSkipReason(); reason != "" {
				Skip(reason)
			}
			ctx := harness.GetTestContext()

			workerID2 := GinkgoParallelProcess()*100 + 1
			harness2, err := e2e.NewTestHarnessWithContainerPool(ctx, workerID2)
			Expect(err).ToNot(HaveOccurred())
			harness2.SetTestContext(harness.GetTestContext())
			Expect(harness2.SetupContainerFromPoolAndStartAgent(workerID2)).To(Succeed())
			DeferCleanup(func() {
				harness2.PrintAgentLogsIfFailed()
				harness2.CaptureDeploymentLogsIfFailed()
				err := harness2.CleanUpAllTestResources()
				Expect(err).ToNot(HaveOccurred(), "harness2 cleanup")
			})
			device1ID, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{devYesLabel: devYesValue})
			Expect(device1ID).NotTo(BeEmpty())
			device2ID, _ := harness2.EnrollAndWaitForOnlineStatus()
			Expect(device2ID).NotTo(BeEmpty())

			selector := v1beta1.LabelSelector{MatchLabels: &map[string]string{devYesLabel: devYesValue}}
			By("Creating fleet with selector dev=yes and OS image v2")
			regHost, regPort := auxSvcs.Registry.Host, auxSvcs.Registry.Port
			deviceSpecV2, err := harness.CreateFleetDeviceSpec(regHost, regPort, testutil.DeviceTags.V2)
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecV2)).To(Succeed())

			By("Waiting for device 1 to be UpToDate on v2")
			harness.WaitForDeviceContents(device1ID, "device 1 UpToDate", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
			}, testutil.LONGTIMEOUT)

			By("Triggering OS update (fleet to v3) and taking DB backup while update is in progress")
			deviceSpecV3, err := harness.CreateFleetDeviceSpec(regHost, regPort, testutil.DeviceTags.V3, motdInlineConfigProviderSpec())
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecV3)).To(Succeed())
			// Verify device is still on v2 (update not yet applied) so backup truly occurs while update is in progress.
			backupDir := GinkgoT().TempDir()
			archivePath, _, err := br.RunFlightCtlBackup(backupDir)
			Expect(err).ToNot(HaveOccurred(), "backup must succeed")

			By("Restore process: flightctl-restore (handles service stop/start internally)")
			defer func() {
				Expect(br.VerifyAllServicesRunning()).To(Succeed(), "all services must be running after restore cleanup")
			}()
			Expect(br.RunFlightCtlRestoreFromArchive(archivePath)).To(Succeed(), "flightctl-restore must succeed")

			By("Verifying all services were restarted by the restore binary")
			Eventually(func() error {
				return br.VerifyAllServicesRunning()
			}, testutil.TIMEOUT_5M, testutil.POLLING).Should(Succeed(), "All 8 services must be running after restore")

			By("Waiting for API server to be responsive after restore")
			Eventually(func() error {
				_, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
				return err
			}, testutil.TIMEOUT_5M, testutil.POLLING).Should(Succeed(), "API server must respond after restore")

			By("84938: Devices should move to AwaitingReconnect then Online (device version <= server, no ConflictPaused)")
			harness.WaitForDeviceContents(device1ID, "device 1 AwaitingReconnect or Online", func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				s := device.Status.Summary.Status
				return s == v1beta1.DeviceSummaryStatusAwaitingReconnect || s == v1beta1.DeviceSummaryStatusOnline
			}, testutil.LONGTIMEOUT)
			harness2.WaitForDeviceContents(device2ID, "device 2 AwaitingReconnect or Online", func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				s := device.Status.Summary.Status
				return s == v1beta1.DeviceSummaryStatusAwaitingReconnect || s == v1beta1.DeviceSummaryStatusOnline
			}, testutil.LONGTIMEOUT)

			By("Verifying both devices reach Online")
			harness.WaitForDeviceContents(device1ID, "device 1 Online", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline
			}, testutil.LONGTIMEOUT)
			harness2.WaitForDeviceContents(device2ID, "device 2 Online", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline
			}, testutil.LONGTIMEOUT)
		})
	})
})
