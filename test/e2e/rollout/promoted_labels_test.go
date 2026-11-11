package rollout_test

import (
	"errors"
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

const (
	promotedLabelPrefix       = "e2e.flightctl.io/"
	promotedTestIDLabel       = "test-id"
	promotedArchitectureField = "status.systemInfo.architecture"
	promotedOperatingSystem   = "linux"
	promotedArchitectureAMD64 = "amd64"
	promotedArchitectureARM64 = "arm64"
	promotedDefaultRegion     = "east"
	promotedRegionWest        = "west"
	promotedRegionSouth       = "south"
	promotedRegionNorth       = "north"
	promotedConfigName        = "selected-region"
	promotedOwnerAConfig      = "owner-a-config"
	promotedOwnerBConfig      = "owner-b-config"
	promotedStaticTestLabel   = "static-test"
)

var rolloutTC *TestContext

var _ = Describe("Promoted label runtime selection", Serial, Label("integration", "sanity", "rollout"), func() {
	var networkFailureActive bool
	AfterEach(func() {
		if networkFailureActive && rolloutTC != nil && len(rolloutTC.harnesses) > 0 {
			if err := promotedFixNetworkFailure(rolloutTC.harnesses[0], rolloutTC.registryHost, rolloutTC.registryPort, &networkFailureActive); err != nil {
				GinkgoWriter.Printf("repair simulated registry network failure before VM cleanup: %v\n", err)
			}
		}
		networkFailureActive = false
		Expect(cleanupPromotedTestContext(rolloutTC)).To(Succeed())
		rolloutTC = nil
	})

	It("selects scalar and map promoted labels with ordinary fleet selectors", func() {
		harness := workerHarness
		suffix := promotedTestSuffix(harness)
		architectureKey := fmt.Sprintf("%sarchitecture-%s", promotedLabelPrefix, suffix)
		regionKey := fmt.Sprintf("%sregion-%s", promotedLabelPrefix, suffix)
		architectureMapping := "architecture-" + suffix
		regionMapping := "region-" + suffix

		By("creating scoped scalar and map mappings")
		Expect(promotedCreateMapping(harness, architectureMapping, &architectureKey,
			promotedScalarExpression(harness, promotedArchitectureField))).To(Succeed())
		Expect(promotedCreateMapping(harness, regionMapping, nil,
			promotedMapExpression(harness, fmt.Sprintf(`{"%s": status.systemInfo.customInfo["region"]}`, regionKey)))).To(Succeed())

		deviceA := "scalar-" + suffix
		deviceB := "map-" + suffix
		deviceC := "static-" + suffix
		By("reporting distinct status values on three devices")
		Expect(promotedCreateDeviceWithStatus(harness, deviceA, nil, promotedArchitectureAMD64, promotedDefaultRegion)).To(Succeed())
		Expect(promotedCreateDeviceWithStatus(harness, deviceB, nil, promotedArchitectureARM64, promotedRegionWest)).To(Succeed())
		Expect(promotedCreateDeviceWithStatus(harness, deviceC, map[string]string{promotedStaticTestLabel: suffix}, promotedArchitectureARM64, promotedRegionSouth)).To(Succeed())

		By("waiting for both mappings and promoted labels to settle")
		Expect(promotedWaitForLabels(harness, deviceA, map[string]string{architectureKey: promotedArchitectureAMD64, regionKey: promotedDefaultRegion})).To(Succeed())
		Expect(promotedWaitForLabels(harness, deviceB, map[string]string{architectureKey: promotedArchitectureARM64, regionKey: promotedRegionWest})).To(Succeed())
		Expect(promotedWaitForLabels(harness, deviceC, map[string]string{architectureKey: promotedArchitectureARM64, regionKey: promotedRegionSouth})).To(Succeed())
		Expect(promotedWaitForMappingReady(harness, architectureMapping)).To(Succeed())
		Expect(promotedWaitForMappingReady(harness, regionMapping)).To(Succeed())

		scalarFleet := "scalar-" + suffix
		mapFleet := "map-" + suffix
		staticFleet := "static-" + suffix
		By("creating disjoint fleets using promoted and static selectors")
		Expect(harness.CreateFleetWithSelector(scalarFleet, map[string]string{architectureKey: promotedArchitectureAMD64})).To(Succeed())
		Expect(harness.CreateFleetWithSelector(mapFleet, map[string]string{regionKey: promotedRegionWest})).To(Succeed())
		Expect(harness.CreateFleetWithSelector(staticFleet, map[string]string{promotedStaticTestLabel: suffix})).To(Succeed())

		By("checking fleet ownership for promoted and static selectors")
		Expect(promotedWaitForOwner(harness, deviceA, scalarFleet)).To(Succeed())
		Expect(promotedWaitForOwner(harness, deviceB, mapFleet)).To(Succeed())
		Expect(promotedWaitForOwner(harness, deviceC, staticFleet)).To(Succeed())

		By("changing an unrelated promoted value without moving the static-only device")
		Expect(promotedReplaceDeviceStatus(harness, deviceC, promotedArchitectureARM64, promotedRegionNorth)).To(Succeed())
		Expect(promotedWaitForLabels(harness, deviceC, map[string]string{architectureKey: promotedArchitectureARM64, regionKey: promotedRegionNorth})).To(Succeed())
		Expect(promotedWaitForOwner(harness, deviceC, staticFleet)).To(Succeed())
	})

	It("renders promoted labels only after ownership settles and rejects raw status variables", func() {
		harness := workerHarness
		suffix := promotedTestSuffix(harness)
		regionKey := fmt.Sprintf("%sregion-%s", promotedLabelPrefix, suffix)
		mappingName := "render-region-" + suffix
		deviceName := "render-" + suffix
		fleetName := "render-" + suffix
		configName := promotedConfigName

		By("creating a map mapping and a fleet whose template reads the promoted label")
		Expect(promotedCreateMapping(harness, mappingName, nil,
			promotedMapExpression(harness, fmt.Sprintf(`{"%s": status.systemInfo.customInfo["region"]}`, regionKey)))).To(Succeed())
		config, err := promotedInlineConfig(configName, fmt.Sprintf(`{{ index .metadata.labels %q }}`, regionKey))
		Expect(err).NotTo(HaveOccurred())
		fleetSpec := promotedFleetSpec(map[string]string{regionKey: "east"}, config)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, fleetSpec)).To(Succeed())

		By("checking that the device is not rendered before it reports the mapped status")
		_, err = resources.CreateDevice(harness, deviceName, &map[string]string{})
		Expect(err).NotTo(HaveOccurred())
		device, err := harness.GetDevice(deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(device.Metadata.Owner).To(BeNil())
		_, err = harness.GetDeviceInlineConfig(device, configName)
		Expect(err).To(HaveOccurred())

		By("reporting status and waiting for promotion, fleet ownership, and rendered config")
		Expect(promotedReplaceDeviceStatus(harness, deviceName, promotedArchitectureAMD64, promotedDefaultRegion)).To(Succeed())
		Expect(promotedWaitForLabels(harness, deviceName, map[string]string{regionKey: promotedDefaultRegion})).To(Succeed())
		Expect(promotedWaitForMappingReady(harness, mappingName)).To(Succeed())
		Expect(promotedWaitForOwner(harness, deviceName, fleetName)).To(Succeed())
		Expect(promotedWaitForInlineConfig(harness, deviceName, configName, promotedDefaultRegion)).To(Succeed())

		By("checking that raw device status is unavailable to the template renderer")
		invalidConfig, err := promotedInlineConfig(configName, `{{ .status.systemInfo.architecture }}`)
		Expect(err).NotTo(HaveOccurred())
		fleetBefore, err := harness.GetFleet(fleetName)
		Expect(err).NotTo(HaveOccurred())
		err = harness.CreateOrUpdateTestFleet(fleetName,
			promotedFleetSpec(map[string]string{regionKey: promotedDefaultRegion}, invalidConfig))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("template references unsupported field"))
		fleetAfter, err := harness.GetFleet(fleetName)
		Expect(err).NotTo(HaveOccurred())
		Expect(fleetAfter.Spec).To(Equal(fleetBefore.Spec))
		device, err = harness.GetDevice(deviceName)
		Expect(err).NotTo(HaveOccurred())
		inline, err := harness.GetDeviceInlineConfig(device, configName)
		Expect(err).NotTo(HaveOccurred())
		Expect(inline.Inline[0].Content).To(Equal(promotedDefaultRegion))
	})

	It("blocks rendering when a promoted label matches multiple fleets", func() {
		harness := workerHarness
		suffix := promotedTestSuffix(harness)
		architectureKey := fmt.Sprintf("%sarchitecture-%s", promotedLabelPrefix, suffix)
		mappingName := "owners-" + suffix
		deviceName := "owners-" + suffix
		fleetA := "owner-a-" + suffix
		fleetB := "owner-b-" + suffix
		selector := map[string]string{architectureKey: promotedArchitectureAMD64}

		By("creating two fleets that would both own the device after promotion")
		Expect(promotedCreateMapping(harness, mappingName, &architectureKey,
			promotedScalarExpression(harness, promotedArchitectureField))).To(Succeed())
		configA, err := promotedInlineConfig(promotedOwnerAConfig, "owner-a")
		Expect(err).NotTo(HaveOccurred())
		configB, err := promotedInlineConfig(promotedOwnerBConfig, "owner-b")
		Expect(err).NotTo(HaveOccurred())
		Expect(harness.CreateOrUpdateTestFleet(fleetA, promotedFleetSpec(selector, configA))).To(Succeed())
		Expect(harness.CreateOrUpdateTestFleet(fleetB, promotedFleetSpec(selector, configB))).To(Succeed())

		By("promoting a value that makes both fleets match")
		Expect(promotedCreateDeviceWithStatus(harness, deviceName, nil, promotedArchitectureAMD64, promotedDefaultRegion)).To(Succeed())
		Expect(promotedWaitForLabels(harness, deviceName, map[string]string{architectureKey: promotedArchitectureAMD64})).To(Succeed())

		By("checking the public multiple-owner condition and lack of rendered config")
		Expect(promotedWaitForMultipleOwnerConflict(harness, deviceName, promotedOwnerAConfig, promotedOwnerBConfig)).To(Succeed())
	})

	It("uses changed promoted values at later rollout turns and records correct rollout events", func() {
		harness := workerHarness
		rolloutTC = setupTestContext(harness.Context)
		tc := rolloutTC

		phaseKey := fmt.Sprintf("%sphase-%s", promotedLabelPrefix, promotedTestSuffix(harness))
		mappingName := "phase-" + promotedTestSuffix(harness)

		By("creating a status-gated phase mapping before device enrollment")
		Expect(promotedCreateMapping(harness, mappingName, &phaseKey, promotedPhaseExpression(harness, "waiting"))).To(Succeed())
		By("enrolling three devices for the first, next, and final batches")
		Expect(tc.setupFleetAndDevices(harness.Context, 3, []map[string]string{
			{labelSite: siteMadrid},
			{labelSite: siteParis},
			{labelSite: siteRome},
		})).To(Succeed())
		By("waiting for the initial promoted phase values")
		Expect(promotedWaitForDeviceLabel(harness, tc.deviceIDs[0], phaseKey, "first")).To(Succeed())
		Expect(promotedWaitForDeviceLabel(harness, tc.deviceIDs[1], phaseKey, "waiting")).To(Succeed())
		Expect(promotedWaitForDeviceLabel(harness, tc.deviceIDs[2], phaseKey, "remaining")).To(Succeed())

		batchSequence := api.BatchSequence{Sequence: &[]api.Batch{
			{Selector: &api.LabelSelector{MatchLabels: &map[string]string{phaseKey: "first"}}, Limit: intLimit(1)},
			{Selector: &api.LabelSelector{MatchLabels: &map[string]string{phaseKey: "second"}}, Limit: intLimit(1)},
		}}
		// Fleet names and template versions are reused, while events survive test cleanup.
		eventCutoff, err := promotedFleetEventCutoff(harness, fleetName)
		Expect(err).NotTo(HaveOccurred())
		Expect(tc.updateAppVersion(util.SleepAppTags.V2)).To(Succeed())
		deviceSpec, err := tc.createDeviceSpec()
		Expect(err).NotTo(HaveOccurred())
		fleetSpec := createFleetSpec(batchSequence, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec)
		fleetSpec.RolloutPolicy.DefaultUpdateTimeout = lo.ToPtr("6m")

		By("holding the first device in its batch while the next promoted value changes")
		Expect(tc.harnesses[0].SimulateNetworkFailure(tc.registryHost, tc.registryPort)).To(Succeed())
		networkFailureActive = true
		Expect(harness.CreateOrUpdateTestFleet(fleetName, fleetSpec)).To(Succeed())
		harness.WaitForBatchStart(fleetName, 0)
		Expect(promotedAssertSelectedBatchDevice(harness, tc.deviceIDs[0])).To(Succeed())
		Expect(promotedReplaceMapping(harness, mappingName, promotedPhaseExpression(harness, "second"))).To(Succeed())
		Expect(promotedWaitForDeviceLabel(harness, tc.deviceIDs[1], phaseKey, "second")).To(Succeed())
		Expect(promotedWaitForMappingReady(harness, mappingName)).To(Succeed())
		Expect(promotedAssertSelectedBatchDevice(harness, tc.deviceIDs[0])).To(Succeed())
		Expect(promotedFixNetworkFailure(tc.harnesses[0], tc.registryHost, tc.registryPort, &networkFailureActive)).To(Succeed())

		By("checking the changed value is selected next and the final batch catches the remainder")
		harness.WaitForBatchStart(fleetName, 1)
		Expect(promotedAssertSelectedBatchDevice(harness, tc.deviceIDs[1])).To(Succeed())
		completedFirst, err := harness.GetDevice(tc.deviceIDs[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(completedFirst.Spec).NotTo(BeNil())
		harness.WaitForBatchStart(fleetName, 2)
		Expect(promotedAssertSelectedBatchDevice(harness, tc.deviceIDs[2])).To(Succeed())
		harness.WaitForBatchStart(fleetName, 3)
		Expect(tc.verifyAllDevicesUpdated(3)).To(Succeed())
		Eventually(harness.GetRolloutStatus, util.LONG_TIMEOUT, util.POLLING).
			WithArguments(fleetName).Should(e2e.HaveReason(api.RolloutInactiveReason))
		firstVersion, err := promotedFleetTemplateVersion(harness, fleetName)
		Expect(err).NotTo(HaveOccurred())
		firstAfter, err := harness.GetDevice(tc.deviceIDs[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(firstAfter.Spec).To(Equal(completedFirst.Spec))

		By("starting a second rollout to detect stale batch IDs from the prior rollout")
		deviceSpec, err = tc.createDeviceSpec()
		Expect(err).NotTo(HaveOccurred())
		markerConfig, err := promotedInlineConfig("rollout-marker", "second rollout")
		Expect(err).NotTo(HaveOccurred())
		configs := []api.ConfigProviderSpec{markerConfig}
		deviceSpec.Config = &configs
		Expect(harness.CreateOrUpdateTestFleet(fleetName,
			createFleetSpec(batchSequence, lo.ToPtr(api.Percentage(SuccessThreshold)), deviceSpec))).To(Succeed())
		secondVersion, err := promotedWaitForTemplateVersionChange(harness, fleetName, firstVersion)
		Expect(err).NotTo(HaveOccurred())
		Eventually(harness.GetRolloutStatus, util.LONG_TIMEOUT, util.POLLING).
			WithArguments(fleetName).Should(e2e.HaveReason(api.RolloutActiveReason))
		harness.WaitForBatchStart(fleetName, 3)
		Expect(tc.verifyAllDevicesUpdated(3)).To(Succeed())
		Eventually(harness.GetRolloutStatus, util.LONG_TIMEOUT, util.POLLING).
			WithArguments(fleetName).Should(e2e.HaveReason(api.RolloutInactiveReason))
		Expect(secondVersion).NotTo(Equal(firstVersion))

		By("checking dispatch IDs and exactly one completion event for each rollout")
		var eventErrors []string
		for _, version := range []string{firstVersion, secondVersion} {
			events, err := promotedWaitForCompletionEvents(harness, fleetName, eventCutoff, version)
			Expect(err).NotTo(HaveOccurred())
			eventErrors = append(eventErrors, promotedRolloutEventErrors(events, version)...)
		}
		Expect(eventErrors).To(BeEmpty())
	})
})

// promotedReplaceMapping updates an existing mapping's expression and returns API failures.
func promotedReplaceMapping(harness *e2e.Harness, name, expression string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if name == "" || strings.TrimSpace(expression) == "" {
		return fmt.Errorf("mapping name and expression are required")
	}
	response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
	if err != nil {
		return fmt.Errorf("get label sync mapping %q: %w", name, err)
	}
	if response == nil {
		return fmt.Errorf("get label sync mapping %q returned no response", name)
	}
	if err := promotedRequireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
		return err
	}
	if response.JSON200 == nil {
		return fmt.Errorf("get label sync mapping %q returned an empty 200 response", name)
	}
	response.JSON200.Spec.Expression = expression
	replaced, err := harness.Client.ReplaceLabelSyncMappingWithResponse(harness.Context, name, *response.JSON200)
	if err != nil {
		return fmt.Errorf("replace label sync mapping %q: %w", name, err)
	}
	if replaced == nil {
		return fmt.Errorf("replace label sync mapping %q returned no response", name)
	}
	return promotedRequireStatus(replaced.StatusCode(), replaced.Body, http.StatusOK)
}

// promotedAssertSelectedBatchDevice waits until the requested device is the selected batch member.
func promotedAssertSelectedBatchDevice(harness *e2e.Harness, expectedID string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if expectedID == "" || fleetName == "" {
		return fmt.Errorf("Fleet name and expected selected device ID are required")
	}
	return promotedRetryUntil("selected rollout batch device", util.TIMEOUT, util.POLLING, func() error {
		selected, err := harness.GetSelectedDevicesForBatch(fleetName)
		if err != nil {
			return fmt.Errorf("get selected batch devices: %w", err)
		}
		if len(selected) != 1 || selected[0].Metadata.Name == nil || *selected[0].Metadata.Name != expectedID {
			return fmt.Errorf("selected batch devices = %v, want only %q", selected, expectedID)
		}
		return nil
	})
}

// promotedFleetTemplateVersion returns the current template version or an API error.
func promotedFleetTemplateVersion(harness *e2e.Harness, name string) (string, error) {
	if err := promotedValidateHarness(harness); err != nil {
		return "", err
	}
	fleet, err := harness.GetFleet(name)
	if err != nil {
		return "", fmt.Errorf("get Fleet %q: %w", name, err)
	}
	if fleet == nil {
		return "", fmt.Errorf("get Fleet %q returned no resource", name)
	}
	if fleet.Metadata.Annotations == nil {
		return "", fmt.Errorf("Fleet %q has no annotations", name)
	}
	version := (*fleet.Metadata.Annotations)[api.FleetAnnotationTemplateVersion]
	if version == "" {
		return "", fmt.Errorf("Fleet %q has no template version annotation", name)
	}
	return version, nil
}

// promotedFleetEventCutoff returns the newest existing event timestamp before a rollout starts.
func promotedFleetEventCutoff(harness *e2e.Harness, name string) (time.Time, error) {
	events, err := promotedFleetEvents(harness, name, time.Time{})
	if err != nil {
		return time.Time{}, err
	}
	if len(events) == 0 {
		return time.Time{}, nil
	}
	if events[0].Metadata.CreationTimestamp == nil {
		return time.Time{}, fmt.Errorf("Fleet event for %q has no creation timestamp", name)
	}
	return *events[0].Metadata.CreationTimestamp, nil
}

// promotedFleetEvents lists Fleet events at or after the supplied timestamp.
func promotedFleetEvents(harness *e2e.Harness, name string, after time.Time) ([]api.Event, error) {
	if err := promotedValidateHarness(harness); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("Fleet name is required")
	}
	selector := "involvedObject.name=" + name
	if !after.IsZero() {
		selector += ",metadata.creationTimestamp>" + after.UTC().Format(time.RFC3339Nano)
	}
	order := api.Desc
	response, err := harness.Client.ListEventsWithResponse(harness.Context, &api.ListEventsParams{FieldSelector: &selector, Order: &order})
	if err != nil {
		return nil, fmt.Errorf("list Fleet events for %q: %w", name, err)
	}
	if response == nil {
		return nil, fmt.Errorf("list Fleet events for %q returned no response", name)
	}
	if err := promotedRequireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
		return nil, err
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("list Fleet events for %q returned an empty 200 response", name)
	}
	return response.JSON200.Items, nil
}

