package multiorg_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("multiorg operation", Ordered, func() {
	BeforeAll(func() {
		harness := e2e.GetWorkerHarness()

		By("Verifying authorization is enabled")
		authMethod := login.WithPassword(harness)
		if authMethod == login.AuthDisabled {
			Skip("Authorization is disabled; skipping multiorg tests")
		}

		By("Verifying organizations support is enabled")
		resp, err := harness.Client.AuthConfigWithResponse(harness.Context)
		Expect(err).ToNot(HaveOccurred(), "failed to query auth config")
		if resp.JSON200 == nil || !resp.JSON200.AuthOrganizationsConfig.Enabled {
			Skip("Organizations are not enabled on this deployment; skipping multiorg tests")
		}
	})

	BeforeEach(func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()
		authMethod := login.WithPassword(harness)
		Expect(authMethod).To(Equal(login.AuthUsernamePassword))
	})

	Context("multiorg operation", func() {
		It("Should list organizations", Label("85918", "sanity"), func() {
			harness := e2e.GetWorkerHarness()
			orgDisplayNames := GetOrgDisplayNames()
			for _, orgDisplayName := range orgDisplayNames {
				orgName := GetOrgNameByDisplayName(orgDisplayName)
				out, err := harness.CLI("get", "organizations")
				logrus.Info(out)
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(orgDisplayName))
				Expect(out).To(ContainSubstring(orgName))
			}
		})

		It("Should set organization", Label("85916", "sanity"), func() {
			harness := e2e.GetWorkerHarness()
			orgDisplayNames := GetOrgDisplayNames()
			for _, orgDisplayName := range orgDisplayNames {
				orgName := GetOrgNameByDisplayName(orgDisplayName)
				out, err := harness.CLI("config", "set-organization", orgName)
				logrus.Info(out)
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(orgDisplayName))
				Expect(out).To(ContainSubstring(orgName))
			}
		})

		It("Should enroll device in the current organization", Label("85914", "sanity"), func() {
			harness := e2e.GetWorkerHarness()
			// Setup VM from pool, revert to pristine snapshot, and start agent
			workerID := GinkgoParallelProcess()
			err := harness.SetupVMFromPoolAndStartAgent(workerID)
			Expect(err).ToNot(HaveOccurred())
			orgDisplayName := GetOrgDisplayNames()[0]
			logrus.Info("Organization Name: ", orgDisplayName)
			orgName := GetOrgNameByDisplayName(orgDisplayName)
			out, err := harness.CLI("config", "set-organization", orgName)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(orgDisplayName))
			Expect(out).To(ContainSubstring(orgName))
			logrus.Info("organization name: ", orgDisplayName)
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			harness.WaitForDeviceContents(deviceId, "The device has completed enrollment and is now online",
				func(device *v1alpha1.Device) bool {
					return device.Status.Summary.Status == v1alpha1.DeviceSummaryStatusOnline
				}, e2e.TIMEOUT)
			out1, err1 := harness.CLI("get", "devices")
			Expect(err1).ToNot(HaveOccurred())
			Expect(out1).To(ContainSubstring(deviceId))
			logrus.Info("device: ", out1)
			orgDisplayName1 := GetOrgDisplayNames()[1]
			orgName1 := GetOrgNameByDisplayName(orgDisplayName1)
			out2, err2 := harness.CLI("config", "set-organization", orgName1)
			Expect(err2).ToNot(HaveOccurred())
			Expect(out2).To(ContainSubstring(orgDisplayName1))
			Expect(out2).To(ContainSubstring(orgName1))
			logrus.Info("organization name: ", orgName1)
			out3, err3 := harness.CLI("config", "current-organization")
			Expect(err3).ToNot(HaveOccurred())
			Expect(out3).To(ContainSubstring(orgName1))
			logrus.Info("organization name: ", orgName1)
			out4, err4 := harness.CLI("get", "devices")
			Expect(err4).ToNot(HaveOccurred())
			Expect(out4).To(Not(ContainSubstring(deviceId)))
			logrus.Info("device: ", out4)
		})

