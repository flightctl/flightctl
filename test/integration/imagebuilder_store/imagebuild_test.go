package imagebuilder_store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/testutil"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutilpkg "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var (
	suiteCtx context.Context
)

func TestImageBuilderStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ImageBuilder Store Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutilpkg.InitSuiteTracerForGinkgo("ImageBuilder Store Suite")
})

func newTestImageBuild(name string) *api.ImageBuild {
	return &api.ImageBuild{
		ApiVersion: api.ImageBuildAPIVersion,
		Kind:       string(api.ResourceKindImageBuild),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
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
}

var _ = Describe("ImageBuildStore", func() {
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
		It("should create an ImageBuild successfully", func() {
			imageBuild := newTestImageBuild("test-build-1")
			result, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)

			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.Metadata.Name)).To(Equal("test-build-1"))
			Expect(result.Metadata.CreationTimestamp).ToNot(BeNil())
			Expect(result.Metadata.Generation).ToNot(BeNil())
			Expect(*result.Metadata.Generation).To(Equal(int64(1)))
			Expect(result.Metadata.ResourceVersion).ToNot(BeNil())
			Expect(*result.Metadata.ResourceVersion).ToNot(BeEmpty())
		})

		It("should fail to create duplicate ImageBuild", func() {
			imageBuild := newTestImageBuild("duplicate-build")

			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			_, err = storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrDuplicateName))
		})

		It("should fail to create ImageBuild with nil name", func() {
			imageBuild := newTestImageBuild("test")
			imageBuild.Metadata.Name = nil

			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("Get", func() {
		It("should get an existing ImageBuild", func() {
			imageBuild := newTestImageBuild("get-test")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImageBuild().Get(ctx, orgId, "get-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.Metadata.Name)).To(Equal("get-test"))
		})

		It("should return not found for non-existent ImageBuild", func() {
			_, err := storeInst.ImageBuild().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return not found for wrong org", func() {
			imageBuild := newTestImageBuild("org-test")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Create another org for isolation test
			wrongOrgId := uuid.New()
			err = testutilpkg.CreateTestOrganization(ctx, mainStoreInst, wrongOrgId)
			Expect(err).ToNot(HaveOccurred())

			_, err = storeInst.ImageBuild().Get(ctx, wrongOrgId, "org-test")
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should get ImageBuild with ImageExports when withExports=true", func() {
			// Create an ImageBuild
			imageBuild := newTestImageBuild("build-with-exports")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExports that reference the ImageBuild
			export1 := &api.ImageExport{
				ApiVersion: api.ImageExportAPIVersion,
				Kind:       string(api.ResourceKindImageExport),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("export-1"),
				},
				Spec: api.ImageExportSpec{
					Source: func() api.ImageExportSource {
						source := api.ImageExportSource{}
						_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
							Type:          api.ImageBuildRefSourceTypeImageBuild,
							ImageBuildRef: "build-with-exports",
						})
						return source
					}(),
					Format: api.ExportFormatTypeQCOW2,
				},
			}
			_, err = storeInst.ImageExport().Create(ctx, orgId, export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := &api.ImageExport{
				ApiVersion: api.ImageExportAPIVersion,
				Kind:       string(api.ResourceKindImageExport),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("export-2"),
				},
				Spec: api.ImageExportSpec{
					Source: func() api.ImageExportSource {
						source := api.ImageExportSource{}
						_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
							Type:          api.ImageBuildRefSourceTypeImageBuild,
							ImageBuildRef: "build-with-exports",
						})
						return source
					}(),
					Format: api.ExportFormatTypeVMDK,
				},
			}
			_, err = storeInst.ImageExport().Create(ctx, orgId, export2)
			Expect(err).ToNot(HaveOccurred())

			// Get without withExports - should not include ImageExports
			result, err := storeInst.ImageBuild().Get(ctx, orgId, "build-with-exports")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Imageexports).To(BeNil())

			// Get with withExports=true - should include ImageExports
			result, err = storeInst.ImageBuild().Get(ctx, orgId, "build-with-exports", store.GetWithExports(true))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Imageexports).ToNot(BeNil())
			Expect(*result.Imageexports).To(HaveLen(2))

			// Verify the ImageExports are correct
			exportNames := make(map[string]bool)
			for _, export := range *result.Imageexports {
				exportNames[lo.FromPtr(export.Metadata.Name)] = true
			}
			Expect(exportNames).To(HaveKey("export-1"))
			Expect(exportNames).To(HaveKey("export-2"))
		})

		It("should get ImageBuild without ImageExports when withExports=false", func() {
			// Create an ImageBuild
			imageBuild := newTestImageBuild("build-no-exports-flag")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Create an ImageExport that references the ImageBuild
			export1 := &api.ImageExport{
				ApiVersion: api.ImageExportAPIVersion,
				Kind:       string(api.ResourceKindImageExport),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("export-for-flag-test"),
				},
				Spec: api.ImageExportSpec{
					Source: func() api.ImageExportSource {
						source := api.ImageExportSource{}
						_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
							Type:          api.ImageBuildRefSourceTypeImageBuild,
							ImageBuildRef: "build-no-exports-flag",
						})
						return source
					}(),
					Format: api.ExportFormatTypeQCOW2,
				},
			}
			_, err = storeInst.ImageExport().Create(ctx, orgId, export1)
			Expect(err).ToNot(HaveOccurred())

			// Get with withExports=false - should not include ImageExports
			result, err := storeInst.ImageBuild().Get(ctx, orgId, "build-no-exports-flag", store.GetWithExports(false))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Imageexports).To(BeNil())
		})
	})

	Context("List", func() {
		It("should list all ImageBuilds", func() {
			for i := 0; i < 3; i++ {
				imageBuild := newTestImageBuild(string(rune('a'+i)) + "-build")
				_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
				Expect(err).ToNot(HaveOccurred())
			}

			result, err := storeInst.ImageBuild().List(ctx, orgId, flightctlstore.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(HaveLen(3))
		})

		It("should list with limit", func() {
			for i := 0; i < 5; i++ {
				imageBuild := newTestImageBuild(string(rune('a'+i)) + "-build")
				_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
				Expect(err).ToNot(HaveOccurred())
			}

			result, err := storeInst.ImageBuild().List(ctx, orgId, flightctlstore.ListParams{Limit: 2})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(HaveLen(2))
		})

		It("should not list ImageBuilds from other orgs", func() {
			imageBuild := newTestImageBuild("org-isolation-test")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Create another org for isolation test
			otherOrgId := uuid.New()
			err = testutilpkg.CreateTestOrganization(ctx, mainStoreInst, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImageBuild().List(ctx, otherOrgId, flightctlstore.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(0))
		})

		It("should list ImageBuilds with ImageExports when withExports=true", func() {
			// Create multiple ImageBuilds
			build1 := newTestImageBuild("list-build-1")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, build1)
			Expect(err).ToNot(HaveOccurred())

			build2 := newTestImageBuild("list-build-2")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, build2)
			Expect(err).ToNot(HaveOccurred())

			build3 := newTestImageBuild("list-build-3")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, build3)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExports that reference the ImageBuilds
			export1 := &api.ImageExport{
				ApiVersion: api.ImageExportAPIVersion,
				Kind:       string(api.ResourceKindImageExport),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("list-export-1"),
				},
				Spec: api.ImageExportSpec{
					Source: func() api.ImageExportSource {
						source := api.ImageExportSource{}
						_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
							Type:          api.ImageBuildRefSourceTypeImageBuild,
							ImageBuildRef: "list-build-1",
						})
						return source
					}(),
					Format: api.ExportFormatTypeQCOW2,
				},
			}
			_, err = storeInst.ImageExport().Create(ctx, orgId, export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := &api.ImageExport{
				ApiVersion: api.ImageExportAPIVersion,
				Kind:       string(api.ResourceKindImageExport),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("list-export-2"),
				},
				Spec: api.ImageExportSpec{
					Source: func() api.ImageExportSource {
						source := api.ImageExportSource{}
						_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
							Type:          api.ImageBuildRefSourceTypeImageBuild,
							ImageBuildRef: "list-build-1",
						})
						return source
					}(),
					Format: api.ExportFormatTypeVMDK,
				},
			}
			_, err = storeInst.ImageExport().Create(ctx, orgId, export2)
			Expect(err).ToNot(HaveOccurred())

			export3 := &api.ImageExport{
				ApiVersion: api.ImageExportAPIVersion,
				Kind:       string(api.ResourceKindImageExport),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("list-export-3"),
				},
				Spec: api.ImageExportSpec{
					Source: func() api.ImageExportSource {
						source := api.ImageExportSource{}
						_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
							Type:          api.ImageBuildRefSourceTypeImageBuild,
							ImageBuildRef: "list-build-2",
						})
						return source
					}(),
					Format: api.ExportFormatTypeQCOW2,
				},
			}
			_, err = storeInst.ImageExport().Create(ctx, orgId, export3)
			Expect(err).ToNot(HaveOccurred())

			// List without withExports - should not include ImageExports
			result, err := storeInst.ImageBuild().List(ctx, orgId, flightctlstore.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(HaveLen(3))
			for _, item := range result.Items {
				Expect(item.Imageexports).To(BeNil())
			}

			// List with withExports=true - should include ImageExports
			result, err = storeInst.ImageBuild().List(ctx, orgId, flightctlstore.ListParams{}, store.ListWithExports(true))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(HaveLen(3))

			// Verify ImageExports are attached correctly
			build1Found := false
			build2Found := false
			build3Found := false
			for _, item := range result.Items {
				name := lo.FromPtr(item.Metadata.Name)
				switch name {
				case "list-build-1":
					build1Found = true
					Expect(item.Imageexports).ToNot(BeNil())
					Expect(*item.Imageexports).To(HaveLen(2))
				case "list-build-2":
					build2Found = true
					Expect(item.Imageexports).ToNot(BeNil())
					Expect(*item.Imageexports).To(HaveLen(1))
				case "list-build-3":
					build3Found = true
					Expect(item.Imageexports).To(BeNil()) // No exports for build-3
				}
			}
			Expect(build1Found).To(BeTrue())
			Expect(build2Found).To(BeTrue())
			Expect(build3Found).To(BeTrue())
		})
	})

	Context("Delete", func() {
		It("should delete an existing ImageBuild and return the deleted resource", func() {
			imageBuild := newTestImageBuild("delete-test")
			created, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			deleted, err := storeInst.ImageBuild().Delete(ctx, orgId, "delete-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).ToNot(BeNil())
			Expect(lo.FromPtr(deleted.Metadata.Name)).To(Equal("delete-test"))
			Expect(deleted.Metadata.Generation).To(Equal(created.Metadata.Generation))
			Expect(deleted.Metadata.ResourceVersion).To(Equal(created.Metadata.ResourceVersion))

			_, err = storeInst.ImageBuild().Get(ctx, orgId, "delete-test")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return success when deleting non-existent ImageBuild (idempotent)", func() {
			// Delete is idempotent - deleting non-existent resource returns success
			deleted, err := storeInst.ImageBuild().Delete(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeNil())
		})

		It("should delete related ImageExports when deleting an ImageBuild (cascading delete)", func() {
			// Create an ImageBuild
			imageBuild := newTestImageBuild("cascade-delete-test")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExports that reference the ImageBuild (with Owner field set for cascading delete)
			export1 := &api.ImageExport{
				ApiVersion: api.ImageExportAPIVersion,
				Kind:       string(api.ResourceKindImageExport),
				Metadata: v1beta1.ObjectMeta{
					Name:  lo.ToPtr("cascade-export-1"),
					Owner: util.SetResourceOwner(string(api.ResourceKindImageBuild), "cascade-delete-test"),
				},
				Spec: api.ImageExportSpec{
					Source: func() api.ImageExportSource {
						source := api.ImageExportSource{}
						_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
							Type:          api.ImageBuildRefSourceTypeImageBuild,
							ImageBuildRef: "cascade-delete-test",
						})
						return source
					}(),
					Format: api.ExportFormatTypeQCOW2,
				},
			}
			_, err = storeInst.ImageExport().Create(ctx, orgId, export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := &api.ImageExport{
				ApiVersion: api.ImageExportAPIVersion,
				Kind:       string(api.ResourceKindImageExport),
				Metadata: v1beta1.ObjectMeta{
					Name:  lo.ToPtr("cascade-export-2"),
					Owner: util.SetResourceOwner(string(api.ResourceKindImageBuild), "cascade-delete-test"),
				},
				Spec: api.ImageExportSpec{
					Source: func() api.ImageExportSource {
						source := api.ImageExportSource{}
						_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
							Type:          api.ImageBuildRefSourceTypeImageBuild,
							ImageBuildRef: "cascade-delete-test",
						})
						return source
					}(),
					Format: api.ExportFormatTypeVMDK,
				},
			}
			_, err = storeInst.ImageExport().Create(ctx, orgId, export2)
			Expect(err).ToNot(HaveOccurred())

			// Create an unrelated ImageExport (should NOT be deleted - different owner)
			unrelatedBuild := newTestImageBuild("unrelated-build")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, unrelatedBuild)
			Expect(err).ToNot(HaveOccurred())

			unrelatedExport := &api.ImageExport{
				ApiVersion: api.ImageExportAPIVersion,
				Kind:       string(api.ResourceKindImageExport),
				Metadata: v1beta1.ObjectMeta{
					Name:  lo.ToPtr("unrelated-export"),
					Owner: util.SetResourceOwner(string(api.ResourceKindImageBuild), "unrelated-build"),
				},
				Spec: api.ImageExportSpec{
					Source: func() api.ImageExportSource {
						source := api.ImageExportSource{}
						_ = source.FromImageBuildRefSource(api.ImageBuildRefSource{
							Type:          api.ImageBuildRefSourceTypeImageBuild,
							ImageBuildRef: "unrelated-build",
						})
						return source
					}(),
					Format: api.ExportFormatTypeQCOW2,
				},
			}
			_, err = storeInst.ImageExport().Create(ctx, orgId, unrelatedExport)
			Expect(err).ToNot(HaveOccurred())

			// Verify all exports exist before delete
			_, err = storeInst.ImageExport().Get(ctx, orgId, "cascade-export-1")
			Expect(err).ToNot(HaveOccurred())
			_, err = storeInst.ImageExport().Get(ctx, orgId, "cascade-export-2")
			Expect(err).ToNot(HaveOccurred())
			_, err = storeInst.ImageExport().Get(ctx, orgId, "unrelated-export")
			Expect(err).ToNot(HaveOccurred())

			// Delete the ImageBuild - should cascade delete related exports
			deleted, err := storeInst.ImageBuild().Delete(ctx, orgId, "cascade-delete-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).ToNot(BeNil())

			// Verify ImageBuild is deleted
			_, err = storeInst.ImageBuild().Get(ctx, orgId, "cascade-delete-test")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))

			// Verify related ImageExports are deleted
			_, err = storeInst.ImageExport().Get(ctx, orgId, "cascade-export-1")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
			_, err = storeInst.ImageExport().Get(ctx, orgId, "cascade-export-2")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))

			// Verify unrelated ImageExport is NOT deleted
			_, err = storeInst.ImageExport().Get(ctx, orgId, "unrelated-export")
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("UpdateStatus", func() {
		It("should update status of an existing ImageBuild", func() {
			imageBuild := newTestImageBuild("status-test")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			imageBuild.Status = &api.ImageBuildStatus{
				Conditions: &[]api.ImageBuildCondition{
					{
						Type:    api.ImageBuildConditionTypeReady,
						Status:  v1beta1.ConditionStatusUnknown,
						Reason:  string(api.ImageBuildConditionReasonBuilding),
						Message: "Build in progress",
					},
				},
			}

			result, err := storeInst.ImageBuild().UpdateStatus(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Status).ToNot(BeNil())
			Expect(len(*result.Status.Conditions)).To(Equal(1))
			Expect((*result.Status.Conditions)[0].Type).To(Equal(api.ImageBuildConditionTypeReady))
			Expect((*result.Status.Conditions)[0].Status).To(Equal(v1beta1.ConditionStatusUnknown))
			Expect((*result.Status.Conditions)[0].Reason).To(Equal(string(api.ImageBuildConditionReasonBuilding)))
			Expect((*result.Status.Conditions)[0].Message).To(Equal("Build in progress"))
		})

		It("should return not found when updating status of non-existent ImageBuild", func() {
			imageBuild := newTestImageBuild("nonexistent")
			imageBuild.Status = &api.ImageBuildStatus{}

			_, err := storeInst.ImageBuild().UpdateStatus(ctx, orgId, imageBuild)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})
	})

	Context("UpdateLastSeen", func() {
		It("should update lastSeen timestamp", func() {
			imageBuild := newTestImageBuild("lastseen-test")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			now := time.Now().UTC()
			err = storeInst.ImageBuild().UpdateLastSeen(ctx, orgId, "lastseen-test", now)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImageBuild().Get(ctx, orgId, "lastseen-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Status).ToNot(BeNil())
			Expect(result.Status.LastSeen).ToNot(BeNil())
		})

		It("should return not found for non-existent ImageBuild", func() {
			err := storeInst.ImageBuild().UpdateLastSeen(ctx, orgId, "nonexistent", time.Now())
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})
})