// promotedRolloutEventErrors reports stale, unknown, missing, or duplicate rollout dispatch events.
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
	var problems []string
	seen := map[int]bool{}
	lastBatch := -1
	for _, event := range events {
		if event.Reason != api.EventReasonFleetRolloutBatchDispatched {
			continue
		}
		if event.Details == nil {
			problems = append(problems, "Fleet rollout batch dispatch event has no details")
			continue
		}
		details, err := event.Details.AsFleetRolloutBatchDispatchedDetails()
		if err != nil {
			problems = append(problems, fmt.Sprintf("decode Fleet rollout batch dispatch event: %v", err))
			continue
		}
		if details.TemplateVersion != version {
			continue
		}
		batches = append(batches, details.Batch)
		number, valid := batchNumbers[details.Batch]
		if !valid || number < lastBatch {
			problems = append(problems, fmt.Sprintf("template %s has stale or unknown dispatch batch %q", version, details.Batch))
			continue
		}
		seen[number] = true
		lastBatch = number
	}
	if !seen[0] || !seen[1] || !seen[2] {
		problems = append(problems, fmt.Sprintf("template %s dispatched %v; expected first, second, and final batches", version, batches))
	}
	count, err := promotedCompletionCount(events, version)
	if err != nil {
		problems = append(problems, err.Error())
	} else if count != 1 {
		problems = append(problems, fmt.Sprintf("template %s has %d completion events; expected 1", version, count))
	}
	return problems
}

