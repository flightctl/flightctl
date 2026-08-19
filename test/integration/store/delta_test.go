package store_test

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
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

	fleetPrepare := func(name string, deadline *time.Time) *model.DeltaPrepare {
		tv := "tv-1"
		return &model.DeltaPrepare{
			OrgID:           orgId,
			Kind:            domain.FleetKind,
			Name:            name,
			TemplateVersion: &tv,
			Deadline:        deadline,
		}
	}

	Context("When inserting a waiting fleet prepare and joining a generation", func() {
		It("should persist the join without an FK from generations to prepares", func() {
			g := generation(orgId, "quay.io/team-a/os")
			_, err := deltaStore.InsertGeneration(ctx, g)
			Expect(err).ToNot(HaveOccurred())

			deadline := time.Now().Add(-time.Hour)
			prep := fleetPrepare("myfleet", &deadline)
			Expect(deltaStore.InsertPrepare(ctx, prep)).To(Succeed())
			Expect(prep.ID).ToNot(Equal(uuid.Nil))
			Expect(prep.Status).To(Equal(model.DeltaPrepareWaiting))

			Expect(deltaStore.JoinPrepareGeneration(ctx, prep.ID, keyOf(g))).To(Succeed())
			Expect(deltaStore.JoinPrepareGeneration(ctx, prep.ID, keyOf(g))).To(Succeed())

			got, err := deltaStore.GetPrepare(ctx, prep.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Kind).To(Equal(domain.FleetKind))
			Expect(got.Name).To(Equal("myfleet"))

			var join model.DeltaPrepareGeneration
			Expect(db.Where(
				"prepare_id = ? AND org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?",
				prep.ID, g.OrgID, g.ImageRepository, g.SourceDigest, g.TargetDigest,
			).Take(&join).Error).ToNot(HaveOccurred())

			var fkCount int64
			Expect(db.Raw(`
				SELECT COUNT(*)
				FROM pg_constraint
				WHERE conrelid = 'delta_generations'::regclass
				  AND confrelid = 'delta_prepares'::regclass
			`).Scan(&fkCount).Error).ToNot(HaveOccurred())
			Expect(fkCount).To(Equal(int64(0)))
		})
	})

	Context("When inserting a second waiting prepare for the same fleet", func() {
		It("should return ErrDuplicateName", func() {
			Expect(deltaStore.InsertPrepare(ctx, fleetPrepare("myfleet", nil))).To(Succeed())
			err := deltaStore.InsertPrepare(ctx, fleetPrepare("myfleet", nil))
			Expect(err).To(MatchError(flterrors.ErrDuplicateName))
		})
	})

	Context("When inserting waiting prepares for a fleet and a device with the same name", func() {
		It("should keep both rows", func() {
			fleetPrep := fleetPrepare("shared", nil)
			Expect(deltaStore.InsertPrepare(ctx, fleetPrep)).To(Succeed())
			specRV := int64(7)
			devicePrep := &model.DeltaPrepare{
				OrgID:               orgId,
				Kind:                domain.DeviceKind,
				Name:                "shared",
				SpecResourceVersion: &specRV,
			}
			Expect(deltaStore.InsertPrepare(ctx, devicePrep)).To(Succeed())

			gotFleet, err := deltaStore.GetPrepare(ctx, fleetPrep.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(gotFleet.Kind).To(Equal(domain.FleetKind))

			gotDevice, err := deltaStore.GetPrepare(ctx, devicePrep.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(gotDevice.Kind).To(Equal(domain.DeviceKind))
		})
	})

	Context("When a waiting prepare is complete, then a new waiting prepare for that fleet", func() {
		It("should allow the second insert", func() {
			complete := fleetPrepare("myfleet", nil)
			complete.Status = model.DeltaPrepareComplete
			Expect(deltaStore.InsertPrepare(ctx, complete)).To(Succeed())
			Expect(deltaStore.InsertPrepare(ctx, fleetPrepare("myfleet", nil))).To(Succeed())
		})
	})

	Context("When listing waiting prepares past deadline", func() {
		It("should return only expired waiting rows", func() {
			past := time.Now().Add(-time.Hour)
			future := time.Now().Add(time.Hour)
			Expect(deltaStore.InsertPrepare(ctx, fleetPrepare("expired", &past))).To(Succeed())
			Expect(deltaStore.InsertPrepare(ctx, fleetPrepare("future", &future))).To(Succeed())
			Expect(deltaStore.InsertPrepare(ctx, fleetPrepare("none", nil))).To(Succeed())

			listed, err := deltaStore.ListWaitingPastDeadline(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).To(HaveLen(1))
			Expect(listed[0].Name).To(Equal("expired"))
			Expect(listed[0].Status).To(Equal(model.DeltaPrepareWaiting))
		})
	})

	Context("When getting a missing prepare", func() {
		It("should return ErrResourceNotFound", func() {
			_, err := deltaStore.GetPrepare(ctx, uuid.New())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})
})
