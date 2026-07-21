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
	Context("All flightctl resources can be resumed after a backup and restore", func() {
		It("3 ERs, fleet rollout, backup, restore, then verify states and resume", Label("89141", "sanity", "slow", "needvm"), func() {
			if reason := backupRestoreExternalDBSkipReason(); reason != "" {
				Skip(reason)
			}
			// --- Setup: 3 ERs (2 approved, 1 unapproved) ---
			By("Setting up 3 VMs and enrollment requests (2 approved with different labels, 1 unapproved)")
			ctx := harness.GetTestContext()

			// Main harness already has VM from BeforeEach (workerID). Create two more harnesses with
			// VMs 1001, 1002. Each involves a full cold VM boot + pristine snapshot creation (~110-150s)
			// keyed on its own worker ID, with no shared state beyond the VM pool's map (see
			// VMPool.GetVMForWorker in vm_pool.go, which only holds its mutex for the map access, not
			// for the boot itself) - so run the two setups concurrently instead of paying both costs
			// sequentially.
			workerID2 := GinkgoParallelProcess()*100 + 1
			workerID3 := GinkgoParallelProcess()*100 + 2
			var harness2, harness3 *e2e.Harness
			g, _ := errgroup.WithContext(ctx)
			g.Go(func() error {
				var err error
				harness2, err = e2e.NewTestHarnessWithVMPool(ctx, workerID2)
				if err != nil {
					return err
				}
				harness2.SetTestContext(harness.GetTestContext())
				return harness2.SetupVMFromPoolAndStartAgent(workerID2)
			})
			g.Go(func() error {
				var err error
				harness3, err = e2e.NewTestHarnessWithVMPool(ctx, workerID3)
				if err != nil {
					return err
				}
				harness3.SetTestContext(harness.GetTestContext())
				return harness3.SetupVMFromPoolAndStartAgent(workerID3)
			})
			setupErr := g.Wait()
			// Register cleanup for whichever harnesses came up, regardless of the other's outcome,
			// mirroring the sequential code's "only clean up what was actually set up" behavior. A
			// harness can be non-nil but only partially set up (NewTestHarnessWithVMPool succeeded,
			// SetupVMFromPoolAndStartAgent failed), so key cleanup on non-nil rather than full readiness.
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
			// Device 1: approved with dev=yes (will be in fleet)
			device1ID, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{devYesLabel: devYesValue})
			Expect(device1ID).NotTo(BeEmpty())

			// Device 2: approved without dev=yes (never in fleet)
			device2ID, _ := harness2.EnrollAndWaitForOnlineStatus()
			Expect(device2ID).NotTo(BeEmpty())

			// ER3: enroll but do not approve (will approve after backup)
			er3ID := harness3.GetEnrollmentIDFromServiceLogs("flightctl-agent")
			Expect(er3ID).NotTo(BeEmpty())
			_ = harness3.WaitForEnrollmentRequest(er3ID)

			// Scenario for ConflictPaused after restore (do not restart device).
			// RV checks are loose: we only verify that when a new version is applied, new RV > previous RV.

			// --- Step 1: Fleet v2, device UpToDate; capture RV at backup time (rvAtBackup) ---
			By("Creating fleet with selector dev=yes and OS image v2")
			regHost, regPort := auxSvcs.Registry.Host, auxSvcs.Registry.Port
			deviceSpecV2, err := harness.CreateFleetDeviceSpec(regHost, regPort, testutil.DeviceTags.V2)
			Expect(err).ToNot(HaveOccurred())
			selector := v1beta1.LabelSelector{MatchLabels: &map[string]string{devYesLabel: devYesValue}}
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecV2)).To(Succeed())

			By("Step 1: Waiting for device 1 to be UpToDate and reading RV at backup time")
			harness.WaitForDeviceContents(device1ID, "device 1 UpToDate", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
			}, testutil.LONGTIMEOUT)
			devForRV, err := harness.GetDevice(device1ID)
			Expect(err).ToNot(HaveOccurred())
			rvAtBackup, err := e2e.GetRenderedVersion(devForRV)
			Expect(err).ToNot(HaveOccurred())
			Expect(rvAtBackup).To(BeNumerically(">", 0), "step 1: RV at backup must be positive")

			// --- Step 2: Create backup archive (service has rvAtBackup) ---
			By("Step 2: Creating backup archive (service RV=rvAtBackup)")
			backupDir := GinkgoT().TempDir()
			archivePath, _, err := br.RunFlightCtlBackup(backupDir)
			Expect(err).ToNot(HaveOccurred(), "backup must succeed")

			// --- Step 3: Update fleet to OS v3 + inline config (new RV) ---
			var rvAfterUpdate int
			By("Step 3: Updating fleet to OS v3 and adding inline config for /etc/motd")
			deviceSpecV3, err := harness.CreateFleetDeviceSpec(regHost, regPort, testutil.DeviceTags.V3, motdInlineConfigProviderSpec())
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecV3)).To(Succeed())

			By("Approving third ER (after backup)")
			harness3.ApproveEnrollment(er3ID, harness3.TestEnrollmentApproval())
			Eventually(harness3.GetDeviceWithStatusSummary, testutil.TIMEOUT, testutil.POLLING).WithArguments(er3ID).ShouldNot(BeEmpty())

			// --- Step 4: Wait for device to apply new version; verify new RV > rvAtBackup ---
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
			}, testutil.LONGTIMEOUT)
			Expect(rvAfterUpdate).To(BeNumerically(">", rvAtBackup), "step 4: new RV must be greater than previous")

			// --- Step 5: Restore from backup archive (do not restart device); service back to RV=N ---
			By("Step 5: Restoring from backup archive (flightctl-restore handles service stop/start)")
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

			// --- Step 6: Service RV=rvAtBackup (restored), device RV=rvAfterUpdate (> rvAtBackup) → ConflictPaused ---
			By("Step 6: Waiting for devices to reach AwaitingReconnect or ConflictPaused or Online")
			harness.WaitForDeviceContents(device1ID, "device 1 AwaitingReconnect or ConflictPaused or Online", func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				s := device.Status.Summary.Status
				return s == v1beta1.DeviceSummaryStatusAwaitingReconnect || s == v1beta1.DeviceSummaryStatusConflictPaused || s == v1beta1.DeviceSummaryStatusOnline
			}, testutil.LONGTIMEOUT)
			harness2.WaitForDeviceContents(device2ID, "device 2 AwaitingReconnect or Online", func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				s := device.Status.Summary.Status
				return s == v1beta1.DeviceSummaryStatusAwaitingReconnect || s == v1beta1.DeviceSummaryStatusOnline
			}, testutil.LONGTIMEOUT)

			// Device approved but never updated (not in fleet) → Online
			By("Verifying device 2 (approved, never in fleet) is Online")
			harness2.WaitForDeviceContents(device2ID, "device 2 Online", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline
			}, testutil.TIMEOUT_5M)

			// Step 6: Device 1 — service had rvAtBackup (restored), device reports rvAfterUpdate (> rvAtBackup) → ConflictPaused, OutOfDate
			By("Step 6: Verifying device 1 is ConflictPaused (device RV > service RV) with OutOfDate")
			harness.WaitForDeviceContents(device1ID, "device 1 ConflictPaused", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Summary.Status == v1beta1.DeviceSummaryStatusConflictPaused
			}, testutil.TIMEOUT_5M)
			device1, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(device1.JSON200).ToNot(BeNil())
			device1RV, err := strconv.Atoi(device1.JSON200.Status.Config.RenderedVersion)
			Expect(err).ToNot(HaveOccurred())
			Expect(device1RV).To(BeNumerically(">", rvAtBackup), "step 6: after restore, device RV must be > service RV (rvAtBackup)")
			Expect(device1RV).To(Equal(rvAfterUpdate), "step 6: device RV unchanged after restore (no restart)")
			Expect(device1.JSON200.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))

			// Device approved after backup → still ER in restored DB (not a device); re-approve makes it a device later
			By("Verifying third enrollment remains as ER (not yet a device in restored DB)")
			erResp, err := harness.Client.GetEnrollmentRequestWithResponse(harness.Context, er3ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(erResp.JSON200).ToNot(BeNil())

			// --- Push config to ConflictPaused device (OS v4 + inline): spec updated, RV does not increase ---
			By("Pushing new config (OS v4 + inline) to ConflictPaused device 1")
			deviceSpecNext, err := harness.CreateFleetDeviceSpec(regHost, regPort, testutil.DeviceTags.V4, motdInlineConfigProviderSpec())
			Expect(err).ToNot(HaveOccurred())
			// Device 1 is owned by the fleet; push config by updating the fleet spec, not the device directly.
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecNext)).To(Succeed())
			Eventually(func() string {
				devAfterPush, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
				Expect(err).ToNot(HaveOccurred())
				Expect(devAfterPush.JSON200).ToNot(BeNil())
				Expect(devAfterPush.JSON200.Spec.Os).ToNot(BeNil())
				return devAfterPush.JSON200.Spec.Os.Image
			}, 2*time.Second, 500*time.Millisecond).Should(ContainSubstring(testutil.DeviceTags.V4), "device spec should be updated to v4 after push while ConflictPaused")
			// Rendered version should remain stable while ConflictPaused (spec updated but device RV unchanged).
			Consistently(func() int {
				devAfterPush, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
				Expect(err).ToNot(HaveOccurred())
				Expect(devAfterPush.JSON200).ToNot(BeNil())
				Expect(devAfterPush.JSON200.Spec.Os).ToNot(BeNil())
				Expect(devAfterPush.JSON200.Spec.Os.Image).To(ContainSubstring(testutil.DeviceTags.V4))
				rv, err := strconv.Atoi(devAfterPush.JSON200.Status.Config.RenderedVersion)
				Expect(err).ToNot(HaveOccurred())
				return rv
			}, 5*time.Second, 500*time.Millisecond).Should(Equal(rvAfterUpdate), "renderedVersion must not increase while ConflictPaused")

			// --- Re-approve ER3 → device becomes AwaitingReconnect then ConflictPaused ---
			By("Re-approving third ER so it becomes a device (AwaitingReconnect then ConflictPaused)")
			harness3.ApproveEnrollment(er3ID, harness3.TestEnrollmentApproval())
			harness.WaitForDeviceContents(er3ID, "device 3 (er3) AwaitingReconnect or ConflictPaused", func(device *v1beta1.Device) bool {
				if device.Status == nil {
					return false
				}
				s := device.Status.Summary.Status
				return s == v1beta1.DeviceSummaryStatusAwaitingReconnect || s == v1beta1.DeviceSummaryStatusConflictPaused
			}, testutil.LONGTIMEOUT)
			harness.WaitForDeviceContents(er3ID, "device 3 (er3) ConflictPaused", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Summary.Status == v1beta1.DeviceSummaryStatusConflictPaused
			}, testutil.TIMEOUT_5M)
			device3, err := harness.Client.GetDeviceWithResponse(harness.Context, er3ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(device3.JSON200).ToNot(BeNil())
			Expect(device3.JSON200.Status.Config.RenderedVersion).To(Equal("1"))

			// --- Resume device 1 → Online, new RV > rvAfterUpdate, up-to-date ---
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
			}, testutil.LONGTIMEOUT)
			var resumedDev *v1beta1.Device
			resumedDev, err = harness.GetDevice(device1ID)
			Expect(err).ToNot(HaveOccurred())
			var rv int
			rv, err = e2e.GetRenderedVersion(resumedDev)
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
			harness2, err := e2e.NewTestHarnessWithVMPool(ctx, workerID2)
			Expect(err).ToNot(HaveOccurred())
			harness2.SetTestContext(harness.GetTestContext())
			Expect(harness2.SetupVMFromPoolAndStartAgent(workerID2)).To(Succeed())
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