// promotedCompletionCount counts completion events for one Fleet template version.
func promotedCompletionCount(events []api.Event, version string) (int, error) {
	count := 0
	for _, event := range events {
		if event.Reason != api.EventReasonFleetRolloutCompleted {
			continue
		}
		if event.Details == nil {
			return 0, fmt.Errorf("Fleet rollout completion event has no details")
		}
		details, err := event.Details.AsFleetRolloutCompletedDetails()
		if err != nil {
			return 0, fmt.Errorf("decode Fleet rollout completion event: %w", err)
		}
		if details.TemplateVersion == version {
			count++
		}
	}
	return count, nil
}

// promotedInlineConfig builds an inline configuration provider and returns construction errors.
func promotedInlineConfig(name, content string) (api.ConfigProviderSpec, error) {
	if name == "" {
		return api.ConfigProviderSpec{}, fmt.Errorf("inline config name is required")
	}
	var config api.ConfigProviderSpec
	if err := config.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
		Name:   name,
		Inline: []api.FileSpec{{Path: "/etc/flightctl/selected-region", Content: content}},
	}); err != nil {
		return api.ConfigProviderSpec{}, fmt.Errorf("build inline config %q: %w", name, err)
	}
	return config, nil
}

// promotedFleetSpec builds a Fleet template that contains one inline config provider.
func promotedFleetSpec(selector map[string]string, config api.ConfigProviderSpec) api.FleetSpec {
	return api.FleetSpec{
		Selector: &api.LabelSelector{MatchLabels: &selector},
		Template: struct {
			Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
			Spec     api.DeviceSpec  "json:\"spec\""
		}{Spec: api.DeviceSpec{Config: &[]api.ConfigProviderSpec{config}}},
	}
}

