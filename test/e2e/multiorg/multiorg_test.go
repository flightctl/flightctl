package multiorg_test

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/test/e2e/infra"
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

	// OCP-oriented defaults; on quadlet, testUserCreds() provides prefixed names.
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

var _ = Describe("Multiorg RBAC E2E Tests", Label("multiorg", "e2e"), func() {
	var (
		harness     *e2e.Harness
		flightCtlNs string
		users       testUserSet
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		flightCtlNs = os.Getenv("FLIGHTCTL_NS")
		users = getTestUsers()
	})

	Context("Shared organization verification", func() {
		It("all users should belong to the same flightctl organization", Label("88358"), func() {
			if infra.IsQuadletEnvironment() {
				Skip("Org verification not applicable on quadlet: cluster-admin defaults to system org")
			}
			orgIDs, err := collectOrgIDs(harness, users.all)
			Expect(err).ToNot(HaveOccurred())
			Expect(orgIDs).To(HaveLen(len(users.all)))

			By("Verifying all users share the same organization")
			Expect(orgIDs[users.operator.name]).To(Equal(orgIDs[users.admin.name]), "Admin and operator should share the same organization")
			Expect(orgIDs[users.viewer.name]).To(Equal(orgIDs[users.admin.name]), "Admin and viewer should share the same organization")
			Expect(orgIDs[users.installer.name]).To(Equal(orgIDs[users.admin.name]), "Admin and installer should share the same organization")
		})
	})

	Context("Device enrollment in shared organization", func() {
		var simulatorCmds []*exec.Cmd

		BeforeEach(func() {
			simulatorCmds = make([]*exec.Cmd, 0)
		})

		AfterEach(func() {
			By("Switching back to admin before stopping simulators")
			_ = loginAndSetOrg(harness, users.admin.name, users.admin.password)
			harness.StopAllSimulators(simulatorCmds, simulatorStopTimeout)
			simulatorCmds = nil
		})

		It("should enroll 30 devices as admin and all users except installer should see them", Label("88359"), func() {
			testID := harness.GetTestIDFromContext()
			deferOrgSimulatorConfig(harness, users)

			By("Logging in as admin to setup simulator config and create devices")
			err := loginAndSetOrg(harness, users.admin.name, users.admin.password)
			Expect(err).ToNot(HaveOccurred())

			setupSharedOrgSimulatorConfig(harness)

			By(fmt.Sprintf("Starting 3 device simulators (%d devices each) as admin", devicesPerUser))
			for i := 0; i < 3; i++ {
				initialIndex := i * devicesPerUser
				// Use default auto-approval for bulk enrollment.
				cmd, simErr := harness.StartLabeledSimulator(harness.Context, testID, users.admin.name, initialIndex, devicesPerUser)
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

			for _, u := range []userCred{users.operator, users.viewer} {
				By(fmt.Sprintf("Switching to %s and verifying all devices are visible", u.name))
				err = loginAndSetOrg(harness, u.name, u.password)
				Expect(err).ToNot(HaveOccurred())

				count := harness.CountDevicesByLabel(testID)
				Expect(count).To(Equal(totalDevices),
					fmt.Sprintf("%s should see all %d devices in the shared organization", u.name, totalDevices))
				GinkgoWriter.Printf("%s sees %d devices (expected %d)\n", u.name, count, totalDevices)
			}

			By("Switching to installer and verifying device list is denied")
			err = loginAndSetOrg(harness, users.installer.name, users.installer.password)
			Expect(err).ToNot(HaveOccurred())

			expectReadOnlyListForbidden(harness, util.Device)
		})
	})

	Context("RBAC enforcement within shared organization", func() {
		var simulatorCmds []*exec.Cmd

		BeforeEach(func() {
			simulatorCmds = make([]*exec.Cmd, 0)
		})

		AfterEach(func() {
			By("Switching back to admin before cleanup")
			_ = loginAndSetOrg(harness, users.admin.name, users.admin.password)
			harness.StopAllSimulators(simulatorCmds, simulatorStopTimeout)
			simulatorCmds = nil
		})

		It("admin should have full CRUD access to devices, fleets, and repositories", Label("88360"), func() {
			suiteCtx := e2e.GetWorkerContext()

			By("Logging in as admin")
			err := loginAndSetOrg(harness, users.admin.name, users.admin.password)
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

		It("operator should have full CRUD access to devices, fleets, and repositories and limited access to enrollment requests", Label("88361"), func() {
			suiteCtx := e2e.GetWorkerContext()
			testID := harness.GetTestIDFromContext()
			var erName string
			deferOrgSimulatorConfig(harness, users)

			By("Logging in as operator")
			err := loginAndSetOrg(harness, users.operator.name, users.operator.password)
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

			By("Testing that operator can perform all CRUD operations on repositories")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness,
				[]string{util.Repository},
				true, operatorLabels, flightCtlNs, []string{e2e.OperationAll})
			Expect(err).ToNot(HaveOccurred())

			By("Creating an enrollment request as admin so operator can read it")
			err = loginAndSetOrg(harness, users.admin.name, users.admin.password)
			Expect(err).ToNot(HaveOccurred())

			setupSharedOrgSimulatorConfig(harness)

			// Keep the enrollment request pending for RBAC approval checks.
			erSimCmd, simErr := harness.StartLabeledSimulatorWithOptions(harness.Context, testID, "operator-er-rbac", 0, 1, e2e.StartLabeledSimulatorOptions{SkipAutoApprove: true})
			Expect(simErr).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, erSimCmd)

			Eventually(func() error {
				var waitErr error
				erName, waitErr = harness.WaitForSimulatorEnrollmentRequest(
					testID, e2e.DeviceSimulatorAgentAlias(0), deviceEnrollTimeout, deviceEnrollPolling)
				return waitErr
			}, deviceEnrollTimeout, deviceEnrollPolling).Should(Succeed(),
				"Expected an enrollment request from the simulator")

			err = loginAndSetOrg(harness, users.operator.name, users.operator.password)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operator can list events")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"events"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operator can list enrollment requests")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operator can get a specific enrollment request")
			_, getErr := harness.GetResourcesByName("enrollmentrequests", erName)
			Expect(getErr).ToNot(HaveOccurred(), "Operator should be able to get an enrollment request")

			By("Testing that operator cannot approve an enrollment request")
			status, approveErr := harness.ApproveEnrollmentRequestWithLabels(erName, nil)
			Expect(approveErr).To(HaveOccurred(), "Operator should not be able to approve an enrollment request")
			Expect(status).To(Equal(http.StatusForbidden), "Expected 403 Forbidden for operator enrollment approval")
		})

		It("viewer should only be able to read devices and fleets, not create or delete", Label("88362"), func() {
			suiteCtx := e2e.GetWorkerContext()
			testID := harness.GetTestIDFromContext()
			var erName string
			deferOrgSimulatorConfig(harness, users)

			By("Logging in as viewer")
			err := loginAndSetOrg(harness, users.viewer.name, users.viewer.password)
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

			By("Creating resources as admin for viewer RBAC tests")
			err = loginAndSetOrg(harness, users.admin.name, users.admin.password)
			Expect(err).ToNot(HaveOccurred())

			_, deviceName, deviceData, deviceCreateErr := harness.CreateResource(util.Device)
			Expect(deviceCreateErr).ToNot(HaveOccurred(), "Admin should be able to create a device for viewer RBAC test")
			DeferCleanup(func() {
				_ = loginAndSetOrg(harness, users.admin.name, users.admin.password)
				_, _ = harness.CleanUpResource(util.Device, deviceName)
			})

			_, fleetName, fleetData, fleetCreateErr := harness.CreateResource(util.Fleet)
			Expect(fleetCreateErr).ToNot(HaveOccurred(), "Admin should be able to create a fleet for viewer RBAC test")
			DeferCleanup(func() {
				_ = loginAndSetOrg(harness, users.admin.name, users.admin.password)
				_, _ = harness.CleanUpResource(util.Fleet, fleetName)
			})

			_, repoName, _, repoCreateErr := harness.CreateResource(util.Repository)
			Expect(repoCreateErr).ToNot(HaveOccurred(), "Admin should be able to create a repository for viewer RBAC test")
			DeferCleanup(func() {
				_ = loginAndSetOrg(harness, users.admin.name, users.admin.password)
				_, _ = harness.CleanUpResource(util.Repository, repoName)
			})

			setupSharedOrgSimulatorConfig(harness)

			// Keep the enrollment request pending for RBAC approval checks.
			erSimCmd, simErr := harness.StartLabeledSimulatorWithOptions(harness.Context, testID, "viewer-er-rbac", 0, 1, e2e.StartLabeledSimulatorOptions{SkipAutoApprove: true})
			Expect(simErr).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, erSimCmd)

			Eventually(func() error {
				var waitErr error
				erName, waitErr = harness.WaitForSimulatorEnrollmentRequest(
					testID, e2e.DeviceSimulatorAgentAlias(0), deviceEnrollTimeout, deviceEnrollPolling)
				return waitErr
			}, deviceEnrollTimeout, deviceEnrollPolling).Should(Succeed(),
				"Expected an enrollment request from the simulator")

			By("Switching back to viewer for update, delete, and read tests")
			err = loginAndSetOrg(harness, users.viewer.name, users.viewer.password)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that viewer cannot update a device")
			updatedDeviceYAML, updateErr := harness.AddLabelsToYAML(string(deviceData), *viewerLabels)
			Expect(updateErr).ToNot(HaveOccurred())
			out, applyErr := harness.CLIWithStdin(updatedDeviceYAML, "apply", "-f", "-")
			Expect(applyErr).To(HaveOccurred(), "Viewer should not be able to update a device")
			Expect(out).To(ContainSubstring(http403Substring), "Expected 403 Forbidden for viewer device update")

			By("Testing that viewer cannot update a fleet")
			updatedFleetYAML, updateErr := harness.AddLabelsToYAML(string(fleetData), *viewerLabels)
			Expect(updateErr).ToNot(HaveOccurred())
			out, applyErr = harness.CLIWithStdin(updatedFleetYAML, "apply", "-f", "-")
			Expect(applyErr).To(HaveOccurred(), "Viewer should not be able to update a fleet")
			Expect(out).To(ContainSubstring(http403Substring), "Expected 403 Forbidden for viewer fleet update")

			By("Testing that viewer cannot delete a device")
			out, deleteErr := harness.CleanUpResource(util.Device, deviceName)
			Expect(deleteErr).To(HaveOccurred(), "Viewer should not be able to delete a device")
			Expect(out).To(ContainSubstring(http403Substring), "Expected 403 Forbidden for viewer device delete")

			By("Testing that viewer cannot delete a fleet")
			out, deleteErr = harness.CleanUpResource(util.Fleet, fleetName)
			Expect(deleteErr).To(HaveOccurred(), "Viewer should not be able to delete a fleet")
			Expect(out).To(ContainSubstring(http403Substring), "Expected 403 Forbidden for viewer fleet delete")

			By("Testing that viewer cannot create repositories")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness,
				[]string{util.Repository},
				false, viewerLabels, flightCtlNs, []string{e2e.OperationCreate})
			Expect(err).ToNot(HaveOccurred())

			By("Testing that viewer can list and get repositories")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"repositories"}, true)
			Expect(err).ToNot(HaveOccurred())
			_, getErr := harness.GetResourcesByName("repositories", repoName)
			Expect(getErr).ToNot(HaveOccurred(), "Viewer should be able to get a repository")

			By("Testing that viewer can list and get enrollment requests")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests"}, true)
			Expect(err).ToNot(HaveOccurred())
			_, getErr = harness.GetResourcesByName("enrollmentrequests", erName)
			Expect(getErr).ToNot(HaveOccurred(), "Viewer should be able to get an enrollment request")

			By("Testing that viewer can list events")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"events"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that viewer cannot approve an enrollment request")
			status, approveErr := harness.ApproveEnrollmentRequestWithLabels(erName, nil)
			Expect(approveErr).To(HaveOccurred(), "Viewer should not be able to approve an enrollment request")
			Expect(status).To(Equal(http.StatusForbidden), "Expected 403 Forbidden for viewer enrollment approval")
		})

		It("should enforce device console access by role", Label("89607", "agent"), func() {
			testID := harness.GetTestIDFromContext()
			deferOrgSimulatorConfig(harness, users)

			By("Creating a device via simulator as admin")
			err := loginAndSetOrg(harness, users.admin.name, users.admin.password)
			Expect(err).ToNot(HaveOccurred())

			setupSharedOrgSimulatorConfig(harness)

			// Use default auto-approval for console RBAC device checks.
			simCmd, simErr := harness.StartLabeledSimulator(harness.Context, testID, "console-rbac", 0, 1)
			Expect(simErr).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, simCmd)

			deviceName, err := harness.WaitForLabeledSimulatorDevice(testID, 0, deviceEnrollTimeout, deviceEnrollPolling)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Device for console RBAC test: %s\n", deviceName)

			for _, tc := range []struct {
				role    string
				user    userCred
				allowed bool
			}{
				{"admin", users.admin, true},
				{"operator", users.operator, true},
				{"viewer", users.viewer, false},
				{"installer", users.installer, false},
			} {
				By(fmt.Sprintf("Testing console access as %s", tc.role))
				err = loginAndSetOrg(harness, tc.user.name, tc.user.password)
				Expect(err).ToNot(HaveOccurred())
				assertConsoleAccess(harness, deviceName, tc.allowed)
			}
		})

		DescribeTable("decommission access control",
			func(user, password string, shouldSucceed bool) {
				deferOrgSimulatorConfig(harness, users)

				By("Creating devices via simulator as admin")
				err := loginAndSetOrg(harness, users.admin.name, users.admin.password)
				Expect(err).ToNot(HaveOccurred())
				setupSharedOrgSimulatorConfig(harness)

				loginFn := makeLoginFunc(users.admin.name, users.admin.password)
				deviceName, cmd, err := harness.EnrollDeviceForDecommissionTest(loginFn, 1, deviceEnrollTimeout)
				Expect(err).ToNot(HaveOccurred())
				simulatorCmds = append(simulatorCmds, cmd)
				deferDecommissionTestCleanup(harness, users, deviceName)
				GinkgoWriter.Printf("Device for decommission test: %s\n", deviceName)

				By(fmt.Sprintf("Logging in as %s and attempting to decommission", user))
				err = loginAndSetOrg(harness, user, password)
				Expect(err).ToNot(HaveOccurred())

				out, decommErr := harness.DecommissionDevice(deviceName)
				if shouldSucceed {
					Expect(decommErr).ToNot(HaveOccurred(), fmt.Sprintf("%s should be able to decommission a device", user))
					Expect(out).To(ContainSubstring("200"), "Expected 200 OK for decommission")
				} else {
					Expect(decommErr).To(HaveOccurred(), fmt.Sprintf("%s should not be able to decommission a device", user))
					Expect(out).To(ContainSubstring(forbiddenSubstring), "Expected Forbidden for decommission attempt")
				}
			},
			Entry("admin can decommission a device", Label("88365"), adminUserCred().name, adminUserCred().password, true),
			Entry("operator cannot decommission a device", Label("88364"), operatorUserCred().name, operatorUserCred().password, false),
			Entry("viewer cannot decommission a device", Label("88363"), viewerUserCred().name, viewerUserCred().password, false),
			Entry("installer cannot decommission a device", Label("88367"), installerUserCred().name, installerUserCred().password, false),
		)

		It("installer should read enrollment requests and be denied device, fleet, repository, and events access", Label("88366"), func() {
			suiteCtx := e2e.GetWorkerContext()
			testID := harness.GetTestIDFromContext()
			var erName string
			deferOrgSimulatorConfig(harness, users)

			By("Logging in as installer")
			err := loginAndSetOrg(harness, users.installer.name, users.installer.password)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that installer can read enrollment requests")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that installer cannot list fleets")
			expectReadOnlyListForbidden(harness, util.Fleet)

			By("Testing that installer cannot list repositories")
			expectReadOnlyListForbidden(harness, "repositories")

			By("Testing that installer cannot list events")
			expectReadOnlyListForbidden(harness, "events")

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

			By("Creating devices and fleets as admin so installer can attempt to update them")
			err = loginAndSetOrg(harness, users.admin.name, users.admin.password)
			Expect(err).ToNot(HaveOccurred())

			_, deviceName, deviceData, deviceCreateErr := harness.CreateResource(util.Device)
			Expect(deviceCreateErr).ToNot(HaveOccurred(), "Admin should be able to create a device for installer RBAC test")
			DeferCleanup(func() {
				_ = loginAndSetOrg(harness, users.admin.name, users.admin.password)
				_, _ = harness.CleanUpResource(util.Device, deviceName)
			})

			_, fleetName, fleetData, fleetCreateErr := harness.CreateResource(util.Fleet)
			Expect(fleetCreateErr).ToNot(HaveOccurred(), "Admin should be able to create a fleet for installer RBAC test")
			DeferCleanup(func() {
				_ = loginAndSetOrg(harness, users.admin.name, users.admin.password)
				_, _ = harness.CleanUpResource(util.Fleet, fleetName)
			})

			setupSharedOrgSimulatorConfig(harness)

			// Keep the enrollment request pending for RBAC approval checks.
			erSimCmd, simErr := harness.StartLabeledSimulatorWithOptions(harness.Context, testID, "installer-er-rbac", 0, 1, e2e.StartLabeledSimulatorOptions{SkipAutoApprove: true})
			Expect(simErr).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, erSimCmd)

			Eventually(func() error {
				var waitErr error
				erName, waitErr = harness.WaitForSimulatorEnrollmentRequest(
					testID, e2e.DeviceSimulatorAgentAlias(0), deviceEnrollTimeout, deviceEnrollPolling)
				return waitErr
			}, deviceEnrollTimeout, deviceEnrollPolling).Should(Succeed(),
				"Expected an enrollment request from the simulator")

			By("Switching back to installer for update and approve tests")
			err = loginAndSetOrg(harness, users.installer.name, users.installer.password)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that installer cannot update a device")
			updatedDeviceYAML, updateErr := harness.AddLabelsToYAML(string(deviceData), *installerLabels)
			Expect(updateErr).ToNot(HaveOccurred())
			out, applyErr := harness.CLIWithStdin(updatedDeviceYAML, "apply", "-f", "-")
			Expect(applyErr).To(HaveOccurred(), "Installer should not be able to update a device")
			Expect(out).To(ContainSubstring(http403Substring), "Expected 403 Forbidden for installer device update")

			By("Testing that installer cannot update a fleet")
			updatedFleetYAML, updateErr := harness.AddLabelsToYAML(string(fleetData), *installerLabels)
			Expect(updateErr).ToNot(HaveOccurred())
			out, applyErr = harness.CLIWithStdin(updatedFleetYAML, "apply", "-f", "-")
			Expect(applyErr).To(HaveOccurred(), "Installer should not be able to update a fleet")
			Expect(out).To(ContainSubstring(http403Substring), "Expected 403 Forbidden for installer fleet update")

			By("Testing that installer can approve an enrollment request")
			status, approveErr := harness.ApproveEnrollmentRequestWithLabels(erName, nil)
			Expect(approveErr).ToNot(HaveOccurred(), "Installer should be able to approve an enrollment request")
			Expect(status).To(Equal(http.StatusOK), "Expected 200 OK for installer enrollment approval")
		})
	})
})

