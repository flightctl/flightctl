package label_sync_mapping

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	apiEventuallyTimeout = testutil.TIMEOUT
	apiPollingInterval   = testutil.POLLING
	mappingScanTimeout   = testutil.DURATION_TIMEOUT
	testIDLabel          = "test-id"
	managedLabelPrefix   = "e2e.flightctl.io/"
	architectureField    = "status.systemInfo.architecture"
	operatingSystemField = "status.systemInfo.operatingSystem"
	linuxOperatingSystem = "linux"
	amd64Architecture    = "amd64"
	arm64Architecture    = "arm64"
)

var workerHarness *e2e.Harness

var _ = Describe("Label sync mapping API lifecycle", Serial, Label("integration", "sanity", "mapping-api"), func() {
	It("supports CRUD and patch while rejecting an invalid expression shape", func() {
		harness := workerHarness
		mappingName := uniqueName(harness, "api-lifecycle")
		mappingKey := managedLabelPrefix + mappingName

		By("creating, reading, and listing a mapping")
		createdExpression := scopedScalarExpression(harness, architectureField)
		Expect(createLabelSyncMapping(harness, mappingName, &mappingKey, createdExpression)).To(Succeed())
		created, err := getLabelSyncMapping(harness, mappingName)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Metadata.Name).NotTo(BeNil())
		Expect(*created.Metadata.Name).To(Equal(mappingName))
		Expect(created.Metadata.Generation).NotTo(BeNil())
		createdGeneration := *created.Metadata.Generation

		getResponse, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, mappingName)
		Expect(err).NotTo(HaveOccurred())
		Expect(getResponse.StatusCode()).To(Equal(http.StatusOK), string(getResponse.Body))
		Expect(getResponse.JSON200).NotTo(BeNil())
		Expect(getResponse.JSON200.Spec.Expression).To(Equal(createdExpression))

		listResponse, err := harness.Client.ListLabelSyncMappingsWithResponse(harness.Context, &api.ListLabelSyncMappingsParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(listResponse.StatusCode()).To(Equal(http.StatusOK), string(listResponse.Body))
		Expect(listResponse.JSON200).NotTo(BeNil())
		mappingNames := make([]string, 0, len(listResponse.JSON200.Items))
		for _, mapping := range listResponse.JSON200.Items {
			if mapping.Metadata.Name != nil {
				mappingNames = append(mappingNames, *mapping.Metadata.Name)
			}
		}
		Expect(mappingNames).To(ContainElement(mappingName))

		By("patching the expression and waiting for the new generation")
		patchedExpression := scopedScalarExpression(harness, operatingSystemField)
		patch := api.PatchRequest{{
			Op:    api.PatchRequestOp("replace"),
			Path:  "/spec/expression",
			Value: patchedExpression,
		}}
		patchResponse, err := harness.Client.PatchLabelSyncMappingWithApplicationJSONPatchPlusJSONBodyWithResponse(harness.Context, mappingName, patch)
		Expect(err).NotTo(HaveOccurred())
		Expect(patchResponse.StatusCode()).To(Equal(http.StatusOK), string(patchResponse.Body))
		Expect(patchResponse.JSON200).NotTo(BeNil())
		Expect(patchResponse.JSON200.Spec.Expression).To(Equal(patchedExpression))
		Expect(patchResponse.JSON200.Metadata.Generation).NotTo(BeNil())
		Expect(*patchResponse.JSON200.Metadata.Generation).To(BeNumerically(">", createdGeneration))
		Expect(waitForMappingReady(harness, mappingName)).To(Succeed())

		By("rejecting an expression whose shape does not match scalar mode")
		invalidName := uniqueName(harness, "invalid-shape")
		invalid, err := newLabelSyncMapping(harness, invalidName, &mappingKey, `{"not": "scalar"}`)
		Expect(err).NotTo(HaveOccurred())
		invalidResponse, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, invalid)
		Expect(err).NotTo(HaveOccurred())
		Expect(invalidResponse.StatusCode()).To(Equal(http.StatusUnprocessableEntity), string(invalidResponse.Body))
		Expect(invalidResponse.JSON422).NotTo(BeNil())

		By("deleting the mapping asynchronously")
		Expect(deleteLabelSyncMapping(harness, mappingName)).To(Succeed())
		Expect(waitForMappingDeleted(harness, mappingName)).To(Succeed())
	})
})

