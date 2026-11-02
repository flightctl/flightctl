package label_sync_mapping

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Label sync mapping audit events", Serial, Label("integration", "sanity", "mapping-audit"), func() {
	It("reports effective label additions, changes, and removals without reporting no-ops", func() {
		harness := e2e.GetWorkerHarness()
		mappingName := uniqueName(harness, "audit-map")
		deviceName := uniqueName(harness, "audit-device")
		effectiveKey := "e2e.flightctl.io/" + uniqueName(harness, "effective")

		By("creating a device before the mapping to capture its event baseline")
		createDeviceWithStatus(harness, deviceName, map[string]string{"operator": "kept"}, "amd64", "linux", nil)
		waitForDeviceLabelsSynced(harness, deviceName)
		baseline := labelUpdateEventCount(harness, deviceName)

		By("adding a mapping-owned value and checking its audit event")
		createLabelSyncMapping(harness, mappingName, &effectiveKey, scopedScalarExpression(harness, "status.systemInfo.architecture"))
		waitForDeviceLabels(harness, deviceName, map[string]string{effectiveKey: "amd64"})
		waitForMappingReady(harness, mappingName)
		waitForDeviceProvenance(harness, deviceName, effectiveKey, mappingName)
		Eventually(func(g Gomega) {
			g.Expect(labelUpdateEventCount(harness, deviceName)).To(BeNumerically(">", baseline))
		}).Should(Succeed())
		addedCount := labelUpdateEventCount(harness, deviceName)

		By("changing the effective value and verifying another label update event")
		replaceDeviceStatus(harness, deviceName, "arm64", "linux", nil)
		waitForDeviceLabels(harness, deviceName, map[string]string{effectiveKey: "arm64"})
		Eventually(func(g Gomega) {
			g.Expect(labelUpdateEventCount(harness, deviceName)).To(BeNumerically(">", addedCount))
		}).Should(Succeed())
		changedCount := labelUpdateEventCount(harness, deviceName)

		By("reconciling an equivalent expression without a label-value change")
		updatedMapping := replaceLabelSyncMapping(
			harness,
			mappingName,
			&effectiveKey,
			scopedScalarExpression(harness, `status.systemInfo.architecture == "amd64" ? "amd64" : status.systemInfo.architecture`),
		)
		Expect(updatedMapping.Metadata.Generation).NotTo(BeNil())
		waitForMappingGenerationReady(harness, mappingName, *updatedMapping.Metadata.Generation)
		Consistently(func() int {
			return labelUpdateEventCount(harness, deviceName)
		}).WithTimeout(5 * apiPollingInterval).WithPolling(apiPollingInterval).Should(Equal(changedCount))

		By("removing the promoted value and verifying the removal event")
		deleteLabelSyncMapping(harness, mappingName)
		waitForDeviceLabels(harness, deviceName, map[string]string{"operator": "kept"}, effectiveKey)
		Eventually(func(g Gomega) {
			g.Expect(labelUpdateEventCount(harness, deviceName)).To(BeNumerically(">", changedCount))
		}).Should(Succeed())
	})

	It("does not report a label-value change for same-value ownership takeover", func() {
		harness := e2e.GetWorkerHarness()
		mappingName := uniqueName(harness, "takeover-map")
		deviceName := uniqueName(harness, "takeover-device")
		ownedKey := "e2e.flightctl.io/" + uniqueName(harness, "takeover")

		By("creating an operator label equal to the eventual mapping output")
		createDeviceWithStatus(harness, deviceName, map[string]string{ownedKey: "amd64"}, "amd64", "linux", nil)
		waitForDeviceLabelsSynced(harness, deviceName)
		baseline := labelUpdateEventCount(harness, deviceName)

		By("transferring ownership without changing the effective label")
		createLabelSyncMapping(harness, mappingName, &ownedKey, scopedScalarExpression(harness, "status.systemInfo.architecture"))
		waitForMappingReady(harness, mappingName)
		waitForDeviceProvenance(harness, deviceName, ownedKey, mappingName)
		waitForDeviceLabels(harness, deviceName, map[string]string{ownedKey: "amd64"})
		Consistently(func() int {
			return labelUpdateEventCount(harness, deviceName)
		}).WithTimeout(5 * apiPollingInterval).WithPolling(apiPollingInterval).Should(Equal(baseline))
	})
})

func labelUpdateEventCount(harness *e2e.Harness, deviceName string) int {
	reason := api.EventReasonResourceUpdated
	events, err := harness.GetDeviceEvents(deviceName, &reason)
	Expect(err).NotTo(HaveOccurred())

	count := 0
	for _, event := range events {
		if event.Details == nil {
			continue
		}
		details, err := event.Details.AsResourceUpdatedDetails()
		if err != nil {
			continue
		}
		for _, field := range details.UpdatedFields {
			if field == api.Labels {
				count++
				break
			}
		}
	}
	return count
}

func waitForDeviceLabelsSynced(harness *e2e.Harness, deviceName string) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetDeviceWithResponse(harness.Context, deviceName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Status).NotTo(BeNil())

		synced := false
		for _, condition := range response.JSON200.Status.Conditions {
			if condition.Type == api.ConditionTypeDeviceLabelsSynced && condition.Status == api.ConditionStatusTrue && condition.Reason == "Reconciled" {
				synced = true
				break
			}
		}
		g.Expect(synced).To(BeTrue())
	}).Should(Succeed())
}

func waitForDeviceProvenance(harness *e2e.Harness, deviceName, key, mappingName string) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetDeviceLabelSyncProvenanceWithResponse(harness.Context, deviceName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(ownersForKey(response.JSON200.Items, key)).To(ConsistOf(mappingName))
	}).Should(Succeed())
}

func waitForMappingGenerationReady(harness *e2e.Harness, mappingName string, generation int64) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, mappingName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Status).NotTo(BeNil())
		g.Expect(response.JSON200.Status.Conditions).NotTo(BeNil())

		ready := false
		for _, condition := range *response.JSON200.Status.Conditions {
			if condition.Type == api.ConditionTypeLabelSyncMappingReady &&
				condition.Status == api.ConditionStatusTrue &&
				condition.ObservedGeneration != nil && *condition.ObservedGeneration >= generation {
				ready = true
				break
			}
		}
		g.Expect(ready).To(BeTrue())
	}).WithTimeout(apiEventuallyTimeout).WithPolling(apiPollingInterval).Should(Succeed())
}
