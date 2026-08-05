//go:build linux

package packagemode_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	packageModeContainerRunAsUser = "flightctl"
	packageModeEnrollTimeout      = 5 * time.Minute
	packageModePollInterval       = 5 * time.Second
	packageModeConfigPath         = "/etc/flightctl/package-mode-e2e.conf"
	packageModeConfigContent      = "package-mode-e2e=enabled"
	packageModeContainerAppName   = "package-mode-sleep"
	packageModeFleetLabelKey      = "package-mode-suite"
	packageModeNormalFleetPrefix  = "package-mode-normal"
	packageModeExpectedRejectText = "package-mode device cannot satisfy spec with os.image"
)

var _ = Describe("Package-mode device scenarios", func() {
	var harness *e2e.Harness

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
	})

	Context("normal package-mode operation", Ordered, func() {
		var (
			deviceID         string
			fleetName        string
			selector         v1beta1.LabelSelector
			packageModeAgent *e2e.PackageModeAgent
			contextFailed    bool
		)

		BeforeAll(func() {
			var err error
			harness = e2e.GetWorkerHarness()
			_, err = login.LoginToAPIWithToken(harness)
			Expect(err).ToNot(HaveOccurred())

			restoreContext := setHarnessSetupContext(harness, packageModeNormalFleetPrefix)
			defer harness.SetTestContext(restoreContext)

			fleetName = uniqueFleetName(packageModeNormalFleetPrefix)
			selector = v1beta1.LabelSelector{
				MatchLabels: &map[string]string{
					packageModeFleetLabelKey: fleetName,
				},
			}

			agentConfigDir := e2e.GetAgentConfigDir()
			Expect(e2e.AgentConfigDirExists()).To(BeTrue(), "agent config dir must exist")

			startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer startCancel()
			packageModeAgent, err = e2e.StartPackageModeAgent(
				startCtx,
				agentConfigDir,
				packageModeAuxSvcs.Registry.Host,
				packageModeAuxSvcs.Registry.Port,
			)
			Expect(err).ToNot(HaveOccurred())

			inlineConfig, err := e2e.NewInlineConfigSpec("package-mode-inline", []v1beta1.FileSpec{{
				Path:    packageModeConfigPath,
				Content: packageModeConfigContent,
			}})
			Expect(err).ToNot(HaveOccurred())

			appImage := harness.GetSleepAppImageRefForFleet(packageModeAuxSvcs.Registry.Host, packageModeAuxSvcs.Registry.Port, testutil.SleepAppTags.V2)
			containerApp, err := e2e.NewContainerApplicationSpecWithRunAs(
				packageModeContainerAppName,
				appImage,
				[]v1beta1.ApplicationPort{},
				nil,
				nil,
				nil,
				packageModeContainerRunAsUser,
			)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec := v1beta1.DeviceSpec{
				Config:       &[]v1beta1.ConfigProviderSpec{inlineConfig},
				Applications: &[]v1beta1.ApplicationProviderSpec{containerApp},
			}

			err = harness.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
			Expect(err).ToNot(HaveOccurred())

			deviceID, err = enrollPackageModeAgent(harness, packageModeAgent, map[string]string{
				packageModeFleetLabelKey: fleetName,
			})
			Expect(err).ToNot(HaveOccurred())

			// Fleet association + template render can lag Online; wait until the rendered
			// device spec actually carries the package-mode application.
			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(deviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Metadata.Owner).ToNot(BeNil())
				g.Expect(*device.Metadata.Owner).To(Equal("Fleet/" + fleetName))
				g.Expect(device.Spec).ToNot(BeNil())
				g.Expect(device.Spec.Applications).ToNot(BeNil())
				g.Expect(deviceHasNamedApplicationSpec(*device.Spec.Applications, packageModeContainerAppName)).To(BeTrue())
			}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())
		})

		AfterEach(func() {
			if CurrentSpecReport().Failed() {
				contextFailed = true
			}
		})

		AfterAll(func() {
			if contextFailed {
				dumpPackageModeFailureDiagnostics(harness, packageModeAgent, deviceID, "normal context")
			}
			if strings.TrimSpace(deviceID) != "" {
				if err := harness.DeleteDeviceIgnoreNotFound(deviceID); err != nil {
					GinkgoWriter.Printf("DeleteDeviceIgnoreNotFound(%s): %v\n", deviceID, err)
				}
			}
			if strings.TrimSpace(fleetName) != "" {
				if err := harness.DeleteFleetIgnoreNotFound(fleetName); err != nil {
					GinkgoWriter.Printf("DeleteFleetIgnoreNotFound(%s): %v\n", fleetName, err)
				}
			}
			if packageModeAgent != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := packageModeAgent.Stop(ctx); err != nil {
					GinkgoWriter.Printf("StopPackageModeAgent: %v\n", err)
				}
			}
		})

		It("enrolls a package-mode device and reports capabilities.osMode=package", Label("90146", "sanity"), func() {
			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(deviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Status).ToNot(BeNil())
				g.Expect(device.Status.Capabilities).ToNot(BeNil())
				g.Expect(device.Status.Capabilities.OsMode).ToNot(BeNil())
				g.Expect(*device.Status.Capabilities.OsMode).To(Equal(v1beta1.OsModePackage))
			}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())
		})

		It("deploys config and a podman application on a package-mode fleet without spec.os.image", Label("90147", "sanity"), func() {
			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(deviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Spec).ToNot(BeNil())
				g.Expect(device.Spec.Os).To(BeNil())
			}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())

			// Nested rootless podman + registry pull needs more than the default 1m TIMEOUT.
			Expect(harness.WaitForApplicationStatus(deviceID, packageModeContainerAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
			Expect(harness.WaitForApplicationSummary(deviceID, testutil.LONG_TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				sshCtx, sshCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer sshCancel()
				output, err := packageModeAgent.RunSSH(sshCtx, []string{"cat", packageModeConfigPath})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(output).To(ContainSubstring(packageModeConfigContent))
			}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(deviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Status).ToNot(BeNil())
				g.Expect(device.Status.Applications).ToNot(BeNil())
				application := findDeviceApplicationStatusByName(device.Status.Applications, packageModeContainerAppName)
				g.Expect(application).ToNot(BeNil())
				g.Expect(application.RunAs).To(Equal(v1beta1.Username(packageModeContainerRunAsUser)))
			}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())
		})
	})

	Context("package-mode spec.os.image rejection", Ordered, func() {
		var (
			deviceID         string
			fleetName        string
			selector         v1beta1.LabelSelector
			packageModeAgent *e2e.PackageModeAgent
			initialVersion   int
			contextFailed    bool
		)

		BeforeAll(func() {
			var err error
			harness = e2e.GetWorkerHarness()
			_, err = login.LoginToAPIWithToken(harness)
			Expect(err).ToNot(HaveOccurred())

			restoreContext := setHarnessSetupContext(harness, "package-mode-reject")
			defer harness.SetTestContext(restoreContext)

			fleetName = uniqueFleetName("package-mode-reject")
			selector = v1beta1.LabelSelector{
				MatchLabels: &map[string]string{
					packageModeFleetLabelKey: fleetName,
				},
			}

			agentConfigDir := e2e.GetAgentConfigDir()
			Expect(e2e.AgentConfigDirExists()).To(BeTrue(), "agent config dir must exist")

			startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer startCancel()
			packageModeAgent, err = e2e.StartPackageModeAgent(
				startCtx,
				agentConfigDir,
				packageModeAuxSvcs.Registry.Host,
				packageModeAuxSvcs.Registry.Port,
			)
			Expect(err).ToNot(HaveOccurred())

			err = harness.CreateOrUpdateTestFleet(fleetName, selector, v1beta1.DeviceSpec{})
			Expect(err).ToNot(HaveOccurred())

			deviceID, err = enrollPackageModeAgent(harness, packageModeAgent, map[string]string{
				packageModeFleetLabelKey: fleetName,
			})
			Expect(err).ToNot(HaveOccurred())

			Expect(harness.WaitForDeviceNewRenderedVersion(deviceID, 1)).ToNot(HaveOccurred())

			initialVersion, err = harness.GetCurrentDeviceRenderedVersion(deviceID)
			Expect(err).ToNot(HaveOccurred())

			registryHost := packageModeAuxSvcs.Registry.Host
			registryPort := packageModeAuxSvcs.Registry.Port
			deviceSpec, err := harness.CreateFleetDeviceSpec(registryHost, registryPort, testutil.DeviceTags.V2)
			Expect(err).ToNot(HaveOccurred())

			err = harness.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			if CurrentSpecReport().Failed() {
				contextFailed = true
			}
		})

		AfterAll(func() {
			if contextFailed {
				dumpPackageModeFailureDiagnostics(harness, packageModeAgent, deviceID, "reject context")
			}
			if strings.TrimSpace(deviceID) != "" {
				if err := harness.DeleteDeviceIgnoreNotFound(deviceID); err != nil {
					GinkgoWriter.Printf("DeleteDeviceIgnoreNotFound(%s): %v\n", deviceID, err)
				}
			}
			if strings.TrimSpace(fleetName) != "" {
				if err := harness.DeleteFleetIgnoreNotFound(fleetName); err != nil {
					GinkgoWriter.Printf("DeleteFleetIgnoreNotFound(%s): %v\n", fleetName, err)
				}
			}
			if packageModeAgent != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := packageModeAgent.Stop(ctx); err != nil {
					GinkgoWriter.Printf("StopPackageModeAgent: %v\n", err)
				}
			}
		})

		It("keeps the package-mode device OutOfDate when the fleet adds spec.os.image", Label("90148", "sanity"), func() {
			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(deviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Status).ToNot(BeNil())
				g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))
				g.Expect(device.Status.Config.RenderedVersion).To(Equal(strconv.Itoa(initialVersion)))
				g.Expect(e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError))).To(BeTrue())
				cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
				g.Expect(cond).ToNot(BeNil())
				g.Expect(cond.Message).To(ContainSubstring(packageModeExpectedRejectText))
			}, testutil.LONG_TIMEOUT, packageModePollInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				logCtx, logCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer logCancel()
				logs, err := packageModeAgent.GetAgentLogs(logCtx)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(logs).To(ContainSubstring(packageModeExpectedRejectText))
			}, testutil.LONG_TIMEOUT, packageModePollInterval).Should(Succeed())
		})
	})
})