var _ = Describe("Label sync mapping ownership", Serial, Label("integration", "sanity", "mapping-ownership"), func() {
	It("protects exact owned keys through cleanup and recreation", func() {
		harness := workerHarness
		suffix := uniqueName(harness, "ownership")
		mappingName := uniqueName(harness, "owned-label")
		deviceName := uniqueName(harness, "owned-device")
		ownedKey := managedLabelPrefix + "owned-" + suffix
		prefixKey := ownedKey + "-extra"
		unrelatedKey := managedLabelPrefix + "unrelated-" + suffix

		By("creating a mapping-owned label and reading its ownership metadata")
		Expect(createLabelSyncMapping(harness, mappingName, &ownedKey, scopedScalarExpression(harness, architectureField))).To(Succeed())
		created, err := getLabelSyncMapping(harness, mappingName)
		Expect(err).NotTo(HaveOccurred())
		Expect(createDeviceWithStatus(harness, deviceName, map[string]string{"operator": "preserved", ownedKey: amd64Architecture}, amd64Architecture, linuxOperatingSystem, nil)).To(Succeed())
		Expect(waitForDeviceLabels(harness, deviceName, map[string]string{ownedKey: amd64Architecture})).To(Succeed())
		Expect(waitForMappingReady(harness, mappingName)).To(Succeed())
		deviceProvenance, err := harness.Client.GetDeviceLabelSyncProvenanceWithResponse(harness.Context, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(deviceProvenance.StatusCode()).To(Equal(http.StatusOK), string(deviceProvenance.Body))
		Expect(deviceProvenance.JSON200).NotTo(BeNil())
		Expect(ownersForKey(deviceProvenance.JSON200.Items, ownedKey)).To(ConsistOf(mappingName))

		organizationProvenance, err := harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{ownedKey, prefixKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(organizationProvenance.StatusCode()).To(Equal(http.StatusOK), string(organizationProvenance.Body))
		Expect(organizationProvenance.JSON200).NotTo(BeNil())
		Expect(ownersForKey(organizationProvenance.JSON200.Items, ownedKey)).To(ConsistOf(mappingName))
		Expect(ownersForKey(organizationProvenance.JSON200.Items, prefixKey)).To(BeEmpty())

		By("blocking a changed exact-key write while allowing unchanged and unrelated writes")
		device, err := getDevice(harness, deviceName)
		Expect(err).NotTo(HaveOccurred())
		unchangedResponse, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(unchangedResponse.StatusCode()).To(Equal(http.StatusOK), string(unchangedResponse.Body))

		Expect(rejectManagedLabelWrite(harness, deviceName, ownedKey, "forged")).To(Succeed())

		device, err = getDevice(harness, deviceName)
		Expect(err).NotTo(HaveOccurred())
		allowedLabels := cloneLabels(device.Metadata.Labels)
		allowedLabels[prefixKey] = "allowed"
		allowedLabels[unrelatedKey] = "allowed"
		device.Metadata.Labels = &allowedLabels
		allowedResponse, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(allowedResponse.StatusCode()).To(Equal(http.StatusOK), string(allowedResponse.Body))
		Expect(waitForDeviceLabels(harness, deviceName, map[string]string{
			ownedKey:     amd64Architecture,
			prefixKey:    "allowed",
			unrelatedKey: "allowed",
		})).To(Succeed())

		By("deleting the mapping and verifying ownership and protection are released")
		Expect(deleteLabelSyncMapping(harness, mappingName)).To(Succeed())
		Expect(waitForDeviceLabels(harness, deviceName, map[string]string{
			prefixKey:    "allowed",
			unrelatedKey: "allowed",
		}, ownedKey)).To(Succeed())
		Expect(waitForMappingDeleted(harness, mappingName)).To(Succeed())

		organizationProvenance, err = harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{ownedKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(organizationProvenance.StatusCode()).To(Equal(http.StatusOK), string(organizationProvenance.Body))
		Expect(organizationProvenance.JSON200).NotTo(BeNil())
		Expect(ownersForKey(organizationProvenance.JSON200.Items, ownedKey)).To(BeEmpty())

		device, err = getDevice(harness, deviceName)
		Expect(err).NotTo(HaveOccurred())
		releasedLabels := cloneLabels(device.Metadata.Labels)
		releasedLabels[ownedKey] = "operator-after-cleanup"
		device.Metadata.Labels = &releasedLabels
		releasedResponse, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(releasedResponse.StatusCode()).To(Equal(http.StatusOK), string(releasedResponse.Body))
		Expect(waitForDeviceLabels(harness, deviceName, map[string]string{ownedKey: "operator-after-cleanup"})).To(Succeed())

		By("recreating the mapping with a new identity and restored protection")
		Expect(createLabelSyncMapping(harness, mappingName, &ownedKey, scopedScalarExpression(harness, operatingSystemField))).To(Succeed())
		recreated, err := getLabelSyncMapping(harness, mappingName)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Metadata.CreationTimestamp).NotTo(BeNil())
		Expect(recreated.Metadata.CreationTimestamp).NotTo(BeNil())
		Expect(*recreated.Metadata.CreationTimestamp).NotTo(Equal(*created.Metadata.CreationTimestamp))
		Expect(waitForDeviceLabels(harness, deviceName, map[string]string{ownedKey: linuxOperatingSystem})).To(Succeed())
		Expect(waitForMappingReady(harness, mappingName)).To(Succeed())

		Expect(rejectManagedLabelWrite(harness, deviceName, ownedKey, "forged-again")).To(Succeed())
	})
})

// rejectManagedLabelWrite retries stale resource versions and requires the ownership conflict.
func rejectManagedLabelWrite(harness *e2e.Harness, deviceName, key, value string) error {
	var lastBody []byte
	for attempt := 0; attempt < 5; attempt++ {
		device, err := getDevice(harness, deviceName)
		if err != nil {
			return err
		}
		labels := cloneLabels(device.Metadata.Labels)
		labels[key] = value
		device.Metadata.Labels = &labels
		response, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		if err != nil {
			return fmt.Errorf("replace device %q: %w", deviceName, err)
		}
		if response == nil {
			return fmt.Errorf("replace device %q returned no response", deviceName)
		}
		lastBody = response.Body
		if response.StatusCode() == http.StatusConflict && strings.Contains(string(lastBody), flterrors.ErrManagedLabelConflict.Error()) {
			return nil
		}
		if response.StatusCode() != http.StatusConflict || !strings.Contains(string(lastBody), flterrors.ErrResourceVersionConflict.Error()) {
			return fmt.Errorf("replace device %q label %q: expected managed-label conflict, got HTTP %d: %s", deviceName, key, response.StatusCode(), lastBody)
		}
		time.Sleep(apiPollingInterval)
	}
	return fmt.Errorf("replace device %q label %q stayed stale after 5 attempts: %s", deviceName, key, lastBody)
}

// getDevice returns a device resource and propagates API or response-shape errors.
var _ = Describe("Label sync mapping promotion", Serial, Label("integration", "sanity", "mapping-promotion"), func() {
	It("promotes scalar, conditional, and transformed map values independently", func() {
		harness := workerHarness
		suffix := uniqueName(harness, "labels")

		architectureKey := fmt.Sprintf("%sarchitecture-%s", managedLabelPrefix, suffix)
		eligibleKey := fmt.Sprintf("%seligible-%s", managedLabelPrefix, suffix)
		regionKey := fmt.Sprintf("%sregion-%s", managedLabelPrefix, suffix)
		rackKey := fmt.Sprintf("%srack-%s", managedLabelPrefix, suffix)
		ignoredKey := fmt.Sprintf("%signored-%s", managedLabelPrefix, suffix)
		failureKey := fmt.Sprintf("%sfailure-%s", managedLabelPrefix, suffix)

		architectureMapping := uniqueName(harness, "architecture")
		eligibleMapping := uniqueName(harness, "eligible")
		customInfoMapping := uniqueName(harness, "custom-info")
		failureMapping := uniqueName(harness, "failing-scalar")
		By("creating independent scalar, conditional, transformed map, and runtime-failing mappings")
		Expect(createLabelSyncMapping(harness, architectureMapping, &architectureKey, scopedScalarExpression(harness, architectureField))).To(Succeed())
		Expect(createLabelSyncMapping(harness, eligibleMapping, &eligibleKey, scopedScalarExpression(harness, `status.systemInfo.architecture == "amd64" ? true : dyn(null)`))).To(Succeed())
		Expect(createLabelSyncMapping(
			harness,
			customInfoMapping,
			nil,
			scopedMapExpression(harness, fmt.Sprintf(
				`status.systemInfo.customInfo.transformMapEntry(k, v, k in ["region", "rack"], {"%s" + k + "-%s": v})`,
				managedLabelPrefix,
				suffix,
			)),
		)).To(Succeed())
		Expect(createLabelSyncMapping(
			harness,
			failureMapping,
			&failureKey,
			scopedScalarExpression(harness, `status.systemInfo.architecture == "amd64" ? string(int(status.systemInfo.architecture)) : dyn(null)`),
		)).To(Succeed())

		matchingDevice := uniqueName(harness, "matching-device")
		nonmatchingDevice := uniqueName(harness, "nonmatching-device")
		By("reporting different system information from two devices")
		Expect(createDeviceWithStatus(harness, matchingDevice, map[string]string{"operator": "kept"}, amd64Architecture, linuxOperatingSystem, map[string]string{"region": "east", "rack": "r1", "ignored": "not-promoted"})).To(Succeed())
		Expect(createDeviceWithStatus(harness, nonmatchingDevice, map[string]string{"operator": "kept"}, arm64Architecture, linuxOperatingSystem, map[string]string{"region": "west", "rack": "r2", "ignored": "not-promoted"})).To(Succeed())

		By("waiting for independent valid outputs while the failing mapping degrades")
		Expect(waitForDeviceLabels(harness, matchingDevice, map[string]string{
			architectureKey: "amd64",
			eligibleKey:     "true",
			regionKey:       "east",
			rackKey:         "r1",
		}, failureKey, ignoredKey)).To(Succeed())
		Expect(waitForDeviceLabels(harness, nonmatchingDevice, map[string]string{
			architectureKey: "arm64",
			regionKey:       "west",
			rackKey:         "r2",
		}, eligibleKey, failureKey, ignoredKey)).To(Succeed())

		Expect(waitForMappingReady(harness, architectureMapping)).To(Succeed())
		Expect(waitForMappingReady(harness, eligibleMapping)).To(Succeed())
		Expect(waitForMappingReady(harness, customInfoMapping)).To(Succeed())
		Expect(waitForMappingDegraded(harness, failureMapping)).To(Succeed())

		By("verifying transformed-map ownership and ordinary label selection")
		provenance, err := harness.Client.GetDeviceLabelSyncProvenanceWithResponse(harness.Context, matchingDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(provenance.StatusCode()).To(Equal(http.StatusOK), string(provenance.Body))
		Expect(provenance.JSON200).NotTo(BeNil())
		Expect(ownersForKey(provenance.JSON200.Items, regionKey)).To(ConsistOf(customInfoMapping))

		selected, err := listDevicesByLabel(harness, fmt.Sprintf("%s=east", regionKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(HaveLen(1))
		Expect(selected[0].Metadata.Name).NotTo(BeNil())
		Expect(*selected[0].Metadata.Name).To(Equal(matchingDevice))
		Expect(selected[0].Metadata.Labels).NotTo(BeNil())
		Expect(*selected[0].Metadata.Labels).To(HaveKeyWithValue(regionKey, "east"))
	})
})

var _ = Describe("Label sync mapping reconciliation", Serial, Label("integration", "sanity", "mapping-reconciliation"), func() {
	It("converges labels across status, mapping, deletion, and recreation changes", func() {
		harness := workerHarness
		suffix := uniqueName(harness, "reconcile")

		initialKey := fmt.Sprintf("%sinitial-%s", managedLabelPrefix, suffix)
		updatedKey := fmt.Sprintf("%supdated-%s", managedLabelPrefix, suffix)
		recreatedKey := fmt.Sprintf("%srecreated-%s", managedLabelPrefix, suffix)
		regionKey := fmt.Sprintf("%sregion-%s", managedLabelPrefix, suffix)
		rackKey := fmt.Sprintf("%srack-%s", managedLabelPrefix, suffix)
		modeChangedKey := fmt.Sprintf("%smode-%s", managedLabelPrefix, suffix)
		scalarMapping := uniqueName(harness, "changing-scalar")
		mapMapping := uniqueName(harness, "changing-map")

		By("creating scalar and transformed-map mappings")
		Expect(createLabelSyncMapping(
			harness,
			scalarMapping,
			&initialKey,
			scopedScalarExpression(harness, `status.systemInfo.architecture == "amd64" ? status.systemInfo.architecture : dyn(null)`),
		)).To(Succeed())
		Expect(createLabelSyncMapping(
			harness,
			mapMapping,
			nil,
			scopedMapExpression(harness, fmt.Sprintf(
				`status.systemInfo.customInfo.transformMapEntry(k, v, k in ["region", "rack"], {"%s" + k + "-%s": v})`,
				managedLabelPrefix,
				suffix,
			)),
		)).To(Succeed())

		deviceName := uniqueName(harness, "changing-device")
		By("reporting initial status and waiting for promoted labels")
		Expect(createDeviceWithStatus(
			harness,
			deviceName,
			map[string]string{"operator": "preserved"},
			"amd64",
			"linux",
			map[string]string{"region": "east", "rack": "r1"},
		)).To(Succeed())
		Expect(waitForDeviceLabels(harness, deviceName, map[string]string{
			"operator": "preserved",
			initialKey: "amd64",
			regionKey:  "east",
			rackKey:    "r1",
		})).To(Succeed())
		Expect(waitForMappingReady(harness, scalarMapping)).To(Succeed())
		Expect(waitForMappingReady(harness, mapMapping)).To(Succeed())

		devices, err := listDevicesByLabel(harness, fmt.Sprintf("%s=east", regionKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(devices).To(HaveLen(1))

		By("changing status without re-enrollment and verifying selector movement")
		Expect(replaceDeviceStatus(harness, deviceName, arm64Architecture, linuxOperatingSystem, map[string]string{"rack": "r2"})).To(Succeed())
		Expect(waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", rackKey: "r2"},
			initialKey,
			regionKey,
		)).To(Succeed())
		devices, err = listDevicesByLabel(harness, fmt.Sprintf("%s=east", regionKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(devices).To(BeEmpty())
		devices, err = listDevicesByLabel(harness, fmt.Sprintf("%s=r2", rackKey))
		Expect(err).NotTo(HaveOccurred())
		Expect(devices).To(HaveLen(1))

		By("changing scalar destination and expression inputs")
		Expect(replaceLabelSyncMapping(harness, scalarMapping, &updatedKey, scopedScalarExpression(harness, architectureField))).To(Succeed())
		Expect(waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", updatedKey: "arm64"},
			initialKey,
		)).To(Succeed())
		Expect(waitForMappingReady(harness, scalarMapping)).To(Succeed())

		By("removing all map inputs and changing the mapping from map to scalar mode")
		Expect(replaceDeviceStatus(harness, deviceName, arm64Architecture, linuxOperatingSystem, map[string]string{})).To(Succeed())
		Expect(waitForDeviceLabels(harness, deviceName, map[string]string{"operator": "preserved", updatedKey: arm64Architecture}, regionKey, rackKey)).To(Succeed())

		Expect(replaceLabelSyncMapping(harness, mapMapping, &modeChangedKey, scopedScalarExpression(harness, `"mode-changed"`))).To(Succeed())
		Expect(waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", updatedKey: "arm64", modeChangedKey: "mode-changed"},
			regionKey,
			rackKey,
		)).To(Succeed())
		Expect(waitForMappingReady(harness, mapMapping)).To(Succeed())

		By("deleting and recreating the scalar mapping with the same name")
		Expect(deleteLabelSyncMapping(harness, scalarMapping)).To(Succeed())
		Expect(waitForDeviceLabels(harness, deviceName, map[string]string{"operator": "preserved", modeChangedKey: "mode-changed"}, updatedKey)).To(Succeed())
		Expect(waitForMappingDeleted(harness, scalarMapping)).To(Succeed())

		Expect(createLabelSyncMapping(harness, scalarMapping, &recreatedKey, scopedScalarExpression(harness, operatingSystemField))).To(Succeed())
		Expect(waitForDeviceLabels(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", recreatedKey: "linux", modeChangedKey: "mode-changed"},
			initialKey,
			updatedKey,
			regionKey,
		)).To(Succeed())
		Expect(waitForMappingReady(harness, scalarMapping)).To(Succeed())
	})
})

// uniqueName returns a resource name scoped to the current Ginkgo spec.
func uniqueName(harness *e2e.Harness, prefix string) string {
	if harness == nil || prefix == "" {
		GinkgoWriter.Printf("cannot build a unique name: harness=%t prefix=%q\n", harness != nil, prefix)
		return ""
	}
	suffix := strings.TrimPrefix(harness.GetTestIDFromContext(), "test-")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("%s-%s", prefix, suffix)
}

// createLabelSyncMapping creates a mapping and schedules its asynchronous cleanup.
func createLabelSyncMapping(harness *e2e.Harness, name string, key *string, expression string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" || strings.TrimSpace(expression) == "" {
		return fmt.Errorf("mapping name and expression are required")
	}
	mapping, err := newLabelSyncMapping(harness, name, key, expression)
	if err != nil {
		return err
	}

	GinkgoWriter.Printf("creating label sync mapping %q\n", name)
	response, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, mapping)
	if err != nil {
		return fmt.Errorf("create label sync mapping %q: %w", name, err)
	}
	if response == nil {
		return fmt.Errorf("create label sync mapping %q returned no response", name)
	}
	if err := requireStatus(response.StatusCode(), response.Body, http.StatusCreated); err != nil {
		return err
	}
	if response.JSON201 == nil {
		return fmt.Errorf("create label sync mapping %q returned an empty 201 response", name)
	}
	DeferCleanup(cleanupLabelSyncMapping, harness, name)
	return nil
}

// getLabelSyncMapping fetches one mapping and reports an actionable error for empty responses.
func getLabelSyncMapping(harness *e2e.Harness, name string) (*api.LabelSyncMapping, error) {
	if err := validateHarness(harness); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("mapping name is required")
	}
	response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
	if err != nil {
		return nil, fmt.Errorf("get label sync mapping %q: %w", name, err)
	}
	if response == nil {
		return nil, fmt.Errorf("get label sync mapping %q returned no response", name)
	}
	if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
		return nil, err
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("get label sync mapping %q returned an empty 200 response", name)
	}
	return response.JSON200, nil
}

// scopedScalarExpression limits scalar expressions to resources created by the current spec.
func scopedScalarExpression(harness *e2e.Harness, expression string) string {
	if harness == nil || expression == "" {
		GinkgoWriter.Printf("cannot scope scalar mapping expression: harness=%t expression=%q\n", harness != nil, expression)
		return ""
	}
	testID := harness.GetTestIDFromContext()
	if testID == "" {
		GinkgoWriter.Printf("cannot scope scalar mapping expression: test ID is empty\n")
		return ""
	}
	return fmt.Sprintf(`metadata.labels[%q] == %q ? (%s) : dyn(null)`, testIDLabel, testID, expression)
}

// scopedMapExpression limits map expressions to resources created by the current spec.
func scopedMapExpression(harness *e2e.Harness, expression string) string {
	if harness == nil || expression == "" {
		GinkgoWriter.Printf("cannot scope map mapping expression: harness=%t expression=%q\n", harness != nil, expression)
		return ""
	}
	testID := harness.GetTestIDFromContext()
	if testID == "" {
		GinkgoWriter.Printf("cannot scope map mapping expression: test ID is empty\n")
		return ""
	}
	return fmt.Sprintf(`metadata.labels[%q] == %q ? (%s) : {}`, testIDLabel, testID, expression)
}

// newLabelSyncMapping constructs a mapping object with the current spec's isolation label.
func newLabelSyncMapping(harness *e2e.Harness, name string, key *string, expression string) (api.LabelSyncMapping, error) {
	if err := validateHarness(harness); err != nil {
		return api.LabelSyncMapping{}, err
	}
	if name == "" || strings.TrimSpace(expression) == "" {
		return api.LabelSyncMapping{}, fmt.Errorf("mapping name and expression are required")
	}
	testID := harness.GetTestIDFromContext()
	if testID == "" {
		return api.LabelSyncMapping{}, fmt.Errorf("test ID is required to scope label sync mapping %q", name)
	}
	labels := map[string]string{testIDLabel: testID}
	return api.LabelSyncMapping{
		ApiVersion: api.LabelSyncMappingAPIVersion,
		Kind:       api.LabelSyncMappingKind(api.LabelSyncMappingKindValue),
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: &labels,
		},
		Spec: api.LabelSyncMappingSpec{
			Expression:   expression,
			Key:          key,
			ResourceType: api.LabelSyncMappingSpecResourceTypeDevice,
		},
	}, nil
}

// createDeviceWithStatus enrolls a device and reports its initial system information.
func createDeviceWithStatus(harness *e2e.Harness, name string, labels map[string]string, architecture, operatingSystem string, customInfo map[string]string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("device name is required")
	}
	if architecture == "" || operatingSystem == "" {
		return fmt.Errorf("device architecture and operating system are required")
	}
	GinkgoWriter.Printf("creating device %q with architecture %q\n", name, architecture)
	_, err := resources.CreateDevice(harness, name, &labels)
	if err != nil {
		return fmt.Errorf("create device %q: %w", name, err)
	}
	return replaceDeviceStatus(harness, name, architecture, operatingSystem, customInfo)
}

