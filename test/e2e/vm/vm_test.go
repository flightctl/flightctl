package vm_test

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

//go:embed cloud-config-drive.yaml.tmpl
var configDriveCloudConfigTemplateText string

var configDriveCloudConfigTemplate = template.Must(
	template.New("cloud-config-drive").Funcs(template.FuncMap{
		"yamlQuote": strconv.Quote,
	}).Parse(configDriveCloudConfigTemplateText),
)

const (
	vmAppName                   = "test-vm"
	vmAppAName                  = "test-vm-a"
	vmAppBName                  = "test-vm-b"
	defaultVMImage              = "quay.io/containerdisks/fedora:40"
	defaultVMUpdatedImage       = "quay.io/containerdisks/fedora:41"
	vmGuestUser                 = e2e.VMFedoraGuestUser
	vmGuestPassword             = "fedora"
	vmModifiedGuestPassword     = "fedora-new"
	vmModifyBaselineGuestMemory = "1G"
	vmModifyBaselineCPUCores    = 1
	vmModifiedGuestMemory       = "2G"
	vmModifiedCPUCores          = 2
	// Guest-visible MemTotal for 2G is slightly below the requested quantity.
	vmModifiedGuestMemoryMinKiB      = 1700000
	vmCloudUser                      = "cloud-user"
	vmCloudUserPassword              = "cloud-user-pass"
	vmPublishedSSHPort               = 2222
	vmBPublishedSSHPort              = 2223
	vmPublishedPortUnavailableWindow = "10s"
	vmBPublishedUDPPort              = 9090
	vmGuestSSHPort                   = 22
	vmGuestMemory                    = "1024M"
	configDriveIndexHTMLContent      = "Hello from ConfigDrive"
	systemdSubStateActive            = "active"
	systemdSubStateRunning           = "running"
	systemdLoadStateLoadedString     = string(v1beta1.SystemdLoadStateLoaded)
	systemdActiveStateActive         = string(v1beta1.SystemdActiveStateActive)
)

func getVMImage() string {
	if image := os.Getenv("FLIGHTCTL_E2E_VM_IMAGE"); image != "" {
		return image
	}
	return defaultVMImage
}

func getVMUpdatedImage() string {
	if image := os.Getenv("FLIGHTCTL_E2E_VM_UPDATED_IMAGE"); image != "" {
		return image
	}
	return defaultVMUpdatedImage
}

