package catalogcollector_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	v1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// Each Context below is one isolated flow: it owns a collector pipeline, a
// source with its own server-side model selection, and a Flight Control
// catalog, and it seeds the Model Registry content it needs from its own spec.
// Nothing is shared except the deployment itself, so the specs can run in any
// order, and the deliberately unusable fixture the last one introduces cannot
// reach any other scenario's collection cycle.
//
// The suite is opt-in rather than part of sanity: it deploys a collector of
// its own and needs the collector image, which the default backend build does
// not produce. CI selects it by the labels below.
var _ = Describe("Catalog collector", Label("catalogcollector"), func() {
	Context("initial synchronization from a Kubeflow Model Registry", func() {
		It("imports a LIVE model into a Flight Control catalog and catalog item", Label("catalogcollector-smoke"), func() {
			sc := resources.scenario(scenarioSmoke)
			seeded := smokeModel.versions[0]

			var catalog *v1alpha1.Catalog

			By("waiting for the collector to create the catalog")
			Eventually(func() error {
				var err error
				catalog, err = suiteHarness.GetCatalog(sc.catalogName)
				return err
			}).WithTimeout(catalogSyncTimeout).WithPolling(catalogSyncPolling).
				Should(Succeed(), "catalog %q must be created by the collector", sc.catalogName)

			By("verifying the catalog carries the collector ownership labels")
			Expect(labelsOf(catalog.Metadata.Labels)).To(SatisfyAll(
				HaveKeyWithValue(managedByLabel, managedByValue),
				HaveKeyWithValue(pipelineLabel, sc.pipelineName),
			), "catalog %q must be labelled as managed by this collector pipeline", sc.catalogName)
			Expect(catalog.Spec.DisplayName).ToNot(BeNil())
			Expect(*catalog.Spec.DisplayName).To(Equal(sc.catalogName))

			By("waiting for the collector to create the catalog item for the seeded model")
			item := waitForCatalogItem(sc, smokeModel.itemName(), catalogSyncTimeout)

			By("verifying the catalog item carries the collector ownership labels")
			Expect(labelsOf(item.Metadata.Labels)).To(SatisfyAll(
				HaveKeyWithValue(managedByLabel, managedByValue),
				HaveKeyWithValue(pipelineLabel, sc.pipelineName),
			), "catalog item %q must be labelled as managed by this collector pipeline", smokeModel.itemName())
			Expect(item.Metadata.Catalog).To(Equal(sc.catalogName))

			By("verifying the catalog item reproduces the registered model metadata")
			Expect(item.Spec.DisplayName).ToNot(BeNil())
			Expect(*item.Spec.DisplayName).To(Equal(smokeModel.name),
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
			Expect(version.Version).To(Equal(seeded.name))
			Expect(version.References).To(HaveKeyWithValue(v1alpha1.CatalogItemArtifactTypeContainer, seeded.digest),
				"version %q must reference the digest seeded in the Model Registry", version.Version)
		})
	})

	Context("a registered model that changes after it was imported", func() {
		It("updates the catalog item in place with the new metadata and version", Label("catalogcollector-update"), func() {
			ctx := context.Background()
			sc := resources.scenario(scenarioUpdate)

			By("seeding a LIVE model with one eligible version")
			original := eligibleVersion(resources, sc, "1.0.0")
			model, err := seedModel(ctx, auxSvcs.ModelRegistry, resources, sc, "model",
				"description before the update", []versionSpec{original})
			Expect(err).ToNot(HaveOccurred(), "failed to seed the model under test")

			By("waiting for the collector to publish it")
			published := waitForCatalogItem(sc, model.itemName(), catalogSyncTimeout)
			Expect(published.Spec.Versions).To(HaveLen(1))
			Expect(published.Spec.Versions[0].Version).To(Equal(original.name))
			originalVersion := published.Spec.Versions[0]
			createdAt := published.Metadata.CreationTimestamp
			Expect(createdAt).ToNot(BeNil(), "the API must report when the item was created")

			By("changing the model metadata in the Model Registry")
			const updatedDescription = "description after the update"
			const updatedOwner = "flightctl-e2e-updated"
			_, err = auxSvcs.ModelRegistry.UpdateRegisteredModel(ctx, model.id, auxiliary.RegisteredModelUpdate{
				Description: lo.ToPtr(updatedDescription),
				Owner:       lo.ToPtr(updatedOwner),
			})
			Expect(err).ToNot(HaveOccurred(), "failed to update the registered model metadata")

			By("adding a second eligible version with a different immutable digest")
			added := eligibleVersion(resources, sc, "2.1.0")
			Expect(added.digest).ToNot(Equal(original.digest),
				"the second version must be pinned to a different digest for this spec to mean anything")
			_, err = addModelVersion(ctx, auxSvcs.ModelRegistry, model, added)
			Expect(err).ToNot(HaveOccurred(), "failed to add the second model version")

			By("waiting for the catalog item to carry the whole expected state")
			// The complete expectation is waited for, not waited for in part
			// and then asserted on. The two registry changes above are two
			// separate calls, and a collection reads the model metadata and
			// its versions separately as well, so a cycle that interleaves
			// with them publishes both versions while still carrying the old
			// description. That is a legitimate intermediate state, and
			// waiting only for the version count would reach the metadata
			// assertions while it was showing.
			var updated *v1alpha1.CatalogItem
			Eventually(func(g Gomega) {
				var err error
				updated, err = suiteHarness.GetCatalogItem(sc.catalogName, model.itemName())
				g.Expect(err).ToNot(HaveOccurred())

				g.Expect(updated.Spec.ShortDescription).To(HaveValue(Equal(updatedDescription)))
				g.Expect(updated.Spec.Provider).To(HaveValue(Equal(updatedOwner)))
				g.Expect(updated.Spec.DisplayName).To(HaveValue(Equal(model.name)),
					"metadata changes must not rename the item")

				g.Expect(publishedVersionDigests(updated)).To(Equal(map[string]string{
					original.name: original.digest,
					added.name:    added.digest,
				}), "both versions must be published, each resolving to its own digest")

				g.Expect(updated.Spec.Artifacts).To(HaveLen(1))
				g.Expect(updated.Spec.Artifacts[0].Uri).To(Equal(artifactRepository),
					"both versions share the one repository stored on the item")
			}).WithTimeout(catalogChangeTimeout).WithPolling(catalogSyncPolling).
				Should(Succeed(), "catalog item %q must carry the new metadata and both versions",
					model.itemName())

			By("verifying the item was updated in place rather than recreated")
			Expect(updated.Metadata.CreationTimestamp).ToNot(BeNil())
			Expect(*updated.Metadata.CreationTimestamp).To(Equal(*createdAt),
				"a changed model must update the existing catalog item, not replace it")

			By("verifying the original version is retained unchanged")
			// Asserted outside the wait above on purpose: this must hold of
			// the state that satisfied it, not be something the spec waits
			// for the collector to converge on.
			retained := catalogItemVersion(updated, original.name)
			Expect(retained).ToNot(BeNil(), "version %q must still be published", original.name)
			Expect(*retained).To(Equal(originalVersion),
				"an update must not rewrite the version that was already published")
		})
	})

	Context("registry content that is archived after it was imported", func() {
		It("removes an archived version and then the archived model's catalog item", Label("catalogcollector-removal"), func() {
			ctx := context.Background()
			sc := resources.scenario(scenarioRemoval)

			By("seeding a LIVE model with two eligible versions")
			kept := eligibleVersion(resources, sc, "1.0.0")
			archived := eligibleVersion(resources, sc, "2.0.0")
			model, err := seedModel(ctx, auxSvcs.ModelRegistry, resources, sc, "model",
				"model whose content is archived", []versionSpec{kept, archived})
			Expect(err).ToNot(HaveOccurred(), "failed to seed the model under test")

			By("waiting for the collector to publish both versions")
			var published *v1alpha1.CatalogItem
			Eventually(func(g Gomega) {
				var err error
				published, err = suiteHarness.GetCatalogItem(sc.catalogName, model.itemName())
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(published.Spec.Versions).To(HaveLen(2))
			}).WithTimeout(catalogSyncTimeout).WithPolling(catalogSyncPolling).
				Should(Succeed(), "catalog item %q must be published with both versions", model.itemName())
			keptVersion := catalogItemVersion(published, kept.name)
			Expect(keptVersion).ToNot(BeNil())

			By("archiving one of the two versions")
			Expect(auxSvcs.ModelRegistry.ArchiveModelVersion(ctx, model.version(archived.name).id)).
				To(Succeed(), "failed to archive version %q", archived.name)

			By("waiting for the archived version to disappear from the catalog item")
			var remaining *v1alpha1.CatalogItem
			Eventually(func(g Gomega) {
				var err error
				remaining, err = suiteHarness.GetCatalogItem(sc.catalogName, model.itemName())
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(remaining.Spec.Versions).To(HaveLen(1))
			}).WithTimeout(catalogChangeTimeout).WithPolling(catalogSyncPolling).
				Should(Succeed(), "archiving version %q must remove it from catalog item %q",
					archived.name, model.itemName())

			By("verifying the remaining version is the one that was not archived, unchanged")
			Expect(remaining.Spec.Versions[0]).To(Equal(*keptVersion),
				"removing one version must leave the other exactly as it was published")

			By("archiving the registered model itself")
			Expect(auxSvcs.ModelRegistry.ArchiveRegisteredModel(ctx, model.id)).
				To(Succeed(), "failed to archive model %q", model.name)

			By("waiting for the catalog item to be deleted")
			Eventually(func() (int, error) {
				return catalogItemStatus(sc.catalogName, model.itemName())
			}).WithTimeout(catalogChangeTimeout).WithPolling(catalogSyncPolling).
				Should(Equal(http.StatusNotFound),
					"archiving model %q must prune catalog item %q", model.name, model.itemName())

			By("verifying the configured catalog itself survives")
			// The source reports its configured catalog on every cycle, with
			// or without items, so an empty selection must empty the catalog
			// rather than remove it.
			Consistently(func() error {
				_, err := suiteHarness.GetCatalog(sc.catalogName)
				return err
			}).WithTimeout(unchangedObservationWindow).WithPolling(unchangedObservationPoll).
				Should(Succeed(), "catalog %q must not be pruned when its last item is", sc.catalogName)
		})
	})

	Context("a collection cycle that fails on unusable registry content", func() {
		It("keeps previously published content when a collection is rejected", Label("catalogcollector-failure-safety"), func() {
			ctx := context.Background()
			sc := resources.scenario(scenarioSafety)

			By("seeding two LIVE models and waiting for both to be published")
			// Two, because the two ways a failed cycle can damage published
			// content are different. Model A is the one whose metadata changes
			// while the cycle is failing, and catches a partial update. Model
			// B is the one that is archived while the cycle is failing, and
			// catches the opposite mistake: reconciling a partial snapshot
			// would prune it, and a scenario whose every model is supposed to
			// survive unchanged cannot tell "nothing was applied" from
			// "nothing was due".
			changedModel, err := seedModel(ctx, auxSvcs.ModelRegistry, resources, sc, "model-a",
				"content whose later change must not survive a failed collection",
				[]versionSpec{eligibleVersion(resources, sc, "1.0.0")})
			Expect(err).ToNot(HaveOccurred(), "failed to seed the model whose metadata changes later")

			retiredModel, err := seedModel(ctx, auxSvcs.ModelRegistry, resources, sc, "model-b",
				"content whose later removal must not survive a failed collection",
				[]versionSpec{eligibleVersion(resources, sc, "2.0.0")})
			Expect(err).ToNot(HaveOccurred(), "failed to seed the model that is archived later")

			changedPublished := waitForCatalogItem(sc, changedModel.itemName(), catalogSyncTimeout)
			Expect(changedPublished.Metadata.CreationTimestamp).ToNot(BeNil())
			changedPublishedAt := *changedPublished.Metadata.CreationTimestamp
			changedPublishedSpec := changedPublished.Spec

			retiredPublished := waitForCatalogItem(sc, retiredModel.itemName(), catalogSyncTimeout)
			Expect(retiredPublished.Metadata.CreationTimestamp).ToNot(BeNil())
			retiredPublishedAt := *retiredPublished.Metadata.CreationTimestamp
			retiredPublishedSpec := retiredPublished.Spec

			By("introducing a selected version whose artifact URI is malformed")
			// The digest is not 64 hexadecimal characters, so the source
			// rejects the artifact as an invalid immutable OCI reference. That
			// is an actionable data error: it fails the whole cycle for this
			// source, which is exactly the situation this spec is about.
			broken, err := seedModel(ctx, auxSvcs.ModelRegistry, resources, sc, "broken",
				"model with an unusable artifact", []versionSpec{{
					name: "9.9.9",
					uri:  fmt.Sprintf("oci://%s@sha256:not-a-valid-digest", artifactRepository),
				}})
			Expect(err).ToNot(HaveOccurred(), "failed to seed the unusable model")

			By("waiting for the collector to attempt and reject the collection repeatedly")
			// More than one rejection is required: a single one proves only
			// that the broken content was seen, while repeated ones prove the
			// source kept cycling in the failed state the rest of this spec
			// depends on.
			waitForRejectedCollections(ctx, sc, broken.name, minObservedCollectionFailures,
				"the collector must report rejecting the collection for source %q", sc.sourceID)

			By("changing one published model and archiving the other while the collection still fails")
			// Both changes are made against a source that cannot complete a
			// cycle, so neither may reach the catalog. Together they make the
			// snapshot the collector keeps rejecting differ from the published
			// state in both directions: one item would have to be rewritten
			// and one would have to be pruned.
			const rejectedDescription = "description from a collection that must never be applied"
			const rejectedOwner = "flightctl-e2e-never-applied"
			_, err = auxSvcs.ModelRegistry.UpdateRegisteredModel(ctx, changedModel.id, auxiliary.RegisteredModelUpdate{
				Description: lo.ToPtr(rejectedDescription),
				Owner:       lo.ToPtr(rejectedOwner),
			})
			Expect(err).ToNot(HaveOccurred(), "failed to change the registered model metadata")
			Expect(auxSvcs.ModelRegistry.ArchiveRegisteredModel(ctx, retiredModel.id)).
				To(Succeed(), "failed to archive model %q", retiredModel.name)

			By("waiting for rejected collections attempted after those changes")
			// The baseline is read after the changes were made, and
			// minObservedCollectionFailures further rejections are required on
			// top of it. The first of those may belong to a cycle that started
			// before the changes; the second cannot, because it could only
			// start after the first had been logged, which is after the
			// baseline read. So at least one whole cycle saw the changed
			// registry, tried to apply it, and was rejected.
			baseline, err := rejectedCollectionCount(ctx, sc, broken.name)
			Expect(err).ToNot(HaveOccurred(), "failed to read the collector log")
			waitForRejectedCollections(ctx, sc, broken.name, baseline+minObservedCollectionFailures,
				"the collector must keep rejecting collections for source %q after the registry changed",
				sc.sourceID)

			By("verifying the previously published content stays exactly as it was")
			// Read immediately before the window opens so the check that closes
			// it counts only rejections logged while the content was watched.
			windowOpened, err := rejectedCollectionCount(ctx, sc, broken.name)
			Expect(err).ToNot(HaveOccurred(), "failed to read the collector log")

			// Watched across several poll intervals rather than checked once:
			// the cycle that could prune or partially rewrite content is the
			// next one, not the instant after a rejection was logged.
			Consistently(func(g Gomega) {
				current, err := suiteHarness.GetCatalogItem(sc.catalogName, changedModel.itemName())
				g.Expect(err).ToNot(HaveOccurred(), "the catalog item of the model whose metadata changed must not be pruned")
				g.Expect(current.Spec.ShortDescription).ToNot(HaveValue(Equal(rejectedDescription)),
					"a rejected collection must not publish the metadata it carried")
				g.Expect(current.Spec.Provider).ToNot(HaveValue(Equal(rejectedOwner)),
					"a rejected collection must not publish the metadata it carried")
				g.Expect(domain.CatalogItemSpecsAreEqual(current.Spec, changedPublishedSpec)).To(BeTrue(),
					"a failed collection must not rewrite published content: got %+v, want %+v",
					current.Spec, changedPublishedSpec)
				g.Expect(current.Metadata.CreationTimestamp).To(HaveValue(Equal(changedPublishedAt)),
					"the published item must not be deleted and recreated")

				surviving, err := suiteHarness.GetCatalogItem(sc.catalogName, retiredModel.itemName())
				g.Expect(err).ToNot(HaveOccurred(),
					"a failed collection must not prune the item of a model it could not confirm archived")
				g.Expect(domain.CatalogItemSpecsAreEqual(surviving.Spec, retiredPublishedSpec)).To(BeTrue(),
					"a failed collection must not rewrite published content: got %+v, want %+v",
					surviving.Spec, retiredPublishedSpec)
				g.Expect(surviving.Metadata.CreationTimestamp).To(HaveValue(Equal(retiredPublishedAt)),
					"the published item must not be deleted and recreated")

				_, err = suiteHarness.GetCatalog(sc.catalogName)
				g.Expect(err).ToNot(HaveOccurred(), "the catalog must not be pruned")

				status, err := catalogItemStatus(sc.catalogName, broken.itemName())
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(status).To(Equal(http.StatusNotFound),
					"a rejected collection must not publish any part of the content that failed it")
			}).WithTimeout(unchangedObservationWindow).WithPolling(unchangedObservationPoll).
				Should(Succeed())

			By("verifying the source was still rejecting collections throughout that window")
			// Without this the window above proves less than it appears to. A
			// source that stopped cycling at all - a crashed pod, a loop that
			// exited - would leave published content untouched too, and every
			// assertion in it would pass while nothing was being tested. The
			// window spans several retry intervals, so at least one more
			// rejection must have been logged while it ran.
			Expect(rejectedCollectionCount(ctx, sc, broken.name)).
				To(BeNumerically(">", windowOpened),
					"source %q must still have been attempting and rejecting collections "+
						"while its published content was being watched", sc.sourceID)
		})
	})
})

// waitForCatalogItem waits for one catalog item to be published and returns it.
func waitForCatalogItem(sc *scenario, itemName string, timeout time.Duration) *v1alpha1.CatalogItem {
	GinkgoHelper()

	var item *v1alpha1.CatalogItem
	Eventually(func() error {
		var err error
		item, err = suiteHarness.GetCatalogItem(sc.catalogName, itemName)
		return err
	}).WithTimeout(timeout).WithPolling(catalogSyncPolling).
		Should(Succeed(), "catalog item %q must be created in catalog %q", itemName, sc.catalogName)
	return item
}

// rejectedCollectionCount reads the collector log once and returns how many
// rejected collection cycles it attributes to this scenario's source.
func rejectedCollectionCount(ctx context.Context, sc *scenario, detail string) (int, error) {
	lines, err := collectorLogLines(ctx, collectorInstance)
	if err != nil {
		return 0, err
	}
	return countRejectedCollections(lines, sc, detail), nil
}

// waitForRejectedCollections waits until the collector log shows at least want
// rejected collection cycles for this scenario's source.
//
// The count is taken over the same bounded log tail every time, so a caller
// that passes a count relative to an earlier reading is asking for that many
// more rejections to be visible, not for that many to have happened since.
// The two agree here because the collector logs a few lines per cycle and the
// tail spans far more cycles than any wait in this suite.
func waitForRejectedCollections(ctx context.Context, sc *scenario, detail string, want int,
	description string, args ...any) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		count, err := rejectedCollectionCount(ctx, sc, detail)
		g.Expect(err).ToNot(HaveOccurred(), "failed to read the collector log")
		g.Expect(count).To(BeNumerically(">=", want))
	}).WithTimeout(catalogChangeTimeout).WithPolling(catalogSyncPolling).
		Should(Succeed(), append([]any{description}, args...)...)
}

