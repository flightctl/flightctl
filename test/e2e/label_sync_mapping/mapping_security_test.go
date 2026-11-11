package label_sync_mapping

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	initialArchitectureMapping = "system-info"
	initialArchitectureKey     = "systeminfo.flightctl.io/architecture"
	noOpAuditWindow            = 5 * testutil.POLLING
)

var _ = Describe("Label sync security and audit", Serial, Label("integration", "sanity", "mapping-security"), func() {
	It("reserves a scalar key even when the mapping currently emits no value", func() {
		h := workerHarness
		key := managedLabelPrefix + uniqueName(h, "empty")
		name := uniqueName(h, "empty-map")
		deviceName := uniqueName(h, "empty-device")
		Expect(createLabelSyncMapping(h, name, &key, scopedScalarExpression(h, "dyn(null)"))).To(Succeed())
		Expect(securityCreateDevice(h, deviceName, nil, amd64Architecture, nil)).To(Succeed())
		Expect(waitForMappingReady(h, name)).To(Succeed())
		Expect(waitForDeviceLabels(h, deviceName, nil, key)).To(Succeed())

		By("rejecting an operator write and another mapping for the reserved key")
		Expect(rejectManagedLabelWrite(h, deviceName, key, "operator")).To(Succeed())

		duplicate, err := newLabelSyncMapping(h, uniqueName(h, "duplicate"), &key, scopedScalarExpression(h, architectureField))
		Expect(err).NotTo(HaveOccurred())
		created, err := h.Client.CreateLabelSyncMappingWithResponse(h.Context, duplicate)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.StatusCode()).To(Equal(http.StatusConflict), string(created.Body))

		By("reporting the reservation in organization provenance while device provenance stays empty")
		owners, err := securityOrgOwners(h, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(owners).To(ConsistOf(name))
		provenance, err := h.Client.GetDeviceLabelSyncProvenanceWithResponse(h.Context, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(provenance.StatusCode()).To(Equal(http.StatusOK), string(provenance.Body))
		Expect(provenance.JSON200).NotTo(BeNil())
		Expect(provenance.JSON200.Items).To(BeEmpty())
		owners, err = securityOrgOwners(h, managedLabelPrefix+uniqueName(h, "unknown"))
		Expect(err).NotTo(HaveOccurred())
		Expect(owners).To(BeEmpty())
		missing, err := h.Client.GetDeviceLabelSyncProvenanceWithResponse(h.Context, uniqueName(h, "missing-device"))
		Expect(err).NotTo(HaveOccurred())
		Expect(missing.StatusCode()).To(Equal(http.StatusNotFound), string(missing.Body))
	})

	It("audits effective additions, changes and removals without a no-op event", func() {
		h := workerHarness
		name := uniqueName(h, "audit-map")
		deviceName := uniqueName(h, "audit-device")
		key := managedLabelPrefix + uniqueName(h, "audit")
		Expect(securityCreateDevice(h, deviceName, map[string]string{"operator": "kept"}, amd64Architecture, nil)).To(Succeed())
		Expect(securityWaitInitialArchitecturePromotion(h, deviceName)).To(Succeed())
		baseline, err := securityLabelEventCount(h, deviceName)
		Expect(err).NotTo(HaveOccurred())

		Expect(createLabelSyncMapping(h, name, &key, scopedScalarExpression(h, architectureField))).To(Succeed())
		Expect(waitForDeviceLabels(h, deviceName, map[string]string{key: amd64Architecture})).To(Succeed())
		Expect(waitForMappingReady(h, name)).To(Succeed())
		Expect(securityWaitForLabelEventCount(h, deviceName, baseline)).To(Succeed())
		added, err := securityLabelEventCount(h, deviceName)
		Expect(err).NotTo(HaveOccurred())

		Expect(replaceDeviceStatus(h, deviceName, arm64Architecture, linuxOperatingSystem, nil)).To(Succeed())
		Expect(waitForDeviceLabels(h, deviceName, map[string]string{key: arm64Architecture})).To(Succeed())
		Expect(securityWaitForLabelEventCount(h, deviceName, added)).To(Succeed())
		changed, err := securityLabelEventCount(h, deviceName)
		Expect(err).NotTo(HaveOccurred())

		By("replacing the expression with an equivalent one")
		get, err := h.Client.GetLabelSyncMappingWithResponse(h.Context, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(get.StatusCode()).To(Equal(http.StatusOK), string(get.Body))
		get.JSON200.Spec.Expression = scopedScalarExpression(h, `status.systemInfo.architecture == "amd64" ? "amd64" : status.systemInfo.architecture`)
		replaced, err := h.Client.ReplaceLabelSyncMappingWithResponse(h.Context, name, *get.JSON200)
		Expect(err).NotTo(HaveOccurred())
		Expect(replaced.StatusCode()).To(Equal(http.StatusOK), string(replaced.Body))
		Expect(replaced.JSON200).NotTo(BeNil())
		Expect(replaced.JSON200.Metadata.Generation).NotTo(BeNil())
		Expect(securityWaitReadyGeneration(h, name, *replaced.JSON200.Metadata.Generation)).To(Succeed())
		Expect(securityAssertLabelEventCountUnchanged(h, deviceName, changed, noOpAuditWindow)).To(Succeed())

		By("deleting the mapping and auditing the effective removal")
		Expect(deleteLabelSyncMapping(h, name)).To(Succeed())
		Expect(waitForDeviceLabels(h, deviceName, map[string]string{"operator": "kept"}, key)).To(Succeed())
		Expect(securityWaitForLabelEventCount(h, deviceName, changed)).To(Succeed())
	})

	It("does not audit a same-value ownership takeover as a label change", func() {
		h := workerHarness
		name := uniqueName(h, "takeover-map")
		deviceName := uniqueName(h, "takeover-device")
		key := managedLabelPrefix + uniqueName(h, "takeover")
		Expect(securityCreateDevice(h, deviceName, map[string]string{key: amd64Architecture}, amd64Architecture, nil)).To(Succeed())
		Expect(securityWaitInitialArchitecturePromotion(h, deviceName)).To(Succeed())
		baseline, err := securityLabelEventCount(h, deviceName)
		Expect(err).NotTo(HaveOccurred())

		Expect(createLabelSyncMapping(h, name, &key, scopedScalarExpression(h, architectureField))).To(Succeed())
		Expect(waitForMappingReady(h, name)).To(Succeed())
		Expect(securityWaitDeviceOwner(h, deviceName, key, name)).To(Succeed())
		Expect(waitForDeviceLabels(h, deviceName, map[string]string{key: amd64Architecture})).To(Succeed())
		Expect(securityAssertLabelEventCountUnchanged(h, deviceName, baseline, noOpAuditWindow)).To(Succeed())
	})

	It("ignores forged label and ownership fields in device status", func() {
		h := workerHarness
		name := uniqueName(h, "allowlist-map")
		deviceName := uniqueName(h, "spoof-device")
		ownedKey := managedLabelPrefix + uniqueName(h, "region")
		forgedKey := managedLabelPrefix + uniqueName(h, "forged")
		expression := fmt.Sprintf(`status.systemInfo.customInfo.transformMapEntry(k, v, k == "region", {"%s": v})`, ownedKey)
		Expect(createLabelSyncMapping(h, name, nil, scopedMapExpression(h, expression))).To(Succeed())
		Expect(securityCreateDevice(h, deviceName, map[string]string{forgedKey: "operator"}, amd64Architecture, map[string]string{
			"region": "east", forgedKey: "injected", api.DeviceAnnotationManagedLabels: `["` + forgedKey + `"]`,
		})).To(Succeed())
		Expect(waitForMappingReady(h, name)).To(Succeed())
		Expect(waitForDeviceLabels(h, deviceName, map[string]string{ownedKey: "east", forgedKey: "operator"})).To(Succeed())
		Expect(securityWaitDeviceOwner(h, deviceName, ownedKey, name)).To(Succeed())

		device, err := getDevice(h, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(device.Metadata.Annotations).NotTo(BeNil())
		managed := (*device.Metadata.Annotations)[api.DeviceAnnotationManagedLabels]
		var managedKeys []string
		Expect(json.Unmarshal([]byte(managed), &managedKeys)).To(Succeed())
		Expect(managedKeys).To(ContainElement(ownedKey))
		Expect(managedKeys).NotTo(ContainElement(forgedKey))

		By("rejecting a direct write to the map-owned key")
		Expect(rejectManagedLabelWrite(h, deviceName, ownedKey, "forged")).To(Succeed())

		By("discarding metadata supplied to the status endpoint")
		device, err = getDevice(h, deviceName)
		Expect(err).NotTo(HaveOccurred())
		labels := cloneLabels(device.Metadata.Labels)
		labels[ownedKey], labels[forgedKey] = "status-forged", "status-forged"
		device.Metadata.Labels = &labels
		annotations := map[string]string{}
		for key, value := range *device.Metadata.Annotations {
			annotations[key] = value
		}
		annotations[api.DeviceAnnotationManagedLabels] = `["` + forgedKey + `"]`
		device.Metadata.Annotations = &annotations
		status, err := h.Client.ReplaceDeviceStatusWithResponse(h.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.StatusCode()).To(Equal(http.StatusOK), string(status.Body))
		Expect(status.JSON200).NotTo(BeNil())
		Expect(status.JSON200.Metadata.Labels).NotTo(BeNil())
		Expect(*status.JSON200.Metadata.Labels).To(And(HaveKeyWithValue(ownedKey, "east"), HaveKeyWithValue(forgedKey, "operator")))
		Expect(status.JSON200.Metadata.Annotations).NotTo(BeNil())
		Expect((*status.JSON200.Metadata.Annotations)[api.DeviceAnnotationManagedLabels]).To(Equal(managed))
		Expect(securityWaitDeviceOwner(h, deviceName, ownedKey, name)).To(Succeed())
		owners, err := securityDeviceOwners(h, deviceName, forgedKey)
		Expect(err).NotTo(HaveOccurred())
		Expect(owners).To(BeEmpty())
	})
})

var _ = Describe("Label sync organization isolation", Serial, Label("integration", "sanity", "mapping-security"), func() {
	It("keeps mapping ownership and device provenance within each organization", func() {
		if infra.DetectEnvironment() != infra.EnvironmentOCP {
			Skip("organization isolation requires OpenShift namespace-based organization discovery")
		}
		h := workerHarness
		config, err := h.ReadClientConfig("")
		Expect(err).NotTo(HaveOccurred())
		originalOrg := config.Organization
		if originalOrg == "" {
			originalOrg, err = h.GetOrganizationID()
			Expect(err).NotTo(HaveOccurred())
			Expect(h.SetCurrentOrganization(originalOrg)).To(Succeed())
			DeferCleanup(restoreOrganizationPreference, h)
		}
		providers := setup.GetDefaultProviders()
		Expect(providers).NotTo(BeNil())
		Expect(providers.RBAC).NotTo(BeNil())
		username, err := h.GetCurrentUserPreferredUsername()
		Expect(err).NotTo(HaveOccurred())
		secondOrg := uniqueName(h, "provenance-org")
		originalOrgMapping, otherOrgMapping := uniqueName(h, "original-org-map"), uniqueName(h, "other-org-map")
		key := managedLabelPrefix + uniqueName(h, "org-key")
		originalOrgDevice, otherOrgDevice := uniqueName(h, "original-org-device"), uniqueName(h, "other-org-device")

		By("creating another organization and granting the test user access")
		Expect(createOrganizationIsolationNamespace(e2e.GetWorkerContext(), providers.RBAC, providers.Infra.GetExternalNamespace(), secondOrg)).To(Succeed())
		secondOrgID := ""
		DeferCleanup(cleanupOrganizationIsolation, h, providers.RBAC, secondOrg, &secondOrgID, originalOrg, originalOrgMapping, otherOrgMapping, otherOrgDevice)
		Expect(providers.RBAC.AddUserToOrg(e2e.GetWorkerContext(), secondOrg, username)).To(Succeed())
		// A new token picks up the project after the API cached the old identity.
		_, err = login.LoginToAPIWithToken(h)
		Expect(err).NotTo(HaveOccurred())
		Expect(h.SetCurrentOrganization(originalOrg)).To(Succeed())
		secondOrgID, err = securityWaitOrganizationID(h, secondOrg)
		Expect(err).NotTo(HaveOccurred())

		firstMappingSpec, err := newLabelSyncMapping(h, originalOrgMapping, &key, scopedScalarExpression(h, architectureField))
		Expect(err).NotTo(HaveOccurred())
		firstMapping, err := h.Client.CreateLabelSyncMappingWithResponse(h.Context, firstMappingSpec)
		Expect(err).NotTo(HaveOccurred())
		Expect(firstMapping.StatusCode()).To(Equal(http.StatusCreated), string(firstMapping.Body))
		Expect(securityCreateDevice(h, originalOrgDevice, nil, amd64Architecture, nil)).To(Succeed())
		Expect(waitForDeviceLabels(h, originalOrgDevice, map[string]string{key: amd64Architecture})).To(Succeed())
		Expect(securityWaitDeviceOwner(h, originalOrgDevice, key, originalOrgMapping)).To(Succeed())
		owners, err := securityOrgOwners(h, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(owners).To(ConsistOf(originalOrgMapping))

		By("creating the same key under a different mapping in the second organization")
		Expect(h.SetCurrentOrganization(secondOrgID)).To(Succeed())
		Expect(securityWaitMappingList(h)).To(Succeed())
		secondMappingSpec, err := newLabelSyncMapping(h, otherOrgMapping, &key, scopedScalarExpression(h, architectureField))
		Expect(err).NotTo(HaveOccurred())
		created, err := h.Client.CreateLabelSyncMappingWithResponse(h.Context, secondMappingSpec)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.StatusCode()).To(Equal(http.StatusCreated), string(created.Body))
		Expect(securityCreateDevice(h, otherOrgDevice, nil, arm64Architecture, nil)).To(Succeed())
		Expect(waitForDeviceLabels(h, otherOrgDevice, map[string]string{key: arm64Architecture})).To(Succeed())
		Expect(securityWaitDeviceOwner(h, otherOrgDevice, key, otherOrgMapping)).To(Succeed())
		owners, err = securityOrgOwners(h, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(owners).To(ConsistOf(otherOrgMapping))
		missing, err := h.Client.GetDeviceWithResponse(h.Context, originalOrgDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(missing.StatusCode()).To(Equal(http.StatusNotFound), string(missing.Body))
		Expect(rejectManagedLabelWrite(h, otherOrgDevice, key, "forged")).To(Succeed())
		Expect(deleteLabelSyncMapping(h, otherOrgMapping)).To(Succeed())
		Expect(securityWaitOrganizationOwnersEmpty(h, key)).To(Succeed())

		By("checking the original organization's ownership was not overwritten")
		Expect(h.SetCurrentOrganization(originalOrg)).To(Succeed())
		owners, err = securityOrgOwners(h, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(owners).To(ConsistOf(originalOrgMapping))
		Expect(securityWaitDeviceOwner(h, originalOrgDevice, key, originalOrgMapping)).To(Succeed())
		Expect(waitForDeviceLabels(h, originalOrgDevice, map[string]string{key: amd64Architecture})).To(Succeed())
	})
})

// securityCreateDevice creates a device with the operating-system status used by security cases.
func securityCreateDevice(harness *e2e.Harness, name string, labels map[string]string, architecture string, custom map[string]string) error {
	return createDeviceWithStatus(harness, name, labels, architecture, linuxOperatingSystem, custom)
}

// securityDeleteMapping deletes a mapping and treats an already-removed object as success.
func securityDeleteMapping(harness *e2e.Harness, name string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("mapping name is required")
	}
	GinkgoWriter.Printf("deleting security test mapping %q\n", name)
	response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, name)
	if err != nil {
		return fmt.Errorf("delete label sync mapping %q: %w", name, err)
	}
	if response == nil {
		return fmt.Errorf("delete label sync mapping %q returned no response", name)
	}
	if response.StatusCode() != http.StatusNotFound && response.StatusCode() != http.StatusOK {
		return fmt.Errorf("delete label sync mapping %q returned HTTP %d: %s", name, response.StatusCode(), response.Body)
	}
	return nil
}

// securityWaitReadyGeneration waits until the mapping observes the requested generation.
func securityWaitReadyGeneration(harness *e2e.Harness, name string, generation int64) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("mapping name is required")
	}
	if generation <= 0 {
		return fmt.Errorf("mapping generation must be positive, got %d", generation)
	}
	return retryUntil(fmt.Sprintf("mapping %q ready at generation %d", name, generation), testutil.DURATION_TIMEOUT, testutil.POLLING, func() error {
		response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, name)
		if err != nil {
			return fmt.Errorf("get mapping %q: %w", name, err)
		}
		if response == nil {
			return fmt.Errorf("get mapping %q returned no response", name)
		}
		if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
			return err
		}
		if response.JSON200 == nil || response.JSON200.Status == nil || response.JSON200.Status.Conditions == nil {
			return fmt.Errorf("mapping %q has no status conditions", name)
		}
		for _, condition := range *response.JSON200.Status.Conditions {
			if condition.Type == api.ConditionTypeLabelSyncMappingReady && condition.Status == api.ConditionStatusTrue &&
				condition.ObservedGeneration != nil && *condition.ObservedGeneration >= generation {
				return nil
			}
		}
		return fmt.Errorf("mapping %q is not Ready at generation %d", name, generation)
	})
}

