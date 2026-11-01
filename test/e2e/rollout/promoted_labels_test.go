package rollout_test

import (
	"fmt"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Promoted label runtime selection", Serial, Label("integration", "sanity", "rollout"), func() {
	var rolloutTC *TestContext

	AfterEach(func() {
		if rolloutTC == nil {
			return
		}
		for _, vmHarness := range rolloutTC.harnesses {
			if vmHarness != nil {
				Expect(vmHarness.CleanUpAllTestResources()).To(Succeed())
				e2e.Cleanup(vmHarness)
			}
		}
		rolloutTC = nil
	})

	It("selects scalar and map promoted labels with ordinary fleet selectors", func() {
		harness := e2e.GetWorkerHarness()
		suffix := promotedTestSuffix(harness)
		architectureKey := fmt.Sprintf("e2e.flightctl.io/architecture-%s", suffix)
		regionKey := fmt.Sprintf("e2e.flightctl.io/region-%s", suffix)
		architectureMapping := "architecture-" + suffix
		regionMapping := "region-" + suffix

		By("creating scoped scalar and map mappings")
		promotedCreateMapping(harness, architectureMapping, &architectureKey,
			promotedScalarExpression(harness, "status.systemInfo.architecture"))
		promotedCreateMapping(harness, regionMapping, nil,
			promotedMapExpression(harness, fmt.Sprintf(`{"%s": status.systemInfo.customInfo["region"]}`, regionKey)))

		deviceA := "scalar-" + suffix
		deviceB := "map-" + suffix
		deviceC := "static-" + suffix
		By("reporting distinct status values on three devices")
		promotedCreateDeviceWithStatus(harness, deviceA, map[string]string{}, "amd64", "east")
		promotedCreateDeviceWithStatus(harness, deviceB, map[string]string{}, "arm64", "west")
		promotedCreateDeviceWithStatus(harness, deviceC, map[string]string{"static-test": suffix}, "arm64", "south")

		By("waiting for both mappings and promoted labels to settle")
		promotedWaitForLabels(harness, deviceA, map[string]string{architectureKey: "amd64", regionKey: "east"})
		promotedWaitForLabels(harness, deviceB, map[string]string{architectureKey: "arm64", regionKey: "west"})
		promotedWaitForLabels(harness, deviceC, map[string]string{architectureKey: "arm64", regionKey: "south"})
		promotedWaitForMappingReady(harness, architectureMapping)
		promotedWaitForMappingReady(harness, regionMapping)

		scalarFleet := "scalar-" + suffix
		mapFleet := "map-" + suffix
		staticFleet := "static-" + suffix
		By("creating disjoint fleets using promoted and static selectors")
		Expect(harness.CreateFleetWithSelector(scalarFleet, map[string]string{architectureKey: "amd64"})).To(Succeed())
		Expect(harness.CreateFleetWithSelector(mapFleet, map[string]string{regionKey: "west"})).To(Succeed())
		Expect(harness.CreateFleetWithSelector(staticFleet, map[string]string{"static-test": suffix})).To(Succeed())

		By("checking ownership and ordinary label queries")
		promotedWaitForOwner(harness, deviceA, scalarFleet)
		promotedWaitForOwner(harness, deviceB, mapFleet)
		promotedWaitForOwner(harness, deviceC, staticFleet)
		promotedExpectSelectedDevice(harness, architectureKey+"=amd64", deviceA)
		promotedExpectSelectedDevice(harness, regionKey+"=west", deviceB)

		By("changing an unrelated promoted value without moving the static-only device")
		promotedReplaceDeviceStatus(harness, deviceC, "arm64", "north")
		promotedWaitForLabels(harness, deviceC, map[string]string{architectureKey: "arm64", regionKey: "north"})
		promotedWaitForOwner(harness, deviceC, staticFleet)
	})

	It("renders promoted labels only after ownership settles and rejects raw status variables", func() {
		harness := e2e.GetWorkerHarness()
		suffix := promotedTestSuffix(harness)
		regionKey := fmt.Sprintf("e2e.flightctl.io/region-%s", suffix)
		mappingName := "render-region-" + suffix
		deviceName := "render-" + suffix
		fleetName := "render-" + suffix
		configName := "selected-region"

		By("creating a map mapping and a fleet whose template reads the promoted label")
		promotedCreateMapping(harness, mappingName, nil,
			promotedMapExpression(harness, fmt.Sprintf(`{"%s": status.systemInfo.customInfo["region"]}`, regionKey)))
		config := promotedInlineConfig(configName, fmt.Sprintf(`{{ index .metadata.labels %q }}`, regionKey))
		fleetSpec := promotedFleetSpec(map[string]string{regionKey: "east"}, config)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, fleetSpec)).To(Succeed())

		By("checking that the device is not rendered before it reports the mapped status")
		_, err := resources.CreateDevice(harness, deviceName, &map[string]string{})
		Expect(err).NotTo(HaveOccurred())
		device, err := harness.GetDevice(deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(device.Metadata.Owner).To(BeNil())
		Expect(device.Spec).To(BeNil())

		By("reporting status and waiting for promotion, fleet ownership, and rendered config")
		promotedReplaceDeviceStatus(harness, deviceName, "amd64", "east")
		promotedWaitForLabels(harness, deviceName, map[string]string{regionKey: "east"})
		promotedWaitForMappingReady(harness, mappingName)
		promotedWaitForOwner(harness, deviceName, fleetName)
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(deviceName)
			g.Expect(err).NotTo(HaveOccurred())
			inline, err := harness.GetDeviceInlineConfig(device, configName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(inline.Inline).To(HaveLen(1))
			g.Expect(inline.Inline[0].Content).To(Equal("east"))
		}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())

		By("checking that raw device status is unavailable to the template renderer")
		invalidConfig := promotedInlineConfig(configName, `{{ .status.systemInfo.architecture }}`)
		Expect(harness.CreateOrUpdateTestFleet(fleetName,
			promotedFleetSpec(map[string]string{regionKey: "east"}, invalidConfig))).To(Succeed())
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(deviceName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(device.Metadata.Annotations).NotTo(BeNil())
			g.Expect(*device.Metadata.Annotations).To(HaveKey(api.DeviceAnnotationLastRolloutError))
			g.Expect((*device.Metadata.Annotations)[api.DeviceAnnotationLastRolloutError]).To(ContainSubstring("status"))
			inline, err := harness.GetDeviceInlineConfig(device, configName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(inline.Inline[0].Content).To(Equal("east"))
		}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
	})

	It("blocks rendering when a promoted label matches multiple fleets", func() {
		harness := e2e.GetWorkerHarness()
		suffix := promotedTestSuffix(harness)
		architectureKey := fmt.Sprintf("e2e.flightctl.io/architecture-%s", suffix)
		mappingName := "owners-" + suffix
		deviceName := "owners-" + suffix
		fleetA := "owner-a-" + suffix
		fleetB := "owner-b-" + suffix
		selector := map[string]string{architectureKey: "amd64"}

		By("creating two fleets that would both own the device after promotion")
		promotedCreateMapping(harness, mappingName, &architectureKey,
			promotedScalarExpression(harness, "status.systemInfo.architecture"))
		configA := promotedInlineConfig("owner-a-config", "owner-a")
		configB := promotedInlineConfig("owner-b-config", "owner-b")
		Expect(harness.CreateOrUpdateTestFleet(fleetA, promotedFleetSpec(selector, configA))).To(Succeed())
		Expect(harness.CreateOrUpdateTestFleet(fleetB, promotedFleetSpec(selector, configB))).To(Succeed())

		By("promoting a value that makes both fleets match")
		promotedCreateDeviceWithStatus(harness, deviceName, map[string]string{}, "amd64", "east")
		promotedWaitForLabels(harness, deviceName, map[string]string{architectureKey: "amd64"})
		promotedWaitForMappingReady(harness, mappingName)

		By("checking the public multiple-owner condition and lack of rendered config")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(deviceName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(device.Status).NotTo(BeNil())
			g.Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeTrue())
			g.Expect(device.Metadata.Owner).To(BeNil())
			g.Expect(device.Spec).To(BeNil())
		}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
	})

	It("uses changed promoted values at later rollout turns and records correct rollout events", func() {
		harness := e2e.GetWorkerHarness()
		rolloutTC = setupTestContext(harness.Context)
		tc := rolloutTC

		By("enrolling two devices for the first and final batches")
		Expect(tc.setupFleetAndDevices(harness.Context, 2, []map[string]string{
			{labelSite: siteMadrid},
			{labelSite: siteParis},
		})).To(Succeed())
		phaseKey := fmt.Sprintf("e2e.flightctl.io/phase-%s", promotedTestSuffix(harness))
		mappingName := "phase-" + promotedTestSuffix(harness)
		phaseExpression := func(parisValue string) string {
			return promotedScalarExpression(harness,
				fmt.Sprintf(`status.systemInfo.architecture != "" ? (metadata.labels["site"] == "madrid" ? "active" : %q) : dyn(null)`, parisValue))
		}

		By("promoting a status-gated phase label before the rollout starts")
		promotedCreateMapping(harness, mappingName, &phaseKey, phaseExpression("waiting"))
		promotedWaitForLabels(harness, tc.deviceIDs[0], map[string]string{phaseKey: "active"})
		promotedWaitForLabels(harness, tc.deviceIDs[1], map[string]string{phaseKey: "waiting"})
		promotedWaitForMappingReady(harness, mappingName)

		batchSequence := api.BatchSequence{Sequence: &[]api.Batch{
			{Selector: &api.LabelSelector{MatchLabels: &map[string]string{phaseKey: "active"}}, Limit: intLimit(1)},
		}}
		Expect(tc.updateAppVersion(util.SleepAppTags.V2)).To(Succeed())
		deviceSpec, err := tc.createDeviceSpec()
		Expect(err).NotTo(HaveOccurred())
		fleetSpec := createFleetSpec(batchSequence, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec)
		fleetSpec.Selector = &api.LabelSelector{MatchLabels: &map[string]string{phaseKey: "active"}}

		By("holding the first device in its batch while the next promoted value changes")
		Expect(tc.harnesses[0].SimulateNetworkFailure(tc.registryHost, tc.registryPort)).To(Succeed())
		defer func() { _ = tc.harnesses[0].FixNetworkFailure(tc.registryHost, tc.registryPort) }()
		Expect(harness.CreateOrUpdateTestFleet(fleetName, fleetSpec)).To(Succeed())
		harness.WaitForBatchStart(fleetName, 0)
		promotedExpectSelectedBatchDevice(harness, tc.deviceIDs[0])
		promotedReplaceMapping(harness, mappingName, phaseExpression("active"))
		promotedWaitForLabels(harness, tc.deviceIDs[1], map[string]string{phaseKey: "active"})
		promotedWaitForOwner(harness, tc.deviceIDs[1], fleetName)
		promotedExpectSelectedBatchDevice(harness, tc.deviceIDs[0])
		Expect(tc.harnesses[0].FixNetworkFailure(tc.registryHost, tc.registryPort)).To(Succeed())

		By("checking the newly eligible device joins the final batch")
		harness.WaitForBatchStart(fleetName, 1)
		promotedExpectSelectedBatchDevice(harness, tc.deviceIDs[1])
		completedFirst, err := harness.GetDevice(tc.deviceIDs[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(completedFirst.Spec).NotTo(BeNil())
		harness.WaitForBatchStart(fleetName, 2)
		Expect(tc.verifyAllDevicesUpdated(2)).To(Succeed())
		Eventually(harness.GetRolloutStatus, util.LONG_TIMEOUT, util.POLLING).
			WithArguments(fleetName).Should(e2e.HaveReason(api.RolloutInactiveReason))
		firstVersion := promotedFleetTemplateVersion(harness, fleetName)
		firstAfter, err := harness.GetDevice(tc.deviceIDs[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(firstAfter.Spec).To(Equal(completedFirst.Spec))

		By("starting a second rollout to detect stale batch IDs from the prior rollout")
		Expect(tc.updateAppVersion(util.SleepAppTags.V3)).To(Succeed())
		deviceSpec, err = tc.createDeviceSpec()
		Expect(err).NotTo(HaveOccurred())
		fleetSpec = createFleetSpec(batchSequence, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec)
		fleetSpec.Selector = &api.LabelSelector{MatchLabels: &map[string]string{phaseKey: "active"}}
		Expect(harness.CreateOrUpdateTestFleet(fleetName, fleetSpec)).To(Succeed())
		harness.WaitForBatchStart(fleetName, 2)
		Expect(tc.verifyAllDevicesUpdated(2)).To(Succeed())
		Eventually(harness.GetRolloutStatus, util.LONG_TIMEOUT, util.POLLING).
			WithArguments(fleetName).Should(e2e.HaveReason(api.RolloutInactiveReason))
		secondVersion := promotedFleetTemplateVersion(harness, fleetName)
		Expect(secondVersion).NotTo(Equal(firstVersion))

		By("checking dispatch IDs and exactly one completion event for each rollout")
		var eventErrors []string
		for _, version := range []string{firstVersion, secondVersion} {
			var events []api.Event
			Eventually(func(g Gomega) {
				events = promotedFleetEvents(harness, fleetName)
				g.Expect(promotedCompletionCount(events, version)).To(BeNumerically(">=", 1))
			}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
			eventErrors = append(eventErrors, promotedRolloutEventErrors(events, version)...)
		}
		Expect(eventErrors).To(BeEmpty())
	})
})
