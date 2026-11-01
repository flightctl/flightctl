package label_sync_mapping

import (
	"fmt"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Label sync mapping promotion", Label("integration", "mapping-promotion"), func() {
	It("promotes scalar, conditional, and transformed map values independently", func() {
		harness := e2e.GetWorkerHarness()
		suffix := uniqueName(harness, "labels")

		architectureKey := fmt.Sprintf("e2e.flightctl.io/architecture-%s", suffix)
		eligibleKey := fmt.Sprintf("e2e.flightctl.io/eligible-%s", suffix)
		regionKey := fmt.Sprintf("e2e.flightctl.io/region-%s", suffix)
		rackKey := fmt.Sprintf("e2e.flightctl.io/rack-%s", suffix)

		architectureMapping := uniqueName(harness, "architecture")
		eligibleMapping := uniqueName(harness, "eligible")
		customInfoMapping := uniqueName(harness, "custom-info")
		createLabelSyncMapping(harness, architectureMapping, &architectureKey, "status.systemInfo.architecture")
		createLabelSyncMapping(harness, eligibleMapping, &eligibleKey, `status.systemInfo.architecture == "amd64" ? true : dyn(null)`)
		createLabelSyncMapping(
			harness,
			customInfoMapping,
			nil,
			fmt.Sprintf(
				`status.systemInfo.customInfo.transformMapEntry(k, v, k in ["region", "rack"], {"e2e.flightctl.io/" + k + "-%s": v})`,
				suffix,
			),
		)

		matchingDevice := uniqueName(harness, "matching-device")
		nonmatchingDevice := uniqueName(harness, "nonmatching-device")
		createDeviceWithStatus(harness, matchingDevice, map[string]string{"operator": "kept"}, "amd64", "linux", map[string]string{"region": "east", "rack": "r1"})
		createDeviceWithStatus(harness, nonmatchingDevice, map[string]string{"operator": "kept"}, "arm64", "linux", map[string]string{"region": "west", "rack": "r2"})

		waitForDeviceLabels(harness, matchingDevice, map[string]string{
			architectureKey: "amd64",
			eligibleKey:     "true",
			regionKey:       "east",
			rackKey:         "r1",
		})
		waitForDeviceLabels(harness, nonmatchingDevice, map[string]string{
			architectureKey: "arm64",
			regionKey:       "west",
			rackKey:         "r2",
		}, eligibleKey)

		waitForMappingReady(harness, architectureMapping)
		waitForMappingReady(harness, eligibleMapping)
		waitForMappingReady(harness, customInfoMapping)

		selected := listDevicesByLabel(harness, fmt.Sprintf("%s=east", regionKey))
		Expect(selected).To(HaveLen(1))
		Expect(selected[0].Metadata.Name).NotTo(BeNil())
		Expect(*selected[0].Metadata.Name).To(Equal(matchingDevice))
	})
})