// replaceDeviceStatus retries a status update until the API accepts its current resource version.
func replaceDeviceStatus(harness *e2e.Harness, name, architecture, operatingSystem string, customInfo map[string]string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("device name is required")
	}
	return retryUntil(fmt.Sprintf("replace status for device %q", name), apiEventuallyTimeout, apiPollingInterval, func() error {
		getResponse, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
		if err != nil {
			return fmt.Errorf("get device %q before status update: %w", name, err)
		}
		if err := requireStatus(getResponse.StatusCode(), getResponse.Body, http.StatusOK); err != nil {
			return err
		}
		if getResponse.JSON200 == nil {
			return fmt.Errorf("get device %q returned an empty 200 response", name)
		}

		status := api.NewDeviceStatus()
		status.SystemInfo.Architecture = architecture
		status.SystemInfo.OperatingSystem = operatingSystem
		if customInfo != nil {
			info := api.CustomDeviceInfo(customInfo)
			status.SystemInfo.CustomInfo = &info
		}
		getResponse.JSON200.Status = &status

		replaceResponse, err := harness.Client.ReplaceDeviceStatusWithResponse(harness.Context, name, *getResponse.JSON200)
		if err != nil {
			return fmt.Errorf("replace status for device %q: %w", name, err)
		}
		return requireStatus(replaceResponse.StatusCode(), replaceResponse.Body, http.StatusOK)
	})
}