var _ = Describe("VM Applications", Ordered, ContinueOnFailure, func() {
	var (
		deviceID  string
		harness   *e2e.Harness
		vmAppSpec v1beta1.ApplicationProviderSpec
	)

	BeforeAll(func() {
		var err error
		vmAppSpec, err = e2e.NewVmApplicationSpec(
			vmAppName,
			getVMImage(),
		)
		Expect(err).ToNot(HaveOccurred())
	})

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	It("deploys a VM application and reports Running status", Label("vm", "90228"), func() {
		By("Adding the VM application to the device")
		err := harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{vmAppSpec}
		})
		Expect(err).ToNot(HaveOccurred())
		waitForVMAppRunningHealthy(harness, deviceID, vmAppName)

		By("Waiting for the VM serial console login prompt")
		cs := harness.NewAppConsoleSessionWaitingForLogin(deviceID, vmAppName, testutil.LONG_TIMEOUT, testutil.POLLING)
		DeferCleanup(cs.Close)
		cs.MustSend(vmGuestUser)
		cs.MustExpectWithin(`(?i)password:`, testutil.DURATION_TIMEOUT, testutil.POLLING)
		cs.MustSend(vmGuestPassword)
		cs.MustExpectWithin(fmt.Sprintf(`.*%s@.*\$`, vmGuestUser), testutil.DURATION_TIMEOUT, testutil.POLLING)
		cs.MustSend(fmt.Sprintf("printf '<<whoami>>%%s<<whoami>>\\n' \"$(whoami)\""))
		cs.MustExpectWithin(fmt.Sprintf("<<whoami>>%s<<whoami>>", vmGuestUser), testutil.DURATION_TIMEOUT, testutil.POLLING)

		By("Disconnecting the serial console with ~.")
		cs.Disconnect()

		By("Verifying SSH via the published host port works")
		Eventually(func(g Gomega) {
			out, sshErr := harness.RunSSHOnDeviceLocalPort(vmPublishedSSHPort, vmGuestUser, vmGuestPassword, "hostname")
			g.Expect(sshErr).NotTo(HaveOccurred(), "SSH to published port %d failed", vmPublishedSSHPort)
			g.Expect(strings.TrimSpace(out)).NotTo(BeEmpty())
		}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
	})

	// Unexpected virt-launcher compute death (not flightctl app stop) should restart via
	// pod/systemd policy and return the app to Running/Healthy without operator start.
	// After recovery, serial console exclusivity is checked: a second connect without
	// --force is rejected, and --force takes over the active session.
	It("recovers Running and Healthy after an unexpected virt-launcher compute crash", Label("vm", "90232", "90239"), func() {
		By("Deploying the VM application")
		err := harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{vmAppSpec}
		})
		Expect(err).ToNot(HaveOccurred())
		waitForVMAppRunningHealthy(harness, deviceID, vmAppName)

		By("Verifying SSH before the crash")
		expectSSHWhoamiWithPassword(harness, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)

		computeContainer := findRunningVMComputeContainerName(harness, vmAppName)
		containerIDBefore := getPodmanContainerID(harness, computeContainer)
		GinkgoWriter.Printf("Pre-crash compute container %q ID %q\n", computeContainer, containerIDBefore)

		By("Force-killing the virt-launcher compute container")
		_, err = harness.VM.RunSSH([]string{"sudo", "podman", "kill", computeContainer}, nil)
		Expect(err).NotTo(HaveOccurred(), "killing podman container %q", computeContainer)

		By("Waiting for the compute container to be recreated")
		var computeContainerAfter, containerIDAfter string
		Eventually(func(g Gomega) {
			name, lookupErr := runningVMComputeContainerName(harness, vmAppName)
			g.Expect(lookupErr).NotTo(HaveOccurred(), "finding running compute container")
			id, idErr := podmanContainerID(harness, name)
			g.Expect(idErr).NotTo(HaveOccurred(), "reading podman container ID for %q", name)
			g.Expect(id).NotTo(Equal(containerIDBefore), "compute container should be recreated after crash")
			computeContainerAfter = name
			containerIDAfter = id
		}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
		GinkgoWriter.Printf("Post-recovery compute container %q ID %q\n", computeContainerAfter, containerIDAfter)

		By("Waiting for the VM application to recover to Running/Healthy without operator start")
		waitForVMAppRunningHealthy(harness, deviceID, vmAppName)

		By("Verifying SSH works again after recovery")
		expectSSHWhoamiWithPassword(harness, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)

		expectSerialConsoleForceTakeover(harness, deviceID, vmAppName, vmGuestUser, vmGuestPassword)
	})

	// Two VM apps on one device without conflict. Both use KubeVirt ConfigDrive
	// (userDataBase64). test-vm-a maps 2222:22; test-vm-b maps 2223:22/tcp and
	// 9090:9090/udp. Verifies concurrent Running/Healthy state, cloud-user login
	// via password, ssh_authorized_keys in ConfigDrive userdata, write_files/runcmd
	// on test-vm-b (python3 http.server, index.html), TCP/UDP publishPorts, and that
	// stopping one VM leaves the other reachable.
	It("runs two VM applications concurrently with TCP and UDP publishPorts", Label("vm", "90230", "90235"), func() {
		sshPublicKey, _, sshKeyCleanup, err := testutil.GenerateTempSSHKeyPair()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(sshKeyCleanup)

		image := getVMImage()
		vmAppASpec, err := e2e.NewVmApplicationSpecFromYAML(
			vmAppAName,
			[]string{fmt.Sprintf("%d:%d", vmPublishedSSHPort, vmGuestSSHPort)},
			e2e.VMYAMLWithConfigDrive(vmAppAName, vmGuestMemory, image, encodeConfigDriveUserData(configDriveCloudUserData(sshPublicKey, vmCloudUserPassword))),
		)
		Expect(err).ToNot(HaveOccurred())

		vmAppBSpec, err := e2e.NewVmApplicationSpecFromYAML(
			vmAppBName,
			[]string{
				fmt.Sprintf("%d:%d/tcp", vmBPublishedSSHPort, vmGuestSSHPort),
				fmt.Sprintf("%d:%d/udp", vmBPublishedUDPPort, vmBPublishedUDPPort),
			},
			e2e.VMYAMLWithConfigDrive(vmAppBName, vmGuestMemory, image, encodeConfigDriveUserData(configDriveCloudUserDataWithServices(sshPublicKey, vmCloudUserPassword))),
		)
		Expect(err).ToNot(HaveOccurred())

		By("Adding both VM applications to the device")
		err = harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{vmAppASpec, vmAppBSpec}
		})
		Expect(err).ToNot(HaveOccurred())

		for _, appName := range []string{vmAppAName, vmAppBName} {
			waitForVMAppRunningHealthy(harness, deviceID, appName)
		}

		By("Verifying SSH via published host ports for both VMs")
		expectSSHWhoamiWithPassword(harness, vmPublishedSSHPort, vmAppAName, vmCloudUser, vmCloudUserPassword)
		expectSSHWhoamiWithPassword(harness, vmBPublishedSSHPort, vmAppBName, vmCloudUser, vmCloudUserPassword)

		expectAuthorizedKeyPresent := func(port int, appName string) {
			Eventually(func(g Gomega) {
				out, sshErr := harness.RunSSHOnDeviceLocalPort(
					port,
					vmCloudUser,
					vmCloudUserPassword,
					fmt.Sprintf(`bash -lc 'grep -F %q ~/.ssh/authorized_keys'`, sshPublicKey),
				)
				g.Expect(sshErr).NotTo(HaveOccurred(), "checking authorized_keys on %s failed", appName)
				g.Expect(strings.TrimSpace(out)).To(Equal(sshPublicKey))
			}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
		}

		By("Verifying ssh_authorized_keys from ConfigDrive are present on both VMs")
		expectAuthorizedKeyPresent(vmPublishedSSHPort, vmAppAName)
		expectAuthorizedKeyPresent(vmBPublishedSSHPort, vmAppBName)

		By("Verifying ConfigDrive write_files and runcmd on test-vm-b")
		Eventually(func(g Gomega) {
			out, sshErr := harness.RunSSHOnDeviceLocalPort(vmBPublishedSSHPort, vmCloudUser, vmCloudUserPassword, `bash -lc 'systemctl is-active hello-http.service'`)
			g.Expect(sshErr).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal("active"))
		}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())

		Eventually(func(g Gomega) {
			out, sshErr := harness.RunSSHOnDeviceLocalPort(
				vmBPublishedSSHPort,
				vmCloudUser,
				vmCloudUserPassword,
				`bash -lc 'command -v python3'`,
			)
			g.Expect(sshErr).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(ContainSubstring("python3"))
		}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())

		Eventually(func(g Gomega) {
			out, sshErr := harness.RunSSHOnDeviceLocalPort(
				vmBPublishedSSHPort,
				vmCloudUser,
				vmCloudUserPassword,
				`bash -lc 'cat /var/www/html/index.html'`,
			)
			g.Expect(sshErr).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal(configDriveIndexHTMLContent))
		}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())

		Eventually(func(g Gomega) {
			out, sshErr := harness.RunSSHOnDeviceLocalPort(
				vmBPublishedSSHPort,
				vmCloudUser,
				vmCloudUserPassword,
				`bash -lc 'curl -sS http://127.0.0.1/'`,
			)
			g.Expect(sshErr).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal(configDriveIndexHTMLContent))
		}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())

		Eventually(func(g Gomega) {
			out, sshErr := harness.RunSSHOnDeviceLocalPort(vmBPublishedSSHPort, vmCloudUser, vmCloudUserPassword, `bash -lc 'systemctl is-active hello-udp.service'`)
			g.Expect(sshErr).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal("active"))
		}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())

		By("Verifying UDP hello service inside test-vm-b")
		expectGuestUDPHello(harness, vmBPublishedSSHPort, vmBPublishedUDPPort, vmAppBName)

		By("Verifying published UDP port on the device host")
		expectPublishedUDPHello(harness, vmBPublishedUDPPort, vmAppBName, "hello")

		By(fmt.Sprintf("Stopping %s and verifying %s remains reachable", vmAppAName, vmAppBName))
		_, err = harness.CLI("app", "stop", fmt.Sprintf("device/%s", deviceID), "--name", vmAppAName, "-y")
		Expect(err).ToNot(HaveOccurred())

		err = harness.WaitForApplicationStatus(deviceID, vmAppAName, v1beta1.ApplicationStatusStopped, testutil.LONG_TIMEOUT, testutil.POLLING)
		if err != nil {
			logVMApplicationUnitStatus(harness, vmAppAName)
		}
		Expect(err).ToNot(HaveOccurred())

		err = harness.WaitForApplicationStatus(deviceID, vmAppBName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)
		if err != nil {
			logVMApplicationUnitStatus(harness, vmAppBName)
		}
		Expect(err).ToNot(HaveOccurred())

		expectSSHWhoamiWithPassword(harness, vmBPublishedSSHPort, vmAppBName, vmCloudUser, vmCloudUserPassword)
	})

	// Changing VM sizing, publishPorts, containerdisk image, and cloud-init password in the
	// device spec re-renders the app; the VM returns to Running/Healthy with observable updates.
	It("re-renders and applies modified VM memory, cores, publishPorts, disk image, and cloud-init password", Label("vm", "90233"), func() {
		baselineImage := getVMImage()
		updatedImage := getVMUpdatedImage()

		baselineSpec, err := e2e.NewVmApplicationSpecFromYAML(
			vmAppName,
			[]string{fmt.Sprintf("%d:%d", vmPublishedSSHPort, vmGuestSSHPort)},
			e2e.VMYAMLWithCPU(vmAppName, vmModifyBaselineGuestMemory, baselineImage, vmModifyBaselineCPUCores, e2e.VMFedoraNoCloudUserData(vmGuestPassword)),
		)
		Expect(err).ToNot(HaveOccurred())

		modifiedSpec, err := e2e.NewVmApplicationSpecFromYAML(
			vmAppName,
			[]string{fmt.Sprintf("%d:%d", vmBPublishedSSHPort, vmGuestSSHPort)},
			e2e.VMYAMLWithCPU(vmAppName, vmModifiedGuestMemory, updatedImage, vmModifiedCPUCores, e2e.VMFedoraNoCloudUserData(vmModifiedGuestPassword)),
		)
		Expect(err).ToNot(HaveOccurred())

		By("Deploying the baseline VM application")
		err = harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{baselineSpec}
		})
		Expect(err).ToNot(HaveOccurred())
		waitForVMAppRunningHealthy(harness, deviceID, vmAppName)

		By("Verifying baseline SSH on the original published port")
		expectSSHWhoamiWithPassword(harness, vmPublishedSSHPort, vmAppName, vmGuestUser, vmGuestPassword)

		By("Applying the modified VM application spec")
		err = harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{modifiedSpec}
		})
		Expect(err).ToNot(HaveOccurred())
		waitForVMAppRunningHealthy(harness, deviceID, vmAppName)

		By("Verifying SSH on the new published port with the updated password")
		expectSSHWhoamiWithPassword(harness, vmBPublishedSSHPort, vmAppName, vmGuestUser, vmModifiedGuestPassword)

		By("Verifying SSH on the previous published port is removed")
		expectSSHUnavailableOnPort(harness, vmPublishedSSHPort, vmAppName, vmGuestUser, vmModifiedGuestPassword)

		By("Verifying guest CPU and memory reflect the updated spec")
		expectGuestCPUCount(harness, vmBPublishedSSHPort, vmAppName, vmGuestUser, vmModifiedGuestPassword, vmModifiedCPUCores)
		expectGuestMemoryAtLeastKiB(harness, vmBPublishedSSHPort, vmAppName, vmGuestUser, vmModifiedGuestPassword, vmModifiedGuestMemoryMinKiB)

		By("Verifying the rendered quadlet workload references the updated containerdisk image")
		expectVMQuadletDirContainsImage(harness, vmAppName, updatedImage)

		By("Verifying serial console login with the updated cloud-init password")
		expectSerialLoginWithPassword(harness, deviceID, vmAppName, vmGuestUser, vmModifiedGuestPassword)
	})
})

