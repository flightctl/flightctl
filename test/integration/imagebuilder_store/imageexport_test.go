package imagebuilder_store_test

import (
	"context"
	"os"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
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

func newTestImageExport(name string) *api.ImageExport {
	source := api.ImageExportSource{}
	_ = source.FromImageReferenceSource(api.ImageReferenceSource{
		Type:       api.ImageReference,
		Repository: "source-registry",
		ImageName:  "source-image",
		ImageTag:   "v1.0",
	})

	return &api.ImageExport{
		ApiVersion: api.ImageExportAPIVersion,
		Kind:       api.ImageExportKind,
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImageExportSpec{
			Source: source,
			Destination: api.ImageExportDestination{
				Repository: "output-registry",
				ImageName:  "output-image",
				Tag:        "v1.0",
			},
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
	})

	Context("Delete", func() {
		It("should delete an existing ImageExport", func() {
			imageExport := newTestImageExport("delete-test")
			_, err := storeInst.ImageExport().Create(ctx, orgId, imageExport)
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.ImageExport().Delete(ctx, orgId, "delete-test")
			Expect(err).ToNot(HaveOccurred())

			_, err = storeInst.ImageExport().Get(ctx, orgId, "delete-test")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return not found when deleting non-existent ImageExport", func() {
			err := storeInst.ImageExport().Delete(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
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
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
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
