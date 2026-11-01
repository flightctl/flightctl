package label_sync_mapping

import (
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Label sync mapping promotion", Serial, Label("integration", "sanity", "mapping-promotion"), func() {
	It("promotes scalar, conditional, and transformed map values independently", func() {
		harness := e2e.GetWorkerHarness()
		suffix := uniqueName(harness, "labels")

		architectureKey := fmt.Sprintf("e2e.flightctl.io/architecture-%s", suffix)
		eligibleKey := fmt.Sprintf("e2e.flightctl.io/eligible-%s", suffix)
		regionKey := fmt.Sprintf("e2e.flightctl.io/region-%s", suffix)
		rackKey := fmt.Sprintf("e2e.flightctl.io/rack-%s", suffix)
		ignoredKey := fmt.Sprintf("e2e.flightctl.io/ignored-%s", suffix)
		failureKey := fmt.Sprintf("e2e.flightctl.io/failure-%s", suffix)

		architectureMapping := uniqueName(harness, "architecture")
		eligibleMapping := uniqueName(harness, "eligible")
		customInfoMapping := uniqueName(harness, "custom-info")
		failureMapping := uniqueName(harness, "failing-scalar")
		By("creating independent scalar, conditional, transformed map, and runtime-failing mappings")
		createLabelSyncMapping(harness, architectureMapping, &architectureKey, scopedScalarExpression(harness, "status.systemInfo.architecture"))
		createLabelSyncMapping(harness, eligibleMapping, &eligibleKey, scopedScalarExpression(harness, `status.systemInfo.architecture == "amd64" ? true : dyn(null)`))
		createLabelSyncMapping(
			harness,
			customInfoMapping,
			nil,
			scopedMapExpression(harness, fmt.Sprintf(
				`status.systemInfo.customInfo.transformMapEntry(k, v, k in ["region", "rack"], {"e2e.flightctl.io/" + k + "-%s": v})`,
				suffix,
			)),
		)
		createLabelSyncMapping(
			harness,
			failureMapping,
			&failureKey,
			scopedScalarExpression(harness, `status.systemInfo.architecture == "amd64" ? string(int(status.systemInfo.architecture)) : dyn(null)`),
		)

		matchingDevice := uniqueName(harness, "matching-device")
		nonmatchingDevice := uniqueName(harness, "nonmatching-device")
		By("reporting different system information from two devices")
		createDeviceWithStatus(harness, matchingDevice, map[string]string{"operator": "kept"}, "amd64", "linux", map[string]string{"region": "east", "rack": "r1", "ignored": "not-promoted"})
		createDeviceWithStatus(harness, nonmatchingDevice, map[string]string{"operator": "kept"}, "arm64", "linux", map[string]string{"region": "west", "rack": "r2", "ignored": "not-promoted"})

		By("waiting for independent valid outputs while the failing mapping degrades")
		waitForDeviceLabels(harness, matchingDevice, map[string]string{
			architectureKey: "amd64",
			eligibleKey:     "true",
			regionKey:       "east",
			rackKey:         "r1",
		}, failureKey, ignoredKey)
		waitForDeviceLabels(harness, nonmatchingDevice, map[string]string{
			architectureKey: "arm64",
			regionKey:       "west",
			rackKey:         "r2",
		}, eligibleKey, failureKey, ignoredKey)

		waitForMappingReady(harness, architectureMapping)
		waitForMappingReady(harness, eligibleMapping)
		waitForMappingReady(harness, customInfoMapping)
		waitForMappingDegraded(harness, failureMapping)

		By("verifying transformed-map ownership and ordinary label selection")
		provenance, err := harness.Client.GetDeviceLabelSyncProvenanceWithResponse(harness.Context, matchingDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(provenance.StatusCode()).To(Equal(http.StatusOK), string(provenance.Body))
		Expect(provenance.JSON200).NotTo(BeNil())
		Expect(ownersForKey(provenance.JSON200.Items, regionKey)).To(ConsistOf(customInfoMapping))

		selected := listDevicesByLabel(harness, fmt.Sprintf("%s=east", regionKey))
		Expect(selected).To(HaveLen(1))
		Expect(selected[0].Metadata.Name).NotTo(BeNil())
		Expect(*selected[0].Metadata.Name).To(Equal(matchingDevice))
		Expect(selected[0].Metadata.Labels).NotTo(BeNil())
		Expect(*selected[0].Metadata.Labels).To(HaveKeyWithValue(regionKey, "east"))
	})
})
