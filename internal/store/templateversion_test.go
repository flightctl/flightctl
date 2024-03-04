package store_test

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func createTemplateVersions(numTemplateVersions int, ctx context.Context, storeInst store.Store, orgId uuid.UUID) error {
	for i := 1; i <= numTemplateVersions; i++ {
		resource := api.TemplateVersion{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("1.0.%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.TemplateVersionSpec{
				Fleet: "myfleet",
			},
		}

		called := false
		callback := store.TemplateVersionStoreCallback(func(tv *model.TemplateVersion) { called = true })
		_, err := storeInst.TemplateVersion().Create(ctx, orgId, &resource, callback)
		if err != nil {
			Expect(called).To(BeFalse())
			return err
		}
		Expect(called).To(BeTrue())
	}
	return nil
}

var _ = Describe("TemplateVersion", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		cfg       *config.Config
		dbName    string
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	Context("TemplateVersion store", func() {
		It("Create no fleet error", func() {
			err := createTemplateVersions(1, ctx, storeInst, orgId)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(gorm.ErrRecordNotFound))
		})

		It("Create duplicate error", func() {
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := createTemplateVersions(1, ctx, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())
			err = createTemplateVersions(1, ctx, storeInst, orgId)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(gorm.ErrInvalidData))
		})

		It("List with paging", func() {
			numResources := 5
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := createTemplateVersions(numResources, ctx, storeInst, orgId)
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

		It("Delete all templateVersions in org", func() {
			numResources := 5
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := createTemplateVersions(numResources, ctx, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())

			otherOrgId, _ := uuid.NewUUID()
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), otherOrgId, "myfleet", nil, nil)
			err = createTemplateVersions(numResources, ctx, storeInst, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.TemplateVersion().DeleteAll(ctx, otherOrgId, util.StrToPtr("Fleet/myfleet"))
			Expect(err).ToNot(HaveOccurred())

			templateVersions, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(templateVersions.Items)).To(Equal(numResources))

			templateVersions, err = storeInst.TemplateVersion().List(ctx, otherOrgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(templateVersions.Items)).To(Equal(0))

			err = storeInst.TemplateVersion().DeleteAll(ctx, orgId, nil)
			Expect(err).ToNot(HaveOccurred())

			templateVersions, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(templateVersions.Items)).To(Equal(0))
		})

		It("Get templateVersion success", func() {
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := createTemplateVersions(1, ctx, storeInst, orgId)
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
			Expect(err).To(Equal(gorm.ErrRecordNotFound))

			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			_, err = storeInst.TemplateVersion().Get(ctx, orgId, "myfleet", "1.0.1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))

			err = createTemplateVersions(1, ctx, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())
			badOrgId, _ := uuid.NewUUID()
			_, err = storeInst.TemplateVersion().Get(ctx, badOrgId, "myfleet", "1.0.1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Delete templateVersion success", func() {
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, nil)
			err := createTemplateVersions(1, ctx, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())
			err = storeInst.TemplateVersion().Delete(ctx, orgId, "myfleet", "1.0.1")
			Expect(err).ToNot(HaveOccurred())
			_, err = storeInst.TemplateVersion().Get(ctx, orgId, "myfleet", "1.0.1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Delete templateVersion success when not found", func() {
			err := storeInst.TemplateVersion().Delete(ctx, orgId, "myfleet", "1.0.1")
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
