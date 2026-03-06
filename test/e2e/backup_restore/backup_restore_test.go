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
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	// #nosec G101
	dbSecretName  = "flightctl-db-app-secret"
	dbUserKey     = "user"
	dbPasswordKey = "userPassword"
	// #nosec G101
	adminSecretName = "flightctl-db-admin-secret"
	adminSecretKey  = "masterPassword"
	// #nosec G101
	kvSecretName = "flightctl-kv-secret"
	kvSecretKey  = "password"
	dbService    = "svc/flightctl-db"
	kvService    = "svc/flightctl-kv"
	dbPort       = 5432
	kvPort       = 6379

	// 84934: fleet and device labels
	backupRestoreFleetName = "backup-restore-fleet"
	devYesLabel            = "dev"
	devYesValue            = "yes"

	KindInternalNamespace = "flightctl-internal"
	KindExternalNamespace = "flightctl-external"
	OcpNamespace          = "flightctl"

	kubectlCommand = "kubectl"
	ocCommand      = "oc"
)

// externalNS and internalNS are set in BeforeEach from runtime detection (kind vs OCP).
var cliCommand, externalNS, internalNS string

var _ = Describe("Service backup and restore", Label("backup-restore"), func() {
	var harness *e2e.Harness
	var br *e2e.BackupRestore

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		cliCommand = lo.Ternary(getContext() == testutil.KIND, kubectlCommand, ocCommand)
		externalNS = lo.Ternary(getContext() == testutil.KIND, KindExternalNamespace, OcpNamespace)
		internalNS = lo.Ternary(getContext() == testutil.KIND, KindInternalNamespace, OcpNamespace)
		login.LoginToAPIWithToken(harness)
		br = newBackupRestore(harness)
	})

	// full backup/restore flow with 3 ERs, fleet, post-backup changes, and resume.
	Context("All flightctl resources can be resumed after a backup and restore", func() {
		It("3 ERs, fleet rollout, backup, restore, then verify states and resume", Label("84934", "sanity"), func() {
			// --- Setup: 3 ERs (2 approved, 1 unapproved) ---
			By("Setting up 3 VMs and enrollment requests (2 approved with different labels, 1 unapproved)")
			ctx := harness.GetTestContext()

			// Main harness already has VM from BeforeEach (workerID). Create two more harnesses with VMs 1001, 1002.
			workerID2 := GinkgoParallelProcess()*100 + 1
			harness2, err := e2e.NewTestHarnessWithVMPool(ctx, workerID2)
			Expect(err).ToNot(HaveOccurred())
			harness2.SetTestContext(harness.GetTestContext())
			Expect(harness2.SetupVMFromPoolAndStartAgent(workerID2)).To(Succeed())
			DeferCleanup(func() {
				err := harness2.CleanUpAllTestResources()
				Expect(err).ToNot(HaveOccurred(), "harness2 cleanup")
			})
			workerID3 := GinkgoParallelProcess()*100 + 2
			harness3, err := e2e.NewTestHarnessWithVMPool(ctx, workerID3)
			Expect(err).ToNot(HaveOccurred())
			harness3.SetTestContext(harness.GetTestContext())
			Expect(harness3.SetupVMFromPoolAndStartAgent(workerID3)).To(Succeed())
			DeferCleanup(func() {
				err := harness3.CleanUpAllTestResources()
				Expect(err).ToNot(HaveOccurred(), "harness3 cleanup")
			})
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
			deviceSpecV2, err := harness.CreateFleetDeviceSpec(testutil.DeviceTags.V2)
			Expect(err).ToNot(HaveOccurred())
			selector := v1beta1.LabelSelector{MatchLabels: &map[string]string{devYesLabel: devYesValue}}
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecV2)).To(Succeed())

			By("Step 1: Waiting for device 1 to be UpToDate and reading RV at backup time")
			harness.WaitForDeviceContents(device1ID, "device 1 UpToDate", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
			}, testutil.LONGTIMEOUT)
			rvAtBackup, err := harness.GetCurrentDeviceRenderedVersion(device1ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(rvAtBackup).To(BeNumerically(">", 0), "step 1: RV at backup must be positive")

			// --- Step 2: Create DB backup (service has rvAtBackup) ---
			By("Step 2: Creating DB backup (service RV=rvAtBackup)")
			backupPath, cleanup, err := br.CreateDBBackup()
			Expect(err).ToNot(HaveOccurred(), "DB backup must succeed (kubectl, pg_dump, and cluster secrets required)")
			defer cleanup()

			// --- Step 3: Update fleet to OS v3 + inline config (new RV) ---
			var rvAfterUpdate int
			By("Step 3: Updating fleet to OS v3 and adding inline config for /etc/motd")
			deviceSpecV3, err := harness.CreateFleetDeviceSpec(testutil.DeviceTags.V3, motdInlineConfigProviderSpec())
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

			// --- Step 5: Restore DB (do not restart device); service back to RV=N ---
			By("Step 5: Scaling down FlightCtl services (except DB and KV)")
			Expect(br.ScaleDownFlightCtlServices()).To(Succeed())
			defer func() {
				Expect(br.ScaleUpFlightCtlServices()).To(Succeed())
			}()

			By("Step 5: Restoring database from backup (service back to RV=N)")
			Expect(br.RestoreDBFromBackup(backupPath)).To(Succeed())

			By("Running flightctl-restore (KV/checkpoint reconciliation)")
			Expect(br.RunFlightCtlRestore()).To(Succeed(), "flightctl-restore must succeed (build with: make flightctl-restore)")

			By("Step 5: Scaling up FlightCtl services (do not restart device)")
			Expect(br.ScaleUpFlightCtlServices()).To(Succeed())

			By("Waiting for API server to be responsive after scale up")
			Eventually(func() error {
				_, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
				return err
			}, testutil.TIMEOUT_5M, testutil.POLLING).Should(Succeed(), "API server must respond after scale up")

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
			deviceSpecNext, err := harness.CreateFleetDeviceSpec(testutil.DeviceTags.V4, motdInlineConfigProviderSpec())
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
			rv, err := harness.GetCurrentDeviceRenderedVersion(device1ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(rv).To(BeNumerically(">", rvAfterUpdate), "after resume, device RV must be greater than RV at update (rvAfterUpdate)")
		})

		// 84938: Backup taken while device update is in progress; after restore, device version <= server → AwaitingReconnect then Online (no ConflictPaused).
		It("backup during update in progress, restore then devices reach Online", Label("84938", "sanity"), func() {
			ctx := harness.GetTestContext()

			workerID2 := GinkgoParallelProcess()*100 + 1
			harness2, err := e2e.NewTestHarnessWithVMPool(ctx, workerID2)
			Expect(err).ToNot(HaveOccurred())
			harness2.SetTestContext(harness.GetTestContext())
			Expect(harness2.SetupVMFromPoolAndStartAgent(workerID2)).To(Succeed())
			DeferCleanup(func() {
				err := harness2.CleanUpAllTestResources()
				Expect(err).ToNot(HaveOccurred(), "harness2 cleanup")
			})
			device1ID, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{devYesLabel: devYesValue})
			Expect(device1ID).NotTo(BeEmpty())
			device2ID, _ := harness2.EnrollAndWaitForOnlineStatus()
			Expect(device2ID).NotTo(BeEmpty())

			selector := v1beta1.LabelSelector{MatchLabels: &map[string]string{devYesLabel: devYesValue}}
			By("Creating fleet with selector dev=yes and OS image v2")
			deviceSpecV2, err := harness.CreateFleetDeviceSpec(testutil.DeviceTags.V2)
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecV2)).To(Succeed())

			By("Waiting for device 1 to be UpToDate on v2")
			harness.WaitForDeviceContents(device1ID, "device 1 UpToDate", func(device *v1beta1.Device) bool {
				return device.Status != nil && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
			}, testutil.LONGTIMEOUT)

			By("Triggering OS update (fleet to v3) and taking DB backup while update is in progress")
			deviceSpecV3, err := harness.CreateFleetDeviceSpec(testutil.DeviceTags.V3, motdInlineConfigProviderSpec())
			Expect(err).ToNot(HaveOccurred())
			Expect(harness.CreateOrUpdateTestFleet(backupRestoreFleetName, selector, deviceSpecV3)).To(Succeed())
			// Verify device is still on v2 (update not yet applied) so backup truly occurs while update is in progress.
			backupPath, cleanup, err := br.CreateDBBackup()
			Expect(err).ToNot(HaveOccurred(), "DB backup must succeed")
			defer cleanup()

			By("Restore process: scale down, restore DB, flightctl-restore, scale up")
			Expect(br.ScaleDownFlightCtlServices()).To(Succeed())
			defer func() {
				Expect(br.ScaleUpFlightCtlServices()).To(Succeed())
			}()
			Expect(br.RestoreDBFromBackup(backupPath)).To(Succeed())
			Expect(br.RunFlightCtlRestore()).To(Succeed(), "flightctl-restore must succeed")
			Expect(br.ScaleUpFlightCtlServices()).To(Succeed())

			By("Waiting for API server to be responsive after scale up")
			Eventually(func() error {
				_, err := harness.Client.GetDeviceWithResponse(harness.Context, device1ID)
				return err
			}, testutil.TIMEOUT_5M, testutil.POLLING).Should(Succeed(), "API server must respond after scale up")

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