func waitForVMAppRunningHealthy(h *e2e.Harness, deviceID, appName string) {
	GinkgoHelper()
	By(fmt.Sprintf("Waiting for VM application %s to reach Running", appName))
	err := h.WaitForApplicationStatus(deviceID, appName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)
	if err != nil {
		logVMApplicationUnitStatus(h, appName)
	}
	Expect(err).ToNot(HaveOccurred())

	By("Verifying the applications summary is Healthy")
	err = h.WaitForApplicationSummary(deviceID, testutil.LONG_TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
	if err != nil {
		logVMApplicationUnitStatus(h, appName)
	}
	Expect(err).ToNot(HaveOccurred())
}

// expectLoginPromptThenPasswordSSH waits until the guest serial login prompt appears,
// then checks published-port SSH. The prompt means getty is up; it does not log in.
// Use after a first start or a stop/start. Skip when the guest was already running.
func expectLoginPromptThenPasswordSSH(h *e2e.Harness, deviceID, appName, user, password string, port int) {
	GinkgoHelper()
	cs := h.NewAppConsoleSessionWaitingForLogin(deviceID, appName, testutil.LONG_TIMEOUT, testutil.POLLING)
	cs.Disconnect()
	cs.Close()
	expectSSHWhoamiWithPassword(h, port, appName, user, password)
}

// expectSSHWhoamiWithPassword polls password SSH to a published host port until whoami returns the guest user.
func expectSSHWhoamiWithPassword(h *e2e.Harness, port int, appName, user, password string) {
	GinkgoHelper()
	const remoteCmd = "/usr/bin/whoami"
	Eventually(func(g Gomega) {
		GinkgoWriter.Printf(
			"Password SSH probe on device host to %s@127.0.0.1:%d running %s (app=%s)\n",
			user, port, remoteCmd, appName,
		)
		out, sshErr := h.RunSSHOnDeviceLocalPort(port, user, password, remoteCmd)
		if sshErr != nil {
			GinkgoWriter.Printf("SSH failed for %s on port %d: %v\n", appName, port, sshErr)
		} else {
			GinkgoWriter.Printf("SSH output for %s on port %d: %q\n", appName, port, strings.TrimSpace(out))
		}
		g.Expect(sshErr).NotTo(HaveOccurred(), "password SSH to %s on published port %d failed", appName, port)
		g.Expect(strings.TrimSpace(out)).To(Equal(user))
	}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
}

// expectSSHUnavailableOnPort polls until password SSH on port fails, then verifies it stays unavailable.
func expectSSHUnavailableOnPort(h *e2e.Harness, port int, appName, user, password string) {
	GinkgoHelper()
	const remoteCmd = "/usr/bin/whoami"
	Eventually(func(g Gomega) {
		_, sshErr := h.RunSSHOnDeviceLocalPort(port, user, password, remoteCmd)
		g.Expect(sshErr).To(HaveOccurred(), "SSH to %s on published port %d should be unavailable", appName, port)
		g.Expect(errors.Is(sshErr, e2e.ErrSSHConnectionRefused) || errors.Is(sshErr, e2e.ErrSSHTimeout)).
			To(BeTrue(), "SSH to %s on port %d failed with %v, want connection refused or timeout", appName, port, sshErr)
	}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())

	Consistently(func(g Gomega) {
		_, sshErr := h.RunSSHOnDeviceLocalPort(port, user, password, remoteCmd)
		g.Expect(sshErr).To(HaveOccurred(), "SSH to %s on published port %d should remain unavailable", appName, port)
		g.Expect(errors.Is(sshErr, e2e.ErrSSHConnectionRefused) || errors.Is(sshErr, e2e.ErrSSHTimeout)).
			To(BeTrue(), "SSH to %s on port %d failed with %v, want connection refused or timeout", appName, port, sshErr)
	}, vmPublishedPortUnavailableWindow, testutil.POLLING).Should(Succeed())
}

// runningVMComputeContainerName finds the running virt-launcher compute container name for appName.
func runningVMComputeContainerName(h *e2e.Harness, appName string) (string, error) {
	pattern := fmt.Sprintf("virt-launcher-%s-compute", appName)
	out, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "{{.Names}}"}, nil)
	if err != nil {
		return "", fmt.Errorf("listing running podman containers: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, pattern) {
			return line, nil
		}
	}
	return "", fmt.Errorf("running compute container matching %q not found", pattern)
}

