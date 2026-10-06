package store_test

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
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

var _ = Describe("Catalog item fleet field selector", func() {
	var (
		ctx          context.Context
		log          *logrus.Logger
		cfg          *config.Config
		dbName       string
		db           *gorm.DB
		catalogStore catalogstore.Store
		orgID        uuid.UUID
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		catalogStore = catalogstore.NewCatalogStore(db, log.WithField("pkg", "catalog-store"))
		orgID = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), orgID)).To(Succeed())
		for _, itemName := range []string{"os", "app", "volume", "unused"} {
			Expect(db.Create(&model.CatalogItem{OrgID: orgID, CatalogName: domain.DefaultCatalogName, AppName: itemName}).Error).To(Succeed())
		}
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	It("When fleets reference catalog items it should filter across all reference types and organizations", func() {
		otherOrgID := uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), otherOrgID)).To(Succeed())
		for _, fleet := range []struct {
			orgID uuid.UUID
			name  string
			spec  string
		}{
			{orgID, "os-fleet", `{"template":{"spec":{"os":{"catalogItemRef":{"catalog":"default","item":"os"}}}}}`},
			{orgID, "app-fleet", `{"template":{"spec":{"applications":[{"catalogItemRef":{"catalog":"default","item":"app"}}]}}}`},
			{orgID, "volume-fleet", `{"template":{"spec":{"applications":[{"volumes":[{"image":{"catalogItemRef":{"catalog":"default","item":"volume"}}}]}]}}}`},
			{orgID, "deleted-fleet", `{"template":{"spec":{"os":{"catalogItemRef":{"catalog":"default","item":"unused"}}}}}`},
			{otherOrgID, "os-fleet", `{"template":{"spec":{"os":{"catalogItemRef":{"catalog":"default","item":"unused"}}}}}`},
		} {
			Expect(db.Exec("INSERT INTO fleets (org_id, name, spec) VALUES (?, ?, ?::jsonb)", fleet.orgID, fleet.name, fleet.spec).Error).To(Succeed())
		}
		Expect(db.Exec("UPDATE fleets SET deleted_at = NOW() WHERE org_id = ? AND name = ?", orgID, "deleted-fleet").Error).To(Succeed())
		fieldSelector, err := selector.NewFieldSelector("fleet in (os-fleet,app-fleet,volume-fleet,deleted-fleet)")
		Expect(err).NotTo(HaveOccurred())
		items, err := catalogStore.ListAllItems(ctx, orgID, store.ListParams{FieldSelector: fieldSelector, Limit: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(items.Items).To(HaveLen(2))
		Expect(items.Metadata.Continue).NotTo(BeNil())
		fieldSelector, err = selector.NewFieldSelector("fleet in (os-fleet,app-fleet,volume-fleet,deleted-fleet)")
		Expect(err).NotTo(HaveOccurred())
		allItems, err := catalogStore.ListAllItems(ctx, orgID, store.ListParams{FieldSelector: fieldSelector})
		Expect(err).NotTo(HaveOccurred())
		Expect(allItems.Items).To(HaveLen(3))
		Expect([]string{*allItems.Items[0].Metadata.Name, *allItems.Items[1].Metadata.Name, *allItems.Items[2].Metadata.Name}).To(Equal([]string{"app", "os", "volume"}))
	})
})