func enrollPackageModeAgent(harness *e2e.Harness, agent *e2e.PackageModeAgent, labels map[string]string) (string, error) {
	GinkgoHelper()

	if harness == nil {
		return "", fmt.Errorf("harness is nil")
	}
	if agent == nil {
		return "", fmt.Errorf("agent is nil")
	}

	enrollmentID, err := agent.GetEnrollmentID(context.Background(), packageModeEnrollTimeout)
	if err != nil {
		return "", fmt.Errorf("get enrollment ID: %w", err)
	}

	if _, err := harness.WaitForEnrollmentRequestResource(enrollmentID, testutil.TIMEOUT_5M, testutil.SHORT_POLLING); err != nil {
		return "", err
	}

	approvalLabels, err := mergeEnrollmentLabels(harness, labels)
	if err != nil {
		return "", err
	}
	if _, err := harness.ApproveEnrollmentRequestWithLabels(enrollmentID, approvalLabels); err != nil {
		return "", err
	}

	Eventually(func() error {
		device, err := harness.CheckDeviceStatus(enrollmentID, v1beta1.DeviceSummaryStatusOnline)
		if err != nil {
			return err
		}
		if device == nil {
			return fmt.Errorf("device %s is still nil", enrollmentID)
		}
		return nil
	}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())

	return enrollmentID, nil
}

