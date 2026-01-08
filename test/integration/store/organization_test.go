package store_test

import (
	"context"

	"github.com/flightctl/flightctl/internal/config/common"
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
		cfg       *common.DatabaseConfig
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
	})
})