// securityWaitInitialArchitecturePromotion waits for the built-in architecture mapping when installed.
func securityWaitInitialArchitecturePromotion(harness *e2e.Harness, deviceName string) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	if deviceName == "" {
		return fmt.Errorf("device name is required")
	}
	response, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, initialArchitectureMapping)
	if err != nil {
		return fmt.Errorf("get initial architecture mapping: %w", err)
	}
	if response == nil {
		return fmt.Errorf("get initial architecture mapping returned no response")
	}
	if response.StatusCode() == http.StatusNotFound {
		return nil
	}
	if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
		return err
	}
	if err := waitForDeviceLabels(harness, deviceName, map[string]string{initialArchitectureKey: amd64Architecture}); err != nil {
		return err
	}
	return securityWaitForLabelEventCount(harness, deviceName, 0)
}

// securityOrgOwners returns owners for a key and an empty slice when the key has no mapping.
func securityOrgOwners(harness *e2e.Harness, key string) ([]string, error) {
	if err := validateHarness(harness); err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("provenance label key is required")
	}
	response, err := harness.Client.GetLabelSyncProvenanceWithResponse(harness.Context, &api.GetLabelSyncProvenanceParams{LabelKeys: []string{key}})
	if err != nil {
		return nil, fmt.Errorf("get organization provenance for %q: %w", key, err)
	}
	if response == nil {
		return nil, fmt.Errorf("organization provenance for %q returned no response", key)
	}
	if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
		return nil, err
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("organization provenance for %q returned an empty 200 response", key)
	}
	for _, item := range response.JSON200.Items {
		if item.Key == key {
			return item.Owners, nil
		}
	}
	return []string{}, nil
}

