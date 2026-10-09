package catalogcollector_test

import (
	v1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Catalog collector", Label("catalogcollector", "sanity"), func() {
	Context("initial synchronization from a Kubeflow Model Registry", func() {
		It("imports a LIVE model into a Flight Control catalog and catalog item", Label("catalogcollector-smoke"), func() {
			var catalog *v1alpha1.Catalog

			By("waiting for the collector to create the catalog")
			Eventually(func() error {
				var err error
				catalog, err = suiteHarness.GetCatalog(resources.catalogName)
				return err
			}).WithTimeout(catalogSyncTimeout).WithPolling(catalogSyncPolling).
				Should(Succeed(), "catalog %q must be created by the collector", resources.catalogName)

			By("verifying the catalog carries the collector ownership labels")
			Expect(labelsOf(catalog.Metadata.Labels)).To(SatisfyAll(
				HaveKeyWithValue(managedByLabel, managedByValue),
				HaveKeyWithValue(pipelineLabel, resources.pipelineName),
			), "catalog %q must be labelled as managed by this collector pipeline", resources.catalogName)
			Expect(catalog.Spec.DisplayName).ToNot(BeNil())
			Expect(*catalog.Spec.DisplayName).To(Equal(resources.catalogName))

			var item *v1alpha1.CatalogItem

			By("waiting for the collector to create the catalog item for the seeded model")
			Eventually(func() error {
				var err error
				item, err = suiteHarness.GetCatalogItem(resources.catalogName, resources.itemName)
				return err
			}).WithTimeout(catalogSyncTimeout).WithPolling(catalogSyncPolling).
				Should(Succeed(), "catalog item %q must be created in catalog %q", resources.itemName, resources.catalogName)

			By("verifying the catalog item carries the collector ownership labels")
			Expect(labelsOf(item.Metadata.Labels)).To(SatisfyAll(
				HaveKeyWithValue(managedByLabel, managedByValue),
				HaveKeyWithValue(pipelineLabel, resources.pipelineName),
			), "catalog item %q must be labelled as managed by this collector pipeline", resources.itemName)
			Expect(item.Metadata.Catalog).To(Equal(resources.catalogName))

			By("verifying the catalog item reproduces the registered model metadata")
			Expect(item.Spec.DisplayName).ToNot(BeNil())
			Expect(*item.Spec.DisplayName).To(Equal(resources.modelName),
				"the display name must keep the original registered model name")
			Expect(item.Spec.Provider).ToNot(BeNil())
			Expect(*item.Spec.Provider).To(Equal(modelOwner))
			Expect(item.Spec.Type).To(Equal(v1alpha1.CatalogItemTypeData))
			Expect(item.Spec.Category).ToNot(BeNil())
			Expect(*item.Spec.Category).To(Equal(v1alpha1.CatalogItemCategoryApplication))

			By("verifying the artifact reference points at the version-less OCI repository")
			Expect(item.Spec.Artifacts).To(HaveLen(1))
			Expect(item.Spec.Artifacts[0].Type).To(Equal(v1alpha1.CatalogItemArtifactTypeContainer))
			Expect(item.Spec.Artifacts[0].Uri).To(Equal(artifactRepository),
				"the repository is stored once on the item; the per-version digest lives in the version references")

			By("verifying the SemVer version resolves to the immutable ModelCar digest")
			Expect(item.Spec.Versions).To(HaveLen(1))
			version := item.Spec.Versions[0]
			Expect(version.Version).To(Equal(resources.modelVersionName))
			Expect(version.References).To(HaveKeyWithValue(v1alpha1.CatalogItemArtifactTypeContainer, resources.artifactDigest),
				"version %q must reference the digest seeded in the Model Registry", version.Version)
		})
	})
})

// labelsOf dereferences the optional label map so Gomega matchers can be used
// on it without a nil check at every assertion.
func labelsOf(labels *map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	return *labels
}