// promotedTestSuffix returns the short test ID suffix used for unique rollout resources.
func promotedTestSuffix(harness *e2e.Harness) string {
	if harness == nil {
		GinkgoWriter.Printf("cannot get a promoted-label suffix from a nil harness\n")
		return ""
	}
	suffix := strings.TrimPrefix(harness.GetTestIDFromContext(), "test-")
	if suffix == "" {
		GinkgoWriter.Printf("cannot get a promoted-label suffix: test ID is empty\n")
		return ""
	}
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return suffix
}

// promotedScalarExpression limits a scalar expression to the current spec's resources.
func promotedScalarExpression(harness *e2e.Harness, expression string) string {
	if harness == nil || strings.TrimSpace(expression) == "" {
		GinkgoWriter.Printf("cannot build scoped scalar expression: harness=%t expression=%q\n", harness != nil, expression)
		return ""
	}
	testID := harness.GetTestIDFromContext()
	if testID == "" {
		GinkgoWriter.Printf("cannot build scoped scalar expression: test ID is empty\n")
		return ""
	}
	return fmt.Sprintf(`metadata.labels[%q] == %q ? (%s) : dyn(null)`, promotedTestIDLabel, testID, expression)
}

// promotedMapExpression limits a map expression to the current spec's resources.
func promotedMapExpression(harness *e2e.Harness, expression string) string {
	if harness == nil || strings.TrimSpace(expression) == "" {
		GinkgoWriter.Printf("cannot build scoped map expression: harness=%t expression=%q\n", harness != nil, expression)
		return ""
	}
	testID := harness.GetTestIDFromContext()
	if testID == "" {
		GinkgoWriter.Printf("cannot build scoped map expression: test ID is empty\n")
		return ""
	}
	return fmt.Sprintf(`metadata.labels[%q] == %q ? (%s) : {}`, promotedTestIDLabel, testID, expression)
}

