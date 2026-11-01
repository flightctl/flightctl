package label_sync_mapping

import (
	"fmt"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("Label sync mapping reconciliation", Label("integration", "mapping-reconciliation"), func() {
	It("converges labels across status, mapping, deletion, and recreation changes", func() {
		harness := e2e.GetWorkerHarness()
		suffix := uniqueName(harness, "reconcile")

		initialKey := fmt.Sprintf("e2e.flightctl.io/initial-%s", suffix)
		updatedKey := fmt.Sprintf("e2e.flightctl.io/updated-%s", suffix)
		recreatedKey := fmt.Sprintf("e2e.flightctl.io/recreated-%s", suffix)
		regionKey := fmt.Sprintf("e2e.flightctl.io/region-%s", suffix)
		scalarMapping := uniqueName(harness, "changing-scalar")
		mapMapping := uniqueName(harness, "changing-map")

		createLabelSyncMapping(
			harness,
			scalarMapping,
			&initialKey,
			`status.systemInfo.architecture == "amd64" ? status.systemInfo.architecture : dyn(null)`,
		)
		createLabelSyncMapping(
			harness,
			mapMapping,
			nil,
			fmt.Sprintf(`{"%s": status.systemInfo.customInfo["region"]}`, regionKey),
		)

		deviceName := uniqueName(harness, "changing-device")
		createDeviceWithStatus(
			harness,
			deviceName,
			map[string]string{"operator": "preserved"},
			"amd64",
			"linux",
			map[string]string{"region": "east"},
		)
		waitForDeviceLabels(harness, deviceName, map[string]string{
			"operator": "preserved",
			initialKey: "amd64",
			regionKey:  "east",
		})
		waitForMappingReady(harness, scalarMapping)
		waitForMappingReady(harness, mapMapping)

		replaceDeviceStatus(harness, deviceName, "arm64", "linux", map[string]string{})
		waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved"},
			initialKey,
			regionKey,
		)

		replaceLabelSyncMapping(harness, scalarMapping, &updatedKey, "status.systemInfo.architecture")
		waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", updatedKey: "arm64"},
			initialKey,
		)
		waitForMappingReady(harness, scalarMapping)

		deleteLabelSyncMapping(harness, scalarMapping)
		waitForDeviceLabels(harness, deviceName, map[string]string{"operator": "preserved"}, updatedKey)
		waitForMappingDeleted(harness, scalarMapping)

		createLabelSyncMapping(harness, scalarMapping, &recreatedKey, "status.systemInfo.operatingSystem")
		waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", recreatedKey: "linux"},
			initialKey,
			updatedKey,
			regionKey,
		)
		waitForMappingReady(harness, scalarMapping)
	})
})
