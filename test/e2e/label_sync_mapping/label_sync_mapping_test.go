package label_sync_mapping_test

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"sigs.k8s.io/yaml"
)

const (
	labelSyncMappingAPIVersion = "flightctl.io/v1beta1"
	labelSyncMappingTimeout    = 5 * time.Minute
	labelSyncMappingPolling    = 125 * time.Millisecond
	statusFixtureDir           = "/var/lib/flightctl/e2e-system-info"
	statusFixtureValue         = statusFixtureDir + "/value"
	statusFixtureKey           = "e2eStatusValue"
	agentConfigPath            = "/etc/flightctl/config.yaml"

	customSystemInfoConfig = `
system-info-custom:
 - siteName
 - emptyValue
 - e2eStatusValue
`
	systemInfoDisabledConfig = `
system-info: []
system-info-custom: []
`
)

var _ = Describe("LabelSyncMapping resource", func() {
	It("When mappings are added, collide, and are removed it should reconcile labels through the periodic worker", Label("sanity", "agent"), func() {
		harness := e2e.GetWorkerHarness()
		testSuffix := labelSyncTestSuffix(harness.GetTestIDFromContext())
		scalarKey := fmt.Sprintf("e2e.flightctl.io/%s-site-scalar", testSuffix)
		mapKey := fmt.Sprintf("e2e.flightctl.io/%s-site-map", testSuffix)
		compositeKey := fmt.Sprintf("e2e.flightctl.io/%s-platform", testSuffix)
		scalarMappingName := fmt.Sprintf("e2e-%s-site-scalar", testSuffix)
		mapMappingName := fmt.Sprintf("e2e-%s-site-map", testSuffix)
		compositeMappingName := fmt.Sprintf("e2e-%s-platform", testSuffix)
		mappingNames := make([]string, 0, 5)
		var deviceID string

		DeferCleanup(func() {
			if CurrentSpecReport().Failed() && deviceID != "" {
				device, err := harness.GetDevice(deviceID)
				if err != nil {
					GinkgoWriter.Printf("failed to retrieve device %q label-sync condition: %v\n", deviceID, err)
				} else if device.Status != nil {
					condition := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceLabelsSynced)
					GinkgoWriter.Printf("device %q LabelsSynced condition: %+v\n", deviceID, condition)
				}
			}
			for _, mappingName := range mappingNames {
				Expect(harness.DeleteLabelSyncMapping(mappingName)).To(Succeed())
			}
			for _, mappingName := range mappingNames {
				harness.WaitForLabelSyncMappingDeleted(mappingName, labelSyncMappingTimeout, labelSyncMappingPolling)
			}
		})

		By("Preparing a deterministic custom-info value before enrollment")
		Expect(writeStatusFixtureValue(harness, "site-a")).To(Succeed())
		Expect(updateAgentConfigFile(harness, customSystemInfoConfig)).To(Succeed())
		deviceID = harness.StartVMAndEnroll()

		By("Waiting for the device to report customInfo and basic systemInfo")
		var systemInfo *v1beta1.DeviceSystemInfo
		Eventually(func() bool {
			systemInfo = harness.GetDeviceSystemInfo(deviceID)
			if systemInfo == nil || systemInfo.CustomInfo == nil {
				return false
			}
			customInfo := *systemInfo.CustomInfo
			return customInfo[statusFixtureKey] == "site-a" &&
				customInfo["siteName"] == "my site" &&
				customInfo["emptyValue"] == "" &&
				len(customInfo) == 3 &&
				systemInfo.OperatingSystem != "" &&
				systemInfo.Architecture != ""
		}, e2e.TIMEOUT, e2e.POLLING).Should(BeTrue(), "the agent should report e2eStatusValue, operatingSystem, and architecture")
		platformValue := systemInfo.OperatingSystem + "-" + systemInfo.Architecture

		By("Creating scalar, map, and composite systemInfo mappings")
		scalarMapping := labelSyncMapping(scalarMappingName, &scalarKey,
			`has(status.systemInfo.customInfo) && status.systemInfo.customInfo != null && "e2eStatusValue" in status.systemInfo.customInfo ? status.systemInfo.customInfo.e2eStatusValue : dyn(null)`)
		mapMapping := labelSyncMapping(mapMappingName, nil,
			fmt.Sprintf(`has(status.systemInfo.customInfo) && status.systemInfo.customInfo != null && "e2eStatusValue" in status.systemInfo.customInfo ? {"%s": status.systemInfo.customInfo.e2eStatusValue} : {}`, mapKey))
		compositeMapping := labelSyncMapping(compositeMappingName, &compositeKey,
			`has(status.systemInfo.operatingSystem) && status.systemInfo.operatingSystem != "" &&
			 has(status.systemInfo.architecture) && status.systemInfo.architecture != "" ?
			 status.systemInfo.operatingSystem + "-" + status.systemInfo.architecture : dyn(null)`)

		for _, mapping := range []v1beta1.LabelSyncMapping{scalarMapping, mapMapping, compositeMapping} {
			mappingName := *mapping.Metadata.Name
			mappingNames = append(mappingNames, mappingName)
			Expect(harness.CreateLabelSyncMapping(mapping)).To(Succeed())
		}

		for _, mappingName := range mappingNames {
			harness.WaitForLabelSyncMappingReady(mappingName, v1beta1.ConditionStatusTrue, "Success", labelSyncMappingTimeout, labelSyncMappingPolling)
		}

		By("Verifying the mapped customInfo and composite systemInfo labels")
		harness.WaitForDeviceLabels(deviceID, "scalar, map, and composite labels are promoted", map[string]string{
			scalarKey:    "site-a",
			mapKey:       "site-a",
			compositeKey: platformValue,
		}, e2e.TIMEOUT)

		By("Changing the custom-info report without re-enrolling the device")
		Expect(writeStatusFixtureValue(harness, "site-b")).To(Succeed())
		Expect(harness.ResetAgent()).To(Succeed())
		Eventually(func() string {
			return customInfoValue(harness, deviceID, statusFixtureKey)
		}, e2e.TIMEOUT, e2e.POLLING).Should(Equal("site-b"))

		harness.WaitForDeviceLabels(deviceID, "scalar and map labels reflect the updated customInfo value", map[string]string{
			scalarKey: "site-b",
			mapKey:    "site-b",
		}, e2e.TIMEOUT)

		By("Removing customInfo and verifying only its mapped outputs disappear")
		Expect(updateAgentConfigFile(harness, systemInfoDisabledConfig)).To(Succeed())
		Eventually(func() string {
			return customInfoValue(harness, deviceID, statusFixtureKey)
		}, e2e.TIMEOUT, e2e.POLLING).Should(BeEmpty())

		harness.WaitForDeviceLabels(deviceID, "customInfo labels are removed while the composite label remains", map[string]string{
			compositeKey: platformValue,
		}, e2e.TIMEOUT, scalarKey, mapKey)

		collisionKey := fmt.Sprintf("e2e.flightctl.io/%s-collision", testSuffix)
		collisionExtraKey := fmt.Sprintf("e2e.flightctl.io/%s-collision-extra", testSuffix)
		collisionMapName := fmt.Sprintf("e2e-%s-collision-map", testSuffix)
		collisionScalarName := fmt.Sprintf("e2e-%s-collision-scalar", testSuffix)
		collisionMap := labelSyncMapping(collisionMapName, nil,
			fmt.Sprintf(`{"%s": "map-value", "%s": "map-extra"}`, collisionKey, collisionExtraKey))
		collisionScalar := labelSyncMapping(collisionScalarName, &collisionKey, "status.systemInfo.architecture")

		By("Adding mappings that compete for the same output key")
		for _, mapping := range []v1beta1.LabelSyncMapping{collisionMap, collisionScalar} {
			mappingName := *mapping.Metadata.Name
			mappingNames = append(mappingNames, mappingName)
			Expect(harness.CreateLabelSyncMapping(mapping)).To(Succeed())
		}

		By("Waiting for the periodic scan to mark both colliding mappings degraded")
		for _, mappingName := range []string{collisionMapName, collisionScalarName} {
			harness.WaitForLabelSyncMappingReady(mappingName, v1beta1.ConditionStatusFalse, "Degraded", labelSyncMappingTimeout, labelSyncMappingPolling)
		}

		By("Verifying the device condition identifies the mapping collision")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(deviceID)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(device.Status).NotTo(BeNil())
			condition := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceLabelsSynced)
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Status).To(Equal(v1beta1.ConditionStatusFalse))
			g.Expect(condition.Reason).To(Equal("ReconciliationFailed"))
			g.Expect(condition.Message).To(And(
				ContainSubstring(collisionKey),
				ContainSubstring(collisionMapName),
				ContainSubstring(collisionScalarName),
				ContainSubstring("emitted by multiple mappings"),
			))
		}, labelSyncMappingTimeout, labelSyncMappingPolling).Should(Succeed())

		harness.WaitForLabelSyncMappingReady(compositeMappingName, v1beta1.ConditionStatusTrue, "Success", labelSyncMappingTimeout, labelSyncMappingPolling)
		harness.WaitForDeviceLabels(deviceID, "the independent composite label remains promoted during the collision", map[string]string{
			compositeKey: platformValue,
		}, e2e.TIMEOUT)

		By("Deleting one colliding mapping and waiting for the remaining map mapping to recover")
		Expect(harness.DeleteLabelSyncMapping(collisionScalarName)).To(Succeed())
		harness.WaitForLabelSyncMappingDeleted(collisionScalarName, labelSyncMappingTimeout, labelSyncMappingPolling)
		harness.WaitForLabelSyncMappingReady(collisionMapName, v1beta1.ConditionStatusTrue, "Success", labelSyncMappingTimeout, labelSyncMappingPolling)
		harness.WaitForDeviceLabels(deviceID, "the surviving map mapping promotes all of its outputs", map[string]string{
			collisionKey:      "map-value",
			collisionExtraKey: "map-extra",
			compositeKey:      platformValue,
		}, e2e.TIMEOUT)

		By("Verifying the device condition reports recovery and clears the collision details")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(deviceID)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(device.Status).NotTo(BeNil())
			condition := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceLabelsSynced)
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Status).To(Equal(v1beta1.ConditionStatusTrue))
			g.Expect(condition.Reason).To(Equal("Reconciled"))
			g.Expect(condition.Message).NotTo(Or(
				ContainSubstring(collisionKey),
				ContainSubstring(collisionMapName),
				ContainSubstring(collisionScalarName),
				ContainSubstring("emitted by multiple mappings"),
			))
		}, labelSyncMappingTimeout, labelSyncMappingPolling).Should(Succeed())

		By("Deleting a good mapping and verifying periodic cleanup preserves the other outputs")
		Expect(harness.DeleteLabelSyncMapping(compositeMappingName)).To(Succeed())
		harness.WaitForLabelSyncMappingDeleted(compositeMappingName, labelSyncMappingTimeout, labelSyncMappingPolling)
		for _, mappingName := range []string{scalarMappingName, mapMappingName, collisionMapName} {
			harness.WaitForLabelSyncMappingReady(mappingName, v1beta1.ConditionStatusTrue, "Success", labelSyncMappingTimeout, labelSyncMappingPolling)
		}
		harness.WaitForDeviceLabels(deviceID, "deleted mapping labels are removed while surviving mappings remain", map[string]string{
			collisionKey:      "map-value",
			collisionExtraKey: "map-extra",
		}, e2e.TIMEOUT, scalarKey, mapKey, compositeKey)

		device, err := harness.GetDevice(deviceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(lo.FromPtr(device.Metadata.Labels)).To(HaveKeyWithValue("test-id", harness.GetTestIDFromContext()))
	})
})

