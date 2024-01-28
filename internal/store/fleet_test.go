package store

import (
	"context"
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func createFleets(numFleets int, ctx context.Context, store *Store, orgId uuid.UUID) {
	for i := 1; i <= numFleets; i++ {
		resource := api.Fleet{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("myfleet-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.FleetSpec{
				Selector: &api.LabelSelector{
					MatchLabels: map[string]string{"key": fmt.Sprintf("value-%d", i)},
				},
			},
		}

		_, err := store.fleetStore.CreateFleet(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("creating fleet: %v", err)
		}
	}
}

var _ = Describe("FleetStore create", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		db        *gorm.DB
		store     *Store
		cfg       *config.Config
		dbName    string
		numFleets int
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numFleets = 3
		db, store, cfg, dbName = PrepareDBForUnitTests(log)

		createFleets(3, ctx, store, orgId)
	})

	AfterEach(func() {
		DeleteTestDB(cfg, db, dbName)
	})

	Context("Fleet store", func() {
		It("Get fleet success", func() {
			fleet, err := store.fleetStore.GetFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Name).To(Equal("myfleet-1"))
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})

		It("Get fleet - not found error", func() {
			_, err := store.fleetStore.GetFleet(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Get fleet - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := store.fleetStore.GetFleet(ctx, badOrgId, "myfleet-1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Delete fleet success", func() {
			err := store.fleetStore.DeleteFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete fleet success when not found", func() {
			err := store.fleetStore.DeleteFleet(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete all fleets in org", func() {
			otherOrgId, _ := uuid.NewUUID()
			err := store.fleetStore.DeleteFleets(ctx, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			listParams := service.ListParams{Limit: 1000}
			fleets, err := store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(numFleets))

			err = store.fleetStore.DeleteFleets(ctx, orgId)
			Expect(err).ToNot(HaveOccurred())

			fleets, err = store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(0))
		})

		It("List with paging", func() {
			listParams := service.ListParams{Limit: 1000}
			allFleets, err := store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allFleets.Items)).To(Equal(numFleets))
			allFleetNames := make([]string, len(allFleets.Items))
			for i, fleet := range allFleets.Items {
				allFleetNames[i] = *fleet.Metadata.Name
			}

			foundFleetNames := make([]string, len(allFleets.Items))
			listParams.Limit = 1
			fleets, err := store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundFleetNames[0] = *fleets.Items[0].Metadata.Name

			cont, err := service.ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundFleetNames[1] = *fleets.Items[0].Metadata.Name

			cont, err = service.ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(fleets.Metadata.RemainingItemCount).To(BeNil())
			Expect(fleets.Metadata.Continue).To(BeNil())
			foundFleetNames[2] = *fleets.Items[0].Metadata.Name

			for i := range allFleetNames {
				Expect(allFleetNames[i]).To(Equal(foundFleetNames[i]))
			}
		})

		It("List with paging", func() {
			listParams := service.ListParams{
				Limit:  1000,
				Labels: map[string]string{"key": "value-1"}}
			fleets, err := store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Items[0].Metadata.Name).To(Equal("myfleet-1"))
		})

		It("CreateOrUpdateFleet create mode", func() {
			condition := api.FleetCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename"),
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: map[string]string{"key": "value"},
					},
				},
				Status: &api.FleetStatus{
					Conditions: &[]api.FleetCondition{condition},
				},
			}
			_, created, err := store.fleetStore.CreateOrUpdateFleet(ctx, orgId, &fleet)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))

			createdFleet, err := store.fleetStore.GetFleet(ctx, orgId, "newresourcename")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(createdFleet.Kind).To(Equal(model.FleetKind))
			Expect(createdFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(createdFleet.Status.Conditions).To(BeNil())
			Expect(*createdFleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*createdFleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})

		It("CreateOrUpdateFleet update mode same template", func() {
			condition := api.FleetCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}

			fleet, err := store.fleetStore.GetFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
			fleet.Spec.Selector = &api.LabelSelector{MatchLabels: map[string]string{"key": "value"}}
			fleet.Status = &api.FleetStatus{Conditions: &[]api.FleetCondition{condition}}

			_, created, err := store.fleetStore.CreateOrUpdateFleet(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))

			updatedFleet, err := store.fleetStore.GetFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(updatedFleet.Status.Conditions).To(BeNil())
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*updatedFleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})

		It("CreateOrUpdateFleet update mode updated spec", func() {
			condition := api.FleetCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}

			fleet, err := store.fleetStore.GetFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS"}
			fleet.Status = &api.FleetStatus{Conditions: &[]api.FleetCondition{condition}}

			_, created, err := store.fleetStore.CreateOrUpdateFleet(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))

			updatedFleet, err := store.fleetStore.GetFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).To(BeNil())
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*updatedFleet.Spec.Template.Metadata.Generation).To(Equal(int64(2)))
		})

		It("UpdateFleetStatus", func() {
			condition := api.FleetCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}

			fleet, err := store.fleetStore.GetFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Selector = &api.LabelSelector{MatchLabels: map[string]string{"key": "value"}}
			fleet.Status = &api.FleetStatus{Conditions: &[]api.FleetCondition{condition}}

			_, err = store.fleetStore.UpdateFleetStatus(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			updatedFleet, err := store.fleetStore.GetFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeNil())
			Expect((*updatedFleet.Status.Conditions)[0].Type).To(Equal("type"))
		})
	})
})
