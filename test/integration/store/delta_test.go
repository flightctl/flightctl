package store_test

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
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

	insertGens := func(gens ...*model.DeltaGeneration) []deltastore.GenerationKey {
		changed, err := deltaStore.InsertGenerations(ctx, gens)
		Expect(err).ToNot(HaveOccurred())
		return changed
	}

	Context("When inserting generations for two image repositories with the same digests", func() {
		It("should keep both rows", func() {
			a := generation(orgId, "quay.io/team-a/os")
			b := generation(orgId, "quay.io/team-b/os")

			changed := insertGens(a, b)
			Expect(changed).To(ConsistOf(keyOf(a), keyOf(b)))

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
			insertGens(g)

			changed := insertGens(generation(orgId, "quay.io/team-a/os"))
			Expect(changed).To(BeEmpty())

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
			insertGens(g)

			stale := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
			Expect(db.Model(&model.DeltaGeneration{}).Where(
				"org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?",
				g.OrgID, g.ImageRepository, g.SourceDigest, g.TargetDigest,
			).Update("updated_at", stale).Error).ToNot(HaveOccurred())

			changed := insertGens(generation(orgId, "quay.io/team-a/os"))
			Expect(changed).To(ConsistOf(keyOf(g)))

			got, err := deltaStore.GetGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationPending))
			Expect(got.ResourceVersion).To(Equal(int64(4)))
			Expect(got.DeltaRef).ToNot(BeNil())
			Expect(*got.DeltaRef).To(Equal(ref))
			Expect(got.SizeBytes).ToNot(BeNil())
			Expect(*got.SizeBytes).To(Equal(size))
			Expect(got.UpdatedAt).To(BeTemporally(">", stale))
		})
	})

	Context("When inserting a mixed batch of new, failed, and pending generations", func() {
		It("should return only the new and failed keys", func() {
			pending := generation(orgId, "quay.io/team-a/os")
			failed := generation(orgId, "quay.io/team-b/os")
			failed.Status = model.DeltaGenerationFailed
			insertGens(pending, failed)

			fresh := generation(orgId, "quay.io/team-c/os")
			changed := insertGens(pending, failed, fresh)
			Expect(changed).To(ConsistOf(keyOf(failed), keyOf(fresh)))
		})
	})

	DescribeTable("When inserting over a terminal or in-progress generation",
		func(status string) {
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = status
			g.ResourceVersion = 2
			insertGens(g)

			changed := insertGens(generation(orgId, "quay.io/team-a/os"))
			Expect(changed).To(BeEmpty())

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

			a := generation(orgId, "quay.io/team-a/os")
			b := generation(otherOrg, "quay.io/team-a/os")
			changed := insertGens(a, b)
			Expect(changed).To(ConsistOf(keyOf(a), keyOf(b)))
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

	Context("When inserting a waiting fleet prepare and joining generations", func() {
		It("should persist the joins without an FK from generations to prepares", func() {
			a := generation(orgId, "quay.io/team-a/os")
			b := generation(orgId, "quay.io/team-b/os")
			deadline := time.Now().Add(-time.Hour)
			prep := fleetPrepare("myfleet", &deadline)
			Expect(deltaStore.InsertPrepare(ctx, prep)).To(Succeed())
			Expect(prep.ID).ToNot(Equal(uuid.Nil))
			Expect(prep.Status).To(Equal(model.DeltaPrepareWaiting))

			insertGens(a, b)
			keys := []deltastore.GenerationKey{keyOf(a), keyOf(b)}
			Expect(deltaStore.InsertPrepareGenerations(ctx, prep.ID, keys)).To(Succeed())
			Expect(deltaStore.InsertPrepareGenerations(ctx, prep.ID, keys)).To(Succeed())

			got, err := deltaStore.GetPrepare(ctx, prep.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Kind).To(Equal(domain.FleetKind))
			Expect(got.Name).To(Equal("myfleet"))

			var joinCount int64
			Expect(db.Model(&model.DeltaPrepareGeneration{}).Where("prepare_id = ?", prep.ID).Count(&joinCount).Error).ToNot(HaveOccurred())
			Expect(joinCount).To(Equal(int64(2)))

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

		It("should return expired waiting rows from every org", func() {
			otherOrg := uuid.New()
			Expect(testutil.CreateTestOrganization(ctx, organizationStore, otherOrg)).To(Succeed())
			past := time.Now().Add(-time.Hour)
			Expect(deltaStore.InsertPrepare(ctx, fleetPrepare("org-a", &past))).To(Succeed())
			otherPrep := &model.DeltaPrepare{
				OrgID:    otherOrg,
				Kind:     domain.FleetKind,
				Name:     "org-b",
				Deadline: &past,
			}
			Expect(deltaStore.InsertPrepare(ctx, otherPrep)).To(Succeed())

			listed, err := deltaStore.ListWaitingPastDeadline(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).To(HaveLen(2))
			names := []string{listed[0].Name, listed[1].Name}
			Expect(names).To(ConsistOf("org-a", "org-b"))
		})
	})

	Context("When joining a prepare to a missing parent", func() {
		It("should return ErrResourceNotFound if the prepare does not exist", func() {
			g := generation(orgId, "quay.io/team-a/os")
			insertGens(g)

			err := deltaStore.InsertPrepareGenerations(ctx, uuid.New(), []deltastore.GenerationKey{keyOf(g)})
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return ErrResourceNotFound if the generation does not exist", func() {
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.InsertPrepare(ctx, prep)).To(Succeed())

			err := deltaStore.InsertPrepareGenerations(ctx, prep.ID, []deltastore.GenerationKey{keyOf(generation(orgId, "quay.io/missing/os"))})
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})

	Context("When getting a missing prepare", func() {
		It("should return ErrResourceNotFound", func() {
			_, err := deltaStore.GetPrepare(ctx, uuid.New())
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})

	Context("When CAS-ing a waiting prepare to complete", func() {
		It("should update once and reject a second CAS", func() {
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.InsertPrepare(ctx, prep)).To(Succeed())

			Expect(deltaStore.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareComplete)).To(Succeed())

			got, err := deltaStore.GetPrepare(ctx, prep.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaPrepareComplete))

			err = deltaStore.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareFailed)
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})
	})

	Context("When CAS-ing a generation with a stale resource_version", func() {
		It("should leave the row unchanged", func() {
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = model.DeltaGenerationInProgress
			g.ResourceVersion = 1
			insertGens(g)

			ref := "oci://delta"
			size := int64(9)
			err := deltaStore.CASGeneration(ctx, keyOf(g), 0, deltastore.GenerationCAS{
				Status:    model.DeltaGenerationSucceeded,
				DeltaRef:  &ref,
				SizeBytes: &size,
			})
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))

			got, err := deltaStore.GetGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationInProgress))
			Expect(got.ResourceVersion).To(Equal(int64(1)))
			Expect(got.DeltaRef).To(BeNil())
		})
	})

	Context("When CAS-ing a generation with the current resource_version", func() {
		It("should write fields and bump resource_version", func() {
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = model.DeltaGenerationInProgress
			g.ResourceVersion = 1
			insertGens(g)

			ref := "oci://delta"
			size := int64(9)
			Expect(deltaStore.CASGeneration(ctx, keyOf(g), 1, deltastore.GenerationCAS{
				Status:    model.DeltaGenerationSucceeded,
				DeltaRef:  &ref,
				SizeBytes: &size,
			})).To(Succeed())

			got, err := deltaStore.GetGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationSucceeded))
			Expect(got.ResourceVersion).To(Equal(int64(2)))
			Expect(got.DeltaRef).ToNot(BeNil())
			Expect(*got.DeltaRef).To(Equal(ref))
			Expect(got.SizeBytes).ToNot(BeNil())
			Expect(*got.SizeBytes).To(Equal(size))
		})
	})

	Context("When Device and Fleet rows exist and delta tables are written", func() {
		It("should leave Device and Fleet JSON unchanged", func() {
			deviceStore := devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
			fleetStore := fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "mydevice", nil, nil, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "myfleet", nil, nil)

			beforeDevice, err := deviceStore.Get(ctx, orgId, "mydevice")
			Expect(err).ToNot(HaveOccurred())
			beforeFleet, err := fleetStore.Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			beforeDeviceJSON, err := json.Marshal(beforeDevice)
			Expect(err).ToNot(HaveOccurred())
			beforeFleetJSON, err := json.Marshal(beforeFleet)
			Expect(err).ToNot(HaveOccurred())

			g := generation(orgId, "quay.io/team-a/os")
			insertGens(g)
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.InsertPrepare(ctx, prep)).To(Succeed())
			Expect(deltaStore.InsertPrepareGenerations(ctx, prep.ID, []deltastore.GenerationKey{keyOf(g)})).To(Succeed())

			afterDevice, err := deviceStore.Get(ctx, orgId, "mydevice")
			Expect(err).ToNot(HaveOccurred())
			afterFleet, err := fleetStore.Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			afterDeviceJSON, err := json.Marshal(afterDevice)
			Expect(err).ToNot(HaveOccurred())
			afterFleetJSON, err := json.Marshal(afterFleet)
			Expect(err).ToNot(HaveOccurred())
			Expect(afterDeviceJSON).To(Equal(beforeDeviceJSON))
			Expect(afterFleetJSON).To(Equal(beforeFleetJSON))

			var genCount, prepCount, joinCount int64
			Expect(db.Model(&model.DeltaGeneration{}).Count(&genCount).Error).ToNot(HaveOccurred())
			Expect(db.Model(&model.DeltaPrepare{}).Count(&prepCount).Error).ToNot(HaveOccurred())
			Expect(db.Model(&model.DeltaPrepareGeneration{}).Count(&joinCount).Error).ToNot(HaveOccurred())
			Expect(genCount).To(Equal(int64(1)))
			Expect(prepCount).To(Equal(int64(1)))
			Expect(joinCount).To(Equal(int64(1)))
		})
	})
})
