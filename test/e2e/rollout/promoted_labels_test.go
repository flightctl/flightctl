package rollout_test

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

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

		By("checking fleet ownership for promoted and static selectors")
		promotedWaitForOwner(harness, deviceA, scalarFleet)
		promotedWaitForOwner(harness, deviceB, mapFleet)
		promotedWaitForOwner(harness, deviceC, staticFleet)

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

func promotedReplaceMapping(harness *e2e.Harness, name, expression string) {
	response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
	Expect(response.JSON200).NotTo(BeNil())
	response.JSON200.Spec.Expression = expression
	replaced, err := harness.Client.ReplaceLabelSyncMappingWithResponse(harness.Context, name, *response.JSON200)
	Expect(err).NotTo(HaveOccurred())
	Expect(replaced.StatusCode()).To(Equal(http.StatusOK), string(replaced.Body))
}

func promotedExpectSelectedBatchDevice(harness *e2e.Harness, expectedID string) {
	Eventually(func(g Gomega) {
		selected, err := harness.GetSelectedDevicesForBatch(fleetName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(selected).To(HaveLen(1))
		g.Expect(selected[0].Metadata.Name).NotTo(BeNil())
		g.Expect(*selected[0].Metadata.Name).To(Equal(expectedID))
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}

func promotedFleetTemplateVersion(harness *e2e.Harness, name string) string {
	fleet, err := harness.GetFleet(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(fleet.Metadata.Annotations).NotTo(BeNil())
	version := (*fleet.Metadata.Annotations)[api.FleetAnnotationTemplateVersion]
	Expect(version).NotTo(BeEmpty())
	return version
}

func promotedFleetEventCutoff(harness *e2e.Harness, name string) time.Time {
	events := promotedFleetEvents(harness, name, time.Time{})
	if len(events) == 0 {
		return time.Time{}
	}
	Expect(events[0].Metadata.CreationTimestamp).NotTo(BeNil())
	return *events[0].Metadata.CreationTimestamp
}

func promotedFleetEvents(harness *e2e.Harness, name string, after time.Time) []api.Event {
	selector := "involvedObject.name=" + name
	if !after.IsZero() {
		selector += ",metadata.creationTimestamp>" + after.UTC().Format(time.RFC3339Nano)
	}
	order := api.Desc
	response, err := harness.Client.ListEventsWithResponse(harness.Context, &api.ListEventsParams{FieldSelector: &selector, Order: &order})
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
	Expect(response.JSON200).NotTo(BeNil())
	return response.JSON200.Items
}

func promotedRolloutEventErrors(events []api.Event, version string) []string {
	sort.SliceStable(events, func(i, j int) bool {
		left, right := events[i].Metadata.CreationTimestamp, events[j].Metadata.CreationTimestamp
		if left == nil || right == nil {
			return false
		}
		return left.Before(*right)
	})
	batchNumbers := map[string]int{
		"-1": -1, api.PreliminaryBatchName: -1,
		"0": 0, "batch 1": 0,
		"1": 1, "batch 2": 1,
		"2": 2, api.FinalImplicitBatchName: 2,
	}
	var batches []string
	var errors []string
	seen := map[int]bool{}
	lastBatch := -1
	for _, event := range events {
		if event.Reason != api.EventReasonFleetRolloutBatchDispatched || event.Details == nil {
			continue
		}
		details, err := event.Details.AsFleetRolloutBatchDispatchedDetails()
		if err != nil || details.TemplateVersion != version {
			continue
		}
		batches = append(batches, details.Batch)
		number, valid := batchNumbers[details.Batch]
		if !valid || number < lastBatch {
			errors = append(errors, fmt.Sprintf("template %s has stale or unknown dispatch batch %q", version, details.Batch))
			continue
		}
		seen[number] = true
		lastBatch = number
	}
	if !seen[0] || !seen[1] || !seen[2] {
		errors = append(errors, fmt.Sprintf("template %s dispatched %v; expected first, second, and final batches", version, batches))
	}
	if count := promotedCompletionCount(events, version); count != 1 {
		errors = append(errors, fmt.Sprintf("template %s has %d completion events; expected 1", version, count))
	}
	return errors
}

func promotedCompletionCount(events []api.Event, version string) int {
	count := 0
	for _, event := range events {
		if event.Reason != api.EventReasonFleetRolloutCompleted || event.Details == nil {
			continue
		}
		details, err := event.Details.AsFleetRolloutCompletedDetails()
		if err == nil && details.TemplateVersion == version {
			count++
		}
	}
	return count
}

func promotedInlineConfig(name, content string) api.ConfigProviderSpec {
	var config api.ConfigProviderSpec
	Expect(config.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
		Name:   name,
		Inline: []api.FileSpec{{Path: "/etc/flightctl/selected-region", Content: content}},
	})).To(Succeed())
	return config
}

func promotedFleetSpec(selector map[string]string, config api.ConfigProviderSpec) api.FleetSpec {
	return api.FleetSpec{
		Selector: &api.LabelSelector{MatchLabels: &selector},
		Template: struct {
			Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
			Spec     api.DeviceSpec  "json:\"spec\""
		}{Spec: api.DeviceSpec{Config: &[]api.ConfigProviderSpec{config}}},
	}
}

func promotedTestSuffix(harness *e2e.Harness) string {
	suffix := strings.TrimPrefix(harness.GetTestIDFromContext(), "test-")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return suffix
}

func promotedScalarExpression(harness *e2e.Harness, expression string) string {
	return fmt.Sprintf(`metadata.labels["test-id"] == %q ? (%s) : dyn(null)`, harness.GetTestIDFromContext(), expression)
}

func promotedMapExpression(harness *e2e.Harness, expression string) string {
	return fmt.Sprintf(`metadata.labels["test-id"] == %q ? (%s) : {}`, harness.GetTestIDFromContext(), expression)
}

func promotedCreateMapping(harness *e2e.Harness, name string, key *string, expression string) {
	labels := map[string]string{"test-id": harness.GetTestIDFromContext()}
	mapping := api.LabelSyncMapping{
		ApiVersion: api.LabelSyncMappingAPIVersion,
		Kind:       api.LabelSyncMappingKind(api.LabelSyncMappingKindValue),
		Metadata:   api.ObjectMeta{Name: &name, Labels: &labels},
		Spec: api.LabelSyncMappingSpec{
			Expression:   expression,
			Key:          key,
			ResourceType: api.LabelSyncMappingSpecResourceTypeDevice,
		},
	}
	response, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, mapping)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusCreated), string(response.Body))
	DeferCleanup(func() {
		response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode()).To(BeElementOf(http.StatusOK, http.StatusNotFound), string(response.Body))
	})
}

