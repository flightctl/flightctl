package store_test

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("OrganizationStore Integration Tests", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		storeInst store.Store
		cfg       *config.Config
		dbName    string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
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

		// Regression test for EDM-2751: Race condition during first-time initialization
		It("Should prevent race condition when creating default organization on empty table (EDM-2751)", func() {
			// This test verifies the fix for EDM-2751 where multiple concurrent processes
			// attempting to create the FIRST default organization would cause duplicate key errors.
			// This simulates a clean Helm deployment where multiple pods start simultaneously.

			// Create a fresh database with organizations table but NO default organization
			freshCtx := testutil.StartSpecTracerForGinkgo(suiteCtx)
			freshLog := flightlog.InitLogs()

			// Use testutil directly to create a fresh database without migrations
			freshCfg := config.NewDefault()
			freshDbName := "test_org_race_" + uuid.New().String()[:8]
			freshCfg.Database.Name = "flightctl"
			adminDB, err := store.InitDB(freshCfg, freshLog)
			Expect(err).ToNot(HaveOccurred())

			// Create the test database
			err = adminDB.Exec("CREATE DATABASE " + freshDbName).Error
			Expect(err).ToNot(HaveOccurred())
			store.CloseDB(adminDB)

			// Connect to the fresh database
			freshCfg.Database.Name = freshDbName
			freshGormDb, err := store.InitDB(freshCfg, freshLog)
			Expect(err).ToNot(HaveOccurred())

			defer func() {
				store.CloseDB(freshGormDb)
				freshCfg.Database.Name = "flightctl"
				adminDB, _ := store.InitDB(freshCfg, freshLog)
				if adminDB != nil {
					adminDB.Exec("DROP DATABASE IF EXISTS " + freshDbName)
					store.CloseDB(adminDB)
				}
			}()

			// Manually create the organizations table structure WITHOUT running InitialMigration
			db := freshGormDb.WithContext(freshCtx)
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

		// Additional test: Verify InitialMigration is idempotent on already-initialized database
		It("Should handle concurrent InitialMigration calls on initialized database", func() {
			// This test verifies idempotency - calling InitialMigration multiple times
			// on an already-initialized database should not cause errors.

			// Run InitialMigration concurrently on the existing database (already has default org)
			const numConcurrentCalls = 10
			errChan := make(chan error, numConcurrentCalls)

			for i := 0; i < numConcurrentCalls; i++ {
				go func() {
					err := storeInst.Organization().InitialMigration(ctx)
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

			// Verify no errors occurred
			Expect(errors).To(BeEmpty(), "InitialMigration should be idempotent")

			// Verify still only one default organization exists
			orgs, err := storeInst.Organization().List(ctx, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(orgs).To(HaveLen(1), "Should still have exactly one default organization")
			Expect(orgs[0].ID).To(Equal(store.NullOrgId))
			Expect(orgs[0].ExternalID).To(Equal(org.DefaultExternalID))
			Expect(orgs[0].DisplayName).To(Equal("Default"))
		})

		// Additional test: Verify InitialMigration is idempotent
		It("Should be idempotent when called multiple times sequentially", func() {
			// Call InitialMigration again on the existing database
			err := storeInst.Organization().InitialMigration(ctx)
			Expect(err).ToNot(HaveOccurred(), "InitialMigration should be idempotent")

			// Verify still only one default organization
			orgs, err := storeInst.Organization().List(ctx, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(orgs).To(HaveLen(1), "Should still have exactly one default organization")
			Expect(orgs[0].ID).To(Equal(store.NullOrgId))
		})
	})
})
