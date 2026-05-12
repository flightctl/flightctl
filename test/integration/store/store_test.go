package store_test

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("DataStore Migration Tests", func() {
	// createFreshDB creates a completely empty database owned by the test user.
	// The caller must call the returned cleanup function (via defer).
	createFreshDB := func(ctx context.Context, freshLog *logrus.Logger, prefix string) (*gorm.DB, func()) {
		freshCfg := config.NewDefault()
		freshDbName := prefix + uuid.New().String()[:8]
		freshCfg.Database.Name = "flightctl"
		adminDB, err := store.InitDB(freshCfg, freshLog)
		Expect(err).ToNot(HaveOccurred())
		Expect(adminDB.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, freshDbName)).Error).To(Succeed())
		store.CloseDB(adminDB)

		freshCfg.Database.Name = freshDbName
		freshGormDb, err := store.InitDB(freshCfg, freshLog)
		Expect(err).ToNot(HaveOccurred())

		cleanup := func() {
			store.CloseDB(freshGormDb)
			freshCfg.Database.Name = "flightctl"
			if adminDB, _ := store.InitDB(freshCfg, freshLog); adminDB != nil {
				Expect(adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, freshDbName)).Error).To(Succeed())
				store.CloseDB(adminDB)
			}
		}
		return freshGormDb, cleanup
	}

	// createFreshDBWithOrgs sets up a minimal database that simulates a pre-catalog
	// installation: only the organizations table exists, with the given orgs pre-inserted.
	// The caller must call the returned cleanup function (via defer).
	createFreshDBWithOrgs := func(ctx context.Context, freshLog *logrus.Logger, orgIDs []uuid.UUID) (*gorm.DB, func()) {
		freshGormDb, cleanup := createFreshDB(ctx, freshLog, "test_backfill_")

		db := freshGormDb.WithContext(ctx)
		Expect(db.AutoMigrate(&model.Organization{})).To(Succeed())
		Expect(db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS org_external_id_idx
			ON organizations (external_id) WHERE external_id <> ''`).Error).To(Succeed())

		for _, id := range orgIDs {
			Expect(db.Create(&model.Organization{
				ID:          id,
				ExternalID:  "ext-" + id.String()[:8],
				DisplayName: "Org " + id.String()[:8],
			}).Error).To(Succeed())
		}

		return freshGormDb, cleanup
	}

	Context("Default catalog backfill", func() {
		It("When upgrading from a pre-catalog installation, it should create a default catalog for every org", func() {
			freshCtx := testutil.StartSpecTracerForGinkgo(suiteCtx)
			freshLog := flightlog.InitLogs()

			org1ID := uuid.New()
			org2ID := uuid.New()
			freshGormDb, cleanup := createFreshDBWithOrgs(freshCtx, freshLog, []uuid.UUID{org1ID, org2ID})
			defer cleanup()

			freshStore := store.NewStore(freshGormDb, freshLog.WithField("pkg", "store"))
			Expect(freshStore.RunMigrations(freshCtx)).To(Succeed())

			for _, orgID := range []uuid.UUID{org1ID, org2ID} {
				catalog, err := freshStore.Catalog().Get(freshCtx, orgID, domain.DefaultCatalogName)
				Expect(err).ToNot(HaveOccurred(), "org %s should have a default catalog after upgrade", orgID)
				Expect(*catalog.Metadata.Name).To(Equal(domain.DefaultCatalogName))
				Expect(*catalog.Spec.DisplayName).To(Equal(domain.DefaultCatalogDisplayName))
			}
		})

		It("When upgrading, it should not create a default catalog for orgs that already have a catalog", func() {
			freshCtx := testutil.StartSpecTracerForGinkgo(suiteCtx)
			freshLog := flightlog.InitLogs()

			orgWithCatalogID := uuid.New()
			orgWithoutCatalogID := uuid.New()
			freshGormDb, cleanup := createFreshDBWithOrgs(freshCtx, freshLog, []uuid.UUID{orgWithCatalogID, orgWithoutCatalogID})
			defer cleanup()

			// Also migrate the catalogs table and pre-insert a catalog for one org,
			// simulating an installation that already has a custom catalog.
			db := freshGormDb.WithContext(freshCtx)
			Expect(db.AutoMigrate(&model.Catalog{})).To(Succeed())
			customDisplayName := "My Custom Catalog"
			Expect(db.Create(&model.Catalog{
				Resource: model.Resource{OrgID: orgWithCatalogID, Name: "my-custom-catalog"},
				Spec:     model.MakeJSONField(domain.CatalogSpec{DisplayName: &customDisplayName}),
			}).Error).To(Succeed())

			freshStore := store.NewStore(freshGormDb, freshLog.WithField("pkg", "store"))
			Expect(freshStore.RunMigrations(freshCtx)).To(Succeed())

			// Org with no prior catalog should have received a default catalog.
			catalog, err := freshStore.Catalog().Get(freshCtx, orgWithoutCatalogID, domain.DefaultCatalogName)
			Expect(err).ToNot(HaveOccurred())
			Expect(*catalog.Metadata.Name).To(Equal(domain.DefaultCatalogName))

			// Org that already had a catalog should still have exactly one catalog.
			catalogs, err := freshStore.Catalog().List(freshCtx, orgWithCatalogID, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(catalogs.Items).To(HaveLen(1), "org with an existing catalog must not receive an additional default catalog")
			Expect(*catalogs.Items[0].Metadata.Name).To(Equal("my-custom-catalog"))
		})

		It("When an org only has owned catalogs, it should still receive a default catalog", func() {
			freshCtx := testutil.StartSpecTracerForGinkgo(suiteCtx)
			freshLog := flightlog.InitLogs()

			orgID := uuid.New()
			freshGormDb, cleanup := createFreshDBWithOrgs(freshCtx, freshLog, []uuid.UUID{orgID})
			defer cleanup()

			// Migrate the catalogs table and pre-insert a catalog that has an owner
			// (e.g. created by a ResourceSync), simulating an installation where all existing
			// catalogs are owned resources.
			db := freshGormDb.WithContext(freshCtx)
			Expect(db.AutoMigrate(&model.Catalog{})).To(Succeed())
			ownedCatalogName := "rs-owned-catalog"
			ownerRef := "ResourceSync/some-resourcesync"
			Expect(db.Create(&model.Catalog{
				Resource: model.Resource{OrgID: orgID, Name: ownedCatalogName, Owner: &ownerRef},
				Spec:     model.MakeJSONField(domain.CatalogSpec{}),
			}).Error).To(Succeed())

			freshStore := store.NewStore(freshGormDb, freshLog.WithField("pkg", "store"))
			Expect(freshStore.RunMigrations(freshCtx)).To(Succeed())

			// The org only had owned catalogs, so the backfill must add the default one.
			defaultCatalog, err := freshStore.Catalog().Get(freshCtx, orgID, domain.DefaultCatalogName)
			Expect(err).ToNot(HaveOccurred(), "org with only owned catalogs should receive a default catalog")
			Expect(*defaultCatalog.Metadata.Name).To(Equal(domain.DefaultCatalogName))

			// The owned catalog must still be there – nothing was deleted.
			catalogs, err := freshStore.Catalog().List(freshCtx, orgID, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(catalogs.Items).To(HaveLen(2), "org should have both the owned catalog and the new default catalog")
		})

		It("When running migrations again after the user deleted the default catalog, it should not recreate it", func() {
			freshCtx := testutil.StartSpecTracerForGinkgo(suiteCtx)
			freshLog := flightlog.InitLogs()

			// Use a fresh DB so the test user owns all tables and can run RunMigrations
			// multiple times without DDL permission errors (which occur with the template
			// DB strategy where tables are owned by the admin user, not the test user).
			freshGormDb, cleanup := createFreshDB(freshCtx, freshLog, "test_backfill_restart_")
			defer cleanup()

			freshStore := store.NewStore(freshGormDb, freshLog.WithField("pkg", "store"))

			// First migration: creates schema, backfills catalogs, records migration key.
			Expect(freshStore.RunMigrations(freshCtx)).To(Succeed())

			// Verify the default catalog was created for the default org.
			_, err := freshStore.Catalog().Get(freshCtx, store.NullOrgId, domain.DefaultCatalogName)
			Expect(err).ToNot(HaveOccurred())

			// Simulate the user intentionally deleting the default catalog.
			noopCallback := store.RemoveOwnerCallback(func(_ context.Context, _ *gorm.DB, _ uuid.UUID, _ string) error {
				return nil
			})
			Expect(freshStore.Catalog().Delete(freshCtx, store.NullOrgId, domain.DefaultCatalogName, noopCallback, nil)).To(Succeed())

			// Confirm it is gone.
			_, err = freshStore.Catalog().Get(freshCtx, store.NullOrgId, domain.DefaultCatalogName)
			Expect(err).To(HaveOccurred(), "default catalog should be absent after deletion")

			// Simulate a server restart: RunMigrations runs again.
			Expect(freshStore.RunMigrations(freshCtx)).To(Succeed())

			// The catalog must not have been recreated because the migration key is already recorded.
			_, err = freshStore.Catalog().Get(freshCtx, store.NullOrgId, domain.DefaultCatalogName)
			Expect(err).To(HaveOccurred(), "default catalog deleted by user must not be recreated on restart")
		})
	})
})
