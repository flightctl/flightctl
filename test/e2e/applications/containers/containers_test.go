package containers_test

import (
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	// Container application constants
	containerAppName  = "container-app"
	containerAppName2 = "container-app-2"
	nginxImage        = "quay.io/flightctl-tests/nginx:1.28-alpine-slim"

	hostPort      = "8080"
	containerPort = "80"

	defaultCPU    = "4"
	defaultMemory = "256m"
	lowMemory     = "1m"
)

var _ = Describe("Single Container Applications", Ordered, func() {
	var (
		deviceId string
		harness  *e2e.Harness

		defaultAppSpec   v1beta1.ApplicationProviderSpec
		lowMemoryAppSpec v1beta1.ApplicationProviderSpec
		secondAppSpec    v1beta1.ApplicationProviderSpec
	)

	BeforeAll(func() {
		defaultPorts := []v1beta1.ApplicationPort{getPortMapping()}
		secondPorts := []v1beta1.ApplicationPort{"8081:80"}

		var err error
		defaultAppSpec, err = e2e.NewContainerApplicationSpec(
			containerAppName, nginxImage, defaultPorts,
			lo.ToPtr(defaultCPU), lo.ToPtr(defaultMemory), nil,
		)
		Expect(err).ToNot(HaveOccurred())

		lowMemoryAppSpec, err = e2e.NewContainerApplicationSpec(
			containerAppName, nginxImage, defaultPorts,
			lo.ToPtr(defaultCPU), lo.ToPtr(lowMemory), nil,
		)
		Expect(err).ToNot(HaveOccurred())

		secondAppSpec, err = e2e.NewContainerApplicationSpec(
			containerAppName2, nginxImage, secondPorts,
			lo.ToPtr(defaultCPU), lo.ToPtr(defaultMemory), nil,
		)
		Expect(err).ToNot(HaveOccurred())
	})

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	It("Verifies that a Single Container Application can be deployed to an Edge Manager device", Label("86285", "sanity"), func() {
		By("Adding the container application to the device")
		err := harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
			GinkgoWriter.Printf("Updating device %s with container application %s\n", deviceId, containerAppName)
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the container is running and the application is deployed as expected")
		harness.VerifyContainerRunning("nginx")
		harness.WaitForApplicationRunningStatus(deviceId, containerAppName)
		harness.WaitForApplicationsSummaryStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)
		harness.VerifyQuadletApplicationFolderExists(containerAppName)

		By("Verifying the container is listening on the mapped port")
		containerPorts, err := harness.GetContainerPorts()
		Expect(err).ToNot(HaveOccurred())
		Expect(containerPorts).To(ContainSubstring(hostPort))

		By("Verifying CPU limit is written to quadlet file")
		harness.VerifyQuadletPodmanArgs(containerAppName, "--cpus", defaultCPU)

		By("Updating the Application memory limit to a low value")
		err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{lowMemoryAppSpec}
			GinkgoWriter.Printf("Updating container application %s with low memory limit %s\n", containerAppName, lowMemory)
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying application status becomes Error due to low memory limit")
		harness.WaitForApplicationStatusByName(deviceId, containerAppName, v1beta1.ApplicationStatusError)

		By("Restoring normal memory limit")
		err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
			GinkgoWriter.Printf("Restoring container application %s with normal memory limit %s\n", containerAppName, defaultMemory)
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying application recovers to Running status with Healthy summary")
		harness.WaitForApplicationRunningStatus(deviceId, containerAppName)
		harness.WaitForApplicationsSummaryStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)

		By("Adding a second container application alongside the first")
		err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			apps := []v1beta1.ApplicationProviderSpec{defaultAppSpec, secondAppSpec}
			device.Spec.Applications = &apps
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying both applications are running")
		harness.WaitForRunningApplicationsCount(deviceId, 2)

		By("Verifying both containers are running")
		err = harness.VerifyContainerCount(2)
		Expect(err).ToNot(HaveOccurred())

		By("Removing all applications from the device")
		err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
			GinkgoWriter.Printf("Removing all applications from device %s\n", deviceId)
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying no containers or applications are running after cleanup")
		err = harness.VerifyContainerCount(0)
		Expect(err).ToNot(HaveOccurred())
		harness.WaitForNoApplications(deviceId)
		harness.VerifyQuadletApplicationFolderDeleted(containerAppName)
	})
})

func getPortMapping() string {
	return fmt.Sprintf("%s:%s", hostPort, containerPort)
}