// promotedCreateMapping creates a spec-scoped mapping and registers cleanup.
func promotedCreateMapping(harness *e2e.Harness, name string, key *string, expression string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if name == "" || strings.TrimSpace(expression) == "" {
		return fmt.Errorf("mapping name and expression are required")
	}
	testID := harness.GetTestIDFromContext()
	if testID == "" {
		return fmt.Errorf("test ID is required to scope label sync mapping %q", name)
	}
	labels := map[string]string{promotedTestIDLabel: testID}
	mapping := api.LabelSyncMapping{
		ApiVersion: api.LabelSyncMappingAPIVersion,
		Kind:       api.LabelSyncMappingKind(api.LabelSyncMappingKindValue),
		Metadata:   api.ObjectMeta{Name: &name, Labels: &labels},
		Spec: api.LabelSyncMappingSpec{
			Expression: expression, Key: key,
			ResourceType: api.LabelSyncMappingSpecResourceTypeDevice,
		},
	}
	GinkgoWriter.Printf("creating promoted-label mapping %q\n", name)
	response, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, mapping)
	if err != nil {
		return fmt.Errorf("create label sync mapping %q: %w", name, err)
	}
	if response == nil {
		return fmt.Errorf("create label sync mapping %q returned no response", name)
	}
	if err := promotedRequireStatus(response.StatusCode(), response.Body, http.StatusCreated); err != nil {
		return err
	}
	DeferCleanup(promotedCleanupMapping, harness, name)
	return nil
}

