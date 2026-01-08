package imagebuilder_service_test

import (
	"context"
	"os"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
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

var (
	suiteCtx context.Context
)

func TestImageBuilderService(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ImageBuilder Service Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutilpkg.InitSuiteTracerForGinkgo("ImageBuilder Service Suite")
})

func newTestImageBuild(name string) api.ImageBuild {
	return api.ImageBuild{
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
				Tag:        "v1.0",
			},
		},
	}
}

func newTestImageExport(name string) api.ImageExport {
	return api.ImageExport{
		ApiVersion: api.ImageExportAPIVersion,
		Kind:       string(api.ResourceKindImageExport),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImageExportSpec{
			Source: api.ImageExportSource{}, // Will be overridden
			Destination: api.ImageExportDestination{
				Repository: "export-registry",
				ImageName:  "export-image",
				Tag:        "v1.0",
			},
			Format: api.ExportFormatTypeQCOW2,
		},
	}
}

var _ = Describe("ImagePipelineService", func() {
	var (
		log           *logrus.Logger
		ctx           context.Context
		orgId         uuid.UUID
		storeInst     store.Store
		mainStoreInst flightctlstore.Store
		cfg           *config.Config
		dbName        string
		db            *gorm.DB
		svc           service.ImagePipelineService
	)

	BeforeEach(func() {
		ctx = testutilpkg.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()

		// Use main store's PrepareDBForUnitTests which includes organizations table
		mainStoreInst, cfg, dbName, db = flightctlstore.PrepareDBForUnitTests(ctx, log)

		// Create imagebuilder store on the same db connection
		storeInst = store.NewStore(db, log.WithField("pkg", "imagebuilder-store"))

		// Run imagebuilder-specific migrations only for local strategy
		strategy := os.Getenv("FLIGHTCTL_TEST_DB_STRATEGY")
		if strategy != testutil.StrategyTemplate {
			err := storeInst.RunMigrations(ctx)
			Expect(err).ToNot(HaveOccurred())
		}

		// Create services
		imageBuildSvc := service.NewImageBuildService(storeInst.ImageBuild(), log)
		imageExportSvc := service.NewImageExportService(storeInst.ImageExport(), storeInst.ImageBuild(), log)
		svc = service.NewImagePipelineService(storeInst.ImagePipeline(), imageBuildSvc, imageExportSvc, storeInst.ImageBuild(), storeInst.ImageExport(), log)

		// Create test organization (required for foreign key constraint)
		orgId = uuid.New()
		err := testutilpkg.CreateTestOrganization(ctx, mainStoreInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		flightctlstore.DeleteTestDB(ctx, log, cfg, mainStoreInst, dbName)
	})

	Context("Create with both resources", func() {
		It("should create ImageBuild and ImageExports atomically", func() {
			imageExports := []api.ImageExport{
				newTestImageExport("atomic-export-1"),
				newTestImageExport("atomic-export-2"),
			}
			req := api.ImagePipelineRequest{
				ImageBuild:   newTestImageBuild("atomic-build"),
				ImageExports: &imageExports,
			}

			result, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.ImageBuild.Metadata.Name)).To(Equal("atomic-build"))
			Expect(result.ImageExports).ToNot(BeNil())
			Expect(len(*result.ImageExports)).To(Equal(2))

			// Verify ImageExports source references ImageBuild
			for i := range *result.ImageExports {
				source, err := (*result.ImageExports)[i].Spec.Source.AsImageBuildRefSource()
				Expect(err).ToNot(HaveOccurred())
				Expect(source.ImageBuildRef).To(Equal("atomic-build"))
			}

			// Verify all exist in the database
			_, err := storeInst.ImageBuild().Get(ctx, orgId, "atomic-build")
			Expect(err).ToNot(HaveOccurred())
			_, err = storeInst.ImageExport().Get(ctx, orgId, "atomic-export-1")
			Expect(err).ToNot(HaveOccurred())
			_, err = storeInst.ImageExport().Get(ctx, orgId, "atomic-export-2")
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("Create with only ImageBuild", func() {
		It("should create only ImageBuild when ImageExports is nil", func() {
			req := api.ImagePipelineRequest{
				ImageBuild: newTestImageBuild("only-build"),
			}

			result, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.ImageBuild.Metadata.Name)).To(Equal("only-build"))
			Expect(result.ImageExports).To(BeNil())

			// Verify ImageBuild exists
			_, err := storeInst.ImageBuild().Get(ctx, orgId, "only-build")
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("Rollback on validation failure", func() {
		It("should rollback when ImageExport validation fails", func() {
			invalidExport := newTestImageExport("invalid-export")
			invalidExport.Spec.Format = "" // Invalid - formats required
			imageExports := []api.ImageExport{invalidExport}

			req := api.ImagePipelineRequest{
				ImageBuild:   newTestImageBuild("rollback-build"),
				ImageExports: &imageExports,
			}

			_, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(400)))

			// Verify both were rolled back - neither should exist
			_, err := storeInst.ImageBuild().Get(ctx, orgId, "rollback-build")
			Expect(err).To(HaveOccurred())

			_, err = storeInst.ImageExport().Get(ctx, orgId, "invalid-export")
			Expect(err).To(HaveOccurred())
		})

		It("should rollback when ImageBuild validation fails", func() {
			invalidBuild := newTestImageBuild("invalid-build")
			invalidBuild.Spec.Source.Repository = "" // Invalid - repository required
			imageExports := []api.ImageExport{newTestImageExport("valid-export")}

			req := api.ImagePipelineRequest{
				ImageBuild:   invalidBuild,
				ImageExports: &imageExports,
			}

			_, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(400)))

			// Verify neither was created
			_, err := storeInst.ImageBuild().Get(ctx, orgId, "invalid-build")
			Expect(err).To(HaveOccurred())

			_, err = storeInst.ImageExport().Get(ctx, orgId, "valid-export")
			Expect(err).To(HaveOccurred())
		})
	})

	Context("Rollback on duplicate", func() {
		It("should rollback when ImageExport name already exists", func() {
			// Pre-create an ImageExport
			existingExport := newTestImageExport("existing-export")
			existingSource := api.ImageExportSource{}
			_ = existingSource.FromImageReferenceSource(api.ImageReferenceSource{
				Type:       api.ImageReference,
				Repository: "source-registry",
				ImageName:  "source-image",
				ImageTag:   "v1.0",
			})
			existingExport.Spec.Source = existingSource
			_, err := storeInst.ImageExport().Create(ctx, orgId, &existingExport)
			Expect(err).ToNot(HaveOccurred())

			// Try to create with duplicate export name
			imageExports := []api.ImageExport{newTestImageExport("existing-export")} // Duplicate!
			req := api.ImagePipelineRequest{
				ImageBuild:   newTestImageBuild("new-build-dup-export"),
				ImageExports: &imageExports,
			}

			_, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(409)))

			// Verify the ImageBuild was rolled back
			_, err = storeInst.ImageBuild().Get(ctx, orgId, "new-build-dup-export")
			Expect(err).To(HaveOccurred())
		})

		It("should fail when ImageBuild name already exists", func() {
			// Pre-create an ImageBuild
			existingBuild := newTestImageBuild("existing-build")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &existingBuild)
			Expect(err).ToNot(HaveOccurred())

			// Try to create with duplicate build name
			imageExports := []api.ImageExport{newTestImageExport("new-export-dup-build")}
			req := api.ImagePipelineRequest{
				ImageBuild:   newTestImageBuild("existing-build"), // Duplicate!
				ImageExports: &imageExports,
			}

			_, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(409)))

			// Verify the ImageExport was not created
			_, err = storeInst.ImageExport().Get(ctx, orgId, "new-export-dup-build")
			Expect(err).To(HaveOccurred())
		})
	})

	Context("Source override", func() {
		It("should override ImageExport source to reference the ImageBuild", func() {
			// Create an ImageExport with an imageReference source
			export := newTestImageExport("override-export")
			originalSource := api.ImageExportSource{}
			_ = originalSource.FromImageReferenceSource(api.ImageReferenceSource{
				Type:       api.ImageReference,
				Repository: "original-registry",
				ImageName:  "original-image",
				ImageTag:   "v1.0",
			})
			export.Spec.Source = originalSource

			imageExports := []api.ImageExport{export}
			req := api.ImagePipelineRequest{
				ImageBuild:   newTestImageBuild("override-build"),
				ImageExports: &imageExports,
			}

			result, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(201)))
			Expect(result.ImageExports).ToNot(BeNil())
			Expect(len(*result.ImageExports)).To(Equal(1))

			// Verify the source was overridden to imageBuild type
			sourceType, err := (*result.ImageExports)[0].Spec.Source.Discriminator()
			Expect(err).ToNot(HaveOccurred())
			Expect(sourceType).To(Equal(string(api.ImageExportSourceTypeImageBuild)))

			source, err := (*result.ImageExports)[0].Spec.Source.AsImageBuildRefSource()
			Expect(err).ToNot(HaveOccurred())
			Expect(source.ImageBuildRef).To(Equal("override-build"))
		})
	})

	Context("Get ImagePipeline", func() {
		It("should get ImageBuild with associated ImageExports", func() {
			// Create ImageBuild
			build := newTestImageBuild("get-build")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &build)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExports that reference the ImageBuild
			export1 := newTestImageExport("get-export-1")
			source1 := api.ImageExportSource{}
			_ = source1.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "get-build",
			})
			export1.Spec.Source = source1
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := newTestImageExport("get-export-2")
			source2 := api.ImageExportSource{}
			_ = source2.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "get-build",
			})
			export2.Spec.Source = source2
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export2)
			Expect(err).ToNot(HaveOccurred())

			// Get the ImagePipeline
			result, status := svc.Get(ctx, orgId, "get-build")

			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.ImageBuild.Metadata.Name)).To(Equal("get-build"))
			Expect(result.ImageExports).ToNot(BeNil())
			Expect(len(*result.ImageExports)).To(Equal(2))

			exportNames := []string{
				lo.FromPtr((*result.ImageExports)[0].Metadata.Name),
				lo.FromPtr((*result.ImageExports)[1].Metadata.Name),
			}
			Expect(exportNames).To(ContainElement("get-export-1"))
			Expect(exportNames).To(ContainElement("get-export-2"))
		})

		It("should return 404 when ImageBuild not found", func() {
			_, status := svc.Get(ctx, orgId, "nonexistent-build")

			Expect(status.Code).To(Equal(int32(404)))
		})

		It("should return empty ImageExports when none exist", func() {
			// Create ImageBuild without exports
			build := newTestImageBuild("no-exports-build")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &build)
			Expect(err).ToNot(HaveOccurred())

			result, status := svc.Get(ctx, orgId, "no-exports-build")

			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.ImageBuild.Metadata.Name)).To(Equal("no-exports-build"))
			Expect(result.ImageExports).To(BeNil())
		})
	})

	Context("List ImagePipelines", func() {
		It("should list ImageBuilds with their associated ImageExports", func() {
			// Create multiple ImageBuilds
			build1 := newTestImageBuild("list-build-1")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &build1)
			Expect(err).ToNot(HaveOccurred())

			build2 := newTestImageBuild("list-build-2")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &build2)
			Expect(err).ToNot(HaveOccurred())

			// Create exports for build-1
			export1 := newTestImageExport("list-export-1")
			source1 := api.ImageExportSource{}
			_ = source1.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "list-build-1",
			})
			export1.Spec.Source = source1
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export1)
			Expect(err).ToNot(HaveOccurred())

			// List ImagePipelines
			result, status := svc.List(ctx, orgId, api.ListImagePipelinesParams{})

			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(len(result.Items)).To(BeNumerically(">=", 2))

			// Find build-1 and build-2 in results
			var foundBuild1, foundBuild2 bool
			for i := range result.Items {
				buildName := lo.FromPtr(result.Items[i].ImageBuild.Metadata.Name)
				if buildName == "list-build-1" {
					foundBuild1 = true
					Expect(result.Items[i].ImageExports).ToNot(BeNil())
					Expect(len(*result.Items[i].ImageExports)).To(Equal(1))
					Expect(lo.FromPtr((*result.Items[i].ImageExports)[0].Metadata.Name)).To(Equal("list-export-1"))
				}
				if buildName == "list-build-2" {
					foundBuild2 = true
					Expect(result.Items[i].ImageExports).To(BeNil())
				}
			}
			Expect(foundBuild1).To(BeTrue())
			Expect(foundBuild2).To(BeTrue())
		})

		It("should return empty list when no ImageBuilds exist", func() {
			result, status := svc.List(ctx, orgId, api.ListImagePipelinesParams{})

			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(BeEmpty())
		})

		It("should filter ImagePipelines by field selector on ImageBuild", func() {
			// Create multiple ImageBuilds
			build1 := newTestImageBuild("filter-build-1")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &build1)
			Expect(err).ToNot(HaveOccurred())

			build2 := newTestImageBuild("filter-build-2")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &build2)
			Expect(err).ToNot(HaveOccurred())

			// Create exports for both builds
			export1 := newTestImageExport("filter-export-1")
			source1 := api.ImageExportSource{}
			_ = source1.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "filter-build-1",
			})
			export1.Spec.Source = source1
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := newTestImageExport("filter-export-2")
			source2 := api.ImageExportSource{}
			_ = source2.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "filter-build-2",
			})
			export2.Spec.Source = source2
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export2)
			Expect(err).ToNot(HaveOccurred())

			// List with field selector to filter by ImageBuild name
			fieldSelector := "metadata.name=filter-build-1"
			result, status := svc.List(ctx, orgId, api.ListImagePipelinesParams{
				FieldSelector: &fieldSelector,
			})

			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(len(result.Items)).To(Equal(1))
			Expect(lo.FromPtr(result.Items[0].ImageBuild.Metadata.Name)).To(Equal("filter-build-1"))
			Expect(result.Items[0].ImageExports).ToNot(BeNil())
			Expect(len(*result.Items[0].ImageExports)).To(Equal(1))
			Expect(lo.FromPtr((*result.Items[0].ImageExports)[0].Metadata.Name)).To(Equal("filter-export-1"))
		})
	})

	Context("Field selector for ImageExports by imageBuildRef", func() {
		It("should filter ImageExports by spec.source.imageBuildRef", func() {
			// Create ImageBuilds
			build1 := newTestImageBuild("filter-export-build-1")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &build1)
			Expect(err).ToNot(HaveOccurred())

			build2 := newTestImageBuild("filter-export-build-2")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &build2)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExports referencing different builds
			export1 := newTestImageExport("filtered-export-1")
			source1 := api.ImageExportSource{}
			_ = source1.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "filter-export-build-1",
			})
			export1.Spec.Source = source1
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := newTestImageExport("filtered-export-2")
			source2 := api.ImageExportSource{}
			_ = source2.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "filter-export-build-2",
			})
			export2.Spec.Source = source2
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export2)
			Expect(err).ToNot(HaveOccurred())

			// List ImageExports filtered by imageBuildRef
			imageExportSvc := service.NewImageExportService(storeInst.ImageExport(), storeInst.ImageBuild(), log)
			fieldSelector := "spec.source.imageBuildRef=filter-export-build-1"
			result, status := imageExportSvc.List(ctx, orgId, api.ListImageExportsParams{
				FieldSelector: &fieldSelector,
			})

			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(len(result.Items)).To(Equal(1))
			Expect(lo.FromPtr(result.Items[0].Metadata.Name)).To(Equal("filtered-export-1"))

			// Verify the source references the correct ImageBuild
			source, err := result.Items[0].Spec.Source.AsImageBuildRefSource()
			Expect(err).ToNot(HaveOccurred())
			Expect(source.ImageBuildRef).To(Equal("filter-export-build-1"))
		})

		It("should filter ImageExports by spec.source.imageBuildRef with IN operator", func() {
			// Create ImageBuilds
			build1 := newTestImageBuild("in-filter-build-1")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &build1)
			Expect(err).ToNot(HaveOccurred())

			build2 := newTestImageBuild("in-filter-build-2")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &build2)
			Expect(err).ToNot(HaveOccurred())

			build3 := newTestImageBuild("in-filter-build-3")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &build3)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageExports referencing different builds
			export1 := newTestImageExport("in-filtered-export-1")
			source1 := api.ImageExportSource{}
			_ = source1.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "in-filter-build-1",
			})
			export1.Spec.Source = source1
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := newTestImageExport("in-filtered-export-2")
			source2 := api.ImageExportSource{}
			_ = source2.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "in-filter-build-2",
			})
			export2.Spec.Source = source2
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export2)
			Expect(err).ToNot(HaveOccurred())

			export3 := newTestImageExport("in-filtered-export-3")
			source3 := api.ImageExportSource{}
			_ = source3.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "in-filter-build-3",
			})
			export3.Spec.Source = source3
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export3)
			Expect(err).ToNot(HaveOccurred())

			// List ImageExports filtered by imageBuildRef using IN operator
			imageExportSvc := service.NewImageExportService(storeInst.ImageExport(), storeInst.ImageBuild(), log)
			fieldSelector := "spec.source.imageBuildRef in (in-filter-build-1,in-filter-build-2)"
			result, status := imageExportSvc.List(ctx, orgId, api.ListImageExportsParams{
				FieldSelector: &fieldSelector,
			})

			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(len(result.Items)).To(Equal(2))

			exportNames := []string{
				lo.FromPtr(result.Items[0].Metadata.Name),
				lo.FromPtr(result.Items[1].Metadata.Name),
			}
			Expect(exportNames).To(ContainElement("in-filtered-export-1"))
			Expect(exportNames).To(ContainElement("in-filtered-export-2"))
			Expect(exportNames).ToNot(ContainElement("in-filtered-export-3"))
		})
	})

	Context("Delete ImagePipeline", func() {
		It("should delete ImagePipeline with associated ImageExports", func() {
			// Create ImagePipeline with ImageBuild and ImageExports
			build := newTestImageBuild("delete-build")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &build)
			Expect(err).ToNot(HaveOccurred())

			export1 := newTestImageExport("delete-export-1")
			source1 := api.ImageExportSource{}
			_ = source1.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "delete-build",
			})
			export1.Spec.Source = source1
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export1)
			Expect(err).ToNot(HaveOccurred())

			export2 := newTestImageExport("delete-export-2")
			source2 := api.ImageExportSource{}
			_ = source2.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "delete-build",
			})
			export2.Spec.Source = source2
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export2)
			Expect(err).ToNot(HaveOccurred())

			// Verify they exist
			result, status := svc.Get(ctx, orgId, "delete-build")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(len(*result.ImageExports)).To(Equal(2))

			// Delete the ImagePipeline
			result, status = svc.Delete(ctx, orgId, "delete-build")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.ImageBuild.Metadata.Name)).To(Equal("delete-build"))
			Expect(len(*result.ImageExports)).To(Equal(2))

			// Verify ImageBuild is deleted
			_, err = storeInst.ImageBuild().Get(ctx, orgId, "delete-build")
			Expect(err).To(HaveOccurred())

			// Verify ImageExports are deleted
			_, err = storeInst.ImageExport().Get(ctx, orgId, "delete-export-1")
			Expect(err).To(HaveOccurred())
			_, err = storeInst.ImageExport().Get(ctx, orgId, "delete-export-2")
			Expect(err).To(HaveOccurred())
		})

		It("should delete ImagePipeline with no ImageExports", func() {
			// Create ImageBuild without exports
			build := newTestImageBuild("delete-build-only")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &build)
			Expect(err).ToNot(HaveOccurred())

			// Verify it exists
			result, status := svc.Get(ctx, orgId, "delete-build-only")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())

			// Delete the ImagePipeline
			result, status = svc.Delete(ctx, orgId, "delete-build-only")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.ImageBuild.Metadata.Name)).To(Equal("delete-build-only"))
			Expect(result.ImageExports).To(BeNil())

			// Verify ImageBuild is deleted
			_, err = storeInst.ImageBuild().Get(ctx, orgId, "delete-build-only")
			Expect(err).To(HaveOccurred())
		})

		It("should return 404 when ImagePipeline not found", func() {
			_, status := svc.Delete(ctx, orgId, "nonexistent-build")
			Expect(status.Code).To(Equal(int32(404)))
		})

		It("should delete ImageExports atomically with ImageBuild in transaction", func() {
			// Create ImagePipeline
			build := newTestImageBuild("atomic-delete-build")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &build)
			Expect(err).ToNot(HaveOccurred())

			export1 := newTestImageExport("atomic-delete-export-1")
			source1 := api.ImageExportSource{}
			_ = source1.FromImageBuildRefSource(api.ImageBuildRefSource{
				Type:          api.ImageBuildRefSourceTypeImageBuild,
				ImageBuildRef: "atomic-delete-build",
			})
			export1.Spec.Source = source1
			_, err = storeInst.ImageExport().Create(ctx, orgId, &export1)
			Expect(err).ToNot(HaveOccurred())

			// Delete should remove both atomically
			// If ImageExport delete fails, ImageBuild should not be deleted
			// (This test verifies the transaction works - in a real failure scenario,
			// we'd need to simulate a failure, but for now we just verify both are deleted)
			result, status := svc.Delete(ctx, orgId, "atomic-delete-build")
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())

			// Both should be gone
			_, err = storeInst.ImageBuild().Get(ctx, orgId, "atomic-delete-build")
			Expect(err).To(HaveOccurred())
			_, err = storeInst.ImageExport().Get(ctx, orgId, "atomic-delete-export-1")
			Expect(err).To(HaveOccurred())
		})
	})
})