// waitForDeviceLabels waits until the expected labels exist and absent labels are removed.
func waitForDeviceLabels(harness *e2e.Harness, name string, expected map[string]string, absent ...string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("device name is required")
	}
	if len(expected) == 0 && len(absent) == 0 {
		return fmt.Errorf("expected or absent device labels are required")
	}
	return retryUntil(fmt.Sprintf("labels on device %q", name), mappingScanTimeout, apiPollingInterval, func() error {
		response, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
		if err != nil {
			return fmt.Errorf("get device %q labels: %w", name, err)
		}
		if response == nil {
			return fmt.Errorf("get device %q labels returned no response", name)
		}
		if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
			return err
		}
		if response.JSON200 == nil || response.JSON200.Metadata.Labels == nil {
			return fmt.Errorf("device %q returned no metadata labels", name)
		}

		labels := *response.JSON200.Metadata.Labels
		for key, value := range expected {
			if actual, exists := labels[key]; !exists || actual != value {
				return fmt.Errorf("device %q label %q = %q (present=%t), want %q", name, key, actual, exists, value)
			}
		}
		for _, key := range absent {
			if _, exists := labels[key]; exists {
				return fmt.Errorf("device %q still has label %q", name, key)
			}
		}
		return nil
	})
}

// waitForMappingReady waits for a mapping's Ready condition to report success.
func waitForMappingReady(harness *e2e.Harness, name string) error {
	return waitForMappingCondition(harness, name, api.ConditionStatusTrue, "Success")
}

