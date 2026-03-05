package multiorg_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// adminLoginFunc returns a LoginFunc that logs in as admin using the given credentials and context.
func adminLoginFunc(k8sContext, k8sApiEndpoint string) e2e.LoginFunc {
	return func(h *e2e.Harness) error {
		return login.LoginAsUser(h, adminUser, adminPass, k8sContext, k8sApiEndpoint)
	}
}

const (
	devicesPerUser       = 10
	totalDevices         = 3 * devicesPerUser
	deviceEnrollTimeout  = 120 * time.Second
	deviceEnrollPolling  = 2 * time.Second
	simulatorStopTimeout = 10 * time.Second

	adminUser    = "admin"
	adminPass    = "admin"
	operatorUser = "operator"
	operatorPass = "operator"
	viewerUser   = "viewer"
	viewerPass   = "viewer"
)

var _ = Describe("Multiorg RBAC E2E Tests", Label("multiorg", "e2e"), func() {
	var (
		harness           *e2e.Harness
		suiteCtx          context.Context
		defaultK8sContext string
		k8sApiEndpoint    string
		flightCtlNs       string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		suiteCtx = e2e.GetWorkerContext()

		var err error
		defaultK8sContext, err = harness.GetDefaultK8sContext()
		Expect(err).ToNot(HaveOccurred(), "Failed to get default K8s context")
		k8sApiEndpoint, err = harness.GetK8sApiEndpoint(suiteCtx, defaultK8sContext)
		Expect(err).ToNot(HaveOccurred(), "Failed to get Kubernetes API endpoint")
		flightCtlNs = os.Getenv("FLIGHTCTL_NS")
	})

	Context("Shared organization verification", func() {
		It("all users should belong to the same flightctl organization", Label("88358"), func() {
			orgIDs := loginAndCollectOrgIDs(harness, defaultK8sContext, k8sApiEndpoint,
				[]userCred{
					{adminUser, adminPass},
					{operatorUser, operatorPass},
					{viewerUser, viewerPass},
				})

			By("Verifying all users share the same organization")
			Expect(orgIDs[operatorUser]).To(Equal(orgIDs[adminUser]), "Admin and operator should share the same organization")
			Expect(orgIDs[viewerUser]).To(Equal(orgIDs[adminUser]), "Admin and viewer should share the same organization")
		})
	})

	Context("Device enrollment in shared organization", func() {
		var simulatorCmds []*exec.Cmd

		BeforeEach(func() {
			simulatorCmds = make([]*exec.Cmd, 0)
		})

		AfterEach(func() {
			By("Switching back to admin before stopping simulators")
			_ = login.LoginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			harness.StopAllSimulators(simulatorCmds, simulatorStopTimeout)
			simulatorCmds = nil
		})

		It("should enroll 30 devices as admin and all users should see them", Label("88359"), func() {
			ctx := context.Background()
			testID := harness.GetTestIDFromContext()

			By("Logging in as admin to setup simulator config and create devices")
			err := login.LoginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			_, err = harness.SetupDeviceSimulatorAgentConfig(0, 0)
			Expect(err).ToNot(HaveOccurred(), "Failed to setup simulator agent config as admin")

			By(fmt.Sprintf("Starting 3 device simulators (%d devices each) as admin", devicesPerUser))
			for i := 0; i < 3; i++ {
				initialIndex := i * devicesPerUser
				cmd, simErr := harness.StartLabeledSimulator(ctx, testID, adminUser, initialIndex, devicesPerUser)
				Expect(simErr).ToNot(HaveOccurred(), fmt.Sprintf("Failed to start simulator batch %d", i))
				simulatorCmds = append(simulatorCmds, cmd)
				GinkgoWriter.Printf("Simulator batch %d started (devices %d-%d)\n",
					i, initialIndex, initialIndex+devicesPerUser-1)
			}

			By(fmt.Sprintf("Waiting for all %d devices to enroll as admin", totalDevices))
			Eventually(func() int {
				return harness.CountDevicesByLabel(testID)
			}, deviceEnrollTimeout, deviceEnrollPolling).Should(Equal(totalDevices),
				fmt.Sprintf("Expected %d total devices to enroll", totalDevices))
			GinkgoWriter.Printf("All %d devices enrolled successfully as admin\n", totalDevices)

			By("Switching to operator and verifying all devices are visible")
			err = login.LoginAsUser(harness, operatorUser, operatorPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			operatorCount := harness.CountDevicesByLabel(testID)
			Expect(operatorCount).To(Equal(totalDevices),
				fmt.Sprintf("Operator should see all %d devices in the shared organization", totalDevices))
			GinkgoWriter.Printf("Operator sees %d devices (expected %d)\n", operatorCount, totalDevices)

			By("Switching to viewer and verifying all devices are visible")
			err = login.LoginAsUser(harness, viewerUser, viewerPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			viewerCount := harness.CountDevicesByLabel(testID)
			Expect(viewerCount).To(Equal(totalDevices),
				fmt.Sprintf("Viewer should see all %d devices in the shared organization", totalDevices))
			GinkgoWriter.Printf("Viewer sees %d devices (expected %d)\n", viewerCount, totalDevices)
		})
	})

	Context("RBAC enforcement within shared organization", func() {
		var simulatorCmds []*exec.Cmd

		BeforeEach(func() {
			simulatorCmds = make([]*exec.Cmd, 0)
		})

		AfterEach(func() {
			By("Switching back to admin before cleanup")
			_ = login.LoginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			harness.StopAllSimulators(simulatorCmds, simulatorStopTimeout)
			simulatorCmds = nil
		})

		It("admin should have full CRUD access to devices, fleets, and repositories", Label("88360"), func() {
			By("Logging in as admin")
			err := login.LoginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			adminLabels := &map[string]string{"test": "multiorg-admin"}

			By("Testing that admin can perform all operations on devices, fleets, and repositories")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness,
				[]string{util.Device, util.Fleet, util.Repository},
				true, adminLabels, flightCtlNs, []string{e2e.OperationAll})
			Expect(err).ToNot(HaveOccurred())

			By("Testing that admin can read enrollment requests and events")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)
			Expect(err).ToNot(HaveOccurred())
		})

		It("operator should have full CRUD access to devices and fleets", Label("88361"), func() {
			By("Logging in as operator")
			err := login.LoginAsUser(harness, operatorUser, operatorPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			operatorLabels := &map[string]string{"test": "multiorg-operator"}

			By("Testing that operator can perform all CRUD operations on devices")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness,
				[]string{util.Device},
				true, operatorLabels, flightCtlNs, []string{e2e.OperationAll})
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operator can perform all CRUD operations on fleets")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness,
				[]string{util.Fleet},
				true, operatorLabels, flightCtlNs, []string{e2e.OperationAll})
			Expect(err).ToNot(HaveOccurred())
		})

		It("viewer should only be able to read devices and fleets, not create or delete", Label("88362"), func() {
			By("Logging in as viewer")
			err := login.LoginAsUser(harness, viewerUser, viewerPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that viewer can list devices and fleets")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{util.Device, util.Fleet}, true)
			Expect(err).ToNot(HaveOccurred())

			viewerLabels := &map[string]string{"test": "multiorg-viewer"}

			By("Testing that viewer cannot create devices")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness,
				[]string{util.Device},
				false, viewerLabels, flightCtlNs, []string{e2e.OperationCreate})
			Expect(err).ToNot(HaveOccurred())

			By("Testing that viewer cannot create fleets")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness,
				[]string{util.Fleet},
				false, viewerLabels, flightCtlNs, []string{e2e.OperationCreate})
			Expect(err).ToNot(HaveOccurred())

			By("Creating resources as admin so viewer can attempt to delete them")
			err = login.LoginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			adminLabels := &map[string]string{"test": "multiorg-viewer-delete-test"}

			_, deviceName, _, deviceCreateErr := harness.CreateResource(util.Device)
			Expect(deviceCreateErr).ToNot(HaveOccurred(), "Admin should be able to create a device for viewer delete test")

			_, fleetName, _, fleetCreateErr := harness.CreateResource(util.Fleet)
			Expect(fleetCreateErr).ToNot(HaveOccurred(), "Admin should be able to create a fleet for viewer delete test")
			_ = adminLabels

			By("Switching back to viewer and attempting to delete admin-created resources")
			err = login.LoginAsUser(harness, viewerUser, viewerPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that viewer cannot delete a device")
			out, deleteErr := harness.CLI("delete", util.Device, deviceName)
			Expect(deleteErr).To(HaveOccurred(), "Viewer should not be able to delete a device")
			Expect(out).To(ContainSubstring("403"), "Expected 403 Forbidden for viewer device delete")

			By("Testing that viewer cannot delete a fleet")
			out, deleteErr = harness.CLI("delete", util.Fleet, fleetName)
			Expect(deleteErr).To(HaveOccurred(), "Viewer should not be able to delete a fleet")
			Expect(out).To(ContainSubstring("403"), "Expected 403 Forbidden for viewer fleet delete")

			By("Cleaning up: switching to admin to delete test resources")
			err = login.LoginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			_, _ = harness.CleanUpResource(util.Device, deviceName)
			_, _ = harness.CleanUpResource(util.Fleet, fleetName)
		})

		It("viewer cannot decommission a device", Label("88363"), func() {
			By("Creating devices via simulator as admin")
			deviceName, cmd, err := harness.EnrollDeviceForDecommissionTest(adminLoginFunc(defaultK8sContext, k8sApiEndpoint), devicesPerUser, deviceEnrollTimeout)
			Expect(err).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)
			GinkgoWriter.Printf("Device to test decommission: %s\n", deviceName)

			By("Logging in as viewer and attempting to decommission the device")
			err = login.LoginAsUser(harness, viewerUser, viewerPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			out, decommErr := harness.DecommissionDevice(deviceName)
			Expect(decommErr).To(HaveOccurred(), "Viewer should not be able to decommission a device")
			Expect(out).To(ContainSubstring("Forbidden"), "Expected Forbidden for viewer decommission attempt")
		})

		It("operator cannot decommission a device", Label("88364"), func() {
			By("Creating devices via simulator as admin")
			deviceName, cmd, err := harness.EnrollDeviceForDecommissionTest(adminLoginFunc(defaultK8sContext, k8sApiEndpoint), devicesPerUser, deviceEnrollTimeout)
			Expect(err).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)
			GinkgoWriter.Printf("Device to test decommission: %s\n", deviceName)

			By("Logging in as operator and attempting to decommission the device")
			err = login.LoginAsUser(harness, operatorUser, operatorPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			out, decommErr := harness.DecommissionDevice(deviceName)
			Expect(decommErr).To(HaveOccurred(), "Operator should not be able to decommission a device")
			Expect(out).To(ContainSubstring("Forbidden"), "Expected Forbidden for operator decommission attempt")
		})

		It("admin can decommission a device", Label("88365"), func() {
			By("Creating devices via simulator as admin")
			deviceName, cmd, err := harness.EnrollDeviceForDecommissionTest(adminLoginFunc(defaultK8sContext, k8sApiEndpoint), devicesPerUser, deviceEnrollTimeout)
			Expect(err).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)
			GinkgoWriter.Printf("Device to decommission: %s\n", deviceName)

			By("Admin decommissioning the device")
			_, decommErr := harness.DecommissionDevice(deviceName)
			Expect(decommErr).ToNot(HaveOccurred(), "Admin should be able to decommission a device")
		})
	})
})

type userCred struct {
	name     string
	password string
}

// loginAndCollectOrgIDs logs in as each user, retrieves their organization ID,
// and returns a map of username -> orgID. Assertions are performed inline.
func loginAndCollectOrgIDs(harness *e2e.Harness, k8sContext, k8sApiEndpoint string, users []userCred) map[string]string {
	orgIDs := make(map[string]string, len(users))
	for _, u := range users {
		By(fmt.Sprintf("Logging in as %s and getting organization", u.name))
		orgID, err := login.LoginAndGetOrgID(harness, u.name, u.password, k8sContext, k8sApiEndpoint)
		Expect(err).ToNot(HaveOccurred())
		Expect(orgID).ToNot(BeEmpty())
		GinkgoWriter.Printf("%s org ID: %s\n", u.name, orgID)
		orgIDs[u.name] = orgID
	}
	return orgIDs
}
