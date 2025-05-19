package store_test

import (
	"context"
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func createResourceSyncs(ctx context.Context, numResourceSyncs int, storeInst store.Store, orgId uuid.UUID) {
	for i := 1; i <= numResourceSyncs; i++ {
		resource := api.ResourceSync{
			Metadata: api.ObjectMeta{
				Name:   lo.ToPtr(fmt.Sprintf("myresourcesync-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.ResourceSyncSpec{
				Repository: "myrepo",
				Path:       "my/path",
			},
		}

		_, err := storeInst.ResourceSync().Create(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("creating resourcesync: %v", err)
		}
	}
}

var _ = Describe("ResourceSyncStore create", func() {
	var (
		log              *logrus.Logger
		ctx              context.Context
		orgId            uuid.UUID
		storeInst        store.Store
		cfg              *config.Config
		dbName           string
		numResourceSyncs int
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numResourceSyncs = 3
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(log)

		createResourceSyncs(ctx, 3, storeInst, orgId)
	})

	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
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
			resp, err := storeInst.ResourceSync().Create(context.Background(), orgId, &rs)
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Metadata.Generation).ToNot(BeNil())
			Expect(*resp.Metadata.Generation).To(Equal(gen))

			// name already exisis
			_, err = storeInst.ResourceSync().Create(context.Background(), orgId, &rs)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(flterrors.ErrDuplicateName))
		})

		It("Get resourcesync success", func() {
			dev, err := storeInst.ResourceSync().Get(ctx, orgId, "myresourcesync-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("myresourcesync-1"))
		})

		It("Get resourcesync - not found error", func() {
			_, err := storeInst.ResourceSync().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get resourcesync - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := storeInst.ResourceSync().Get(ctx, badOrgId, "myresourcesync-1")
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
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "myfleet", nil, fleetowner)
			callbackCalled := false
			err := storeInst.ResourceSync().Delete(ctx, orgId, rsName, func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
				Expect(owner).To(Equal(*fleetowner))
				f, err := storeInst.Fleet().List(ctx, orgId, listParams)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(f.Items)).To(Equal(1))
				err = storeInst.Fleet().UnsetOwner(ctx, tx, orgId, owner)
				callbackCalled = true
				return err
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
			f, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(f.Items)).To(Equal(0))
		})

		It("Delete resourcesync fail when not found", func() {
			callbackCalled := false
			err := storeInst.ResourceSync().Delete(ctx, orgId, "nonexistent", func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
				callbackCalled = true
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeFalse())
		})

		It("Delete all resourcesyncs in org", func() {
			owner := util.SetResourceOwner(api.ResourceSyncKind, "myresourcesync-1")
			otherOrgId, _ := uuid.NewUUID()
			testutil.CreateTestFleets(ctx, 2, storeInst.Fleet(), orgId, "myfleet", true, owner)
			testutil.CreateTestFleets(ctx, 2, storeInst.Fleet(), otherOrgId, "myfleet", true, owner)
			callbackCalled := false
			err := storeInst.ResourceSync().DeleteAll(ctx, otherOrgId, func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, kind string) error {
				callbackCalled = true
				Expect(kind).To(Equal(api.ResourceSyncKind))
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())

			listParams := store.ListParams{Limit: 1000}
			resourcesyncs, err := storeInst.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(numResourceSyncs))

			callbackCalled = false
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).ToNot(BeNil())
			Expect(*fleet.Metadata.Owner).To(Equal(*owner))

			err = storeInst.ResourceSync().DeleteAll(ctx, orgId, func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, kind string) error {
				callbackCalled = true
				Expect(kind).To(Equal(api.ResourceSyncKind))
				return storeInst.Fleet().UnsetOwnerByKind(ctx, tx, orgId, kind)
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).To(BeNil())

			resourcesyncs, err = storeInst.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(0))

			fleet, err = storeInst.Fleet().Get(ctx, otherOrgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).ToNot(BeNil())
			Expect(*fleet.Metadata.Owner).To(Equal(*owner))
		})

		It("List with paging", func() {
			listParams := store.ListParams{Limit: 1000}
			allResourceSyncs, err := storeInst.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allResourceSyncs.Items)).To(Equal(numResourceSyncs))
			allNames := make([]string, len(allResourceSyncs.Items))
			for i, dev := range allResourceSyncs.Items {
				allNames[i] = *dev.Metadata.Name
			}

			foundNames := make([]string, len(allResourceSyncs.Items))
			listParams.Limit = 1
			resourcesyncs, err := storeInst.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(1))
			Expect(*resourcesyncs.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundNames[0] = *resourcesyncs.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(resourcesyncs.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			resourcesyncs, err = storeInst.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(1))
			Expect(*resourcesyncs.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundNames[1] = *resourcesyncs.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(resourcesyncs.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			resourcesyncs, err = storeInst.ResourceSync().List(ctx, orgId, listParams)
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
			resourcesyncs, err := storeInst.ResourceSync().List(ctx, orgId, listParams)
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
			rs, created, _, err := storeInst.ResourceSync().CreateOrUpdate(ctx, orgId, &resourcesync)
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
			rs, created, _, err := storeInst.ResourceSync().CreateOrUpdate(ctx, orgId, &resourcesync)
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
