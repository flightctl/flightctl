package store_test

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	canarystore "github.com/flightctl/flightctl/internal/store/canary"
	"github.com/flightctl/flightctl/internal/store/model"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("CanaryStore", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		canaryStr canarystore.Store
		cfg       *config.Config
		dbName    string
		db        *gorm.DB
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		canaryStr = canarystore.NewCanaryStore(db, log.WithField("pkg", "canary-store"))
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	Context("When running initial migration", func() {
		It("should create the encryption_canaries table without error", func() {
			err := canaryStr.InitialMigration(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When getting a non-existent canary", func() {
		It("should return ErrResourceNotFound", func() {
			_, err := canaryStr.Get(ctx, "v1", "nonexistent")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})
	})

	Context("When creating and getting a canary", func() {
		It("should round-trip the full record", func() {
			now := time.Now().UTC().Truncate(time.Microsecond)
			canary := &model.EncryptionCanary{
				Strategy:       "v1",
				KeyID:          "default",
				EncryptedValue: []byte("encrypted-test-value"),
				CreatedAt:      now,
			}
			err := canaryStr.Create(ctx, canary)
			Expect(err).ToNot(HaveOccurred())

			result, err := canaryStr.Get(ctx, "v1", "default")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Strategy).To(Equal("v1"))
			Expect(result.KeyID).To(Equal("default"))
			Expect(result.EncryptedValue).To(Equal([]byte("encrypted-test-value")))
			Expect(result.CreatedAt.UTC()).To(BeTemporally("~", now, time.Millisecond))
		})
	})

	Context("When creating a duplicate canary", func() {
		It("should return an error", func() {
			canary := &model.EncryptionCanary{
				Strategy:       "v1",
				KeyID:          "default",
				EncryptedValue: []byte("value-1"),
				CreatedAt:      time.Now().UTC(),
			}
			err := canaryStr.Create(ctx, canary)
			Expect(err).ToNot(HaveOccurred())

			duplicate := &model.EncryptionCanary{
				Strategy:       "v1",
				KeyID:          "default",
				EncryptedValue: []byte("value-2"),
				CreatedAt:      time.Now().UTC(),
			}
			err = canaryStr.Create(ctx, duplicate)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("When creating canaries with composite primary key", func() {
		It("should allow same keyID under different strategies", func() {
			err := canaryStr.Create(ctx, &model.EncryptionCanary{
				Strategy:       "v1",
				KeyID:          "default",
				EncryptedValue: []byte("v1-value"),
				CreatedAt:      time.Now().UTC(),
			})
			Expect(err).ToNot(HaveOccurred())

			err = canaryStr.Create(ctx, &model.EncryptionCanary{
				Strategy:       "v2",
				KeyID:          "default",
				EncryptedValue: []byte("v2-value"),
				CreatedAt:      time.Now().UTC(),
			})
			Expect(err).ToNot(HaveOccurred())

			v1, err := canaryStr.Get(ctx, "v1", "default")
			Expect(err).ToNot(HaveOccurred())
			Expect(v1.EncryptedValue).To(Equal([]byte("v1-value")))

			v2, err := canaryStr.Get(ctx, "v2", "default")
			Expect(err).ToNot(HaveOccurred())
			Expect(v2.EncryptedValue).To(Equal([]byte("v2-value")))
		})
	})

	Context("When listing canaries", func() {
		It("should return all canaries", func() {
			for _, keyID := range []string{"key-1", "key-2", "key-3"} {
				err := canaryStr.Create(ctx, &model.EncryptionCanary{
					Strategy:       "v1",
					KeyID:          keyID,
					EncryptedValue: []byte("value-" + keyID),
					CreatedAt:      time.Now().UTC(),
				})
				Expect(err).ToNot(HaveOccurred())
			}

			results, err := canaryStr.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(3))
		})

		It("should return canaries across multiple strategies", func() {
			for _, s := range []string{"v1", "v2"} {
				err := canaryStr.Create(ctx, &model.EncryptionCanary{
					Strategy:       s,
					KeyID:          "default",
					EncryptedValue: []byte("value-" + s),
					CreatedAt:      time.Now().UTC(),
				})
				Expect(err).ToNot(HaveOccurred())
			}

			results, err := canaryStr.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(2))
		})

		It("should return empty list when no canaries exist", func() {
			results, err := canaryStr.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(BeEmpty())
		})
	})

	Context("When deleting a canary", func() {
		It("should remove the canary", func() {
			err := canaryStr.Create(ctx, &model.EncryptionCanary{
				Strategy:       "v1",
				KeyID:          "to-delete",
				EncryptedValue: []byte("value"),
				CreatedAt:      time.Now().UTC(),
			})
			Expect(err).ToNot(HaveOccurred())

			deleted, err := canaryStr.Delete(ctx, "v1", "to-delete")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeTrue())

			_, err = canaryStr.Get(ctx, "v1", "to-delete")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("should return false when deleting a non-existent canary", func() {
			deleted, err := canaryStr.Delete(ctx, "v1", "nonexistent")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeFalse())
		})

		It("should not affect other canaries", func() {
			for _, keyID := range []string{"keep", "delete"} {
				err := canaryStr.Create(ctx, &model.EncryptionCanary{
					Strategy:       "v1",
					KeyID:          keyID,
					EncryptedValue: []byte("value-" + keyID),
					CreatedAt:      time.Now().UTC(),
				})
				Expect(err).ToNot(HaveOccurred())
			}

			deleted, err := canaryStr.Delete(ctx, "v1", "delete")
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeTrue())

			result, err := canaryStr.Get(ctx, "v1", "keep")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())

			results, err := canaryStr.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(1))
		})
	})
})