// waitForMappingDegraded waits for a mapping to report a degraded Ready condition.
func waitForMappingDegraded(harness *e2e.Harness, name string) error {
	return waitForMappingCondition(harness, name, api.ConditionStatusFalse, "Degraded")
}

// waitForMappingCondition polls a mapping until its Ready condition matches status and reason.
func waitForMappingCondition(harness *e2e.Harness, name string, status api.ConditionStatus, reason string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" || reason == "" {
		return fmt.Errorf("mapping name and condition reason are required")
	}
	// Mapping Ready is published by a periodic scan whose default interval is two minutes.
	return retryUntil(fmt.Sprintf("mapping %q condition %s/%s", name, status, reason), mappingScanTimeout, apiPollingInterval, func() error {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		if err != nil {
			return fmt.Errorf("get label sync mapping %q: %w", name, err)
		}
		if response == nil {
			return fmt.Errorf("get label sync mapping %q returned no response", name)
		}
		if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
			return err
		}
		if response.JSON200 == nil || response.JSON200.Status == nil || response.JSON200.Status.Conditions == nil {
			return fmt.Errorf("mapping %q has no status conditions", name)
		}
		generation := response.JSON200.Metadata.Generation
		if generation == nil {
			return fmt.Errorf("mapping %q has no metadata generation", name)
		}

		observed := make([]string, 0, len(*response.JSON200.Status.Conditions))
		for _, condition := range *response.JSON200.Status.Conditions {
			observedGeneration := "unset"
			if condition.ObservedGeneration != nil {
				observedGeneration = fmt.Sprint(*condition.ObservedGeneration)
			}
			observed = append(observed, fmt.Sprintf("%s=%s/%s at generation %s", condition.Type, condition.Status, condition.Reason, observedGeneration))
			if condition.Type == api.ConditionTypeLabelSyncMappingReady && condition.Status == status && condition.Reason == reason &&
				condition.ObservedGeneration != nil && *condition.ObservedGeneration == *generation {
				return nil
			}
		}
		return fmt.Errorf("mapping %q has not reached Ready=%s reason=%q at generation %d; observed conditions: %v", name, status, reason, *generation, observed)
	})
}