// publishedVersionDigests maps every published version name to the immutable
// container reference it resolves to, so a spec can state the whole expected
// version set as one value instead of a lookup per version.
func publishedVersionDigests(item *v1alpha1.CatalogItem) map[string]string {
	digests := make(map[string]string, len(item.Spec.Versions))
	for _, version := range item.Spec.Versions {
		digests[version.Version] = version.References[v1alpha1.CatalogItemArtifactTypeContainer]
	}
	return digests
}

// catalogItemVersion returns the published version with this name, or nil.
func catalogItemVersion(item *v1alpha1.CatalogItem, version string) *v1alpha1.CatalogItemVersion {
	for i := range item.Spec.Versions {
		if item.Spec.Versions[i].Version == version {
			return &item.Spec.Versions[i]
		}
	}
	return nil
}

// catalogItemStatus returns the HTTP status the API answers a read of one
// catalog item with.
//
// Absence is asserted on the status rather than on "an error happened":
// GetCatalogItem reports a transport failure and a deleted item the same way,
// and a spec that accepts any error would pass while the API was unreachable.
func catalogItemStatus(catalogName, itemName string) (int, error) {
	client := suiteHarness.GetV1Alpha1Client()
	if client == nil {
		return 0, fmt.Errorf("v1alpha1 client not available")
	}
	response, err := client.GetCatalogItemWithResponse(suiteHarness.Context, catalogName, itemName)
	if err != nil {
		return 0, fmt.Errorf("read catalog item %s/%s: %w", catalogName, itemName, err)
	}
	return response.StatusCode(), nil
}

// labelsOf dereferences the optional label map so Gomega matchers can be used
// on it without a nil check at every assertion.
func labelsOf(labels *map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	return *labels
}
