package label_sync_mapping

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type mappingCleanupContextKey struct{}

type mappingCleanupRegistry struct {
	names map[string]struct{}
}

const (
	apiEventuallyTimeout = testutil.TIMEOUT
	apiPollingInterval   = testutil.POLLING
)

func uniqueName(harness *e2e.Harness, prefix string) string {
	suffix := strings.TrimPrefix(harness.GetTestIDFromContext(), "test-")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("%s-%s", prefix, suffix)
}

func createLabelSyncMapping(harness *e2e.Harness, name string, key *string, expression string) *api.LabelSyncMapping {
	mapping := newLabelSyncMapping(harness, name, key, expression)

	response, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, mapping)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusCreated), string(response.Body))
	Expect(response.JSON201).NotTo(BeNil())

	registerMappingCleanup(harness, name)

	return response.JSON201
}

func registerMappingCleanup(harness *e2e.Harness, name string) {
	registry, ok := harness.Context.Value(mappingCleanupContextKey{}).(*mappingCleanupRegistry)
	if !ok {
		registry = &mappingCleanupRegistry{names: map[string]struct{}{}}
		harness.SetTestContext(context.WithValue(harness.Context, mappingCleanupContextKey{}, registry))
		DeferCleanup(func() {
			names := make([]string, 0, len(registry.names))
			for mappingName := range registry.names {
				names = append(names, mappingName)
			}
			sort.Strings(names)

			for _, mappingName := range names {
				response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, mappingName)
				Expect(err).NotTo(HaveOccurred())
				Expect(response.StatusCode()).To(BeElementOf(http.StatusOK, http.StatusNotFound), string(response.Body))
			}
			Eventually(func(g Gomega) {
				for _, mappingName := range names {
					response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, mappingName)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(response.StatusCode()).To(Equal(http.StatusNotFound), string(response.Body))
				}
			}).Should(Succeed())
		})
	}
	registry.names[name] = struct{}{}
}

func scopedScalarExpression(harness *e2e.Harness, expression string) string {
	return fmt.Sprintf(`metadata.labels["test-id"] == %q ? (%s) : dyn(null)`, harness.GetTestIDFromContext(), expression)
}

func scopedMapExpression(harness *e2e.Harness, expression string) string {
	return fmt.Sprintf(`metadata.labels["test-id"] == %q ? (%s) : {}`, harness.GetTestIDFromContext(), expression)
}

func newLabelSyncMapping(harness *e2e.Harness, name string, key *string, expression string) api.LabelSyncMapping {
	labels := map[string]string{"test-id": harness.GetTestIDFromContext()}
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
	}
}

func createDeviceWithStatus(harness *e2e.Harness, name string, labels map[string]string, architecture, operatingSystem string, customInfo map[string]string) *api.Device {
	device, err := resources.CreateDevice(harness, name, &labels)
	Expect(err).NotTo(HaveOccurred())
	replaceDeviceStatus(harness, name, architecture, operatingSystem, customInfo)
	return device
}

func replaceDeviceStatus(harness *e2e.Harness, name, architecture, operatingSystem string, customInfo map[string]string) {
	Eventually(func(g Gomega) {
		getResponse, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(getResponse.StatusCode()).To(Equal(http.StatusOK), string(getResponse.Body))
		g.Expect(getResponse.JSON200).NotTo(BeNil())

		status := api.NewDeviceStatus()
		status.SystemInfo.Architecture = architecture
		status.SystemInfo.OperatingSystem = operatingSystem
		if customInfo != nil {
			info := api.CustomDeviceInfo(customInfo)
			status.SystemInfo.CustomInfo = &info
		}
		getResponse.JSON200.Status = &status

		replaceResponse, err := harness.Client.ReplaceDeviceStatusWithResponse(harness.Context, name, *getResponse.JSON200)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(replaceResponse.StatusCode()).To(Equal(http.StatusOK), string(replaceResponse.Body))
	}).WithTimeout(apiEventuallyTimeout).WithPolling(apiPollingInterval).Should(Succeed())
}

func waitForDeviceLabels(harness *e2e.Harness, name string, expected map[string]string, absent ...string) *api.Device {
	var device *api.Device
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Metadata.Labels).NotTo(BeNil())

		labels := *response.JSON200.Metadata.Labels
		for key, value := range expected {
			g.Expect(labels).To(HaveKeyWithValue(key, value))
		}
		for _, key := range absent {
			g.Expect(labels).NotTo(HaveKey(key))
		}
		device = response.JSON200
	}).Should(Succeed())
	return device
}

func waitForMappingReady(harness *e2e.Harness, name string) *api.LabelSyncMapping {
	return waitForMappingCondition(harness, name, api.ConditionStatusTrue, "Success")
}

