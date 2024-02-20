package store

import (
	"context"
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func createResourceSyncs(numResourceSyncs int, ctx context.Context, store Store, orgId uuid.UUID) {
	for i := 1; i <= numResourceSyncs; i++ {
		resource := api.ResourceSync{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("myresourcesync-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.ResourceSyncSpec{
				Repository: util.StrToPtr("myrepo"),
				Path:       util.StrToPtr("my/path"),
			},
		}

		_, err := store.ResourceSync().Create(ctx, orgId, &resource)
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
		store            Store
		cfg              *config.Config
		dbName           string
		numResourceSyncs int
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numResourceSyncs = 3
		store, cfg, dbName = PrepareDBForUnitTests(log)

		createResourceSyncs(3, ctx, store, orgId)
	})

	AfterEach(func() {
		DeleteTestDB(cfg, store, dbName)
	})

	Context("ResourceSync store", func() {
		It("Get resourcesync success", func() {
			dev, err := store.ResourceSync().Get(ctx, orgId, "myresourcesync-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("myresourcesync-1"))
		})

		It("Get resourcesync - not found error", func() {
			_, err := store.ResourceSync().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Get resourcesync - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := store.ResourceSync().Get(ctx, badOrgId, "myresourcesync-1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Delete resourcesync success", func() {
			rsName := "myresourcesync-1"
			fleetowner := util.SetResourceOwner(model.ResourceSyncKind, rsName)
			listParams := ListParams{
				Limit: 100,
				Owner: fleetowner,
			}
			createFleets(1, ctx, store, orgId, fleetowner)
			callbackCalled := false
			err := store.ResourceSync().Delete(ctx, orgId, rsName, func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
				Expect(owner).To(Equal(*fleetowner))
				f, err := store.Fleet().List(ctx, orgId, listParams)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(f.Items)).To(Equal(1))
				err = store.Fleet().UnsetOwner(ctx, tx, orgId, owner)
				callbackCalled = true
				return err
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())
			f, err := store.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(f.Items)).To(Equal(0))
		})

		It("Delete resourcesync fail when not found", func() {
			callbackCalled := false
			err := store.ResourceSync().Delete(ctx, orgId, "nonexistent", func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
				callbackCalled = true
				return nil
			})
			Expect(err).To(HaveOccurred())
			Expect(callbackCalled).To(BeFalse())
		})

		It("Delete all resourcesyncs in org", func() {
			owner := util.SetResourceOwner(model.ResourceSyncKind, "myresourcesync-1")
			otherOrgId, _ := uuid.NewUUID()
			createFleets(2, ctx, store, orgId, owner)
			createFleets(2, ctx, store, otherOrgId, owner)
			callbackCalled := false
			err := store.ResourceSync().DeleteAll(ctx, otherOrgId, func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, kind string) error {
				callbackCalled = true
				Expect(kind).To(Equal(model.ResourceSyncKind))
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())

			listParams := ListParams{Limit: 1000}
			resourcesyncs, err := store.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(numResourceSyncs))

			callbackCalled = false
			fleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).ToNot(BeNil())
			Expect(*fleet.Metadata.Owner).To(Equal(*owner))

			err = store.ResourceSync().DeleteAll(ctx, orgId, func(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, kind string) error {
				callbackCalled = true
				Expect(kind).To(Equal(model.ResourceSyncKind))
				return store.Fleet().UnsetOwnerByKind(ctx, tx, orgId, kind)
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(callbackCalled).To(BeTrue())

			fleet, err = store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).To(BeNil())

			resourcesyncs, err = store.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(0))

			fleet, err = store.Fleet().Get(ctx, otherOrgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).ToNot(BeNil())
			Expect(*fleet.Metadata.Owner).To(Equal(*owner))
		})

		It("List with paging", func() {
			listParams := ListParams{Limit: 1000}
			allResourceSyncs, err := store.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allResourceSyncs.Items)).To(Equal(numResourceSyncs))
			allNames := make([]string, len(allResourceSyncs.Items))
			for i, dev := range allResourceSyncs.Items {
				allNames[i] = *dev.Metadata.Name
			}

			foundNames := make([]string, len(allResourceSyncs.Items))
			listParams.Limit = 1
			resourcesyncs, err := store.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(1))
			Expect(*resourcesyncs.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundNames[0] = *resourcesyncs.Items[0].Metadata.Name

			cont, err := ParseContinueString(resourcesyncs.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			resourcesyncs, err = store.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(1))
			Expect(*resourcesyncs.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundNames[1] = *resourcesyncs.Items[0].Metadata.Name

			cont, err = ParseContinueString(resourcesyncs.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			resourcesyncs, err = store.ResourceSync().List(ctx, orgId, listParams)
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
			listParams := ListParams{
				Limit:  1000,
				Labels: map[string]string{"key": "value-1"}}
			resourcesyncs, err := store.ResourceSync().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resourcesyncs.Items)).To(Equal(1))
			Expect(*resourcesyncs.Items[0].Metadata.Name).To(Equal("myresourcesync-1"))
		})

		It("CreateOrUpdateResourceSync create mode", func() {
			condition := api.Condition{
				Type:               api.ResourceSyncAccessible,
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			resourcesync := api.ResourceSync{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename"),
				},
				Spec: api.ResourceSyncSpec{
					Repository: util.StrToPtr("myrepo"),
					Path:       util.StrToPtr("my/path"),
				},
				Status: &api.ResourceSyncStatus{
					Conditions: &[]api.Condition{condition},
				},
			}
			rs, created, err := store.ResourceSync().CreateOrUpdate(ctx, orgId, &resourcesync)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))
			Expect(rs.ApiVersion).To(Equal(model.ResourceSyncAPI))
			Expect(rs.Kind).To(Equal(model.ResourceSyncKind))
			Expect(*rs.Spec.Repository).To(Equal("myrepo"))
			Expect(*rs.Spec.Path).To(Equal("my/path"))
			Expect(rs.Status.Conditions).To(BeNil())
		})

		It("CreateOrUpdateResourceSync update mode", func() {
			condition := api.Condition{
				Type:               api.ResourceSyncAccessible,
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			resourcesync := api.ResourceSync{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("myresourcesync-1"),
				},
				Spec: api.ResourceSyncSpec{
					Repository: util.StrToPtr("myotherrepo"),
					Path:       util.StrToPtr("my/other/path"),
				},
				Status: &api.ResourceSyncStatus{
					Conditions: &[]api.Condition{condition},
				},
			}
			rs, created, err := store.ResourceSync().CreateOrUpdate(ctx, orgId, &resourcesync)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(rs.ApiVersion).To(Equal(model.ResourceSyncAPI))
			Expect(rs.Kind).To(Equal(model.ResourceSyncKind))
			Expect(*rs.Spec.Repository).To(Equal("myotherrepo"))
			Expect(*rs.Spec.Path).To(Equal("my/other/path"))
			Expect(rs.Status.Conditions).To(BeNil())
		})
	})
})
