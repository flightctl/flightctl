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

const (
	devicesPerUser       = 10
	totalDevices         = 3 * devicesPerUser
	deviceEnrollTimeout  = 120 * time.Second
	deviceEnrollPolling  = 2 * time.Second
	simulatorStopTimeout = 10 * time.Second

	adminUser     = "admin"
	adminPass     = "admin"
	operatorUser  = "operator"
	operatorPass  = "operator"
	viewerUser    = "viewer"
	viewerPass    = "viewer"
	installerUser = "installer"
	installerPass = "installer"

	forbiddenSubstring = "Forbidden"
	http403Substring   = "403"
)

// testUsers lists all user credentials used across the multiorg suite.
var testUsers = []userCred{
	{adminUser, adminPass},
	{operatorUser, operatorPass},
	{viewerUser, viewerPass},
	{installerUser, installerPass},
}

var _ = Describe("Multiorg RBAC E2E Tests", Label("multiorg", "e2e"), func() {
	var (
		harness     *e2e.Harness
		suiteCtx    context.Context
		flightCtlNs string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		suiteCtx = e2e.GetWorkerContext()
		flightCtlNs = os.Getenv("FLIGHTCTL_NS")
	})

	Context("Shared organization verification", func() {
		It("all users should belong to the same flightctl organization", Label("88358"), func() {
			orgIDs, err := collectOrgIDs(harness, testUsers)
			Expect(err).ToNot(HaveOccurred())
			Expect(orgIDs).To(HaveLen(len(testUsers)))

			By("Verifying all users share the same organization")
			Expect(orgIDs[operatorUser]).To(Equal(orgIDs[adminUser]), "Admin and operator should share the same organization")
			Expect(orgIDs[viewerUser]).To(Equal(orgIDs[adminUser]), "Admin and viewer should share the same organization")
			Expect(orgIDs[installerUser]).To(Equal(orgIDs[adminUser]), "Admin and installer should share the same organization")
		})
	})

	Context("Device enrollment in shared organization", func() {
		var simulatorCmds []*exec.Cmd

		BeforeEach(func() {
			simulatorCmds = make([]*exec.Cmd, 0)
		})

		AfterEach(func() {
			By("Switching back to admin before stopping simulators")
			_ = login.Login(harness, adminUser, adminPass)
			harness.StopAllSimulators(simulatorCmds, simulatorStopTimeout)
			simulatorCmds = nil
		})

		It("should enroll 30 devices as admin and all users should see them", Label("88359"), func() {
			ctx := context.Background()
			testID := harness.GetTestIDFromContext()

			By("Logging in as admin to setup simulator config and create devices")
			err := login.Login(harness, adminUser, adminPass)
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

			for _, u := range []userCred{{operatorUser, operatorPass}, {viewerUser, viewerPass}, {installerUser, installerPass}} {
				By(fmt.Sprintf("Switching to %s and verifying all devices are visible", u.name))
				err = login.Login(harness, u.name, u.password)
				Expect(err).ToNot(HaveOccurred())

				count := harness.CountDevicesByLabel(testID)
				Expect(count).To(Equal(totalDevices),
					fmt.Sprintf("%s should see all %d devices in the shared organization", u.name, totalDevices))
				GinkgoWriter.Printf("%s sees %d devices (expected %d)\n", u.name, count, totalDevices)
			}
		})
	})

	Context("RBAC enforcement within shared organization", func() {
		var simulatorCmds []*exec.Cmd

		BeforeEach(func() {
			simulatorCmds = make([]*exec.Cmd, 0)
		})

		AfterEach(func() {
			By("Switching back to admin before cleanup")
			_ = login.Login(harness, adminUser, adminPass)
			harness.StopAllSimulators(simulatorCmds, simulatorStopTimeout)
			simulatorCmds = nil
		})

		It("admin should have full CRUD access to devices, fleets, and repositories", Label("88360"), func() {
			By("Logging in as admin")
			err := login.Login(harness, adminUser, adminPass)
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
			err := login.Login(harness, operatorUser, operatorPass)
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
			err := login.Login(harness, viewerUser, viewerPass)
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
			err = login.Login(harness, adminUser, adminPass)
			Expect(err).ToNot(HaveOccurred())

			_, deviceName, _, deviceCreateErr := harness.CreateResource(util.Device)
			Expect(deviceCreateErr).ToNot(HaveOccurred(), "Admin should be able to create a device for viewer delete test")

			_, fleetName, _, fleetCreateErr := harness.CreateResource(util.Fleet)
			Expect(fleetCreateErr).ToNot(HaveOccurred(), "Admin should be able to create a fleet for viewer delete test")

			By("Switching back to viewer and attempting to delete admin-created resources")
			err = login.Login(harness, viewerUser, viewerPass)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that viewer cannot delete a device")
			out, deleteErr := harness.CleanUpResource(util.Device, deviceName)
			Expect(deleteErr).To(HaveOccurred(), "Viewer should not be able to delete a device")
			Expect(out).To(ContainSubstring(http403Substring), "Expected 403 Forbidden for viewer device delete")

			By("Testing that viewer cannot delete a fleet")
			out, deleteErr = harness.CleanUpResource(util.Fleet, fleetName)
			Expect(deleteErr).To(HaveOccurred(), "Viewer should not be able to delete a fleet")
			Expect(out).To(ContainSubstring(http403Substring), "Expected 403 Forbidden for viewer fleet delete")

			By("Cleaning up: switching to admin to delete test resources")
			err = login.Login(harness, adminUser, adminPass)
			Expect(err).ToNot(HaveOccurred())
			_, _ = harness.CleanUpResource(util.Device, deviceName)
			_, _ = harness.CleanUpResource(util.Fleet, fleetName)
		})

		It("viewer cannot decommission a device", Label("88363"), func() {
			By("Creating devices via simulator as admin")
			loginFn := makeLoginFunc(adminUser, adminPass)
			deviceName, cmd, err := harness.EnrollDeviceForDecommissionTest(loginFn, devicesPerUser, deviceEnrollTimeout)
			Expect(err).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)
			GinkgoWriter.Printf("Device to test decommission: %s\n", deviceName)

			By("Logging in as viewer and attempting to decommission the device")
			err = login.Login(harness, viewerUser, viewerPass)
			Expect(err).ToNot(HaveOccurred())

			out, decommErr := harness.DecommissionDevice(deviceName)
			Expect(decommErr).To(HaveOccurred(), "Viewer should not be able to decommission a device")
			Expect(out).To(ContainSubstring(forbiddenSubstring), "Expected Forbidden for viewer decommission attempt")
		})

		It("operator cannot decommission a device", Label("88364"), func() {
			By("Creating devices via simulator as admin")
			loginFn := makeLoginFunc(adminUser, adminPass)
			deviceName, cmd, err := harness.EnrollDeviceForDecommissionTest(loginFn, devicesPerUser, deviceEnrollTimeout)
			Expect(err).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)
			GinkgoWriter.Printf("Device to test decommission: %s\n", deviceName)

			By("Logging in as operator and attempting to decommission the device")
			err = login.Login(harness, operatorUser, operatorPass)
			Expect(err).ToNot(HaveOccurred())

			out, decommErr := harness.DecommissionDevice(deviceName)
			Expect(decommErr).To(HaveOccurred(), "Operator should not be able to decommission a device")
			Expect(out).To(ContainSubstring(forbiddenSubstring), "Expected Forbidden for operator decommission attempt")
		})

		It("admin can decommission a device", Label("88365"), func() {
			By("Creating devices via simulator as admin")
			loginFn := makeLoginFunc(adminUser, adminPass)
			deviceName, cmd, err := harness.EnrollDeviceForDecommissionTest(loginFn, devicesPerUser, deviceEnrollTimeout)
			Expect(err).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)
			GinkgoWriter.Printf("Device to decommission: %s\n", deviceName)

			By("Admin decommissioning the device")
			out, decommErr := harness.DecommissionDevice(deviceName)
			Expect(decommErr).ToNot(HaveOccurred(), "Admin should be able to decommission a device")
			Expect(out).To(ContainSubstring("200"), "Expected 200 OK for admin decommission")
		})

		It("installer can read enrollment requests but cannot create devices or fleets", Label("88366"), func() {
			By("Logging in as installer")
			err := login.Login(harness, installerUser, installerPass)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that installer can read enrollment requests")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests"}, true)
			Expect(err).ToNot(HaveOccurred())

			installerLabels := &map[string]string{"test": "multiorg-installer"}

			By("Testing that installer cannot create devices")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness,
				[]string{util.Device},
				false, installerLabels, flightCtlNs, []string{e2e.OperationCreate})
			Expect(err).ToNot(HaveOccurred())

			By("Testing that installer cannot create fleets")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness,
				[]string{util.Fleet},
				false, installerLabels, flightCtlNs, []string{e2e.OperationCreate})
			Expect(err).ToNot(HaveOccurred())
		})

		It("installer cannot decommission a device", Label("88367"), func() {
			By("Creating devices via simulator as admin")
			loginFn := makeLoginFunc(adminUser, adminPass)
			deviceName, cmd, err := harness.EnrollDeviceForDecommissionTest(loginFn, devicesPerUser, deviceEnrollTimeout)
			Expect(err).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)
			GinkgoWriter.Printf("Device to test decommission: %s\n", deviceName)

			By("Logging in as installer and attempting to decommission the device")
			err = login.Login(harness, installerUser, installerPass)
			Expect(err).ToNot(HaveOccurred())

			out, decommErr := harness.DecommissionDevice(deviceName)
			Expect(decommErr).To(HaveOccurred(), "Installer should not be able to decommission a device")
			Expect(out).To(ContainSubstring(forbiddenSubstring), "Expected Forbidden for installer decommission attempt")
		})
	})
})