func waitForMappingDegraded(harness *e2e.Harness, name string) *api.LabelSyncMapping {
	return waitForMappingCondition(harness, name, api.ConditionStatusFalse, "Degraded")
}

func waitForMappingCondition(harness *e2e.Harness, name string, status api.ConditionStatus, reason string) *api.LabelSyncMapping {
	var mapping *api.LabelSyncMapping
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Status).NotTo(BeNil())
		g.Expect(response.JSON200.Status.Conditions).NotTo(BeNil())

		ready := false
		for _, condition := range *response.JSON200.Status.Conditions {
			if condition.Type == api.ConditionTypeLabelSyncMappingReady && condition.Status == status && condition.Reason == reason {
				ready = true
				break
			}
		}
		g.Expect(ready).To(BeTrue())
		mapping = response.JSON200
	}).Should(Succeed())
	return mapping
}

func waitForMappingDeleted(harness *e2e.Harness, name string) {
	Eventually(func(g Gomega) {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusNotFound), string(response.Body))
	}).Should(Succeed())
}

func deleteLabelSyncMapping(harness *e2e.Harness, name string) {
	response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, name)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
}

func replaceLabelSyncMapping(harness *e2e.Harness, name string, key *string, expression string) *api.LabelSyncMapping {
	getResponse, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
	Expect(err).NotTo(HaveOccurred())
	Expect(getResponse.StatusCode()).To(Equal(http.StatusOK), string(getResponse.Body))
	Expect(getResponse.JSON200).NotTo(BeNil())

	getResponse.JSON200.Spec.Key = key
	getResponse.JSON200.Spec.Expression = expression
	replaceResponse, err := harness.Client.ReplaceLabelSyncMappingWithResponse(harness.Context, name, *getResponse.JSON200)
	Expect(err).NotTo(HaveOccurred())
	Expect(replaceResponse.StatusCode()).To(Equal(http.StatusOK), string(replaceResponse.Body))
	Expect(replaceResponse.JSON200).NotTo(BeNil())
	return replaceResponse.JSON200
}

func listDevicesByLabel(harness *e2e.Harness, selector string) []api.Device {
	response, err := harness.Client.ListDevicesWithResponse(harness.Context, &api.ListDevicesParams{LabelSelector: &selector})
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
	Expect(response.JSON200).NotTo(BeNil())
	return response.JSON200.Items
}

var _ = Describe("Label sync mapping API lifecycle", Serial, Label("integration", "sanity", "mapping-api"), func() {
	It("supports CRUD and patch while rejecting an invalid expression shape", func() {
		harness := e2e.GetWorkerHarness()
		mappingName := uniqueName(harness, "api-lifecycle")
		mappingKey := "e2e.flightctl.io/" + mappingName

		By("creating, reading, and listing a mapping")
		createdExpression := scopedScalarExpression(harness, "status.systemInfo.architecture")
		created := createLabelSyncMapping(harness, mappingName, &mappingKey, createdExpression)
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
		found := false
		for _, mapping := range listResponse.JSON200.Items {
			if mapping.Metadata.Name != nil && *mapping.Metadata.Name == mappingName {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue())

		By("patching the expression and waiting for the new generation")
		patchedExpression := scopedScalarExpression(harness, "status.systemInfo.operatingSystem")
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
		waitForMappingReady(harness, mappingName)

		By("rejecting an expression whose shape does not match scalar mode")
		invalidName := uniqueName(harness, "invalid-shape")
		invalid := newLabelSyncMapping(harness, invalidName, &mappingKey, `{"not": "scalar"}`)
		invalidResponse, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, invalid)
		Expect(err).NotTo(HaveOccurred())
		Expect(invalidResponse.StatusCode()).To(Equal(http.StatusUnprocessableEntity), string(invalidResponse.Body))
		Expect(invalidResponse.JSON422).NotTo(BeNil())

		By("deleting the mapping asynchronously")
		deleteLabelSyncMapping(harness, mappingName)
		waitForMappingDeleted(harness, mappingName)
	})
})

