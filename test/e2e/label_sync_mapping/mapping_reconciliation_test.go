package label_sync_mapping

import (
	"fmt"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Label sync mapping reconciliation", Serial, Label("integration", "sanity", "mapping-reconciliation"), func() {
	It("converges labels across status, mapping, deletion, and recreation changes", func() {
		harness := e2e.GetWorkerHarness()
		suffix := uniqueName(harness, "reconcile")

		initialKey := fmt.Sprintf("e2e.flightctl.io/initial-%s", suffix)
		updatedKey := fmt.Sprintf("e2e.flightctl.io/updated-%s", suffix)
		recreatedKey := fmt.Sprintf("e2e.flightctl.io/recreated-%s", suffix)
		regionKey := fmt.Sprintf("e2e.flightctl.io/region-%s", suffix)
		rackKey := fmt.Sprintf("e2e.flightctl.io/rack-%s", suffix)
		modeChangedKey := fmt.Sprintf("e2e.flightctl.io/mode-%s", suffix)
		scalarMapping := uniqueName(harness, "changing-scalar")
		mapMapping := uniqueName(harness, "changing-map")

		By("creating scalar and transformed-map mappings")
		createLabelSyncMapping(
			harness,
			scalarMapping,
			&initialKey,
			scopedScalarExpression(harness, `status.systemInfo.architecture == "amd64" ? status.systemInfo.architecture : dyn(null)`),
		)
		createLabelSyncMapping(
			harness,
			mapMapping,
			nil,
			scopedMapExpression(harness, fmt.Sprintf(
				`status.systemInfo.customInfo.transformMapEntry(k, v, k in ["region", "rack"], {"e2e.flightctl.io/" + k + "-%s": v})`,
				suffix,
			)),
		)

		deviceName := uniqueName(harness, "changing-device")
		By("reporting initial status and waiting for promoted labels")
		createDeviceWithStatus(
			harness,
			deviceName,
			map[string]string{"operator": "preserved"},
			"amd64",
			"linux",
			map[string]string{"region": "east", "rack": "r1"},
		)
		waitForDeviceLabels(harness, deviceName, map[string]string{
			"operator": "preserved",
			initialKey: "amd64",
			regionKey:  "east",
			rackKey:    "r1",
		})
		waitForMappingReady(harness, scalarMapping)
		waitForMappingReady(harness, mapMapping)

		Expect(listDevicesByLabel(harness, fmt.Sprintf("%s=east", regionKey))).To(HaveLen(1))

		By("changing status without re-enrollment and verifying selector movement")
		replaceDeviceStatus(harness, deviceName, "arm64", "linux", map[string]string{"rack": "r2"})
		waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", rackKey: "r2"},
			initialKey,
			regionKey,
		)
		Expect(listDevicesByLabel(harness, fmt.Sprintf("%s=east", regionKey))).To(BeEmpty())
		Expect(listDevicesByLabel(harness, fmt.Sprintf("%s=r2", rackKey))).To(HaveLen(1))

		By("changing scalar destination and expression inputs")
		replaceLabelSyncMapping(harness, scalarMapping, &updatedKey, scopedScalarExpression(harness, "status.systemInfo.architecture"))
		waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", updatedKey: "arm64"},
			initialKey,
		)
		waitForMappingReady(harness, scalarMapping)

		By("removing all map inputs and changing the mapping from map to scalar mode")
		replaceDeviceStatus(harness, deviceName, "arm64", "linux", map[string]string{})
		waitForDeviceLabels(harness, deviceName, map[string]string{"operator": "preserved", updatedKey: "arm64"}, regionKey, rackKey)

		replaceLabelSyncMapping(harness, mapMapping, &modeChangedKey, scopedScalarExpression(harness, `"mode-changed"`))
		waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", updatedKey: "arm64", modeChangedKey: "mode-changed"},
			regionKey,
			rackKey,
		)
		waitForMappingReady(harness, mapMapping)

		By("deleting and recreating the scalar mapping with the same name")
		deleteLabelSyncMapping(harness, scalarMapping)
		waitForDeviceLabels(harness, deviceName, map[string]string{"operator": "preserved", modeChangedKey: "mode-changed"}, updatedKey)
		waitForMappingDeleted(harness, scalarMapping)

		createLabelSyncMapping(harness, scalarMapping, &recreatedKey, scopedScalarExpression(harness, "status.systemInfo.operatingSystem"))
		waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", recreatedKey: "linux", modeChangedKey: "mode-changed"},
			initialKey,
			updatedKey,
			regionKey,
		)
		waitForMappingReady(harness, scalarMapping)
	})
})