// securityDeviceOwners returns the mapping owners for a device label, or an empty slice.
func securityDeviceOwners(harness *e2e.Harness, device, key string) ([]string, error) {
	if err := validateHarness(harness); err != nil {
		return nil, err
	}
	if device == "" || key == "" {
		return nil, fmt.Errorf("device name and provenance label key are required")
	}
	response, err := harness.Client.GetDeviceLabelSyncProvenanceWithResponse(harness.Context, device)
	if err != nil {
		return nil, fmt.Errorf("get provenance for device %q: %w", device, err)
	}
	if response == nil {
		return nil, fmt.Errorf("device provenance for %q returned no response", device)
	}
	if err := requireStatus(response.StatusCode(), response.Body, http.StatusOK); err != nil {
		return nil, err
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("device provenance for %q returned an empty 200 response", device)
	}
	for _, item := range response.JSON200.Items {
		if item.Key == key {
			return item.Owners, nil
		}
	}
	return []string{}, nil
}

// securityWaitDeviceOwner waits until a device's label provenance names the expected mapping.
func securityWaitDeviceOwner(harness *e2e.Harness, device, key, mapping string) error {
	return retryUntil(fmt.Sprintf("mapping %q to own %q on device %q", mapping, key, device), testutil.DURATION_TIMEOUT, testutil.POLLING, func() error {
		owners, err := securityDeviceOwners(harness, device, key)
		if err != nil {
			return err
		}
		if len(owners) != 1 || owners[0] != mapping {
			return fmt.Errorf("device %q label %q owners = %v, want [%s]", device, key, owners, mapping)
		}
		return nil
	})
}