// motdInlineConfigProviderSpec returns a ConfigProviderSpec that writes content to /etc/motd (per test plan 4.1).
func motdInlineConfigProviderSpec() v1beta1.ConfigProviderSpec {
	mode := 0644
	inline := v1beta1.InlineConfigProviderSpec{
		Inline: []v1beta1.FileSpec{{
			Path:    "/etc/motd",
			Mode:    &mode,
			Content: "backup-restore-e2e\n",
		}},
		Name: "motd-inline",
	}
	var spec v1beta1.ConfigProviderSpec
	if err := spec.FromInlineConfigProviderSpec(inline); err != nil {
		panic(err)
	}
	return spec
}

func getContext() string {
	e2eCtx, err := e2e.GetContext()
	Expect(err).NotTo(HaveOccurred())
	return e2eCtx
}

// newBackupRestore returns a BackupRestore by calling harness.NewBackupRestore with this package's constants and current externalNS/internalNS.
func newBackupRestore(harness *e2e.Harness) *e2e.BackupRestore {
	return harness.NewBackupRestore(
		cliCommand, externalNS, internalNS, dbService, kvService,
		dbSecretName, dbUserKey, dbPasswordKey,
		adminSecretName, adminSecretKey, kvSecretName, kvSecretKey,
		dbPort, kvPort,
	)
}