var _ = Describe("Label sync mapping ownership", Serial, Label("integration", "sanity", "mapping-ownership"), func() {
	It("protects exact owned keys through cleanup and recreation", func() {
		harness := e2e.GetWorkerHarness()
		suffix := uniqueName(harness, "ownership")
		mappingName := uniqueName(harness, "owned-label")
		deviceName := uniqueName(harness, "owned-device")
		ownedKey := "e2e.flightctl.io/owned-" + suffix
		prefixKey := ownedKey + "-extra"
		unrelatedKey := "e2e.flightctl.io/unrelated-" + suffix

		By("creating a mapping-owned label and reading its ownership metadata")
		created := createLabelSyncMapping(harness, mappingName, &ownedKey, scopedScalarExpression(harness, "status.systemInfo.architecture"))
		createDeviceWithStatus(harness, deviceName, map[string]string{"operator": "preserved", ownedKey: "amd64"}, "amd64", "linux", nil)
		waitForDeviceLabels(harness, deviceName, map[string]string{ownedKey: "amd64"})
		waitForMappingReady(harness, mappingName)
		Eventually(func(g Gomega) {
			response, err := harness.Client.GetDeviceWithResponse(harness.Context, deviceName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
			g.Expect(response.JSON200).NotTo(BeNil())
			g.Expect(response.JSON200.Metadata.Annotations).NotTo(BeNil())
			g.Expect(*response.JSON200.Metadata.Annotations).To(HaveKey(api.DeviceAnnotationManagedLabels))
		}).Should(Succeed())

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
		device := getDevice(harness, deviceName)
		changedLabels := cloneLabels(device.Metadata.Labels)
		changedLabels[ownedKey] = "forged"
		device.Metadata.Labels = &changedLabels
		blockedResponse, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(blockedResponse.StatusCode()).To(Equal(http.StatusConflict), string(blockedResponse.Body))

		device = getDevice(harness, deviceName)
		allowedLabels := cloneLabels(device.Metadata.Labels)
		allowedLabels[prefixKey] = "allowed"
		allowedLabels[unrelatedKey] = "allowed"
		device.Metadata.Labels = &allowedLabels
		allowedResponse, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(allowedResponse.StatusCode()).To(Equal(http.StatusOK), string(allowedResponse.Body))
		waitForDeviceLabels(harness, deviceName, map[string]string{
			ownedKey:     "amd64",
			prefixKey:    "allowed",
			unrelatedKey: "allowed",
		})

		By("deleting the mapping and verifying ownership and protection are released")
		deleteLabelSyncMapping(harness, mappingName)
		waitForDeviceLabels(harness, deviceName, map[string]string{
			prefixKey:    "allowed",
			unrelatedKey: "allowed",
		}, ownedKey)
		waitForMappingDeleted(harness, mappingName)

		organizationProvenance, err = harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{ownedKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(organizationProvenance.StatusCode()).To(Equal(http.StatusOK), string(organizationProvenance.Body))
		Expect(organizationProvenance.JSON200).NotTo(BeNil())
		Expect(ownersForKey(organizationProvenance.JSON200.Items, ownedKey)).To(BeEmpty())

		device = getDevice(harness, deviceName)
		releasedLabels := cloneLabels(device.Metadata.Labels)
		releasedLabels[ownedKey] = "operator-after-cleanup"
		device.Metadata.Labels = &releasedLabels
		releasedResponse, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(releasedResponse.StatusCode()).To(Equal(http.StatusOK), string(releasedResponse.Body))
		waitForDeviceLabels(harness, deviceName, map[string]string{ownedKey: "operator-after-cleanup"})

		By("recreating the mapping with a new identity and restored protection")
		recreated := createLabelSyncMapping(harness, mappingName, &ownedKey, scopedScalarExpression(harness, "status.systemInfo.operatingSystem"))
		Expect(created.Metadata.CreationTimestamp).NotTo(BeNil())
		Expect(recreated.Metadata.CreationTimestamp).NotTo(BeNil())
		Expect(*recreated.Metadata.CreationTimestamp).NotTo(Equal(*created.Metadata.CreationTimestamp))
		waitForDeviceLabels(harness, deviceName, map[string]string{ownedKey: "linux"})
		waitForMappingReady(harness, mappingName)
		organizationProvenance, err = harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{ownedKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(organizationProvenance.StatusCode()).To(Equal(http.StatusOK), string(organizationProvenance.Body))
		Expect(organizationProvenance.JSON200).NotTo(BeNil())
		Expect(ownersForKey(organizationProvenance.JSON200.Items, ownedKey)).To(ConsistOf(mappingName))

		device = getDevice(harness, deviceName)
		recreatedLabels := cloneLabels(device.Metadata.Labels)
		recreatedLabels[ownedKey] = "forged-again"
		device.Metadata.Labels = &recreatedLabels
		blockedResponse, err = harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(blockedResponse.StatusCode()).To(Equal(http.StatusConflict), string(blockedResponse.Body))
	})
})

func getDevice(harness *e2e.Harness, name string) *api.Device {
	response, err := harness.Client.GetDeviceWithResponse(harness.Context, name)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
	Expect(response.JSON200).NotTo(BeNil())
	return response.JSON200
}

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

func ownersForKey(items []api.LabelSyncProvenanceItem, key string) []string {
	for _, item := range items {
		if item.Key == key {
			return item.Owners
		}
	}
	return nil
}

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