// securityLabelEventCount counts device update events that report label changes.
func securityLabelEventCount(harness *e2e.Harness, name string) (int, error) {
	if err := validateHarness(harness); err != nil {
		return 0, err
	}
	if name == "" {
		return 0, fmt.Errorf("device name is required")
	}
	reason := api.EventReasonResourceUpdated
	events, err := harness.GetDeviceEvents(name, &reason)
	if err != nil {
		return 0, fmt.Errorf("get label update events for device %q: %w", name, err)
	}
	count := 0
	for _, event := range events {
		if event.Details == nil {
			continue
		}
		details, err := event.Details.AsResourceUpdatedDetails()
		if err != nil {
			return 0, fmt.Errorf("decode resource update event for device %q: %w", name, err)
		}
		for _, field := range details.UpdatedFields {
			if field == api.Labels {
				count++
				break
			}
		}
	}
	return count, nil
}

// securityWaitForLabelEventCount waits until the device has more label events than the baseline.
func securityWaitForLabelEventCount(harness *e2e.Harness, name string, baseline int) error {
	return retryUntil(fmt.Sprintf("label event count on device %q to exceed %d", name, baseline), testutil.DURATION_TIMEOUT, testutil.POLLING, func() error {
		count, err := securityLabelEventCount(harness, name)
		if err != nil {
			return err
		}
		if count <= baseline {
			return fmt.Errorf("device %q has %d label events, baseline is %d", name, count, baseline)
		}
		return nil
	})
}

