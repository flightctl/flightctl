package devicesimulator_test

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	deviceCount          = 3
	enrollTimeout        = 120 * time.Second
	enrollPolling        = 2 * time.Second
	simulatorStopTimeout = 10 * time.Second
)

var _ = Describe("Device Simulator E2E Tests", Label("devicesimulator", "e2e"), func() {
	var (
		harness *e2e.Harness
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
	})

	Context("When using --skip-auto-enrollment", func() {
		var simulatorCmd *exec.Cmd

		AfterEach(func() {
			if simulatorCmd != nil {
				_ = harness.StopDeviceSimulator(simulatorCmd, simulatorStopTimeout)
				simulatorCmd = nil
			}
		})

		It("should create enrollment requests that remain unapproved until manually approved", func() {
			By("Starting the device simulator with --skip-auto-enrollment")
			var err error
			simulatorCmd, err = harness.RunDeviceSimulator(harness.Context,
				"--count", fmt.Sprintf("%d", deviceCount),
				"--skip-auto-enrollment",
			)
			Expect(err).ToNot(HaveOccurred())

			By(fmt.Sprintf("Waiting for %d enrollment requests to appear", deviceCount))
			var enrollmentRequests []v1beta1.EnrollmentRequest
			Eventually(func() int {
				resp, listErr := harness.Client.ListEnrollmentRequestsWithResponse(harness.Context, &v1beta1.ListEnrollmentRequestsParams{})
				if listErr != nil || resp.JSON200 == nil {
					return 0
				}
				enrollmentRequests = resp.JSON200.Items
				return len(enrollmentRequests)
			}, enrollTimeout, enrollPolling).Should(BeNumerically(">=", deviceCount),
				fmt.Sprintf("expected at least %d enrollment requests", deviceCount))

			By("Verifying all enrollment requests are NOT approved")
			for _, er := range enrollmentRequests {
				isApproved := er.Status != nil && er.Status.Approval != nil && er.Status.Approval.Approved
				Expect(isApproved).To(BeFalse(),
					fmt.Sprintf("enrollment request %s should not be approved", safeNameStr(er.Metadata.Name)))
			}

			By("Verifying no devices exist from the simulator")
			for _, er := range enrollmentRequests {
				name := safeNameStr(er.Metadata.Name)
				resp, getErr := harness.Client.GetDeviceWithResponse(harness.Context, name)
				Expect(getErr).ToNot(HaveOccurred())
				Expect(resp.JSON200).To(BeNil(),
					fmt.Sprintf("device %s should not exist before approval", name))
			}

			firstER := enrollmentRequests[0]
			firstName := safeNameStr(firstER.Metadata.Name)

			By(fmt.Sprintf("Manually approving enrollment request %s", firstName))
			harness.ApproveEnrollment(firstName, harness.TestEnrollmentApproval())

			By("Verifying the approved device appears")
			Eventually(func() bool {
				resp, getErr := harness.Client.GetDeviceWithResponse(harness.Context, firstName)
				return getErr == nil && resp != nil && resp.JSON200 != nil
			}, enrollTimeout, enrollPolling).Should(BeTrue(),
				fmt.Sprintf("device %s should exist after approval", firstName))

			By("Verifying the remaining enrollment requests are still unapproved")
			for _, er := range enrollmentRequests[1:] {
				name := safeNameStr(er.Metadata.Name)
				resp, getErr := harness.Client.GetEnrollmentRequestWithResponse(harness.Context, name)
				Expect(getErr).ToNot(HaveOccurred())
				Expect(resp.JSON200).ToNot(BeNil())
				isApproved := resp.JSON200.Status != nil && resp.JSON200.Status.Approval != nil && resp.JSON200.Status.Approval.Approved
				Expect(isApproved).To(BeFalse(),
					fmt.Sprintf("enrollment request %s should still not be approved", name))
			}
		})
	})
})

func safeNameStr(name *string) string {
	if name == nil {
		return "<nil>"
	}
	return *name
}
