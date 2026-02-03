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
	// TIMEOUT is the default timeout for waiting operations
	TIMEOUT = "5m"

	// Container application constants
	containerAppName  = "container-app"
	containerAppName2 = "container-app-2"
	nginxImage        = "docker.io/library/nginx:1.28-alpine-slim"

	hostPort      = "8080"
	containerPort = "80"

	defaultCPU    = "0.5"
	lowCPU        = "0.05"
	defaultMemory = "256m"
	lowMemory     = "1m"
)

var _ = Describe("Single Container Applications", Label("86285", "sanity"), func() {
	var (
		deviceId string
	)

	BeforeEach(func() {
		harness := e2e.GetWorkerHarness()
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	Describe("Core Deployment Lifecycle", func() {
		It("verifies a single container can be deployed, observed, and removed", func() {
			harness := e2e.GetWorkerHarness()

			By("Creating container application spec with ports, resources, and volumes")

			// Create a mount volume for logs (container apps support mount volumes)
			logsVolume, err := e2e.NewMountVolume("nginx-logs", "/var/log/nginx")
			Expect(err).ToNot(HaveOccurred())
			volumes := []v1beta1.ApplicationVolume{logsVolume}

			ports := []v1beta1.ApplicationPort{getPortMapping()}
			appSpec, err := e2e.NewContainerApplicationSpec(
				containerAppName,
				nginxImage,
				ports,
				lo.ToPtr(defaultCPU),
				lo.ToPtr(defaultMemory),
				&volumes,
			)
			Expect(err).ToNot(HaveOccurred())

			By("Adding the container application to the device")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
				GinkgoWriter.Printf("Updating device %s with container application %s\n", deviceId, containerAppName)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verifying the container is running on the device")
			harness.VerifyContainerRunning("nginx", TIMEOUT)

			By("Checking the application status is Running")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName, TIMEOUT)

			By("Verifying the application folder exists")
			harness.VerifyQuadletApplicationFolderExists(containerAppName, TIMEOUT)

			By("Checking the device applications summary status")
			Eventually(func() v1beta1.ApplicationsSummaryStatusType {
				response, err := harness.GetDeviceWithStatusSystem(deviceId)
				Expect(err).ToNot(HaveOccurred())
				return response.JSON200.Status.ApplicationsSummary.Status
			}, TIMEOUT).Should(Equal(v1beta1.ApplicationsSummaryStatusHealthy))

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
			harness.VerifyNoContainersRunning(TIMEOUT)

			By("Verifying the application folder is deleted")
			harness.VerifyQuadletApplicationFolderDeleted(containerAppName, TIMEOUT)

			By("Verifying the device has no applications in status")
			Eventually(func() int {
				response, err := harness.GetDeviceWithStatusSystem(deviceId)
				Expect(err).ToNot(HaveOccurred())
				return len(response.JSON200.Status.Applications)
			}, TIMEOUT).Should(Equal(0))
		})
	})

	Describe("Container Resource Limits", func() {
		It("should observe CPU throttling when low CPU limit is applied", func() {
			harness := e2e.GetWorkerHarness()

			By("Deploying container application with normal CPU limit")
			ports := []v1beta1.ApplicationPort{"8080:80"}

			appSpec, err := e2e.NewContainerApplicationSpec(
				containerAppName,
				nginxImage,
				ports,
				lo.ToPtr(defaultCPU),
				lo.ToPtr(defaultMemory),
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
				GinkgoWriter.Printf("Deploying container application %s with normal CPU limit %s\n", containerAppName, defaultCPU)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for application to be running and healthy")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName, TIMEOUT)

			By("Checking the device applications summary status is healthy")
			Eventually(func() v1beta1.ApplicationsSummaryStatusType {
				response, err := harness.GetDeviceWithStatusSystem(deviceId)
				Expect(err).ToNot(HaveOccurred())
				return response.JSON200.Status.ApplicationsSummary.Status
			}, TIMEOUT).Should(Equal(v1beta1.ApplicationsSummaryStatusHealthy))

			By("Updating to low CPU limit")
			lowCpuAppSpec, err := e2e.NewContainerApplicationSpec(
				containerAppName,
				nginxImage,
				ports,
				lo.ToPtr(lowCPU),
				lo.ToPtr(defaultMemory),
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{lowCpuAppSpec}
				GinkgoWriter.Printf("Updating container application %s with low CPU limit %s\n", containerAppName, lowCPU)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for container to restart with new CPU limit")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName, TIMEOUT)

			By("Verifying CPU limit is applied")
			harness.VerifyContainerCPULimitApplied(TIMEOUT)
		})

		It("should report Error status when memory limit is too low", func() {
			harness := e2e.GetWorkerHarness()

			By("Deploying container application with normal memory limit")
			ports := []v1beta1.ApplicationPort{getPortMapping()}

			appSpec, err := e2e.NewContainerApplicationSpec(
				containerAppName,
				nginxImage,
				ports,
				lo.ToPtr(defaultCPU),
				lo.ToPtr(defaultMemory),
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
				GinkgoWriter.Printf("Deploying container application %s with normal memory limit %s\n", containerAppName, defaultMemory)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for application to be running and healthy")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName, TIMEOUT)

			By("Updating to extremely low memory limit")
			lowMemoryAppSpec, err := e2e.NewContainerApplicationSpec(
				containerAppName,
				nginxImage,
				ports,
				lo.ToPtr(defaultCPU),
				lo.ToPtr(lowMemory),
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{lowMemoryAppSpec}
				GinkgoWriter.Printf("Updating container application %s with low memory limit %s\n", containerAppName, lowMemory)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verifying application status becomes Error due to low memory limit")
			Eventually(func() v1beta1.ApplicationStatusType {
				response, err := harness.GetDeviceWithStatusSystem(deviceId)
				if err != nil {
					return ""
				}
				for _, app := range response.JSON200.Status.Applications {
					if app.Name == containerAppName {
						return app.Status
					}
				}
				return ""
			}, TIMEOUT, "5s").Should(Equal(v1beta1.ApplicationStatusError), "Expected application status to become Error due to low memory limit")

			By("Restoring normal memory limit")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
				GinkgoWriter.Printf("Restoring container application %s with normal memory limit %s\n", containerAppName, defaultMemory)
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verifying application recovers to Running status with Healthy summary")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName, TIMEOUT)

			Eventually(func() v1beta1.ApplicationsSummaryStatusType {
				response, err := harness.GetDeviceWithStatusSystem(deviceId)
				Expect(err).ToNot(HaveOccurred())
				return response.JSON200.Status.ApplicationsSummary.Status
			}, TIMEOUT).Should(Equal(v1beta1.ApplicationsSummaryStatusHealthy))
		})
	})

	Context("Multi-Application Namespace Isolation", func() {
		It("should allow multiple container applications to coexist with different namespaces", func() {
			harness := e2e.GetWorkerHarness()

			By("Creating the first container application")
			ports := []v1beta1.ApplicationPort{getPortMapping()}

			containerAppSpec, err := e2e.NewContainerApplicationSpec(
				containerAppName,
				nginxImage,
				ports,
				lo.ToPtr(defaultCPU),
				lo.ToPtr(defaultMemory),
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			By("Adding the first container application to the device")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{containerAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verifying the first container application is running")
			harness.WaitForApplicationRunningStatus(deviceId, containerAppName, TIMEOUT)

			By("Creating a second container application")
			ports2 := []v1beta1.ApplicationPort{"8081:80"} // Different port to avoid conflict

			containerAppSpec2, err := e2e.NewContainerApplicationSpec(
				containerAppName2,
				nginxImage,
				ports2,
				lo.ToPtr(defaultCPU),
				lo.ToPtr(defaultMemory),
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			By("Adding the second container application alongside the first")
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				// Keep first container app and add second container app
				*device.Spec.Applications = append(*device.Spec.Applications, containerAppSpec2)
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying both applications are running")
			Eventually(func() int {
				runningCount := 0
				response, err := harness.GetDeviceWithStatusSystem(deviceId)
				Expect(err).ToNot(HaveOccurred())
				for _, app := range response.JSON200.Status.Applications {
					if app.Status == v1beta1.ApplicationStatusRunning {
						runningCount++
					}
				}
				return runningCount
			}, TIMEOUT).Should(Equal(2))

			By("Verifying both containers are running")
			Eventually(func() int {
				count, err := harness.GetRunningContainerCount()
				Expect(err).ToNot(HaveOccurred())
				return count
			}, TIMEOUT).Should(BeNumerically(">=", 2))
		})
	})
})

func getPortMapping() string {
	return fmt.Sprintf("%s:%s", hostPort, containerPort)
}