// findRunningVMComputeContainerName returns the running virt-launcher compute container name for appName.
func findRunningVMComputeContainerName(h *e2e.Harness, appName string) string {
	GinkgoHelper()
	var containerName string
	Eventually(func(g Gomega) {
		var err error
		containerName, err = runningVMComputeContainerName(h, appName)
		g.Expect(err).NotTo(HaveOccurred())
	}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
	return containerName
}

// podmanContainerID returns the running podman container ID for containerName on the device.
func podmanContainerID(h *e2e.Harness, containerName string) (string, error) {
	out, err := h.VM.RunSSH([]string{
		"sudo", "podman", "ps",
		"--filter", "name=^" + regexp.QuoteMeta(containerName) + "$",
		"--format", "{{.ID}}",
	}, nil)
	if err != nil {
		return "", fmt.Errorf("reading podman container ID for %q: %w", containerName, err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "", fmt.Errorf("podman container ID for %q not found", containerName)
	}
	return strings.TrimSpace(lines[0]), nil
}

// getPodmanContainerID returns the running podman container ID for containerName on the device.
func getPodmanContainerID(h *e2e.Harness, containerName string) string {
	GinkgoHelper()
	id, err := podmanContainerID(h, containerName)
	Expect(err).NotTo(HaveOccurred())
	return id
}

func expectGuestCPUCount(h *e2e.Harness, port int, appName, user, password string, wantCPUs int) {
	GinkgoHelper()
	want := strconv.Itoa(wantCPUs)
	Eventually(func(g Gomega) {
		out, sshErr := h.RunSSHOnDeviceLocalPort(port, user, password, "nproc")
		g.Expect(sshErr).NotTo(HaveOccurred(), "reading nproc on %s failed", appName)
		g.Expect(strings.TrimSpace(out)).To(Equal(want))
	}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
}

func expectGuestMemoryAtLeastKiB(h *e2e.Harness, port int, appName, user, password string, minKiB int) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		out, sshErr := h.RunSSHOnDeviceLocalPort(
			port,
			user,
			password,
			`bash -lc 'grep MemTotal /proc/meminfo | tr -s " " | cut -d" " -f2'`,
		)
		g.Expect(sshErr).NotTo(HaveOccurred(), "reading MemTotal on %s failed", appName)
		memKiB, parseErr := strconv.Atoi(strings.TrimSpace(out))
		g.Expect(parseErr).NotTo(HaveOccurred(), "parsing MemTotal output %q", out)
		g.Expect(memKiB).To(BeNumerically(">=", minKiB), "guest MemTotal on %s", appName)
	}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
}

func expectVMQuadletDirContainsImage(h *e2e.Harness, appName, imageRef string) {
	GinkgoHelper()
	computeContainerFile := vmApplicationComputeContainerFileName(appName)
	filePath := fmt.Sprintf("%s/%s/%s", e2e.QuadletUnitPath, appName, computeContainerFile)
	expectedSource := fmt.Sprintf("source=%s", imageRef)
	Eventually(func(g Gomega) {
		out, err := h.VM.RunSSH([]string{"sudo", "cat", filePath}, nil)
		g.Expect(err).NotTo(HaveOccurred(), "reading compute quadlet workload %q", filePath)
		g.Expect(out.String()).To(ContainSubstring(expectedSource), "containerdisk image in %q", filePath)
	}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
}

func expectSerialLoginWithPassword(h *e2e.Harness, deviceID, appName, user, password string) {
	GinkgoHelper()
	cs := h.NewAppConsoleSessionWaitingForLogin(deviceID, appName, testutil.LONG_TIMEOUT, testutil.POLLING)
	DeferCleanup(cs.Close)
	loginOnSerialConsole(cs, user, password)
	cs.Disconnect()
}

// loginOnSerialConsole completes a password login on an already-open serial session
// and checks whoami. The session is left connected.
func loginOnSerialConsole(cs *e2e.ConsoleSession, user, password string) {
	GinkgoHelper()
	cs.MustSend(user)
	cs.MustExpectWithin(`(?i)password:`, testutil.DURATION_TIMEOUT, testutil.POLLING)
	cs.MustSend(password)
	cs.MustExpectWithin(fmt.Sprintf(`.*%s@.*\$`, user), testutil.DURATION_TIMEOUT, testutil.POLLING)
	cs.MustSend(fmt.Sprintf("printf '<<whoami>>%%s<<whoami>>\\n' \"$(whoami)\""))
	cs.MustExpectWithin(fmt.Sprintf("<<whoami>>%s<<whoami>>", user), testutil.DURATION_TIMEOUT, testutil.POLLING)
}

// expectSerialConsoleForceTakeover verifies serial login, then that a second connect
// without --force is rejected and --force replaces the first session.
func expectSerialConsoleForceTakeover(h *e2e.Harness, deviceID, appName, user, password string) {
	GinkgoHelper()
	By("Verifying serial console login works")
	cs1 := h.NewAppConsoleSessionWaitingForLogin(deviceID, appName, testutil.LONG_TIMEOUT, testutil.POLLING)
	DeferCleanup(cs1.Close)
	loginOnSerialConsole(cs1, user, password)

	By("Rejecting a second serial console without --force")
	out, err := h.CLI(
		"app", "console",
		fmt.Sprintf("device/%s", deviceID),
		"--name", appName,
		"--type", "serial",
	)
	Expect(err).To(HaveOccurred())
	Expect(out).To(ContainSubstring(fmt.Sprintf("serial console session already active for application %s", appName)))

	By("Logging out of the guest so the replacement session sees a login prompt")
	cs1.MustSend("exit")
	cs1.MustExpectWithin(`(?i)login:`, testutil.DURATION_TIMEOUT, testutil.POLLING)

	By("Taking over the serial console with --force")
	cs2 := h.NewAppConsoleSession(deviceID, appName, "serial", "--force")
	DeferCleanup(cs2.Close)

	By("Waiting for the first session to disconnect with a replacement message")
	Eventually(func(g Gomega) {
		g.Expect(string(cs1.Stdout.Contents())).To(ContainSubstring("console session replaced by a new connection"))
	}, testutil.DURATION_TIMEOUT, testutil.POLLING).Should(Succeed())
	Eventually(cs1.Stdout.Closed).WithTimeout(testutil.DURATION_TIMEOUT).WithPolling(testutil.POLLING).Should(BeTrue())

	By("Verifying the replacement serial session is usable")
	_, err = io.WriteString(cs2.Stdin, "\n")
	Expect(err).NotTo(HaveOccurred())
	cs2.MustExpectWithin(`(?i)login:`, testutil.DURATION_TIMEOUT, testutil.POLLING)
	loginOnSerialConsole(cs2, user, password)
}

// expectGuestUDPHello polls a UDP probe inside the guest via SSH to a published TCP port.
func expectGuestUDPHello(h *e2e.Harness, sshPort, udpPort int, appName string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		probeCmd := e2e.UDPProbePythonCommand(udpPort)
		GinkgoWriter.Printf(
			"Guest UDP probe via SSH on %s@127.0.0.1:%d running %q (app=%s)\n",
			vmCloudUser, sshPort, probeCmd, appName,
		)
		out, sshErr := h.RunSSHOnDeviceLocalPort(sshPort, vmCloudUser, vmCloudUserPassword, probeCmd)
		if sshErr != nil {
			GinkgoWriter.Printf("Guest UDP probe failed for %s on guest port %d: %v\n", appName, udpPort, sshErr)
		} else {
			GinkgoWriter.Printf("Guest UDP probe output for %s on guest port %d: %q\n", appName, udpPort, strings.TrimSpace(out))
		}
		g.Expect(sshErr).NotTo(HaveOccurred(), "guest UDP probe for %s on port %d failed", appName, udpPort)
		g.Expect(strings.TrimSpace(out)).To(Equal("hello"))
	}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
}

// expectPublishedUDPHello polls a UDP probe on the device host published port until the expected reply is received.
func expectPublishedUDPHello(h *e2e.Harness, port int, appName, expectedReply string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		GinkgoWriter.Printf(
			"UDP probe: socat/python3 send b\"ping\" recv UDP4:127.0.0.1:%d (app=%s)\n",
			port, appName,
		)
		out, udpErr := h.RunUDPProbeOnDeviceLocalPort(port)
		if udpErr != nil {
			GinkgoWriter.Printf("UDP probe failed for %s on port %d: %v\n", appName, port, udpErr)
		} else {
			GinkgoWriter.Printf("UDP probe output for %s on port %d: %q\n", appName, port, out)
		}
		g.Expect(udpErr).NotTo(HaveOccurred(), "UDP probe to %s on published port %d failed", appName, port)
		g.Expect(out).To(Equal(expectedReply))
	}, testutil.LONG_TIMEOUT, testutil.POLLING).Should(Succeed())
}

