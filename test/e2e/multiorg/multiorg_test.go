package multiorg_test

import (
	"fmt"
	"strings"

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
