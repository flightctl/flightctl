package store_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("FleetStore create", func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		orgId     uuid.UUID
		storeInst store.Store
		cfg       *config.Config
		dbName    string
		numFleets int
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numFleets = 3
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(log)

		testutil.CreateTestFleets(ctx, 3, storeInst.Fleet(), orgId, "myfleet", false, nil)
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	Context("Fleet store", func() {
		It("Get fleet success", func() {
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Name).To(Equal("myfleet-1"))
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})

		It("Get fleet - not found error", func() {
			_, err := storeInst.Fleet().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get fleet - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := storeInst.Fleet().Get(ctx, badOrgId, "myfleet-1")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Delete fleet success", func() {
			called := false
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called = true
			})
			err := storeInst.Fleet().Delete(ctx, orgId, callback, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})

		It("Delete fleet success when not found", func() {
			called := false
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called = true
			})
			err := storeInst.Fleet().Delete(ctx, orgId, callback, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeFalse())
		})

		It("Delete all fleets in org", func() {
			called := false
			callback := store.FleetStoreAllDeletedCallback(func(orgId uuid.UUID) {
				called = true
			})

			otherOrgId, _ := uuid.NewUUID()
			err := storeInst.Fleet().DeleteAll(ctx, otherOrgId, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			listParams := store.ListParams{Limit: 1000}
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(numFleets))

			called = false
			err = storeInst.Fleet().DeleteAll(ctx, orgId, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			fleets, err = storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(0))
		})

		It("List with paging", func() {
			listParams := store.ListParams{Limit: 1000}
			allFleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allFleets.Items)).To(Equal(numFleets))
			allFleetNames := make([]string, len(allFleets.Items))
			for i, fleet := range allFleets.Items {
				allFleetNames[i] = *fleet.Metadata.Name
			}

			foundFleetNames := make([]string, len(allFleets.Items))
			listParams.Limit = 1
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundFleetNames[0] = *fleets.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundFleetNames[1] = *fleets.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(fleets.Metadata.RemainingItemCount).To(BeNil())
			Expect(fleets.Metadata.Continue).To(BeNil())
			foundFleetNames[2] = *fleets.Items[0].Metadata.Name

			for i := range allFleetNames {
				Expect(allFleetNames[i]).To(Equal(foundFleetNames[i]))
			}
		})

		It("List by label", func() {
			listParams := store.ListParams{
				Limit:  1000,
				Labels: map[string]string{"key": "value-1"}}
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Items[0].Metadata.Name).To(Equal("myfleet-1"))
		})

		It("CreateOrUpdate create mode", func() {
			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename"),
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: map[string]string{"key": "value"},
					},
				},
				Status: nil,
			}
			called := false
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called = true
			})
			_, created, err := storeInst.Fleet().CreateOrUpdate(ctx, orgId, &fleet, callback)
			Expect(called).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))

			createdFleet, err := storeInst.Fleet().Get(ctx, orgId, "newresourcename")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(createdFleet.Kind).To(Equal(model.FleetKind))
			Expect(createdFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(createdFleet.Status.Conditions).ToNot(BeNil())
			Expect(createdFleet.Status.Conditions).To(BeEmpty())
			Expect(*createdFleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*createdFleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})

		It("CreateOrUpdate update mode same template", func() {
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
			fleet.Spec.Selector = &api.LabelSelector{MatchLabels: map[string]string{"key": "value"}}
			fleet.Status = nil

			called := false
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called = true
			})
			_, created, err := storeInst.Fleet().CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(called).To(BeTrue())

			updatedFleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeNil())
			Expect(updatedFleet.Status.Conditions).To(BeEmpty())
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*updatedFleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})

		It("CreateOrUpdate update mode updated spec", func() {
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS"}
			fleet.Status = nil

			called := false
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called = true
			})
			_, created, err := storeInst.Fleet().CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(called).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())

			updatedFleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeNil())
			Expect(updatedFleet.Status.Conditions).To(BeEmpty())
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*updatedFleet.Spec.Template.Metadata.Generation).To(Equal(int64(2)))
		})

		It("CreateOrUpdate wrong owner", func() {
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS"}
			fleet.Status = nil

			called := false
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called = true
			})
			fleet.Metadata.Owner = util.StrToPtr("test")
			_, created, err := storeInst.Fleet().CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(called).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))

			updatedFleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeNil())
			Expect(updatedFleet.Status.Conditions).To(BeEmpty())
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*updatedFleet.Spec.Template.Metadata.Generation).To(Equal(int64(2)))
			Expect(updatedFleet.Metadata.Owner).ToNot(BeNil())
			Expect(*updatedFleet.Metadata.Owner).To(Equal("test"))

			updatedFleet.Metadata.Owner = util.StrToPtr("test2")
			called = false
			_, _, err = storeInst.Fleet().CreateOrUpdate(ctx, orgId, updatedFleet, callback)
			Expect(called).To(BeFalse())
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrUpdatingResourceWithOwnerNotAllowed))

			updatedFleet.Metadata.Owner = nil
			_, _, err = storeInst.Fleet().CreateOrUpdate(ctx, orgId, updatedFleet, callback)
			Expect(called).To(BeFalse())
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrUpdatingResourceWithOwnerNotAllowed))

			updatedFleet.Metadata.Owner = util.StrToPtr("test")
			updatedFleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS2"}
			_, _, err = storeInst.Fleet().CreateOrUpdate(ctx, orgId, updatedFleet, callback)
			Expect(called).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
		})

		It("UnsetOwnerForMatchingFleets", func() {
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Metadata.Owner = util.StrToPtr("owner")
			fleet2, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-2")
			Expect(err).ToNot(HaveOccurred())
			fleet.Metadata.Owner = util.StrToPtr("owner")
			fleet2.Metadata.Owner = util.StrToPtr("owner2")
			called := false
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called = true
			})
			_, created, err := storeInst.Fleet().CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(called).To(BeTrue())
			_, created, err = storeInst.Fleet().CreateOrUpdate(ctx, orgId, fleet2, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(called).To(BeTrue())

			updatedFleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*updatedFleet.Metadata.Owner).To(Equal("owner"))
			err = storeInst.Fleet().UnsetOwner(ctx, nil, orgId, "owner")
			Expect(err).ToNot(HaveOccurred())
			updatedFleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.Metadata.Owner).To(BeNil())
			updatedFleet2, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet2.Metadata.Owner).ToNot(BeNil())
			Expect(*updatedFleet2.Metadata.Owner).To(Equal("owner2"))
		})

		It("CreateOrUpdateMultiple", func() {
			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename"),
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: map[string]string{"key": "value"},
					},
				},
				Status: nil,
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
				Status: nil,
			}
			called := 0
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called++
			})
			err := storeInst.Fleet().CreateOrUpdateMultiple(ctx, orgId, callback, &fleet, &fleet2)
			Expect(called).To(Equal(2))
			Expect(err).ToNot(HaveOccurred())

			createdFleet, err := storeInst.Fleet().Get(ctx, orgId, "newresourcename")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(createdFleet.Kind).To(Equal(model.FleetKind))
			Expect(createdFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(createdFleet.Status.Conditions).ToNot(BeNil())
			Expect(createdFleet.Status.Conditions).To(BeEmpty())
			Expect(*createdFleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*createdFleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

			createdFleet2, err := storeInst.Fleet().Get(ctx, orgId, "newresourcename_2")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet2.ApiVersion).To(Equal(model.FleetAPI))
			Expect(createdFleet2.Kind).To(Equal(model.FleetKind))
			Expect(createdFleet2.Spec.Selector.MatchLabels["key"]).To(Equal("value"))
			Expect(createdFleet.Status.Conditions).ToNot(BeNil())
			Expect(createdFleet2.Status.Conditions).To(BeEmpty())
			Expect(*createdFleet2.Metadata.Generation).To(Equal(int64(1)))
			Expect(*createdFleet2.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
		})
		It("CreateOrUpdateMultiple with error", func() {
			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename"),
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: map[string]string{"key": "value"},
					},
				},
				Status: nil,
			}
			fleet2 := api.Fleet{
				Metadata: api.ObjectMeta{},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: map[string]string{"key": "value"},
					},
				},
				Status: nil,
			}
			called := 0
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called++
			})
			err := storeInst.Fleet().CreateOrUpdateMultiple(ctx, orgId, callback, &fleet, &fleet2)
			Expect(called).To(Equal(0))
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNameIsNil))
		})

		It("UpdateStatus", func() {
			condition := api.Condition{
				Type:               api.EnrollmentRequestApproved,
				LastTransitionTime: time.Now(),
				Status:             api.ConditionStatusFalse,
				Reason:             "reason",
				Message:            "message",
			}

			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Selector = &api.LabelSelector{MatchLabels: map[string]string{"key": "value"}}
			fleet.Status = &api.FleetStatus{Conditions: []api.Condition{condition}}

			_, err = storeInst.Fleet().UpdateStatus(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			updatedFleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPI))
			Expect(updatedFleet.Kind).To(Equal(model.FleetKind))
			Expect(updatedFleet.Spec.Selector.MatchLabels["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(updatedFleet.Status.Conditions[0].Type).To(Equal(api.EnrollmentRequestApproved))
		})

		It("List with owner param", func() {
			owner := "owner"
			listParams := store.ListParams{
				Limit: 100,
				Owner: &owner,
			}

			callback := store.FleetStoreAllDeletedCallback(func(orgId uuid.UUID) {})
			err := storeInst.Fleet().DeleteAll(ctx, orgId, callback)
			Expect(err).ToNot(HaveOccurred())
			testutil.CreateTestFleets(ctx, numFleets, storeInst.Fleet(), orgId, "myfleet", true, util.StrToPtr(owner))

			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).ToNot(BeNil())
			Expect(*fleet.Metadata.Owner).To(Equal(owner))

			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(numFleets))
			for i := 0; i < numFleets; i++ {
				fleetName := fmt.Sprintf("myfleet-%d", i+1)
				Expect(*fleets.Items[i].Metadata.Name).To(Equal(fleetName))
				Expect(*fleets.Items[i].Metadata.Owner).To(Equal(owner))
			}

			err = storeInst.Fleet().UnsetOwner(ctx, nil, orgId, owner)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).To(BeNil())

			fleets, err = storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(BeZero())
		})

		It("UpdateConditions", func() {
			conditions := []api.Condition{
				{
					Type:    api.EnrollmentRequestApproved,
					Status:  api.ConditionStatusFalse,
					Reason:  "reason",
					Message: "message",
				},
			}

			err := storeInst.Fleet().UpdateConditions(ctx, orgId, "myfleet-1", conditions)
			Expect(err).ToNot(HaveOccurred())
			updatedFleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(updatedFleet.Status.Conditions[0].Type).To(Equal(api.EnrollmentRequestApproved))
			Expect(updatedFleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))
		})

		It("OverwriteRepositoryRefs", func() {
			err := testutil.CreateRepositories(ctx, 2, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.Fleet().OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			fleets, err := storeInst.Repository().GetFleetRefs(ctx, orgId, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets.Items).To(HaveLen(1))
			Expect(*(fleets.Items[0]).Metadata.Name).To(Equal("myfleet-1"))

			err = storeInst.Fleet().OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			repos, err = storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-2"))

			fleets, err = storeInst.Repository().GetFleetRefs(ctx, orgId, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets.Items).To(HaveLen(0))

			fleets, err = storeInst.Repository().GetFleetRefs(ctx, orgId, "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets.Items).To(HaveLen(1))
			Expect(*(fleets.Items[0]).Metadata.Name).To(Equal("myfleet-1"))
		})

		It("Delete fleet with repo association", func() {
			err := testutil.CreateRepositories(ctx, 1, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.Fleet().OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			called := false
			callback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {
				called = true
			})
			err = storeInst.Fleet().Delete(ctx, orgId, callback, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})

		It("Delete all fleets with repo association", func() {
			err := testutil.CreateRepositories(ctx, 1, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.Fleet().OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			called := false
			callback := store.FleetStoreAllDeletedCallback(func(orgId uuid.UUID) {
				called = true
			})
			err = storeInst.Fleet().DeleteAll(ctx, orgId, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})
	})
})
