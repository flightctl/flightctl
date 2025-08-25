package store_test

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("TemplateVersion", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		tvStore   store.TemplateVersion
		cfg       *config.Config
		dbName    string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		tvStore = storeInst.TemplateVersion()

		orgId = uuid.New()
		err := testutil.CreateTestOrganization(ctx, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
	})

	Context("TemplateVersion store", func() {
		It("Create no fleet error", func() {
			err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet", "1.0.0", nil)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Create duplicate error", func() {
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet", "1.0.0", nil)
			Expect(err).ToNot(HaveOccurred())
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet", "1.0.0", nil)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrDuplicateName))
		})

		It("List with paging", func() {
			numResources := 5
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := testutil.CreateTestTemplateVersions(ctx, numResources, tvStore, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{}
			allTemplateVersions, err := storeInst.TemplateVersion().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allTemplateVersions.Items)).To(Equal(numResources))
			allNames := make([]string, numResources)
			for i, templateVersion := range allTemplateVersions.Items {
				allNames[i] = *templateVersion.Metadata.Name
			}

			foundNames := make([]string, numResources)
			listParams.Limit = 2
			templateVersions, err := storeInst.TemplateVersion().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(templateVersions.Items)).To(Equal(2))
			Expect(*templateVersions.Metadata.RemainingItemCount).To(Equal(int64(3)))
			foundNames[0] = *templateVersions.Items[0].Metadata.Name
			foundNames[1] = *templateVersions.Items[1].Metadata.Name

			cont, err := store.ParseContinueString(templateVersions.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			templateVersions, err = storeInst.TemplateVersion().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(templateVersions.Items)).To(Equal(2))
			Expect(*templateVersions.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundNames[2] = *templateVersions.Items[0].Metadata.Name
			foundNames[3] = *templateVersions.Items[1].Metadata.Name

			cont, err = store.ParseContinueString(templateVersions.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			templateVersions, err = storeInst.TemplateVersion().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(templateVersions.Items)).To(Equal(1))
			Expect(templateVersions.Metadata.RemainingItemCount).To(BeNil())
			Expect(templateVersions.Metadata.Continue).To(BeNil())
			foundNames[4] = *templateVersions.Items[0].Metadata.Name

			for i := range allNames {
				Expect(allNames[i]).To(Equal(foundNames[i]))
			}
		})

		It("Delete fleet deletes its templateVersions", func() {
			numResources := 5
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := testutil.CreateTestTemplateVersions(ctx, numResources, tvStore, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			otherOrgId := uuid.New()
			err = testutil.CreateTestOrganization(ctx, storeInst, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			testutil.CreateTestFleet(ctx, storeInst.Fleet(), otherOrgId, "myfleet", nil, nil)
			err = testutil.CreateTestTemplateVersions(ctx, numResources, tvStore, otherOrgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.Fleet().Delete(ctx, otherOrgId, "myfleet", nil)
			Expect(err).ToNot(HaveOccurred())

			templateVersions, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(templateVersions.Items)).To(Equal(numResources))

			templateVersions, err = storeInst.TemplateVersion().List(ctx, otherOrgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(templateVersions.Items)).To(Equal(0))
		})

		It("Get templateVersion success", func() {
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet", "1.0.1", nil)
			Expect(err).ToNot(HaveOccurred())
			templateVersion, err := storeInst.TemplateVersion().Get(ctx, orgId, "myfleet", "1.0.1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*templateVersion.Metadata.Name).To(Equal("1.0.1"))
			Expect(*templateVersion.Metadata.Owner).To(Equal("Fleet/myfleet"))
			Expect(*templateVersion.Metadata.Generation).To(Equal(int64(1)))
		})

		It("Get templateVersion - not found errors", func() {
			_, err := storeInst.TemplateVersion().Get(ctx, orgId, "myfleet", "1.0.1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(flterrors.ErrResourceNotFound))

			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			_, err = storeInst.TemplateVersion().Get(ctx, orgId, "myfleet", "1.0.1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(flterrors.ErrResourceNotFound))

			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet", "1.0.1", nil)
			Expect(err).ToNot(HaveOccurred())
			badOrgId, _ := uuid.NewUUID()
			_, err = storeInst.TemplateVersion().Get(ctx, badOrgId, "myfleet", "1.0.1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(flterrors.ErrResourceNotFound))
		})

		It("Delete templateVersion success", func() {
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet", "1.0.1", nil)
			Expect(err).ToNot(HaveOccurred())
			deleted, err := storeInst.TemplateVersion().Delete(ctx, orgId, "myfleet", "1.0.1", nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeTrue())
			_, err = storeInst.TemplateVersion().Get(ctx, orgId, "myfleet", "1.0.1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(flterrors.ErrResourceNotFound))
		})

		It("Delete templateVersion success when not found", func() {
			deleted, err := storeInst.TemplateVersion().Delete(ctx, orgId, "myfleet", "1.0.1", nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeFalse())
		})

		It("Get latest", func() {
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet", "1.0.1", nil)
			Expect(err).ToNot(HaveOccurred())
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet", "1.0.2", nil)
			Expect(err).ToNot(HaveOccurred())
			tv, err := storeInst.TemplateVersion().GetLatest(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(*tv.Metadata.Name).To(Equal("1.0.2"))
		})
	})
})
