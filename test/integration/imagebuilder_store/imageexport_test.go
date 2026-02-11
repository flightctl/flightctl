package imagebuilder_store_test

import (
	"context"
	"os"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/store/testutil"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutilpkg "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func newTestImageExport(name string) *api.ImageExport {
	source := api.ImageExportSource{}
	_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
		Type:          api.ImageBuildRefSourceTypeImageBuild,
		ImageBuildRef: "test-image-build",
	})

	return &api.ImageExport{
		ApiVersion: api.ImageExportAPIVersion,
		Kind:       string(api.ResourceKindImageExport),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImageExportSpec{
			Source: source,
			Format: api.ExportFormatTypeQCOW2,
		},
	}
}

func newTestImageExportWithImageBuildRef(name string, imageBuildRef string) *api.ImageExport {
	source := api.ImageExportSource{}
	_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
		Type:          api.ImageBuildRefSourceTypeImageBuild,
		ImageBuildRef: imageBuildRef,
	})

	return &api.ImageExport{
		ApiVersion: api.ImageExportAPIVersion,
		Kind:       string(api.ResourceKindImageExport),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImageExportSpec{
			Source: source,
			Format: api.ExportFormatTypeQCOW2,
		},
	}
}

var _ = Describe("ImageExportStore", func() {
	var (
		log           *logrus.Logger
		ctx           context.Context
		orgId         uuid.UUID
		storeInst     store.Store
		mainStoreInst flightctlstore.Store
		cfg           *config.Config
		dbName        string
		db            *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutilpkg.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()

		// Use main store's PrepareDBForUnitTests which includes organizations table
		mainStoreInst, cfg, dbName, db = flightctlstore.PrepareDBForUnitTests(ctx, log)

		// Create imagebuilder store on the same db connection
		storeInst = store.NewStore(db, log.WithField("pkg", "imagebuilder-store"))

		// Run imagebuilder-specific migrations only for local strategy
		// Template strategy already has imagebuilder tables from flightctl-db-migrate
		strategy := os.Getenv("FLIGHTCTL_TEST_DB_STRATEGY")
		if strategy != testutil.StrategyTemplate {
			err := storeInst.RunMigrations(ctx)
			Expect(err).ToNot(HaveOccurred())
		}

		// Create test organization (required for foreign key constraint)
		orgId = uuid.New()
		err := testutilpkg.CreateTestOrganization(ctx, mainStoreInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		flightctlstore.DeleteTestDB(ctx, log, cfg, mainStoreInst, dbName)
	})

	Context("Create", func() {
		It("should create an ImageExport successfully", func() {
			imageExport := newTestImageExport("test-export-1")
			result, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)

			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.Metadata.Name)).To(Equal("test-export-1"))
			Expect(result.Metadata.CreationTimestamp).ToNot(BeNil())
			Expect(result.Metadata.Generation).ToNot(BeNil())
			Expect(*result.Metadata.Generation).To(Equal(int64(1)))
			Expect(result.Metadata.ResourceVersion).ToNot(BeNil())
			Expect(*result.Metadata.ResourceVersion).ToNot(BeEmpty())
		})

		It("should fail to create duplicate ImageExport", func() {
			imageExport := newTestImageExport("duplicate-export")

			_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).ToNot(HaveOccurred())

			_, err = storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrDuplicateName))
		})

		It("should fail to create ImageExport with nil name", func() {
			imageExport := newTestImageExport("test")
			imageExport.Metadata.Name = nil

			_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("Get", func() {
		It("should get an existing ImageExport", func() {
			imageExport := newTestImageExport("get-test")
			_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImageExport().Get(ctx, orgId, "get-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.Metadata.Name)).To(Equal("get-test"))
		})

		It("should return not found for non-existent ImageExport", func() {
			_, err := storeInst.ImageExport().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return not found for wrong org", func() {
			imageExport := newTestImageExport("org-test")
			_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).ToNot(HaveOccurred())

			// Create another org for isolation test
			wrongOrgId := uuid.New()
			err = testutilpkg.CreateTestOrganization(ctx, mainStoreInst, wrongOrgId)
			Expect(err).ToNot(HaveOccurred())

			_, err = storeInst.ImageExport().Get(ctx, wrongOrgId, "org-test")
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})

	Context("List", func() {
		It("should list all ImageExports", func() {
			for i := 0; i < 3; i++ {
				imageExport := newTestImageExport(string(rune('a'+i)) + "-export")
				_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
				Expect(err).ToNot(HaveOccurred())
			}

			result, err := storeInst.ImageExport().List(ctx, orgId, flightctlstore.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(HaveLen(3))
		})

		It("should list with limit", func() {
			for i := 0; i < 5; i++ {
				imageExport := newTestImageExport(string(rune('a'+i)) + "-export")
				_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
				Expect(err).ToNot(HaveOccurred())
			}

			result, err := storeInst.ImageExport().List(ctx, orgId, flightctlstore.ListParams{Limit: 2})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(HaveLen(2))
		})

		It("should not list ImageExports from other orgs", func() {
			imageExport := newTestImageExport("org-isolation-test")
			_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).ToNot(HaveOccurred())

			// Create another org for isolation test
			otherOrgId := uuid.New()
			err = testutilpkg.CreateTestOrganization(ctx, mainStoreInst, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImageExport().List(ctx, otherOrgId, flightctlstore.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(0))
		})

		It("should filter ImageExports by imageBuildRef using fieldSelector with = operator", func() {
			// Create ImageBuilds
			build1 := &api.ImageBuild{
				ApiVersion: api.ImageBuildAPIVersion,
				Kind:       string(api.ResourceKindImageBuild),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("filter-build-1"),
				},
				Spec: api.ImageBuildSpec{
					Source: api.ImageBuildSource{
						Repository: "input-registry",
						ImageName:  "input-image",
						ImageTag:   "v1.0",
					},
					Destination: api.ImageBuildDestination{
						Repository: "output-registry",
						ImageName:  "output-image",
						ImageTag:   "v1.0",
					},
				},
			}
			_, err := storeInst.ImageBuild().Create(ctx, orgId, build1)
			Expect(err).ToNot(HaveOccurred())

			build2 := &api.ImageBuild{
				ApiVersion: api.ImageBuildAPIVersion,
				Kind:       string(api.ResourceKindImageBuild),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("filter-build-2"),
				},
				Spec: api.ImageBuildSpec{
					Source: api.ImageBuildSource{
						Repository: "input-registry",
						ImageName:  "input-image",
						ImageTag:   "v1.0",
					},
					Destination: api.ImageBuildDestination{
						Repository: "output-registry",
						ImageName:  "output-image",
						ImageTag:   "v1.0",
					},
				},
			}
			_, err = storeInst.ImageBuild().Create(ctx, orgId, build2)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExports that reference build1
			export1 := newTestImageExportWithImageBuildRef("filter-export-1", "filter-build-1")
			_, err = storeInst.ImageExport().Create(ctx, orgId, export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := newTestImageExportWithImageBuildRef("filter-export-2", "filter-build-1")
			_, err = storeInst.ImageExport().Create(ctx, orgId, export2)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExport that references build2
			export3 := newTestImageExportWithImageBuildRef("filter-export-3", "filter-build-2")
			_, err = storeInst.ImageExport().Create(ctx, orgId, export3)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExport with ImageReferenceSource (should not be included)
			export4 := newTestImageExport("filter-export-4")
			_, err = storeInst.ImageExport().Create(ctx, orgId, export4)
			Expect(err).ToNot(HaveOccurred())

			// Filter by build1 - should return only export1 and export2
			listParams := flightctlstore.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.source.imageBuildRef": "filter-build-1"}),
			}
			result, err := storeInst.ImageExport().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(2))

			exportNames := make(map[string]bool)
			for _, item := range result.Items {
				exportNames[lo.FromPtr(item.Metadata.Name)] = true
			}
			Expect(exportNames).To(HaveKey("filter-export-1"))
			Expect(exportNames).To(HaveKey("filter-export-2"))
			Expect(exportNames).ToNot(HaveKey("filter-export-3"))
			Expect(exportNames).ToNot(HaveKey("filter-export-4"))
		})

		It("should filter ImageExports by imageBuildRef using fieldSelector with in operator", func() {
			// Create ImageBuilds
			build1 := &api.ImageBuild{
				ApiVersion: api.ImageBuildAPIVersion,
				Kind:       string(api.ResourceKindImageBuild),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("in-filter-build-1"),
				},
				Spec: api.ImageBuildSpec{
					Source: api.ImageBuildSource{
						Repository: "input-registry",
						ImageName:  "input-image",
						ImageTag:   "v1.0",
					},
					Destination: api.ImageBuildDestination{
						Repository: "output-registry",
						ImageName:  "output-image",
						ImageTag:   "v1.0",
					},
				},
			}
			_, err := storeInst.ImageBuild().Create(ctx, orgId, build1)
			Expect(err).ToNot(HaveOccurred())

			build2 := &api.ImageBuild{
				ApiVersion: api.ImageBuildAPIVersion,
				Kind:       string(api.ResourceKindImageBuild),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("in-filter-build-2"),
				},
				Spec: api.ImageBuildSpec{
					Source: api.ImageBuildSource{
						Repository: "input-registry",
						ImageName:  "input-image",
						ImageTag:   "v1.0",
					},
					Destination: api.ImageBuildDestination{
						Repository: "output-registry",
						ImageName:  "output-image",
						ImageTag:   "v1.0",
					},
				},
			}
			_, err = storeInst.ImageBuild().Create(ctx, orgId, build2)
			Expect(err).ToNot(HaveOccurred())

			build3 := &api.ImageBuild{
				ApiVersion: api.ImageBuildAPIVersion,
				Kind:       string(api.ResourceKindImageBuild),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("in-filter-build-3"),
				},
				Spec: api.ImageBuildSpec{
					Source: api.ImageBuildSource{
						Repository: "input-registry",
						ImageName:  "input-image",
						ImageTag:   "v1.0",
					},
					Destination: api.ImageBuildDestination{
						Repository: "output-registry",
						ImageName:  "output-image",
						ImageTag:   "v1.0",
					},
				},
			}
			_, err = storeInst.ImageBuild().Create(ctx, orgId, build3)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExports that reference different builds
			export1 := newTestImageExportWithImageBuildRef("in-filter-export-1", "in-filter-build-1")
			_, err = storeInst.ImageExport().Create(ctx, orgId, export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := newTestImageExportWithImageBuildRef("in-filter-export-2", "in-filter-build-2")
			_, err = storeInst.ImageExport().Create(ctx, orgId, export2)
			Expect(err).ToNot(HaveOccurred())

			export3 := newTestImageExportWithImageBuildRef("in-filter-export-3", "in-filter-build-3")
			_, err = storeInst.ImageExport().Create(ctx, orgId, export3)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExport with ImageReferenceSource (should not be included)
			export4 := newTestImageExport("in-filter-export-4")
			_, err = storeInst.ImageExport().Create(ctx, orgId, export4)
			Expect(err).ToNot(HaveOccurred())

			// Filter by build1 and build2 using in operator - should return export1 and export2
			fieldSelector, err := selector.NewFieldSelector("spec.source.imageBuildRef in (in-filter-build-1,in-filter-build-2)")
			Expect(err).ToNot(HaveOccurred())
			listParams := flightctlstore.ListParams{
				Limit:         1000,
				FieldSelector: fieldSelector,
			}
			result, err := storeInst.ImageExport().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(2))

			exportNames := make(map[string]bool)
			for _, item := range result.Items {
				exportNames[lo.FromPtr(item.Metadata.Name)] = true
			}
			Expect(exportNames).To(HaveKey("in-filter-export-1"))
			Expect(exportNames).To(HaveKey("in-filter-export-2"))
			Expect(exportNames).ToNot(HaveKey("in-filter-export-3"))
			Expect(exportNames).ToNot(HaveKey("in-filter-export-4"))
		})

		It("should respect org isolation when filtering ImageExports by imageBuildRef", func() {
			// Create ImageBuild in first org
			build1 := &api.ImageBuild{
				ApiVersion: api.ImageBuildAPIVersion,
				Kind:       string(api.ResourceKindImageBuild),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("org-isolation-build"),
				},
				Spec: api.ImageBuildSpec{
					Source: api.ImageBuildSource{
						Repository: "input-registry",
						ImageName:  "input-image",
						ImageTag:   "v1.0",
					},
					Destination: api.ImageBuildDestination{
						Repository: "output-registry",
						ImageName:  "output-image",
						ImageTag:   "v1.0",
					},
				},
			}
			_, err := storeInst.ImageBuild().Create(ctx, orgId, build1)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExport in first org
			export1 := newTestImageExportWithImageBuildRef("org-isolation-export", "org-isolation-build")
			_, err = storeInst.ImageExport().Create(ctx, orgId, export1)
			Expect(err).ToNot(HaveOccurred())

			// Create another org
			otherOrgId := uuid.New()
			err = testutilpkg.CreateTestOrganization(ctx, mainStoreInst, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageBuild with same name in second org
			build2 := &api.ImageBuild{
				ApiVersion: api.ImageBuildAPIVersion,
				Kind:       string(api.ResourceKindImageBuild),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("org-isolation-build"),
				},
				Spec: api.ImageBuildSpec{
					Source: api.ImageBuildSource{
						Repository: "input-registry",
						ImageName:  "input-image",
						ImageTag:   "v1.0",
					},
					Destination: api.ImageBuildDestination{
						Repository: "output-registry",
						ImageName:  "output-image",
						ImageTag:   "v1.0",
					},
				},
			}
			_, err = storeInst.ImageBuild().Create(ctx, otherOrgId, build2)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExport in second org
			export2 := newTestImageExportWithImageBuildRef("org-isolation-export-2", "org-isolation-build")
			_, err = storeInst.ImageExport().Create(ctx, otherOrgId, export2)
			Expect(err).ToNot(HaveOccurred())

			// Filter in first org - should only return export from first org
			listParams := flightctlstore.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.source.imageBuildRef": "org-isolation-build"}),
			}
			result, err := storeInst.ImageExport().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(1))
			Expect(lo.FromPtr(result.Items[0].Metadata.Name)).To(Equal("org-isolation-export"))

			// Filter in second org - should only return export from second org
			result, err = storeInst.ImageExport().List(ctx, otherOrgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(1))
			Expect(lo.FromPtr(result.Items[0].Metadata.Name)).To(Equal("org-isolation-export-2"))
		})

		It("should return empty list when filtering by non-existent imageBuildRef", func() {
			// Create ImageExport with ImageReferenceSource (not referencing any build)
			export1 := newTestImageExport("no-build-export")
			_, err := storeInst.ImageExport().Create(ctx, orgId, export1)
			Expect(err).ToNot(HaveOccurred())

			// Filter by non-existent build - should return empty list
			listParams := flightctlstore.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.source.imageBuildRef": "non-existent-build"}),
			}
			result, err := storeInst.ImageExport().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(0))
		})
	})

	Context("Delete", func() {
		It("should delete an existing ImageExport and return the deleted resource", func() {
			imageExport := newTestImageExport("delete-test")
			created, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).ToNot(HaveOccurred())

			deleted, err := storeInst.ImageExport().Delete(ctx, orgId, "delete-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).ToNot(BeNil())
			Expect(lo.FromPtr(deleted.Metadata.Name)).To(Equal("delete-test"))
			Expect(deleted.Metadata.Generation).To(Equal(created.Metadata.Generation))
			Expect(deleted.Metadata.ResourceVersion).To(Equal(created.Metadata.ResourceVersion))

			_, err = storeInst.ImageExport().Get(ctx, orgId, "delete-test")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return success when deleting non-existent ImageExport (idempotent)", func() {
			// Delete is idempotent - deleting non-existent resource returns success
			deleted, err := storeInst.ImageExport().Delete(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeNil())
		})
	})

	Context("UpdateStatus", func() {
		It("should update status of an existing ImageExport", func() {
			imageExport := newTestImageExport("status-test")
			_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).ToNot(HaveOccurred())

			imageExport.Status = &api.ImageExportStatus{
				Conditions: &[]api.ImageExportCondition{
					{
						Type:    api.ImageExportConditionTypeReady,
						Status:  v1beta1.ConditionStatusUnknown,
						Reason:  string(api.ImageExportConditionReasonConverting),
						Message: "Export in progress",
					},
				},
			}

			result, err := storeInst.ImageExport().UpdateStatus(ctx, orgId, imageExport)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Status).ToNot(BeNil())
			Expect(len(*result.Status.Conditions)).To(Equal(1))
			Expect((*result.Status.Conditions)[0].Type).To(Equal(api.ImageExportConditionTypeReady))
			Expect((*result.Status.Conditions)[0].Status).To(Equal(v1beta1.ConditionStatusUnknown))
			Expect((*result.Status.Conditions)[0].Reason).To(Equal(string(api.ImageExportConditionReasonConverting)))
			Expect((*result.Status.Conditions)[0].Message).To(Equal("Export in progress"))
		})

		It("should return not found when updating status of non-existent ImageExport", func() {
			imageExport := newTestImageExport("nonexistent")
			imageExport.Status = &api.ImageExportStatus{}

			_, err := storeInst.ImageExport().UpdateStatus(ctx, orgId, imageExport)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})
	})

	Context("UpdateLastSeen", func() {
		It("should update lastSeen timestamp", func() {
			imageExport := newTestImageExport("lastseen-test")
			_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).ToNot(HaveOccurred())

			now := time.Now().UTC()
			err = storeInst.ImageExport().UpdateLastSeen(ctx, orgId, "lastseen-test", now)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImageExport().Get(ctx, orgId, "lastseen-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Status).ToNot(BeNil())
			Expect(result.Status.LastSeen).ToNot(BeNil())
		})

		It("should return not found for non-existent ImageExport", func() {
			err := storeInst.ImageExport().UpdateLastSeen(ctx, orgId, "nonexistent", time.Now())
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})

})
