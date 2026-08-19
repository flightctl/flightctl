package helm_test

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	lifecycleContainerApp   = "nginx"
	lifecycleContainerURL   = "http://127.0.0.1:8080/"
	lifecycleQuadletApp     = "nginx-quadlet"
	lifecycleQuadletURL     = "http://127.0.0.1:8081/"
	lifecycleComposeApp     = "compose-demo"
	lifecycleHelmApp        = "helm-demo"
	lifecycleHelmNamespace  = "test-app"
	lifecycleVMApp          = "test-vm"
	lifecycleVMSSHPort      = 2222
	lifecycleVMUser         = "fedora"
	lifecycleVMPassword     = "fedora"
	lifecycleNoopWindow     = 3 * time.Second
	lifecycleAlpineImage    = "quay.io/flightctl-tests/alpine:v1"
	lifecycleDefaultVMImage = "quay.io/containerdisks/fedora:40"
)

var _ = Describe("Application lifecycle stop start restart", Label("microshift"), func() {
	var (
		harness      *e2e.Harness
		deviceID     string
		helmChartRef string
		microshiftOs = &v1beta1.DeviceOsSpec{Image: util.NewDeviceImageReference(util.DeviceTags.V12).String()}
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
		services := auxiliary.Get(harness.GetTestContext())
		helmChartRef = fmt.Sprintf("%s:%s/flightctl/%s", services.Registry.Host, services.Registry.Port, helmChartV1)
	})

	It("stops, starts, and restarts each app type without taking the others down", Label("slow", "vm", "90240"), func() {
		apps := []lifecycleApp{
			{
				name:  lifecycleContainerApp,
				up:    func() { expectHTTPReachable(harness, lifecycleContainerURL) },
				down:  func() { expectHTTPUnreachable(harness, lifecycleContainerURL) },
				alive: func() { expectHTTPReachable(harness, lifecycleContainerURL) },
			},
			{
				name:  lifecycleQuadletApp,
				up:    func() { expectHTTPReachable(harness, lifecycleQuadletURL) },
				down:  func() { expectHTTPUnreachable(harness, lifecycleQuadletURL) },
				alive: func() { expectHTTPReachable(harness, lifecycleQuadletURL) },
			},
			{
				name:  lifecycleComposeApp,
				up:    func() { expectComposeWorkloadUp(harness) },
				down:  func() { expectComposeWorkloadDown(harness) },
				alive: func() { expectComposeWorkloadUp(harness) },
			},
			{
				name:  lifecycleHelmApp,
				up:    func() { expectHelmPodsReady(harness, lifecycleHelmNamespace) },
				down:  func() { expectHelmPodsGone(harness, lifecycleHelmNamespace) },
				alive: func() { expectHelmPodsPresent(harness, lifecycleHelmNamespace) },
			},
			{
				name:  lifecycleVMApp,
				up:    func() { expectVMSSHUp(harness) },
				down:  func() { expectNoVirtLauncher(harness, lifecycleVMApp) },
				alive: func() { expectVirtLauncherRunning(harness, lifecycleVMApp) },
			},
		}

		By("Updating OS to one with microshift")
		err := harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Os = microshiftOs
		})
		Expect(err).NotTo(HaveOccurred())

		By("Ensuring microshift is ready")
		err = harness.EnsureMicroshiftConfigs()
		Expect(err).NotTo(HaveOccurred())
		err = harness.WaitForMicroshiftReady(e2e.MicroshiftKubeconfigPath)
		Expect(err).NotTo(HaveOccurred())

		By("Deploying container, quadlet, compose, helm, and vm applications")
		specs, err := lifecycleApplicationSpecs(helmChartRef)
		Expect(err).NotTo(HaveOccurred())
		err = harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &specs
		})
		Expect(err).NotTo(HaveOccurred())

		By("Waiting until all applications are Running and the summary is Healthy")
		for _, app := range apps {
			waitForAppStatus(harness, deviceID, app.name, v1beta1.ApplicationStatusRunning)
		}
		err = harness.WaitForApplicationSummary(deviceID, util.LONG_TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		Expect(err).NotTo(HaveOccurred())

		By("Sanity-checking every application type while all are running")
		for _, app := range apps {
			By("Sanity-checking " + app.name)
			app.up()
		}

		for _, app := range apps {
			runLifecycleMatrix(harness, deviceID, app, apps)
		}
	})
})

type lifecycleApp struct {
	name  string
	up    func()
	down  func()
	alive func()
}

