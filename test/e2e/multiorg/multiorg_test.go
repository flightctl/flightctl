package multiorg_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

// _ is a blank identifier used to ignore values or expressions, often applied to satisfy interface or assignment requirements.
var _ = Describe("multiorg operation", func() {
	BeforeEach(func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()
		authMethod := login.WithPassword(harness)
		Expect(authMethod).To(Equal(login.AuthUsernamePassword))
	})
	Context("multiorg operation", func() {
		It("Should list organizations", func() {
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
		It("Should set organization", func() {
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
		It("Should enroll device in the current organization", func() {
			harness := e2e.GetWorkerHarness()
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

		It("Should create 2 devices in the current organization", func() {
			harness := e2e.GetWorkerHarness()
			orgName := GetOrgNameByDisplayName("pinkcorp")
			out, err := harness.CLI("config", "set-organization", orgName)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(orgName))

			_, err = harness.SetupDeviceSimulatorAgentConfig(harness.RegistryEndpoint(), "info", 2*time.Second, 2*time.Second)
			Expect(err).ToNot(HaveOccurred())

			cmd, err := harness.RunDeviceSimulator(context.Background(), "--count", "2", "--log-level", "info", "--label", "organization="+orgName)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				out1, err1 := harness.CLI("get", "devices")
				g.Expect(err1).ToNot(HaveOccurred())
				g.Expect(out1).To(ContainSubstring("device-00000"))
				g.Expect(out1).To(ContainSubstring("device-00001"))
			}, 60*time.Second, 2*time.Second).Should(Succeed())

			DeferCleanup(func() {
				_ = harness.StopDeviceSimulator(cmd, 5*time.Second)
				_, err = DeleteAllDevicesFound()
				if err != nil {
					logrus.Error("error deleting devices: ", err)
				}
			})
		})
		It("Should create 10 devices in every organization", func() {
			harness := e2e.GetWorkerHarness()
			orgNames := GetOrgDisplayNames()
			for _, orgName := range orgNames {
				logrus.Info("setting organization: ", orgName)
				orgName1 := GetOrgNameByDisplayName(orgName)
				out, err := harness.CLI("config", "set-organization", orgName1)
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(orgName1))

				_, err = harness.SetupDeviceSimulatorAgentConfig(harness.RegistryEndpoint(), "info", 2*time.Second, 2*time.Second)
				Expect(err).ToNot(HaveOccurred())

				cmd, err := harness.RunDeviceSimulator(context.Background(), "--count", "10", "--log-level", "debug", "--label", "organization="+orgName1)
				Expect(err).ToNot(HaveOccurred())

				Eventually(func(g Gomega) {
					out, err := harness.CLI("get", "devices")
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(out).To(ContainSubstring("device-00000"))
					g.Expect(out).To(ContainSubstring("device-00001"))
					g.Expect(out).To(ContainSubstring("device-00002"))
					g.Expect(out).To(ContainSubstring("device-00003"))
					g.Expect(out).To(ContainSubstring("device-00004"))
					g.Expect(out).To(ContainSubstring("device-00005"))
					g.Expect(out).To(ContainSubstring("device-00006"))
					g.Expect(out).To(ContainSubstring("device-00007"))
					g.Expect(out).To(ContainSubstring("device-00008"))
					g.Expect(out).To(ContainSubstring("device-00009"))
				}, 60*time.Second, 2*time.Second).Should(Succeed())

				_ = harness.StopDeviceSimulator(cmd, 5*time.Second)
				_, err = DeleteAllDevicesFound()
				if err != nil {
					logrus.Error("error deleting devices: ", err)
				}
				logrus.Info("devices created in organization: ", orgName)
				logrus.Info("devices: ", out)
			}
		})

		It("Should create 2 devices in the current organization", func() {
			harness := e2e.GetWorkerHarness()
			orgName := getOrgNameByDisplayName("pinkcorp")
			out, err := harness.CLI("config", "set-organization", orgName)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(orgName))

			_, err = harness.SetupDeviceSimulatorAgentConfig(harness.RegistryEndpoint(), "info", 2*time.Second, 2*time.Second)
			Expect(err).ToNot(HaveOccurred())

			cmd, err := harness.RunDeviceSimulator(context.Background(), "--count", "2")
			Expect(err).ToNot(HaveOccurred())

			DeferCleanup(func() {
				_ = harness.StopDeviceSimulator(cmd, 5*time.Second)
			})

			Eventually(func(g Gomega) {
				out1, err1 := harness.CLI("get", "devices")
				g.Expect(err1).ToNot(HaveOccurred())
				g.Expect(out1).To(ContainSubstring("device-00000"))
				g.Expect(out1).To(ContainSubstring("device-00001"))
			}, 30*time.Second, 2*time.Second).Should(Succeed())
		})
		It("Should create 10 devices in every organization", func() {
			harness := e2e.GetWorkerHarness()
			orgNames := []string{"pinkcorp", "orangecorp"}
			for _, orgName := range orgNames {
				logrus.Info("setting organization: ", orgName)
				orgName1 := getOrgNameByDisplayName(orgName)
				out, err := harness.CLI("config", "set-organization", orgName1)
				Expect(err).ToNot(HaveOccurred())
				Expect(out).To(ContainSubstring(orgName1))

				_, err = harness.SetupDeviceSimulatorAgentConfig(harness.RegistryEndpoint(), "info", 2*time.Second, 2*time.Second)
				Expect(err).ToNot(HaveOccurred())

				cmd, err := harness.RunDeviceSimulator(context.Background(), "--count", "10", "--log-level", "info", "--label", "organization="+orgName1)
				Expect(err).ToNot(HaveOccurred())

				Eventually(func(g Gomega) {
					out, err := harness.CLI("get", "devices")
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(out).To(ContainSubstring("device-00000"))
					g.Expect(out).To(ContainSubstring("device-00001"))
					g.Expect(out).To(ContainSubstring("device-00002"))
					g.Expect(out).To(ContainSubstring("device-00003"))
					g.Expect(out).To(ContainSubstring("device-00004"))
					g.Expect(out).To(ContainSubstring("device-00005"))
					g.Expect(out).To(ContainSubstring("device-00006"))
					g.Expect(out).To(ContainSubstring("device-00007"))
					g.Expect(out).To(ContainSubstring("device-00008"))
					g.Expect(out).To(ContainSubstring("device-00009"))
				}, 30*time.Second, 2*time.Second).Should(Succeed())
				DeferCleanup(func() {
					_ = harness.StopDeviceSimulator(cmd, 5*time.Second)
				})
				logrus.Info("devices created in organization: ", orgName)
				logrus.Info("devices: ", out)
			}
		})
	})
})

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
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
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

