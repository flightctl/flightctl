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
			out, err := harness.CLI("get", "organizations")
			logrus.Info(out)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("pinkcorp"))
		})
		It("Should set organization", func() {
			harness := e2e.GetWorkerHarness()
			orgName := getOrgNameByDisplayName("pinkcorp")
			logrus.Info("orgName: ", orgName)
			out, err := harness.CLI("config", "set-organization", orgName)
			logrus.Info(out)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(orgName))
		})
		It("Should get current organization", func() {
			harness := e2e.GetWorkerHarness()
			orgName := getOrgNameByDisplayName("pinkcorp")
			out, err := harness.CLI("config", "set-organization", orgName)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(orgName))
			logrus.Info("orgName: ", orgName)
			out1, err1 := harness.CLI("config", "current-organization")
			logrus.Info(out1)
			Expect(err1).ToNot(HaveOccurred())
			Expect(out1).To(ContainSubstring(orgName))
		})
		It("Should enroll device in the current organization", func() {
			harness := e2e.GetWorkerHarness()
			workerID := GinkgoParallelProcess()
			err := harness.SetupVMFromPoolAndStartAgent(workerID)
			Expect(err).ToNot(HaveOccurred())
			orgName := getOrgNameByDisplayName("pinkcorp")
			out, err := harness.CLI("config", "set-organization", orgName)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring(orgName))
			logrus.Info("orgName: ", orgName)
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			defer func() {

				_, err := harness.ManageResource("delete", fmt.Sprintf("device/%s", deviceId))
				Expect(err).NotTo(HaveOccurred())
				_, err = harness.ManageResource("delete", fmt.Sprintf("er/%s", deviceId))
				Expect(err).NotTo(HaveOccurred())
			}()
			harness.WaitForDeviceContents(deviceId, "The device has completed enrollment and is now online",
				func(device *v1alpha1.Device) bool {
					return device.Status.Summary.Status == v1alpha1.DeviceSummaryStatusOnline
				}, e2e.TIMEOUT)
			out1, err1 := harness.CLI("get", "devices")
			Expect(err1).ToNot(HaveOccurred())
			Expect(out1).To(ContainSubstring(deviceId))
			logrus.Info("device: ", out1)
			orgName1 := getOrgNameByDisplayName("orangecorp")
			out2, err2 := harness.CLI("config", "set-organization", orgName1)
			Expect(err2).ToNot(HaveOccurred())
			Expect(out2).To(ContainSubstring(orgName1))
			logrus.Info("orgName1: ", orgName1)
			out3, err3 := harness.CLI("config", "current-organization")
			Expect(err3).ToNot(HaveOccurred())
			Expect(out3).To(ContainSubstring(orgName1))
			logrus.Info("orgName1: ", orgName1)
			out4, err4 := harness.CLI("get", "devices")
			Expect(err4).ToNot(HaveOccurred())
			Expect(out4).To(Not(ContainSubstring(deviceId)))
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

func getOrgNameByDisplayName(displayName string) string {
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
