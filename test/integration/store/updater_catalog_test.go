package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	updaterstore "github.com/flightctl/flightctl/internal/store/updatercatalog"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

func updaterHash(value string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value))) }
func updaterCandidate(revision int64, version string) updaterstore.Candidate {
	c := updaterstore.Candidate{Revision: revision, InputDigest: updaterHash(fmt.Sprintf("%d/%s", revision, version))}
	if version != "" {
		raw := fmt.Sprintf(`{"name":"orin","version":%q}`, version)
		c.Releases = []updaterstore.Release{{Name: "orin", Version: version, DescriptorDigest: updaterHash(raw), Descriptor: []byte(raw), Schemas: []updaterstore.Schema{{Digest: updaterHash("config-" + version), ObservationSchema: []byte(`{"type":"object"}`), BudgetKeys: []string{"maxCycles"}}}}}
	}
	return c
}

var _ = Describe("Updater catalog persistence", func() {
	var (
		ctx     context.Context
		cfg     *config.Config
		dbName  string
		db      *gorm.DB
		catalog *updaterstore.Store
	)
	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, flightlog.InitLogs(), "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		catalog, err = updaterstore.NewStore(db)
		Expect(err).NotTo(HaveOccurred())
	})
	AfterEach(func() { Expect(testdb.DeleteTestDB(ctx, flightlog.InitLogs(), cfg, db, dbName)).To(Succeed()) })
	It("When identities contain prefixes and punctuation it should return the same canonical view on activation replay and restart", func() {
		c := updaterCandidate(1, "1")
		extra := c.Releases[0]
		c.Releases[0].Name = "a"
		c.Releases[0].Descriptor = []byte(`{"name":"a","version":"1"}`)
		c.Releases[0].DescriptorDigest = updaterHash(string(c.Releases[0].Descriptor))
		extra.Name = "a-b"
		extra.Descriptor = []byte(`{"name":"a-b","version":"1"}`)
		extra.DescriptorDigest = updaterHash(string(extra.Descriptor))
		c.Releases = append(c.Releases, extra)
		accepted, err := catalog.Activate(ctx, c)
		Expect(err).NotTo(HaveOccurred())
		replay, err := catalog.Activate(ctx, c)
		Expect(err).NotTo(HaveOccurred())
		Expect(replay.Snapshot).To(Equal(accepted.Snapshot))
		restarted, err := updaterstore.NewStore(db.Session(&gorm.Session{NewDB: true}))
		Expect(err).NotTo(HaveOccurred())
		snapshot, err := restarted.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot).To(Equal(accepted.Snapshot))
		omitted := updaterCandidate(2, "")
		revoked, err := catalog.Activate(ctx, omitted)
		Expect(err).NotTo(HaveOccurred())
		replay, err = catalog.Activate(ctx, omitted)
		Expect(err).NotTo(HaveOccurred())
		Expect(replay.Snapshot).To(Equal(revoked.Snapshot))
		snapshot, err = restarted.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot).To(Equal(revoked.Snapshot))

	})

	It("When migration detects an unexpected policy digest mismatch it should fail closed", func() {
		Expect(catalog.InitialMigration(ctx)).To(Succeed())
		_, err := catalog.Activate(ctx, updaterCandidate(1, "1"))
		Expect(err).NotTo(HaveOccurred())
		legacyDigest := updaterHash("legacy materialization")
		Expect(db.WithContext(ctx).Model(&model.UpdaterCatalogHead{}).Where("id = ?", 1).Update("policy_digest", legacyDigest).Error).To(Succeed())
		err = catalog.InitialMigration(ctx)
		Expect(err).To(MatchError(ContainSubstring("accepted updater policy digest is inconsistent")))
		s, err := catalog.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Revision).To(Equal(int64(1)))
		Expect(s.Releases).To(HaveLen(1))
		Expect(s.PolicyDigest).To(Equal(legacyDigest))
	})
	It("When revision two omits a release it should replay the same file before requiring retained tombstones", func() {
		first, err := catalog.Activate(ctx, updaterCandidate(1, "1"))
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Changed).To(BeTrue())
		c := updaterCandidate(2, "")
		second, err := catalog.Activate(ctx, c)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Snapshot.Tombstones).To(HaveLen(1))
		Expect(second.Snapshot.Releases).To(HaveLen(1))
		Expect(second.Snapshot.Schemas).To(HaveLen(1))
		replay, err := catalog.Activate(ctx, c)
		Expect(err).NotTo(HaveOccurred())
		Expect(replay.Changed).To(BeFalse())
		Expect(replay.Snapshot).To(Equal(second.Snapshot))
		pending, err := catalog.PendingRefresh(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(HaveLen(2))
		_, err = catalog.Activate(ctx, updaterCandidate(3, ""))
		Expect(err).To(MatchError(updaterstore.ErrHistoryConflict))
		next := updaterCandidate(3, "")
		next.Revocations = second.Snapshot.Tombstones
		third, err := catalog.Activate(ctx, next)
		Expect(err).NotTo(HaveOccurred())
		Expect(third.Snapshot.PolicyDigest).NotTo(Equal(second.Snapshot.PolicyDigest))
	})
	It("When a name/version is retargeted or a revision conflicts it should preserve all accepted state", func() {
		_, err := catalog.Activate(ctx, updaterCandidate(1, "1"))
		Expect(err).NotTo(HaveOccurred())
		before, err := catalog.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		c := updaterCandidate(2, "1")
		c.Releases[0].Descriptor = []byte(`{"changed":true}`)
		c.Releases[0].DescriptorDigest = updaterHash(string(c.Releases[0].Descriptor))
		_, err = catalog.Activate(ctx, c)
		Expect(err).To(MatchError(updaterstore.ErrHistoryConflict))
		c = updaterCandidate(1, "2")
		_, err = catalog.Activate(ctx, c)
		Expect(err).To(MatchError(updaterstore.ErrRevisionConflict))
		c = updaterCandidate(2, "1")
		_, err = catalog.Activate(ctx, c)
		Expect(err).NotTo(HaveOccurred())
		_, err = catalog.Activate(ctx, updaterCandidate(1, "1"))
		Expect(err).To(MatchError(updaterstore.ErrStaleRevision))
		after, err := catalog.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Releases).To(Equal(before.Releases))
	})
	It("When historical evidence conflicts after revocation it should reject the entire activation", func() {
		original := updaterCandidate(1, "1")
		_, err := catalog.Activate(ctx, original)
		Expect(err).NotTo(HaveOccurred())
		revoked, err := catalog.Activate(ctx, updaterCandidate(2, ""))
		Expect(err).NotTo(HaveOccurred())
		c := updaterCandidate(3, "2")
		c.Revocations = revoked.Snapshot.Tombstones
		c.Releases[0].Schemas[0].Digest = original.Releases[0].Schemas[0].Digest
		c.Releases[0].Schemas[0].BudgetKeys = []string{"maxReboots"}
		_, err = catalog.Activate(ctx, c)
		Expect(err).To(MatchError(updaterstore.ErrHistoryConflict))
		c.Releases[0].Schemas[0].BudgetKeys = original.Releases[0].Schemas[0].BudgetKeys
		c.Releases[0].Schemas[0].ObservationSchema = []byte(`{"type":"object","properties":{"other":{"type":"string"}}}`)
		_, err = catalog.Activate(ctx, c)
		Expect(err).To(MatchError(updaterstore.ErrHistoryConflict))
		c.Releases[0].Schemas[0] = original.Releases[0].Schemas[0]
		_, err = catalog.Activate(ctx, c)
		Expect(err).NotTo(HaveOccurred())
		c = updaterCandidate(4, "1")
		c.Revocations = revoked.Snapshot.Tombstones
		_, err = catalog.Activate(ctx, c)
		Expect(err).To(HaveOccurred())
	})
	It("When activation is concurrent it should commit one snapshot and one refresh per revision", func() {
		c := updaterCandidate(1, "1")
		results := make(chan updaterstore.ActivationResult, 8)
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); r, e := catalog.Activate(ctx, c); results <- r; errs <- e }()
		}
		wg.Wait()
		close(results)
		close(errs)
		for e := range errs {
			Expect(e).NotTo(HaveOccurred())
		}
		changed := 0
		for r := range results {
			if r.Changed {
				changed++
			}
		}
		Expect(changed).To(Equal(1))
		pending, err := catalog.PendingRefresh(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(HaveLen(1))
	})
	It("When different inputs race for one revision it should reject the loser without partial state", func() {
		candidates := []updaterstore.Candidate{updaterCandidate(1, "1"), updaterCandidate(1, "2")}
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, c := range candidates {
			wg.Add(1)
			go func(c updaterstore.Candidate) { defer wg.Done(); _, err := catalog.Activate(ctx, c); errs <- err }(c)
		}
		wg.Wait()
		close(errs)
		successes, conflicts := 0, 0
		for err := range errs {
			if err == nil {
				successes++
			} else {
				Expect(err).To(MatchError(updaterstore.ErrRevisionConflict))
				conflicts++
			}
		}
		Expect(successes).To(Equal(1))
		Expect(conflicts).To(Equal(1))
		snapshot, err := catalog.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.Releases).To(HaveLen(1))
		Expect(snapshot.Schemas).To(HaveLen(1))
		pending, err := catalog.PendingRefresh(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(HaveLen(1))
	})
	It("When higher revisions race it should never roll accepted policy backward", func() {
		_, err := catalog.Activate(ctx, updaterCandidate(1, "1"))
		Expect(err).NotTo(HaveOccurred())
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, revision := range []int64{2, 3} {
			wg.Add(1)
			go func(revision int64) {
				defer wg.Done()
				_, err := catalog.Activate(ctx, updaterCandidate(revision, "1"))
				errs <- err
			}(revision)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				Expect(err).To(MatchError(updaterstore.ErrStaleRevision))
			}
		}
		snapshot, err := catalog.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.Revision).To(Equal(int64(3)))
		Expect(snapshot.Releases).To(HaveLen(1))
	})

	It("When requests are canceled or metadata is invalid it should leave accepted state untouched", func() {
		bad := updaterCandidate(0, "1")
		_, err := catalog.Activate(ctx, bad)
		Expect(err).To(HaveOccurred())
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = catalog.Activate(canceled, updaterCandidate(1, "1"))
		Expect(err).To(HaveOccurred())
		_, err = catalog.Snapshot(canceled)
		Expect(err).To(HaveOccurred())
		for _, limit := range []int{0, -1, 257} {
			_, err = catalog.PendingRefresh(ctx, limit)
			Expect(err).To(HaveOccurred())
		}
		snapshot, err := catalog.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.Revision).To(BeZero())
	})
	It("When retained tombstones are changed or unknown it should reject without restoring approval", func() {
		_, err := catalog.Activate(ctx, updaterCandidate(1, "1"))
		Expect(err).NotTo(HaveOccurred())
		revoked, err := catalog.Activate(ctx, updaterCandidate(2, ""))
		Expect(err).NotTo(HaveOccurred())
		for _, change := range []func(*model.UpdaterRevocation){func(r *model.UpdaterRevocation) { r.Reason = "changed" }, func(r *model.UpdaterRevocation) { r.DescriptorDigest = updaterHash("other") }, func(r *model.UpdaterRevocation) { r.Name = "unknown" }} {
			next := updaterCandidate(3, "")
			next.Revocations = append([]model.UpdaterRevocation(nil), revoked.Snapshot.Tombstones...)
			change(&next.Revocations[0])
			_, err = catalog.Activate(ctx, next)
			Expect(err).To(MatchError(updaterstore.ErrHistoryConflict))
		}
		snapshot, err := catalog.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot).To(Equal(revoked.Snapshot))
	})
	It("When descriptor capacity is exhausted it should reject growth but still allow revocation", func() {
		original := updaterCandidate(1, "1")
		raw := `{"name":"orin","version":"1","padding":"` + strings.Repeat("x", 4*1024*1024-64) + `"}`
		original.Releases[0].Descriptor = []byte(raw)
		original.Releases[0].DescriptorDigest = updaterHash(raw)
		_, err := catalog.Activate(ctx, original)
		Expect(err).NotTo(HaveOccurred())
		growing := updaterCandidate(2, "2")
		growing.Releases = append(growing.Releases, original.Releases[0])
		growing.Releases[0].Descriptor = []byte(`{"name":"orin","version":"2","padding":"` + strings.Repeat("x", 1024) + `"}`)
		growing.Releases[0].DescriptorDigest = updaterHash(string(growing.Releases[0].Descriptor))
		_, err = catalog.Activate(ctx, growing)
		Expect(err).To(HaveOccurred())
		revoked, err := catalog.Activate(ctx, updaterCandidate(2, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(revoked.Snapshot.Tombstones).To(HaveLen(1))
	})

	It("When outbox persistence fails it should roll back bindings and accepted revision", func() {
		adminDB, err := store.InitMigrationDB(cfg, flightlog.InitLogs())
		Expect(err).NotTo(HaveOccurred())
		sqlDB, err := adminDB.DB()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(sqlDB.Close()).To(Succeed()) })
		Expect(adminDB.WithContext(ctx).Exec(`ALTER TABLE updater_catalog_refreshes ADD CONSTRAINT test_fail_refresh CHECK (revision < 1)`).Error).To(Succeed())
		_, err = catalog.Activate(ctx, updaterCandidate(1, "1"))
		Expect(err).To(HaveOccurred())
		s, err := catalog.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Revision).To(BeZero())
		Expect(s.Releases).To(BeEmpty())
		Expect(s.Schemas).To(BeEmpty())
	})
	It("When a caller transaction rolls back it should also roll back catalog activation", func() {
		sentinel := errors.New("rollback")
		err := store.WithTransaction(ctx, db, func(txCtx context.Context) error {
			_, e := catalog.Activate(txCtx, updaterCandidate(1, "1"))
			if e != nil {
				return e
			}
			return sentinel
		})
		Expect(err).To(MatchError(sentinel))
		s, err := catalog.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Revision).To(BeZero())
	})
	It("When a new store instance reads after restart it should retain evidence and tombstones", func() {
		_, err := catalog.Activate(ctx, updaterCandidate(1, "1"))
		Expect(err).NotTo(HaveOccurred())
		accepted, err := catalog.Activate(ctx, updaterCandidate(2, ""))
		Expect(err).NotTo(HaveOccurred())
		restarted, err := updaterstore.NewStore(db.Session(&gorm.Session{NewDB: true}))
		Expect(err).NotTo(HaveOccurred())
		got, err := restarted.Snapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(accepted.Snapshot))
		Expect(got.Schemas).To(HaveLen(1))
		var rows int64
		Expect(db.WithContext(ctx).Model(&model.UpdaterCatalogRefresh{}).Count(&rows).Error).To(Succeed())
		Expect(rows).To(Equal(int64(2)))
	})
})