// promotedCleanupMapping deletes a test mapping during Ginkgo cleanup.
func promotedCleanupMapping(harness *e2e.Harness, name string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("mapping name is required")
	}
	response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, name)
	if err != nil {
		return fmt.Errorf("delete label sync mapping %q during cleanup: %w", name, err)
	}
	if response == nil {
		return fmt.Errorf("delete label sync mapping %q during cleanup returned no response", name)
	}
	if response.StatusCode() == http.StatusNotFound {
		return nil
	}
	return promotedRequireStatus(response.StatusCode(), response.Body, http.StatusOK)
}

// promotedCreateDeviceWithStatus enrolls a device and reports its architecture and region.
func promotedCreateDeviceWithStatus(harness *e2e.Harness, name string, labels map[string]string, architecture, region string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if name == "" || architecture == "" || region == "" {
		return fmt.Errorf("device name, architecture, and region are required")
	}
	if labels == nil {
		labels = map[string]string{}
	}
	GinkgoWriter.Printf("creating promoted-label test device %q\n", name)
	if _, err := resources.CreateDevice(harness, name, &labels); err != nil {
		return fmt.Errorf("create device %q: %w", name, err)
	}
	return promotedReplaceDeviceStatus(harness, name, architecture, region)
}

// promotedReplaceDeviceStatus retries a status update until the API accepts the latest resource version.
func promotedReplaceDeviceStatus(harness *e2e.Harness, name, architecture, region string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("device name is required")
	}
	return promotedRetryUntil(fmt.Sprintf("replace status for device %q", name), util.TIMEOUT, util.POLLING, func() error {
		response, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
		if err != nil {
			return fmt.Errorf("get device %q: %w", name, err)
		}
		if response == nil {
			return fmt.Errorf("get device %q returned no response", name)
		}
		if err := promotedRequireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
			return err
		}
		if response.JSON200 == nil {
			return fmt.Errorf("get device %q returned an empty 200 response", name)
		}
		status := api.NewDeviceStatus()
		if response.JSON200.Status != nil {
			status = *response.JSON200.Status
		}
		status.SystemInfo.Architecture = architecture
		status.SystemInfo.OperatingSystem = promotedOperatingSystem
		info := api.CustomDeviceInfo{"region": region}
		status.SystemInfo.CustomInfo = &info
		response.JSON200.Status = &status
		replaced, err := harness.Client.ReplaceDeviceStatusWithResponse(harness.Context, name, *response.JSON200)
		if err != nil {
			return fmt.Errorf("replace status for device %q: %w", name, err)
		}
		if replaced == nil {
			return fmt.Errorf("replace status for device %q returned no response", name)
		}
		return promotedRequireStatus(replaced.StatusCode(), replaced.Body, http.StatusOK)
	})
}