// logVMApplicationUnitStatus logs generated systemd unit state when product-level VM app status checks fail.
func logVMApplicationUnitStatus(h *e2e.Harness, appName string) {
	if appName == "" {
		GinkgoWriter.Println("VM application unit diagnostics skipped: app name is empty")
		return
	}
	units, err := vmApplicationUnitStatus(h, appName)
	if err != nil {
		GinkgoWriter.Printf("VM application %s unit diagnostics failed: %v\n", appName, err)
		return
	}
	if len(units) == 0 {
		GinkgoWriter.Printf("VM application %s unit diagnostics found no matching systemd units\n", appName)
		return
	}
	running, details := vmApplicationUnitsRunningStatus(units, appName)
	GinkgoWriter.Printf("VM application %s unit readiness=%t: %s\n", appName, running, details)
}

// vmApplicationUnitPatterns returns the generated systemd unit patterns for a VM app.
func vmApplicationUnitPatterns(appName string) []string {
	if appName == "" {
		return nil
	}
	return []string{
		vmApplicationTargetUnitName(appName),
		vmApplicationComputeServiceName(appName),
	}
}

// vmApplicationUnitStatus returns the live systemd units generated for a VM app.
func vmApplicationUnitStatus(h *e2e.Harness, appName string) ([]e2e.SystemdUnitState, error) {
	patterns := vmApplicationUnitPatterns(appName)
	if len(patterns) == 0 {
		return nil, fmt.Errorf("VM application unit patterns are empty for app %q", appName)
	}
	units, _, err := h.ListSystemdUnitsOnVM(patterns...)
	if err != nil {
		return nil, err
	}
	return units, nil
}

