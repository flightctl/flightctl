package store_test

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("SyncStateStore", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		cfg       *config.Config
		dbName    string
		db        *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		cfg, dbName, db = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		storeInst = store.NewStore(db, log.WithField("pkg", "store"))

		orgId = uuid.New()
		err := testutil.CreateTestOrganization(ctx, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		_ = storeInst.Close()
		testdb.DeleteTestDB(ctx, log, cfg, db, dbName)
	})

	Context("When running initial migration", func() {
		It("should create the sync_state table without error", func() {
			Expect(storeInst.SyncState()).ToNot(BeNil())
		})
	})

	Context("When getting a non-existent sync state", func() {
		It("should return nil without error", func() {
			result, err := storeInst.SyncState().Get(ctx, orgId, "git:nonexistent/main")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeNil())
		})
	})

	Context("When setting and getting a sync state", func() {
		It("should round-trip the full record", func() {
			now := time.Now().UTC().Truncate(time.Microsecond)
			state := &model.SyncState{
				OrgID:         orgId,
				ResourceKey:   "git:my-repo/main",
				Fingerprint:   "abc123def456",
				LastCheckedAt: now,
				LastChangeAt:  &now,
			}
			err := storeInst.SyncState().Set(ctx, orgId, state)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.SyncState().Get(ctx, orgId, "git:my-repo/main")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.OrgID).To(Equal(orgId))
			Expect(result.ResourceKey).To(Equal("git:my-repo/main"))
			Expect(result.Fingerprint).To(Equal("abc123def456"))
			Expect(result.LastCheckedAt.UTC()).To(BeTemporally("~", now, time.Millisecond))
			Expect(result.LastChangeAt).ToNot(BeNil())
			Expect(result.LastChangeAt.UTC()).To(BeTemporally("~", now, time.Millisecond))
		})
	})

	Context("When upserting a sync state", func() {
		It("should update the existing record", func() {
			now := time.Now().UTC().Truncate(time.Microsecond)
			state := &model.SyncState{
				OrgID:         orgId,
				ResourceKey:   "git:my-repo/main",
				Fingerprint:   "abc123",
				LastCheckedAt: now,
			}
			err := storeInst.SyncState().Set(ctx, orgId, state)
			Expect(err).ToNot(HaveOccurred())

			later := now.Add(5 * time.Minute)
			state.Fingerprint = "def456"
			state.LastCheckedAt = later
			state.LastChangeAt = &later
			err = storeInst.SyncState().Set(ctx, orgId, state)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.SyncState().Get(ctx, orgId, "git:my-repo/main")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Fingerprint).To(Equal("def456"))
			Expect(result.LastCheckedAt.UTC()).To(BeTemporally("~", later, time.Millisecond))
		})
	})

	Context("When updating last_checked_at only", func() {
		It("should update the timestamp without changing fingerprint", func() {
			now := time.Now().UTC().Truncate(time.Microsecond)
			state := &model.SyncState{
				OrgID:         orgId,
				ResourceKey:   "git:my-repo/main",
				Fingerprint:   "abc123",
				LastCheckedAt: now,
			}
			err := storeInst.SyncState().Set(ctx, orgId, state)
			Expect(err).ToNot(HaveOccurred())

			later := now.Add(10 * time.Minute)
			err = storeInst.SyncState().SetLastCheckedAt(ctx, orgId, "git:my-repo/main", later)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.SyncState().Get(ctx, orgId, "git:my-repo/main")
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Fingerprint).To(Equal("abc123"))
			Expect(result.LastCheckedAt.UTC()).To(BeTemporally("~", later, time.Millisecond))
		})
	})

	Context("When upserting with uuid.Nil org_id as sentinel org_id", func() {
		It("should update the existing record instead of failing on duplicate insert", func() {
			now := time.Now().UTC().Truncate(time.Microsecond)
			state := &model.SyncState{
				OrgID:         uuid.Nil,
				ResourceKey:   "secret:prod/db-creds",
				Fingerprint:   "rv1000",
				LastCheckedAt: now,
			}
			err := storeInst.SyncState().Set(ctx, uuid.Nil, state)
			Expect(err).ToNot(HaveOccurred())

			later := now.Add(5 * time.Minute)
			state.Fingerprint = "rv1001"
			state.LastCheckedAt = later
			state.LastChangeAt = &later
			err = storeInst.SyncState().Set(ctx, uuid.Nil, state)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.SyncState().Get(ctx, uuid.Nil, "secret:prod/db-creds")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Fingerprint).To(Equal("rv1001"))
			Expect(result.LastCheckedAt.UTC()).To(BeTemporally("~", later, time.Millisecond))
		})
	})

	Context("When setting and getting ProbeStatus and ProbeMessage", func() {
		It("should round-trip probe status fields", func() {
			now := time.Now().UTC().Truncate(time.Microsecond)
			state := &model.SyncState{
				OrgID:         orgId,
				ResourceKey:   "git:probe-repo/main",
				Fingerprint:   "abc123",
				LastCheckedAt: now,
				ProbeStatus:   "ProbeFailed",
				ProbeMessage:  "connection refused",
			}
			err := storeInst.SyncState().Set(ctx, orgId, state)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.SyncState().Get(ctx, orgId, "git:probe-repo/main")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.ProbeStatus).To(Equal("ProbeFailed"))
			Expect(result.ProbeMessage).To(Equal("connection refused"))
		})

		It("should persist ProbeStatus and ProbeMessage via BulkUpsert", func() {
			now := time.Now().UTC().Truncate(time.Microsecond)
			states := []model.SyncState{
				{
					ResourceKey:   "git:bulk-repo/main",
					Fingerprint:   "aaa",
					LastCheckedAt: now,
					ProbeStatus:   "Synced",
					ProbeMessage:  "",
				},
				{
					ResourceKey:   "http:bulk-repo/config",
					Fingerprint:   "bbb",
					LastCheckedAt: now,
					ProbeStatus:   "ProbeFailed",
					ProbeMessage:  "timeout",
				},
			}
			err := storeInst.SyncState().BulkUpsert(ctx, orgId, states)
			Expect(err).ToNot(HaveOccurred())

			r1, err := storeInst.SyncState().Get(ctx, orgId, "git:bulk-repo/main")
			Expect(err).ToNot(HaveOccurred())
			Expect(r1.ProbeStatus).To(Equal("Synced"))

			r2, err := storeInst.SyncState().Get(ctx, orgId, "http:bulk-repo/config")
			Expect(err).ToNot(HaveOccurred())
			Expect(r2.ProbeStatus).To(Equal("ProbeFailed"))
			Expect(r2.ProbeMessage).To(Equal("timeout"))
		})
	})

	Context("When querying with org isolation", func() {
		It("should not return records from a different org", func() {
			now := time.Now().UTC().Truncate(time.Microsecond)
			state := &model.SyncState{
				OrgID:         orgId,
				ResourceKey:   "git:my-repo/main",
				Fingerprint:   "abc123",
				LastCheckedAt: now,
			}
			err := storeInst.SyncState().Set(ctx, orgId, state)
			Expect(err).ToNot(HaveOccurred())

			otherOrg := uuid.New()
			err = testutil.CreateTestOrganization(ctx, storeInst, otherOrg)
			Expect(err).ToNot(HaveOccurred())

			result, err := storeInst.SyncState().Get(ctx, otherOrg, "git:my-repo/main")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeNil())
		})
	})
})
