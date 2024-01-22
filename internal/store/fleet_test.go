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
			dev, err := store.fleetStore.GetFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("myfleet-1"))
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
			log.Infof("DELETING DEVICES WITH ORG ID %s", otherOrgId)
			err := store.fleetStore.DeleteFleets(ctx, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			listParams := service.ListParams{Limit: 1000}
			fleets, err := store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(numFleets))

			log.Infof("DELETING DEVICES WITH ORG ID %s", orgId)
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
			allDevNames := make([]string, len(allFleets.Items))
			for i, dev := range allFleets.Items {
				allDevNames[i] = *dev.Metadata.Name
			}

			foundDevNames := make([]string, len(allFleets.Items))
			listParams.Limit = 1
			fleets, err := store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundDevNames[0] = *fleets.Items[0].Metadata.Name

			cont, err := service.ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundDevNames[1] = *fleets.Items[0].Metadata.Name

			cont, err = service.ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = store.fleetStore.ListFleets(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(fleets.Metadata.RemainingItemCount).To(BeNil())
			Expect(fleets.Metadata.Continue).To(BeNil())
			foundDevNames[2] = *fleets.Items[0].Metadata.Name

			for i := range allDevNames {
				Expect(allDevNames[i]).To(Equal(foundDevNames[i]))
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
			dev, created, err := store.fleetStore.CreateOrUpdateFleet(ctx, orgId, &fleet)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))
			Expect(dev.ApiVersion).To(Equal(model.FleetAPI))
			Expect(dev.Kind).To(Equal(model.FleetKind))
			Expect(dev.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(dev.Status.Conditions).To(BeNil())
		})

		It("CreateOrUpdateFleet update mode", func() {
			condition := api.FleetCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("myfleet-1"),
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
			dev, created, err := store.fleetStore.CreateOrUpdateFleet(ctx, orgId, &fleet)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(dev.ApiVersion).To(Equal(model.FleetAPI))
			Expect(dev.Kind).To(Equal(model.FleetKind))
			Expect(dev.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(dev.Status.Conditions).To(BeNil())
		})

		It("UpdateFleetStatus", func() {
			condition := api.FleetCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("myfleet-1"),
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
			_, err := store.fleetStore.UpdateFleetStatus(ctx, orgId, &fleet)
			Expect(err).ToNot(HaveOccurred())
			dev, err := store.fleetStore.GetFleet(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.ApiVersion).To(Equal(model.FleetAPI))
			Expect(dev.Kind).To(Equal(model.FleetKind))
			Expect(dev.Spec.Selector.MatchLabels["key"]).To(Equal("value-1"))
			Expect(dev.Status.Conditions).ToNot(BeNil())
			Expect((*dev.Status.Conditions)[0].Type).To(Equal("type"))
		})
	})
})