// waitForMappingDeleted waits until the API no longer returns the mapping.
func waitForMappingDeleted(harness *e2e.Harness, name string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("mapping name is required")
	}
	return retryUntil(fmt.Sprintf("deletion of mapping %q", name), mappingScanTimeout, apiPollingInterval, func() error {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		if err != nil {
			return fmt.Errorf("get label sync mapping %q during deletion: %w", name, err)
		}
		if response == nil {
			return fmt.Errorf("get label sync mapping %q during deletion returned no response", name)
		}
		if response.StatusCode() == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("mapping %q still exists: HTTP %d: %s", name, response.StatusCode(), response.Body)
	})
}

// deleteLabelSyncMapping requests deletion and reports API errors to the calling test.
func deleteLabelSyncMapping(harness *e2e.Harness, name string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("mapping name is required")
	}
	GinkgoWriter.Printf("deleting label sync mapping %q\n", name)
	response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, name)
	if err != nil {
		return fmt.Errorf("delete label sync mapping %q: %w", name, err)
	}
	if response == nil {
		return fmt.Errorf("delete label sync mapping %q returned no response", name)
	}
	return requireStatus(response.StatusCode(), response.Body, http.StatusOK)
}

// replaceLabelSyncMapping updates the destination key and expression for an existing mapping.
func replaceLabelSyncMapping(harness *e2e.Harness, name string, key *string, expression string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" || strings.TrimSpace(expression) == "" {
		return fmt.Errorf("mapping name and expression are required")
	}
	getResponse, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
	if err != nil {
		return fmt.Errorf("get label sync mapping %q before replace: %w", name, err)
	}
	if getResponse == nil {
		return fmt.Errorf("get label sync mapping %q before replace returned no response", name)
	}
	if err := requireStatus(getResponse.StatusCode(), getResponse.Body, http.StatusOK); err != nil {
		return err
	}
	if getResponse.JSON200 == nil {
		return fmt.Errorf("get label sync mapping %q returned an empty 200 response", name)
	}

	getResponse.JSON200.Spec.Key = key
	getResponse.JSON200.Spec.Expression = expression
	replaceResponse, err := harness.Client.ReplaceLabelSyncMappingWithResponse(harness.Context, name, *getResponse.JSON200)
	if err != nil {
		return fmt.Errorf("replace label sync mapping %q: %w", name, err)
	}
	if replaceResponse == nil {
		return fmt.Errorf("replace label sync mapping %q returned no response", name)
	}
	if err := requireStatus(replaceResponse.StatusCode(), replaceResponse.Body, http.StatusOK); err != nil {
		return err
	}
	if replaceResponse.JSON200 == nil {
		return fmt.Errorf("replace label sync mapping %q returned an empty 200 response", name)
	}
	return nil
}