// promotedWaitForLabels waits until every expected label is present on a device.
func promotedWaitForLabels(harness *e2e.Harness, name string, expected map[string]string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if name == "" || len(expected) == 0 {
		return fmt.Errorf("device name and expected labels are required")
	}
	for key := range expected {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("expected device label key must not be empty")
		}
	}
	return promotedRetryUntil(fmt.Sprintf("labels on device %q", name), util.TIMEOUT, util.POLLING, func() error {
		device, err := harness.GetDevice(name)
		if err != nil {
			return fmt.Errorf("get device %q: %w", name, err)
		}
		if device == nil {
			return fmt.Errorf("get device %q returned no resource", name)
		}
		if device.Metadata.Labels == nil {
			return fmt.Errorf("device %q has no labels", name)
		}
		for key, value := range expected {
			if actual, exists := (*device.Metadata.Labels)[key]; !exists || actual != value {
				return fmt.Errorf("device %q label %q = %q (present=%t), want %q", name, key, actual, exists, value)
			}
		}
		return nil
	})
}

// promotedWaitForMappingReady waits for a mapping's Ready condition.
func promotedWaitForMappingReady(harness *e2e.Harness, name string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("mapping name is required")
	}
	return promotedRetryUntil(fmt.Sprintf("mapping %q Ready condition", name), util.DURATION_TIMEOUT, util.POLLING, func() error {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		if err != nil {
			return fmt.Errorf("get mapping %q: %w", name, err)
		}
		if response == nil {
			return fmt.Errorf("get mapping %q returned no response", name)
		}
		if err := promotedRequireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
			return err
		}
		if response.JSON200 == nil || response.JSON200.Status == nil || response.JSON200.Status.Conditions == nil {
			return fmt.Errorf("mapping %q has no status conditions", name)
		}
		if !api.IsStatusConditionTrue(*response.JSON200.Status.Conditions, api.ConditionTypeLabelSyncMappingReady) {
			return fmt.Errorf("mapping %q is not Ready", name)
		}
		return nil
	})
}

// promotedWaitForOwner waits until the device has the requested Fleet owner.
func promotedWaitForOwner(harness *e2e.Harness, deviceName, fleetName string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if deviceName == "" || fleetName == "" {
		return fmt.Errorf("device and Fleet names are required")
	}
	return promotedRetryUntil(fmt.Sprintf("Fleet %q ownership of device %q", fleetName, deviceName), util.TIMEOUT, util.POLLING, func() error {
		device, err := harness.GetDevice(deviceName)
		if err != nil {
			return fmt.Errorf("get device %q: %w", deviceName, err)
		}
		if device == nil {
			return fmt.Errorf("get device %q returned no resource", deviceName)
		}
		if device.Metadata.Owner == nil || *device.Metadata.Owner != "Fleet/"+fleetName {
			return fmt.Errorf("device %q owner is %v, want Fleet/%s", deviceName, device.Metadata.Owner, fleetName)
		}
		return nil
	})
}

// promotedWaitForInlineConfig waits until a device has the expected rendered inline content.
func promotedWaitForInlineConfig(harness *e2e.Harness, deviceName, configName, content string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if deviceName == "" || configName == "" {
		return fmt.Errorf("device and config names are required")
	}
	return promotedRetryUntil(fmt.Sprintf("inline config %q on device %q", configName, deviceName), util.TIMEOUT, util.POLLING, func() error {
		device, err := harness.GetDevice(deviceName)
		if err != nil {
			return fmt.Errorf("get device %q: %w", deviceName, err)
		}
		if device == nil {
			return fmt.Errorf("get device %q returned no resource", deviceName)
		}
		inline, err := harness.GetDeviceInlineConfig(device, configName)
		if err != nil {
			return fmt.Errorf("get inline config %q for device %q: %w", configName, deviceName, err)
		}
		if len(inline.Inline) != 1 || inline.Inline[0].Content != content {
			return fmt.Errorf("device %q inline config %q did not render %q", deviceName, configName, content)
		}
		return nil
	})
}

// promotedWaitForMultipleOwnerConflict waits for multiple-owner status and no rendered templates.
func promotedWaitForMultipleOwnerConflict(harness *e2e.Harness, deviceName string, configNames ...string) error {
	if err := promotedValidateHarness(harness); err != nil {
		return err
	}
	if deviceName == "" || len(configNames) == 0 {
		return fmt.Errorf("device name and config names are required")
	}
	return promotedRetryUntil(fmt.Sprintf("multiple-owner condition on device %q", deviceName), util.TIMEOUT, util.POLLING, func() error {
		device, err := harness.GetDevice(deviceName)
		if err != nil {
			return fmt.Errorf("get device %q: %w", deviceName, err)
		}
		if device == nil {
			return fmt.Errorf("get device %q returned no resource", deviceName)
		}
		if device.Status == nil || !api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners) {
			return fmt.Errorf("device %q has no multiple-owner condition", deviceName)
		}
		if device.Metadata.Owner != nil {
			return fmt.Errorf("device %q unexpectedly has owner %q", deviceName, *device.Metadata.Owner)
		}
		if device.Spec != nil && device.Spec.Config != nil {
			for _, configName := range configNames {
				if _, err := harness.GetDeviceInlineConfig(device, configName); err == nil {
					return fmt.Errorf("device %q unexpectedly rendered config %q", deviceName, configName)
				} else if !strings.Contains(err.Error(), "config not found in the device") {
					return fmt.Errorf("check config %q on device %q: %w", configName, deviceName, err)
				}
			}
		}
		return nil
	})
}