// securityAssertLabelEventCountUnchanged checks that a label event count stays fixed for a window.
func securityAssertLabelEventCountUnchanged(harness *e2e.Harness, name string, expected int, window time.Duration) error {
	if window <= 0 {
		return fmt.Errorf("no-op audit window must be positive, got %s", window)
	}
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		count, err := securityLabelEventCount(harness, name)
		if err != nil {
			return err
		}
		if count != expected {
			return fmt.Errorf("device %q label event count changed from %d to %d", name, expected, count)
		}
		time.Sleep(testutil.POLLING)
	}
	return nil
}

// securityWaitOrganizationID polls for the new organization's FlightCtl ID.
func securityWaitOrganizationID(harness *e2e.Harness, namespace string) (string, error) {
	if err := validateHarness(harness); err != nil {
		return "", err
	}
	if namespace == "" {
		return "", fmt.Errorf("organization namespace is required")
	}
	var organizationID string
	err := retryUntil(fmt.Sprintf("organization ID for namespace %q", namespace), testutil.DURATION_TIMEOUT, testutil.POLLING, func() error {
		id, err := harness.GetOrganizationIDForNamespace(namespace)
		if err != nil {
			return fmt.Errorf("get organization ID for %q: %w", namespace, err)
		}
		if id == "" {
			return fmt.Errorf("organization %q has no ID yet", namespace)
		}
		organizationID = id
		return nil
	})
	return organizationID, err
}

