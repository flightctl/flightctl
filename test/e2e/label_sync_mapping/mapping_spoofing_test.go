package label_sync_mapping

import (
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Label sync mapping spoofing resistance", Serial, Label("integration", "sanity", "mapping-spoofing"), func() {
	It("promotes only keys selected by the server-side mapping", func() {
		harness := e2e.GetWorkerHarness()
		suffix := uniqueName(harness, "spoof")
		mappingName := uniqueName(harness, "allowlisted-map")
		deviceName := uniqueName(harness, "spoofed-device")
		regionKey := fmt.Sprintf("e2e.flightctl.io/region-%s", suffix)
		forgedKey := fmt.Sprintf("e2e.flightctl.io/forged-%s", suffix)

		By("creating a map that promotes only the approved custom-info key")
		createLabelSyncMapping(
			harness,
			mappingName,
			nil,
			scopedMapExpression(harness, fmt.Sprintf(
				`status.systemInfo.customInfo.transformMapEntry(k, v, k == "region", {"%s": v})`,
				regionKey,
			)),
		)

		By("reporting an approved value alongside forged label and ownership-shaped keys")
		customInfo := map[string]string{
			"region":                          "east",
			forgedKey:                         "spoofed-value",
			api.DeviceAnnotationManagedLabels: "[\"" + forgedKey + "\"]",
		}
		createDeviceWithStatus(
			harness,
			deviceName,
			map[string]string{"operator": "preserved", forgedKey: "operator-value"},
			"amd64",
			"linux",
			customInfo,
		)
		waitForDeviceLabels(harness, deviceName, map[string]string{
			"operator": "preserved",
			regionKey:  "east",
			forgedKey:  "operator-value",
		})
		waitForMappingReady(harness, mappingName)

		By("verifying ownership follows only the mapping output")
		provenance, err := harness.Client.GetDeviceLabelSyncProvenanceWithResponse(harness.Context, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(provenance.StatusCode()).To(Equal(http.StatusOK), string(provenance.Body))
		Expect(provenance.JSON200).NotTo(BeNil())
		Expect(ownersForKey(provenance.JSON200.Items, regionKey)).To(ConsistOf(mappingName))
		Expect(ownersForKey(provenance.JSON200.Items, forgedKey)).To(BeEmpty())

		device := getDevice(harness, deviceName)
		Expect(device.Metadata.Annotations).NotTo(BeNil())
		managedLabels := (*device.Metadata.Annotations)[api.DeviceAnnotationManagedLabels]
		Expect(managedLabels).NotTo(BeEmpty())
		var managedKeys []string
		Expect(json.Unmarshal([]byte(managedLabels), &managedKeys)).To(Succeed())
		Expect(managedKeys).To(ContainElement(regionKey))
		Expect(managedKeys).NotTo(ContainElement(forgedKey))

		By("protecting the map output while allowing the unrelated operator key")
		changedLabels := cloneLabels(device.Metadata.Labels)
		changedLabels[regionKey] = "forged"
		device.Metadata.Labels = &changedLabels
		blockedWrite, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(blockedWrite.StatusCode()).To(Equal(http.StatusConflict), string(blockedWrite.Body))

		device = getDevice(harness, deviceName)
		allowedLabels := cloneLabels(device.Metadata.Labels)
		allowedLabels[forgedKey] = "operator-updated"
		device.Metadata.Labels = &allowedLabels
		allowedWrite, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(allowedWrite.StatusCode()).To(Equal(http.StatusOK), string(allowedWrite.Body))
		waitForDeviceLabels(harness, deviceName, map[string]string{regionKey: "east", forgedKey: "operator-updated"})

		By("ignoring labels and ownership metadata supplied to the status endpoint")
		device = getDevice(harness, deviceName)
		spoofedLabels := cloneLabels(device.Metadata.Labels)
		spoofedLabels[regionKey] = "status-forged"
		spoofedLabels[forgedKey] = "status-forged"
		device.Metadata.Labels = &spoofedLabels
		spoofedAnnotations := map[string]string{}
		for key, value := range *device.Metadata.Annotations {
			spoofedAnnotations[key] = value
		}
		spoofedAnnotations[api.DeviceAnnotationManagedLabels] = `["` + forgedKey + `"]`
		device.Metadata.Annotations = &spoofedAnnotations
		statusWrite, err := harness.Client.ReplaceDeviceStatusWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(statusWrite.StatusCode()).To(Equal(http.StatusOK), string(statusWrite.Body))
		Expect(statusWrite.JSON200).NotTo(BeNil())
		Expect(statusWrite.JSON200.Metadata.Labels).NotTo(BeNil())
		Expect(*statusWrite.JSON200.Metadata.Labels).To(HaveKeyWithValue(regionKey, "east"))
		Expect(*statusWrite.JSON200.Metadata.Labels).To(HaveKeyWithValue(forgedKey, "operator-updated"))
		Expect(statusWrite.JSON200.Metadata.Annotations).NotTo(BeNil())
		Expect((*statusWrite.JSON200.Metadata.Annotations)[api.DeviceAnnotationManagedLabels]).To(Equal(managedLabels))

		device = waitForDeviceLabels(harness, deviceName, map[string]string{
			regionKey: "east", forgedKey: "operator-updated",
		})
		Expect(device.Metadata.Annotations).NotTo(BeNil())
		Expect((*device.Metadata.Annotations)[api.DeviceAnnotationManagedLabels]).To(Equal(managedLabels))
		waitForDeviceProvenance(harness, deviceName, regionKey, mappingName)
		provenance, err = harness.Client.GetDeviceLabelSyncProvenanceWithResponse(harness.Context, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(provenance.StatusCode()).To(Equal(http.StatusOK), string(provenance.Body))
		Expect(ownersForKey(provenance.JSON200.Items, forgedKey)).To(BeEmpty())
	})
})
