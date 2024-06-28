package agent_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const TIMEOUT = "1m"
const POLLING = "250ms"

func TestAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent E2E Suite")
}

var _ = Describe("VM Agent behavior", func() {
	var (
		harness *e2e.Harness
	)

	BeforeEach(func() {
		harness = e2e.NewTestHarness()
		err := harness.VM.RunAndWaitForSSH()
		if err != nil {
			fmt.Println("============ Console output ============")
			fmt.Println(harness.VM.GetConsoleOutput())
			fmt.Println("========================================")
		}
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			fmt.Printf("oops... %s failed, collecting agent output\n", CurrentSpecReport().FullText())

			stdout, _ := harness.VM.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
			fmt.Print("\n\n\n")
			fmt.Println("============ systemctl status flightctl-agent ============")
			fmt.Println(stdout.String())
			fmt.Println("=============== journalctl -u flightctl-agent ============")
			stdout, _ = harness.VM.RunSSH([]string{"sudo", "journalctl", "-u", "flightctl-agent"}, nil)
			fmt.Println(stdout.String())
			fmt.Println("======================= VM Console =======================")
			fmt.Println(harness.VM.GetConsoleOutput())
			fmt.Println("==========================================================")
			fmt.Print("\n\n\n")
		}
		harness.Cleanup()
	})

	Context("vm", func() {

		It("should print QR output to console", func() {
			// Wait for the top-most part of the QR output to appear
			Eventually(harness.VM.GetConsoleOutput, TIMEOUT, POLLING).Should(ContainSubstring("████████████████████████████████"))

			fmt.Println("============ Console output ============")
			lines := strings.Split(harness.VM.GetConsoleOutput(), "\n")
			fmt.Println(strings.Join(lines[len(lines)-20:], "\n"))
			fmt.Println("========================================")
		})

		It("should have flightctl-agent running", func() {
			stdout, err := harness.VM.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("Active: active (running)"))
		})

		It("should be reporting device status on enrollment request", func() {
			// Get the enrollment Request ID from the console output
			enrollmentID := harness.GetEnrollmentIDFromConsole()
			logrus.Infof("Enrollment ID found in VM console output: %s", enrollmentID)

			// Wait for the device to create the enrollment request, and check the TPM details
			enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
			Expect(enrollmentRequest.Spec).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus.SystemInfo.IsEmpty()).NotTo(BeTrue())

			// Approve the enrollment and wait for the device details to be populated by the agent
			harness.ApproveEnrollment(enrollmentID, testutil.TestEnrollmentApproval())
			logrus.Infof("Waiting for device %s to report status so we can check TPM PCRs again", enrollmentID)

			// wait for the device to pickup enrollment and report measurements on device status
			Eventually(getDeviceWithStatusSystem, TIMEOUT, POLLING).WithArguments(
				harness, enrollmentID).ShouldNot(BeNil())
		})
	})

})

// get device from API, and return only devices that have a Status.SystemInfo
func getDeviceWithStatusSystem(harness *e2e.Harness, enrollmentID string) *client.ReadDeviceResponse {
	device, err := harness.Client.ReadDeviceWithResponse(harness.Context, enrollmentID)
	Expect(err).NotTo(HaveOccurred())
	// we keep waiting for a 200 response, with filled in Status.SystemInfo
	if device.JSON200 == nil || device.JSON200.Status == nil || device.JSON200.Status.SystemInfo.IsEmpty() {
		return nil
	}
	return device
}