func mergeEnrollmentLabels(harness *e2e.Harness, labels map[string]string) (map[string]string, error) {
	testLabels, err := harness.TestResourceLabels()
	if err != nil {
		return nil, err
	}

	merged := map[string]string{}
	for key, value := range testLabels {
		merged[key] = value
	}
	for key, value := range labels {
		merged[key] = value
	}
	return merged, nil
}

func uniqueFleetName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func setHarnessSetupContext(harness *e2e.Harness, prefix string) context.Context {
	restoreContext := harness.GetTestContext()
	setupContext := context.WithValue(restoreContext, testutil.TestIDKey, uniqueFleetName(prefix+"-setup"))
	harness.SetTestContext(setupContext)
	return restoreContext
}

func findDeviceApplicationStatusByName(applications []v1beta1.DeviceApplicationStatus, applicationName string) *v1beta1.DeviceApplicationStatus {
	for i := range applications {
		if applications[i].Name == applicationName {
			return &applications[i]
		}
	}
	return nil
}

func deviceHasNamedApplicationSpec(applications []v1beta1.ApplicationProviderSpec, applicationName string) bool {
	for _, app := range applications {
		name, err := app.GetName()
		if err == nil && name != nil && *name == applicationName {
			return true
		}
	}
	return false
}

func dumpPackageModeFailureDiagnostics(harness *e2e.Harness, agent *e2e.PackageModeAgent, deviceID, label string) {
	GinkgoHelper()
	if strings.TrimSpace(deviceID) != "" && harness != nil {
		device, err := harness.GetDevice(deviceID)
		if err != nil {
			GinkgoWriter.Printf("=== device status (%s): get failed: %v ===\n", label, err)
		} else if device != nil {
			owner := ""
			if device.Metadata.Owner != nil {
				owner = *device.Metadata.Owner
			}
			var specApps any
			if device.Spec != nil && device.Spec.Applications != nil {
				specApps = *device.Spec.Applications
			}
			if device.Status != nil {
				GinkgoWriter.Printf("=== device (%s) owner=%s updated=%s rendered=%s appsSummary=%v statusApps=%v specApps=%v conditions=%v ===\n",
					label,
					owner,
					device.Status.Updated.Status,
					device.Status.Config.RenderedVersion,
					device.Status.ApplicationsSummary,
					device.Status.Applications,
					specApps,
					device.Status.Conditions,
				)
			} else {
				GinkgoWriter.Printf("=== device (%s) owner=%s status=nil specApps=%v ===\n", label, owner, specApps)
			}
		}
	}
	if agent == nil {
		return
	}
	logCtx, logCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer logCancel()
	logs, err := agent.GetAgentLogs(logCtx)
	if err != nil {
		GinkgoWriter.Printf("=== flightctl-agent logs (%s): get failed: %v ===\n", label, err)
		return
	}
	GinkgoWriter.Printf("=== flightctl-agent logs (%s) ===\n%s\n", label, logs)
}
