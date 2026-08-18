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
	It("rolls out VM and container apps and applies fleet vs device lifecycle", Label("vm", "slow"), func() {
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
		labelDeviceIntoFleet(harness, device1ID, fleetName)

		By("Waiting for both apps to run on device 1")
		waitForAppStatus(harness, device1ID, nginxAppName, v1beta1.ApplicationStatusRunning)
		waitForAppStatus(harness, device1ID, vmAppName, v1beta1.ApplicationStatusRunning)
		err = harness.WaitForApplicationSummary(device1ID, testutil.LONG_TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		if err != nil {
			logVMApplicationUnitStatus(harness, vmAppName)
		}
		Expect(err).ToNot(HaveOccurred())
		expectNginxReachable(harness)
		expectSSHWhoamiWithPassword(harness, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)

		By("Stopping the VM application on the fleet")
		_, err = harness.CLI("app", "stop", "fleet/"+fleetName, "--name", vmAppName, "-y")
		Expect(err).ToNot(HaveOccurred())
		waitForAppStatus(harness, device1ID, vmAppName, v1beta1.ApplicationStatusStopped)
		expectSSHInaccessible(harness, vmPublishedSSHPort, vmAppName)
		waitForAppStatus(harness, device1ID, nginxAppName, v1beta1.ApplicationStatusRunning)
		expectNginxReachable(harness)

		By("Labeling device 2 into the fleet so it inherits the fleet stop default")
		labelDeviceIntoFleet(harness, device2ID, fleetName)
		waitForAppStatus(harness2, device2ID, nginxAppName, v1beta1.ApplicationStatusRunning)
		waitForAppStatus(harness2, device2ID, vmAppName, v1beta1.ApplicationStatusStopped)
		waitForAppStatus(harness, device1ID, nginxAppName, v1beta1.ApplicationStatusRunning)
		waitForAppStatus(harness, device1ID, vmAppName, v1beta1.ApplicationStatusStopped)

		By("Starting the VM application on the fleet")
		_, err = harness.CLI("app", "start", "fleet/"+fleetName, "--name", vmAppName, "-y")
		Expect(err).ToNot(HaveOccurred())
		waitForAppStatus(harness, device1ID, vmAppName, v1beta1.ApplicationStatusRunning)
		waitForAppStatus(harness2, device2ID, vmAppName, v1beta1.ApplicationStatusRunning)
		expectSSHWhoamiWithPassword(harness, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)
		expectSSHWhoamiWithPassword(harness2, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)

		By("Stopping the VM application on device 1 only")
		_, err = harness.CLI("app", "stop", "device/"+device1ID, "--name", vmAppName, "-y")
		Expect(err).ToNot(HaveOccurred())
		waitForAppStatus(harness, device1ID, vmAppName, v1beta1.ApplicationStatusStopped)
		expectSSHInaccessible(harness, vmPublishedSSHPort, vmAppName)
		waitForAppStatus(harness2, device2ID, vmAppName, v1beta1.ApplicationStatusRunning)
		expectSSHWhoamiWithPassword(harness2, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)

		By("Starting the VM application on the fleet again so the newer fleet action wins")
		_, err = harness.CLI("app", "start", "fleet/"+fleetName, "--name", vmAppName, "-y")
		Expect(err).ToNot(HaveOccurred())
		waitForAppStatus(harness, device1ID, vmAppName, v1beta1.ApplicationStatusRunning)
		waitForAppStatus(harness2, device2ID, vmAppName, v1beta1.ApplicationStatusRunning)
		expectSSHWhoamiWithPassword(harness, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)
		expectSSHWhoamiWithPassword(harness2, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)

		By("Rejecting fleet-scoped app restart")
		out, err := harness.CLI("app", "restart", "fleet/"+fleetName, "--name", vmAppName, "-y")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("kind must be Device"))
	})
})

func labelDeviceIntoFleet(h *e2e.Harness, deviceID, fleetName string) {
	GinkgoHelper()
	nextRenderedVersion, err := h.PrepareNextDeviceVersion(deviceID)
	Expect(err).ToNot(HaveOccurred())
	Expect(h.SetLabelsForDevice(deviceID, map[string]string{fleetLabelKey: fleetName})).To(Succeed())
	Expect(h.WaitForDeviceNewRenderedVersion(deviceID, nextRenderedVersion)).To(Succeed())
}

func waitForAppStatus(h *e2e.Harness, deviceID, appName string, status v1beta1.ApplicationStatusType) {
	GinkgoHelper()
	err := h.WaitForApplicationStatus(deviceID, appName, status, testutil.LONG_TIMEOUT, testutil.POLLING)
	if err != nil && appName == vmAppName {
		logVMApplicationUnitStatus(h, appName)
	}
	Expect(err).ToNot(HaveOccurred())
}

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

func expectSSHInaccessible(h *e2e.Harness, port int, appName string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		_, sshErr := h.RunSSHOnDeviceLocalPort(port, vmGuestUser, vmGuestPassword, "/usr/bin/whoami")
		g.Expect(sshErr).To(HaveOccurred(), "expected SSH to %s on port %d to fail after stop", appName, port)
	}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
}