func lifecycleApplicationSpecs(helmChartRef string) ([]v1beta1.ApplicationProviderSpec, error) {
	containerSpec, err := e2e.NewContainerApplicationSpec(
		lifecycleContainerApp,
		nginxImage,
		[]v1beta1.ApplicationPort{"8080:80"},
		nil, nil, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("container spec: %w", err)
	}

	quadletContent := fmt.Sprintf(`[Container]
Image=%s
PublishPort=8081:80
[Install]
WantedBy=default.target
`, nginxImage)
	quadletSpec, err := e2e.NewQuadletInlineSpec(
		lifecycleQuadletApp,
		flightctlUser,
		[]string{"nginx.container"},
		[]string{quadletContent},
	)
	if err != nil {
		return nil, fmt.Errorf("quadlet spec: %w", err)
	}

	composeContent := fmt.Sprintf(`version: "3.8"
services:
  alpine:
    image: %s
    command: ["sleep", "infinity"]
`, lifecycleAlpineImage)
	composeSpec, err := e2e.NewComposeInlineSpec(lifecycleComposeApp, "podman-compose.yaml", composeContent, "")
	if err != nil {
		return nil, fmt.Errorf("compose spec: %w", err)
	}

	helmSpec, err := e2e.NewHelmApplicationSpec(lifecycleHelmApp, helmChartRef, lifecycleHelmNamespace, nil)
	if err != nil {
		return nil, fmt.Errorf("helm spec: %w", err)
	}

	vmSpec, err := e2e.NewVmApplicationSpec(lifecycleVMApp, lifecycleVMImage())
	if err != nil {
		return nil, fmt.Errorf("vm spec: %w", err)
	}

	return []v1beta1.ApplicationProviderSpec{containerSpec, quadletSpec, composeSpec, helmSpec, vmSpec}, nil
}

func lifecycleVMImage() string {
	if image := os.Getenv("FLIGHTCTL_E2E_VM_IMAGE"); image != "" {
		return image
	}
	return lifecycleDefaultVMImage
}

func runLifecycleMatrix(h *e2e.Harness, deviceID string, target lifecycleApp, all []lifecycleApp) {
	GinkgoHelper()

	By("Stop " + target.name)
	appLifecycleCLI(h, "stop", deviceID, target.name)
	waitForStop(h, deviceID, target.name)
	target.down()
	expectOthersAlive(h, deviceID, target.name, all)

	By("Stop " + target.name + " while already stopped")
	appLifecycleCLI(h, "stop", deviceID, target.name)
	expectAppStatus(h, deviceID, target.name, v1beta1.ApplicationStatusStopped)

	By("Restart " + target.name + " while stopped")
	appLifecycleCLI(h, "restart", deviceID, target.name)
	expectAppStatusConsistently(h, deviceID, target.name, v1beta1.ApplicationStatusStopped)

	By("Start " + target.name)
	appLifecycleCLI(h, "start", deviceID, target.name)
	waitForAppStatus(h, deviceID, target.name, v1beta1.ApplicationStatusRunning)
	target.up()
	expectOthersRunning(h, deviceID, target.name, all)

	By("Start " + target.name + " while already running")
	appLifecycleCLI(h, "start", deviceID, target.name)
	expectAppStatus(h, deviceID, target.name, v1beta1.ApplicationStatusRunning)

	By("Restart " + target.name + " while running")
	appLifecycleCLI(h, "restart", deviceID, target.name)
	waitForAppStatus(h, deviceID, target.name, v1beta1.ApplicationStatusRunning)
	target.up()
	expectOthersRunning(h, deviceID, target.name, all)
}

func expectOthersAlive(h *e2e.Harness, deviceID, targetName string, all []lifecycleApp) {
	GinkgoHelper()
	for _, app := range all {
		if app.name == targetName {
			continue
		}
		expectAppStatus(h, deviceID, app.name, v1beta1.ApplicationStatusRunning)
		app.alive()
	}
}

func expectOthersRunning(h *e2e.Harness, deviceID, targetName string, all []lifecycleApp) {
	GinkgoHelper()
	for _, app := range all {
		if app.name == targetName {
			continue
		}
		expectAppStatus(h, deviceID, app.name, v1beta1.ApplicationStatusRunning)
	}
}

func appLifecycleCLI(h *e2e.Harness, action, deviceID, appName string) {
	GinkgoHelper()
	_, err := h.CLI("app", action, fmt.Sprintf("device/%s", deviceID), "--name", appName, "-y")
	Expect(err).NotTo(HaveOccurred(), "flightctl app %s device/%s --name %s", action, deviceID, appName)
}

func waitForStop(h *e2e.Harness, deviceID, appName string) {
	GinkgoHelper()
	waitForAppStatus(h, deviceID, appName, v1beta1.ApplicationStatusStopped)
	status, err := currentAppStatus(h, deviceID, appName)
	Expect(err).NotTo(HaveOccurred())
	Expect(status).To(Equal(v1beta1.ApplicationStatusStopped), "application %s", appName)
	Expect(status).NotTo(Equal(v1beta1.ApplicationStatusError), "application %s", appName)
	Expect(status).NotTo(Equal(v1beta1.ApplicationStatusCompleted), "application %s", appName)
}

func waitForAppStatus(h *e2e.Harness, deviceID, appName string, status v1beta1.ApplicationStatusType) {
	GinkgoHelper()
	waitForAppStatusWithTimeout(h, deviceID, appName, status, util.LONG_TIMEOUT)
}

func waitForAppStatusWithTimeout(h *e2e.Harness, deviceID, appName string, status v1beta1.ApplicationStatusType, timeout time.Duration) {
	GinkgoHelper()
	err := h.WaitForApplicationStatus(deviceID, appName, status, timeout, util.POLLING)
	Expect(err).NotTo(HaveOccurred())
}