<<<<<<< HEAD
<<<<<<< HEAD
		It("Should create 2 devices in the current organization", Label("85913", "integration"), func() {
=======
		It("Should create 2 devices in the current organization", Label("85913", "sanity"), func() {
>>>>>>> 80c428bc (EDM-1931: Multiorg E2E test suite)
=======
		It("Should create 2 devices in the current organization", Label("85913", "sanity"), func() {
>>>>>>> eb936b78 (EDM-1931: Multiorg E2E test suite)
			harness := e2e.GetWorkerHarness()
			orgNames := GetOrgDisplayNames()
			orgName := orgNames[0]
			orgName1 := GetOrgNameByDisplayName(orgName)
			out, err := harness.CLI("config", "set-organization", orgName1)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(orgName1))
			_, err = harness.SetupDeviceSimulatorAgentConfig(harness.RegistryEndpoint(), "info", 2*time.Second, 2*time.Second)
			Expect(err).ToNot(HaveOccurred())
			cmd, err := harness.RunDeviceSimulator(context.Background(), "--count", "2", "--log-level", "info", "--label", "organization="+orgName1)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func(g Gomega) {
				out, err := harness.CLI("get", "devices")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(out).To(ContainSubstring("device-00000"))
				g.Expect(out).To(ContainSubstring("device-00001"))
				_ = harness.StopDeviceSimulator(cmd, 5*time.Second)
			}, 60*time.Second, 2*time.Second).Should(Succeed())
			logrus.Info("devices created in organization: ", orgName)
			_ = harness.StopDeviceSimulator(cmd, 5*time.Second)
		})
		It("Should create 10 devices in every organization", Label("85912", "sanity"), func() {
			harness := e2e.GetWorkerHarness()
			orgNames := GetOrgDisplayNames()
			for idx, orgName := range orgNames {
				logrus.Info("setting organization: ", orgName)
				orgName1 := GetOrgNameByDisplayName(orgName)
				switchOrganization(harness, orgName1)
				_, err := harness.SetupDeviceSimulatorAgentConfig(harness.RegistryEndpoint(), "info", 2*time.Second, 2*time.Second)
				Expect(err).ToNot(HaveOccurred())

				cmd, err := harness.RunDeviceSimulator(context.Background(), "--count", "10", "--log-level", "info", "--label", "organization="+orgName)
				Expect(err).ToNot(HaveOccurred())

				expectDevicesVisible(harness, 0, 10)

				// Switch to a different organization (next, or previous if at end) and assert
				// that the devices and fleets created in the current org are not visible there.
				if len(orgNames) > 2 {
					var neighborIdx int
					if idx+1 < len(orgNames) {
						neighborIdx = idx + 1
					} else if idx > 0 {
						neighborIdx = idx - 1
					}
					neighborOrgDisplay := orgNames[neighborIdx]
					neighborOrg := GetOrgNameByDisplayName(neighborOrgDisplay)

					switchOrganization(harness, neighborOrg)

					expectDevicesNotVisible(harness, 0, 10)

					outFleetsOther, errFleetsOther := harness.CLI("get", "fleets")
					Expect(errFleetsOther).ToNot(HaveOccurred())
					Expect(outFleetsOther).To(Not(ContainSubstring("sim-fleet-00")))
				}

				_ = harness.StopDeviceSimulator(cmd, 5*time.Second)
				logrus.Info("Device simulator stopped in organization: ", orgName)
				_, err = harness.DeleteAllDevicesFound()
				if err != nil {
					logrus.Error("error deleting devices: ", err)
				}
				logrus.Info("devices created in organization: ", orgName)

			}
		})
		It("generates and applies multi-fleet YAML on current organization", Label("85917", "sanity"), func() {
			harness := e2e.GetWorkerHarness()
			orgNames := GetOrgDisplayNames()
			orgName1 := GetOrgNameByDisplayName(orgNames[0])
			switchOrganization(harness, orgName1)
			yamlDoc, err := harness.GenerateFleetYAMLsForSimulator(2, 10, "sim-fleet")
			logrus.Info("yamlDoc: ", yamlDoc)
			Expect(err).ToNot(HaveOccurred())
			Expect(yamlDoc).To(ContainSubstring("kind: Fleet"))
			Expect(yamlDoc).To(ContainSubstring(`devices-per-fleet: "10"`))
			// Save generated YAML as fleet.yaml in a temp directory
			tmpDir, err := os.MkdirTemp("", "fleetyaml-")
			logrus.Info("temp directory: ", tmpDir)
			Expect(err).ToNot(HaveOccurred())
			fleetYamlPath := filepath.Join(tmpDir, "fleet.yaml")
			err = os.WriteFile(fleetYamlPath, []byte(yamlDoc), 0600)
			Expect(err).ToNot(HaveOccurred())
			_, err = harness.CLI("apply", "-f", fleetYamlPath)
			defer os.RemoveAll(tmpDir)
			Expect(err).ToNot(HaveOccurred())
			_, err = harness.SetupDeviceSimulatorAgentConfig(harness.RegistryEndpoint(), "info", 2*time.Second, 2*time.Second)
			Expect(err).ToNot(HaveOccurred())
			cmd, err := harness.RunDeviceSimulator(context.Background(), "--count", "10", "--log-level", "info", "--initial-device-index", "0", "--label", "fleet=sim-fleet-00")
			Expect(err).ToNot(HaveOccurred())
			expectDevicesVisible(harness, 0, 10)

			cmd1, err1 := harness.RunDeviceSimulator(context.Background(), "--count", "10", "--log-level", "info", "--initial-device-index", "10", "--label", "fleet=sim-fleet-01")
			Expect(err1).ToNot(HaveOccurred())
			expectDevicesVisible(harness, 10, 10)
			out4, err4 := harness.CLI("get", "fleets")
			Expect(err4).ToNot(HaveOccurred())
			Expect(out4).To(ContainSubstring("sim-fleet-00"))
			Expect(out4).To(ContainSubstring("sim-fleet-01"))
			orgName2 := GetOrgDisplayNames()[1]
			logrus.Info("organization name: ", orgName2)
			orgName21 := GetOrgNameByDisplayName(orgName2)
			switchOrganization(harness, orgName21)
			logrus.Info("organization name: ", orgName21)
			switchOrganization(harness, orgName1)
			logrus.Info("organization name: ", orgName1)
			_, _ = harness.CLI("delete", "fleets", "-l", fmt.Sprintf("test-id=%s", harness.GetTestIDFromContext()))
			err = harness.StopDeviceSimulator(cmd, 5*time.Second)
			Expect(err).ToNot(HaveOccurred())
			deleteDevices, errDeleteDevices := harness.DeleteAllDevicesFound()
			Expect(errDeleteDevices).ToNot(HaveOccurred())
			logrus.Info("devices deleted: ", deleteDevices)
			err1 = harness.StopDeviceSimulator(cmd1, 5*time.Second)
			Expect(err1).ToNot(HaveOccurred())
			deleteDevices1, errDeleteDevices1 := harness.DeleteAllDevicesFound()
			Expect(errDeleteDevices1).ToNot(HaveOccurred())
			logrus.Info("devices deleted: ", deleteDevices1)
		})
		It("Should create 10 devices in a fleet in all organizations", Label("85915", "sanity"), func() {
			harness := e2e.GetWorkerHarness()
			orgNames := GetOrgDisplayNames()
			for idx, orgName := range orgNames {
				logrus.Info("setting organization: ", orgName)
				orgName1 := GetOrgNameByDisplayName(orgName)
				switchOrganization(harness, orgName1)
				logrus.Info("current organization: ", orgName1)
				_, err := harness.SetupDeviceSimulatorAgentConfig(harness.RegistryEndpoint(), "info", 2*time.Second, 2*time.Second)
				Expect(err).ToNot(HaveOccurred())
				yamlDoc, err := harness.GenerateFleetYAMLsForSimulator(1, 10, "sim-fleet")
				Expect(err).ToNot(HaveOccurred())
				Expect(yamlDoc).To(ContainSubstring("kind: Fleet"))
				Expect(yamlDoc).To(ContainSubstring(`devices-per-fleet: "10"`))
				Expect(err).ToNot(HaveOccurred())
				_, err = harness.CLIWithStdin(yamlDoc, "apply", "-f", "-")
				Expect(err).ToNot(HaveOccurred())
				cmd, err := harness.RunDeviceSimulator(context.Background(), "--count", "10", "--log-level", "info", "--initial-device-index", "0", "--label", "fleet=sim-fleet-00", "--label", "organization="+orgName1)
				Expect(err).ToNot(HaveOccurred())
				expectDevicesVisible(harness, 0, 10)
				var neighborIdx int
				if len(orgNames) > 2 {
					if idx+1 < len(orgNames) {
						neighborIdx = idx + 1
					} else if idx > 0 {
						neighborIdx = idx - 1
					}
					neighborOrgDisplay := orgNames[neighborIdx]
					neighborOrg := GetOrgNameByDisplayName(neighborOrgDisplay)
					switchOrganization(harness, neighborOrg)
					expectDevicesVisible(harness, 0, 10)
					outFleetsOther, errFleetsOther := harness.CLI("get", "fleets")
					Expect(errFleetsOther).ToNot(HaveOccurred())
					Expect(outFleetsOther).To(Not(ContainSubstring("sim-fleet-00")))
					neighborOrgDisplay = orgNames[neighborIdx]
					neighborOrg = GetOrgNameByDisplayName(neighborOrgDisplay)
					switchOrganization(harness, neighborOrg)
					outFleetsOther, errFleetsOther = harness.CLI("get", "fleets")
					Expect(errFleetsOther).ToNot(HaveOccurred())
					Expect(outFleetsOther).To(ContainSubstring("sim-fleet-00"))
					logrus.Info("fleets: ", outFleetsOther)
				}
				_ = harness.StopDeviceSimulator(cmd, 5*time.Second)
				logrus.Info("Device simulator stopped in organization: ", orgName)
				_, errDelete := harness.DeleteAllDevicesFound()
				if errDelete != nil {
					logrus.Error("error deleting devices: ", errDelete)
				}
				logrus.Info("devices created in organization: ", orgName)
			}
		})
	})

})

