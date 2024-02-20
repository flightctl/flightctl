package store

import (
	"context"
	"errors"
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

func createFleets(numFleets int, ctx context.Context, store Store, orgId uuid.UUID, owner *string) {
	for i := 1; i <= numFleets; i++ {
		resource := api.Fleet{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("myfleet-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
				Owner:  owner,
			},
			Spec: api.FleetSpec{
				Selector: &api.LabelSelector{
					MatchLabels: map[string]string{"key": fmt.Sprintf("value-%d", i)},
				},
			},
		}
		updated := false
		callback := FleetStoreCallback(func(orgId uuid.UUID, name *string, templateUpdated bool) {
			updated = templateUpdated
		})

		_, err := store.Fleet().Create(ctx, orgId, &resource, callback)
		if err != nil {
			log.Fatalf("creating fleet: %v", err)
		}

		Expect(updated).To(BeTrue())
	}
}

var _ = Describe("FleetStore create", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		store     Store
		cfg       *config.Config
		dbName    string
		numFleets int
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numFleets = 3
		store, cfg, dbName = PrepareDBForUnitTests(log)

		createFleets(numFleets, ctx, store, orgId, nil)
	})

	AfterEach(func() {
		DeleteTestDB(cfg, store, dbName)
	})

	Context("Fleet store", func() {
		It("Get fleet success", func() {
			fleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Name).To(Equal("myfleet-1"))
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})

		It("Get fleet - not found error", func() {
			_, err := store.Fleet().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Get fleet - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := store.Fleet().Get(ctx, badOrgId, "myfleet-1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Delete fleet success", func() {
			err := store.Fleet().Delete(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete fleet success when not found", func() {
			err := store.Fleet().Delete(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete all fleets in org", func() {
			otherOrgId, _ := uuid.NewUUID()
			err := store.Fleet().DeleteAll(ctx, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			listParams := ListParams{Limit: 1000}
			fleets, err := store.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(numFleets))

			err = store.Fleet().DeleteAll(ctx, orgId)
			Expect(err).ToNot(HaveOccurred())

			fleets, err = store.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(0))
		})

		It("List with paging", func() {
			listParams := ListParams{Limit: 1000}
			allFleets, err := store.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allFleets.Items)).To(Equal(numFleets))
			allFleetNames := make([]string, len(allFleets.Items))
			for i, fleet := range allFleets.Items {
				allFleetNames[i] = *fleet.Metadata.Name
			}

			foundFleetNames := make([]string, len(allFleets.Items))
			listParams.Limit = 1
			fleets, err := store.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundFleetNames[0] = *fleets.Items[0].Metadata.Name

			cont, err := ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = store.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundFleetNames[1] = *fleets.Items[0].Metadata.Name

			cont, err = ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = store.Fleet().List(ctx, orgId, listParams)
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
			listParams := ListParams{
				Limit:  1000,
				Labels: map[string]string{"key": "value-1"}}
			fleets, err := store.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Items[0].Metadata.Name).To(Equal("myfleet-1"))
		})

		It("CreateOrUpdate create mode", func() {
			condition := api.Condition{
				Type:               api.EnrollmentRequestApproved,
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
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
					Conditions: &[]api.Condition{condition},
				},
			}
			updated := false
			callback := FleetStoreCallback(func(orgId uuid.UUID, name *string, templateUpdated bool) {
				updated = templateUpdated
			})
			_, created, err := store.Fleet().CreateOrUpdate(ctx, orgId, &fleet, callback)
			Expect(updated).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))

			createdFleet, err := store.Fleet().Get(ctx, orgId, "newresourcename")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(createdFleet.Kind).To(Equal(model.FleetKind))
			Expect(createdFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(createdFleet.Status.Conditions).To(BeNil())
			Expect(*createdFleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*createdFleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})

		It("CreateOrUpdate update mode same template", func() {
			condition := api.Condition{
				Type:               api.EnrollmentRequestApproved,
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}

			fleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
			fleet.Spec.Selector = &api.LabelSelector{MatchLabels: map[string]string{"key": "value"}}
			fleet.Status = &api.FleetStatus{Conditions: &[]api.Condition{condition}}

			updated := false
			callback := FleetStoreCallback(func(orgId uuid.UUID, name *string, templateUpdated bool) {
				updated = templateUpdated
			})
			_, created, err := store.Fleet().CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(updated).To(Equal(false))

			updatedFleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(updatedFleet.Status.Conditions).To(BeNil())
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*updatedFleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})

		It("CreateOrUpdate update mode updated spec", func() {
			condition := api.Condition{
				Type:               api.EnrollmentRequestApproved,
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}

			fleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS"}
			fleet.Status = &api.FleetStatus{Conditions: &[]api.Condition{condition}}

			updated := false
			callback := FleetStoreCallback(func(orgId uuid.UUID, name *string, templateUpdated bool) {
				updated = templateUpdated
			})
			_, created, err := store.Fleet().CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(updated).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))

			updatedFleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).To(BeNil())
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*updatedFleet.Spec.Template.Metadata.Generation).To(Equal(int64(2)))
		})

		It("CreateOrUpdate wrong owner", func() {
			condition := api.Condition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}

			fleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS"}
			fleet.Status = &api.FleetStatus{Conditions: &[]api.Condition{condition}}

			updated := false
			callback := FleetStoreCallback(func(orgId uuid.UUID, name *string, templateUpdated bool) {
				updated = templateUpdated
			})
			fleet.Metadata.Owner = util.StrToPtr("test")
			_, created, err := store.Fleet().CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(updated).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))

			updatedFleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).To(BeNil())
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*updatedFleet.Spec.Template.Metadata.Generation).To(Equal(int64(2)))
			Expect(updatedFleet.Metadata.Owner).ToNot(BeNil())
			Expect(*updatedFleet.Metadata.Owner).To(Equal("test"))

			updatedFleet.Metadata.Owner = util.StrToPtr("test2")
			updated = false
			_, _, err = store.Fleet().CreateOrUpdate(ctx, orgId, updatedFleet, callback)
			Expect(updated).To(BeFalse())
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, gorm.ErrInvalidData)).To(BeTrue())

			updatedFleet.Metadata.Owner = nil
			_, _, err = store.Fleet().CreateOrUpdate(ctx, orgId, updatedFleet, callback)
			Expect(updated).To(BeFalse())
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, gorm.ErrInvalidData)).To(BeTrue())

			updatedFleet.Metadata.Owner = util.StrToPtr("test")
			updatedFleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS2"}
			_, _, err = store.Fleet().CreateOrUpdate(ctx, orgId, updatedFleet, callback)
			Expect(updated).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
		})

		It("UnsetOwnerForMatchingFleets", func() {
			fleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Metadata.Owner = util.StrToPtr("owner")
			fleet2, err := store.Fleet().Get(ctx, orgId, "myfleet-2")
			Expect(err).ToNot(HaveOccurred())
			fleet.Metadata.Owner = util.StrToPtr("owner")
			fleet2.Metadata.Owner = util.StrToPtr("owner2")
			updated := false
			callback := FleetStoreCallback(func(orgId uuid.UUID, name *string, templateUpdated bool) {
				updated = templateUpdated
			})
			_, created, err := store.Fleet().CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(updated).To(BeFalse())
			_, created, err = store.Fleet().CreateOrUpdate(ctx, orgId, fleet2, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(updated).To(BeFalse())

			updatedFleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*updatedFleet.Metadata.Owner).To(Equal("owner"))
			err = store.Fleet().UnsetOwner(ctx, nil, orgId, "owner")
			Expect(err).ToNot(HaveOccurred())
			updatedFleet, err = store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.Metadata.Owner).To(BeNil())
			updatedFleet2, err := store.Fleet().Get(ctx, orgId, "myfleet-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet2.Metadata.Owner).ToNot(BeNil())
			Expect(*updatedFleet2.Metadata.Owner).To(Equal("owner2"))
		})

		It("CreateOrUpdateMultiple", func() {
			condition := api.Condition{
				Type:               api.EnrollmentRequestApproved,
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
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
					Conditions: &[]api.Condition{condition},
				},
			}
			fleet2 := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename_2"),
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: map[string]string{"key": "value"},
					},
				},
				Status: &api.FleetStatus{
					Conditions: &[]api.Condition{condition},
				},
			}
			updated := 0
			callback := FleetStoreCallback(func(orgId uuid.UUID, name *string, templateUpdated bool) {
				if templateUpdated {
					updated = updated + 1
				}
			})
			err := store.Fleet().CreateOrUpdateMultiple(ctx, orgId, callback, &fleet, &fleet2)
			Expect(updated).To(Equal(2))
			Expect(err).ToNot(HaveOccurred())

			createdFleet, err := store.Fleet().Get(ctx, orgId, "newresourcename")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(createdFleet.Kind).To(Equal(model.FleetKind))
			Expect(createdFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(createdFleet.Status.Conditions).To(BeNil())
			Expect(*createdFleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*createdFleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

			createdFleet2, err := store.Fleet().Get(ctx, orgId, "newresourcename_2")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet2.ApiVersion).To(Equal(model.FleetAPI))
			Expect(createdFleet2.Kind).To(Equal(model.FleetKind))
			Expect(createdFleet2.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(createdFleet2.Status.Conditions).To(BeNil())
			Expect(*createdFleet2.Metadata.Generation).To(Equal(int64(1)))
			Expect(*createdFleet2.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})
		It("CreateOrUpdateMultiple with error", func() {
			condition := api.Condition{
				Type:               api.EnrollmentRequestApproved,
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
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
					Conditions: &[]api.Condition{condition},
				},
			}
			fleet2 := api.Fleet{
				Metadata: api.ObjectMeta{},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: map[string]string{"key": "value"},
					},
				},
				Status: &api.FleetStatus{
					Conditions: &[]api.Condition{condition},
				},
			}
			updated := 0
			callback := FleetStoreCallback(func(orgId uuid.UUID, name *string, templateUpdated bool) {
				if templateUpdated {
					updated = updated + 1
				}
			})
			err := store.Fleet().CreateOrUpdateMultiple(ctx, orgId, callback, &fleet, &fleet2)
			Expect(updated).To(Equal(0))
			Expect(err).To(HaveOccurred())
		})

		It("UpdateStatus", func() {
			condition := api.Condition{
				Type:               api.EnrollmentRequestApproved,
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}

			fleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Selector = &api.LabelSelector{MatchLabels: map[string]string{"key": "value"}}
			fleet.Status = &api.FleetStatus{Conditions: &[]api.Condition{condition}}

			_, err = store.Fleet().UpdateStatus(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			updatedFleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeNil())
			Expect((*updatedFleet.Status.Conditions)[0].Type).To(Equal(api.EnrollmentRequestApproved))
		})

		It("List with owner param", func() {
			owner := "owner"
			listParams := ListParams{
				Limit: 100,
				Owner: &owner,
			}

			err := store.Fleet().DeleteAll(ctx, orgId)
			Expect(err).ToNot(HaveOccurred())
			createFleets(numFleets, ctx, store, orgId, util.StrToPtr(owner))

			fleet, err := store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).ToNot(BeNil())
			Expect(*fleet.Metadata.Owner).To(Equal(owner))

			fleets, err := store.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(numFleets))
			for i := 0; i < numFleets; i++ {
				fleetName := fmt.Sprintf("myfleet-%d", i+1)
				Expect(*fleets.Items[i].Metadata.Name).To(Equal(fleetName))
				Expect(*fleets.Items[i].Metadata.Owner).To(Equal(owner))
			}

			err = store.Fleet().UnsetOwner(ctx, nil, orgId, owner)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = store.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).To(BeNil())

			fleets, err = store.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(BeZero())
		})
	})
})
