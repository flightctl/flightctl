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
		_, err = harness.GetDeviceInlineConfig(device, configName)
		Expect(err).To(HaveOccurred())

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
		fleetBefore, err := harness.GetFleet(fleetName)
		Expect(err).NotTo(HaveOccurred())
		err = harness.CreateOrUpdateTestFleet(fleetName,
			promotedFleetSpec(map[string]string{regionKey: "east"}, invalidConfig))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("template references unsupported field"))
		fleetAfter, err := harness.GetFleet(fleetName)
		Expect(err).NotTo(HaveOccurred())
		Expect(fleetAfter.Spec).To(Equal(fleetBefore.Spec))
		device, err = harness.GetDevice(deviceName)
		Expect(err).NotTo(HaveOccurred())
		inline, err := harness.GetDeviceInlineConfig(device, configName)
		Expect(err).NotTo(HaveOccurred())
		Expect(inline.Inline[0].Content).To(Equal("east"))
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

		By("checking the public multiple-owner condition and lack of rendered config")
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(deviceName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(device.Status).NotTo(BeNil())
			g.Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeTrue())
			g.Expect(device.Metadata.Owner).To(BeNil())
			_, err = harness.GetDeviceInlineConfig(device, "owner-a-config")
			g.Expect(err).To(HaveOccurred())
			_, err = harness.GetDeviceInlineConfig(device, "owner-b-config")
			g.Expect(err).To(HaveOccurred())
		}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
	})

	It("uses changed promoted values at later rollout turns and records correct rollout events", func() {
		harness := e2e.GetWorkerHarness()
		rolloutTC = setupTestContext(harness.Context)
		tc := rolloutTC

		phaseKey := fmt.Sprintf("e2e.flightctl.io/phase-%s", promotedTestSuffix(harness))
		mappingName := "phase-" + promotedTestSuffix(harness)
		phaseExpression := func(parisValue string) string {
			return promotedScalarExpression(harness,
				fmt.Sprintf(`status.systemInfo.architecture != "" ? (metadata.labels["site"] == "madrid" ? "first" : metadata.labels["site"] == "paris" ? %q : "remaining") : dyn(null)`, parisValue))
		}

		By("creating a status-gated phase mapping before device enrollment")
		promotedCreateMapping(harness, mappingName, &phaseKey, phaseExpression("waiting"))
		By("enrolling three devices for the first, next, and final batches")
		Expect(tc.setupFleetAndDevices(harness.Context, 3, []map[string]string{
			{labelSite: siteMadrid},
			{labelSite: siteParis},
			{labelSite: siteRome},
		})).To(Succeed())
		By("waiting for the initial promoted phase values")
		promotedWaitForLabels(harness, tc.deviceIDs[0], map[string]string{phaseKey: "first"})
		promotedWaitForLabels(harness, tc.deviceIDs[1], map[string]string{phaseKey: "waiting"})
		promotedWaitForLabels(harness, tc.deviceIDs[2], map[string]string{phaseKey: "remaining"})

		batchSequence := api.BatchSequence{Sequence: &[]api.Batch{
			{Selector: &api.LabelSelector{MatchLabels: &map[string]string{phaseKey: "first"}}, Limit: intLimit(1)},
			{Selector: &api.LabelSelector{MatchLabels: &map[string]string{phaseKey: "second"}}, Limit: intLimit(1)},
		}}
		// Fleet names and template versions are reused, while events survive test cleanup.
		eventCutoff := promotedFleetEventCutoff(harness, fleetName)
		Expect(tc.updateAppVersion(util.SleepAppTags.V2)).To(Succeed())
		deviceSpec, err := tc.createDeviceSpec()
		Expect(err).NotTo(HaveOccurred())
		fleetSpec := createFleetSpec(batchSequence, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec)
		fleetSpec.RolloutPolicy.DefaultUpdateTimeout = lo.ToPtr("6m")

		By("holding the first device in its batch while the next promoted value changes")
		Expect(tc.harnesses[0].SimulateNetworkFailure(tc.registryHost, tc.registryPort)).To(Succeed())
		defer func() { _ = tc.harnesses[0].FixNetworkFailure(tc.registryHost, tc.registryPort) }()
		Expect(harness.CreateOrUpdateTestFleet(fleetName, fleetSpec)).To(Succeed())
		harness.WaitForBatchStart(fleetName, 0)
		promotedExpectSelectedBatchDevice(harness, tc.deviceIDs[0])
		promotedReplaceMapping(harness, mappingName, phaseExpression("second"))
		Eventually(func(g Gomega) {
			device, err := harness.GetDevice(tc.deviceIDs[1])
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(device.Metadata.Labels).NotTo(BeNil())
			g.Expect(*device.Metadata.Labels).To(HaveKeyWithValue(phaseKey, "second"))
		}).WithTimeout(util.DURATION_TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
		promotedWaitForMappingReady(harness, mappingName)
		promotedExpectSelectedBatchDevice(harness, tc.deviceIDs[0])
		Expect(tc.harnesses[0].FixNetworkFailure(tc.registryHost, tc.registryPort)).To(Succeed())

		By("checking the changed value is selected next and the final batch catches the remainder")
		harness.WaitForBatchStart(fleetName, 1)
		promotedExpectSelectedBatchDevice(harness, tc.deviceIDs[1])
		completedFirst, err := harness.GetDevice(tc.deviceIDs[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(completedFirst.Spec).NotTo(BeNil())
		harness.WaitForBatchStart(fleetName, 2)
		promotedExpectSelectedBatchDevice(harness, tc.deviceIDs[2])
		harness.WaitForBatchStart(fleetName, 3)
		Expect(tc.verifyAllDevicesUpdated(3)).To(Succeed())
		Eventually(harness.GetRolloutStatus, util.LONG_TIMEOUT, util.POLLING).
			WithArguments(fleetName).Should(e2e.HaveReason(api.RolloutInactiveReason))
		firstVersion := promotedFleetTemplateVersion(harness, fleetName)
		firstAfter, err := harness.GetDevice(tc.deviceIDs[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(firstAfter.Spec).To(Equal(completedFirst.Spec))

		By("starting a second rollout to detect stale batch IDs from the prior rollout")
		deviceSpec, err = tc.createDeviceSpec()
		Expect(err).NotTo(HaveOccurred())
		configs := []api.ConfigProviderSpec{promotedInlineConfig("rollout-marker", "second rollout")}
		deviceSpec.Config = &configs
		Expect(harness.CreateOrUpdateTestFleet(fleetName,
			createFleetSpec(batchSequence, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec))).To(Succeed())
		Eventually(func(g Gomega) {
			fleet, err := harness.GetFleet(fleetName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(fleet.Metadata.Annotations).NotTo(BeNil())
			g.Expect((*fleet.Metadata.Annotations)[api.FleetAnnotationTemplateVersion]).NotTo(Equal(firstVersion))
		}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
		Eventually(harness.GetRolloutStatus, util.LONG_TIMEOUT, util.POLLING).
			WithArguments(fleetName).Should(e2e.HaveReason(api.RolloutActiveReason))
		harness.WaitForBatchStart(fleetName, 3)
		Expect(tc.verifyAllDevicesUpdated(3)).To(Succeed())
		Eventually(harness.GetRolloutStatus, util.LONG_TIMEOUT, util.POLLING).
			WithArguments(fleetName).Should(e2e.HaveReason(api.RolloutInactiveReason))
		secondVersion := promotedFleetTemplateVersion(harness, fleetName)
		Expect(secondVersion).NotTo(Equal(firstVersion))

		By("checking dispatch IDs and exactly one completion event for each rollout")
		var eventErrors []string
		for _, version := range []string{firstVersion, secondVersion} {
			var events []api.Event
			Eventually(func(g Gomega) {
				events = promotedFleetEvents(harness, fleetName, eventCutoff)
				g.Expect(promotedCompletionCount(events, version)).To(BeNumerically(">=", 1))
			}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
			eventErrors = append(eventErrors, promotedRolloutEventErrors(events, version)...)
		}
		Expect(eventErrors).To(BeEmpty())
	})
})
