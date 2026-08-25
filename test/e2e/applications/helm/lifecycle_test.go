package helm_test

import (
	"fmt"
	"os"
	"sort"
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
	lifecycleContainerApp       = "nginx"
	lifecycleContainerURL       = "http://127.0.0.1:8080/"
	lifecycleQuadletApp         = "nginx-quadlet"
	lifecycleQuadletURL         = "http://127.0.0.1:8081/"
	lifecycleComposeApp         = "compose-demo"
	lifecycleHelmApp            = "helm-demo"
	lifecycleHelmNamespace      = "test-app"
	lifecycleVMApp              = "test-vm"
	lifecycleVMSSHPort          = 2222
	lifecycleVMUser             = "fedora"
	lifecycleVMPassword         = "fedora"
	lifecycleNoopWindow         = 5 * time.Second
	lifecycleAlpineImage        = "quay.io/flightctl-tests/alpine:v1"
	lifecycleDefaultVMImage     = "quay.io/containerdisks/fedora:40"
	lifecycleCurlConnectTimeout = "2"
	lifecycleCurlMaxTime        = "5"
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

	// App types: container, quadlet, compose, helm, and vm.
	It("stops, starts, and restarts each app type without taking the others down", Label("slow", "vm", "90240"), func() {
		apps := []lifecycleApp{
			{
				name:     lifecycleContainerApp,
				up:       func() { expectHTTP(harness, lifecycleContainerURL, true) },
				down:     func() { expectHTTP(harness, lifecycleContainerURL, false) },
				alive:    func() { expectHTTP(harness, lifecycleContainerURL, true) },
				identity: func() (string, error) { return podmanWorkloadIdentity(harness, "root", lifecycleContainerApp) },
			},
			{
				name:  lifecycleQuadletApp,
				up:    func() { expectHTTP(harness, lifecycleQuadletURL, true) },
				down:  func() { expectHTTP(harness, lifecycleQuadletURL, false) },
				alive: func() { expectHTTP(harness, lifecycleQuadletURL, true) },
				// EDM-3440: podman as the runAs user fails on root-owned ~/.config.
				identity: func() (string, error) {
					return systemdUserWorkloadIdentity(harness, flightctlUser, lifecycleQuadletApp)
				},
			},
			{
				name:     lifecycleComposeApp,
				up:       func() { expectComposeWorkloadUp(harness) },
				down:     func() { expectComposeWorkloadDown(harness) },
				alive:    func() { expectComposeWorkloadUp(harness) },
				identity: func() (string, error) { return podmanWorkloadIdentity(harness, "root", lifecycleComposeApp) },
			},
			{
				name:     lifecycleHelmApp,
				up:       func() { expectHelmPodsReady(harness, lifecycleHelmNamespace) },
				down:     func() { expectHelmPodsGone(harness, lifecycleHelmNamespace) },
				alive:    func() { expectHelmPodsPresent(harness, lifecycleHelmNamespace) },
				identity: func() (string, error) { return helmWorkloadIdentity(harness, lifecycleHelmNamespace) },
			},
			{
				name:  lifecycleVMApp,
				up:    func() { expectVMSSHUp(harness) },
				down:  func() { expectVirtLauncher(harness, lifecycleVMApp, false) },
				alive: func() { expectVirtLauncher(harness, lifecycleVMApp, true) },
				identity: func() (string, error) {
					return podmanWorkloadIdentity(harness, "root", "virt-launcher-"+lifecycleVMApp)
				},
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

// lifecycleApp is one application type under test and the probes used while cycling others.
type lifecycleApp struct {
	name     string
	up       func()                 // workload is fully serving (HTTP, Ready pods, or guest SSH)
	down     func()                 // workload is gone
	alive    func()                 // lighter check that the app is still up while another is stopped
	identity func() (string, error) // container/pod/virt-launcher instance identity
}

// lifecycleApplicationSpecs returns container, quadlet, compose, helm, and VM specs to deploy together.
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

// lifecycleVMImage is the guest image for the VM app, overridable via FLIGHTCTL_E2E_VM_IMAGE.
func lifecycleVMImage() string {
	if image := os.Getenv("FLIGHTCTL_E2E_VM_IMAGE"); image != "" {
		return image
	}
	return lifecycleDefaultVMImage
}

// runLifecycleMatrix stop/start/restarts target and asserts the other apps stay up.
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
	target.down()

	By("Start " + target.name)
	appLifecycleCLI(h, "start", deviceID, target.name)
	waitForAppStatus(h, deviceID, target.name, v1beta1.ApplicationStatusRunning)
	target.up()
	expectOthersAlive(h, deviceID, target.name, all)

	By("Start " + target.name + " while already running")
	beforeStart, err := target.identity()
	Expect(err).NotTo(HaveOccurred(), "capturing workload identity for %s before a second start", target.name)
	Expect(beforeStart).NotTo(BeEmpty(), "workload identity for %s should be present before a second start", target.name)
	appLifecycleCLI(h, "start", deviceID, target.name)
	expectUnchangedWorkloadIdentity(h, deviceID, target, beforeStart)
	target.up()

	By("Restart " + target.name + " while running")
	beforeRestart, err := target.identity()
	Expect(err).NotTo(HaveOccurred(), "capturing workload identity for %s before restart", target.name)
	Expect(beforeRestart).NotTo(BeEmpty(), "workload identity for %s should be present before restart", target.name)
	appLifecycleCLI(h, "restart", deviceID, target.name)
	waitForNewWorkloadIdentity(target, beforeRestart)
	waitForAppStatus(h, deviceID, target.name, v1beta1.ApplicationStatusRunning)
	target.up()
	expectOthersAlive(h, deviceID, target.name, all)
}

// expectUnchangedWorkloadIdentity asserts the app stays Running with the same
// workload identity for lifecycleNoopWindow, so a start that restarts the
// workload cannot pass.
func expectUnchangedWorkloadIdentity(h *e2e.Harness, deviceID string, target lifecycleApp, before string) {
	GinkgoHelper()
	Consistently(func(g Gomega) {
		got, err := currentAppStatus(h, deviceID, target.name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(got).To(Equal(v1beta1.ApplicationStatusRunning), "application %s should remain running", target.name)
		identity, err := target.identity()
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(identity).NotTo(BeEmpty(), "workload identity for %s should be present after start", target.name)
		g.Expect(identity).To(Equal(before), "workload identity for %s should remain unchanged after start", target.name)
	}, lifecycleNoopWindow, util.POLLING).Should(Succeed())
}

// waitForNewWorkloadIdentity waits until target.identity differs from before, so a no-op restart cannot pass.
func waitForNewWorkloadIdentity(target lifecycleApp, before string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		got, err := target.identity()
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(got).NotTo(BeEmpty(), "workload identity for %s should be present after restart", target.name)
		g.Expect(got).NotTo(Equal(before), "workload identity for %s should change after restart", target.name)
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

// podmanWorkloadIdentity returns sorted "id startedAt" lines for running containers whose names contain nameSubstring.
func podmanWorkloadIdentity(h *e2e.Harness, user, nameSubstring string) (string, error) {
	namesOutput, err := h.RunPodmanPsContainerNamesAsUser(user, false)
	if err != nil {
		return "", fmt.Errorf("listing containers for user %s: %w", user, err)
	}
	var names []string
	for _, line := range strings.Split(namesOutput, "\n") {
		name := strings.TrimSpace(line)
		if name != "" && strings.Contains(name, nameSubstring) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no running containers matching %q for user %s", nameSubstring, user)
	}

	out, err := podmanInspectAsUser(h, user, names)
	if err != nil {
		return "", fmt.Errorf("inspecting containers matching %q for user %s: %w", nameSubstring, user, err)
	}
	lines := normalizedIdentityLines(out)
	if len(lines) == 0 {
		return "", fmt.Errorf("empty inspect output for containers matching %q for user %s", nameSubstring, user)
	}
	return strings.Join(lines, "\n"), nil
}

// podmanInspectAsUser inspects containers as user and returns "id startedAt" per container.
func podmanInspectAsUser(h *e2e.Harness, user string, containers []string) (string, error) {
	format := "{{.Id}} {{.State.StartedAt}}"
	quoted := make([]string, 0, len(containers))
	for _, name := range containers {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	inspect := fmt.Sprintf("podman inspect --format %q %s", format, strings.Join(quoted, " "))
	return h.RunShellAsUserOnVM(user, inspect)
}

// systemdUserWorkloadIdentity returns sorted "unit invocationID" lines for running
// user systemd services whose names contain nameSubstring. Rootless podman as the
// runAs user fails while ~/.config is root-owned (EDM-3440), so quadlet restart
// identity is taken from the user systemd instance via systemctl --user -M.
func systemdUserWorkloadIdentity(h *e2e.Harness, user, nameSubstring string) (string, error) {
	listCmd := fmt.Sprintf("systemctl --user -M %s@ list-units --type=service --state=running --no-legend --plain --no-pager", user)
	namesOutput, err := h.RunShellAsUserOnVM("root", listCmd)
	if err != nil {
		return "", fmt.Errorf("listing systemd user units for %s: %v", user, err)
	}
	var units []string
	for _, line := range strings.Split(namesOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if strings.Contains(name, nameSubstring) {
			units = append(units, name)
		}
	}
	if len(units) == 0 {
		return "", fmt.Errorf("no running systemd user units matching %q for user %s", nameSubstring, user)
	}

	showCmd := fmt.Sprintf("systemctl --user -M %s@ show -p Id -p InvocationID -- %s", user, strings.Join(units, " "))
	out, err := h.RunShellAsUserOnVM("root", showCmd)
	if err != nil {
		return "", fmt.Errorf("showing InvocationID for units matching %q for user %s: %v", nameSubstring, user, err)
	}
	pairs := systemdIdentityPairs(out)
	if len(pairs) == 0 {
		return "", fmt.Errorf("empty InvocationID for units matching %q for user %s", nameSubstring, user)
	}
	return strings.Join(pairs, "\n"), nil
}

// systemdIdentityPairs parses systemctl show -p Id -p InvocationID output into sorted "id invocation" lines.
func systemdIdentityPairs(output string) []string {
	var pairs []string
	var id, invocation string
	flush := func() {
		if id != "" && invocation != "" {
			pairs = append(pairs, id+" "+invocation)
		}
		id, invocation = "", ""
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "Id":
			id = value
		case "InvocationID":
			invocation = value
		}
	}
	flush()
	sort.Strings(pairs)
	return pairs
}

// helmWorkloadIdentity returns sorted "uid startTime" lines for pods in namespace.
func helmWorkloadIdentity(h *e2e.Harness, namespace string) (string, error) {
	cmd := []string{
		"sudo", "oc", "get", "pods",
		"-n", namespace,
		"--no-headers",
		"-o", "custom-columns=UID:.metadata.uid,START:.status.startTime",
		fmt.Sprintf("--kubeconfig=%s", e2e.MicroshiftKubeconfigPath),
	}
	stdout, err := h.VM.RunSSH(cmd, nil)
	output := ""
	if stdout != nil {
		output = strings.TrimSpace(stdout.String())
	}
	if err != nil {
		if strings.Contains(output, "NotFound") || strings.Contains(output, "not found") {
			return "", fmt.Errorf("no pods in namespace %s", namespace)
		}
		return "", fmt.Errorf("listing pods in namespace %s: %w", namespace, err)
	}
	if output == "" || strings.Contains(output, "No resources found") {
		return "", fmt.Errorf("no pods in namespace %s", namespace)
	}
	lines := normalizedIdentityLines(output)
	if len(lines) == 0 {
		return "", fmt.Errorf("no pods in namespace %s", namespace)
	}
	return strings.Join(lines, "\n"), nil
}

// normalizedIdentityLines trims, collapses whitespace, drops empties, and sorts identity lines.
func normalizedIdentityLines(output string) []string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		lines = append(lines, strings.Join(fields, " "))
	}
	sort.Strings(lines)
	return lines
}

// expectOthersAlive asserts every app except target is Running and passes its alive probe.
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

// appLifecycleCLI runs `flightctl app <action>` for appName on the device.
func appLifecycleCLI(h *e2e.Harness, action, deviceID, appName string) {
	GinkgoHelper()
	out, err := h.CLI("app", action, fmt.Sprintf("device/%s", deviceID), "--name", appName, "-y")
	Expect(err).NotTo(HaveOccurred(), "flightctl app %s device/%s --name %s", action, deviceID, appName)
	Expect(out).To(ContainSubstring(fmt.Sprintf("Requested %s of application %q", action, appName)),
		"CLI should confirm %s of %s", action, appName)
}

// waitForStop waits until the app is Stopped.
func waitForStop(h *e2e.Harness, deviceID, appName string) {
	GinkgoHelper()
	waitForAppStatus(h, deviceID, appName, v1beta1.ApplicationStatusStopped)
	status, err := currentAppStatus(h, deviceID, appName)
	Expect(err).NotTo(HaveOccurred())
	Expect(status).To(Equal(v1beta1.ApplicationStatusStopped), "application %s", appName)
}

// waitForAppStatus waits until the named application reports status.
func waitForAppStatus(h *e2e.Harness, deviceID, appName string, status v1beta1.ApplicationStatusType) {
	GinkgoHelper()
	err := h.WaitForApplicationStatus(deviceID, appName, status, util.LONG_TIMEOUT, util.POLLING)
	Expect(err).NotTo(HaveOccurred())
}

// expectAppStatus asserts the named application currently reports status.
func expectAppStatus(h *e2e.Harness, deviceID, appName string, status v1beta1.ApplicationStatusType) {
	GinkgoHelper()
	got, err := currentAppStatus(h, deviceID, appName)
	Expect(err).NotTo(HaveOccurred())
	Expect(got).To(Equal(status), "application %s should be %s", appName, status)
}

// expectAppStatusConsistently asserts the named application remains at status for lifecycleNoopWindow.
func expectAppStatusConsistently(h *e2e.Harness, deviceID, appName string, status v1beta1.ApplicationStatusType) {
	GinkgoHelper()
	Consistently(func(g Gomega) {
		got, err := currentAppStatus(h, deviceID, appName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(got).To(Equal(status), "application %s should remain %s", appName, status)
	}, lifecycleNoopWindow, util.POLLING).Should(Succeed())
}

// currentAppStatus returns the application's status from the device.
func currentAppStatus(h *e2e.Harness, deviceID, appName string) (v1beta1.ApplicationStatusType, error) {
	resp, err := h.GetDeviceWithStatusSystem(deviceID)
	if err != nil {
		return "", fmt.Errorf("getting device %s: %w", deviceID, err)
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

// expectHTTP polls until GET url from the device succeeds. When unreachable, it
// waits for curl to fail on the device (not an SSH transport error), then
// requires that condition throughout lifecycleNoopWindow.
func expectHTTP(h *e2e.Harness, url string, reachable bool) {
	GinkgoHelper()
	if reachable {
		Eventually(func() error {
			return h.CurlOnDevice(url, lifecycleCurlConnectTimeout, lifecycleCurlMaxTime)
		}, util.TIMEOUT, util.POLLING).Should(Succeed(), "GET %s should succeed", url)
		return
	}

	Eventually(func(g Gomega) {
		err := h.CurlOnDevice(url, lifecycleCurlConnectTimeout, lifecycleCurlMaxTime)
		g.Expect(err).To(HaveOccurred(), "GET %s should be unreachable", url)
		g.Expect(isHTTPConnectionFailure(err)).To(BeTrue(),
			"GET %s failed with %v, want a curl error from the device, not an SSH transport failure", url, err)
	}, util.TIMEOUT, util.POLLING).Should(Succeed())

	Consistently(func(g Gomega) {
		err := h.CurlOnDevice(url, lifecycleCurlConnectTimeout, lifecycleCurlMaxTime)
		g.Expect(err).To(HaveOccurred(), "GET %s should remain unreachable", url)
		g.Expect(isHTTPConnectionFailure(err)).To(BeTrue(),
			"GET %s failed with %v, want a curl error from the device, not an SSH transport failure", url, err)
	}, lifecycleNoopWindow, util.POLLING).Should(Succeed())
}

// isHTTPConnectionFailure reports whether err is from curl running on the device.
// CurlOnDevice wraps VM.RunSSH; SSH transport failures have no curl: prefix.
func isHTTPConnectionFailure(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "curl:")
}

// expectComposeWorkloadUp polls until the compose app container appears in podman ps.
func expectComposeWorkloadUp(h *e2e.Harness) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		names, err := h.RunPodmanPsContainerNames(false)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(names).To(ContainSubstring(lifecycleComposeApp), "compose workload should be running")
	}, util.TIMEOUT, util.POLLING).Should(Succeed())
}

// expectComposeWorkloadDown polls until the compose app container is gone from podman ps.
func expectComposeWorkloadDown(h *e2e.Harness) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		names, err := h.RunPodmanPsContainerNames(false)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(names).NotTo(ContainSubstring(lifecycleComposeApp), "compose workload should not be running")
	}, util.TIMEOUT, util.POLLING).Should(Succeed())
}

