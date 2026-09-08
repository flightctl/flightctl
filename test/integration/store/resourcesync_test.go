package store_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	resourcesyncstore "github.com/flightctl/flightctl/internal/store/resourcesync"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("ResourceSyncStore create", func() {
	var (
		log               *logrus.Logger
		ctx               context.Context
		orgId             uuid.UUID
		resourceSyncStore resourcesyncstore.Store
		fleetStore        fleetstore.Store
		organizationStore organizationstore.Store
		cfg               *config.Config
		dbName            string
		db                *gorm.DB
		numResourceSyncs  int
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		numResourceSyncs = 3
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		resourceSyncStore = resourcesyncstore.NewResourceSyncStore(db, log.WithField("pkg", "resourcesync-store"))
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		organizationStore = organizationstore.NewOrganizationStore(db)

		orgId = uuid.New()
		err = testutil.CreateTestOrganization(ctx, organizationStore, orgId)
		Expect(err).ToNot(HaveOccurred())

		testutil.CreateTestResourceSyncs(ctx, 3, resourceSyncStore, orgId)
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	Context("ResourceSync store", func() {
		It("Create resourcesync", func() {
			var gen int64 = 1
			rs := api.ResourceSync{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("rs1"),
					Labels: &map[string]string{"key": "rs1"},
				},
				Spec: api.ResourceSyncSpec{
					Repository: "myrepo",
					Path:       "my/path",
				},
			}
			resp, err := resourceSyncStore.Create(ctx, orgId, &rs)
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Metadata.Generation).ToNot(BeNil())
			Expect(*resp.Metadata.Generation).To(Equal(gen))

			// name already exisis
			_, err = resourceSyncStore.Create(ctx, orgId, &rs)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrDuplicateName))
		})

		It("Get resourcesync success", func() {
			dev, err := resourceSyncStore.Get(ctx, orgId, "myresourcesync-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("myresourcesync-1"))
		})

		It("Get resourcesync - not found error", func() {
			_, err := resourceSyncStore.Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get resourcesync - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := resourceSyncStore.Get(ctx, badOrgId, "myresourcesync-1")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Delete resourcesync success", func() {
			rsName := "myresourcesync-1"
			fleetowner := util.SetResourceOwner(api.ResourceSyncKind, rsName)
			listParams := store.ListParams{
				Limit: 100,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(
					map[string]string{"metadata.owner": *fleetowner}, selector.WithPrivateSelectors()),
			}
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "myfleet", nil, fleetowner)
			err := resourceSyncStore.WithTransaction(ctx, func(ctx context.Context) error {
				if _, err := resourceSyncStore.Delete(ctx, orgId, rsName); err != nil {
					return err
				}
				f, err := fleetStore.List(ctx, orgId, listParams)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(f.Items)).To(Equal(1))
				return fleetStore.UnsetOwner(ctx, store.DB(ctx, nil), orgId, *fleetowner)
			})
			Expect(err).ToNot(HaveOccurred())
			f, err := fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(f.Items)).To(Equal(0))
		})

		It("Delete resourcesync fail when not found", func() {
			_, err := resourceSyncStore.Delete(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
		})

		It("List with paging", func() {
			listParams := store.ListParams{Limit: 1000}
			allResourceSyncs, err := resourceSyncStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allResourceSyncs.Items)).To(Equal(numResourceSyncs))
			allNames := make([]string, len(allResourceSyncs.Items))
			for i, dev := range allResourceSyncs.Items {
				allNames[i] = *dev.Metadata.Name
			}

			foundNames := make([]string, len(allResourceSyncs.Items))
			listParams.Limit = 1
			resourcesyncs, err := resourceSyncStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(1))
			Expect(*resourcesyncs.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundNames[0] = *resourcesyncs.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(resourcesyncs.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			resourcesyncs, err = resourceSyncStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(1))
			Expect(*resourcesyncs.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundNames[1] = *resourcesyncs.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(resourcesyncs.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			resourcesyncs, err = resourceSyncStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(1))
			Expect(resourcesyncs.Metadata.RemainingItemCount).To(BeNil())
			Expect(resourcesyncs.Metadata.Continue).To(BeNil())
			foundNames[2] = *resourcesyncs.Items[0].Metadata.Name

			for i := range allNames {
				Expect(allNames[i]).To(Equal(foundNames[i]))
			}
		})

		It("List with paging", func() {
			listParams := store.ListParams{
				Limit:         1000,
				LabelSelector: selector.NewLabelSelectorFromMapOrDie(map[string]string{"key": "value-1"})}
			resourcesyncs, err := resourceSyncStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(1))
			Expect(*resourcesyncs.Items[0].Metadata.Name).To(Equal("myresourcesync-1"))
		})

		It("CreateOrUpdateResourceSync create mode", func() {
			resourcesync := api.ResourceSync{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("newresourcename"),
				},
				Spec: api.ResourceSyncSpec{
					Repository: "myrepo",
					Path:       "my/path",
				},
				Status: nil,
			}
			rs, _, created, err := resourceSyncStore.CreateOrUpdate(ctx, orgId, &resourcesync)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))
			Expect(rs.ApiVersion).To(Equal(model.ResourceSyncAPIVersion()))
			Expect(rs.Kind).To(Equal(api.ResourceSyncKind))
			Expect(rs.Spec.Repository).To(Equal("myrepo"))
			Expect(rs.Spec.Path).To(Equal("my/path"))
			Expect(rs.Status.Conditions).ToNot(BeNil())
			Expect(rs.Status.Conditions).To(BeEmpty())
		})

		It("CreateOrUpdateResourceSync update mode", func() {
			resourcesync := api.ResourceSync{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("myresourcesync-1"),
				},
				Spec: api.ResourceSyncSpec{
					Repository: "myotherrepo",
					Path:       "my/other/path",
				},
				Status: nil,
			}
			rs, _, created, err := resourceSyncStore.CreateOrUpdate(ctx, orgId, &resourcesync)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(rs.ApiVersion).To(Equal(model.ResourceSyncAPIVersion()))
			Expect(rs.Kind).To(Equal(api.ResourceSyncKind))
			Expect(rs.Spec.Repository).To(Equal("myotherrepo"))
			Expect(rs.Spec.Path).To(Equal("my/other/path"))
			Expect(rs.Status.Conditions).ToNot(BeNil())
			Expect(rs.Status.Conditions).To(BeEmpty())
		})
	})
})
