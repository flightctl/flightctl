package store_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
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

var _ = Describe("FleetStore create", func() {
	var (
		log               *logrus.Logger
		ctx               context.Context
		orgId             uuid.UUID
		fleetStore        fleetstore.Store
		deviceStore       devicestore.Store
		resourceSyncStore resourcesyncstore.Store
		repositoryStore   repositorystore.Store
		organizationStore organizationstore.Store
		cfg               *config.Config
		dbName            string
		db                *gorm.DB
		numFleets         int
		called            bool
		callback          store.EventCallback
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		numFleets = 3
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		resourceSyncStore = resourcesyncstore.NewResourceSyncStore(db, log.WithField("pkg", "resourcesync-store"))
		repositoryStore = repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		organizationStore = organizationstore.NewOrganizationStore(db)

		orgId = uuid.New()
		err = testutil.CreateTestOrganization(ctx, organizationStore, orgId)
		Expect(err).ToNot(HaveOccurred())

		called = false
		callback = store.EventCallback(func(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
			called = true
		})

		testutil.CreateTestFleets(ctx, 3, fleetStore, orgId, "myfleet", false, nil)
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	Context("Fleet store", func() {
		It("Get fleet success", func() {
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Name).To(Equal("myfleet-1"))
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
		})

		It("Get fleet - not found error", func() {
			_, err := fleetStore.Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get fleet - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := fleetStore.Get(ctx, badOrgId, "myfleet-1")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get fleet with device summary", func() {
			testutil.CreateTestDevices(ctx, 5, deviceStore, orgId, util.SetResourceOwner(api.FleetKind, "myfleet-1"), true)
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
			_, err := deviceStore.UpdateStatus(ctx, orgId, &device, nil)
			Expect(err).ToNot(HaveOccurred())
			device.Metadata.Name = lo.ToPtr("mydevice-2")
			device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusDegraded
			device.Status.Summary.Status = api.DeviceSummaryStatusDegraded
			_, err = deviceStore.UpdateStatus(ctx, orgId, &device, nil)
			Expect(err).ToNot(HaveOccurred())
			device.Metadata.Name = lo.ToPtr("mydevice-3")
			device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
			device.Status.Summary.Status = api.DeviceSummaryStatusOnline
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpdating
			_, err = deviceStore.UpdateStatus(ctx, orgId, &device, nil)
			Expect(err).ToNot(HaveOccurred())
			device.Metadata.Name = lo.ToPtr("mydevice-4")
			device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
			device.Status.Summary.Status = api.DeviceSummaryStatusRebooting
			device.Status.Updated.Status = api.DeviceUpdatedStatusUpdating
			_, err = deviceStore.UpdateStatus(ctx, orgId, &device, nil)
			Expect(err).ToNot(HaveOccurred())
			device.Metadata.Name = lo.ToPtr("mydevice-5")
			device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusError
			device.Status.Summary.Status = api.DeviceSummaryStatusError
			device.Status.Updated.Status = api.DeviceUpdatedStatusUnknown
			_, err = deviceStore.UpdateStatus(ctx, orgId, &device, nil)
			Expect(err).ToNot(HaveOccurred())

			otherOrgId := uuid.New()
			err = testutil.CreateTestOrganization(ctx, organizationStore, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			// A device in another org that shouldn't be included
			testutil.CreateTestDevice(ctx, deviceStore, otherOrgId, "other-org-dev", util.SetResourceOwner(api.FleetKind, "myfleet-1"), nil, nil)

			//				App:        Device:     updated:
			// mydevice-1 | Healthy   | Online    | UpToDate
			// mydevice-2 | Degraded  | Degraded  | UpToDate
			// mydevice-3 | Healthy   | Online    | Updating
			// mydevice-4 | Healthy   | Rebooting | Updating
			// mydevice-5 | Error     | Error     | Unknown
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1", fleetstore.GetWithDeviceSummary(true))
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
			err := fleetStore.Delete(ctx, orgId, "myfleet-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})

		It("Delete fleet success when not found", func() {
			err := fleetStore.Delete(ctx, orgId, "nonexistent", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeFalse())
		})

		It("List with paging", func() {
			listParams := store.ListParams{Limit: 1000}
			allFleets, err := fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allFleets.Items)).To(Equal(numFleets))
			allFleetNames := make([]string, len(allFleets.Items))
			for i, fleet := range allFleets.Items {
				allFleetNames[i] = *fleet.Metadata.Name
			}

			foundFleetNames := make([]string, len(allFleets.Items))
			listParams.Limit = 1
			fleets, err := fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundFleetNames[0] = *fleets.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(1))
			Expect(*fleets.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundFleetNames[1] = *fleets.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(fleets.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			fleets, err = fleetStore.List(ctx, orgId, listParams)
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
			fleets, err := fleetStore.List(ctx, orgId, listParams)
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
			fleets, err := fleetStore.List(ctx, orgId, listParams)
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
			fleets, err := fleetStore.List(ctx, orgId, listParams)
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
			fleets, err := fleetStore.List(ctx, orgId, listParams)
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
			fleets, err := fleetStore.List(ctx, orgId, listParams)
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
			fleets, err := fleetStore.List(ctx, orgId, listParams)
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
			fleets, err := fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(3))
			Expect(*fleets.Items[0].Metadata.Name).To(Equal("myfleet-1"))
			Expect(*fleets.Items[1].Metadata.Name).To(Equal("myfleet-2"))
			Expect(*fleets.Items[2].Metadata.Name).To(Equal("myfleet-3"))
		})

		It("List with device count", func() {
			testutil.CreateTestDevices(ctx, 5, deviceStore, orgId, util.SetResourceOwner(api.FleetKind, "myfleet-1"), true)
			testutil.CreateTestDevicesWithOffset(ctx, 3, deviceStore, orgId, util.SetResourceOwner(api.FleetKind, "myfleet-2"), true, 5)
			fleets, err := fleetStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(3))
			lo.ForEach(fleets.Items, func(f api.Fleet, _ int) { Expect(f.Status.DevicesSummary).To(BeNil()) })
			fleets, err = fleetStore.List(ctx, orgId, store.ListParams{}, fleetstore.ListWithDevicesSummary(true))
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
			_, created, err := fleetStore.CreateOrUpdate(ctx, orgId, &fleet, nil, callback)
			Expect(called).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))

			createdFleet, err := fleetStore.Get(ctx, orgId, "newresourcename")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet.ApiVersion).To(Equal(model.FleetAPIVersion()))
			Expect(createdFleet.Kind).To(Equal(api.FleetKind))
			Expect(lo.FromPtr(createdFleet.Spec.Selector.MatchLabels)["key"]).To(Equal("value"))
			Expect(createdFleet.Status.Conditions).ToNot(BeNil())
			Expect(createdFleet.Status.Conditions).To(BeEmpty())
			Expect(*createdFleet.Metadata.Generation).To(Equal(int64(1)))
		})

		It("CreateOrUpdate update mode same template", func() {
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
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
			_, err = fleetStore.UpdateStatus(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			updatedFleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(updatedFleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))

			updatedFleet.Spec.Selector = &api.LabelSelector{MatchLabels: &map[string]string{"key": "value"}}
			updatedFleet.Metadata.Labels = nil
			updatedFleet.Metadata.Annotations = nil

			returnedFleet, created, err := fleetStore.CreateOrUpdate(ctx, orgId, updatedFleet, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(called).To(BeTrue())

			Expect(returnedFleet.Metadata.Labels).ShouldNot(BeNil())

			updatedFleet, err = fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.ApiVersion).To(Equal(model.FleetAPIVersion()))
			Expect(updatedFleet.Kind).To(Equal(api.FleetKind))
			Expect(lo.FromPtr(updatedFleet.Spec.Selector.MatchLabels)["key"]).To(Equal("value"))
			Expect(updatedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(updatedFleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(*updatedFleet.Metadata.Generation).To(Equal(int64(2)))
		})

		It("CreateOrUpdate update mode updated spec", func() {
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Template.Spec.Os = &api.DeviceOsSpec{Image: "my new OS"}
			fleet.Status = nil

			_, created, err := fleetStore.CreateOrUpdate(ctx, orgId, fleet, nil, callback)
			Expect(called).To(BeTrue())
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())

			updatedFleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
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

			returnedFleet, err := fleetStore.UpdateStatus(ctx, orgId, &fleet)
			Expect(err).ToNot(HaveOccurred())
			Expect(returnedFleet.ApiVersion).To(Equal(model.FleetAPIVersion()))
			Expect(returnedFleet.Kind).To(Equal(api.FleetKind))
			Expect(lo.FromPtr(returnedFleet.Spec.Selector.MatchLabels)["key"]).To(Equal("value-1"))
			Expect(returnedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(returnedFleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))

			updatedFleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
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

			for i := 1; i <= numFleets; i++ {
				called = false
				err := fleetStore.Delete(ctx, orgId, fmt.Sprintf("myfleet-%d", i), callback)
				Expect(err).ToNot(HaveOccurred())
				Expect(called).To(BeTrue())
			}
			testutil.CreateTestFleets(ctx, numFleets, fleetStore, orgId, "myfleet", true, lo.ToPtr(owner))

			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).ToNot(BeNil())
			Expect(*fleet.Metadata.Owner).To(Equal(owner))

			fleets, err := fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(numFleets))
			for i := 0; i < numFleets; i++ {
				fleetName := fmt.Sprintf("myfleet-%d", i+1)
				Expect(*fleets.Items[i].Metadata.Name).To(Equal(fleetName))
				Expect(*fleets.Items[i].Metadata.Owner).To(Equal(owner))
			}

			err = fleetStore.UnsetOwner(ctx, nil, orgId, owner)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Owner).To(BeNil())

			fleets, err = fleetStore.List(ctx, orgId, listParams)
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

			err := fleetStore.UpdateConditions(ctx, orgId, "myfleet-1", conditions, nil)
			Expect(err).ToNot(HaveOccurred())
			updatedFleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedFleet.Status.Conditions).ToNot(BeEmpty())
			Expect(updatedFleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeEnrollmentRequestApproved))
			Expect(updatedFleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))
		})

		It("OverwriteRepositoryRefs", func() {
			err := testutil.CreateRepositories(ctx, 2, repositoryStore, orgId)
			Expect(err).ToNot(HaveOccurred())

			err = fleetStore.OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := fleetStore.GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			fleets, err := repositoryStore.GetFleetRefs(ctx, orgId, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets.Items).To(HaveLen(1))
			Expect(*(fleets.Items[0]).Metadata.Name).To(Equal("myfleet-1"))

			err = fleetStore.OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			repos, err = fleetStore.GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-2"))

			fleets, err = repositoryStore.GetFleetRefs(ctx, orgId, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets.Items).To(HaveLen(0))

			fleets, err = repositoryStore.GetFleetRefs(ctx, orgId, "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets.Items).To(HaveLen(1))
			Expect(*(fleets.Items[0]).Metadata.Name).To(Equal("myfleet-1"))
		})

		It("Delete fleet with repo association", func() {
			err := testutil.CreateRepositories(ctx, 1, repositoryStore, orgId)
			Expect(err).ToNot(HaveOccurred())

			err = fleetStore.OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := fleetStore.GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = fleetStore.Delete(ctx, orgId, "myfleet-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})

		It("CountByRolloutStatus - with specific orgId", func() {
			// Create fleets with different rollout statuses using RolloutInProgress conditions
			fleet1 := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("fleet-rollout-1"),
				},
				Status: &api.FleetStatus{
					Conditions: []api.Condition{
						{
							Type:   api.ConditionTypeFleetRolloutInProgress,
							Status: api.ConditionStatusTrue,
							Reason: api.RolloutActiveReason,
						},
					},
				},
			}
			fleet2 := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("fleet-rollout-2"),
				},
				Status: &api.FleetStatus{
					Conditions: []api.Condition{
						{
							Type:   api.ConditionTypeFleetRolloutInProgress,
							Status: api.ConditionStatusFalse,
							Reason: api.RolloutSuspendedReason,
						},
					},
				},
			}
			fleet3 := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("fleet-rollout-3"),
				},
				Status: &api.FleetStatus{
					Conditions: []api.Condition{
						{
							Type:   api.ConditionTypeFleetRolloutInProgress,
							Status: api.ConditionStatusTrue,
							Reason: api.RolloutActiveReason,
						},
					},
				},
			}

			_, err := fleetStore.Create(ctx, orgId, &fleet1, nil)
			Expect(err).ToNot(HaveOccurred())
			_, err = fleetStore.Create(ctx, orgId, &fleet2, nil)
			Expect(err).ToNot(HaveOccurred())
			_, err = fleetStore.Create(ctx, orgId, &fleet3, nil)
			Expect(err).ToNot(HaveOccurred())

			// Test with specific orgId
			results, err := fleetStore.CountByRolloutStatus(ctx, &orgId, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())

			// Count expected results: 3 original fleets + 3 new ones = 6 total
			// Original fleets have no RolloutInProgress condition (will be "Inactive")
			// New fleets have: 2 Active, 1 Suspended
			totalCount := int64(0)
			statusCounts := make(map[string]int64)
			for _, result := range results {
				Expect(result.OrgID).To(Equal(orgId.String()))
				totalCount += result.Count
				statusCounts[result.Status] += result.Count
			}
			Expect(totalCount).To(Equal(int64(6))) // 3 original + 3 new fleets

			// Check specific status counts
			Expect(statusCounts["Inactive"]).To(Equal(int64(3)))  // Original fleets (no RolloutInProgress condition)
			Expect(statusCounts["Active"]).To(Equal(int64(2)))    // fleet-rollout-1 and fleet-rollout-3
			Expect(statusCounts["Suspended"]).To(Equal(int64(1))) // fleet-rollout-2
		})

		It("CountByRolloutStatus - with nil orgId (all orgs)", func() {
			// Create another org with a fleet
			otherOrgId := uuid.New()
			err := testutil.CreateTestOrganization(ctx, organizationStore, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("other-org-fleet"),
				},
				Status: &api.FleetStatus{
					Conditions: []api.Condition{
						{
							Type:   api.ConditionTypeFleetRolloutInProgress,
							Status: api.ConditionStatusFalse,
							Reason: api.RolloutWaitingReason,
						},
					},
				},
			}
			_, err = fleetStore.Create(ctx, otherOrgId, &fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			// Test with nil orgId (should get all orgs)
			results, err := fleetStore.CountByRolloutStatus(ctx, nil, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())

			// Should have results for both organizations
			orgIds := make(map[string]bool)
			for _, result := range results {
				orgIds[result.OrgID] = true
			}
			Expect(orgIds).To(HaveKey(orgId.String()))
			Expect(orgIds).To(HaveKey(otherOrgId.String()))
		})

		It("Store allows updating owned fleet spec and labels (ownership enforced in service)", func() {
			resourceSync := api.ResourceSync{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("test-resourcesync"),
				},
				Spec: api.ResourceSyncSpec{
					Repository: "myrepo",
					Path:       "my/path",
				},
			}
			_, err := resourceSyncStore.Create(ctx, orgId, &resourceSync, nil)
			Expect(err).ToNot(HaveOccurred())

			owner := util.SetResourceOwner(api.ResourceSyncKind, "test-resourcesync")
			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("owned-fleet"),
					Labels: &map[string]string{"original": "label"},
					Owner:  owner,
				},
				Spec: api.FleetSpec{
					Selector: &api.LabelSelector{
						MatchLabels: &map[string]string{"key": "original"},
					},
				},
			}
			_, err = fleetStore.Create(ctx, orgId, &fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			createdFleet, err := fleetStore.Get(ctx, orgId, "owned-fleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(createdFleet.Metadata.Owner).ToNot(BeNil())
			Expect(*createdFleet.Metadata.Owner).To(Equal("ResourceSync/test-resourcesync"))

			updatedFleet := *createdFleet
			updatedFleet.Spec.Selector = &api.LabelSelector{
				MatchLabels: &map[string]string{"key": "updated"},
			}
			_, err = fleetStore.Update(ctx, orgId, &updatedFleet, nil, nil)
			Expect(err).ToNot(HaveOccurred())

			refetched, err := fleetStore.Get(ctx, orgId, "owned-fleet")
			Expect(err).ToNot(HaveOccurred())
			refetched.Metadata.Labels = &map[string]string{"updated": "label"}
			_, err = fleetStore.Update(ctx, orgId, refetched, nil, nil)
			Expect(err).ToNot(HaveOccurred())

			got, err := fleetStore.Get(ctx, orgId, "owned-fleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Spec.Selector.MatchLabels).To(Equal(&map[string]string{"key": "updated"}))
			Expect(got.Metadata.Labels).To(Equal(&map[string]string{"updated": "label"}))
		})

		It("MutateAnnotation sets the key from an initial empty value and preserves other annotations", func() {
			err := fleetStore.UpdateAnnotations(ctx, orgId, "myfleet-1", map[string]string{"unrelated": "kept"}, nil, nil)
			Expect(err).ToNot(HaveOccurred())

			err = fleetStore.MutateAnnotation(ctx, orgId, "myfleet-1", "counter", func(current string) (string, error) {
				Expect(current).To(Equal(""), "mutate should observe the empty string when the key is unset")
				return "1", nil
			})
			Expect(err).ToNot(HaveOccurred())

			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect((*fleet.Metadata.Annotations)["counter"]).To(Equal("1"))
			Expect((*fleet.Metadata.Annotations)["unrelated"]).To(Equal("kept"), "MutateAnnotation must not clobber unrelated annotation keys")
		})

		It("MutateAnnotation re-invokes mutate with the freshly-read value on every call", func() {
			for i := 1; i <= 3; i++ {
				expected := i
				err := fleetStore.MutateAnnotation(ctx, orgId, "myfleet-1", "counter", func(current string) (string, error) {
					n := 0
					if current != "" {
						_, err := fmt.Sscanf(current, "%d", &n)
						Expect(err).ToNot(HaveOccurred())
					}
					return fmt.Sprintf("%d", n+1), nil
				})
				Expect(err).ToNot(HaveOccurred())

				fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
				Expect(err).ToNot(HaveOccurred())
				Expect((*fleet.Metadata.Annotations)["counter"]).To(Equal(fmt.Sprintf("%d", expected)))
			}
		})

		It("MutateAnnotation is safe against concurrent read-modify-write races (no lost updates)", func() {
			const numWriters = 8
			errCh := make(chan error, numWriters)
			for i := 0; i < numWriters; i++ {
				go func() {
					errCh <- fleetStore.MutateAnnotation(ctx, orgId, "myfleet-1", "counter", func(current string) (string, error) {
						n := 0
						if current != "" {
							_, err := fmt.Sscanf(current, "%d", &n)
							if err != nil {
								return "", err
							}
						}
						return fmt.Sprintf("%d", n+1), nil
					})
				}()
			}
			for i := 0; i < numWriters; i++ {
				Expect(<-errCh).ToNot(HaveOccurred())
			}

			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect((*fleet.Metadata.Annotations)["counter"]).To(Equal(fmt.Sprintf("%d", numWriters)),
				"every concurrent increment must be observed; a lost update would leave the counter below numWriters")
		})

	})
})
