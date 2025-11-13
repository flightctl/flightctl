package store_test

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
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
			orgs, err := storeInst.Organization().List(ctx)
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

			orgs, err := storeInst.Organization().List(ctx)
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
	})
})