// vmApplicationUnitsRunning reports whether the VM app target is active and the compute service is running.
func vmApplicationUnitsRunning(units []e2e.SystemdUnitState, appName string) bool {
	running, _ := vmApplicationUnitsRunningStatus(units, appName)
	return running
}

// vmApplicationUnitsRunningStatus reports VM app readiness and explains missing or mismatched unit state.
func vmApplicationUnitsRunningStatus(units []e2e.SystemdUnitState, appName string) (bool, string) {
	targetRunning, targetDetails := vmApplicationUnitHasState(units, vmApplicationTargetUnitName(appName), systemdLoadStateLoadedString, systemdActiveStateActive, systemdSubStateActive)
	computeRunning, computeDetails := vmApplicationUnitHasState(units, vmApplicationComputeServiceName(appName), systemdLoadStateLoadedString, systemdActiveStateActive, systemdSubStateRunning)
	if targetRunning && computeRunning {
		return true, "target and compute service have required states"
	}
	return false, fmt.Sprintf("%s; %s; matching units: %s", targetDetails, computeDetails, vmApplicationFormatUnits(units))
}

// vmApplicationUnitHasState reports whether a named unit has the expected load, active, and sub states.
func vmApplicationUnitHasState(units []e2e.SystemdUnitState, unitName string, loadState string, activeState string, subState string) (bool, string) {
	for _, unit := range units {
		if unit.Unit != unitName {
			continue
		}
		if unit.LoadState == loadState &&
			unit.ActiveState == activeState &&
			unit.SubState == subState {
			return true, fmt.Sprintf("%s has required state", unitName)
		}
		return false, fmt.Sprintf("%s has load=%q active=%q sub=%q, want load=%q active=%q sub=%q", unitName, unit.LoadState, unit.ActiveState, unit.SubState, loadState, activeState, subState)
	}
	return false, fmt.Sprintf("%s is missing, want load=%q active=%q sub=%q", unitName, loadState, activeState, subState)
}

