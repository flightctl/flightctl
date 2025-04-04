package tasks_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/tasks_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

var _ = Describe("FleetSelector", func() {
	var (
		log             *logrus.Logger
		ctx             context.Context
		orgId           uuid.UUID
		deviceStore     store.Device
		fleetStore      store.Fleet
		storeInst       store.Store
		serviceHandler  *service.ServiceHandler
		cfg             *config.Config
		dbName          string
		callbackManager tasks_client.CallbackManager
		logic           tasks.FleetSelectorMatchingLogic
	)

	BeforeEach(func() {
		ctx = context.WithValue(context.Background(), service.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(log)
		deviceStore = storeInst.Device()
		fleetStore = storeInst.Fleet()
		ctrl := gomock.NewController(GinkgoT())
		publisher := queues.NewMockPublisher(ctrl)
		publisher.EXPECT().Publish(gomock.Any()).Return(nil).AnyTimes()
		callbackManager = tasks_client.NewCallbackManager(publisher, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, callbackManager, kvStore, nil, log, "", "")
		logic = tasks.NewFleetSelectorMatchingLogic(callbackManager, log, serviceHandler, tasks_client.ResourceReference{OrgID: orgId, Name: "fleet", Kind: api.FleetKind})
		logic.SetItemsPerPage(2)
	})

	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
	})

	Context("FleetSelector", func() {
		It("Fleet selector updated no overlap", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", &map[string]string{"key": "value"}, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "otherfleet", &map[string]string{"otherkey": "othervalue"}, nil)

			// This device has no current owner, should now match "fleet"
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "no-owner", nil, nil, &map[string]string{"key": "value"})
			// This device is owned by "otherfleet", should now match "fleet"
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "otherfleet-to-fleet", lo.ToPtr("Fleet/otherfleet"), nil, &map[string]string{"key": "value"})
			// This device is owned by "fleet", but no longer matches it
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "fleet-to-none", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key": "novalue"})
			// This device is owned by "fleet" and stays that way
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "stay-in-fleet", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key": "value"})
			// This device is owned by "otherfleet", should now match both fleets (error)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "otherfleet-to-error", lo.ToPtr("Fleet/otherfleet"), nil, &map[string]string{"key": "value", "otherkey": "othervalue"})

			err := logic.FleetSelectorUpdatedNoOverlapping(ctx)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(5))

			for _, device := range devices.Items {
				switch *device.Metadata.Name {
				case "no-owner":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet"))
				case "otherfleet-to-fleet":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet"))
				case "fleet-to-none":
					Expect(device.Metadata.Owner).To(BeNil())
				case "stay-in-fleet":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet"))
				case "otherfleet-to-error":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/otherfleet"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeTrue())
				}
			}

			// Both fleets now overlap
			fleets, err := fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(2))
			for _, fleet := range fleets.Items {
				cond := api.IsStatusConditionTrue(fleet.Status.Conditions, api.ConditionTypeFleetOverlappingSelectors)
				Expect(cond).To(BeTrue())
			}
		})

		It("Fleet deleted with no overlap should remove device owners", func() {
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key": "value"})
			err := logic.FleetSelectorUpdatedNoOverlapping(ctx)
			Expect(err).ToNot(HaveOccurred())
			device, err := deviceStore.Get(ctx, orgId, "device")
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Metadata.Owner).To(BeNil())
		})

		It("Nil fleet selector should match no devices", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", nil, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "nillabels", lo.ToPtr("Fleet/fleet"), nil, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "emptylabels", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device1", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key1": "value1"})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device2", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key2": "value2"})
			err := logic.FleetSelectorUpdatedNoOverlapping(ctx)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(4))
			for _, device := range devices.Items {
				Expect(device.Metadata.Owner).To(BeNil())
			}
		})

		It("Empty fleet selector should match no devices", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", &map[string]string{}, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "nillabels", lo.ToPtr("Fleet/fleet"), nil, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "emptylabels", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device1", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key1": "value1"})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device2", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key2": "value2"})
			err := logic.FleetSelectorUpdatedNoOverlapping(ctx)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(4))
			for _, device := range devices.Items {
				Expect(device.Metadata.Owner).To(BeNil())
			}
		})

		It("Fleet selector updated with overlap", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", &map[string]string{"key1": "val1"}, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet2", &map[string]string{"key2": "val2"}, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet3", &map[string]string{"key3": "val3"}, nil)

			// All fleets were overlapping
			condition := api.Condition{Type: api.ConditionTypeFleetOverlappingSelectors, Status: api.ConditionStatusTrue}
			listParams := store.ListParams{Limit: 0}
			fleets, err := fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(3))
			for _, fleet := range fleets.Items {
				fleet.Status.Conditions = []api.Condition{}
				cond := api.SetStatusCondition(&fleet.Status.Conditions, condition)
				Expect(cond).To(BeTrue())
				err = fleetStore.UpdateConditions(ctx, orgId, *fleet.Metadata.Name, fleet.Status.Conditions)
				Expect(err).ToNot(HaveOccurred())
			}

			// Now, some were fixed and not overlapping, but fleet2 and fleet3 still overlap on one device
			// Match fleet1
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "fleet", lo.ToPtr("Fleet/fleet2"), nil, &map[string]string{"key1": "val1"})
			// Match fleet2
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "fleet2", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key2": "val2"})
			// Match fleet3
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "fleet3", lo.ToPtr("Fleet/fleet2"), nil, &map[string]string{"key3": "val3"})
			// Match fleet2 and fleet3
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "fleet2+3", lo.ToPtr("Fleet/fleet2"), nil, &map[string]string{"key2": "val2", "key3": "val3"})
			// Match no fleet
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "nofleet", lo.ToPtr("Fleet/fleet4"), nil, &map[string]string{"key4": "val4"})
			// Match no fleet with no labels
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "nolabels", lo.ToPtr("Fleet/fleet4"), nil, &map[string]string{})

			// All devices had multiple owners
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(6))
			for _, device := range devices.Items {
				condition := api.Condition{Type: api.ConditionTypeDeviceMultipleOwners, Status: api.ConditionStatusTrue, Message: "overlap"}
				err = deviceStore.SetServiceConditions(ctx, orgId, *device.Metadata.Name, []api.Condition{condition})
				Expect(err).ToNot(HaveOccurred())
			}

			err = logic.HandleOrgwideUpdate(ctx)
			Expect(err).ToNot(HaveOccurred())

			// fleet should not be overlapping, but fleet2 and fleet3 should be
			fleets, err = fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(3))
			for _, fleet := range fleets.Items {
				condTrue := api.IsStatusConditionTrue(fleet.Status.Conditions, api.ConditionTypeFleetOverlappingSelectors)
				switch *fleet.Metadata.Name {
				case "fleet":
					Expect(condTrue).To(BeFalse())
				default:
					Expect(condTrue).To(BeTrue())
				}
			}

			// Check device ownership and annotations
			devices, err = deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(6))

			for _, device := range devices.Items {
				switch *device.Metadata.Name {
				case "fleet":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
				case "fleet2":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet2"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
				case "fleet3":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet3"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
				case "fleet2+3":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet2"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeTrue())
				case "nofleet":
					Expect(device.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
				case "nolabels":
					Expect(device.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
				}
			}
		})

		It("Device labels updated with no overlap", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet1", &map[string]string{"key1": "val1"}, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet2", &map[string]string{"key2": "val2"}, nil)

			// No ownership change
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "stay-with-fleet1", lo.ToPtr("Fleet/fleet1"), nil, &map[string]string{"key1": "val1"})
			// Ownership change
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "change-to-fleet2", lo.ToPtr("Fleet/fleet1"), nil, &map[string]string{"key2": "val2"})
			// Multiple owners
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "multiple-owners", lo.ToPtr("Fleet/fleet1"), nil, &map[string]string{"key1": "val1", "key2": "val2"})
			// No match
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "no-match", lo.ToPtr("Fleet/fleet2"), nil, &map[string]string{"key3": "val3"})
			// Match no fleet with no labels
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "no-labels", lo.ToPtr("Fleet/fleet3"), nil, &map[string]string{})

			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(5))
			for _, device := range devices.Items {
				resourceRef := tasks_client.ResourceReference{OrgID: orgId, Name: *device.Metadata.Name, Kind: api.DeviceKind}
				logic = tasks.NewFleetSelectorMatchingLogic(callbackManager, log, serviceHandler, resourceRef)
				logic.SetItemsPerPage(2)

				err = logic.CompareFleetsAndSetDeviceOwner(ctx)
				Expect(err).ToNot(HaveOccurred())
				updatedDev, err := deviceStore.Get(ctx, orgId, *device.Metadata.Name)
				Expect(err).ToNot(HaveOccurred())

				switch *device.Metadata.Name {
				case "stay-with-fleet1":
					Expect(*updatedDev.Metadata.Owner).To(Equal("Fleet/fleet1"))
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
				case "change-to-fleet2":
					Expect(*updatedDev.Metadata.Owner).To(Equal("Fleet/fleet2"))
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
				case "multiple-owners":
					Expect(*updatedDev.Metadata.Owner).To(Equal("Fleet/fleet1"))
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeTrue())
				case "no-match":
					Expect(updatedDev.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
				case "no-labels":
					Expect(updatedDev.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
				}
			}
		})

		It("Delete all devices", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet1", &map[string]string{"key1": "val1"}, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet2", &map[string]string{"key2": "val2"}, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet3", &map[string]string{}, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet4", &map[string]string{"key4": "val4"}, nil)

			// fleet2 and fleet4 were overlapping
			condition := api.Condition{Type: api.ConditionTypeFleetOverlappingSelectors, Status: api.ConditionStatusTrue}
			fleetConditions := []api.Condition{}
			cond := api.SetStatusCondition(&fleetConditions, condition)
			Expect(cond).To(BeTrue())
			err := fleetStore.UpdateConditions(ctx, orgId, "fleet2", fleetConditions)
			Expect(err).ToNot(HaveOccurred())
			err = fleetStore.UpdateConditions(ctx, orgId, "fleet4", fleetConditions)
			Expect(err).ToNot(HaveOccurred())

			err = logic.HandleDeleteAllDevices(ctx)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 0}
			fleets, err := fleetStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets.Items)).To(Equal(4))
			for _, fleet := range fleets.Items {
				if fleet.Status.Conditions == nil {
					continue
				}
				condTrue := api.IsStatusConditionTrue(fleet.Status.Conditions, api.ConditionTypeFleetOverlappingSelectors)
				Expect(condTrue).To(BeFalse())
			}
		})

		It("Delete all fleets", func() {
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "single-owner", lo.ToPtr("Fleet/fleet1"), nil, &map[string]string{"key1": "val1"})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "multiple-owners", lo.ToPtr("Fleet/fleet1"), nil, &map[string]string{"key1": "val1", "key2": "val2"})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "no-owner", nil, nil, &map[string]string{"key3": "val3"})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "no-labels", nil, nil, &map[string]string{})

			condition := api.Condition{Type: api.ConditionTypeDeviceMultipleOwners, Status: api.ConditionStatusTrue}
			err := deviceStore.SetServiceConditions(ctx, orgId, "multiple-owners", []api.Condition{condition})
			Expect(err).ToNot(HaveOccurred())

			err = logic.HandleDeleteAllFleets(ctx)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(4))
			for _, device := range devices.Items {
				if device.Metadata.Owner != nil {
					Expect(device.Metadata.Owner).To(BeNil())
				}
				Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
			}
		})
	})
})