// --- Types ---

type userCred struct {
	name     string
	password string
}

type testUserSet struct {
	admin     userCred
	operator  userCred
	viewer    userCred
	installer userCred
	all       []userCred
}

// --- Helper functions ---

// getTestUsers returns the full set of test user credentials for the current environment.
func getTestUsers() testUserSet {
	creds := testUserCreds()
	return testUserSet{
		admin:     creds[0],
		operator:  creds[1],
		viewer:    creds[2],
		installer: creds[3],
		all:       creds,
	}
}

// Individual credential accessors for use in DescribeTable Entry parameters,
// which are evaluated at parse time (before BeforeEach).
func adminUserCred() userCred     { return testUserCreds()[0] }
func operatorUserCred() userCred  { return testUserCreds()[1] }
func viewerUserCred() userCred    { return testUserCreds()[2] }
func installerUserCred() userCred { return testUserCreds()[3] }

// expectReadOnlyListForbidden asserts listing resource types is denied with HTTP 403.
func expectReadOnlyListForbidden(harness *e2e.Harness, resourceTypes ...string) {
	for _, resourceType := range resourceTypes {
		out, err := harness.GetResourcesByName(resourceType)
		Expect(err).To(HaveOccurred(), "listing %s should fail for this role", resourceType)
		Expect(out).To(Or(
			ContainSubstring(http403Substring),
			ContainSubstring(forbiddenSubstring),
		), "Expected RBAC denial (403/Forbidden) when listing %s", resourceType)
	}
}

