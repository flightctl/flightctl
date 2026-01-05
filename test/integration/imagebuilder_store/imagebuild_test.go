package imagebuilder_store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
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
	})

	Context("Delete", func() {
		It("should delete an existing ImageBuild", func() {
			imageBuild := newTestImageBuild("delete-test")
			_, err := storeInst.ImageBuild().Create(ctx, orgId, imageBuild)
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.ImageBuild().Delete(ctx, orgId, "delete-test")
			Expect(err).ToNot(HaveOccurred())

			_, err = storeInst.ImageBuild().Get(ctx, orgId, "delete-test")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return not found when deleting non-existent ImageBuild", func() {
			err := storeInst.ImageBuild().Delete(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
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
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
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
