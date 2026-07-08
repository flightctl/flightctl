package imagebuilder_store_test

import (
	"context"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutilpkg "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func newTestImagePromotion(name, imageBuildRef string) *domain.ImagePromotion {
	target := api.ImagePromotionTarget{}
	_ = target.FromNewCatalogItemTarget(api.NewCatalogItemTarget{
		Type:            api.NewCatalogItem,
		CatalogName:     "test-catalog",
		CatalogItemName: "test-item",
		Version:         "1.0.0",
	})

	return &api.ImagePromotion{
		ApiVersion: api.ImagePromotionAPIVersion,
		Kind:       string(api.ResourceKindImagePromotion),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImagePromotionSpec{
			Source: api.ImagePromotionSource{
				ImageBuildRef: imageBuildRef,
			},
			Target: target,
		},
	}
}

func newTestImagePromotionWithStatus(name, imageBuildRef string, reason api.ImagePromotionConditionReason) *domain.ImagePromotion {
	promotion := newTestImagePromotion(name, imageBuildRef)
	promotion.Status = &api.ImagePromotionStatus{
		Conditions: &[]api.ImagePromotionCondition{
			{
				Type:   api.ImagePromotionConditionTypeReady,
				Status: v1beta1.ConditionStatusFalse,
				Reason: string(reason),
			},
		},
	}
	return promotion
}