// securityWaitMappingList polls until the mapping API is available in the selected organization.
func securityWaitMappingList(harness *e2e.Harness) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	return retryUntil("label sync mapping list", testutil.DURATION_TIMEOUT, testutil.POLLING, func() error {
		response, err := harness.Client.ListLabelSyncMappingsWithResponse(harness.Context, &api.ListLabelSyncMappingsParams{})
		if err != nil {
			return fmt.Errorf("list label sync mappings: %w", err)
		}
		if response == nil {
			return fmt.Errorf("list label sync mappings returned no response")
		}
		return requireStatus(response.StatusCode(), response.Body, http.StatusOK)
	})
}

// securityWaitOrganizationOwnersEmpty waits until organization provenance has no owners for a key.
func securityWaitOrganizationOwnersEmpty(harness *e2e.Harness, key string) error {
	return retryUntil(fmt.Sprintf("organization ownership release for %q", key), testutil.DURATION_TIMEOUT, testutil.POLLING, func() error {
		owners, err := securityOrgOwners(harness, key)
		if err != nil {
			return err
		}
		if len(owners) != 0 {
			return fmt.Errorf("organization provenance for %q still has owners %v", key, owners)
		}
		return nil
	})
}

// restoreOrganizationPreference resets the client config to use the default organization.
func restoreOrganizationPreference(harness *e2e.Harness) error {
	if err := validateHarness(harness); err != nil {
		return err
	}
	path, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		return fmt.Errorf("get FlightCtl client config path: %w", err)
	}
	config, err := harness.ReadClientConfig("")
	if err != nil {
		return fmt.Errorf("read FlightCtl client config: %w", err)
	}
	config.Organization = ""
	if err := config.Persist(path); err != nil {
		return fmt.Errorf("persist default organization preference: %w", err)
	}
	return harness.RefreshClient()
}