// listDevicesByLabel returns all devices matching a selector after validating the API response.
func listDevicesByLabel(harness *e2e.Harness, selector string) ([]api.Device, error) {
	if err := validateHarness(harness); err != nil {
		return nil, err
	}
	if strings.TrimSpace(selector) == "" {
		return nil, fmt.Errorf("device label selector is required")
	}
	response, err := harness.Client.ListDevicesWithResponse(harness.Context, &api.ListDevicesParams{LabelSelector: &selector})
	if err != nil {
		return nil, fmt.Errorf("list devices for selector %q: %w", selector, err)
	}
	if response == nil {
		return nil, fmt.Errorf("list devices for selector %q returned no response", selector)
	}
	if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
		return nil, err
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("list devices for selector %q returned an empty 200 response", selector)
	}
	return response.JSON200.Items, nil
}

// getDevice returns a device resource and propagates API or response-shape errors.
func getDevice(harness *e2e.Harness, name string) (*api.Device, error) {
	if err := validateHarness(harness); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("device name is required")
	}
	response, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
	if err != nil {
		return nil, fmt.Errorf("get device %q: %w", name, err)
	}
	if response == nil {
		return nil, fmt.Errorf("get device %q returned no response", name)
	}
	if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
		return nil, err
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("get device %q returned an empty 200 response", name)
	}
	return response.JSON200, nil
}

