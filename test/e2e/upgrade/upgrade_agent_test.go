package agent_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
		harness      *e2e.Harness
		enrollmentID string
		testCounter  = uint32(0)
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
		// save logs
		counter := atomic.AddUint32(&testCounter, 1)
		os.Mkdir("logs", 0755)

		// agent
		stdout, err := harness.VM.RunSSH([]string{"sudo", "journalctl", "-u", "flightctl-agent.service"}, nil)
		Expect(err).ToNot(HaveOccurred())
		filename := filepath.Join("logs", fmt.Sprintf("test-%d-flightctl-agent.log", counter))
		err = os.WriteFile(filename, stdout.Bytes(), 0644)
		Expect(err).ToNot(HaveOccurred())

		// server
		stdout, err = harness.VM.RunSSH([]string{"sudo", "journalctl", "-u", "flightctl-server.service"}, nil)
		Expect(err).ToNot(HaveOccurred())
		filename = filepath.Join("logs", fmt.Sprintf("test-%d-flightctl-agent.log", counter))
		err = os.WriteFile(filename, stdout.Bytes(), 0644)
		Expect(err).ToNot(HaveOccurred())

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

		It("should be reporting tpm info on enrollment request as well as device status", func() {
			// Get the enrollment Request ID from the console output
			enrollmentID = harness.GetEnrollmentIDFromConsole()
			logrus.Infof("Enrollment ID found in VM console output: %s", enrollmentID)

			// Wait for the device to create the enrollment request, and check the TPM details
			enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
			Expect(enrollmentRequest.Spec).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus.SystemInfo).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus.SystemInfo.Measurements).ToNot(BeNil())
			verifyPCRRegistersNotEmpty(enrollmentRequest.Spec.DeviceStatus.SystemInfo.Measurements)

			// Approve the enrollment and wait for the device details to be populated by the agent
			harness.ApproveEnrollment(enrollmentID, testutil.TestEnrollmentApproval())
			logrus.Infof("Waiting for device %s to report status so we can check TPM PCRs again", enrollmentID)

			// wait for the device to pickup enrollment and report measurements on device status
			Eventually(getDeviceWithStatusSystemInfo, TIMEOUT, POLLING).WithArguments(
				harness, enrollmentID).ShouldNot(BeNil())

			device := getDeviceWithStatusSystemInfo(harness, enrollmentID)

			// make sure that the PCR registers aren't empty
			verifyPCRRegistersNotEmpty(device.JSON200.Status.SystemInfo.Measurements)

			// verify that the measurements are the same as the ones we saw in the enrollment request
			Expect(device.JSON200.Status.SystemInfo.Measurements).To(Equal(enrollmentRequest.Spec.DeviceStatus.SystemInfo.Measurements))
		})

		It("should be able to upgrade from latest localhost/local-flightctl-agent:latest", func() {
			fmt.Printf("Upgrading device %s to localhost/local-flightctl-agent:latest\n", enrollmentID)
			// Eventually(harness.VM.GetConsoleOutput, "10m", POLLING).Should(ContainSubstring("rebooting into new image"))
			stdout, err := harness.VM.RunSSH([]string{"cat", "/proc/sys/kernel/random/boot_id"}, nil)
			Expect(err).ToNot(HaveOccurred())
			orgBootID := strings.TrimSpace(stdout.String())
			// upgrade the device spec should rollout the new template and reboot the device.
			harness.UpdateOsImageTo(enrollmentID, "localhost:5000/local-flightctl-agent:latest")
			// wait for the device to reboot
			Eventually(getBootId, "10m", "5s").WithArguments(harness).ShouldNot(Equal(orgBootID))
		})
	})

})

// PCR registers are initialized to a string of 00's at boot. Later on as
// measurements are taken during boot steps, the PCR registers are updated inside the
// TPM with: update_pcr(n, new_measurement){ pcr[n] = SHAx(pcr[n] || new_measurement) }
// This means that the PCR registers should not be empty or all 0's after boot.
// More details about the specific registers can be found here:
// https://uapi-group.org/specifications/specs/linux_tpm_pcr_registry/
func verifyPCRRegistersNotEmpty(measurements map[string]string) {
	Expect(measurements).ToNot(BeNil())
	for i := 1; i < 10; i++ {
		pcrReg := fmt.Sprintf("pcr%02d", i)
		pcr := measurements[pcrReg]
		Expect(pcr).ToNot(BeNil())
		// the length of the mesaurement will be different depending on the TPM
		// sha algorithm so we just check that it's not empty or all 0's, we remove all
		// 0's to make sure that the measurement isn't just a bunch of 0's
		pcrRemove0x := strings.ReplaceAll(pcr, "0", "")
		Expect(pcrRemove0x).ToNot(BeEmpty(), "PCR %s is empty or all 0's, is the VM booting in EFI secure boot?", pcrReg)
	}
}

// get device from API, and return only devices that have a Status.SystemInfo
func getDeviceWithStatusSystemInfo(harness *e2e.Harness, enrollmentID string) *client.ReadDeviceResponse {
	device, err := harness.Client.ReadDeviceWithResponse(harness.Context, enrollmentID)
	Expect(err).NotTo(HaveOccurred())
	// we keep waiting for a 200 response, with filled in Status.SystemInfo
	if device.JSON200 == nil || device.JSON200.Status == nil ||
		device.JSON200.Status.SystemInfo == nil {
		return nil
	}
	return device
}

func getBootId(harness *e2e.Harness) string {
	stdout, err := harness.VM.RunSSH([]string{"cat", "/proc/sys/kernel/random/boot_id"}, nil)
	Expect(err).ToNot(HaveOccurred())
	bootID := strings.TrimSpace(stdout.String())
	fmt.Printf("Current boot ID: %s\n", bootID)
	return bootID
}