// GenerateFleetYAMLsForSimulator returns a multi-document Fleet YAML string with
// fleetCount Fleet objects, each selecting devices labeled with its fleet name,
// and annotates each Fleet with the desired devices-per-fleet count for clarity.
// It validates inputs are positive and returns an error otherwise.
func GenerateFleetYAMLsForSimulator(fleetCount, devicesPerFleet int) (string, error) {
	if fleetCount <= 0 {
		return "", fmt.Errorf("fleetCount must be > 0")
	}
	if devicesPerFleet <= 0 {
		return "", fmt.Errorf("devicesPerFleet must be > 0")
	}

	var b strings.Builder
	for i := 0; i < fleetCount; i++ {
		if i > 0 {
			b.WriteString("\n---\n")
		}
		fleetName := fmt.Sprintf("sim-fleet-%02d", i)
		// Minimal valid Fleet spec mirroring example, with selector/labels and an annotation to record devices-per-fleet
		b.WriteString("apiVersion: flightctl.io/v1alpha1\n")
		b.WriteString("kind: Fleet\n")
		b.WriteString("metadata:\n")
		b.WriteString(fmt.Sprintf("  name: %s\n", fleetName))
		b.WriteString("  annotations:\n")
		b.WriteString(fmt.Sprintf("    devices-per-fleet: \"%d\"\n", devicesPerFleet))
		b.WriteString("spec:\n")
		b.WriteString("  selector:\n")
		b.WriteString("    matchLabels:\n")
		b.WriteString(fmt.Sprintf("      fleet: %s\n", fleetName))
		b.WriteString("  template:\n")
		b.WriteString("    metadata:\n")
		b.WriteString("      labels:\n")
		b.WriteString(fmt.Sprintf("        fleet: %s\n", fleetName))
		b.WriteString("    spec:\n")
		b.WriteString("      os:\n")
		b.WriteString("        image: quay.io/redhat/rhde:9.2\n")
		b.WriteString("      config:\n")
		b.WriteString("        - name: base\n")
		b.WriteString("          gitRef:\n")
		b.WriteString("            repository: flightctl-demos\n")
		b.WriteString("            targetRevision: main\n")
		b.WriteString("            path: /demos/basic-nginx-demo/configuration/\n")
	}
	return b.String(), nil
}

// ValidateFleetYAMLDevicesPerFleet performs a basic consistency check on the generated YAML,
// ensuring it contains the expected number of Fleet documents and the devices-per-fleet annotation.
func ValidateFleetYAMLDevicesPerFleet(yamlContent string, expectedDevicesPerFleet int, expectedFleetCount int) error {
	if yamlContent == "" {
		return fmt.Errorf("yaml content is empty")
	}
	// Split K8s multi-document YAMLs (leading doc counts as one)
	docs := 1
	for _, line := range strings.Split(yamlContent, "\n") {
		if strings.TrimSpace(line) == "---" {
			docs++
		}
	}
	if docs != expectedFleetCount {
		return fmt.Errorf("expected %d fleet docs, found %d", expectedFleetCount, docs)
	}
	expectedAnno := fmt.Sprintf("devices-per-fleet: \"%d\"", expectedDevicesPerFleet)
	if !strings.Contains(yamlContent, expectedAnno) {
		return fmt.Errorf("expected annotation %q not found", expectedAnno)
	}
	return nil
}

// DeleteAllDevicesFound lists all devices and deletes each one. It returns the
// device IDs that were deleted, in the order processed.
func DeleteAllDevicesFound() ([]string, error) {
	harness := e2e.GetWorkerHarness()
	out, err := harness.CLI("get", "devices", "-o", "name")
	if err != nil {
		return nil, err
	}
	var deleted []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		id = strings.TrimPrefix(id, "device/")
		if _, err := harness.ManageResource("delete", "device/"+id); err != nil {
			return deleted, err
		}
		deleted = append(deleted, id)
	}
	return deleted, nil
}