func promotedCreateDeviceWithStatus(harness *e2e.Harness, name string, labels map[string]string, architecture, region string) {
	_, err := resources.CreateDevice(harness, name, &labels)
	Expect(err).NotTo(HaveOccurred())
	promotedReplaceDeviceStatus(harness, name, architecture, region)
}

func promotedReplaceDeviceStatus(harness *e2e.Harness, name, architecture, region string) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		status := api.NewDeviceStatus()
		if response.JSON200.Status != nil {
			status = *response.JSON200.Status
		}
		status.SystemInfo.Architecture = architecture
		status.SystemInfo.OperatingSystem = "linux"
		info := api.CustomDeviceInfo{"region": region}
		status.SystemInfo.CustomInfo = &info
		response.JSON200.Status = &status
		replaced, err := harness.Client.ReplaceDeviceStatusWithResponse(harness.Context, name, *response.JSON200)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(replaced.StatusCode()).To(Equal(http.StatusOK), string(replaced.Body))
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}

func promotedWaitForLabels(harness *e2e.Harness, name string, expected map[string]string) {
	Eventually(func(g Gomega) {
		device, err := harness.GetDevice(name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(device.Metadata.Labels).NotTo(BeNil())
		for key, value := range expected {
			g.Expect(*device.Metadata.Labels).To(HaveKeyWithValue(key, value))
		}
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}

func promotedWaitForMappingReady(harness *e2e.Harness, name string) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Status).NotTo(BeNil())
		g.Expect(response.JSON200.Status.Conditions).NotTo(BeNil())
		g.Expect(api.IsStatusConditionTrue(*response.JSON200.Status.Conditions, api.ConditionTypeLabelSyncMappingReady)).To(BeTrue())
	}).WithTimeout(util.DURATION_TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}

func promotedWaitForOwner(harness *e2e.Harness, deviceName, fleetName string) {
	Eventually(func(g Gomega) {
		device, err := harness.GetDevice(deviceName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(device.Metadata.Owner).NotTo(BeNil())
		g.Expect(*device.Metadata.Owner).To(Equal("Fleet/" + fleetName))
	}).WithTimeout(util.TIMEOUT).WithPolling(util.POLLING).Should(Succeed())
}
