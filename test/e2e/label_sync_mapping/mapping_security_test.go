package label_sync_mapping

import (
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Label sync security and audit", Serial, Label("integration", "sanity", "mapping-security"), func() {
	It("reserves a scalar key even when the mapping currently emits no value", func() {
		h := e2e.GetWorkerHarness()
		key := "e2e.flightctl.io/" + uniqueName(h, "empty")
		name := uniqueName(h, "empty-map")
		deviceName := uniqueName(h, "empty-device")
		createLabelSyncMapping(h, name, &key, scopedScalarExpression(h, "dyn(null)"))
		securityCreateDevice(h, deviceName, nil, "amd64", nil)
		waitForMappingReady(h, name)
		waitForDeviceLabels(h, deviceName, nil, key)

		By("rejecting an operator write and another mapping for the reserved key")
		device := getDevice(h, deviceName)
		labels := cloneLabels(device.Metadata.Labels)
		labels[key] = "operator"
		device.Metadata.Labels = &labels
		write, err := h.Client.ReplaceDeviceWithResponse(h.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(write.StatusCode()).To(Equal(http.StatusConflict), string(write.Body))

		duplicate := newLabelSyncMapping(h, uniqueName(h, "duplicate"), &key, scopedScalarExpression(h, "status.systemInfo.architecture"))
		created, err := h.Client.CreateLabelSyncMappingWithResponse(h.Context, duplicate)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.StatusCode()).To(Equal(http.StatusConflict), string(created.Body))

		By("reporting the reservation in organization provenance while device provenance stays empty")
		Expect(securityOrgOwners(h, key)).To(ConsistOf(name))
		provenance, err := h.Client.GetDeviceLabelSyncProvenanceWithResponse(h.Context, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(provenance.StatusCode()).To(Equal(http.StatusOK), string(provenance.Body))
		Expect(provenance.JSON200).NotTo(BeNil())
		Expect(provenance.JSON200.Items).To(BeEmpty())
		Expect(securityOrgOwners(h, "e2e.flightctl.io/"+uniqueName(h, "unknown"))).To(BeEmpty())
		missing, err := h.Client.GetDeviceLabelSyncProvenanceWithResponse(h.Context, uniqueName(h, "missing-device"))
		Expect(err).NotTo(HaveOccurred())
		Expect(missing.StatusCode()).To(Equal(http.StatusNotFound), string(missing.Body))
	})

	It("audits effective additions, changes and removals without a no-op event", func() {
		h := e2e.GetWorkerHarness()
		name := uniqueName(h, "audit-map")
		deviceName := uniqueName(h, "audit-device")
		key := "e2e.flightctl.io/" + uniqueName(h, "audit")
		securityCreateDevice(h, deviceName, map[string]string{"operator": "kept"}, "amd64", nil)
		securityWaitSynced(h, deviceName)
		baseline := securityLabelEventCount(h, deviceName)

		createLabelSyncMapping(h, name, &key, scopedScalarExpression(h, "status.systemInfo.architecture"))
		waitForDeviceLabels(h, deviceName, map[string]string{key: "amd64"})
		waitForMappingReady(h, name)
		Eventually(func() int { return securityLabelEventCount(h, deviceName) }).WithTimeout(testutil.DURATION_TIMEOUT).Should(BeNumerically(">", baseline))
		added := securityLabelEventCount(h, deviceName)

		securitySetStatus(h, deviceName, "arm64", nil)
		waitForDeviceLabels(h, deviceName, map[string]string{key: "arm64"})
		Eventually(func() int { return securityLabelEventCount(h, deviceName) }).WithTimeout(testutil.DURATION_TIMEOUT).Should(BeNumerically(">", added))
		changed := securityLabelEventCount(h, deviceName)

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
		securityWaitReadyGeneration(h, name, *replaced.JSON200.Metadata.Generation)
		Consistently(func() int { return securityLabelEventCount(h, deviceName) }).WithTimeout(5 * testutil.POLLING).WithPolling(testutil.POLLING).Should(Equal(changed))

		By("deleting the mapping and auditing the effective removal")
		deleteLabelSyncMapping(h, name)
		waitForDeviceLabels(h, deviceName, map[string]string{"operator": "kept"}, key)
		Eventually(func() int { return securityLabelEventCount(h, deviceName) }).WithTimeout(testutil.DURATION_TIMEOUT).Should(BeNumerically(">", changed))
	})

	It("does not audit a same-value ownership takeover as a label change", func() {
		h := e2e.GetWorkerHarness()
		name := uniqueName(h, "takeover-map")
		deviceName := uniqueName(h, "takeover-device")
		key := "e2e.flightctl.io/" + uniqueName(h, "takeover")
		securityCreateDevice(h, deviceName, map[string]string{key: "amd64"}, "amd64", nil)
		securityWaitSynced(h, deviceName)
		baseline := securityLabelEventCount(h, deviceName)

		createLabelSyncMapping(h, name, &key, scopedScalarExpression(h, "status.systemInfo.architecture"))
		waitForMappingReady(h, name)
		securityWaitDeviceOwner(h, deviceName, key, name)
		waitForDeviceLabels(h, deviceName, map[string]string{key: "amd64"})
		Consistently(func() int { return securityLabelEventCount(h, deviceName) }).WithTimeout(5 * testutil.POLLING).WithPolling(testutil.POLLING).Should(Equal(baseline))
	})

	It("ignores forged label and ownership fields in device status", func() {
		h := e2e.GetWorkerHarness()
		name := uniqueName(h, "allowlist-map")
		deviceName := uniqueName(h, "spoof-device")
		ownedKey := "e2e.flightctl.io/" + uniqueName(h, "region")
		forgedKey := "e2e.flightctl.io/" + uniqueName(h, "forged")
		expression := fmt.Sprintf(`status.systemInfo.customInfo.transformMapEntry(k, v, k == "region", {"%s": v})`, ownedKey)
		createLabelSyncMapping(h, name, nil, scopedMapExpression(h, expression))
		securityCreateDevice(h, deviceName, map[string]string{forgedKey: "operator"}, "amd64", map[string]string{
			"region": "east", forgedKey: "injected", api.DeviceAnnotationManagedLabels: `["` + forgedKey + `"]`,
		})
		waitForMappingReady(h, name)
		waitForDeviceLabels(h, deviceName, map[string]string{ownedKey: "east", forgedKey: "operator"})
		securityWaitDeviceOwner(h, deviceName, ownedKey, name)
		Expect(securityDeviceOwners(h, deviceName, forgedKey)).To(BeEmpty())

		device := getDevice(h, deviceName)
		Expect(device.Metadata.Annotations).NotTo(BeNil())
		managed := (*device.Metadata.Annotations)[api.DeviceAnnotationManagedLabels]
		var managedKeys []string
		Expect(json.Unmarshal([]byte(managed), &managedKeys)).To(Succeed())
		Expect(managedKeys).To(ContainElement(ownedKey))
		Expect(managedKeys).NotTo(ContainElement(forgedKey))

		By("blocking a direct write to the promoted key while allowing the unrelated key")
		unchanged := cloneLabels(device.Metadata.Labels)
		device.Metadata.Labels = &unchanged
		unchangedWrite, err := h.Client.ReplaceDeviceWithResponse(h.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(unchangedWrite.StatusCode()).To(Equal(http.StatusOK), string(unchangedWrite.Body))
		device = getDevice(h, deviceName)
		labels := cloneLabels(device.Metadata.Labels)
		labels[ownedKey] = "forged"
		device.Metadata.Labels = &labels
		blocked, err := h.Client.ReplaceDeviceWithResponse(h.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(blocked.StatusCode()).To(Equal(http.StatusConflict), string(blocked.Body))
		device = getDevice(h, deviceName)
		labels = cloneLabels(device.Metadata.Labels)
		labels[forgedKey] = "operator-updated"
		device.Metadata.Labels = &labels
		allowed, err := h.Client.ReplaceDeviceWithResponse(h.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(allowed.StatusCode()).To(Equal(http.StatusOK), string(allowed.Body))

		By("discarding metadata supplied to the status endpoint")
		device = getDevice(h, deviceName)
		labels = cloneLabels(device.Metadata.Labels)
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
		Expect(*status.JSON200.Metadata.Labels).To(And(HaveKeyWithValue(ownedKey, "east"), HaveKeyWithValue(forgedKey, "operator-updated")))
		Expect(status.JSON200.Metadata.Annotations).NotTo(BeNil())
		Expect((*status.JSON200.Metadata.Annotations)[api.DeviceAnnotationManagedLabels]).To(Equal(managed))
		securityWaitDeviceOwner(h, deviceName, ownedKey, name)
		Expect(securityDeviceOwners(h, deviceName, forgedKey)).To(BeEmpty())
	})
})

var _ = Describe("Label sync organization isolation", Serial, Label("integration", "sanity", "mapping-security"), func() {
	It("keeps mapping ownership and device provenance within each organization", func() {
		h := e2e.GetWorkerHarness()
		config, err := h.ReadClientConfig("")
		Expect(err).NotTo(HaveOccurred())
		originalOrg := config.Organization
		if originalOrg == "" {
			originalOrg, err = h.GetOrganizationID()
			Expect(err).NotTo(HaveOccurred())
			Expect(h.SetCurrentOrganization(originalOrg)).To(Succeed())
			DeferCleanup(func() {
				path, err := client.DefaultFlightctlClientConfigPath()
				Expect(err).NotTo(HaveOccurred())
				config, err := h.ReadClientConfig("")
				Expect(err).NotTo(HaveOccurred())
				config.Organization = ""
				Expect(config.Persist(path)).To(Succeed())
				Expect(h.RefreshClient()).To(Succeed())
			})
		}
		providers := setup.GetDefaultProviders()
		Expect(providers).NotTo(BeNil())
		Expect(providers.RBAC).NotTo(BeNil())
		username, err := h.GetCurrentUserPreferredUsername()
		Expect(err).NotTo(HaveOccurred())
		secondOrg := uniqueName(h, "provenance-org")
		firstName, secondName := uniqueName(h, "first-map"), uniqueName(h, "second-map")
		key := "e2e.flightctl.io/" + uniqueName(h, "org-key")
		firstDevice, secondDevice := uniqueName(h, "first-device"), uniqueName(h, "second-device")

		By("creating another organization and granting the test user access")
		Expect(providers.RBAC.CreateOrganization(e2e.GetWorkerContext(), secondOrg)).To(Succeed())
		secondOrgID := ""
		DeferCleanup(func() {
			if secondOrgID != "" {
				Expect(h.SetCurrentOrganization(secondOrgID)).To(Succeed())
				securityDeleteMapping(h, secondName)
				Expect(h.DeleteDeviceIgnoreNotFound(secondDevice)).To(Succeed())
			}
			Expect(h.SetCurrentOrganization(originalOrg)).To(Succeed())
			securityDeleteMapping(h, firstName)
			Expect(providers.RBAC.DeleteOrganization(e2e.GetWorkerContext(), secondOrg)).To(Succeed())
		})
		Expect(providers.RBAC.AddUserToOrg(e2e.GetWorkerContext(), secondOrg, username)).To(Succeed())
		Eventually(func(g Gomega) {
			id, err := h.GetOrganizationIDForNamespace(secondOrg)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(id).NotTo(BeEmpty())
			secondOrgID = id
		}).WithTimeout(testutil.DURATION_TIMEOUT).Should(Succeed())

		firstMapping, err := h.Client.CreateLabelSyncMappingWithResponse(h.Context, newLabelSyncMapping(h, firstName, &key, scopedScalarExpression(h, "status.systemInfo.architecture")))
		Expect(err).NotTo(HaveOccurred())
		Expect(firstMapping.StatusCode()).To(Equal(http.StatusCreated), string(firstMapping.Body))
		securityCreateDevice(h, firstDevice, nil, "amd64", nil)
		waitForDeviceLabels(h, firstDevice, map[string]string{key: "amd64"})
		securityWaitDeviceOwner(h, firstDevice, key, firstName)
		Expect(securityOrgOwners(h, key)).To(ConsistOf(firstName))

		By("creating the same key under a different mapping in the second organization")
		Expect(h.SetCurrentOrganization(secondOrgID)).To(Succeed())
		Eventually(func(g Gomega) {
			response, err := h.Client.ListLabelSyncMappingsWithResponse(h.Context, &api.ListLabelSyncMappingsParams{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		}).WithTimeout(testutil.DURATION_TIMEOUT).Should(Succeed())
		created, err := h.Client.CreateLabelSyncMappingWithResponse(h.Context, newLabelSyncMapping(h, secondName, &key, scopedScalarExpression(h, "status.systemInfo.architecture")))
		Expect(err).NotTo(HaveOccurred())
		Expect(created.StatusCode()).To(Equal(http.StatusCreated), string(created.Body))
		securityCreateDevice(h, secondDevice, nil, "arm64", nil)
		waitForDeviceLabels(h, secondDevice, map[string]string{key: "arm64"})
		securityWaitDeviceOwner(h, secondDevice, key, secondName)
		Expect(securityOrgOwners(h, key)).To(ConsistOf(secondName))
		missing, err := h.Client.GetDeviceWithResponse(h.Context, firstDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(missing.StatusCode()).To(Equal(http.StatusNotFound), string(missing.Body))
		second := getDevice(h, secondDevice)
		labels := cloneLabels(second.Metadata.Labels)
		labels[key] = "forged"
		second.Metadata.Labels = &labels
		blocked, err := h.Client.ReplaceDeviceWithResponse(h.Context, secondDevice, *second)
		Expect(err).NotTo(HaveOccurred())
		Expect(blocked.StatusCode()).To(Equal(http.StatusConflict), string(blocked.Body))
		deleteLabelSyncMapping(h, secondName)
		Eventually(func(g Gomega) { g.Expect(securityOrgOwners(h, key)).To(BeEmpty()) }).WithTimeout(testutil.DURATION_TIMEOUT).Should(Succeed())

		By("checking the original organization's ownership was not overwritten")
		Expect(h.SetCurrentOrganization(originalOrg)).To(Succeed())
		Expect(securityOrgOwners(h, key)).To(ConsistOf(firstName))
		securityWaitDeviceOwner(h, firstDevice, key, firstName)
		waitForDeviceLabels(h, firstDevice, map[string]string{key: "amd64"})
	})
})

func securityCreateDevice(h *e2e.Harness, name string, labels map[string]string, arch string, custom map[string]string) {
	createDeviceWithStatus(h, name, labels, arch, "linux", custom)
}

func securitySetStatus(h *e2e.Harness, name, arch string, custom map[string]string) {
	replaceDeviceStatus(h, name, arch, "linux", custom)
}

func securityDeleteMapping(h *e2e.Harness, name string) {
	response, err := h.Client.DeleteLabelSyncMappingWithResponse(h.Context, name)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(BeElementOf(http.StatusOK, http.StatusNotFound), string(response.Body))
}

func securityWaitReadyGeneration(h *e2e.Harness, name string, generation int64) {
	Eventually(func(g Gomega) {
		response, err := h.Client.GetLabelSyncMappingWithResponse(h.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		g.Expect(response.JSON200.Status).NotTo(BeNil())
		g.Expect(response.JSON200.Status.Conditions).NotTo(BeNil())
		ready := false
		for _, condition := range *response.JSON200.Status.Conditions {
			if condition.Type == api.ConditionTypeLabelSyncMappingReady && condition.Status == api.ConditionStatusTrue &&
				condition.ObservedGeneration != nil && *condition.ObservedGeneration >= generation {
				ready = true
			}
		}
		g.Expect(ready).To(BeTrue())
	}).WithTimeout(testutil.DURATION_TIMEOUT).WithPolling(testutil.POLLING).Should(Succeed())
}

func securityWaitSynced(h *e2e.Harness, name string) {
	Eventually(func(g Gomega) {
		response, err := h.Client.GetDeviceWithResponse(h.Context, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		device := response.JSON200
		g.Expect(device.Status).NotTo(BeNil())
		synced := false
		for _, condition := range device.Status.Conditions {
			if condition.Type == api.ConditionTypeDeviceLabelsSynced && condition.Status == api.ConditionStatusTrue && condition.Reason == "Reconciled" {
				synced = true
			}
		}
		g.Expect(synced).To(BeTrue())
	}).WithTimeout(testutil.DURATION_TIMEOUT).WithPolling(testutil.POLLING).Should(Succeed())
}

func securityOrgOwners(h *e2e.Harness, key string) []string {
	response, err := h.Client.GetLabelSyncProvenanceWithResponse(h.Context, &api.GetLabelSyncProvenanceParams{LabelKeys: []string{key}})
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
	Expect(response.JSON200).NotTo(BeNil())
	Expect(response.JSON200.Items).To(HaveLen(1))
	Expect(response.JSON200.Items[0].Key).To(Equal(key))
	return response.JSON200.Items[0].Owners
}

func securityDeviceOwners(h *e2e.Harness, device, key string) []string {
	response, err := h.Client.GetDeviceLabelSyncProvenanceWithResponse(h.Context, device)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
	Expect(response.JSON200).NotTo(BeNil())
	for _, item := range response.JSON200.Items {
		if item.Key == key {
			return item.Owners
		}
	}
	return nil
}

func securityWaitDeviceOwner(h *e2e.Harness, device, key, mapping string) {
	Eventually(func(g Gomega) {
		response, err := h.Client.GetDeviceLabelSyncProvenanceWithResponse(h.Context, device)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		g.Expect(response.JSON200).NotTo(BeNil())
		var owners []string
		for _, item := range response.JSON200.Items {
			if item.Key == key {
				owners = item.Owners
			}
		}
		g.Expect(owners).To(ConsistOf(mapping))
	}).WithTimeout(testutil.DURATION_TIMEOUT).WithPolling(testutil.POLLING).Should(Succeed())
}

func securityLabelEventCount(h *e2e.Harness, name string) int {
	reason := api.EventReasonResourceUpdated
	events, err := h.GetDeviceEvents(name, &reason)
	Expect(err).NotTo(HaveOccurred())
	count := 0
	for _, event := range events {
		if event.Details == nil {
			continue
		}
		details, err := event.Details.AsResourceUpdatedDetails()
		if err != nil {
			continue
		}
		for _, field := range details.UpdatedFields {
			if field == api.Labels {
				count++
				break
			}
		}
	}
	return count
}