// vmApplicationFormatUnits returns compact diagnostic text for matching systemd units.
func vmApplicationFormatUnits(units []e2e.SystemdUnitState) string {
	if len(units) == 0 {
		return "none"
	}
	formatted := make([]string, 0, len(units))
	for _, unit := range units {
		formatted = append(formatted, fmt.Sprintf("%s(load=%s active=%s sub=%s)", unit.Unit, unit.LoadState, unit.ActiveState, unit.SubState))
	}
	return strings.Join(formatted, ", ")
}

// vmApplicationTargetUnitName returns the exact generated Flight Control target unit name for a VM app.
func vmApplicationTargetUnitName(appName string) string {
	return quadlet.NamespaceResource(vmApplicationID(appName), lifecycle.QuadletTargetName)
}

// vmApplicationComputeServiceName returns the generated virt-launcher compute service name for a VM app.
func vmApplicationComputeServiceName(appName string) string {
	return quadlet.NamespaceResource(vmApplicationID(appName), fmt.Sprintf("virt-launcher-%s-compute.service", appName))
}

// vmApplicationComputeContainerFileName returns the generated virt-launcher compute container quadlet file name.
func vmApplicationComputeContainerFileName(appName string) string {
	return quadlet.NamespaceResource(vmApplicationID(appName), fmt.Sprintf("virt-launcher-%s-compute.container", appName))
}

