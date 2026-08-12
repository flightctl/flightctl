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
	packageModeEnrollTimeout      = 5 * time.Minute
	packageModePollInterval       = 5 * time.Second
	packageModeConfigPath         = "/etc/flightctl/package-mode-e2e.conf"
	packageModeConfigContent      = "package-mode-e2e=enabled"
	packageModeContainerAppName   = "package-mode-sleep"
	packageModeContainerImagePath = "flightctl-tests/nginx:1.28-alpine-slim"
	packageModeContainerImageRef  = "quay.io/flightctl-tests/nginx:1.28-alpine-slim"
	packageModeFleetLabelKey      = "package-mode-suite"
	packageModeNormalFleetPrefix  = "package-mode-normal"
	packageModeExpectedRejectText = e2e.PackageModeRejectMessage
)

var _ = Describe("Package-mode device scenarios", Ordered, func() {
	var (
		harness          *e2e.Harness
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

		startCtx, startCancel := context.WithTimeout(harness.GetTestContext(), 5*time.Minute)
		defer startCancel()
		packageModeAgent, err = e2e.StartPackageModeAgent(
			startCtx,
			agentConfigDir,
			packageModeAuxSvcs.Registry.Host,
			packageModeAuxSvcs.Registry.Port,
		)
		Expect(err).ToNot(HaveOccurred())

		deviceSpec, err := buildPackageModeFleetDeviceSpec(
			harness,
			packageModeAuxSvcs.Registry.Host,
			packageModeAuxSvcs.Registry.Port,
		)
		Expect(err).ToNot(HaveOccurred())

		err = harness.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
		Expect(err).ToNot(HaveOccurred())

		deviceID, err = enrollPackageModeAgent(harness, packageModeAgent, map[string]string{
			packageModeFleetLabelKey: fleetName,
		})
		Expect(err).ToNot(HaveOccurred())

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

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			contextFailed = true
		}
	})

	AfterAll(func() {
		if contextFailed {
			dumpPackageModeFailureDiagnostics(harness, packageModeAgent, deviceID, "package-mode suite")
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

		err := harness.WaitForApplicationStatus(deviceID, packageModeContainerAppName, v1beta1.ApplicationStatusRunning, testutil.LONG_TIMEOUT, testutil.POLLING)
		if err != nil {
			dumpPackageModeFailureDiagnostics(harness, packageModeAgent, deviceID, "package-mode app status wait")
		}
		Expect(err).ToNot(HaveOccurred())

		err = harness.WaitForApplicationSummary(deviceID, testutil.LONG_TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)
		if err != nil {
			dumpPackageModeFailureDiagnostics(harness, packageModeAgent, deviceID, "package-mode app summary wait")
		}
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) {
			readCtx, readCancel := context.WithTimeout(harness.GetTestContext(), 30*time.Second)
			defer readCancel()
			output, err := packageModeAgent.ReadFile(readCtx, packageModeConfigPath)
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
			g.Expect(application.Name).To(Equal(packageModeContainerAppName))
			g.Expect(application.Status).To(Equal(v1beta1.ApplicationStatusRunning))
		}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())
	})

	It("keeps the package-mode device OutOfDate when the fleet adds spec.os.image", Label("90148", "sanity"), func() {
		initialVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceID)
		Expect(err).ToNot(HaveOccurred())

		deviceSpec, err := harness.CreateFleetDeviceSpec(
			packageModeAuxSvcs.Registry.Host,
			packageModeAuxSvcs.Registry.Port,
			testutil.DeviceTags.V2,
		)
		Expect(err).ToNot(HaveOccurred())
		originalDeviceSpec, err := buildPackageModeFleetDeviceSpec(
			harness,
			packageModeAuxSvcs.Registry.Host,
			packageModeAuxSvcs.Registry.Port,
		)
		Expect(err).ToNot(HaveOccurred())

		err = harness.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
		Expect(err).ToNot(HaveOccurred())
		cleanupCtx := harness.GetTestContext()
		DeferCleanup(restorePackageModeFleetSpec, harness, cleanupCtx, fleetName, selector, originalDeviceSpec)

		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(deviceID)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(device).ToNot(BeNil())
			g.Expect(device.Status).ToNot(BeNil())
			g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))
			g.Expect(device.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusOnline))
			g.Expect(device.Status.Config.RenderedVersion).To(Equal(strconv.Itoa(initialVersion)))
		}, testutil.LONG_TIMEOUT, packageModePollInterval).Should(Succeed())

		Eventually(func(g Gomega) {
			logCtx, logCancel := context.WithTimeout(harness.GetTestContext(), 30*time.Second)
			defer logCancel()
			logs, err := packageModeAgent.GetAgentLogs(logCtx)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(logs).To(ContainSubstring(packageModeExpectedRejectText))
		}, testutil.LONG_TIMEOUT, packageModePollInterval).Should(Succeed())
	})
})

