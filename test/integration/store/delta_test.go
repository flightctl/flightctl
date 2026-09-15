package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("DeltaStore", func() {
	var (
		log               *logrus.Logger
		ctx               context.Context
		orgId             uuid.UUID
		deltaStore        *deltastore.DeltaStore
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
		Expect(deltaStore.InitialMigration(ctx)).To(Succeed())
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
		current, err := deltaStore.InsertDeltaGenerations(ctx, gens)
		Expect(err).ToNot(HaveOccurred())
		keys := make([]deltastore.GenerationKey, 0, len(current))
		for i := range current {
			keys = append(keys, keyOf(&current[i]))
		}
		return keys
	}

	createDeltaPrepareGenerations := func(ctx context.Context, store *deltastore.DeltaStore, prepareID uuid.UUID, keys []deltastore.GenerationKey) error {
		joins := make([]*model.DeltaPrepareGeneration, 0, len(keys))
		for _, key := range keys {
			joins = append(joins, &model.DeltaPrepareGeneration{
				PrepareID:       prepareID,
				OrgID:           key.OrgID,
				ImageRepository: key.ImageRepository,
				SourceDigest:    key.SourceDigest,
				TargetDigest:    key.TargetDigest,
			})
		}
		_, err := store.CreateDeltaPrepareGenerations(ctx, joins)
		return err
	}

	insertRejectedGeneration := func(ctx context.Context, store *deltastore.DeltaStore, generation *model.DeltaGeneration) error {
		generation.Status = model.DeltaGenerationRejected
		_, err := store.InsertDeltaGenerations(ctx, []*model.DeltaGeneration{generation})
		return err
	}

	Context("When inserting generations for two image repositories with the same digests", func() {
		It("should keep both rows", func() {
			a := generation(orgId, "quay.io/team-a/os")
			b := generation(orgId, "quay.io/team-b/os")

			changed := insertGens(a, b)
			Expect(changed).To(ConsistOf(keyOf(a), keyOf(b)))

			gotA, err := deltaStore.GetDeltaGeneration(ctx, keyOf(a))
			Expect(err).ToNot(HaveOccurred())
			Expect(gotA.ImageRepository).To(Equal("quay.io/team-a/os"))
			Expect(gotA.Status).To(Equal(model.DeltaGenerationPending))

			gotB, err := deltaStore.GetDeltaGeneration(ctx, keyOf(b))
			Expect(err).ToNot(HaveOccurred())
			Expect(gotB.ImageRepository).To(Equal("quay.io/team-b/os"))
			Expect(gotB.Status).To(Equal(model.DeltaGenerationPending))
		})
	})

	Context("When inserting the same generation key twice", func() {
		It("should no-op when the existing row is pending", func() {
			g := generation(orgId, "quay.io/team-a/os")
			insertGens(g)

			current := insertGens(generation(orgId, "quay.io/team-a/os"))
			Expect(current).To(ConsistOf(keyOf(g)))

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
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

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
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

	Context("When inserting the same generation key twice in one batch", func() {
		It("should insert the key once", func() {
			g := generation(orgId, "quay.io/team-a/os")
			dup := generation(orgId, "quay.io/team-a/os")
			changed := insertGens(g, dup)
			Expect(changed).To(ConsistOf(keyOf(g)))

			var count int64
			Expect(db.Model(&model.DeltaGeneration{}).Count(&count).Error).ToNot(HaveOccurred())
			Expect(count).To(Equal(int64(1)))
		})
	})

	Context("When inserting a mixed batch of new, failed, and pending generations", func() {
		It("should return only the new and failed keys", func() {
			pending := generation(orgId, "quay.io/team-a/os")
			failed := generation(orgId, "quay.io/team-b/os")
			failed.Status = model.DeltaGenerationFailed
			insertGens(pending, failed)

			fresh := generation(orgId, "quay.io/team-c/os")
			current := insertGens(pending, failed, fresh)
			Expect(current).To(ConsistOf(keyOf(pending), keyOf(failed), keyOf(fresh)))
		})
	})

	DescribeTable("When inserting over a terminal or in-progress generation",
		func(status string) {
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = status
			g.ResourceVersion = 2
			insertGens(g)

			current := insertGens(generation(orgId, "quay.io/team-a/os"))
			Expect(current).To(ConsistOf(keyOf(g)))

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
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
			_, err := deltaStore.GetDeltaGeneration(ctx, keyOf(generation(orgId, "quay.io/missing/os")))
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})

	Context("When admitting waiting prepares", func() {
		It("should atomically replace an older prepare and ignore stale events", func() {
			oldTV := "v1"
			old := &model.DeltaPrepare{
				OrgID:                 orgId,
				Kind:                  domain.FleetKind,
				Name:                  "fleet-1",
				TemplateVersion:       &oldTV,
				SourceResourceVersion: 1,
			}
			first, err := deltaStore.CreateOrReplaceWaitingDeltaPrepare(ctx, old)
			Expect(err).ToNot(HaveOccurred())
			Expect(first.Accepted).To(BeTrue())

			newTV := "v2"
			newer := &model.DeltaPrepare{
				OrgID:                 orgId,
				Kind:                  domain.FleetKind,
				Name:                  "fleet-1",
				TemplateVersion:       &newTV,
				SourceResourceVersion: 2,
			}
			second, err := deltaStore.CreateOrReplaceWaitingDeltaPrepare(ctx, newer)
			Expect(err).ToNot(HaveOccurred())
			Expect(second.Accepted).To(BeTrue())
			Expect(second.Replaced).To(BeTrue())

			oldStored, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: old.ID})
			Expect(err).ToNot(HaveOccurred())
			Expect(oldStored.Status).To(Equal(model.DeltaPrepareFailed))

			staleTV := "v1"
			stale := &model.DeltaPrepare{
				OrgID:                 orgId,
				Kind:                  domain.FleetKind,
				Name:                  "fleet-1",
				TemplateVersion:       &staleTV,
				SourceResourceVersion: 1,
			}
			staleResult, err := deltaStore.CreateOrReplaceWaitingDeltaPrepare(ctx, stale)
			Expect(err).ToNot(HaveOccurred())
			Expect(staleResult.Accepted).To(BeFalse())
			Expect(staleResult.Prepare.ID).To(Equal(newer.ID))

			duplicate := &model.DeltaPrepare{
				OrgID:                 orgId,
				Kind:                  domain.FleetKind,
				Name:                  "fleet-1",
				TemplateVersion:       &newTV,
				SourceResourceVersion: 2,
			}
			duplicateResult, err := deltaStore.CreateOrReplaceWaitingDeltaPrepare(ctx, duplicate)
			Expect(err).ToNot(HaveOccurred())
			Expect(duplicateResult.Accepted).To(BeTrue())
			Expect(duplicateResult.Prepare.ID).To(Equal(newer.ID))
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
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())
			Expect(prep.ID).ToNot(Equal(uuid.Nil))
			Expect(prep.Status).To(Equal(model.DeltaPrepareWaiting))

			insertGens(a, b)
			keys := []deltastore.GenerationKey{keyOf(a), keyOf(b)}
			Expect(createDeltaPrepareGenerations(ctx, deltaStore, prep.ID, keys)).To(Succeed())
			Expect(createDeltaPrepareGenerations(ctx, deltaStore, prep.ID, keys)).To(Succeed())

			got, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: prep.ID})
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

		It("should roll back the complete batch when one join fails", func() {
			g := generation(orgId, "quay.io/team-a/os")
			insertGens(g)
			prep := fleetPrepare("atomic-joins", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())

			valid := &model.DeltaPrepareGeneration{
				PrepareID: prep.ID, OrgID: g.OrgID, ImageRepository: g.ImageRepository,
				SourceDigest: g.SourceDigest, TargetDigest: g.TargetDigest,
			}
			invalid := *valid
			invalid.TargetDigest = "sha256:missing"

			_, err := deltaStore.CreateDeltaPrepareGenerations(ctx, []*model.DeltaPrepareGeneration{valid, &invalid})
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))

			joins, err := deltaStore.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{PrepareID: &prep.ID})
			Expect(err).ToNot(HaveOccurred())
			Expect(joins).To(BeEmpty())
		})
	})

	Context("When inserting the same prepare-generation key twice in one batch", func() {
		It("should keep one join row", func() {
			g := generation(orgId, "quay.io/team-a/os")
			insertGens(g)
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())

			key := keyOf(g)
			Expect(createDeltaPrepareGenerations(ctx, deltaStore, prep.ID, []deltastore.GenerationKey{key, key})).To(Succeed())

			var joinCount int64
			Expect(db.Model(&model.DeltaPrepareGeneration{}).Where("prepare_id = ?", prep.ID).Count(&joinCount).Error).ToNot(HaveOccurred())
			Expect(joinCount).To(Equal(int64(1)))
		})
	})

	Context("When inserting a second waiting prepare for the same fleet", func() {
		It("should return ErrDuplicateName", func() {
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("myfleet", nil))).To(Succeed())
			err := deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("myfleet", nil))
			Expect(err).To(MatchError(flterrors.ErrDuplicateName))
		})
	})

	Context("When looking up a waiting prepare by identity", func() {
		It("should return the waiting row for that org kind and name", func() {
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())

			got, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{OrgID: orgId, Kind: domain.FleetKind, Name: "myfleet"}, deltastore.WithPrepareStatus(model.DeltaPrepareWaiting))
			Expect(err).ToNot(HaveOccurred())
			Expect(got).ToNot(BeNil())
			Expect(got.ID).To(Equal(prep.ID))
			Expect(*got.TemplateVersion).To(Equal("tv-1"))
		})

		It("should return nil when no waiting row exists", func() {
			got, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{OrgID: orgId, Kind: domain.FleetKind, Name: "missing"}, deltastore.WithPrepareStatus(model.DeltaPrepareWaiting))
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(BeNil())
		})

		It("should return nil for a complete or failed row", func() {
			complete := fleetPrepare("done", nil)
			complete.Status = model.DeltaPrepareComplete
			Expect(deltaStore.CreateDeltaPrepare(ctx, complete)).To(Succeed())

			failed := fleetPrepare("failed", nil)
			failed.Status = model.DeltaPrepareFailed
			Expect(deltaStore.CreateDeltaPrepare(ctx, failed)).To(Succeed())

			gotComplete, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{OrgID: orgId, Kind: domain.FleetKind, Name: "done"}, deltastore.WithPrepareStatus(model.DeltaPrepareWaiting))
			Expect(err).ToNot(HaveOccurred())
			Expect(gotComplete).To(BeNil())

			gotFailed, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{OrgID: orgId, Kind: domain.FleetKind, Name: "failed"}, deltastore.WithPrepareStatus(model.DeltaPrepareWaiting))
			Expect(err).ToNot(HaveOccurred())
			Expect(gotFailed).To(BeNil())
		})

		It("should not return a waiting row of a different kind", func() {
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("shared", nil))).To(Succeed())
			got, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{OrgID: orgId, Kind: domain.DeviceKind, Name: "shared"}, deltastore.WithPrepareStatus(model.DeltaPrepareWaiting))
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(BeNil())
		})
	})

	Context("When inserting waiting prepares for a fleet and a device with the same name", func() {
		It("should keep both rows", func() {
			fleetPrep := fleetPrepare("shared", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrep)).To(Succeed())
			devicePrep := &model.DeltaPrepare{
				OrgID:    orgId,
				Kind:     domain.DeviceKind,
				Name:     "shared",
				SpecHash: lo.ToPtr("spec-hash"),
			}
			Expect(deltaStore.CreateDeltaPrepare(ctx, devicePrep)).To(Succeed())

			gotFleet, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: fleetPrep.ID})
			Expect(err).ToNot(HaveOccurred())
			Expect(gotFleet.Kind).To(Equal(domain.FleetKind))

			gotDevice, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: devicePrep.ID})
			Expect(err).ToNot(HaveOccurred())
			Expect(gotDevice.Kind).To(Equal(domain.DeviceKind))
		})
	})

	Context("When a waiting prepare is complete, then a new waiting prepare for that fleet", func() {
		It("should allow the second insert", func() {
			complete := fleetPrepare("myfleet", nil)
			complete.Status = model.DeltaPrepareComplete
			Expect(deltaStore.CreateDeltaPrepare(ctx, complete)).To(Succeed())
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("myfleet", nil))).To(Succeed())
		})
	})

	Context("When listing waiting prepares past deadline", func() {
		It("should return only expired waiting rows", func() {
			past := time.Now().Add(-time.Hour)
			future := time.Now().Add(time.Hour)
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("expired", &past))).To(Succeed())
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("future", &future))).To(Succeed())
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("none", nil))).To(Succeed())

			listed, err := deltaStore.ListWaitingPastDeadline(ctx, deltastore.MaxListWaitingPastDeadline, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).To(HaveLen(1))
			Expect(listed[0].Name).To(Equal("expired"))
			Expect(listed[0].Status).To(Equal(model.DeltaPrepareWaiting))
		})

		It("should return expired waiting rows from every org", func() {
			otherOrg := uuid.New()
			Expect(testutil.CreateTestOrganization(ctx, organizationStore, otherOrg)).To(Succeed())
			past := time.Now().Add(-time.Hour)
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("org-a", &past))).To(Succeed())
			otherPrep := &model.DeltaPrepare{
				OrgID:    otherOrg,
				Kind:     domain.FleetKind,
				Name:     "org-b",
				Deadline: &past,
			}
			Expect(deltaStore.CreateDeltaPrepare(ctx, otherPrep)).To(Succeed())

			listed, err := deltaStore.ListWaitingPastDeadline(ctx, deltastore.MaxListWaitingPastDeadline, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).To(HaveLen(2))
			names := []string{listed[0].Name, listed[1].Name}
			Expect(names).To(ConsistOf("org-a", "org-b"))
		})

		It("should return at most limit rows ordered by deadline then id", func() {
			earlier := time.Now().Add(-2 * time.Hour)
			later := time.Now().Add(-time.Hour)
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("first", &earlier))).To(Succeed())
			Expect(deltaStore.CreateDeltaPrepare(ctx, fleetPrepare("second", &later))).To(Succeed())

			listed, err := deltaStore.ListWaitingPastDeadline(ctx, 1, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).To(HaveLen(1))
			Expect(listed[0].Name).To(Equal("first"))
		})
	})

	Context("When joining a prepare to a missing parent", func() {
		It("should return ErrResourceNotFound if the prepare does not exist", func() {
			g := generation(orgId, "quay.io/team-a/os")
			insertGens(g)

			err := createDeltaPrepareGenerations(ctx, deltaStore, uuid.New(), []deltastore.GenerationKey{keyOf(g)})
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return ErrResourceNotFound if the generation does not exist", func() {
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())

			err := createDeltaPrepareGenerations(ctx, deltaStore, prep.ID, []deltastore.GenerationKey{keyOf(generation(orgId, "quay.io/missing/os"))})
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})

	Context("When getting a missing prepare", func() {
		It("should return ErrResourceNotFound", func() {
			_, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: uuid.New()})
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})

	Context("When updating a waiting prepare to complete", func() {
		It("should update the full object and reject a stale resource_version", func() {
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())

			prep.Status = model.DeltaPrepareComplete
			updated, err := deltaStore.UpdateDeltaPrepare(ctx, prep.ResourceVersion, prep)
			Expect(err).ToNot(HaveOccurred())

			got, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: prep.ID})
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaPrepareComplete))

			prep.Status = model.DeltaPrepareFailed
			_, err = deltaStore.UpdateDeltaPrepare(ctx, updated.ResourceVersion-1, prep)
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})
	})

	Context("When updating a generation with a stale resource_version", func() {
		It("should leave the row unchanged", func() {
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = model.DeltaGenerationInProgress
			g.ResourceVersion = 1
			insertGens(g)

			ref := "oci://delta"
			size := int64(9)
			stored, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			stored.Status = model.DeltaGenerationSucceeded
			stored.DeltaRef = &ref
			stored.SizeBytes = &size
			_, err = deltaStore.UpdateDeltaGeneration(ctx, 0, stored)
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationInProgress))
			Expect(got.ResourceVersion).To(Equal(int64(1)))
			Expect(got.DeltaRef).To(BeNil())
		})
	})

	Context("When updating a generation through the resource API", func() {
		It("should persist the full object and bump resource_version", func() {
			g := generation(orgId, "quay.io/team-a/os")
			_, err := deltaStore.InsertDeltaGenerations(ctx, []*model.DeltaGeneration{g})
			Expect(err).ToNot(HaveOccurred())

			stored, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			phase := string(domain.DeltaGenerationPhasePush)
			ref := "oci://delta"
			size := int64(42)
			stored.Status = model.DeltaGenerationSucceeded
			stored.Phase = &phase
			stored.DeltaRef = &ref
			stored.SizeBytes = &size

			updated, err := deltaStore.UpdateDeltaGeneration(ctx, stored.ResourceVersion, stored)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated.ResourceVersion).To(Equal(stored.ResourceVersion + 1))
			Expect(updated.Status).To(Equal(model.DeltaGenerationSucceeded))
			Expect(updated.Phase).ToNot(BeNil())
			Expect(*updated.Phase).To(Equal(phase))
			Expect(updated.DeltaRef).ToNot(BeNil())
			Expect(*updated.DeltaRef).To(Equal(ref))
			Expect(*updated.SizeBytes).To(Equal(size))

			stale := *updated
			stale.Status = model.DeltaGenerationFailed
			_, err = deltaStore.UpdateDeltaGeneration(ctx, stored.ResourceVersion, &stale)
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})
	})

	Context("When updating a prepare through the resource API", func() {
		It("should persist the full object and enforce resource_version", func() {
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())

			stored, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: prep.ID})
			Expect(err).ToNot(HaveOccurred())
			stored.Status = model.DeltaPrepareComplete
			updated, err := deltaStore.UpdateDeltaPrepare(ctx, stored.ResourceVersion, stored)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated.Status).To(Equal(model.DeltaPrepareComplete))
			Expect(updated.ResourceVersion).To(Equal(stored.ResourceVersion + 1))

			_, err = deltaStore.UpdateDeltaPrepare(ctx, stored.ResourceVersion, updated)
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})
	})

	Context("When transitioning a prepare through the legacy adapter", func() {
		It("should bump resource_version with the status transition", func() {
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())
			initialRV := prep.ResourceVersion

			Expect(deltaStore.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareComplete)).To(Succeed())

			updated, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: prep.ID})
			Expect(err).ToNot(HaveOccurred())
			Expect(updated.Status).To(Equal(model.DeltaPrepareComplete))
			Expect(updated.ResourceVersion).To(Equal(initialRV + 1))

			updated.Status = model.DeltaPrepareFailed
			_, err = deltaStore.UpdateDeltaPrepare(ctx, initialRV, updated)
			Expect(err).To(MatchError(flterrors.ErrNoRowsUpdated))
		})
	})

	Context("When listing prepare-generation joins with optional filters", func() {
		It("should support both directions and an unfiltered query", func() {
			g := generation(orgId, "quay.io/team-a/os")
			insertGens(g)
			prep := fleetPrepare("myfleet", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())
			join := &model.DeltaPrepareGeneration{
				PrepareID: prep.ID, OrgID: g.OrgID, ImageRepository: g.ImageRepository,
				SourceDigest: g.SourceDigest, TargetDigest: g.TargetDigest,
			}
			_, err := deltaStore.CreateDeltaPrepareGenerations(ctx, []*model.DeltaPrepareGeneration{join})
			Expect(err).To(Succeed())

			all, err := deltaStore.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{})
			Expect(err).ToNot(HaveOccurred())
			Expect(all).To(HaveLen(1))

			byPrepare, err := deltaStore.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{PrepareID: &prep.ID})
			Expect(err).ToNot(HaveOccurred())
			Expect(byPrepare).To(HaveLen(1))

			key := keyOf(g)
			byGeneration, err := deltaStore.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{GenerationKey: &key})
			Expect(err).ToNot(HaveOccurred())
			Expect(byGeneration).To(HaveLen(1))
		})

		It("should list more rows than one query batch for a composite-key model", func() {
			const generationCount = 501
			generations := make([]*model.DeltaGeneration, generationCount)
			keys := make([]deltastore.GenerationKey, generationCount)
			for i := range generations {
				generations[i] = generation(orgId, fmt.Sprintf("quay.io/team-a/os-%03d", i))
				keys[i] = keyOf(generations[i])
			}
			insertGens(generations...)

			prep := fleetPrepare("many-generations", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())
			Expect(createDeltaPrepareGenerations(ctx, deltaStore, prep.ID, keys)).To(Succeed())

			listed, err := deltaStore.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{PrepareID: &prep.ID})
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).To(HaveLen(generationCount))
		})

		It("should list every prepare waiting on one generation", func() {
			const prepareCount = 1001
			g := generation(orgId, "quay.io/team-a/os")
			insertGens(g)

			joins := make([]*model.DeltaPrepareGeneration, prepareCount)
			for i := range joins {
				prep := fleetPrepare(fmt.Sprintf("many-prepares-%04d", i), nil)
				Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())
				joins[i] = &model.DeltaPrepareGeneration{
					PrepareID: prep.ID, OrgID: g.OrgID, ImageRepository: g.ImageRepository,
					SourceDigest: g.SourceDigest, TargetDigest: g.TargetDigest,
				}
			}
			_, err := deltaStore.CreateDeltaPrepareGenerations(ctx, joins)
			Expect(err).ToNot(HaveOccurred())

			key := keyOf(g)
			listed, err := deltaStore.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{GenerationKey: &key})
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).To(HaveLen(prepareCount))
		})
	})

	Context("When inserting a rejected generation for a new key", func() {
		It("should persist rejected status and size_bytes", func() {
			size := int64(128)
			g := generation(orgId, "quay.io/team-a/os")
			g.SizeBytes = &size
			Expect(insertRejectedGeneration(ctx, deltaStore, g)).To(Succeed())

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationRejected))
			Expect(got.SizeBytes).ToNot(BeNil())
			Expect(*got.SizeBytes).To(Equal(size))
		})
	})

	Context("When inserting a rejected generation over a failed row", func() {
		It("should set rejected, refresh size_bytes, and bump resource_version", func() {
			oldSize := int64(10)
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = model.DeltaGenerationFailed
			g.ResourceVersion = 3
			g.SizeBytes = &oldSize
			insertGens(g)

			newSize := int64(99)
			incoming := generation(orgId, "quay.io/team-a/os")
			incoming.SizeBytes = &newSize
			Expect(insertRejectedGeneration(ctx, deltaStore, incoming)).To(Succeed())

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationRejected))
			Expect(got.ResourceVersion).To(Equal(int64(4)))
			Expect(got.SizeBytes).ToNot(BeNil())
			Expect(*got.SizeBytes).To(Equal(newSize))
		})
	})

	Context("When inserting a rejected generation over a pending row", func() {
		It("should set rejected and refresh size_bytes", func() {
			g := generation(orgId, "quay.io/team-a/os")
			insertGens(g)

			newSize := int64(55)
			incoming := generation(orgId, "quay.io/team-a/os")
			incoming.SizeBytes = &newSize
			Expect(insertRejectedGeneration(ctx, deltaStore, incoming)).To(Succeed())

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationRejected))
			Expect(got.SizeBytes).ToNot(BeNil())
			Expect(*got.SizeBytes).To(Equal(newSize))
		})
	})

	Context("When inserting a rejected generation over an existing rejected row", func() {
		It("should refresh size_bytes and leave status rejected", func() {
			oldSize := int64(10)
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = model.DeltaGenerationRejected
			g.SizeBytes = &oldSize
			insertGens(g)

			newSize := int64(44)
			incoming := generation(orgId, "quay.io/team-a/os")
			incoming.SizeBytes = &newSize
			Expect(insertRejectedGeneration(ctx, deltaStore, incoming)).To(Succeed())

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaGenerationRejected))
			Expect(got.SizeBytes).ToNot(BeNil())
			Expect(*got.SizeBytes).To(Equal(newSize))
		})
	})

	DescribeTable("When inserting a rejected generation over a protected status",
		func(status string) {
			size := int64(7)
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = status
			g.ResourceVersion = 5
			g.SizeBytes = &size
			insertGens(g)

			incoming := generation(orgId, "quay.io/team-a/os")
			newSize := int64(100)
			incoming.SizeBytes = &newSize
			Expect(insertRejectedGeneration(ctx, deltaStore, incoming)).To(Succeed())

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(status))
			Expect(got.ResourceVersion).To(Equal(int64(5)))
			Expect(got.SizeBytes).ToNot(BeNil())
			Expect(*got.SizeBytes).To(Equal(size))
		},
		Entry("succeeded", model.DeltaGenerationSucceeded),
		Entry("in_progress", model.DeltaGenerationInProgress),
	)

	Context("When listing prepare-generation joins by generation", func() {
		It("should return only joins for that key", func() {
			g := generation(orgId, "quay.io/team-a/os")
			other := generation(orgId, "quay.io/team-b/os")
			insertGens(g, other)

			waiting := fleetPrepare("waiting-fleet", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, waiting)).To(Succeed())
			Expect(createDeltaPrepareGenerations(ctx, deltaStore, waiting.ID, []deltastore.GenerationKey{keyOf(g)})).To(Succeed())

			complete := fleetPrepare("complete-fleet", nil)
			complete.Status = model.DeltaPrepareComplete
			Expect(deltaStore.CreateDeltaPrepare(ctx, complete)).To(Succeed())
			Expect(createDeltaPrepareGenerations(ctx, deltaStore, complete.ID, []deltastore.GenerationKey{keyOf(g)})).To(Succeed())

			otherWaiting := fleetPrepare("other-fleet", nil)
			Expect(deltaStore.CreateDeltaPrepare(ctx, otherWaiting)).To(Succeed())
			Expect(createDeltaPrepareGenerations(ctx, deltaStore, otherWaiting.ID, []deltastore.GenerationKey{keyOf(other)})).To(Succeed())

			key := keyOf(g)
			listed, err := deltaStore.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{GenerationKey: &key})
			Expect(err).ToNot(HaveOccurred())
			Expect(listed).To(HaveLen(2))
			prepareIDs := []uuid.UUID{listed[0].PrepareID, listed[1].PrepareID}
			Expect(prepareIDs).To(ConsistOf(waiting.ID, complete.ID))
		})
	})

	Context("When updating a generation with the current resource_version", func() {
		It("should write fields and bump resource_version", func() {
			g := generation(orgId, "quay.io/team-a/os")
			g.Status = model.DeltaGenerationInProgress
			g.ResourceVersion = 1
			insertGens(g)

			ref := "oci://delta"
			size := int64(9)
			stored, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
			Expect(err).ToNot(HaveOccurred())
			stored.Status = model.DeltaGenerationSucceeded
			stored.DeltaRef = &ref
			stored.SizeBytes = &size
			_, err = deltaStore.UpdateDeltaGeneration(ctx, 1, stored)
			Expect(err).ToNot(HaveOccurred())

			got, err := deltaStore.GetDeltaGeneration(ctx, keyOf(g))
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
			Expect(deltaStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())
			Expect(createDeltaPrepareGenerations(ctx, deltaStore, prep.ID, []deltastore.GenerationKey{keyOf(g)})).To(Succeed())

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
