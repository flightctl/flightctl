package store_test

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("SyncStateStore", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		cfg       *config.Config
		dbName    string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)

		orgId = uuid.New()
		err := testutil.CreateTestOrganization(ctx, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
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