// expectHelmPodsPresent polls until at least one pod exists in namespace.
func expectHelmPodsPresent(h *e2e.Harness, namespace string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		pods, err := h.GetPodsInNamespace(namespace)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(pods).NotTo(BeEmpty(), "expected pods in namespace %s", namespace)
	}, util.TIMEOUT, util.POLLING).Should(Succeed())
}

// expectHelmPodsReady waits until pods exist in namespace and are Ready.
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

// expectHelmPodsGone waits until namespace has no pods.
func expectHelmPodsGone(h *e2e.Harness, namespace string) {
	GinkgoHelper()
	err := h.WaitForNoPodsInNamespace(namespace, util.LONG_TIMEOUT)
	Expect(err).NotTo(HaveOccurred())
}

// expectVMSSHUp polls password SSH to the published guest port until whoami returns the guest user.
func expectVMSSHUp(h *e2e.Harness) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		out, err := h.RunSSHOnDeviceLocalPort(lifecycleVMSSHPort, lifecycleVMUser, lifecycleVMPassword, "/usr/bin/whoami")
		g.Expect(err).NotTo(HaveOccurred(), "password SSH to %s on published port %d failed", lifecycleVMApp, lifecycleVMSSHPort)
		g.Expect(strings.TrimSpace(out)).To(Equal(lifecycleVMUser))
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

// expectVirtLauncher polls podman ps for the virt-launcher container of appName, checking for running or not.
func expectVirtLauncher(h *e2e.Harness, appName string, running bool) {
	GinkgoHelper()
	pattern := fmt.Sprintf("virt-launcher-%s", appName)
	timeout := util.TIMEOUT
	matcher := ContainSubstring(pattern)
	msg := "virt-launcher for %s should be running"
	if !running {
		timeout = util.LONG_TIMEOUT
		matcher = Not(ContainSubstring(pattern))
		msg = "virt-launcher for %s should not be running"
	}
	Eventually(func(g Gomega) {
		names, err := h.RunPodmanPsContainerNames(false)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(names).To(matcher, msg, appName)
	}, timeout, util.POLLING).Should(Succeed())
}
