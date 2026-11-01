package label_sync_mapping

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

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
