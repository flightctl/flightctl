package catalogcollector_test

import (
	"errors"
	"net"
	"net/http"
	"syscall"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The destination stamps these labels on every resource it manages. They are
// duplicated here because the destination keeps them unexported; a change to
// either side must be reflected in the other.
const (
	managedByLabel = "flightctl.io/managed-by"
	managedByValue = "flightctl-catalog-collector"
	pipelineLabel  = "flightctl.io/catalog-collector-pipeline"
)

var _ = Describe("Catalog collector end-to-end pipeline", func() {
	Context("when a complete, valid snapshot is delivered", func() {
		It("should create the catalogs and catalog items it describes", func() {
			harness := newCollectorHarness()

			snapshot := snapshotPayload{
				Revision: "rev-1",
				Catalogs: []apiv1alpha1.Catalog{
					withCatalogLabels(
						newCatalog("edge-apps", "Edge Applications"),
						map[string]string{"tier": "gold"},
					),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
					newCatalogItem("edge-apps", "redis", "Redis"),
				},
			}

			status, body := harness.PostSnapshot(snapshot)
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)

			catalog := harness.GetCatalog("edge-apps")
			Expect(catalog).ToNot(BeNil())
			Expect(catalog.Spec.DisplayName).To(HaveValue(Equal("Edge Applications")))
			Expect(labelsOf(catalog.Metadata.Labels)).To(SatisfyAll(
				HaveKeyWithValue("tier", "gold"),
				HaveKeyWithValue(managedByLabel, managedByValue),
				HaveKeyWithValue(pipelineLabel, harness.PipelineID),
			))

			nginx := harness.GetCatalogItem("edge-apps", "nginx")
			Expect(nginx).ToNot(BeNil())
			Expect(nginx.Spec.DisplayName).To(HaveValue(Equal("NGINX")))
			Expect(nginx.Spec.Versions).To(HaveLen(1))
			Expect(labelsOf(nginx.Metadata.Labels)).To(SatisfyAll(
				HaveKeyWithValue(managedByLabel, managedByValue),
				HaveKeyWithValue(pipelineLabel, harness.PipelineID),
			))

			Expect(harness.ManagedCatalogNames()).To(ConsistOf("edge-apps"))
			Expect(harness.ManagedCatalogItemKeys()).To(ConsistOf(
				"edge-apps/nginx", "edge-apps/redis"))
		})
	})

	Context("when the pipeline contains a catalog name processor", func() {
		It("should persist the transformed names rather than the source names", func() {
			harness := newCollectorHarness(withCatalogNameMappings(
				map[string]string{"upstream-models": "edge-models"},
			))

			snapshot := snapshotPayload{
				Revision: "rev-1",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("upstream-models", "Upstream Models"),
					newCatalog("shared-apps", "Shared Applications"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("upstream-models", "detector", "Detector"),
					newCatalogItem("shared-apps", "nginx", "NGINX"),
				},
			}

			status, body := harness.PostSnapshot(snapshot)
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)

			// The mapped catalog is persisted under its new name only, and the
			// item that referenced the old name follows it.
			Expect(harness.GetCatalog("upstream-models")).To(BeNil())
			renamed := harness.GetCatalog("edge-models")
			Expect(renamed).ToNot(BeNil())
			Expect(renamed.Spec.DisplayName).To(HaveValue(Equal("Upstream Models")))

			Expect(harness.GetCatalogItem("edge-models", "detector")).ToNot(BeNil())
			Expect(harness.GetCatalogItem("upstream-models", "detector")).To(BeNil())

			// Catalogs with no mapping entry pass through untouched.
			Expect(harness.GetCatalog("shared-apps")).ToNot(BeNil())
			Expect(harness.ManagedCatalogNames()).To(ConsistOf("edge-models", "shared-apps"))
			Expect(harness.ManagedCatalogItemKeys()).To(ConsistOf(
				"edge-models/detector", "shared-apps/nginx"))
		})
	})

	Context("when the same snapshot content is delivered repeatedly", func() {
		It("should leave every already-correct resource unwritten", func() {
			harness := newCollectorHarness()

			snapshot := snapshotPayload{
				Revision: "rev-1",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
					newCatalogItem("edge-apps", "redis", "Redis"),
				},
			}

			status, body := harness.PostSnapshot(snapshot)
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)

			afterFirstSync := harness.WriteMarkers()
			Expect(afterFirstSync).To(SatisfyAll(
				HaveKey("catalog/edge-apps"),
				HaveKey("catalogitem/edge-apps/nginx"),
				HaveKey("catalogitem/edge-apps/redis"),
			))

			// Same revision: a destination must still verify actual state, but
			// it must not rewrite resources that already match.
			status, body = harness.PostSnapshot(snapshot)
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)
			Expect(harness.WriteMarkers()).To(Equal(afterFirstSync))

			// A new revision carrying identical content must also be a no-op.
			snapshot.Revision = "rev-2"
			status, body = harness.PostSnapshot(snapshot)
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)
			Expect(harness.WriteMarkers()).To(Equal(afterFirstSync))

			Expect(harness.ManagedCatalogItemKeys()).To(ConsistOf(
				"edge-apps/nginx", "edge-apps/redis"))
		})
	})

	Context("when an updated snapshot is delivered", func() {
		It("should update only the resources whose desired state changed", func() {
			harness := newCollectorHarness()

			status, body := harness.PostSnapshot(snapshotPayload{
				Revision: "rev-1",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
					newCatalogItem("edge-apps", "redis", "Redis"),
				},
			})
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)
			afterFirstSync := harness.WriteMarkers()

			updatedItem := newCatalogItem("edge-apps", "nginx", "NGINX (updated)")
			updatedItem.Spec.Versions = append(updatedItem.Spec.Versions,
				apiv1alpha1.CatalogItemVersion{
					Version:    "2.0.0",
					References: map[apiv1alpha1.CatalogItemArtifactType]string{"container": "v2.0.0"},
					Channels:   []string{"fast"},
				})

			status, body = harness.PostSnapshot(snapshotPayload{
				Revision: "rev-2",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications (updated)"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					updatedItem,
					newCatalogItem("edge-apps", "redis", "Redis"),
					newCatalogItem("edge-apps", "grafana", "Grafana"),
				},
			})
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)

			catalog := harness.GetCatalog("edge-apps")
			Expect(catalog).ToNot(BeNil())
			Expect(catalog.Spec.DisplayName).To(HaveValue(Equal("Edge Applications (updated)")))

			nginx := harness.GetCatalogItem("edge-apps", "nginx")
			Expect(nginx).ToNot(BeNil())
			Expect(nginx.Spec.DisplayName).To(HaveValue(Equal("NGINX (updated)")))
			Expect(nginx.Spec.Versions).To(HaveLen(2))

			Expect(harness.GetCatalogItem("edge-apps", "grafana")).ToNot(BeNil())
			Expect(harness.ManagedCatalogItemKeys()).To(ConsistOf(
				"edge-apps/nginx", "edge-apps/redis", "edge-apps/grafana"))

			afterSecondSync := harness.WriteMarkers()
			Expect(afterSecondSync).To(HaveKey("catalogitem/edge-apps/grafana"))
			Expect(afterSecondSync["catalog/edge-apps"]).
				ToNot(Equal(afterFirstSync["catalog/edge-apps"]))
			Expect(afterSecondSync["catalogitem/edge-apps/nginx"]).
				ToNot(Equal(afterFirstSync["catalogitem/edge-apps/nginx"]))
			Expect(afterSecondSync["catalogitem/edge-apps/redis"]).
				To(Equal(afterFirstSync["catalogitem/edge-apps/redis"]))
		})
	})

	Context("when a resource disappears from a complete, valid snapshot", func() {
		It("should prune the managed resources the snapshot no longer declares", func() {
			harness := newCollectorHarness()

			status, body := harness.PostSnapshot(snapshotPayload{
				Revision: "rev-1",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications"),
					newCatalog("legacy-apps", "Legacy Applications"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
					newCatalogItem("edge-apps", "redis", "Redis"),
					newCatalogItem("legacy-apps", "telnet", "Telnet"),
				},
			})
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)

			// Drop one item and one whole catalog.
			status, body = harness.PostSnapshot(snapshotPayload{
				Revision: "rev-2",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
				},
			})
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)

			Expect(harness.GetCatalogItem("edge-apps", "redis")).To(BeNil())
			Expect(harness.GetCatalog("legacy-apps")).To(BeNil())
			Expect(harness.GetCatalogItem("edge-apps", "nginx")).ToNot(BeNil())
			Expect(harness.ManagedCatalogNames()).To(ConsistOf("edge-apps"))
			Expect(harness.ManagedCatalogItemKeys()).To(ConsistOf("edge-apps/nginx"))

			// An empty snapshot is a valid desired state and removes everything
			// the pipeline owns.
			status, body = harness.PostSnapshot(snapshotPayload{
				Revision:     "rev-3",
				Catalogs:     []apiv1alpha1.Catalog{},
				CatalogItems: []apiv1alpha1.CatalogItem{},
			})
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)
			Expect(harness.ManagedCatalogNames()).To(BeEmpty())
			Expect(harness.ManagedCatalogItemKeys()).To(BeEmpty())

			// Pruning is scoped to the pipeline: resources it never owned,
			// such as the organization's built-in catalog, are left alone.
			Expect(harness.GetCatalog("default")).ToNot(BeNil())
		})
	})

	Context("when resources exist outside the pipeline's ownership", func() {
		It("should leave them untouched and refuse to adopt a colliding name", func() {
			harness := newCollectorHarness()

			foreignCatalog := withCatalogLabels(
				newCatalog("external-catalog", "Externally Managed"),
				map[string]string{"owner": "platform-team"},
			)
			harness.CreateCatalog(foreignCatalog)
			harness.CreateCatalogItem(
				newCatalogItem("external-catalog", "legacy-app", "Legacy App"))
			foreignMarkers := harness.WriteMarkers()

			status, body := harness.PostSnapshot(snapshotPayload{
				Revision: "rev-1",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
				},
			})
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)

			// The unmanaged resources are neither relabelled nor pruned.
			persistedForeign := harness.GetCatalog("external-catalog")
			Expect(persistedForeign).ToNot(BeNil())
			Expect(labelsOf(persistedForeign.Metadata.Labels)).To(SatisfyAll(
				HaveKeyWithValue("owner", "platform-team"),
				Not(HaveKey(managedByLabel)),
				Not(HaveKey(pipelineLabel)),
			))
			Expect(harness.GetCatalogItem("external-catalog", "legacy-app")).ToNot(BeNil())

			markersAfterSync := harness.WriteMarkers()
			Expect(markersAfterSync["catalog/external-catalog"]).
				To(Equal(foreignMarkers["catalog/external-catalog"]))
			Expect(markersAfterSync["catalogitem/external-catalog/legacy-app"]).
				To(Equal(foreignMarkers["catalogitem/external-catalog/legacy-app"]))

			// A snapshot that claims an unmanaged name fails instead of
			// overwriting the existing resource.
			status, body = harness.PostSnapshot(snapshotPayload{
				Revision: "rev-2",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications"),
					newCatalog("external-catalog", "Hijacked"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
				},
			})
			Expect(status).To(Equal(http.StatusBadGateway), "source response body: %s", body)

			stillForeign := harness.GetCatalog("external-catalog")
			Expect(stillForeign).ToNot(BeNil())
			Expect(stillForeign.Spec.DisplayName).To(HaveValue(Equal("Externally Managed")))
			Expect(harness.WriteMarkers()["catalog/external-catalog"]).
				To(Equal(foreignMarkers["catalog/external-catalog"]))

			// The pipeline's own resources survive the failed reconciliation.
			Expect(harness.GetCatalog("edge-apps")).ToNot(BeNil())
			Expect(harness.GetCatalogItem("edge-apps", "nginx")).ToNot(BeNil())
		})
	})

	Context("when an invalid snapshot is delivered", func() {
		It("should reject it and keep previously synchronized resources intact", func() {
			harness := newCollectorHarness()

			status, body := harness.PostSnapshot(snapshotPayload{
				Revision: "rev-1",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
				},
			})
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)
			baseline := harness.WriteMarkers()

			By("rejecting a snapshot whose catalog item fails resource validation")
			invalidItem := newCatalogItem("edge-apps", "broken", "Broken")
			invalidItem.Spec.Versions = nil
			status, body = harness.PostSnapshot(snapshotPayload{
				Revision:     "rev-2",
				Catalogs:     []apiv1alpha1.Catalog{newCatalog("edge-apps", "Edge Applications")},
				CatalogItems: []apiv1alpha1.CatalogItem{invalidItem},
			})
			Expect(status).To(Equal(http.StatusBadGateway), "source response body: %s", body)

			By("rejecting a snapshot that omits a required collection")
			status, body = harness.PostRawSnapshot(`{"revision":"rev-3","catalogs":[]}`)
			Expect(status).To(Equal(http.StatusBadRequest), "source response body: %s", body)

			By("rejecting a malformed request body")
			status, body = harness.PostRawSnapshot(`{"revision":`)
			Expect(status).To(Equal(http.StatusBadRequest), "source response body: %s", body)

			By("keeping the last successfully synchronized state")
			Expect(harness.GetCatalog("edge-apps")).ToNot(BeNil())
			Expect(harness.GetCatalogItem("edge-apps", "nginx")).ToNot(BeNil())
			Expect(harness.GetCatalogItem("edge-apps", "broken")).To(BeNil())
			Expect(harness.WriteMarkers()).To(Equal(baseline))

			By("accepting a later valid snapshot")
			status, body = harness.PostSnapshot(snapshotPayload{
				Revision: "rev-4",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
					newCatalogItem("edge-apps", "redis", "Redis"),
				},
			})
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)
			Expect(harness.ManagedCatalogItemKeys()).To(ConsistOf(
				"edge-apps/nginx", "edge-apps/redis"))
		})
	})

	Context("when the collector exposes health endpoints", func() {
		It("should report live and ready while running and stop cleanly on cancellation", func() {
			harness := newCollectorHarness()

			Expect(harness.ProbeStatus(harness.LiveURL())).To(Equal(http.StatusOK))
			Expect(harness.ProbeStatus(harness.ReadyURL())).To(Equal(http.StatusOK))

			status, body := harness.PostSnapshot(snapshotPayload{
				Revision: "rev-1",
				Catalogs: []apiv1alpha1.Catalog{
					newCatalog("edge-apps", "Edge Applications"),
				},
				CatalogItems: []apiv1alpha1.CatalogItem{
					newCatalogItem("edge-apps", "nginx", "NGINX"),
				},
			})
			Expect(status).To(Equal(http.StatusNoContent), "source response body: %s", body)

			By("shutting down gracefully when the service context is cancelled")
			Expect(harness.StopCollector()).To(Succeed())

			// Both the probe server and the snapshot listener are closed by the
			// shutdown sequence, so neither endpoint answers any longer.
			Eventually(harness.ProbeStatus, shutdownTimeout, pollInterval).
				WithArguments(harness.ReadyURL()).Should(BeZero())
			Eventually(harness.ProbeStatus, shutdownTimeout, pollInterval).
				WithArguments(harness.LiveURL()).Should(BeZero())

			By("closing the snapshot listener so it stops accepting connections")
			// A probe that returns no status only proves no HTTP response was
			// produced; dialling the source address directly proves the
			// listening socket itself is gone. Each attempt is bounded by
			// DialTimeout so a black-holed address cannot wedge the spec.
			Eventually(func(g Gomega) {
				conn, err := net.DialTimeout(
					"tcp", harness.SourceAddress(), probeTimeout)
				if err == nil {
					g.Expect(conn.Close()).To(Succeed())
				}
				g.Expect(err).To(HaveOccurred(),
					"snapshot listener at %s still accepts connections",
					harness.SourceAddress())
				g.Expect(errors.Is(err, syscall.ECONNREFUSED)).To(BeTrue(),
					"expected connection refused, got: %v", err)
			}, shutdownTimeout, pollInterval).Should(Succeed())

			By("leaving the synchronized resources in place")
			Expect(harness.GetCatalog("edge-apps")).ToNot(BeNil())
			Expect(harness.GetCatalogItem("edge-apps", "nginx")).ToNot(BeNil())
		})
	})
})