var _ = Describe("ImagePromotionStore", func() {
	var (
		log               *logrus.Logger
		ctx               context.Context
		orgId             uuid.UUID
		storeInst         store.Store
		organizationStore organizationstore.Store
		cfg               *config.Config
		dbName            string
		db                *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutilpkg.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()

		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", flightctlstore.InitDB)
		Expect(err).NotTo(HaveOccurred())
		organizationStore = organizationstore.NewOrganizationStore(db)

		storeInst = store.NewStore(db, log.WithField("pkg", "imagebuilder-store"))

		orgId = uuid.New()
		err = testutilpkg.CreateTestOrganization(ctx, organizationStore, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	Context("Create", func() {
		It("should create an ImagePromotion successfully", func() {
			promotion := newTestImagePromotion("test-promotion-1", "test-build-1")
			result, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)

			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.Metadata.Name)).To(Equal("test-promotion-1"))
			Expect(result.Metadata.CreationTimestamp).ToNot(BeNil())
			Expect(result.Metadata.Generation).ToNot(BeNil())
			Expect(*result.Metadata.Generation).To(Equal(int64(1)))
			Expect(result.Metadata.ResourceVersion).ToNot(BeNil())
			Expect(*result.Metadata.ResourceVersion).ToNot(BeEmpty())
			Expect(result.Spec.Source.ImageBuildRef).To(Equal("test-build-1"))
		})

		It("should fail to create a duplicate ImagePromotion", func() {
			promotion := newTestImagePromotion("duplicate-promotion", "test-build")

			_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			_, err = storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrDuplicateName))
		})

		It("should fail to create an ImagePromotion with nil name", func() {
			promotion := newTestImagePromotion("test", "test-build")
			promotion.Metadata.Name = nil

			_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNameIsNil))
		})

		It("should create an ImagePromotion with initial status", func() {
			promotion := newTestImagePromotionWithStatus("promotion-with-status", "test-build", api.ImagePromotionConditionReasonWaitingForArtifacts)

			result, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Status).ToNot(BeNil())
			Expect(result.Status.Conditions).ToNot(BeNil())
			Expect(*result.Status.Conditions).To(HaveLen(1))
			Expect((*result.Status.Conditions)[0].Reason).To(Equal(string(api.ImagePromotionConditionReasonWaitingForArtifacts)))
		})
	})

	Context("Get", func() {
		It("should get an existing ImagePromotion", func() {
			promotion := newTestImagePromotion("get-test", "test-build")
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImagePromotion().Get(ctx, orgId, "get-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(lo.FromPtr(result.Metadata.Name)).To(Equal("get-test"))
			Expect(result.Spec.Source.ImageBuildRef).To(Equal("test-build"))
		})

		It("should return not found for a non-existent ImagePromotion", func() {
			_, err := storeInst.ImagePromotion().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return not found for a wrong org", func() {
			promotion := newTestImagePromotion("org-test", "test-build")
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			wrongOrgId := uuid.New()
			err = testutilpkg.CreateTestOrganization(ctx, organizationStore, wrongOrgId)
			Expect(err).ToNot(HaveOccurred())

			_, err = storeInst.ImagePromotion().Get(ctx, wrongOrgId, "org-test")
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})

	Context("List", func() {
		It("should list all ImagePromotions", func() {
			for i := 0; i < 3; i++ {
				promotion := newTestImagePromotion(string(rune('a'+i))+"-promotion", "test-build")
				_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
				Expect(err).ToNot(HaveOccurred())
			}

			result, err := storeInst.ImagePromotion().List(ctx, orgId, flightctlstore.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(HaveLen(3))
		})

		It("should list with limit and return a continue token", func() {
			for i := 0; i < 5; i++ {
				promotion := newTestImagePromotion(string(rune('a'+i))+"-promotion", "test-build")
				_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
				Expect(err).ToNot(HaveOccurred())
			}

			result, err := storeInst.ImagePromotion().List(ctx, orgId, flightctlstore.ListParams{Limit: 2})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(HaveLen(2))
			Expect(result.Metadata.Continue).ToNot(BeNil())
		})

		It("should not list ImagePromotions from other orgs", func() {
			promotion := newTestImagePromotion("org-isolation-test", "test-build")
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			otherOrgId := uuid.New()
			err = testutilpkg.CreateTestOrganization(ctx, organizationStore, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImagePromotion().List(ctx, otherOrgId, flightctlstore.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Items).To(HaveLen(0))
		})

		It("should return an empty list when no ImagePromotions exist", func() {
			result, err := storeInst.ImagePromotion().List(ctx, orgId, flightctlstore.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Items).To(HaveLen(0))
		})
	})

	Context("Delete", func() {
		It("should delete an existing ImagePromotion and return the deleted resource", func() {
			promotion := newTestImagePromotion("delete-test", "test-build")
			created, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			deleted, err := storeInst.ImagePromotion().Delete(ctx, orgId, "delete-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).ToNot(BeNil())
			Expect(lo.FromPtr(deleted.Metadata.Name)).To(Equal("delete-test"))
			Expect(deleted.Metadata.Generation).To(Equal(created.Metadata.Generation))
			Expect(deleted.Metadata.ResourceVersion).To(Equal(created.Metadata.ResourceVersion))

			_, err = storeInst.ImagePromotion().Get(ctx, orgId, "delete-test")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return nil when deleting a non-existent ImagePromotion (idempotent)", func() {
			deleted, err := storeInst.ImagePromotion().Delete(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeNil())
		})

		It("should not delete an ImagePromotion from a different org", func() {
			promotion := newTestImagePromotion("cross-org-delete", "test-build")
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			otherOrgId := uuid.New()
			err = testutilpkg.CreateTestOrganization(ctx, organizationStore, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			deleted, err := storeInst.ImagePromotion().Delete(ctx, otherOrgId, "cross-org-delete")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeNil())

			result, err := storeInst.ImagePromotion().Get(ctx, orgId, "cross-org-delete")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
		})
	})

	Context("Update", func() {
		It("should update spec and labels of an existing ImagePromotion", func() {
			promotion := newTestImagePromotion("update-test", "build-v1")
			created, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			created.Spec.Source.ImageBuildRef = "build-v2"
			created.Metadata.Labels = &map[string]string{"env": "prod"}

			result, err := storeInst.ImagePromotion().Update(ctx, orgId, created)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Spec.Source.ImageBuildRef).To(Equal("build-v2"))
			Expect(*result.Metadata.Labels).To(HaveKeyWithValue("env", "prod"))
		})

		It("should increment resource version on update", func() {
			promotion := newTestImagePromotion("rv-update-test", "test-build")
			created, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())
			Expect(*created.Metadata.ResourceVersion).To(Equal("1"))

			result, err := storeInst.ImagePromotion().Update(ctx, orgId, created)
			Expect(err).ToNot(HaveOccurred())
			Expect(*result.Metadata.ResourceVersion).To(Equal("2"))
		})

		It("should fail to update when resource version does not match", func() {
			promotion := newTestImagePromotion("rv-conflict-test", "test-build")
			created, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			staleRV := "999"
			created.Metadata.ResourceVersion = &staleRV

			_, err = storeInst.ImagePromotion().Update(ctx, orgId, created)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})

		It("should fail to update a non-existent ImagePromotion", func() {
			promotion := newTestImagePromotion("nonexistent", "test-build")
			_, err := storeInst.ImagePromotion().Update(ctx, orgId, promotion)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})

		It("should fail to update an ImagePromotion with nil name", func() {
			promotion := newTestImagePromotion("test", "test-build")
			promotion.Metadata.Name = nil

			_, err := storeInst.ImagePromotion().Update(ctx, orgId, promotion)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNameIsNil))
		})

		It("should update status when provided alongside spec", func() {
			promotion := newTestImagePromotion("update-with-status", "test-build")
			created, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			created.Status = &api.ImagePromotionStatus{
				Conditions: &[]api.ImagePromotionCondition{
					{
						Type:    api.ImagePromotionConditionTypeReady,
						Status:  v1beta1.ConditionStatusTrue,
						Reason:  string(api.ImagePromotionConditionReasonCompleted),
						Message: "Promotion completed",
					},
				},
			}

			result, err := storeInst.ImagePromotion().Update(ctx, orgId, created)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Status).ToNot(BeNil())
			Expect(*result.Status.Conditions).To(HaveLen(1))
			Expect((*result.Status.Conditions)[0].Reason).To(Equal(string(api.ImagePromotionConditionReasonCompleted)))
		})
	})

	Context("UpdateStatus", func() {
		It("should update the status of an existing ImagePromotion", func() {
			promotion := newTestImagePromotion("status-test", "test-build")
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			promotion.Status = &api.ImagePromotionStatus{
				Conditions: &[]api.ImagePromotionCondition{
					{
						Type:    api.ImagePromotionConditionTypeReady,
						Status:  v1beta1.ConditionStatusFalse,
						Reason:  string(api.ImagePromotionConditionReasonPublishing),
						Message: "Publishing in progress",
					},
				},
			}

			result, err := storeInst.ImagePromotion().UpdateStatus(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Status).ToNot(BeNil())
			Expect(*result.Status.Conditions).To(HaveLen(1))
			Expect((*result.Status.Conditions)[0].Type).To(Equal(api.ImagePromotionConditionTypeReady))
			Expect((*result.Status.Conditions)[0].Status).To(Equal(v1beta1.ConditionStatusFalse))
			Expect((*result.Status.Conditions)[0].Reason).To(Equal(string(api.ImagePromotionConditionReasonPublishing)))
			Expect((*result.Status.Conditions)[0].Message).To(Equal("Publishing in progress"))
		})

		It("should increment resource version on status update", func() {
			promotion := newTestImagePromotion("rv-status-test", "test-build")
			created, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())
			Expect(*created.Metadata.ResourceVersion).To(Equal("1"))

			created.Status = &api.ImagePromotionStatus{}
			result, err := storeInst.ImagePromotion().UpdateStatus(ctx, orgId, created)
			Expect(err).ToNot(HaveOccurred())
			Expect(*result.Metadata.ResourceVersion).To(Equal("2"))
		})

		It("should fail to update status when resource version does not match", func() {
			promotion := newTestImagePromotion("rv-status-conflict", "test-build")
			created, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			staleRV := "999"
			created.Metadata.ResourceVersion = &staleRV
			created.Status = &api.ImagePromotionStatus{}

			_, err = storeInst.ImagePromotion().UpdateStatus(ctx, orgId, created)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})

		It("should fail when updating status of a non-existent ImagePromotion", func() {
			promotion := newTestImagePromotion("nonexistent", "test-build")
			promotion.Status = &api.ImagePromotionStatus{}

			_, err := storeInst.ImagePromotion().UpdateStatus(ctx, orgId, promotion)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})

		It("should fail when updating status with nil name", func() {
			promotion := newTestImagePromotion("test", "test-build")
			promotion.Metadata.Name = nil
			promotion.Status = &api.ImagePromotionStatus{}

			_, err := storeInst.ImagePromotion().UpdateStatus(ctx, orgId, promotion)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceNameIsNil))
		})

		It("should fail when updating status with nil status", func() {
			promotion := newTestImagePromotion("nil-status-test", "test-build")
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, promotion)
			Expect(err).ToNot(HaveOccurred())

			promotion.Status = nil
			_, err = storeInst.ImagePromotion().UpdateStatus(ctx, orgId, promotion)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrResourceIsNil))
		})
	})

	Context("ListPendingForBuild", func() {
		It("should return promotions in WaitingForArtifacts state for the given imageBuildRef", func() {
			waiting := newTestImagePromotionWithStatus("waiting-promotion", "target-build", api.ImagePromotionConditionReasonWaitingForArtifacts)
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, waiting)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImagePromotion().ListPendingForBuild(ctx, orgId, "target-build")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(lo.FromPtr(result[0].Metadata.Name)).To(Equal("waiting-promotion"))
		})

		It("should return promotions in AmendmentFailed state for the given imageBuildRef", func() {
			failed := newTestImagePromotionWithStatus("amendment-failed-promotion", "target-build", api.ImagePromotionConditionReasonAmendmentFailed)
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, failed)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImagePromotion().ListPendingForBuild(ctx, orgId, "target-build")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(lo.FromPtr(result[0].Metadata.Name)).To(Equal("amendment-failed-promotion"))
		})

		It("should return both WaitingForArtifacts and AmendmentFailed promotions", func() {
			waiting := newTestImagePromotionWithStatus("waiting-promotion-2", "multi-build", api.ImagePromotionConditionReasonWaitingForArtifacts)
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, waiting)
			Expect(err).ToNot(HaveOccurred())

			failed := newTestImagePromotionWithStatus("amendment-failed-promotion-2", "multi-build", api.ImagePromotionConditionReasonAmendmentFailed)
			_, err = storeInst.ImagePromotion().Create(ctx, orgId, failed)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImagePromotion().ListPendingForBuild(ctx, orgId, "multi-build")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(2))
		})

		It("should not return promotions in Completed or other terminal states", func() {
			completed := newTestImagePromotionWithStatus("completed-promotion", "terminal-build", api.ImagePromotionConditionReasonCompleted)
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, completed)
			Expect(err).ToNot(HaveOccurred())

			buildFailed := newTestImagePromotionWithStatus("build-failed-promotion", "terminal-build", api.ImagePromotionConditionReasonBuildFailed)
			_, err = storeInst.ImagePromotion().Create(ctx, orgId, buildFailed)
			Expect(err).ToNot(HaveOccurred())

			publishing := newTestImagePromotionWithStatus("publishing-promotion", "terminal-build", api.ImagePromotionConditionReasonPublishing)
			_, err = storeInst.ImagePromotion().Create(ctx, orgId, publishing)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImagePromotion().ListPendingForBuild(ctx, orgId, "terminal-build")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(0))
		})

		It("should not return promotions for a different imageBuildRef", func() {
			waiting := newTestImagePromotionWithStatus("other-build-promotion", "other-build", api.ImagePromotionConditionReasonWaitingForArtifacts)
			_, err := storeInst.ImagePromotion().Create(ctx, orgId, waiting)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImagePromotion().ListPendingForBuild(ctx, orgId, "target-build")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(0))
		})

		It("should not return promotions from a different org", func() {
			otherOrgId := uuid.New()
			err := testutilpkg.CreateTestOrganization(ctx, organizationStore, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			waiting := newTestImagePromotionWithStatus("other-org-promotion", "shared-build", api.ImagePromotionConditionReasonWaitingForArtifacts)
			_, err = storeInst.ImagePromotion().Create(ctx, otherOrgId, waiting)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.ImagePromotion().ListPendingForBuild(ctx, orgId, "shared-build")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(0))
		})

		It("should return an empty list when no promotions exist for the imageBuildRef", func() {
			result, err := storeInst.ImagePromotion().ListPendingForBuild(ctx, orgId, "nonexistent-build")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(0))
		})
	})
})