// cloneLabels returns a writable copy of a possibly nil label map.
func cloneLabels(labels *map[string]string) map[string]string {
	cloned := map[string]string{}
	if labels == nil {
		return cloned
	}
	for key, value := range *labels {
		cloned[key] = value
	}
	return cloned
}

// ownersForKey returns the mapping owners for a key, or nil when no matching key exists.
func ownersForKey(items []api.LabelSyncProvenanceItem, key string) []string {
	if key == "" {
		return nil
	}
	for _, item := range items {
		if item.Key == key {
			return item.Owners
		}
	}
	return nil
}

// cleanupLabelSyncMapping deletes a mapping during Ginkgo cleanup and waits for removal.
func cleanupLabelSyncMapping(harness *e2e.Harness, name string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	GinkgoWriter.Printf("cleaning up label sync mapping %q\n", name)
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
	if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
		return err
	}
	return waitForMappingDeleted(harness, name)
}

// validateHarness checks that an E2E harness has the client needed by API helpers.
func validateHarness(harness *e2e.Harness) error {
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

// requireStatus returns an error containing the response body when an API status is unexpected.
func requireStatus(actual int, body []byte, expected int) error {
	if actual != expected {
		return fmt.Errorf("unexpected API status: got %d, want %d: %s", actual, expected, body)
	}
	return nil
}

// retryUntil retries a check until it succeeds or the configured deadline expires.
func retryUntil(description string, timeout, polling time.Duration, check func() error) error {
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("retry description is required")
	}
	if timeout <= 0 || polling <= 0 || check == nil {
		return fmt.Errorf("retry for %q requires positive timeout and polling durations and a check", description)
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