// deferDecommissionTestCleanup deletes the decommission test device and enrollment request
// by name. Decommission clears device labels, so label-based AfterEach cleanup can miss them.
func deferDecommissionTestCleanup(harness *e2e.Harness, users testUserSet, deviceName string) {
	GinkgoHelper()
	DeferCleanup(func() {
		if deviceName == "" {
			return
		}
		_ = loginAndSetOrg(harness, users.admin.name, users.admin.password)
		_, _ = harness.CleanUpResource(util.EnrollmentRequest, deviceName)
		_ = harness.DeleteDeviceIgnoreNotFound(deviceName)
	})
}

// setupSharedOrgSimulatorConfig regenerates ~/.flightctl/agent.yaml for the shared multiorg org.
func setupSharedOrgSimulatorConfig(harness *e2e.Harness) {
	GinkgoHelper()
	_, err := harness.SetupDeviceSimulatorAgentConfig(0, 0)
	Expect(err).ToNot(HaveOccurred(), "Failed to setup simulator agent config for shared org")
}

// deferOrgSimulatorConfig registers cleanup that resets CLI org to the system
// default and regenerates ~/.flightctl/agent.yaml after tests that change simulator config.
func deferOrgSimulatorConfig(harness *e2e.Harness, users testUserSet) {
	GinkgoHelper()
	DeferCleanup(func() {
		if err := revertDefaultOrgSimulatorConfig(harness, users); err != nil {
			GinkgoWriter.Printf("Warning: failed to restore default org and device simulator config: %v\n", err)
		}
	})
}