// expectDevicesVisible asserts that devices from startIndex to startIndex+numDevices-1
// are present in the flightctl devices list (formatted as device-00000, etc.).
func expectDevicesVisible(harness *e2e.Harness, startIndex int, numDevices int) {
	Eventually(func(g Gomega) {
		out, err := harness.CLI("get", "devices")
		g.Expect(err).ToNot(HaveOccurred())
		for i := startIndex; i < startIndex+numDevices; i++ {
			g.Expect(out).To(ContainSubstring(fmt.Sprintf("device-%05d", i)))
		}
	}, 120*time.Second, 2*time.Second).Should(Succeed())
}

// expectDevicesNotVisible asserts that devices from startIndex to startIndex+numDevices-1
// are not present in the flightctl devices list.
func expectDevicesNotVisible(harness *e2e.Harness, startIndex int, numDevices int) {
	Eventually(func(g Gomega) {
		out, err := harness.CLI("get", "devices")
		g.Expect(err).ToNot(HaveOccurred())
		for i := startIndex; i < startIndex+numDevices; i++ {
			g.Expect(out).To(Not(ContainSubstring(fmt.Sprintf("device-%05d", i))))
		}
	}, 60*time.Second, 2*time.Second).Should(Succeed())
}

// switchOrganization sets the current CLI organization to the provided orgName
// and verifies the context was updated accordingly.
func switchOrganization(h *e2e.Harness, orgName string) string {
	out, err := h.CLI("config", "set-organization", orgName)
	Expect(err).ToNot(HaveOccurred())
	Expect(out).To(ContainSubstring(orgName))
	logrus.Info("switched to organization: ", orgName)

	outCur, errCur := h.CLI("config", "current-organization")
	Expect(errCur).ToNot(HaveOccurred())
	Expect(outCur).To(ContainSubstring(orgName))
	logrus.Info("current organization: ", outCur)
	return out
}

func GetOrgNameByDisplayName(displayName string) string {
	harness := e2e.GetWorkerHarness()
	out, err := harness.CLI("get", "organizations")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return ""
	}
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == displayName {
			return fields[0]
		}
	}
	return ""
}

// getOrgDisplayNames returns the display names of all organizations
func GetOrgDisplayNames() []string {
	harness := e2e.GetWorkerHarness()
	out, err := harness.CLI("get", "organizations")
	if err != nil {
		Fail("no organizations found: failed to get organizations")
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		Fail("no organizations found")
		return []string{}
	}
	var displayNames []string
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			displayNames = append(displayNames, fields[1])
		}
	}
	return displayNames
}
