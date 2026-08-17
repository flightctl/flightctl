package vm_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	vmAppName                    = "test-vm"
	vmAppAName                   = "test-vm-a"
	vmAppBName                   = "test-vm-b"
	defaultVMImage               = "quay.io/containerdisks/fedora:40"
	vmGuestUser                  = "fedora"
	vmGuestPassword              = "fedora"
	vmCloudUser                  = "cloud-user"
	vmCloudUserPassword          = "cloud-user-pass"
	vmPublishedSSHPort           = 2222
	vmBPublishedSSHPort          = 2223
	vmBPublishedUDPPort          = 9090
	vmGuestMemory                = "1024M"
	configDriveIndexHTMLContent  = "Hello from ConfigDrive"
	systemdSubStateActive        = "active"
	systemdSubStateRunning       = "running"
	systemdLoadStateLoadedString = string(v1beta1.SystemdLoadStateLoaded)
	systemdActiveStateActive     = string(v1beta1.SystemdActiveStateActive)
)

func getVMImage() string {
	if image := os.Getenv("FLIGHTCTL_E2E_VM_IMAGE"); image != "" {
		return image
	}
	return defaultVMImage
}

var _ = Describe("VM Applications", Ordered, func() {
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

		By("Verifying the VM application reports Running status")
		err = harness.WaitForApplicationStatus(deviceID, vmAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)
		if err != nil {
			logVMApplicationUnitStatus(harness, vmAppName)
		}
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the applications summary is Healthy")
		err = harness.WaitForApplicationSummary(deviceID, testutil.LONG_TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		if err != nil {
			logVMApplicationUnitStatus(harness, vmAppName)
		}
		Expect(err).ToNot(HaveOccurred())

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
			[]string{fmt.Sprintf("%d:22", vmPublishedSSHPort)},
			e2e.VMYAMLWithConfigDrive(vmAppAName, vmGuestMemory, image, encodeConfigDriveUserData(configDriveCloudUserData(sshPublicKey, vmCloudUserPassword))),
		)
		Expect(err).ToNot(HaveOccurred())

		vmAppBSpec, err := e2e.NewVmApplicationSpecFromYAML(
			vmAppBName,
			[]string{
				fmt.Sprintf("%d:22/tcp", vmBPublishedSSHPort),
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
			By(fmt.Sprintf("Waiting for VM application %s to reach Running", appName))
			err = harness.WaitForApplicationStatus(deviceID, appName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)
			if err != nil {
				logVMApplicationUnitStatus(harness, appName)
			}
			Expect(err).ToNot(HaveOccurred())
		}

		By("Verifying the applications summary is Healthy")
		err = harness.WaitForApplicationSummary(deviceID, testutil.LONG_TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		if err != nil {
			logVMApplicationUnitStatus(harness, vmAppAName)
			logVMApplicationUnitStatus(harness, vmAppBName)
		}
		Expect(err).ToNot(HaveOccurred())

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
})

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

// vmApplicationID returns the production app ID used to namespace generated VM units.
func vmApplicationID(appName string) string {
	return lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
}

func encodeConfigDriveUserData(cloudConfig string) string {
	return base64.StdEncoding.EncodeToString([]byte(cloudConfig))
}

func configDriveCloudUserIdentityYAML(sshPublicKey, password string) string {
	return fmt.Sprintf(`ssh_pwauth: true
users:
  - name: %s
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    lock_passwd: false
    ssh_authorized_keys:
      - %s
chpasswd:
  expire: false
  users:
    - name: %s
      password: %s
      type: text`, vmCloudUser, sshPublicKey, vmCloudUser, password)
}

func configDriveCloudUserData(sshPublicKey, password string) string {
	return "#cloud-config\n" + configDriveCloudUserIdentityYAML(sshPublicKey, password) + "\n"
}

func configDriveCloudUserDataWithServices(sshPublicKey, password string) string {
	return fmt.Sprintf(`#cloud-config
%s
write_files:
  - path: /var/www/html/index.html
    content: %q
    owner: root:root
    permissions: '0644'
  - path: /usr/local/bin/hello-udp-listener.py
    owner: root:root
    permissions: '0755'
    content: |
      #!/usr/bin/env python3
      import socket

      PORT = %d

      sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
      sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
      sock.bind(("0.0.0.0", PORT))
      while True:
          _data, addr = sock.recvfrom(1024)
          sock.sendto(b"hello\n", addr)
  - path: /etc/systemd/system/hello-http.service
    owner: root:root
    permissions: '0644'
    content: |
      [Unit]
      Description=Hello HTTP service
      After=network-online.target
      Wants=network-online.target

      [Service]
      Type=simple
      WorkingDirectory=/var/www/html
      ExecStart=/usr/bin/python3 -m http.server 80
      Restart=on-failure

      [Install]
      WantedBy=multi-user.target
  - path: /etc/systemd/system/hello-udp.service
    owner: root:root
    permissions: '0644'
    content: |
      [Unit]
      Description=Hello UDP reply service
      After=network-online.target
      Wants=network-online.target

      [Service]
      Type=simple
      ExecStart=/usr/bin/python3 /usr/local/bin/hello-udp-listener.py
      Restart=on-failure

      [Install]
      WantedBy=multi-user.target
runcmd:
  - systemctl daemon-reload
  - systemctl enable --now hello-http.service
  - systemctl enable --now hello-udp.service
`, configDriveCloudUserIdentityYAML(sshPublicKey, password), configDriveIndexHTMLContent, vmBPublishedUDPPort)
}