func revertDefaultOrgSimulatorConfig(harness *e2e.Harness, users testUserSet) error {
	if infra.DetectEnvironment() == infra.EnvironmentOCP {
		kubeadminPass := os.Getenv("KUBEADMIN_PASS")
		if kubeadminPass == "" {
			return fmt.Errorf("KUBEADMIN_PASS not set")
		}
		if err := login.Login(harness, "kubeadmin", kubeadminPass); err != nil {
			return fmt.Errorf("login kubeadmin: %w", err)
		}
	} else if err := loginAndSetOrg(harness, users.admin.name, users.admin.password); err != nil {
		return fmt.Errorf("login admin for revert: %w", err)
	}

	if err := harness.SetCurrentOrganization(org.DefaultID.String()); err != nil {
		return fmt.Errorf("set default organization: %w", err)
	}
	if _, err := harness.SetupDeviceSimulatorAgentConfig(0, 0); err != nil {
		return fmt.Errorf("regenerate device simulator agent config: %w", err)
	}
	GinkgoWriter.Printf("Restored default organization %s and device simulator config\n", org.DefaultID)
	return nil
}

func makeLoginFunc(user, password string) e2e.LoginFunc {
	return func(h *e2e.Harness) error {
		return loginAndSetOrg(h, user, password)
	}
}