// labelSyncTestSuffix returns a short, DNS-compatible suffix from the generated test ID.
func labelSyncTestSuffix(testID string) string {
	suffix := strings.ReplaceAll(strings.TrimPrefix(testID, "test-"), "-", "")
	return suffix[:8]
}

// labelSyncMapping builds a device mapping with the supplied output mode and CEL expression.
func labelSyncMapping(name string, key *string, expression string) v1beta1.LabelSyncMapping {
	return v1beta1.LabelSyncMapping{
		ApiVersion: labelSyncMappingAPIVersion,
		Kind:       v1beta1.LabelSyncMappingKindLabelSyncMapping,
		Metadata:   v1beta1.ObjectMeta{Name: &name},
		Spec: v1beta1.LabelSyncMappingSpec{
			ResourceType: v1beta1.LabelSyncMappingSpecResourceTypeDevice,
			Key:          key,
			Expression:   expression,
		},
	}
}

// customInfoValue returns one reported customInfo entry, or an empty string when it is absent.
func customInfoValue(harness *e2e.Harness, deviceID, key string) string {
	systemInfo := harness.GetDeviceSystemInfo(deviceID)
	if systemInfo == nil || systemInfo.CustomInfo == nil {
		return ""
	}
	return (*systemInfo.CustomInfo)[key]
}