// promotedWaitForDeviceLabel waits until one device label reaches an expected value.
func promotedWaitForDeviceLabel(harness *e2e.Harness, deviceName, key, value string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("device label key is required")
	}
	return promotedWaitForLabels(harness, deviceName, map[string]string{key: value})
}

// promotedPhaseExpression creates the scoped label expression used by the changing-batch case.
func promotedPhaseExpression(harness *e2e.Harness, parisValue string) string {
	if parisValue == "" {
		GinkgoWriter.Printf("cannot build phase expression with an empty Paris value\n")
		return ""
	}
	expression := fmt.Sprintf(
		`status.systemInfo.architecture != "" ? (metadata.labels[%q] == %q ? "first" : metadata.labels[%q] == %q ? %q : "remaining") : dyn(null)`,
		labelSite, siteMadrid, labelSite, siteParis, parisValue,
	)
	return promotedScalarExpression(harness, expression)
}

// promotedWaitForTemplateVersionChange waits until a Fleet receives a different template version.
func promotedWaitForTemplateVersionChange(harness *e2e.Harness, fleetName, previous string) (string, error) {
	if err := promotedValidateHarness(harness); err != nil {
		return "", err
	}
	if fleetName == "" {
		return "", fmt.Errorf("Fleet name is required")
	}
	var current string
	err := promotedRetryUntil(fmt.Sprintf("new template version for Fleet %q", fleetName), util.TIMEOUT, util.POLLING, func() error {
		version, err := promotedFleetTemplateVersion(harness, fleetName)
		if err != nil {
			return err
		}
		if version == previous {
			return fmt.Errorf("Fleet %q still has template version %q", fleetName, version)
		}
		current = version
		return nil
	})
	return current, err
}

// promotedWaitForCompletionEvents waits until a rollout version has a completion event.
func promotedWaitForCompletionEvents(harness *e2e.Harness, fleetName string, after time.Time, version string) ([]api.Event, error) {
	if err := promotedValidateHarness(harness); err != nil {
		return nil, err
	}
	if fleetName == "" || version == "" {
		return nil, fmt.Errorf("Fleet name and template version are required")
	}
	var events []api.Event
	err := promotedRetryUntil(fmt.Sprintf("completion event for template %q", version), util.TIMEOUT, util.POLLING, func() error {
		current, err := promotedFleetEvents(harness, fleetName, after)
		if err != nil {
			return err
		}
		events = current
		count, err := promotedCompletionCount(events, version)
		if err != nil {
			return err
		}
		if count < 1 {
			return fmt.Errorf("template %q has no completion event yet", version)
		}
		return nil
	})
	return events, err
}

// promotedFixNetworkFailure repairs a simulated network failure once and updates its active flag.
func promotedFixNetworkFailure(harness *e2e.Harness, registryHost, registryPort string, active *bool) error {
	if active == nil || !*active {
		return nil
	}
	if harness == nil {
		return fmt.Errorf("network repair requires a VM harness")
	}
	if err := harness.FixNetworkFailure(registryHost, registryPort); err != nil {
		return fmt.Errorf("repair simulated registry network failure: %w", err)
	}
	*active = false
	return nil
}

// cleanupPromotedTestContext removes VM test resources created for a promoted-label rollout.
func cleanupPromotedTestContext(context *TestContext) error {
	if context == nil {
		return nil
	}
	var cleanupErrors []error
	for _, harness := range context.harnesses {
		if harness == nil {
			continue
		}
		if err := harness.CleanUpAllTestResources(); err != nil {
			GinkgoWriter.Printf("cleanup promoted-label VM resources failed: %v\n", err)
			cleanupErrors = append(cleanupErrors, err)
		}
		e2e.Cleanup(harness)
	}
	return errors.Join(cleanupErrors...)
}

// promotedValidateHarness checks that the API client is available for the test operation.
func promotedValidateHarness(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("E2E harness is nil")
	}
	if harness.Client == nil {
		return fmt.Errorf("E2E harness client is nil")
	}
	if harness.Context == nil {
		return fmt.Errorf("E2E harness context is nil")
	}
	return nil
}

// promotedRequireStatus reports unexpected response codes with their response body.
func promotedRequireStatus(actual int, body []byte, expected int) error {
	if actual != expected {
		return fmt.Errorf("unexpected API status: got %d, want %d: %s", actual, expected, body)
	}
	return nil
}

// promotedRetryUntil polls a check until it succeeds or reaches its deadline.
func promotedRetryUntil(description string, timeout, polling time.Duration, check func() error) error {
	if strings.TrimSpace(description) == "" || timeout <= 0 || polling <= 0 || check == nil {
		return fmt.Errorf("retry requires a description, check, and positive timeout and polling durations")
	}
	GinkgoWriter.Printf("waiting for %s (timeout=%s poll=%s)\n", description, timeout, polling)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := check(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(polling)
	}
	GinkgoWriter.Printf("timed out waiting for %s: %v\n", description, lastErr)
	return fmt.Errorf("timed out waiting for %s: %w", description, lastErr)
}
