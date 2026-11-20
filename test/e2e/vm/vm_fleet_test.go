package vm_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	nginxAppName  = "nginx"
	nginxImage    = "quay.io/flightctl-tests/nginx:1.28-alpine-slim"
	nginxHostPort = "8080"
	flightctlUser = "flightctl"
	fleetLabelKey = "fleet"
)

var _ = Describe("VM Applications on a Fleet", func() {
	It("rolls out VM and container apps and applies fleet vs device lifecycle", Label("vm", "slow", "90242"), func() {
		harness := e2e.GetWorkerHarness()
		ctx := harness.GetTestContext()
		testID := harness.GetTestIDFromContext()
		fleetName := fmt.Sprintf("fleet-with-apps-%s", testID)
		fleetSelector := v1beta1.LabelSelector{
			MatchLabels: &map[string]string{fleetLabelKey: fleetName},
		}

		By("Starting a second agent VM and enrolling both devices unlabeled")
		workerID2 := GinkgoParallelProcess()*100 + 1
		harness2, err := e2e.NewTestHarnessWithVMPool(ctx, workerID2)
		Expect(err).ToNot(HaveOccurred())
		harness2.SetTestContext(ctx)
		DeferCleanup(func() {
			harness2.PrintAgentLogsIfFailed()
			printVMQuadletDiagnosticsIfFailed(harness2)
			harness2.CaptureDeploymentLogsIfFailed()
			Expect(harness2.CleanUpAllTestResources()).To(Succeed(), "harness2 cleanup")
		})
		Expect(harness2.SetupVMFromPoolAndStartAgent(workerID2)).To(Succeed())

		device1ID, device1 := harness.EnrollAndWaitForOnlineStatus()
		Expect(device1ID).NotTo(BeEmpty())
		Expect(device1).NotTo(BeNil())
		device2ID, device2 := harness2.EnrollAndWaitForOnlineStatus()
		Expect(device2ID).NotTo(BeEmpty())
		Expect(device2).NotTo(BeNil())

		By("Creating a fleet with NGINX and a VM application")
		nginxSpec, err := e2e.NewContainerApplicationSpecWithRunAs(
			nginxAppName,
			nginxImage,
			[]v1beta1.ApplicationPort{nginxHostPort + ":80"},
			nil,
			nil,
			nil,
			flightctlUser,
		)
		Expect(err).ToNot(HaveOccurred())
		vmSpec, err := e2e.NewVmApplicationSpec(vmAppName, getVMImage())
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.CreateOrUpdateTestFleet(fleetName, fleetSelector, v1beta1.DeviceSpec{
			Applications: &[]v1beta1.ApplicationProviderSpec{nginxSpec, vmSpec},
		})).To(Succeed())

		By("Labeling device 1 into the fleet")
		harness.LabelDeviceIntoFleet(device1ID, fleetLabelKey, fleetName)

		By("Waiting for both apps to run on device 1")
		Expect(harness.WaitForApplicationStatus(device1ID, nginxAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).To(Succeed())
		waitForVMAppRunningHealthy(harness, device1ID, vmAppName)
		expectNginxReachable(harness)
		expectLoginPromptThenPasswordSSH(harness, device1ID, vmAppName, vmGuestUser, vmGuestPassword, vmPublishedSSHPort)

		By("Stopping the VM application on the fleet")
		_, err = harness.CLI("app", "stop", "fleet/"+fleetName, "--name", vmAppName, "-y")
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(device1ID, vmAppName, v1beta1.ApplicationStatusStopped, testutil.LONG_TIMEOUT, testutil.POLLING)).To(Succeed())
		harness.ExpectSSHUnavailableOnPort(vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)
		Expect(harness.WaitForApplicationStatus(device1ID, nginxAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).To(Succeed())
		expectNginxReachable(harness)

		By("Labeling device 2 into the fleet so it inherits the fleet stop default")
		harness.LabelDeviceIntoFleet(device2ID, fleetLabelKey, fleetName)
		Expect(harness2.WaitForApplicationStatus(device2ID, nginxAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).To(Succeed())
		Expect(harness2.WaitForApplicationStatus(device2ID, vmAppName, v1beta1.ApplicationStatusStopped, testutil.LONG_TIMEOUT, testutil.POLLING)).To(Succeed())
		Expect(harness.WaitForApplicationStatus(device1ID, nginxAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).To(Succeed())
		Expect(harness.WaitForApplicationStatus(device1ID, vmAppName, v1beta1.ApplicationStatusStopped, testutil.LONG_TIMEOUT, testutil.POLLING)).To(Succeed())

		By("Starting the VM application on the fleet")
		_, err = harness.CLI("app", "start", "fleet/"+fleetName, "--name", vmAppName, "-y")
		Expect(err).ToNot(HaveOccurred())
		waitForVMAppRunningHealthy(harness, device1ID, vmAppName)
		waitForVMAppRunningHealthy(harness2, device2ID, vmAppName)
		expectLoginPromptThenPasswordSSH(harness, device1ID, vmAppName, vmGuestUser, vmGuestPassword, vmPublishedSSHPort)
		expectLoginPromptThenPasswordSSH(harness2, device2ID, vmAppName, vmGuestUser, vmGuestPassword, vmPublishedSSHPort)

		By("Stopping the VM application on device 1 only")
		_, err = harness.CLI("app", "stop", "device/"+device1ID, "--name", vmAppName, "-y")
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.WaitForApplicationStatus(device1ID, vmAppName, v1beta1.ApplicationStatusStopped, testutil.LONG_TIMEOUT, testutil.POLLING)).To(Succeed())
		harness.ExpectSSHUnavailableOnPort(vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)
		waitForVMAppRunningHealthy(harness2, device2ID, vmAppName)
		expectSSHWhoamiWithPassword(harness2, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)

		By("Starting the VM application on the fleet again so the newer fleet action wins")
		_, err = harness.CLI("app", "start", "fleet/"+fleetName, "--name", vmAppName, "-y")
		Expect(err).ToNot(HaveOccurred())
		waitForVMAppRunningHealthy(harness, device1ID, vmAppName)
		waitForVMAppRunningHealthy(harness2, device2ID, vmAppName)
		expectLoginPromptThenPasswordSSH(harness, device1ID, vmAppName, vmGuestUser, vmGuestPassword, vmPublishedSSHPort)
		expectSSHWhoamiWithPassword(harness2, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)

		By("Rejecting fleet-scoped app restart")
		out, err := harness.CLI("app", "restart", "fleet/"+fleetName, "--name", vmAppName, "-y")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("kind must be Device"))
	})
})

func curlNginxHTTPStatus(h *e2e.Harness) (string, error) {
	out, err := h.VM.RunSSH([]string{"curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "--connect-timeout", "2", "--max-time", "5", "http://127.0.0.1:" + nginxHostPort + "/"}, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func expectNginxReachable(h *e2e.Harness) {
	GinkgoHelper()
	Eventually(func() (string, error) { return curlNginxHTTPStatus(h) }, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Equal("200"))
}