// updateAgentConfigFile replaces the supplied top-level settings and reloads the agent.
func updateAgentConfigFile(harness *e2e.Harness, config string) error {
	current, err := harness.VM.RunSSH([]string{"sudo", "cat", agentConfigPath}, nil)
	if err != nil {
		return fmt.Errorf("failed to read agent config: %w", err)
	}
	updated, err := mergeAgentConfig(current.Bytes(), config)
	if err != nil {
		return err
	}
	if err := harness.WriteAgentFile(agentConfigPath, string(updated)); err != nil {
		return err
	}
	if err := harness.ResetAgent(); err != nil {
		return fmt.Errorf("failed to reload agent config: %w", err)
	}
	return nil
}

// mergeAgentConfig preserves unrelated settings and explicit empty collection lists.
func mergeAgentConfig(current []byte, updates string) ([]byte, error) {
	config := make(map[string]any)
	if err := yaml.Unmarshal(current, &config); err != nil {
		return nil, fmt.Errorf("failed to parse agent config: %w", err)
	}
	var overrides map[string]any
	if err := yaml.Unmarshal([]byte(updates), &overrides); err != nil {
		return nil, fmt.Errorf("failed to parse agent config updates: %w", err)
	}
	maps.Copy(config, overrides)
	updated, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal agent config: %w", err)
	}
	return updated, nil
}

// writeStatusFixtureValue updates the writable input read by the image-baked custom-info collector.
func writeStatusFixtureValue(harness *e2e.Harness, value string) error {
	if _, err := harness.RunScriptOnVM("install -d -m 0755 " + statusFixtureDir); err != nil {
		return fmt.Errorf("create custom-info value directory: %w", err)
	}
	if err := harness.WriteAgentFile(statusFixtureValue, value); err != nil {
		return fmt.Errorf("write custom-info value: %w", err)
	}
	return nil
}