// createOrganizationIsolationNamespace creates an organization with the Helm release label the API discovers.
func createOrganizationIsolationNamespace(ctx context.Context, rbac infra.RBACProvider, apiNamespace, name string) error {
	clientProvider, ok := rbac.(interface{ GetClient() kubernetes.Interface })
	if !ok {
		return fmt.Errorf("RBAC provider does not expose a Kubernetes client")
	}
	client := clientProvider.GetClient()
	deployment, err := client.AppsV1().Deployments(apiNamespace).Get(ctx, "flightctl-api", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get API deployment in namespace %s: %w", apiNamespace, err)
	}
	releaseName := deployment.Labels["app.kubernetes.io/instance"]
	if releaseName == "" {
		return fmt.Errorf("API deployment in namespace %s has no Helm release label", apiNamespace)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{infra.OrgLabelKey: releaseName},
	}}
	if _, err := client.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create organization namespace %s: %w", name, err)
	}
	return nil
}

// cleanupOrganizationIsolation removes test mappings, the temporary device, and the temporary organization.
func cleanupOrganizationIsolation(harness *e2e.Harness, rbac infra.RBACProvider, secondOrg string, secondOrgID *string, originalOrg, originalOrgMapping, otherOrgMapping, otherOrgDevice string) error {
	if harness == nil || rbac == nil {
		return fmt.Errorf("organization cleanup requires a harness and RBAC provider")
	}
	if secondOrg == "" || originalOrg == "" || originalOrgMapping == "" || otherOrgMapping == "" || otherOrgDevice == "" {
		return fmt.Errorf("organization cleanup requires both organizations and all resource names")
	}
	var cleanupErrors []string
	record := func(action string, err error) {
		if err != nil {
			GinkgoWriter.Printf("%s failed during organization cleanup: %v\n", action, err)
			cleanupErrors = append(cleanupErrors, fmt.Sprintf("%s: %v", action, err))
		}
	}
	if secondOrgID != nil && *secondOrgID != "" {
		record("select temporary organization", harness.SetCurrentOrganization(*secondOrgID))
		record("delete temporary mapping", securityDeleteMapping(harness, otherOrgMapping))
		record("delete temporary device", harness.DeleteDeviceIgnoreNotFound(otherOrgDevice))
	}
	record("restore original organization", harness.SetCurrentOrganization(originalOrg))
	record("delete original mapping", securityDeleteMapping(harness, originalOrgMapping))
	record("delete temporary organization", rbac.DeleteOrganization(e2e.GetWorkerContext(), secondOrg))
	if len(cleanupErrors) > 0 {
		return fmt.Errorf("organization cleanup errors: %s", strings.Join(cleanupErrors, "; "))
	}
	return nil
}