func expectAppStatus(h *e2e.Harness, deviceID, appName string, status v1beta1.ApplicationStatusType) {
	GinkgoHelper()
	got, err := currentAppStatus(h, deviceID, appName)
	Expect(err).NotTo(HaveOccurred())
	Expect(got).To(Equal(status), "application %s should be %s", appName, status)
}

func expectAppStatusConsistently(h *e2e.Harness, deviceID, appName string, status v1beta1.ApplicationStatusType) {
	GinkgoHelper()
	Consistently(func(g Gomega) {
		got, err := currentAppStatus(h, deviceID, appName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(got).To(Equal(status), "application %s should remain %s", appName, status)
	}, lifecycleNoopWindow, util.POLLING).Should(Succeed())
}

func currentAppStatus(h *e2e.Harness, deviceID, appName string) (v1beta1.ApplicationStatusType, error) {
	resp, err := h.GetDeviceWithStatusSystem(deviceID)
	if err != nil {
		return "", err
	}
	if resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil {
		return "", fmt.Errorf("device %s has no status", deviceID)
	}
	for _, app := range resp.JSON200.Status.Applications {
		if app.Name == appName {
			return app.Status, nil
		}
	}
	return "", fmt.Errorf("application %s not found in device status", appName)
}

func expectHTTPReachable(h *e2e.Harness, url string) {
	GinkgoHelper()
	Eventually(func() error {
		return curlLocal(h, url)
	}, util.TIMEOUT, util.POLLING).Should(Succeed(), "GET %s should succeed", url)
}

func expectHTTPUnreachable(h *e2e.Harness, url string) {
	GinkgoHelper()
	Eventually(func() error {
		return curlLocal(h, url)
	}, util.TIMEOUT, util.POLLING).Should(HaveOccurred(), "GET %s should fail", url)
}

func curlLocal(h *e2e.Harness, url string) error {
	_, err := h.VM.RunSSH([]string{"curl", "-sS", "--fail", "--connect-timeout", "2", "--max-time", "5", url}, nil)
	return err
}

func expectComposeWorkloadUp(h *e2e.Harness) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		names, err := h.RunPodmanPsContainerNames(false)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(names).To(ContainSubstring(lifecycleComposeApp), "compose workload should be running")
	}, util.TIMEOUT, util.POLLING).Should(Succeed())
}

func expectComposeWorkloadDown(h *e2e.Harness) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		names, err := h.RunPodmanPsContainerNames(false)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(names).NotTo(ContainSubstring(lifecycleComposeApp), "compose workload should not be running")
	}, util.TIMEOUT, util.POLLING).Should(Succeed())
}

func expectHelmPodsPresent(h *e2e.Harness, namespace string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		pods, err := h.GetPodsInNamespace(namespace)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(pods).NotTo(BeEmpty(), "expected pods in namespace %s", namespace)
	}, util.TIMEOUT, util.POLLING).Should(Succeed())
}

func expectHelmPodsReady(h *e2e.Harness, namespace string) {
	GinkgoHelper()
	expectHelmPodsPresent(h, namespace)

	_, err := h.VM.RunSSH([]string{
		"sudo", "oc", "wait",
		"--for=condition=Ready", "pods", "--all",
		"-n", namespace,
		"--timeout=60s",
		fmt.Sprintf("--kubeconfig=%s", e2e.MicroshiftKubeconfigPath),
	}, nil)
	Expect(err).NotTo(HaveOccurred(), "waiting for Ready pods in namespace %s", namespace)
}

func expectHelmPodsGone(h *e2e.Harness, namespace string) {
	GinkgoHelper()
	err := h.WaitForNoPodsInNamespace(namespace, util.LONG_TIMEOUT)
	Expect(err).NotTo(HaveOccurred())
}

func expectVMSSHUp(h *e2e.Harness) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		out, err := h.RunSSHOnDeviceLocalPort(lifecycleVMSSHPort, lifecycleVMUser, lifecycleVMPassword, "/usr/bin/whoami")
		g.Expect(err).NotTo(HaveOccurred(), "password SSH to %s on published port %d failed", lifecycleVMApp, lifecycleVMSSHPort)
		g.Expect(strings.TrimSpace(out)).To(Equal(lifecycleVMUser))
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

func expectVirtLauncherRunning(h *e2e.Harness, appName string) {
	GinkgoHelper()
	pattern := fmt.Sprintf("virt-launcher-%s", appName)
	Eventually(func(g Gomega) {
		names, err := h.RunPodmanPsContainerNames(false)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(names).To(ContainSubstring(pattern), "virt-launcher for %s should be running", appName)
	}, util.TIMEOUT, util.POLLING).Should(Succeed())
}

func expectNoVirtLauncher(h *e2e.Harness, appName string) {
	GinkgoHelper()
	pattern := fmt.Sprintf("virt-launcher-%s", appName)
	Eventually(func(g Gomega) {
		names, err := h.RunPodmanPsContainerNames(false)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(names).NotTo(ContainSubstring(pattern), "virt-launcher for %s should not be running", appName)
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}
