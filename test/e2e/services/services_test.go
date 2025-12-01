package services

import (
	"fmt"

	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("FlightCtl Services Installation and Status", Label("OCP-85998", "services"), func() {

	It("should validate flightctl-services installation and all components are running", func() {
		// Step 1: Verify RPM Package Installation
		By("verifying flightctl-services package is installed")
		installed, version, err := util.IsRPMInstalled("flightctl-services")
		Expect(err).ToNot(HaveOccurred())
		Expect(installed).To(BeTrue(), "flightctl-services package should be installed")
		Expect(version).ToNot(BeEmpty(), "package version should not be empty")
		GinkgoWriter.Printf("✓ flightctl-services package version: %s\n", version)

		By("verifying all required subpackages are installed")
		requiredPackages := []string{
			"flightctl-services",
			"flightctl-cli",
		}
		for _, pkg := range requiredPackages {
			installed, version, err := util.IsRPMInstalled(pkg)
			Expect(err).ToNot(HaveOccurred())
			Expect(installed).To(BeTrue(), fmt.Sprintf("%s package should be installed", pkg))
			GinkgoWriter.Printf("✓ %s package version: %s\n", pkg, version)
		}

		// Step 2: Verify Systemd Services
		By("verifying flightctl.target exists")
		exists, err := util.SystemdUnitExists("flightctl.target")
		Expect(err).ToNot(HaveOccurred())
		Expect(exists).To(BeTrue(), "flightctl.target should exist")

		By("listing all flightctl systemd units")
		units, err := util.ListSystemdUnits("flightctl*")
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("Found %d flightctl systemd units\n", len(units))
		for _, unit := range units {
			GinkgoWriter.Printf("  - %s\n", unit)
		}

		By("getting status of all flightctl services")
		status, err := util.GetSystemdStatus("flightctl*")
		Expect(err).ToNot(HaveOccurred())
		Expect(status).ToNot(BeEmpty())
		GinkgoWriter.Printf("FlightCtl services status preview:\n%s\n",
			util.TruncateString(status, 500))

		// Step 3: Verify Podman Containers
		By("listing all running podman containers")
		containers, err := util.ListPodmanContainers()
		Expect(err).ToNot(HaveOccurred())
		Expect(containers).ToNot(BeEmpty(), "should have at least one container running")
		GinkgoWriter.Printf("Found %d running containers\n", len(containers))

		By("verifying all expected containers are present and running")
		expectedContainers := []string{
			"flightctl-db",
			"flightctl-api",
			"flightctl-worker",
			"flightctl-periodic",
			"flightctl-ui",
			"flightctl-kv",
			"flightctl-alertmanager",
			"flightctl-alertmanager-proxy",
			"flightctl-alert-exporter",
			"flightctl-cli-artifacts",
		}

		containerMap := make(map[string]util.ContainerInfo)
		for _, container := range containers {
			containerMap[container.Name] = container
			GinkgoWriter.Printf("  - %s (status: %s, image: %s)\n",
				container.Name, container.Status, container.Image)
		}

		for _, expectedName := range expectedContainers {
			container, found := containerMap[expectedName]
			Expect(found).To(BeTrue(),
				fmt.Sprintf("container %s should be running", expectedName))
			Expect(container.Status).To(ContainSubstring("Up"),
				fmt.Sprintf("container %s should be in 'Up' status", expectedName))
		}

		By("verifying flightctl podman network exists")
		networkExists, err := util.PodmanNetworkExists("flightctl")
		Expect(err).ToNot(HaveOccurred())
		Expect(networkExists).To(BeTrue(), "flightctl podman network should exist")

		By("inspecting flightctl network configuration")
		networkInfo, err := util.InspectPodmanNetwork("flightctl")
		Expect(err).ToNot(HaveOccurred())
		Expect(networkInfo).ToNot(BeEmpty())
		GinkgoWriter.Printf("FlightCtl network configuration preview (first 300 chars):\n%s\n",
			util.TruncateString(networkInfo, 300))

		By("listing podman volumes")
		volumes, err := util.ListPodmanVolumes()
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("Found %d podman volumes\n", len(volumes))
		for _, volume := range volumes {
			GinkgoWriter.Printf("  - %s\n", volume)
		}

		// Step 4: Verify Service Connectivity
		By("verifying API service is accessible on port 3443")
		listening, err := util.IsPortListening("3443")
		Expect(err).ToNot(HaveOccurred())
		Expect(listening).To(BeTrue(), "API service should be listening on port 3443")

		By("verifying gRPC service is accessible on port 7443")
		listening, err = util.IsPortListening("7443")
		Expect(err).ToNot(HaveOccurred())
		Expect(listening).To(BeTrue(), "gRPC service should be listening on port 7443")

		By("verifying UI is accessible on port 443")
		listening, err = util.IsPortListening("443")
		Expect(err).ToNot(HaveOccurred())
		Expect(listening).To(BeTrue(), "UI should be listening on port 443")

		// Step 5: Collect System Information
		By("collecting OS information")
		osInfo, err := util.GetOSRelease()
		Expect(err).ToNot(HaveOccurred())
		Expect(osInfo).ToNot(BeEmpty())
		GinkgoWriter.Printf("OS Information preview (first 200 chars):\n%s\n",
			util.TruncateString(osInfo, 200))

		By("collecting kernel information")
		kernelInfo, err := util.GetKernelInfo()
		Expect(err).ToNot(HaveOccurred())
		Expect(kernelInfo).ToNot(BeEmpty())
		GinkgoWriter.Printf("Kernel: %s\n", kernelInfo)

		By("collecting SELinux status")
		selinuxStatus, err := util.GetSELinuxStatus()
		// Don't fail if SELinux is not available
		if err == nil {
			GinkgoWriter.Printf("SELinux status: %s\n", selinuxStatus)
		} else {
			GinkgoWriter.Printf("SELinux not available or not accessible\n")
		}
	})
})
