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
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
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
		store.DeleteTestDB(log, cfg, storeInst, dbName)
	})

	Context("Fleet store", func() {
		It("Get fleet success", func() {
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Name).To(Equal("myfleet-1"))
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
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

		It("Get fleet with device summary", func() {
			testutil.CreateTestDevices(ctx, 5, storeInst.Device(), orgId, util.SetResourceOwner(api.FleetKind, "myfleet-1"), true)
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("mydevice-1"),
				},
				Status: &api.DeviceStatus{
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusHealthy,
					},
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline,
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUpToDate,
					},
				},
			}
			_, err := storeInst.Device().UpdateStatus(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			device.Metadata.Name = lo.ToPtr("mydevice-2")
			device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusDegraded
			device.Status.Summary.Status = api.DeviceSummaryStatusDegraded
			_, err = storeInst.Device().UpdateStatus(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			device.Metadata.Name = lo.ToPtr("mydevice-3")
			device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
			device.Status.Summary.Status = api.DeviceSummaryStatusOnline
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpdating
			_, err = storeInst.Device().UpdateStatus(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			device.Metadata.Name = lo.ToPtr("mydevice-4")
			device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
			device.Status.Summary.Status = api.DeviceSummaryStatusRebooting
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpdating
			_, err = storeInst.Device().UpdateStatus(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			device.Metadata.Name = lo.ToPtr("mydevice-5")
			device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusError
			device.Status.Summary.Status = api.DeviceSummaryStatusError
			device.Status.Updated.Status = api.DeviceUpdatedStatusUnknown
			_, err = storeInst.Device().UpdateStatus(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())

			// A device in another org that shouldn't be included
			testutil.CreateTestDevice(ctx, storeInst.Device(), uuid.New(), "other-org-dev", util.SetResourceOwner(api.FleetKind, "myfleet-1"), nil, nil)

			//				App:        Device:     updated:
			// mydevice-1 | Healthy   | Online    | UpToDate
			// mydevice-2 | Degraded  | Degraded  | UpToDate
			// mydevice-3 | Healthy   | Online    | Updating
			// mydevice-4 | Healthy   | Rebooting | Updating
			// mydevice-5 | Error     | Error     | Unknown
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1", store.GetWithDeviceSummary(true))
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Status.DevicesSummary).ToNot(BeNil())
			Expect(fleet.Status.DevicesSummary.Total).To(Equal(int64(5)))
			applicationStatus := fleet.Status.DevicesSummary.ApplicationStatus
			Expect(applicationStatus).ToNot(BeNil())
			Expect(applicationStatus[string(api.ApplicationsSummaryStatusHealthy)]).To(Equal(int64(3)))
			Expect(applicationStatus[string(api.ApplicationsSummaryStatusDegraded)]).To(Equal(int64(1)))
			Expect(applicationStatus[string(api.ApplicationsSummaryStatusError)]).To(Equal(int64(1)))
			summaryStatus := fleet.Status.DevicesSummary.SummaryStatus
			Expect(summaryStatus).ToNot(BeNil())
			Expect(summaryStatus[string(api.DeviceSummaryStatusOnline)]).To(Equal(int64(2)))
			Expect(summaryStatus[string(api.DeviceSummaryStatusDegraded)]).To(Equal(int64(1)))
			Expect(summaryStatus[string(api.DeviceSummaryStatusRebooting)]).To(Equal(int64(1)))
			Expect(summaryStatus[string(api.DeviceSummaryStatusError)]).To(Equal(int64(1)))
			updateStatus := fleet.Status.DevicesSummary.UpdateStatus
			Expect(updateStatus).ToNot(BeNil())
			Expect(updateStatus[string(api.DeviceUpdatedStatusUpToDate)]).To(Equal(int64(2)))
			Expect(updateStatus[string(api.DeviceUpdatedStatusUpdating)]).To(Equal(int64(2)))
			Expect(updateStatus[string(api.DeviceUpdatedStatusUnknown)]).To(Equal(int64(1)))
		})

		It("Delete fleet success", func() {
			called := false
			callback := store.FleetStoreCallback(func(uuid.UUID, *api.Fleet, *api.Fleet) {
				called = true
			})
			err := storeInst.Fleet().Delete(ctx, orgId, "myfleet-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})

		It("Delete fleet success when not found", func() {
			called := false
			callback := store.FleetStoreCallback(func(uuid.UUID, *api.Fleet, *api.Fleet) {
				called = true
			})
			err := storeInst.Fleet().Delete(ctx, orgId, "nonexistent", callback)
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
				Limit:         1000,
				LabelSelector: selector.NewLabelSelectorFromMapOrDie(map[string]string{"key": "value-1"})}
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Items[0].Metadata.Name).To(Equal("myfleet-1"))
		})

		It("List by in match expression", func() {
			listParams := store.ListParams{
				Limit: 1000,
				LabelSelector: selector.NewLabelSelectorOrDie(
					api.MatchExpression{
						Key:      "key",
						Operator: api.In,
						Values:   lo.ToPtr([]string{"value-1"}),
					}.String()),
			}
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Items[0].Metadata.Name).To(Equal("myfleet-1"))
		})
		It("List by not in match expression", func() {
			listParams := store.ListParams{
				Limit: 1000,
				LabelSelector: selector.NewLabelSelectorOrDie(
					api.MatchExpression{
						Key:      "key",
						Operator: api.NotIn,
						Values:   lo.ToPtr([]string{"value-1"}),
					}.String()),
			}
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(2))
			Expect(*fleets.Items[0].Metadata.Name).To(Equal("myfleet-2"))
			Expect(*fleets.Items[1].Metadata.Name).To(Equal("myfleet-3"))
		})

		It("List by exists match expression", func() {
			listParams := store.ListParams{
				Limit: 1000,
				LabelSelector: selector.NewLabelSelectorOrDie(
					api.MatchExpression{
						Key:      "key",
						Operator: api.Exists,
					}.String()),
			}
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(3))
			Expect(*fleets.Items[0].Metadata.Name).To(Equal("myfleet-1"))
			Expect(*fleets.Items[1].Metadata.Name).To(Equal("myfleet-2"))
			Expect(*fleets.Items[2].Metadata.Name).To(Equal("myfleet-3"))
		})

		It("List by exists match expression where key doesn't exist", func() {
			listParams := store.ListParams{
				Limit: 1000,
				LabelSelector: selector.NewLabelSelectorOrDie(
					api.MatchExpression{
						Key:      "key1",
						Operator: api.Exists,
					}.String()),
			}
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(0))
		})

		It("List by does not exist match expression", func() {
			listParams := store.ListParams{
				Limit: 1000,
				LabelSelector: selector.NewLabelSelectorOrDie(
					api.MatchExpression{
						Key:      "key",
						Operator: api.DoesNotExist,
					}.String()),
			}
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(0))
		})

		It("List by does not exist match expression where key does not exist", func() {
			listParams := store.ListParams{
				Limit: 1000,
				LabelSelector: selector.NewLabelSelectorOrDie(
					api.MatchExpression{
						Key:      "key1",
						Operator: api.DoesNotExist,
					}.String()),
			}
			fleets, err := storeInst.Fleet().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(3))
			Expect(*fleets.Items[0].Metadata.Name).To(Equal("myfleet-1"))
			Expect(*fleets.Items[1].Metadata.Name).To(Equal("myfleet-2"))
			Expect(*fleets.Items[2].Metadata.Name).To(Equal("myfleet-3"))
		})

		It("List with device count", func() {
			testutil.CreateTestDevices(ctx, 5, storeInst.Device(), orgId, util.SetResourceOwner(api.FleetKind, "myfleet-1"), true)
			testutil.CreateTestDevicesWithOffset(ctx, 3, storeInst.Device(), orgId, util.SetResourceOwner(api.FleetKind, "myfleet-2"), true, 5)
			fleets, err := storeInst.Fleet().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(3))
			lo.ForEach(fleets.Items, func(f api.Fleet, _ int) { Expect(f.Status.DevicesSummary).To(BeNil()) })
			fleets, err = storeInst.Fleet().List(ctx, orgId, store.ListParams{}, store.ListWithDevicesSummary(true))
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(3))
			for _, fleet := range fleets.Items {
				Expect(fleet.Status.DevicesSummary).ToNot(BeNil())
				total := fleet.Status.DevicesSummary.Total
				switch lo.FromPtr(fleet.Metadata.Name) {
				case "myfleet-1":
					Expect(total).To(Equal(int64(5)))
				case "myfleet-2":
					Expect(total).To(Equal(int64(3)))
				case "myfleet-3":
					Expect(total).To(Equal(int64(0)))
				default:
					Fail(fmt.Sprintf("unexpected fleet %s", lo.FromPtr(fleet.Metadata.Name)))
				}
			}
		})

		It("CreateOrUpdate create mode", func() {
			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("newresourcename"),
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{"key": "value"},
					},
				},
				Status: nil,
			}
			called := false
			callback := store.FleetStoreCallback(func(uuid.UUID, *api.Fleet, *api.Fleet) {
				called = true
			})
			_, created, err := storeInst.Fleet().CreateOrUpdate(ctx, orgId, &fleet, nil, true, callback)
			Expect(called).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))

			createdFleet, err := storeInst.Fleet().Get(ctx, orgId, "newresourcename")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet.ApiVersion).To(Equal(model.FleetAPIVersion()))
			Expect(createdFleet.Kind).To(Equal(api.FleetKind))
			Expect(lo.FromPtr(createdFleet.Spec.Selector.MatchLabels)["key"]).To(Equal("value"))
			Expect(createdFleet.Status.Conditions).ToNot(BeNil())
			Expect(createdFleet.Status.Conditions).To(BeEmpty())
			Expect(*createdFleet.Metadata.Generation).To(Equal(int64(1)))
		})

		It("CreateOrUpdate update mode same template", func() {
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))

			condition := api.Condition{
				Type:               api.ConditionTypeFleetValid,
				LastTransitionTime: time.Now(),
				Status:             api.ConditionStatusFalse,
				Reason:             "reason",
				Message:            "message",
			}
			fleet.Status = &api.FleetStatus{Conditions: []api.Condition{condition}}
			_, err = storeInst.Fleet().UpdateStatus(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			updatedFleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(updatedFleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))

			updatedFleet.Spec.Selector = &api.LabelSelector{MatchLabels: &map[string]string{"key": "value"}}
			updatedFleet.Metadata.Labels = nil
			updatedFleet.Metadata.Annotations = nil

			called := false
			callback := store.FleetStoreCallback(func(uuid.UUID, *api.Fleet, *api.Fleet) {
				called = true
			})
			returnedFleet, created, err := storeInst.Fleet().CreateOrUpdate(ctx, orgId, updatedFleet, nil, true, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(called).To(BeTrue())

			Expect(returnedFleet.Metadata.Labels).ShouldNot(BeNil())

			updatedFleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPIVersion()))
			Expect(updatedFleet.Kind).To(Equal(api.FleetKind))
			Expect(lo.FromPtr(updatedFleet.Spec.Selector.MatchLabels)["key"]).To(Equal("value"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(updatedFleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
		})

		It("CreateOrUpdate update mode updated spec", func() {
			fleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Template.Spec.Os = &api.DeviceOsSpec{Image: "my new OS"}
			fleet.Status = nil

			called := false
			callback := store.FleetStoreCallback(func(uuid.UUID, *api.Fleet, *api.Fleet) {
				called = true
			})
			_, created, err := storeInst.Fleet().CreateOrUpdate(ctx, orgId, fleet, nil, true, callback)
			Expect(called).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())

			updatedFleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPIVersion()))
			Expect(updatedFleet.Kind).To(Equal(api.FleetKind))
			Expect(lo.FromPtr(updatedFleet.Spec.Selector.MatchLabels)["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeNil())
			Expect(updatedFleet.Status.Conditions).To(BeEmpty())
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
		})

		It("UpdateStatus", func() {
			condition := api.Condition{
				Type:               api.ConditionTypeFleetValid,
				LastTransitionTime: time.Now(),
				Status:             api.ConditionStatusFalse,
				Reason:             "reason",
				Message:            "message",
			}

			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("myfleet-1"),
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{MatchLabels: &map[string]string{"key": "value"}},
				},
				Status: &api.FleetStatus{Conditions: []api.Condition{condition}},
			}

			returnedFleet, err := storeInst.Fleet().UpdateStatus(ctx, orgId, &fleet)
			Expect(err).ToNot(HaveOccurred())
			Expect(returnedFleet.ApiVersion).To(Equal(model.FleetAPIVersion()))
			Expect(returnedFleet.Kind).To(Equal(api.FleetKind))
			Expect(lo.FromPtr(returnedFleet.Spec.Selector.MatchLabels)["key"]).To(Equal("value-1"))
			Expect(returnedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(returnedFleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))

			updatedFleet, err := storeInst.Fleet().Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPIVersion()))
			Expect(updatedFleet.Kind).To(Equal(api.FleetKind))
			Expect(lo.FromPtr(updatedFleet.Spec.Selector.MatchLabels)["key"]).To(Equal("value-1"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(updatedFleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
		})

		It("List with owner param", func() {
			owner := "owner"
			listParams := store.ListParams{
				Limit: 100,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(
					map[string]string{"metadata.owner": owner}, selector.WithPrivateSelectors()),
			}

			callback := store.FleetStoreAllDeletedCallback(func(orgId uuid.UUID) {})
			err := storeInst.Fleet().DeleteAll(ctx, orgId, callback)
			Expect(err).ToNot(HaveOccurred())
			testutil.CreateTestFleets(ctx, numFleets, storeInst.Fleet(), orgId, "myfleet", true, lo.ToPtr(owner))

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
					Type:    api.ConditionTypeEnrollmentRequestApproved,
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
			Expect(updatedFleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeEnrollmentRequestApproved))
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
			callback := store.FleetStoreCallback(func(uuid.UUID, *api.Fleet, *api.Fleet) {
				called = true
			})
			err = storeInst.Fleet().Delete(ctx, orgId, "myfleet-1", callback)
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