// vmApplicationID returns the production app ID used to namespace generated VM units.
func vmApplicationID(appName string) string {
	return lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
}

func encodeConfigDriveUserData(cloudConfig string) string {
	return base64.StdEncoding.EncodeToString([]byte(cloudConfig))
}

type configDriveCloudConfigParams struct {
	User            string
	Password        string
	SSHPublicKey    string
	FaillockCommand string
	WithServices    bool
	IndexHTML       string
	UDPPort         int
}

func renderConfigDriveCloudUserData(sshPublicKey, password string, withServices bool) string {
	var buf bytes.Buffer
	params := configDriveCloudConfigParams{
		User:            vmCloudUser,
		Password:        password,
		SSHPublicKey:    sshPublicKey,
		FaillockCommand: e2e.VMGuestDisableFaillockCommand(vmCloudUser),
		WithServices:    withServices,
		IndexHTML:       configDriveIndexHTMLContent,
		UDPPort:         vmBPublishedUDPPort,
	}
	if err := configDriveCloudConfigTemplate.Execute(&buf, params); err != nil {
		panic("rendering cloud-config-drive: " + err.Error())
	}
	return buf.String()
}

func configDriveCloudUserData(sshPublicKey, password string) string {
	return renderConfigDriveCloudUserData(sshPublicKey, password, false)
}

func configDriveCloudUserDataWithServices(sshPublicKey, password string) string {
	return renderConfigDriveCloudUserData(sshPublicKey, password, true)
}