// loginAndSetOrg logs in and ensures the user operates in the shared test org.
// On quadlet, cluster-admin users default to the system org, so we explicitly
// switch to the shared org. On OCP this is a no-op.
func loginAndSetOrg(harness *e2e.Harness, user, password string) error {
	if err := login.Login(harness, user, password); err != nil {
		return err
	}
	if infra.IsQuadletEnvironment() && quadletSharedOrgID != "" {
		return harness.SetCurrentOrganization(quadletSharedOrgID)
	}
	return nil
}

func collectOrgIDs(harness *e2e.Harness, users []userCred) (map[string]string, error) {
	if harness == nil {
		return nil, fmt.Errorf("harness is nil")
	}
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

func loginAndGetOrgID(harness *e2e.Harness, user, password string) (string, error) {
	if err := loginAndSetOrg(harness, user, password); err != nil {
		return "", fmt.Errorf("login failed for %s: %w", user, err)
	}
	orgID, err := harness.GetOrganizationID()
	if err != nil {
		return "", fmt.Errorf("getting org for %s: %w", user, err)
	}
	return orgID, nil
}

// assertConsoleAccess verifies devices/console RBAC via flightctl console.
func assertConsoleAccess(harness *e2e.Harness, deviceName string, allowed bool) {
	GinkgoHelper()
	const consoleMarker = "multiorg-console-ok"
	out, err := harness.RunConsoleCommand(deviceName, []string{"--notty"}, "echo", consoleMarker)
	if allowed {
		Expect(err).ToNot(HaveOccurred(), "Console command should succeed for authorized role")
		Expect(out).To(ContainSubstring(consoleMarker), "Console should execute remote command successfully")
		return
	}
	Expect(err).To(HaveOccurred(), "Console should fail for unauthorized role")
	Expect(out).To(Or(
		ContainSubstring(http403Substring),
		ContainSubstring(forbiddenSubstring),
	), "Expected 403 Forbidden for console access")
}
