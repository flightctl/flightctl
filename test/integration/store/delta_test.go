package store_test

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("DeltaStore", func() {
	var (
		log               *logrus.Logger
		ctx               context.Context
		orgId             uuid.UUID
		deltaStore        deltastore.Store
		organizationStore organizationstore.Store
		cfg               *config.Config
		dbName            string
		db                *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		deltaStore = deltastore.NewStore(db, log.WithField("pkg", "delta-store"))
		organizationStore = organizationstore.NewOrganizationStore(db)

		orgId = uuid.New()
		err = testutil.CreateTestOrganization(ctx, organizationStore, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	generation := func(org uuid.UUID, repo string) *model.DeltaGeneration {
		return &model.DeltaGeneration{
			OrgID:           org,
			ImageRepository: repo,
			SourceDigest:    "sha256:aaa",
			TargetDigest:    "sha256:bbb",
		}
	}

	keyOf := func(g *model.DeltaGeneration) deltastore.GenerationKey {
		return deltastore.GenerationKey{
			OrgID:           g.OrgID,
			ImageRepository: g.ImageRepository,
			SourceDigest:    g.SourceDigest,
			TargetDigest:    g.TargetDigest,
		}
	}

	Context("When inserting generations for two image repositories with the same digests", func() {
		It("should keep both rows", func() {
			a := generation(orgId, "quay.io/team-a/os")
			b := generation(orgId, "quay.io/team-b/os")

			changedA, err := deltaStore.InsertGeneration(ctx, a)
			Expect(err).ToNot(HaveOccurred())
			Expect(changedA).To(BeTrue())

			changedB, err := deltaStore.InsertGeneration(ctx, b)
			Expect(err).ToNot(HaveOccurred())
			Expect(changedB).To(BeTrue())

			gotA, err := deltaStore.GetGeneration(ctx, keyOf(a))
			Expect(err).ToNot(HaveOccurred())
			Expect(gotA.ImageRepository).To(Equal("quay.io/team-a/os"))
			Expect(gotA.Status).To(Equal(model.DeltaGenerationPending))

			gotB, err := deltaStore.GetGeneration(ctx, keyOf(b))
			Expect(err).ToNot(HaveOccurred())
			Expect(gotB.ImageRepository).To(Equal("quay.io/team-b/os"))
			Expect(gotB.Status).To(Equal(model.DeltaGenerationPending))
		})
	})

	Context("When inserting the same generation key twice", func() {
		It("should no-op when the existing row is pending", func() {
			g := generation(orgId, "quay.io/team-a/os")
			_, err := deltaStore.InsertGeneration(ctx, g)
			Expect(err).ToNot(HaveOccurred())

			changed, err := deltaStore.InsertGeneration(ctx, generation(orgId, "quay.io/team-a/os"))
			Expect(err).ToNot(HaveOccurred())
			Expect(changed).To(BeFalse())

			got, err := deltaStore.GetGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationPending))
			Expect(got.ResourceVersion).To(Equal(int64(0)))
		})
	})

	Context("When inserting over an existing failed row", func() {
		It("should reset status to pending and bump resource_version", func() {
			ref := "oci://delta"
			size := int64(42)
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = model.DeltaGenerationFailed
			g.ResourceVersion = 3
			g.DeltaRef = &ref
			g.SizeBytes = &size
			_, err := deltaStore.InsertGeneration(ctx, g)
			Expect(err).ToNot(HaveOccurred())

			changed, err := deltaStore.InsertGeneration(ctx, generation(orgId, "quay.io/team-a/os"))
			Expect(err).ToNot(HaveOccurred())
			Expect(changed).To(BeTrue())

			got, err := deltaStore.GetGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationPending))
			Expect(got.ResourceVersion).To(Equal(int64(4)))
			Expect(got.DeltaRef).ToNot(BeNil())
			Expect(*got.DeltaRef).To(Equal(ref))
			Expect(got.SizeBytes).ToNot(BeNil())
			Expect(*got.SizeBytes).To(Equal(size))
		})
	})

	DescribeTable("When inserting over a terminal or in-progress generation",
		func(status string) {
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = status
			g.ResourceVersion = 2
			_, err := deltaStore.InsertGeneration(ctx, g)
			Expect(err).ToNot(HaveOccurred())

			changed, err := deltaStore.InsertGeneration(ctx, generation(orgId, "quay.io/team-a/os"))
			Expect(err).ToNot(HaveOccurred())
			Expect(changed).To(BeFalse())

			got, err := deltaStore.GetGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(status))
			Expect(got.ResourceVersion).To(Equal(int64(2)))
		},
		Entry("succeeded", model.DeltaGenerationSucceeded),
		Entry("rejected", model.DeltaGenerationRejected),
		Entry("in_progress", model.DeltaGenerationInProgress),
	)

	Context("When inserting the same digests in two orgs", func() {
		It("should keep both rows", func() {
			otherOrg := uuid.New()
			Expect(testutil.CreateTestOrganization(ctx, organizationStore, otherOrg)).To(Succeed())

			changedA, err := deltaStore.InsertGeneration(ctx, generation(orgId, "quay.io/team-a/os"))
			Expect(err).ToNot(HaveOccurred())
			Expect(changedA).To(BeTrue())

			changedB, err := deltaStore.InsertGeneration(ctx, generation(otherOrg, "quay.io/team-a/os"))
			Expect(err).ToNot(HaveOccurred())
			Expect(changedB).To(BeTrue())
		})
	})

	Context("When getting a missing generation", func() {
		It("should return ErrResourceNotFound", func() {
			_, err := deltaStore.GetGeneration(ctx, keyOf(generation(orgId, "quay.io/missing/os")))
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})
})
