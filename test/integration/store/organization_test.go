package store_test

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("OrganizationStore Integration Tests", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
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
	})

	AfterEach(func() {
		_ = storeInst.Close()
		testdb.DeleteTestDB(ctx, log, cfg, db, dbName)
	})

	Context("Organization Store", func() {
		It("Should create a default organization during initial migration", func() {
			orgs, err := storeInst.Organization().List(ctx, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(orgs).To(HaveLen(1))
			Expect(orgs[0].ID).To(Equal(store.NullOrgId))
			Expect(orgs[0].ExternalID).To(Equal(org.DefaultExternalID))
			Expect(orgs[0].DisplayName).To(Equal("Default"))
		})

		It("When initial migration runs, it should seed the default catalog for the default organization", func() {
			catalog, err := storeInst.Catalog().Get(ctx, store.NullOrgId, domain.DefaultCatalogName)
			Expect(err).ToNot(HaveOccurred())
			Expect(catalog).ToNot(BeNil())
			Expect(*catalog.Metadata.Name).To(Equal(domain.DefaultCatalogName))
			Expect(*catalog.Spec.DisplayName).To(Equal(domain.DefaultCatalogDisplayName))
		})

		It("When initial migration runs again, it should not duplicate the default catalog", func() {
			err := storeInst.Organization().InitialMigration(ctx)
			Expect(err).ToNot(HaveOccurred())

			catalogs, err := storeInst.Catalog().List(ctx, store.NullOrgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			defaultCatalogs := 0
			for _, c := range catalogs.Items {
				if *c.Metadata.Name == domain.DefaultCatalogName {
					defaultCatalogs++
				}
			}
			Expect(defaultCatalogs).To(Equal(1), "Default catalog should not be duplicated on repeated InitialMigration")
		})

		It("Should create a new organization with provided ID", func() {
			orgID := uuid.New()
			externalID := "test-external-id"
			displayName := "Test Organization"

			org := &model.Organization{
				ID:          orgID,
				DisplayName: displayName,
				ExternalID:  externalID,
			}

			createdOrg, err := storeInst.Organization().Create(ctx, org)
			Expect(err).ToNot(HaveOccurred())
			Expect(createdOrg).ToNot(BeNil())
			Expect(createdOrg.ID).To(Equal(orgID))
			Expect(createdOrg.DisplayName).To(Equal(displayName))
			Expect(createdOrg.ExternalID).To(Equal(externalID))
			Expect(createdOrg.CreatedAt).ToNot(BeZero())
			Expect(createdOrg.UpdatedAt).ToNot(BeZero())
		})

		It("Should generate UUID when not provided", func() {
			displayName := "Auto UUID Test Org"
			externalID := "auto-uuid-test"
			org := &model.Organization{
				DisplayName: displayName,
				ExternalID:  externalID,
			}

			createdOrg, err := storeInst.Organization().Create(ctx, org)
			Expect(err).ToNot(HaveOccurred())
			Expect(createdOrg).ToNot(BeNil())
			Expect(createdOrg.ID).ToNot(Equal(uuid.Nil))
			Expect(createdOrg.DisplayName).To(Equal(displayName))
			Expect(createdOrg.ExternalID).To(Equal(externalID))
		})

		It("Should list all organizations", func() {
			externalID1 := "org-1"
			externalID2 := "org-2"
			org1 := &model.Organization{
				ID:          uuid.New(),
				DisplayName: "Organization 1",
				ExternalID:  externalID1,
			}
			org2 := &model.Organization{
				ID:          uuid.New(),
				DisplayName: "Organization 2",
				ExternalID:  externalID2,
			}

			_, err := storeInst.Organization().Create(ctx, org1)
			Expect(err).ToNot(HaveOccurred())

			_, err = storeInst.Organization().Create(ctx, org2)
			Expect(err).ToNot(HaveOccurred())

			orgs, err := storeInst.Organization().List(ctx, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(orgs).To(HaveLen(3))

			// Ensure the non-default orgs exist as expected
			orgMap := make(map[string]*model.Organization)
			for _, org := range orgs {
				if org.ExternalID != "" {
					orgMap[org.ExternalID] = org
				}
			}

			Expect(orgMap).To(HaveKey("org-1"))
			Expect(orgMap).To(HaveKey("org-2"))
			Expect(orgMap["org-1"].DisplayName).To(Equal("Organization 1"))
			Expect(orgMap["org-2"].DisplayName).To(Equal("Organization 2"))
		})

		It("Should error with empty DisplayName", func() {
			externalID := "no-display-name"
			org := &model.Organization{
				ID:         uuid.New(),
				ExternalID: externalID,
				// DisplayName is intentionally left empty
			}

			createdOrg, err := storeInst.Organization().Create(ctx, org)

			Expect(err).To(HaveOccurred())
			Expect(createdOrg).To(BeNil())
		})

		It("Should support field selector", func() {

			u1 := uuid.MustParse("00000000-0000-0000-0000-000000000011")
			u2 := uuid.MustParse("00000000-0000-0000-0000-000000000022")
			u3 := uuid.MustParse("00000000-0000-0000-0000-000000000033")
			u4 := uuid.MustParse("00000000-0000-0000-0000-000000000044")

			// Insert four orgs
			_, err := storeInst.Organization().Create(ctx, &model.Organization{ID: u1, DisplayName: "Org-11", ExternalID: "ext-11"})
			Expect(err).ToNot(HaveOccurred())
			_, err = storeInst.Organization().Create(ctx, &model.Organization{ID: u2, DisplayName: "Org-22", ExternalID: "ext-22"})
			Expect(err).ToNot(HaveOccurred())
			_, err = storeInst.Organization().Create(ctx, &model.Organization{ID: u3, DisplayName: "Org-33", ExternalID: "ext-33"})
			Expect(err).ToNot(HaveOccurred())
			_, err = storeInst.Organization().Create(ctx, &model.Organization{ID: u4, DisplayName: "Org-44", ExternalID: "ext-44"})
			Expect(err).ToNot(HaveOccurred())

			// Filter to a subset: u2, u3, u4 (3 items)
			fs, err := selector.NewFieldSelector("metadata.name in (\"" + u2.String() + "\",\"" + u3.String() + "\",\"" + u4.String() + "\")")
			Expect(err).ToNot(HaveOccurred())

			// List should return all matching items (no pagination)
			list, err := storeInst.Organization().List(ctx, store.ListParams{FieldSelector: fs})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(list)).To(Equal(3))

			// Verify all expected orgs are present
			orgIDs := make(map[uuid.UUID]bool)
			for _, org := range list {
				orgIDs[org.ID] = true
			}
			Expect(orgIDs).To(HaveKey(u2))
			Expect(orgIDs).To(HaveKey(u3))
			Expect(orgIDs).To(HaveKey(u4))
			Expect(orgIDs).ToNot(HaveKey(u1))
		})

		It("Should upsert organizations with ON CONFLICT DO NOTHING and return existing rows", func() {
			// First call: insert new orgs via UpsertMany
			orgsToUpsert := []*model.Organization{
				{DisplayName: "Upsert Org 1", ExternalID: "upsert-ext-1"},
				{DisplayName: "Upsert Org 2", ExternalID: "upsert-ext-2"},
			}
			result, err := storeInst.Organization().UpsertMany(ctx, orgsToUpsert)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(2))
			byExternalID := make(map[string]*model.Organization)
			for _, o := range result {
				byExternalID[o.ExternalID] = o
			}
			Expect(byExternalID).To(HaveKey("upsert-ext-1"))
			Expect(byExternalID).To(HaveKey("upsert-ext-2"))
			Expect(byExternalID["upsert-ext-1"].ID).ToNot(Equal(uuid.Nil))
			Expect(byExternalID["upsert-ext-1"].DisplayName).To(Equal("Upsert Org 1"))
			Expect(byExternalID["upsert-ext-2"].DisplayName).To(Equal("Upsert Org 2"))

			// Second call: same external IDs (conflict). ON CONFLICT DO NOTHING must succeed without SQL error.
			// Returns existing rows from DB, not the new payload.
			orgsAgain := []*model.Organization{
				{DisplayName: "Different Name 1", ExternalID: "upsert-ext-1"},
				{DisplayName: "Different Name 2", ExternalID: "upsert-ext-2"},
			}
			resultAgain, err := storeInst.Organization().UpsertMany(ctx, orgsAgain)
			Expect(err).ToNot(HaveOccurred(), "UpsertMany on conflict must not error (EDM-3438)")
			Expect(resultAgain).To(HaveLen(2))
			byExternalIDAgain := make(map[string]*model.Organization)
			for _, o := range resultAgain {
				byExternalIDAgain[o.ExternalID] = o
			}
			// Still the original display names (DO NOTHING = no update)
			Expect(byExternalIDAgain["upsert-ext-1"].DisplayName).To(Equal("Upsert Org 1"))
			Expect(byExternalIDAgain["upsert-ext-1"].ExternalID).To(Equal("upsert-ext-1"))
			Expect(byExternalIDAgain["upsert-ext-2"].DisplayName).To(Equal("Upsert Org 2"))
			Expect(byExternalIDAgain["upsert-ext-2"].ExternalID).To(Equal("upsert-ext-2"))
			// Same IDs as first result
			Expect(byExternalIDAgain["upsert-ext-1"].ID).To(Equal(byExternalID["upsert-ext-1"].ID))
			Expect(byExternalIDAgain["upsert-ext-2"].ID).To(Equal(byExternalID["upsert-ext-2"].ID))
		})

		It("Should prevent race condition when creating default organization on empty table (EDM-2751)", func() {
			// Create a fresh database with organizations table but NO default organization
			freshCtx := testutil.StartSpecTracerForGinkgo(suiteCtx)
			freshLog := flightlog.InitLogs()

			// Create an empty test database (no template, no migrations)
			freshCfg, freshDbName, freshGormDb := testdb.CreateEmptyTestDB(freshCtx, freshLog, "test_org_race_", store.InitDB)
			defer testdb.DeleteTestDB(freshCtx, freshLog, freshCfg, freshGormDb, freshDbName)

			// Manually create the organizations table structure WITHOUT running InitialMigration
			db := freshGormDb.WithContext(freshCtx)
			var err error
			err = db.AutoMigrate(&model.Organization{})
			Expect(err).ToNot(HaveOccurred())

			// Create the external_id unique index
			err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS org_external_id_idx
			               ON organizations (external_id) WHERE external_id <> ''`).Error
			Expect(err).ToNot(HaveOccurred())

			// Verify table is empty
			var count int64
			err = db.Model(&model.Organization{}).Count(&count).Error
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(int64(0)), "Organizations table should start empty")

			// Now run InitialMigration concurrently from multiple goroutines
			// This is the ACTUAL race condition scenario from EDM-2751
			const numConcurrentCalls = 10
			errChan := make(chan error, numConcurrentCalls)

			orgStore := store.NewOrganization(freshGormDb)
			for i := 0; i < numConcurrentCalls; i++ {
				go func() {
					err := orgStore.InitialMigration(freshCtx)
					errChan <- err
				}()
			}

			// Collect all errors
			errors := make([]error, 0, numConcurrentCalls)
			for i := 0; i < numConcurrentCalls; i++ {
				err := <-errChan
				if err != nil {
					errors = append(errors, err)
				}
			}

			// Verify no errors occurred (WITHOUT the fix, this would fail with duplicate key errors)
			Expect(errors).To(BeEmpty(), "Race condition should be prevented by ON CONFLICT DO NOTHING")

			// Verify exactly one default organization was created
			var orgs []*model.Organization
			err = freshGormDb.Find(&orgs).Error
			Expect(err).ToNot(HaveOccurred())
			Expect(orgs).To(HaveLen(1), "Exactly one default organization should exist")
			Expect(orgs[0].ID).To(Equal(store.NullOrgId))
			Expect(orgs[0].ExternalID).To(Equal(org.DefaultExternalID))
			Expect(orgs[0].DisplayName).To(Equal("Default"))
		})

		// Table-driven test for InitialMigration idempotency on already-initialized database
		DescribeTable("InitialMigration idempotency on initialized database",
			func(parallel bool, runs int) {
				errChan := make(chan error, runs)
				run := func() {
					errChan <- storeInst.Organization().InitialMigration(ctx)
				}

				if parallel {
					// Execute concurrently
					for i := 0; i < runs; i++ {
						go run()
					}
				} else {
					// Execute sequentially
					for i := 0; i < runs; i++ {
						run()
					}
				}

				// Collect and verify no errors occurred
				for i := 0; i < runs; i++ {
					Expect(<-errChan).ToNot(HaveOccurred(), "InitialMigration should be idempotent")
				}

				// Verify still only one default organization exists
				orgs, err := storeInst.Organization().List(ctx, store.ListParams{})
				Expect(err).ToNot(HaveOccurred())
				Expect(orgs).To(HaveLen(1), "Should still have exactly one default organization")
				Expect(orgs[0].ID).To(Equal(store.NullOrgId))
				Expect(orgs[0].ExternalID).To(Equal(org.DefaultExternalID))
				Expect(orgs[0].DisplayName).To(Equal("Default"))
			},
			Entry("concurrent calls", true, 10),
			Entry("sequential calls", false, 1),
		)
	})
})
