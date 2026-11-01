package label_sync_mapping

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Label sync mapping API lifecycle", Label("integration", "mapping-api"), func() {
	It("supports CRUD and patch while rejecting an invalid expression shape", func() {
		harness := e2e.GetWorkerHarness()
		mappingName := uniqueName(harness, "api-lifecycle")
		mappingKey := "e2e.flightctl.io/" + mappingName

		created := createLabelSyncMapping(harness, mappingName, &mappingKey, "status.systemInfo.architecture")
		Expect(created.Metadata.Name).NotTo(BeNil())
		Expect(*created.Metadata.Name).To(Equal(mappingName))
		Expect(created.Metadata.Generation).NotTo(BeNil())
		createdGeneration := *created.Metadata.Generation

		getResponse, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, mappingName)
		Expect(err).NotTo(HaveOccurred())
		Expect(getResponse.StatusCode()).To(Equal(http.StatusOK), string(getResponse.Body))
		Expect(getResponse.JSON200).NotTo(BeNil())
		Expect(getResponse.JSON200.Spec.Expression).To(Equal("status.systemInfo.architecture"))

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

		patch := api.PatchRequest{{
			Op:    api.PatchRequestOp("replace"),
			Path:  "/spec/expression",
			Value: "status.systemInfo.operatingSystem",
		}}
		patchResponse, err := harness.Client.PatchLabelSyncMappingWithApplicationJSONPatchPlusJSONBodyWithResponse(harness.Context, mappingName, patch)
		Expect(err).NotTo(HaveOccurred())
		Expect(patchResponse.StatusCode()).To(Equal(http.StatusOK), string(patchResponse.Body))
		Expect(patchResponse.JSON200).NotTo(BeNil())
		Expect(patchResponse.JSON200.Spec.Expression).To(Equal("status.systemInfo.operatingSystem"))
		Expect(patchResponse.JSON200.Metadata.Generation).NotTo(BeNil())
		Expect(*patchResponse.JSON200.Metadata.Generation).To(BeNumerically(">", createdGeneration))
		waitForMappingReady(harness, mappingName)

		invalidName := uniqueName(harness, "invalid-shape")
		invalid := newLabelSyncMapping(harness, invalidName, &mappingKey, `{"not": "scalar"}`)
		invalidResponse, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, invalid)
		Expect(err).NotTo(HaveOccurred())
		Expect(invalidResponse.StatusCode()).To(Equal(http.StatusUnprocessableEntity), string(invalidResponse.Body))
		Expect(invalidResponse.JSON422).NotTo(BeNil())

		deleteLabelSyncMapping(harness, mappingName)
		waitForMappingDeleted(harness, mappingName)
	})
})