// --- Types ---

// userCred holds a username and password pair for test user login.
type userCred struct {
	name     string
	password string
}

// --- Helper functions ---

// makeLoginFunc returns a LoginFunc closure for the given user credentials.
func makeLoginFunc(user, password string) e2e.LoginFunc {
	return func(h *e2e.Harness) error {
		return login.Login(h, user, password)
	}
}

// collectOrgIDs logs in as each user and returns a map of username -> orgID.
func collectOrgIDs(harness *e2e.Harness, users []userCred) (map[string]string, error) {
	orgIDs := make(map[string]string, len(users))
	for _, u := range users {
		orgID, err := loginAndGetOrgID(harness, u.name, u.password)
		if err != nil {
			return nil, fmt.Errorf("login/org for %s: %w", u.name, err)
		}
		if orgID == "" {
			return nil, fmt.Errorf("empty org ID for user %s", u.name)
		}
		GinkgoWriter.Printf("%s org ID: %s\n", u.name, orgID)
		orgIDs[u.name] = orgID
	}
	return orgIDs, nil
}

// loginAndGetOrgID logs in as the specified user and returns their organization ID.
func loginAndGetOrgID(harness *e2e.Harness, user, password string) (string, error) {
	if err := login.Login(harness, user, password); err != nil {
		return "", fmt.Errorf("login failed for %s: %w", user, err)
	}
	orgID, err := harness.GetOrganizationID()
	if err != nil {
		return "", fmt.Errorf("getting org for %s: %w", user, err)
	}
	return orgID, nil
}
