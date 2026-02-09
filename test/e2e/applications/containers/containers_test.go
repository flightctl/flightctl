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

	defaultCPU    = "0.5"
	lowCPU        = "0.05"
	defaultMemory = "256m"
	lowMemory     = "1m"
)

var _ = Describe("Single Container Applications", Ordered, Label("86285", "sanity"), func() {
	var (
		deviceId string
		harness  *e2e.Harness

		defaultAppSpec   v1beta1.ApplicationProviderSpec
		lowCPUAppSpec    v1beta1.ApplicationProviderSpec
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

		lowCPUAppSpec, err = e2e.NewContainerApplicationSpec(
			containerAppName, nginxImage, defaultPorts,
			lo.ToPtr(lowCPU), lo.ToPtr(defaultMemory), nil,
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

	Describe("Core Deployment Lifecycle", func() {
		It("verifies a single container can be deployed, observed, and removed", func() {
			By("Adding the container application to the device")
			err := harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
				GinkgoWriter.Printf("Updating device %s with container application %s\n", deviceId, containerAppName)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verifying the container is running on the device")
			harness.VerifyContainerRunning("nginx")

			By("Checking the application status is Running")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName)

			By("Verifying the application folder exists")
			harness.VerifyQuadletApplicationFolderExists(containerAppName)

			By("Checking the device applications summary status")
			harness.WaitForApplicationStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)

			By("Verifying the container is listening on the mapped port")
			containerPorts, err := harness.GetContainerPorts()
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Container ports: %s\n", containerPorts)
			Expect(containerPorts).To(ContainSubstring(hostPort))

			By("Removing the application from the device spec")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
				GinkgoWriter.Printf("Removing all applications from device %s\n", deviceId)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verifying no containers are running")
			err = harness.VerifyContainerCount(0)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying the application folder is deleted")
			harness.VerifyQuadletApplicationFolderDeleted(containerAppName)

			By("Verifying the device has no applications in status")
			harness.WaitForNoApplications(deviceId)
		})
	})

	Describe("Container Resource Limits", func() {
		It("should observe CPU throttling when low CPU limit is applied", func() {
			By("Deploying container application with normal CPU limit")
			err := harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
				GinkgoWriter.Printf("Deploying container application %s with normal CPU limit %s\n", containerAppName, defaultCPU)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for application to be running and healthy")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName)

			By("Checking the device applications summary status is healthy")
			harness.WaitForApplicationStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)

			By("Updating to low CPU limit")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{lowCPUAppSpec}
				GinkgoWriter.Printf("Updating container application %s with low CPU limit %s\n", containerAppName, lowCPU)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for container to restart with new CPU limit")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName)

			By("Verifying CPU limit is applied")
			harness.VerifyContainerCPULimitApplied()
		})

		It("should report Error status when memory limit is too low", func() {
			By("Deploying container application with normal memory limit")
			err := harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
				GinkgoWriter.Printf("Deploying container application %s with normal memory limit %s\n", containerAppName, defaultMemory)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for application to be running and healthy")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName)

			By("Updating to extremely low memory limit")
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
			harness.WaitForApplicationStatus(deviceId, v1beta1.ApplicationsSummaryStatusHealthy)
		})
	})

	Context("Multi-Application Namespace Isolation", func() {
		It("should allow multiple container applications to coexist with different namespaces", func() {
			By("Adding the first container application to the device")
			err := harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{defaultAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verifying the first container application is running")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName)

			By("Adding the second container application alongside the first")

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
		})
	})
})

func getPortMapping() string {
	return fmt.Sprintf("%s:%s", hostPort, containerPort)
}
