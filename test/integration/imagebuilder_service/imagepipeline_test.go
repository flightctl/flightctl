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
		Kind:       api.ImageBuildKind,
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
		Kind:       api.ImageExportKind,
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
		svc = service.NewImagePipelineService(storeInst.ImagePipeline(), imageBuildSvc, imageExportSvc, log)

		// Create test organization (required for foreign key constraint)
		orgId = uuid.New()
		err := testutilpkg.CreateTestOrganization(ctx, mainStoreInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		flightctlstore.DeleteTestDB(ctx, log, cfg, mainStoreInst, dbName)
	})

	Context("Create with both resources", func() {
		It("should create ImageBuild and ImageExport atomically", func() {
			req := api.ImagePipelineRequest{
				ImageBuild:  newTestImageBuild("atomic-build"),
				ImageExport: lo.ToPtr(newTestImageExport("atomic-export")),
			}

			result, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.ImageBuild.Metadata.Name)).To(Equal("atomic-build"))
			Expect(result.ImageExport).ToNot(BeNil())
			Expect(lo.FromPtr(result.ImageExport.Metadata.Name)).To(Equal("atomic-export"))

			// Verify ImageExport source references ImageBuild
			source, err := result.ImageExport.Spec.Source.AsImageBuildRefSource()
			Expect(err).ToNot(HaveOccurred())
			Expect(source.ImageBuildRef).To(Equal("atomic-build"))

			// Verify both exist in the database
			_, err = storeInst.ImageBuild().Get(ctx, orgId, "atomic-build")
			Expect(err).ToNot(HaveOccurred())
			_, err = storeInst.ImageExport().Get(ctx, orgId, "atomic-export")
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("Create with only ImageBuild", func() {
		It("should create only ImageBuild when ImageExport is nil", func() {
			req := api.ImagePipelineRequest{
				ImageBuild:  newTestImageBuild("only-build"),
				ImageExport: nil,
			}

			result, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(201)))
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.ImageBuild.Metadata.Name)).To(Equal("only-build"))
			Expect(result.ImageExport).To(BeNil())

			// Verify ImageBuild exists
			_, err := storeInst.ImageBuild().Get(ctx, orgId, "only-build")
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("Rollback on validation failure", func() {
		It("should rollback when ImageExport validation fails", func() {
			invalidExport := newTestImageExport("invalid-export")
			invalidExport.Spec.Format = "" // Invalid - formats required

			req := api.ImagePipelineRequest{
				ImageBuild:  newTestImageBuild("rollback-build"),
				ImageExport: &invalidExport,
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

			req := api.ImagePipelineRequest{
				ImageBuild:  invalidBuild,
				ImageExport: lo.ToPtr(newTestImageExport("valid-export")),
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
			req := api.ImagePipelineRequest{
				ImageBuild:  newTestImageBuild("new-build-dup-export"),
				ImageExport: lo.ToPtr(newTestImageExport("existing-export")), // Duplicate!
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
			req := api.ImagePipelineRequest{
				ImageBuild:  newTestImageBuild("existing-build"), // Duplicate!
				ImageExport: lo.ToPtr(newTestImageExport("new-export-dup-build")),
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

			req := api.ImagePipelineRequest{
				ImageBuild:  newTestImageBuild("override-build"),
				ImageExport: &export,
			}

			result, status := svc.Create(ctx, orgId, req)

			Expect(status.Code).To(Equal(int32(201)))
			Expect(result.ImageExport).ToNot(BeNil())

			// Verify the source was overridden to imageBuild type
			sourceType, err := result.ImageExport.Spec.Source.Discriminator()
			Expect(err).ToNot(HaveOccurred())
			Expect(sourceType).To(Equal(string(api.ImageExportSourceTypeImageBuild)))

			source, err := result.ImageExport.Spec.Source.AsImageBuildRefSource()
			Expect(err).ToNot(HaveOccurred())
			Expect(source.ImageBuildRef).To(Equal("override-build"))
		})
	})
})
