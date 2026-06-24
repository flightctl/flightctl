package helm_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("VM Agent Helm Application Tests", Ordered, func() {
	var (
		harness        *e2e.Harness
		services       *auxiliary.Services
		deviceId       string
		testAppChartV1 string
		testAppChartV2 string
		authChartV1    string
		microshiftOs   = &v1beta1.DeviceOsSpec{Image: util.NewDeviceImageReference(util.DeviceTags.V12).String()}
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		services = auxiliary.Get(harness.GetTestContext())
		deviceId, _ = harness.EnrollAndWaitForOnlineStatus()

		registryEndpoint := fmt.Sprintf("%s:%s", services.Registry.Host, services.Registry.Port)
		privateEndpoint := services.Registry.Authenticated.HostPort
		testAppChartV1 = fmt.Sprintf("%s/flightctl/%s", registryEndpoint, helmChartV1)
		testAppChartV2 = fmt.Sprintf("%s/flightctl/%s", registryEndpoint, helmChartV2)
		authChartV1 = fmt.Sprintf("%s/flightctl/%s", privateEndpoint, helmChartV1)
	})

	Context("helm application", func() {
		It("should deploy, update, and remove a helm application on microshift", Label("87529", "agent", "slow"), func() {
			By("Update OS to one with microshift")
			err := harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Os = microshiftOs
			})
			Expect(err).NotTo(HaveOccurred())

			By("Ensuring all microshift configs are generated")
			err = harness.EnsureMicroshiftConfigs()
			Expect(err).NotTo(HaveOccurred())
			By("Ensuring microshift is ready")
			err = harness.WaitForMicroshiftReady(e2e.MicroshiftKubeconfigPath)
			Expect(err).NotTo(HaveOccurred())

			By("Add helm application configuration")
			helmAppSpec, err := e2e.NewHelmApplicationSpec(helmAppName, testAppChartV1, helmAppNamespace, nil)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{helmAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verify application reaches Running status")
			err = harness.WaitForApplicationStatus(deviceId, helmAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForApplicationSummary(deviceId, util.TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
			Expect(err).ToNot(HaveOccurred())

			By("Update to different helm chart version")
			helmAppSpecUpdated, err := e2e.NewHelmApplicationSpec(helmAppName, testAppChartV2, helmAppNamespace, nil)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{helmAppSpecUpdated}
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForApplicationStatus(deviceId, helmAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Application %s updated to version %s\n", helmAppName, testAppChartV2)

			By("Verify pods are running in the namespace")
			pods, err := harness.GetPodsInNamespace(helmAppNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(pods).ToNot(BeEmpty(), "Expected pods to be running after helm upgrade")
			GinkgoWriter.Printf("Pods running in namespace %s: %v\n", helmAppNamespace, pods)

			By("Remove helm application")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
			})
			Expect(err).ToNot(HaveOccurred())
			harness.WaitForNoApplications(deviceId)
			err = harness.WaitForApplicationSummary(deviceId, util.TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusNoApplications)
			Expect(err).ToNot(HaveOccurred())

			By("Verify no pods are running in the namespace")
			err = harness.WaitForNoPodsInNamespace(helmAppNamespace, util.TIMEOUT)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should deploy multiple application types and manage their lifecycle", Label("87530", "agent", "slow"), func() {
			By("Create fleet with multiple application types (quadlet + container + helm)")
			rootfulQuadlet, err := rootfulQuadletApp(flightctlRepo)
			Expect(err).ToNot(HaveOccurred())
			containerAppSpec, err := e2e.NewContainerApplicationSpec(containerAppName, nginxImage, []v1beta1.ApplicationPort{}, nil, nil, nil)
			Expect(err).ToNot(HaveOccurred())
			helmAppSpec, err := e2e.NewHelmApplicationSpec(helmAppName, testAppChartV1, helmAppNamespace, nil)
			Expect(err).ToNot(HaveOccurred())

			fleetDeviceSpec := v1beta1.DeviceSpec{
				Os:           microshiftOs,
				Applications: &[]v1beta1.ApplicationProviderSpec{rootfulQuadlet, containerAppSpec, helmAppSpec},
			}

			fleetSelector := v1beta1.LabelSelector{
				MatchLabels: &map[string]string{fleetSelectorKey: fleetSelectorValue},
			}

			err = harness.CreateOrUpdateTestFleet(testFleetName, fleetSelector, fleetDeviceSpec)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Created fleet %s with quadlet, container, and helm applications\n", testFleetName)

			By("Assign device to fleet via labels")
			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
				harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{
					fleetSelectorKey: fleetSelectorValue,
				})
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Verify all applications reach Running status")
			err = harness.WaitForApplicationStatus(deviceId, quadletAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForApplicationStatus(deviceId, helmAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForApplicationSummary(deviceId, util.TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
			Expect(err).ToNot(HaveOccurred())

			response, err := harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.JSON200.Status.Applications).To(HaveLen(3))

			By("Update fleet to remove quadlet and container applications, verify helm remains")
			fleetDeviceSpec.Applications = &[]v1beta1.ApplicationProviderSpec{helmAppSpec}

			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			err = harness.CreateOrUpdateTestFleet(testFleetName, fleetSelector, fleetDeviceSpec)
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForApplicationStatus(deviceId, helmAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
			response, err = harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.JSON200.Status.Applications).To(HaveLen(1))
			Expect(response.JSON200.Status.Applications[0].Name).To(Equal(helmAppName))

			By("Update fleet to remove all applications")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			fleetDeviceSpec.Applications = &[]v1beta1.ApplicationProviderSpec{}
			err = harness.CreateOrUpdateTestFleet(testFleetName, fleetSelector, fleetDeviceSpec)
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			harness.WaitForNoApplications(deviceId)
			err = harness.WaitForApplicationSummary(deviceId, util.TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusNoApplications)
			Expect(err).ToNot(HaveOccurred())

			By("Update fleet with container application with runAs=flightctl (rootless)")
			rootlessContainerAppSpec, err := e2e.NewContainerApplicationSpecWithRunAs(containerAppName, nginxImage, []v1beta1.ApplicationPort{}, nil, nil, nil, flightctlUser)
			Expect(err).ToNot(HaveOccurred())

			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			fleetDeviceSpec.Applications = &[]v1beta1.ApplicationProviderSpec{rootlessContainerAppSpec}
			err = harness.CreateOrUpdateTestFleet(testFleetName, fleetSelector, fleetDeviceSpec)
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("Verify rootless container application reaches Running status")
			err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())

			response, err = harness.GetDeviceWithStatusSystem(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.JSON200.Status.Applications).To(HaveLen(1))
			Expect(response.JSON200.Status.Applications[0].RunAs).To(Equal(v1beta1.Username(flightctlUser)))
			GinkgoWriter.Printf("Verified application is running as %s\n", flightctlUser)

			By("Clean up - remove all applications from fleet")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			fleetDeviceSpec.Applications = &[]v1beta1.ApplicationProviderSpec{}
			err = harness.CreateOrUpdateTestFleet(testFleetName, fleetSelector, fleetDeviceSpec)
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			harness.WaitForNoApplications(deviceId)
			err = harness.WaitForApplicationSummary(deviceId, util.TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusNoApplications)
			Expect(err).ToNot(HaveOccurred())

			By("Verify no resources are left")
			err = harness.WaitForNoPodsInNamespace(helmAppNamespace, util.TIMEOUT)
			Expect(err).ToNot(HaveOccurred())

			By("Verify no quadlet or container app resources are running")
			Eventually(func() bool {
				names, err := harness.RunPodmanPsContainerNames(true)
				if err != nil {
					return false
				}
				return !strings.Contains(names, quadletAppName) && !strings.Contains(names, containerAppName)
			}, util.TIMEOUT, util.POLLING).Should(BeTrue(), "quadlet and container apps should be removed")
		})

		It("should report degraded application status when pods are unavailable", Label("87531", "sanity", "agent", "slow"), func() {
			By("Deploy helm application with mixed healthy and delayed-fail deployments")
			mixedDeploymentsValues := map[string]interface{}{
				"deployments": []interface{}{
					map[string]interface{}{
						"name":            "healthy",
						"replicaCount":    1,
						"message":         "healthy pod",
						"failOnStart":     false,
						"delayBeforeFail": 0,
						"resources":       map[string]interface{}{},
					},
					map[string]interface{}{
						"name":            "failing",
						"replicaCount":    1,
						"message":         "failing pod",
						"failOnStart":     true,
						"delayBeforeFail": 20,
						"resources":       map[string]interface{}{},
					},
				},
			}
			helmAppSpec, err := e2e.NewHelmApplicationSpecWithValues(helmAppName, testAppChartV1, helmAppNamespace, mixedDeploymentsValues)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Os = microshiftOs
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{helmAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verify application initially reaches healthy status while failing pod sleeps")
			harness.WaitForApplicationReadyCount(deviceId, helmAppName, "2/2", v1beta1.ApplicationsSummaryStatusHealthy)
			GinkgoWriter.Printf("Application %s is initially healthy with 2/2 pods running\n", helmAppName)

			By("Wait for failing pod to crash and verify degraded status")
			harness.WaitForApplicationReadyCount(deviceId, helmAppName, "1/2", v1beta1.ApplicationsSummaryStatusDegraded)
			GinkgoWriter.Printf("Application %s is degraded with 1/2 pods running\n", helmAppName)

			By("Update to all delayed-fail deployments")
			allFailingValues := map[string]interface{}{
				"deployments": []interface{}{
					map[string]interface{}{
						"name":            "failing1",
						"replicaCount":    1,
						"message":         "failing pod 1",
						"failOnStart":     true,
						"delayBeforeFail": 20,
						"resources":       map[string]interface{}{},
					},
					map[string]interface{}{
						"name":            "failing2",
						"replicaCount":    1,
						"message":         "failing pod 2",
						"failOnStart":     true,
						"delayBeforeFail": 20,
						"resources":       map[string]interface{}{},
					},
				},
			}
			failingAppSpec, err := e2e.NewHelmApplicationSpecWithValues(helmAppName, testAppChartV1, helmAppNamespace, allFailingValues)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{failingAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verify application reaches error status after pods crash")
			err = harness.WaitForApplicationSummary(deviceId, util.DURATION_TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusError)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("Application %s is in Error state due to all pods failing\n", helmAppName)

			By("Remove helm application")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
			})
			Expect(err).ToNot(HaveOccurred())
			harness.WaitForNoApplications(deviceId)
			err = harness.WaitForApplicationSummary(deviceId, util.TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusNoApplications)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should validate helm application error handling across multiple failure scenarios", Label("87532", "agent", "slow"), func() {
			By("Add helm application to base device without microshift dependencies")
			helmAppSpec, err := e2e.NewHelmApplicationSpec(helmAppName, testAppChartV1, helmAppNamespace, nil)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceAndWaitForFailure(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{helmAppSpec}
			}, "prefetch failed for test-chart")
			Expect(err).ToNot(HaveOccurred())

			By("Verify agent logs contain dependency error")
			Eventually(func() string {
				logs, err := harness.ReadPrimaryVMAgentLogs("", util.FLIGHTCTL_AGENT_SERVICE)
				Expect(err).NotTo(HaveOccurred())
				return logs
			}).WithContext(harness.GetTestContext()).
				WithTimeout(util.TIMEOUT).
				WithPolling(10 * time.Second).
				Should(ContainSubstring("required commands not found"))

			By("Deploy 3 applications where the helm chart references images that cannot be pulled")
			containerAppSpec, err := e2e.NewContainerApplicationSpec(containerAppName, nginxImage, []v1beta1.ApplicationPort{}, nil, nil, nil)
			Expect(err).ToNot(HaveOccurred())
			quadletAppSpec, err := rootfulQuadletApp(flightctlRepo)
			Expect(err).ToNot(HaveOccurred())
			badHelmAppSpec, err := e2e.NewHelmApplicationSpecWithValues(helmAppName, testAppChartV1, helmAppNamespace,
				map[string]interface{}{
					"image": map[string]interface{}{
						"repository": "quay.io/flightctl/images",
						"tag":        "doesnotexist",
					},
				})
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceAndWaitForFailure(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{
					containerAppSpec, quadletAppSpec, badHelmAppSpec,
				}
				device.Spec.Os = microshiftOs
			}, "prefetch failed for quay.io/flightctl/images:doesnotexist")
			Expect(err).ToNot(HaveOccurred())

			By("Configure rootless quadlet app with auth only accessible by root")
			authJSON := buildAuthJSON(services.Registry.Authenticated.Username, services.Registry.Authenticated.Password, authFlightctlRepo)
			rootOnlyAuthConfig, err := rootfulContainerCreds(authJSON)
			Expect(err).ToNot(HaveOccurred())
			rootlessQuadlet, err := rootlessQuadletApp(authFlightctlRepo)
			Expect(err).ToNot(HaveOccurred())
			helmAppSpec, err = e2e.NewHelmApplicationSpecWithValues(helmAppName, testAppChartV1, helmAppNamespace, map[string]interface{}{})
			Expect(err).ToNot(HaveOccurred())
			// can fail for either the model or the quadlet package
			err = harness.UpdateDeviceAndWaitForFailure(deviceId, func(device *v1beta1.Device) {
				device.Spec.Os = microshiftOs
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{rootOnlyAuthConfig}
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{rootlessQuadlet, helmAppSpec}
			}, quadletImage, quadletModelDataImage)
			Expect(err).ToNot(HaveOccurred())

			By("Configure helm app from private registry without providing auth")
			privateHelmAppSpec, err := e2e.NewHelmApplicationSpecWithValues(helmAppName, authChartV1, helmAppNamespace, map[string]interface{}{})
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceAndWaitForFailure(deviceId, func(device *v1beta1.Device) {
				device.Spec.Os = microshiftOs
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{privateHelmAppSpec}
			}, helmChartV1)
			Expect(err).ToNot(HaveOccurred())
		})
		It("runAs flightctl applications can be deployed in a device", Label("87874", "agent", "slow"), func() {
			By("Create device with multiple application types (quadlet + container + helm) in rootless mode")
			rootlessQuadlet, err := rootlessQuadletApp(flightctlRepo)
			Expect(err).ToNot(HaveOccurred())

			containerAppSpec, err := e2e.NewContainerApplicationSpecWithRunAs(containerAppName, nginxImage, []v1beta1.ApplicationPort{}, nil, nil, nil, flightctlUser)
			Expect(err).ToNot(HaveOccurred())

			helmAppSpec, err := e2e.NewHelmApplicationSpec(helmAppName, testAppChartV1, helmAppNamespace, nil)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Os = microshiftOs
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{containerAppSpec, rootlessQuadlet, helmAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Move quadlets and containers to rootful mode")
			rootfulQuadlet, err := rootfulQuadletApp(flightctlRepo)
			Expect(err).ToNot(HaveOccurred())

			containerAppSpec, err = e2e.NewContainerApplicationSpec(containerAppName, nginxImage, []v1beta1.ApplicationPort{}, nil, nil, nil)
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{containerAppSpec, rootfulQuadlet, helmAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Disable connectivity from the registry to ensure prefetch relies on old images")
			DeferCleanup(func() { _ = harness.FixNetworkFailure(services.Registry.Host, services.Registry.Port) })
			err = harness.SimulateNetworkFailure(services.Registry.Host, services.Registry.Port)
			Expect(err).ToNot(HaveOccurred())

			By("Swap quadlets back to rootless")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{containerAppSpec, rootlessQuadlet, helmAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.FixNetworkFailure(services.Registry.Host, services.Registry.Port)
			Expect(err).ToNot(HaveOccurred())

			By("Ensure all applications are running")
			err = harness.WaitForApplicationStatus(deviceId, quadletAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForApplicationStatus(deviceId, containerAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForApplicationStatus(deviceId, helmAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForApplicationSummary(deviceId, util.TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
			Expect(err).ToNot(HaveOccurred())
		})
		It("runAs flightctl application with auth can be deployed to a device", Label("88004", "sanity", "agent", "slow"), func() {
			By("Deploy a helm app with helm registry credentials")
			creds := buildAuthJSON(services.Registry.Authenticated.Username, services.Registry.Authenticated.Password, services.Registry.Authenticated.HostPort, authFlightctlRepo)

			helmAppSpec, err := e2e.NewHelmApplicationSpec(helmAppName, authChartV1, helmAppNamespace, nil)
			Expect(err).ToNot(HaveOccurred())
			helmAuth, err := helmCreds(creds)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Os = microshiftOs
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{helmAuth}
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{helmAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForApplicationStatus(deviceId, helmAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())

			By("Deploy a rootful quadlet app")
			rootfulQuadlet, err := rootfulQuadletApp(authFlightctlRepo)
			Expect(err).ToNot(HaveOccurred())
			rootfulContainerAuth, err := rootfulContainerCreds(creds)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Os = microshiftOs
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{helmAuth, rootfulContainerAuth}
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{helmAppSpec, rootfulQuadlet}
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForApplicationStatus(deviceId, quadletAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())

			By("Deploy a rootless quadlet app")
			rootlessContainerAuth, err := rootlessContainerCreds(creds)
			Expect(err).ToNot(HaveOccurred())
			rootlessQuadlet, err := rootlessQuadletApp(authFlightctlRepo)
			Expect(err).ToNot(HaveOccurred())
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{helmAuth, rootlessContainerAuth}
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{helmAppSpec, rootlessQuadlet}
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForApplicationStatus(deviceId, quadletAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())
		})
		It("Helm application images can be pruned", Label("88005", "agent", "slow"), func() {
			By("Deploy a helm app and quadlet app that reference the same set of images")
			helmAppSpec, err := e2e.NewHelmApplicationSpec(helmAppName, testAppChartV1, helmAppNamespace, []string{"failure.values.yaml"})
			Expect(err).ToNot(HaveOccurred())
			quadletAppSpec, err := rootlessQuadletApp(flightctlRepo)
			Expect(err).ToNot(HaveOccurred())

			pruningConfig, err := e2e.NewInlineConfigSpec("pruning", []v1beta1.FileSpec{
				{
					Content: `
image-pruning:
  enabled: true
`,
					Path: "/etc/flightctl/conf.d/enable-pruning.yaml",
					User: "",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Os = microshiftOs
				device.Spec.Config = &[]v1beta1.ConfigProviderSpec{pruningConfig}
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{helmAppSpec, quadletAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.WaitForApplicationStatus(deviceId, helmAppName, v1beta1.ApplicationStatusRunning, util.TIMEOUT, util.POLLING)
			Expect(err).ToNot(HaveOccurred())

			By("Remove the helm app")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{quadletAppSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Ensure no helm artifacts are remaining")
			Eventually(func() error {
				chartsOut, err := harness.VM.RunSSH([]string{"sudo", "ls", "-A", "/var/lib/flightctl/helm/charts"}, nil)
				if err != nil {
					return err
				}
				if strings.TrimSpace(chartsOut.String()) != "" {
					return fmt.Errorf("helm charts directory not empty: %s", chartsOut.String())
				}
				return nil
			}, util.TIMEOUT, util.POLLING).Should(Succeed())

			By("Verify no pods are running in the namespace")
			err = harness.WaitForNoPodsInNamespace(helmAppNamespace, util.TIMEOUT)
			Expect(err).ToNot(HaveOccurred())

			By("Ensure that the shared image wasn't deleted while quadlet app is still running")
			sharedImage := "quay.io/flightctl-tests/alpine"
			exists, err := harness.CrictlImageExists(sharedImage)
			Expect(err).ToNot(HaveOccurred())
			Expect(exists).To(BeTrue(), "shared image %s should still exist while quadlet app is running", sharedImage)

			By("Remove the quadlet app")
			err = harness.UpdateDeviceAndWaitForVersion(deviceId, func(device *v1beta1.Device) {
				device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
			})
			Expect(err).ToNot(HaveOccurred())

			By("Verify shared image is pruned after all apps are removed")
			Eventually(func() bool {
				exists, err := harness.CrictlImageExists(sharedImage)
				if err != nil {
					return false
				}
				return !exists
			}, util.TIMEOUT_5M, util.LONG_POLLING).Should(BeTrue(), "shared image %s should be pruned after all apps are removed", sharedImage)
		})
	})
})

func helmCreds(creds string) (v1beta1.ConfigProviderSpec, error) {
	return e2e.NewInlineConfigSpec("helm-creds", []v1beta1.FileSpec{
		{
			Content: creds,
			Mode:    lo.ToPtr(0600),
			Path:    "/root/.config/helm/registry/config.json",
		},
	})
}
func rootfulContainerCreds(creds string) (v1beta1.ConfigProviderSpec, error) {
	return e2e.NewInlineConfigSpec("rootfull-container", []v1beta1.FileSpec{
		{
			Content: creds,
			Mode:    lo.ToPtr(0600),
			Path:    "/root/.config/containers/auth.json",
		},
	})
}
func rootlessContainerCreds(creds string) (v1beta1.ConfigProviderSpec, error) {
	return e2e.NewInlineConfigSpec("rootless-container", []v1beta1.FileSpec{
		{
			Content: creds,
			Mode:    lo.ToPtr(0600),
			Path:    "/var/home/flightctl/.config/containers/auth.json",
			User:    flightctlUser,
			Group:   flightctlUser,
		},
	})
}

func rootfulQuadletApp(repo string) (v1beta1.ApplicationProviderSpec, error) {
	return quadletApp(repo, "", quadletAppName)
}

func rootlessQuadletApp(repo string) (v1beta1.ApplicationProviderSpec, error) {
	return quadletApp(repo, flightctlUser, quadletAppName)
}

func quadletApp(repo string, user string, appName string) (v1beta1.ApplicationProviderSpec, error) {
	modelDataVolume := v1beta1.ApplicationVolume{
		Name:          "model-data",
		ReclaimPolicy: lo.ToPtr(v1beta1.Retain),
	}
	err := modelDataVolume.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{
			Reference:  fmt.Sprintf("%s/%s", repo, quadletModelDataImage),
			PullPolicy: lo.ToPtr(v1beta1.PullIfNotPresent),
		}})

	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("building model data volume: %w", err)
	}

	quad, err := e2e.NewQuadletApplicationSpec(appName, fmt.Sprintf("%s/%s", repo, quadletImage), user, map[string]string{
		"LOG_MESSAGE": "E2E test quadlet application",
	}, modelDataVolume)
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("creating quadlet application: %w", err)
	}
	return quad, nil
}

func buildAuthJSON(username, password string, registries ...string) string {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	auths := make([]string, 0, len(registries))
	for _, registry := range registries {
		auths = append(auths, fmt.Sprintf(`"%s":{"auth":"%s"}`, registry, auth))
	}
	return fmt.Sprintf(`{"auths":{%s}}`, strings.Join(auths, ","))
}

const (
	helmAppName      = "test-chart"
	helmAppNamespace = "test"
	helmChartV1      = "charts/test-app:0.1.0"
	helmChartV2      = "charts/test-app:0.2.0"

	containerAppName = "nginx-server"
	nginxImage       = "quay.io/flightctl-tests/nginx:1.28-alpine-slim"

	flightctlRepo     = "quay.io/flightctl"
	authFlightctlRepo = "quay.io/flightctl-private"

	quadletAppName        = "quadlet-app"
	quadletImage          = "quadlets/quadlet-app:latest"
	quadletModelDataImage = "quadlets/model-data:latest"

	flightctlUser = "flightctl"

	testFleetName      = "fleet-helm-test"
	fleetSelectorKey   = "fleet"
	fleetSelectorValue = "helm-test"
)
