package decommission_test

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	decommissionSuccessMsg   = "Device scheduled for decommissioning: 200 OK:"
	decommissionCompleteDesc = "The device has completed decommissioning"
)

// DecommissionCLITestParams holds the CLI args and expected error substring for table-driven tests.
type DecommissionCLITestParams struct {
	Args          []string
	ExpectedError string
}

var _ = Describe("CLI decommission test", func() {
	Context("Decommission", func() {
		It("Should decommission a device via CLI", Label("decommission", "81782"), func() {
			harness := e2e.GetWorkerHarness()

			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			defer cleanupDeviceAndER(harness, deviceId)

			GinkgoWriter.Printf("decommission device with id: %s\n", deviceId)

			By("Initiating decommission via CLI")
			out, err := harness.CLI("decommission", "devices/"+deviceId)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring(decommissionSuccessMsg))
			GinkgoWriter.Printf("%s\n", out)

			By("Waiting for decommission to complete")
			harness.WaitForDeviceContents(deviceId, decommissionCompleteDesc,
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, "DeviceDecommissioning", "True", string(v1beta1.DecommissionStateComplete))
				}, TIMEOUT)

			By("Waiting for VM to reboot and SSH to be ready")
			Eventually(func() error {
				return harness.VM.WaitForSSHToBeReady()
			}, REBOOT_TIMEOUT, POLLING).Should(Succeed())

			By("Verifying the agent management certificate no longer exists (requires re-enrollment)")
			certMissing, err := checkCertificateDoesNotExist(harness)
			Expect(err).NotTo(HaveOccurred())
			Expect(certMissing).To(BeTrue(), "Expected certificate file to NOT exist after decommission")

			By("Verifying the spec JSON files do not contain the old device ID")
			staleFiles, err := checkSpecFilesDoNotContainDeviceID(harness, deviceId)
			Expect(err).NotTo(HaveOccurred())
			Expect(staleFiles).To(BeEmpty(), "Expected no spec files to contain old device ID, but found: %v", staleFiles)

			By("Verifying the agent generates a new device ID after reboot")
			newEnrollmentId := harness.GetEnrollmentIDFromServiceLogs(testutil.FLIGHTCTL_AGENT_SERVICE)
			GinkgoWriter.Printf("New enrollment ID after decommission: %s\n", newEnrollmentId)
			Expect(newEnrollmentId).NotTo(BeEmpty(), "Expected agent to generate a new enrollment ID after decommission")
			Expect(newEnrollmentId).NotTo(Equal(deviceId),
				fmt.Sprintf("Expected new device ID to differ from original. Got: %s", newEnrollmentId))

			By("Verifying the device creates a new enrollment request on the management server")
			newEnrollmentRequest := harness.WaitForEnrollmentRequest(newEnrollmentId)
			Expect(newEnrollmentRequest).NotTo(BeNil(), "Expected new enrollment request to be created on management server")
			GinkgoWriter.Printf("New enrollment request found on management server: %s\n", *newEnrollmentRequest.Metadata.Name)

			defer cleanupDeviceAndER(harness, newEnrollmentId)

			By("Approving the new enrollment request")
			harness.ApproveEnrollment(newEnrollmentId, harness.TestEnrollmentApproval())
			GinkgoWriter.Printf("Approved new enrollment request: %s\n", newEnrollmentId)

			By("Verifying the re-enrolled device comes online with the new ID")
			Eventually(harness.GetDeviceWithStatusSystem, TIMEOUT, POLLING).WithArguments(
				newEnrollmentId).ShouldNot(BeNil())

			newDevice, err := harness.GetDevice(newEnrollmentId)
			Expect(err).NotTo(HaveOccurred())
			Expect(newDevice).NotTo(BeNil())
			Expect(newDevice.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusOnline),
				"Expected re-enrolled device to be online")
			GinkgoWriter.Printf("Re-enrolled device %s is now online\n", newEnrollmentId)
		})
	})

	Context("CLI argument validation", func() {
		It("Should reject invalid decommission CLI arguments", Label("88265"), func() {
			harness := e2e.GetWorkerHarness()

			tests := testutil.Cases(
				decommissionCLIEntry("no resource name", []string{"device"}, "exactly one resource name must be specified"),
				decommissionCLIEntry("empty name in slash format", []string{"device/"}, "resource name cannot be empty"),
				decommissionCLIEntry("wrong resource kind", []string{"fleet/my-fleet"}, "kind must be Device"),
				decommissionCLIEntry("invalid resource kind", []string{"foobar/something"}, "invalid resource kind"),
				decommissionCLIEntry("too many arguments", []string{"device", "name1", "name2"}, "exactly one resource name"),
				decommissionCLIEntry("mixed TYPE/NAME with extra args", []string{"device/foo", "extraArg"}, "cannot mix TYPE/NAME syntax with additional arguments"),
				decommissionCLIEntry("invalid --target flag", []string{"device/foo", "--target", "xyz"}, "decommission target must be one of"),
			)

			testutil.RunTable(tests, func(params DecommissionCLITestParams) {
				args := append([]string{"decommission"}, params.Args...)
				out, err := harness.CLI(args...)
				Expect(err).To(HaveOccurred(), "Expected error for args: %v", params.Args)
				Expect(out).To(ContainSubstring(params.ExpectedError),
					"Expected output to contain %q for args: %v", params.ExpectedError, params.Args)
			})
		})
	})

	Context("API error handling", func() {
		It("Should return 404 when decommissioning a non-existent device", Label("88271"), func() {
			harness := e2e.GetWorkerHarness()

			By("Attempting to decommission a device that does not exist")
			out, err := harness.CLI("decommission", "device/does-not-exist-12345")
			Expect(err).To(HaveOccurred(), "Expected error when device does not exist")
			Expect(out).To(ContainSubstring("404"),
				"Expected 404 status for non-existent device")
		})

		It("Should return 409 when decommissioning while ongoing and after completion", Label("88272-88273"), func() {
			harness := e2e.GetWorkerHarness()

			By("Enrolling a device and waiting for it to come online")
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			defer cleanupDeviceAndER(harness, deviceId)
			GinkgoWriter.Printf("Enrolled device for conflict test: %s\n", deviceId)

			By("Decommissioning the device")
			out, err := harness.CLI("decommission", "devices/"+deviceId)
			Expect(err).NotTo(HaveOccurred(), "First decommission should succeed")
			Expect(out).To(ContainSubstring(decommissionSuccessMsg), "First decommission should return 200 OK")

			By("Attempting to decommission while decommission is still ongoing")
			out, err = harness.CLI("decommission", "devices/"+deviceId)
			Expect(err).To(HaveOccurred(), "Decommission during ongoing should fail with conflict")
			Expect(out).To(SatisfyAny(
				ContainSubstring("409"),
				ContainSubstring("already has decommissioning requested"),
			), "Expected 409 conflict while decommission is ongoing")

			By("Waiting for decommission to complete")
			harness.WaitForDeviceContents(deviceId, decommissionCompleteDesc,
				func(device *v1beta1.Device) bool {
					return e2e.ConditionExists(device, "DeviceDecommissioning", "True", string(v1beta1.DecommissionStateComplete))
				}, TIMEOUT)

			By("Attempting to decommission after decommission has completed")
			out, err = harness.CLI("decommission", "devices/"+deviceId)
			Expect(err).To(HaveOccurred(), "Decommission after completion should fail with conflict")
			Expect(out).To(SatisfyAny(
				ContainSubstring("409"),
				ContainSubstring("already has decommissioning requested"),
			), "Expected 409 conflict after decommission completed")
		})

		It("Should return 500 when sending a malformed JSON body to the decommission endpoint", Label("88274"), func() {
			harness := e2e.GetWorkerHarness()

			By("Enrolling a device and waiting for it to come online")
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			defer cleanupDeviceAndER(harness, deviceId)
			GinkgoWriter.Printf("Enrolled device for malformed body test: %s\n", deviceId)

			By("Sending a malformed JSON body directly to the decommission API")
			malformedBody := strings.NewReader("not-valid-json")
			response, err := harness.Client.DecommissionDeviceWithBodyWithResponse(
				harness.Context, deviceId, "application/json", malformedBody,
			)
			Expect(err).NotTo(HaveOccurred(), "HTTP request itself should not fail")
			Expect(response.HTTPResponse).NotTo(BeNil())
			Expect(response.HTTPResponse.StatusCode).To(Equal(http.StatusBadRequest),
				"Expected 400 for malformed JSON body, got %d", response.HTTPResponse.StatusCode)
			Expect(string(response.Body)).To(ContainSubstring("failed to decode request body"),
				"Expected parse failure message in response body")
			GinkgoWriter.Printf("Received expected 400 response: %s\n", string(response.Body))
		})
	})
})

// decommissionCLIEntry creates a table-driven test case for CLI argument validation.
func decommissionCLIEntry(desc string, args []string, expectedError string) testutil.TestCase[DecommissionCLITestParams] {
	return testutil.TestCase[DecommissionCLITestParams]{
		Description: desc,
		Params: DecommissionCLITestParams{
			Args:          args,
			ExpectedError: expectedError,
		},
	}
}
