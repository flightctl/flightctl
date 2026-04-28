package containers_test

import (
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	// Container application constants
	containerAppName  = "container-app"
	containerAppName2 = "container-app-2"
	nginxImage        = "quay.io/flightctl-tests/nginx:1.28-alpine-slim"
	nonExistentImage  = "quay.io/flightctl-tests/does-not-exist:never"

	hostPort       = "8080"
	secondHostPort = "8081"
	containerPort  = "80"

	defaultCPU    = "4"
	defaultMemory = "256m"
	lowMemory     = "1m"
)

var _ = Describe("Single Container Applications", Ordered, func() {
	var (
		deviceId string
		harness  *e2e.Harness

		defaultAppSpec        v1beta1.ApplicationProviderSpec
		lowMemoryAppSpec      v1beta1.ApplicationProviderSpec
		secondAppSpec         v1beta1.ApplicationProviderSpec
		badImageAppSpec       v1beta1.ApplicationProviderSpec
		samePortSecondAppSpec v1beta1.ApplicationProviderSpec
	)

	BeforeAll(func() {
		defaultPorts := []v1beta1.ApplicationPort{getPortMapping()}
		secondPorts := []v1beta1.ApplicationPort{v1beta1.ApplicationPort(secondHostPort + ":" + containerPort)}

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

		badImageAppSpec, err = e2e.NewContainerApplicationSpec(
			containerAppName, nonExistentImage, defaultPorts,
			lo.ToPtr(defaultCPU), lo.ToPtr(defaultMemory), nil,
		)
		Expect(err).ToNot(HaveOccurred())

		samePortSecondAppSpec, err = e2e.NewContainerApplicationSpec(
			containerAppName2, nginxImage, defaultPorts,
			lo.ToPtr(defaultCPU), lo.ToPtr(defaultMemory), nil,
		)
		Expect(err).ToNot(HaveOccurred())
	})

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	It("fails on image update to nonexistent reference and recovers when fixed", Label("88860", "sanity"), func() {
		By("Deploying a container application with valid image, ports, and resource limits")
		GinkgoWriter.Printf("Updating device %s with container application %s\n", deviceId, containerAppName)
		err := harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the container application is running with port mapped")
		err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())
		containerPorts, err := harness.GetContainerPorts()
		Expect(err).ToNot(HaveOccurred())
		Expect(containerPorts).To(ContainSubstring(hostPort), "expected host port %s to be mapped", hostPort)
		harness.VerifyQuadletApplicationFolderExists(containerAppName)

		By("Updating the image to a nonexistent reference while keeping ports and resources")
		GinkgoWriter.Printf("Updating container app %s to nonexistent image %s\n", containerAppName, nonExistentImage)
		err = harness.UpdateDeviceAndWaitForFailure(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{badImageAppSpec}
		}, "prefetch failed")
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the quadlet folder from the previous working deployment still exists on the device")
		harness.VerifyQuadletApplicationFolderExists(containerAppName)

		By("Fixing the image back to a valid reference")
		GinkgoWriter.Printf("Restoring container app %s to valid image %s\n", containerAppName, nginxImage)
		newVersion, err := harness.PrepareNextDeviceVersionFromCurrentStatus(deviceId)
		Expect(err).ToNot(HaveOccurred())
		err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
		})
		Expect(err).ToNot(HaveOccurred())
		err = harness.WaitForDeviceNewRenderedVersion(deviceId, newVersion)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the application recovers to Running status")
		err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())
		err = harness.WaitForApplicationSummary(deviceId, testutil.TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		Expect(err).ToNot(HaveOccurred())
	})

	It("reports error status when a second container application binds to an already used port", Label("88861", "sanity"), func() {
		By("Deploying first container application on port " + getPortMapping())
		err := harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying first application is running with port mapped")
		err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())
		containerPorts, err := harness.GetContainerPorts()
		Expect(err).ToNot(HaveOccurred())
		Expect(containerPorts).To(ContainSubstring(hostPort), "expected host port %s to be mapped for first app", hostPort)

		By("Deploying second container application on the same port")
		GinkgoWriter.Printf("Deploying second container app %s on conflicting port %s\n", containerAppName2, getPortMapping())
		err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec, samePortSecondAppSpec}
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying second application enters Error status due to port conflict")
		err = harness.WaitForApplicationStatus(deviceId, containerAppName2, v1beta1.ApplicationStatusError, testutil.TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying first application remains Running")
		err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())
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
		err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())
		err = harness.WaitForApplicationSummary(deviceId, testutil.TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		Expect(err).ToNot(HaveOccurred())
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
		err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusError, testutil.TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())

		By("Restoring normal memory limit")
		err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
			GinkgoWriter.Printf("Restoring container application %s with normal memory limit %s\n", containerAppName, defaultMemory)
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying application recovers to Running status with Healthy summary")
		err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)
		Expect(err).ToNot(HaveOccurred())
		err = harness.WaitForApplicationSummary(deviceId, testutil.TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		Expect(err).ToNot(HaveOccurred())

		By("Adding a second container application alongside the first")
		err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
			apps := []v1beta1.ApplicationProviderSpec{defaultAppSpec, secondAppSpec}
			device.Spec.Applications = &apps
		})
		Expect(err).ToNot(HaveOccurred())

		By("Verifying both applications are running")
		harness.WaitForApplicationsCount(deviceId, 2, v1beta1.ApplicationStatusRunning)

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
