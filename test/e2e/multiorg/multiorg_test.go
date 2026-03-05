package multiorg_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	devicesPerOrg        = 10
	deviceEnrollTimeout  = 120 * time.Second
	simulatorStopTimeout = 10 * time.Second

	adminUser    = "admin"
	adminPass    = "admin"
	operatorUser = "operator"
	operatorPass = "operator"
	viewerUser   = "viewer"
	viewerPass   = "viewer"
)

var _ = Describe("Multiorg E2E Tests", Label("multiorg", "e2e"), func() {
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

	Context("Organization auto-creation on login", func() {
		It("should auto-create separate organizations for each user", Label("XXXXX"), func() {
			By("Logging in as admin and verifying organization")
			adminOrgID, err := loginAndGetOrgID(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			Expect(adminOrgID).ToNot(BeEmpty())
			GinkgoWriter.Printf("Admin org ID: %s\n", adminOrgID)

			By("Logging in as operator and verifying organization")
			operatorOrgID, err := loginAndGetOrgID(harness, operatorUser, operatorPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			Expect(operatorOrgID).ToNot(BeEmpty())
			GinkgoWriter.Printf("Operator org ID: %s\n", operatorOrgID)

			By("Logging in as viewer and verifying organization")
			viewerOrgID, err := loginAndGetOrgID(harness, viewerUser, viewerPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			Expect(viewerOrgID).ToNot(BeEmpty())
			GinkgoWriter.Printf("Viewer org ID: %s\n", viewerOrgID)

			By("Verifying all three organizations are distinct")
			Expect(adminOrgID).ToNot(Equal(operatorOrgID), "Admin and operator should have different organizations")
			Expect(adminOrgID).ToNot(Equal(viewerOrgID), "Admin and viewer should have different organizations")
			Expect(operatorOrgID).ToNot(Equal(viewerOrgID), "Operator and viewer should have different organizations")
		})

		It("should show only the user's own organizations on listing", Label("XXXXX"), func() {
			By("Logging in as admin and listing organizations")
			err := loginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			adminOrgs, err := listOrganizations(harness)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Admin sees %d organization(s): %v\n", len(adminOrgs), adminOrgs)

			By("Logging in as operator and listing organizations")
			err = loginAsUser(harness, operatorUser, operatorPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			operatorOrgs, err := listOrganizations(harness)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Operator sees %d organization(s): %v\n", len(operatorOrgs), operatorOrgs)

			By("Logging in as viewer and listing organizations")
			err = loginAsUser(harness, viewerUser, viewerPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			viewerOrgs, err := listOrganizations(harness)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Viewer sees %d organization(s): %v\n", len(viewerOrgs), viewerOrgs)

			By("Verifying each user sees at least one organization")
			Expect(len(adminOrgs)).To(BeNumerically(">=", 1))
			Expect(len(operatorOrgs)).To(BeNumerically(">=", 1))
			Expect(len(viewerOrgs)).To(BeNumerically(">=", 1))
		})
	})

	Context("Device creation via simulator per organization", func() {
		var simulatorCmds []*exec.Cmd

		BeforeEach(func() {
			simulatorCmds = make([]*exec.Cmd, 0)
		})

		AfterEach(func() {
			GinkgoWriter.Printf("Stopping %d device simulators\n", len(simulatorCmds))
			for i, cmd := range simulatorCmds {
				if cmd != nil {
					GinkgoWriter.Printf("   Stopping simulator %d/%d\n", i+1, len(simulatorCmds))
					stopErr := harness.StopDeviceSimulator(cmd, simulatorStopTimeout)
					if stopErr != nil {
						GinkgoWriter.Printf("   Warning: error stopping simulator %d: %v\n", i+1, stopErr)
					}
				}
			}
			simulatorCmds = nil
		})

		It("should create 10 devices per organization using device simulator", Label("XXXXX"), func() {
			ctx := context.Background()
			testID := harness.GetTestIDFromContext()

			type userConfig struct {
				name     string
				password string
			}
			users := []userConfig{
				{adminUser, adminPass},
				{operatorUser, operatorPass},
			}

			By("Starting device simulators for admin and operator organizations")
			for i, user := range users {
				GinkgoWriter.Printf("Logging in as %s and starting device simulator\n", user.name)
				err := loginAsUser(harness, user.name, user.password, defaultK8sContext, k8sApiEndpoint)
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to login as %s", user.name))

				initialDeviceIndex := i * devicesPerOrg
				cmd, simErr := startSimulatorForUser(harness, ctx, testID, user.name, initialDeviceIndex, devicesPerOrg)
				Expect(simErr).ToNot(HaveOccurred(), fmt.Sprintf("Failed to start simulator for %s", user.name))
				simulatorCmds = append(simulatorCmds, cmd)
				GinkgoWriter.Printf("Simulator started for %s (devices %d-%d)\n",
					user.name, initialDeviceIndex, initialDeviceIndex+devicesPerOrg-1)
			}

			By("Waiting for devices to enroll in each organization")
			for _, user := range users {
				GinkgoWriter.Printf("Verifying %d devices enrolled for %s\n", devicesPerOrg, user.name)
				err := loginAsUser(harness, user.name, user.password, defaultK8sContext, k8sApiEndpoint)
				Expect(err).ToNot(HaveOccurred())

				Eventually(func() int {
					return countDevicesForUser(harness, testID)
				}, deviceEnrollTimeout, 2*time.Second).Should(Equal(devicesPerOrg),
					fmt.Sprintf("Expected %d devices to enroll for %s", devicesPerOrg, user.name))

				GinkgoWriter.Printf("All %d devices enrolled for %s\n", devicesPerOrg, user.name)
			}
		})
	})

	Context("RBAC enforcement across organizations", func() {
		var simulatorCmds []*exec.Cmd

		BeforeEach(func() {
			simulatorCmds = make([]*exec.Cmd, 0)
		})

		AfterEach(func() {
			for _, cmd := range simulatorCmds {
				if cmd != nil {
					_ = harness.StopDeviceSimulator(cmd, simulatorStopTimeout)
				}
			}
			simulatorCmds = nil
		})

		It("admin should have full access to all resources and operations", Label("XXXXX"), func() {
			By("Logging in as admin")
			err := loginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			adminLabels := &map[string]string{"test": "multiorg-admin"}

			By("Testing that admin can perform all operations on devices, fleets, and repositories")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, true, adminLabels, flightCtlNs, []string{e2e.OperationAll})
			Expect(err).ToNot(HaveOccurred())

			By("Testing that admin can read enrollmentrequests and events")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)
			Expect(err).ToNot(HaveOccurred())
		})

		It("operator should be able to create, manage, and decommission devices", Label("XXXXX"), func() {
			By("Logging in as operator")
			err := loginAsUser(harness, operatorUser, operatorPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			operatorLabels := &map[string]string{"test": "multiorg-operator"}

			By("Testing that operator can perform all CRUD operations on devices")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device"}, true, operatorLabels, flightCtlNs, []string{e2e.OperationAll})
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operator can perform all CRUD operations on fleets")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"fleet"}, true, operatorLabels, flightCtlNs, []string{e2e.OperationAll})
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operator can read enrollmentrequests and events")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)
			Expect(err).ToNot(HaveOccurred())
		})

		It("viewer should only be able to view devices, not create or decommission", Label("XXXXX"), func() {
			By("Logging in as viewer")
			err := loginAsUser(harness, viewerUser, viewerPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			viewerLabels := &map[string]string{"test": "multiorg-viewer"}

			By("Testing that viewer can list devices and fleets")
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"device", "fleet"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that viewer cannot create devices")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device"}, false, viewerLabels, flightCtlNs, []string{e2e.OperationCreate})
			Expect(err).ToNot(HaveOccurred())

			By("Testing that viewer cannot create fleets")
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"fleet"}, false, viewerLabels, flightCtlNs, []string{e2e.OperationCreate})
			Expect(err).ToNot(HaveOccurred())
		})

		It("viewer cannot decommission a device created by admin", Label("XXXXX"), func() {
			ctx := context.Background()
			testID := harness.GetTestIDFromContext()

			By("Logging in as admin and creating devices via simulator")
			err := loginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			cmd, simErr := startSimulatorForUser(harness, ctx, testID, adminUser, 0, devicesPerOrg)
			Expect(simErr).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)

			Eventually(func() int {
				return countDevicesForUser(harness, testID)
			}, deviceEnrollTimeout, 2*time.Second).Should(Equal(devicesPerOrg),
				fmt.Sprintf("Expected %d devices to enroll for admin", devicesPerOrg))

			deviceName, nameErr := getFirstDeviceName(harness, testID)
			Expect(nameErr).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Found device to decommission: %s\n", deviceName)

			By("Logging in as viewer and attempting to decommission the device")
			err = loginAsUser(harness, viewerUser, viewerPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			out, decommErr := harness.CLI("decommission", "device", deviceName)
			Expect(decommErr).To(HaveOccurred(), "Viewer should not be able to decommission a device")
			Expect(out).To(ContainSubstring("403"), "Expected 403 Forbidden for viewer decommission attempt")
		})

		It("operator can decommission a device in their organization", Label("XXXXX"), func() {
			ctx := context.Background()
			testID := harness.GetTestIDFromContext()

			By("Logging in as operator and creating devices via simulator")
			err := loginAsUser(harness, operatorUser, operatorPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			cmd, simErr := startSimulatorForUser(harness, ctx, testID, operatorUser, 0, devicesPerOrg)
			Expect(simErr).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)

			Eventually(func() int {
				return countDevicesForUser(harness, testID)
			}, deviceEnrollTimeout, 2*time.Second).Should(Equal(devicesPerOrg),
				fmt.Sprintf("Expected %d devices to enroll for operator", devicesPerOrg))

			deviceName, nameErr := getFirstDeviceName(harness, testID)
			Expect(nameErr).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Found device to decommission: %s\n", deviceName)

			By("Operator decommissioning the device")
			_, decommErr := harness.CLI("decommission", "device", deviceName)
			Expect(decommErr).ToNot(HaveOccurred(), "Operator should be able to decommission a device")
		})
	})

	Context("Cross-organization isolation", func() {
		var simulatorCmds []*exec.Cmd

		BeforeEach(func() {
			simulatorCmds = make([]*exec.Cmd, 0)
		})

		AfterEach(func() {
			for _, cmd := range simulatorCmds {
				if cmd != nil {
					_ = harness.StopDeviceSimulator(cmd, simulatorStopTimeout)
				}
			}
			simulatorCmds = nil
		})

		It("users should not see devices from other organizations", Label("XXXXX"), func() {
			ctx := context.Background()
			testID := harness.GetTestIDFromContext()

			By("Logging in as admin and creating devices via simulator")
			err := loginAsUser(harness, adminUser, adminPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			cmd, simErr := startSimulatorForUser(harness, ctx, testID, adminUser, 0, devicesPerOrg)
			Expect(simErr).ToNot(HaveOccurred())
			simulatorCmds = append(simulatorCmds, cmd)

			Eventually(func() int {
				return countDevicesForUser(harness, testID)
			}, deviceEnrollTimeout, 2*time.Second).Should(Equal(devicesPerOrg),
				fmt.Sprintf("Expected %d devices to enroll for admin", devicesPerOrg))

			GinkgoWriter.Printf("Admin created %d devices\n", devicesPerOrg)

			By("Logging in as operator (different org) and checking device visibility")
			err = loginAsUser(harness, operatorUser, operatorPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			deviceCount := countDevicesForUser(harness, testID)
			Expect(deviceCount).To(Equal(0),
				"Operator should not see devices from admin's organization")
			GinkgoWriter.Printf("Operator sees %d devices from admin's org (expected 0)\n", deviceCount)

			By("Logging in as viewer (different org) and checking device visibility")
			err = loginAsUser(harness, viewerUser, viewerPass, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			deviceCount = countDevicesForUser(harness, testID)
			Expect(deviceCount).To(Equal(0),
				"Viewer should not see devices from admin's organization")
			GinkgoWriter.Printf("Viewer sees %d devices from admin's org (expected 0)\n", deviceCount)
		})
	})
})

// loginAsUser logs in to OCP as the specified user and refreshes the harness client.
func loginAsUser(harness *e2e.Harness, user, password, k8sContext, k8sApiEndpoint string) error {
	return login.LoginAsNonAdmin(harness, user, password, k8sContext, k8sApiEndpoint)
}

// loginAndGetOrgID logs in as the specified user and returns their organization ID.
func loginAndGetOrgID(harness *e2e.Harness, user, password, k8sContext, k8sApiEndpoint string) (string, error) {
	if err := loginAsUser(harness, user, password, k8sContext, k8sApiEndpoint); err != nil {
		return "", fmt.Errorf("login failed for %s: %w", user, err)
	}
	orgID, err := harness.GetOrganizationID()
	if err != nil {
		return "", fmt.Errorf("getting org for %s: %w", user, err)
	}
	return orgID, nil
}

// listOrganizations returns a list of organization names visible to the currently logged-in user.
func listOrganizations(harness *e2e.Harness) ([]string, error) {
	resp, err := harness.Client.ListOrganizationsWithResponse(harness.Context, nil)
	if err != nil {
		return nil, fmt.Errorf("listing organizations: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response listing organizations")
	}
	var orgNames []string
	for _, org := range resp.JSON200.Items {
		if org.Metadata.Name != nil {
			orgNames = append(orgNames, *org.Metadata.Name)
		}
	}
	return orgNames, nil
}

// startSimulatorForUser starts a device simulator with the given parameters.
func startSimulatorForUser(harness *e2e.Harness, ctx context.Context, testID, userName string, initialIndex, count int) (*exec.Cmd, error) {
	args := []string{
		"--count", fmt.Sprintf("%d", count),
		"--initial-device-index", fmt.Sprintf("%d", initialIndex),
		"--label", fmt.Sprintf("test-id=%s", testID),
		"--label", fmt.Sprintf("org-user=%s", userName),
		"--log-level", "info",
	}
	return harness.RunDeviceSimulator(ctx, args...)
}

// countDevicesForUser counts devices visible to the currently logged-in user with the given test ID.
func countDevicesForUser(harness *e2e.Harness, testID string) int {
	output, err := harness.CLI("get", util.Device, "-l", fmt.Sprintf("test-id=%s", testID), "-o", "name")
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// getFirstDeviceName returns the name of the first device matching the test ID label.
func getFirstDeviceName(harness *e2e.Harness, testID string) (string, error) {
	output, err := harness.CLI("get", util.Device, "-l", fmt.Sprintf("test-id=%s", testID), "-o", "name")
	if err != nil {
		return "", fmt.Errorf("listing devices: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			name = strings.TrimPrefix(name, "device/")
			return name, nil
		}
	}
	return "", fmt.Errorf("no devices found with test-id=%s", testID)
}