// enrollPackageModeAgent approves the helper container enrollment request and waits for the device to come online.
func enrollPackageModeAgent(harness *e2e.Harness, agent *e2e.PackageModeAgent, labels map[string]string) (string, error) {
	GinkgoHelper()

	if harness == nil {
		return "", fmt.Errorf("harness is nil")
	}
	if agent == nil {
		return "", fmt.Errorf("agent is nil")
	}

	enrollmentID, err := agent.GetEnrollmentID(harness.GetTestContext(), packageModeEnrollTimeout)
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

	deadline := time.Now().Add(packageModeEnrollTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		device, err := harness.CheckDeviceStatus(enrollmentID, v1beta1.DeviceSummaryStatusOnline)
		if err == nil && device != nil {
			return enrollmentID, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("device %s is still nil", enrollmentID)
		}
		time.Sleep(packageModePollInterval)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out waiting for device %s to become online", enrollmentID)
	}
	return "", lastErr
}

// mergeEnrollmentLabels combines harness-owned test labels with caller-provided labels for approval.
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

// buildPackageModeFleetDeviceSpec returns the package-mode fleet spec used by the happy-path suite setup.
func buildPackageModeFleetDeviceSpec(harness *e2e.Harness, registryHost, registryPort string) (v1beta1.DeviceSpec, error) {
	if harness == nil {
		return v1beta1.DeviceSpec{}, fmt.Errorf("harness is nil")
	}

	inlineConfig, err := e2e.NewInlineConfigSpec("package-mode-inline", []v1beta1.FileSpec{{
		Path:    packageModeConfigPath,
		Content: packageModeConfigContent,
	}})
	if err != nil {
		return v1beta1.DeviceSpec{}, err
	}

	appImage := packageModeContainerImageForFleet(registryHost, registryPort)
	containerApp, err := e2e.NewContainerApplicationSpec(
		packageModeContainerAppName,
		appImage,
		nil,
		nil,
		nil,
		nil,
	)
	if err != nil {
		return v1beta1.DeviceSpec{}, err
	}

	return v1beta1.DeviceSpec{
		Config:       &[]v1beta1.ConfigProviderSpec{inlineConfig},
		Applications: &[]v1beta1.ApplicationProviderSpec{containerApp},
	}, nil
}

// uniqueFleetName returns a unique fleet name for the current test process.
func uniqueFleetName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// packageModeContainerImageForFleet returns the plain container image used by package-mode app tests.
func packageModeContainerImageForFleet(registryHost, registryPort string) string {
	if registryHost == "" || registryPort == "" {
		return packageModeContainerImageRef
	}
	return fmt.Sprintf("%s:%s/%s", registryHost, registryPort, packageModeContainerImagePath)
}

// restorePackageModeFleetSpec restores the original package-mode fleet spec under the captured spec context.
func restorePackageModeFleetSpec(harness *e2e.Harness, cleanupCtx context.Context, fleetName string, selector v1beta1.LabelSelector, deviceSpec v1beta1.DeviceSpec) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}

	restoreCtx := harness.GetTestContext()
	harness.SetTestContext(cleanupCtx)
	defer harness.SetTestContext(restoreCtx)

	return harness.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
}

// setHarnessSetupContext swaps in a dedicated setup context and returns the previous one for restoration.
func setHarnessSetupContext(harness *e2e.Harness, prefix string) context.Context {
	restoreContext := harness.GetTestContext()
	setupContext := context.WithValue(restoreContext, testutil.TestIDKey, uniqueFleetName(prefix+"-setup"))
	harness.SetTestContext(setupContext)
	return restoreContext
}

// findDeviceApplicationStatusByName returns the named application status entry if it exists.
func findDeviceApplicationStatusByName(applications []v1beta1.DeviceApplicationStatus, applicationName string) *v1beta1.DeviceApplicationStatus {
	for i := range applications {
		if applications[i].Name == applicationName {
			return &applications[i]
		}
	}
	return nil
}

// deviceHasNamedApplicationSpec reports whether the rendered device spec includes the named application.
func deviceHasNamedApplicationSpec(applications []v1beta1.ApplicationProviderSpec, applicationName string) bool {
	for _, app := range applications {
		name, err := app.GetName()
		if err != nil {
			GinkgoWriter.Printf("deviceHasNamedApplicationSpec: GetName failed: %v\n", err)
			continue
		}
		if name != nil && *name == applicationName {
			return true
		}
	}
	return false
}

// dumpPackageModeFailureDiagnostics writes device status and filtered agent logs for failed package-mode specs.
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
