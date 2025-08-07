package tasks_test

import (
	"context"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
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
		eventStore      store.Event
		storeInst       store.Store
		serviceHandler  service.Service
		cfg             *config.Config
		dbName          string
		callbackManager tasks_client.CallbackManager
		logic           tasks.FleetSelectorMatchingLogic
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-worker")
		ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-worker")
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		deviceStore = storeInst.Device()
		fleetStore = storeInst.Fleet()
		eventStore = storeInst.Event()
		ctrl := gomock.NewController(GinkgoT())
		publisher := queues.NewMockPublisher(ctrl)
		publisher.EXPECT().Publish(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		callbackManager = tasks_client.NewCallbackManager(publisher, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, callbackManager, kvStore, nil, log, "", "", []string{})
		logic = tasks.NewFleetSelectorMatchingLogic(callbackManager, log, serviceHandler, tasks_client.ResourceReference{OrgID: orgId, Name: "fleet", Kind: api.FleetKind})
		logic.SetItemsPerPage(2)
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
	})

	// Helper function to get events for a specific involved object
	getEventsForResource := func(kind, name string) []api.Event {
		listParams := store.ListParams{
			Limit:       100,
			SortColumns: []store.SortColumn{store.SortByCreatedAt, store.SortByName},
			SortOrder:   lo.ToPtr(store.SortDesc),
		}
		eventList, err := eventStore.List(ctx, orgId, listParams)
		Expect(err).ToNot(HaveOccurred())

		var matchingEvents []api.Event
		for _, event := range eventList.Items {
			if event.InvolvedObject.Kind == kind && event.InvolvedObject.Name == name {
				matchingEvents = append(matchingEvents, event)
			}
		}
		return matchingEvents
	}

	// Helper function to validate ResourceUpdated events for owner changes
	validateResourceUpdatedEvent := func(deviceName string, events []api.Event, expectedPreviousOwner, expectedNewOwner *string) {
		found := false
		for _, event := range events {
			if event.Reason == api.EventReasonResourceUpdated {
				Expect(event.Type).To(Equal(api.Normal))
				Expect(event.Details).ToNot(BeNil())

				details, err := event.Details.AsResourceUpdatedDetails()
				Expect(err).ToNot(HaveOccurred())

				// Check if this is an owner update
				hasOwnerUpdate := false
				for _, field := range details.UpdatedFields {
					if field == api.Owner {
						hasOwnerUpdate = true
						break
					}
				}
				if !hasOwnerUpdate {
					continue // This is not an owner update event
				}

				if expectedPreviousOwner != nil {
					Expect(details.PreviousOwner).To(Equal(expectedPreviousOwner))
				} else {
					Expect(details.PreviousOwner).To(BeNil())
				}

				if expectedNewOwner != nil {
					Expect(details.NewOwner).To(Equal(expectedNewOwner))
				} else {
					Expect(details.NewOwner).To(BeNil())
				}

				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), fmt.Sprintf("ResourceUpdated event for owner change not found for device %s", deviceName))
	}

	// Helper function to validate DeviceMultipleOwnersDetected events
	validateDeviceMultipleOwnersDetectedEvent := func(deviceName string, events []api.Event, expectedMatchingFleets []string) {
		found := false
		for _, event := range events {
			if event.Reason == api.EventReasonDeviceMultipleOwnersDetected {
				Expect(event.Type).To(Equal(api.Warning))
				Expect(event.Details).ToNot(BeNil())

				details, err := event.Details.AsDeviceMultipleOwnersDetectedDetails()
				Expect(err).ToNot(HaveOccurred())

				Expect(details.MatchingFleets).To(ConsistOf(expectedMatchingFleets))
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), fmt.Sprintf("DeviceMultipleOwnersDetected event not found for device %s", deviceName))
	}

	// Helper function to validate DeviceMultipleOwnersResolved events
	validateDeviceMultipleOwnersResolvedEvent := func(deviceName string, events []api.Event, expectedResolutionType api.DeviceMultipleOwnersResolvedDetailsResolutionType, expectedAssignedOwner *string) {
		found := false
		for _, event := range events {
			if event.Reason == api.EventReasonDeviceMultipleOwnersResolved {
				Expect(event.Type).To(Equal(api.Normal))
				Expect(event.Details).ToNot(BeNil())

				details, err := event.Details.AsDeviceMultipleOwnersResolvedDetails()
				Expect(err).ToNot(HaveOccurred())

				Expect(details.ResolutionType).To(Equal(expectedResolutionType))
				if expectedAssignedOwner != nil {
					Expect(details.AssignedOwner).To(Equal(expectedAssignedOwner))
				} else {
					Expect(details.AssignedOwner).To(BeNil())
				}
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), fmt.Sprintf("DeviceMultipleOwnersResolved event not found for device %s", deviceName))
	}

	// Helper function to validate InternalTaskFailed events
	_ = func(events []api.Event, expectedTaskType string) {
		found := false
		for _, event := range events {
			if event.Reason == api.EventReasonInternalTaskFailed {
				Expect(event.Type).To(Equal(api.Warning))
				Expect(event.Details).ToNot(BeNil())

				details, err := event.Details.AsInternalTaskFailedDetails()
				Expect(err).ToNot(HaveOccurred())

				Expect(details.TaskType).To(Equal(expectedTaskType))
				Expect(details.ErrorMessage).ToNot(BeEmpty())
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "InternalTaskFailed event not found")
	}

	Context("FleetSelector", func() {
		It("Fleet selector with event validation", func() {
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
			// This device is owned by "otherfleet", should now match both fleets (multiple owners condition)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "otherfleet-to-multiowner", lo.ToPtr("Fleet/otherfleet"), nil, &map[string]string{"key": "value", "otherkey": "othervalue"})
			// This device is unowned and should now match both fleets. Ownership should stay unassigned
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "no-owner-multiowner", nil, nil, &map[string]string{"key": "value", "otherkey": "othervalue"})

			err := logic.FleetSelectorUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(6))

			for _, device := range devices.Items {
				switch *device.Metadata.Name {
				case "no-owner":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet"))
					events := getEventsForResource(api.DeviceKind, "no-owner")
					validateResourceUpdatedEvent("no-owner", events, nil, lo.ToPtr("Fleet/fleet"))
				case "no-owner-multiowner":
					Expect(device.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeTrue())
					events := getEventsForResource(api.DeviceKind, "no-owner-multiowner")
					validateDeviceMultipleOwnersDetectedEvent("no-owner-multiowner", events, []string{"fleet", "otherfleet"})
				case "otherfleet-to-fleet":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet"))
					events := getEventsForResource(api.DeviceKind, "otherfleet-to-fleet")
					validateResourceUpdatedEvent("otherfleet-to-fleet", events, lo.ToPtr("Fleet/otherfleet"), lo.ToPtr("Fleet/fleet"))
				case "fleet-to-none":
					Expect(device.Metadata.Owner).To(BeNil())
					events := getEventsForResource(api.DeviceKind, "fleet-to-none")
					validateResourceUpdatedEvent("fleet-to-none", events, lo.ToPtr("Fleet/fleet"), nil)
				case "stay-in-fleet":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet"))
					// Should not have ownership change events since ownership didn't change
				case "otherfleet-to-multiowner":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/otherfleet"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeTrue())
					events := getEventsForResource(api.DeviceKind, "otherfleet-to-multiowner")
					validateDeviceMultipleOwnersDetectedEvent("otherfleet-to-multiowner", events, []string{"fleet", "otherfleet"})
				}
			}

		})

		It("Fleet deleted should remove device owners and emit events", func() {
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key": "value"})
			err := logic.FleetSelectorUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			device, err := deviceStore.Get(ctx, orgId, "device")
			Expect(err).ToNot(HaveOccurred())
			Expect(device.Metadata.Owner).To(BeNil())

			// Validate ownership change event
			events := getEventsForResource(api.DeviceKind, "device")
			validateResourceUpdatedEvent("device", events, lo.ToPtr("Fleet/fleet"), nil)

		})

		It("Nil fleet selector should match no devices and emit events", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", nil, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "nillabels", lo.ToPtr("Fleet/fleet"), nil, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "emptylabels", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device1", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key1": "value1"})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device2", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key2": "value2"})
			err := logic.FleetSelectorUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(4))

			for _, device := range devices.Items {
				Expect(device.Metadata.Owner).To(BeNil())
				// Validate ownership change events
				events := getEventsForResource(api.DeviceKind, *device.Metadata.Name)
				validateResourceUpdatedEvent(*device.Metadata.Name, events, lo.ToPtr("Fleet/fleet"), nil)
			}
		})

		It("Empty fleet selector should match no devices and emit events", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", &map[string]string{}, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "nillabels", lo.ToPtr("Fleet/fleet"), nil, nil)
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "emptylabels", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device1", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key1": "value1"})
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "device2", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key2": "value2"})
			err := logic.FleetSelectorUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(4))

			for _, device := range devices.Items {
				Expect(device.Metadata.Owner).To(BeNil())
				// Validate ownership change events
				events := getEventsForResource(api.DeviceKind, *device.Metadata.Name)
				validateResourceUpdatedEvent(*device.Metadata.Name, events, lo.ToPtr("Fleet/fleet"), nil)
			}
		})

		It("Fleet selector updated with multiple owners resolves conflicts and emits events", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", &map[string]string{"key1": "val1"}, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet2", &map[string]string{"key2": "val2"}, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet3", &map[string]string{"key3": "val3"}, nil)

			// Match fleet1
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "fleet", lo.ToPtr("Fleet/fleet2"), nil, &map[string]string{"key1": "val1"})
			// Match fleet2
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "fleet2", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key2": "val2"})
			// Match fleet3
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "fleet3", lo.ToPtr("Fleet/fleet2"), nil, &map[string]string{"key3": "val3"})
			// Match fleet2 and fleet3 - should have multiple owners condition
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "fleet2+3", lo.ToPtr("Fleet/fleet2"), nil, &map[string]string{"key2": "val2", "key3": "val3"})
			// Match no fleet
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "nofleet", lo.ToPtr("Fleet/fleet4"), nil, &map[string]string{"key4": "val4"})
			// Match no fleet with no labels
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "nolabels", lo.ToPtr("Fleet/fleet4"), nil, &map[string]string{})
			// Match no fleet with no previous owner
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "nolabels-noowner", nil, nil, &map[string]string{})

			// Set all devices to have multiple owners condition initially
			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(7))
			for _, device := range devices.Items {
				condition := api.Condition{Type: api.ConditionTypeDeviceMultipleOwners, Status: api.ConditionStatusTrue, Message: "fleet2,fleet3"}
				err = deviceStore.SetServiceConditions(ctx, orgId, *device.Metadata.Name, []api.Condition{condition}, nil)
				Expect(err).ToNot(HaveOccurred())
			}

			err = logic.FleetSelectorUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Check device ownership and conditions
			devices, err = deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(7))

			for _, device := range devices.Items {
				events := getEventsForResource(api.DeviceKind, *device.Metadata.Name)

				switch *device.Metadata.Name {
				case "fleet":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateResourceUpdatedEvent("fleet", events, lo.ToPtr("Fleet/fleet2"), lo.ToPtr("Fleet/fleet"))
					validateDeviceMultipleOwnersResolvedEvent("fleet", events, api.SingleMatch, lo.ToPtr("Fleet/fleet"))
				case "fleet2":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet2"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateResourceUpdatedEvent("fleet2", events, lo.ToPtr("Fleet/fleet"), lo.ToPtr("Fleet/fleet2"))
					validateDeviceMultipleOwnersResolvedEvent("fleet2", events, api.SingleMatch, lo.ToPtr("Fleet/fleet2"))
				case "fleet3":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet3"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateResourceUpdatedEvent("fleet3", events, lo.ToPtr("Fleet/fleet2"), lo.ToPtr("Fleet/fleet3"))
					validateDeviceMultipleOwnersResolvedEvent("fleet3", events, api.SingleMatch, lo.ToPtr("Fleet/fleet3"))
				case "fleet2+3":
					Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet2"))
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeTrue())
					// Validate that DeviceMultipleOwnersDetectedEvent is NOT emitted since device already had multiple owners
					for _, event := range events {
						if event.InvolvedObject.Name == "fleet2+3" && event.Reason == api.EventReasonDeviceMultipleOwnersDetected {
							Fail("DeviceMultipleOwnersDetectedEvent should not be emitted for device that already had multiple owners")
						}
					}
				case "nofleet":
					Expect(device.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateResourceUpdatedEvent("nofleet", events, lo.ToPtr("Fleet/fleet4"), nil)
					validateDeviceMultipleOwnersResolvedEvent("nofleet", events, api.NoMatch, nil)
				case "nolabels":
					Expect(device.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateResourceUpdatedEvent("nolabels", events, lo.ToPtr("Fleet/fleet4"), nil)
					validateDeviceMultipleOwnersResolvedEvent("nolabels", events, api.NoMatch, nil)
				case "nolabels-noowner":
					Expect(device.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateDeviceMultipleOwnersResolvedEvent("nolabels-noowner", events, api.NoMatch, nil)
				}
			}

			// Validate fleet processing completed event

		})

		It("Device labels updated with comprehensive event validation", func() {
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
			// Match no fleet with no labels, but ensure the multiple owners condition is cleared
			noLabelsNoOwnerDevice := "nolabels-noowner"
			testutil.CreateTestDevice(ctx, deviceStore, orgId, noLabelsNoOwnerDevice, nil, nil, &map[string]string{})
			condition := api.Condition{Type: api.ConditionTypeDeviceMultipleOwners, Status: api.ConditionStatusTrue, Message: "fleet1,fleet2"}
			err := deviceStore.SetServiceConditions(ctx, orgId, noLabelsNoOwnerDevice, []api.Condition{condition}, nil)
			Expect(err).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(6))

			for _, device := range devices.Items {
				resourceRef := tasks_client.ResourceReference{OrgID: orgId, Name: *device.Metadata.Name, Kind: api.DeviceKind}
				deviceLogic := tasks.NewFleetSelectorMatchingLogic(callbackManager, log, serviceHandler, resourceRef)
				deviceLogic.SetItemsPerPage(2)

				err = deviceLogic.DeviceLabelsUpdated(ctx)
				Expect(err).ToNot(HaveOccurred())

				updatedDev, err := deviceStore.Get(ctx, orgId, *device.Metadata.Name)
				Expect(err).ToNot(HaveOccurred())

				events := getEventsForResource(api.DeviceKind, *device.Metadata.Name)

				switch *device.Metadata.Name {
				case "stay-with-fleet1":
					Expect(*updatedDev.Metadata.Owner).To(Equal("Fleet/fleet1"))
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					// Should not have ownership change events since ownership didn't change
				case "change-to-fleet2":
					Expect(*updatedDev.Metadata.Owner).To(Equal("Fleet/fleet2"))
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateResourceUpdatedEvent(*device.Metadata.Name, events, lo.ToPtr("Fleet/fleet1"), lo.ToPtr("Fleet/fleet2"))
				case "multiple-owners":
					Expect(*updatedDev.Metadata.Owner).To(Equal("Fleet/fleet1"))
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeTrue())
					validateDeviceMultipleOwnersDetectedEvent(*device.Metadata.Name, events, []string{"fleet1", "fleet2"})
				case "no-match":
					Expect(updatedDev.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateResourceUpdatedEvent(*device.Metadata.Name, events, lo.ToPtr("Fleet/fleet2"), nil)
				case "no-labels":
					Expect(updatedDev.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateResourceUpdatedEvent(*device.Metadata.Name, events, lo.ToPtr("Fleet/fleet3"), nil)
				case noLabelsNoOwnerDevice:
					Expect(updatedDev.Metadata.Owner).To(BeNil())
					Expect(api.IsStatusConditionTrue(updatedDev.Status.Conditions, api.ConditionTypeDeviceMultipleOwners)).To(BeFalse())
					validateDeviceMultipleOwnersResolvedEvent(noLabelsNoOwnerDevice, events, api.NoMatch, nil)
				}
			}
		})

		It("Should skip updating decommissioning devices", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", &map[string]string{"key": "value"}, nil)

			// Create device with decommissioning flag
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "decommissioning-device", lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key": "value"})
			device, err := deviceStore.Get(ctx, orgId, "decommissioning-device")
			Expect(err).ToNot(HaveOccurred())
			device.Spec.Decommissioning = &api.DeviceDecommission{}
			callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
			_, _, err = deviceStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Change fleet selector so device no longer matches
			fleet, err := fleetStore.Get(ctx, orgId, "fleet")
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Selector.MatchLabels = &map[string]string{"different": "value"}
			fleetCallback := store.FleetStoreCallback(func(context.Context, uuid.UUID, *api.Fleet, *api.Fleet) {})
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet, nil, false, fleetCallback, nil)
			Expect(err).ToNot(HaveOccurred())

			err = logic.FleetSelectorUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Device should still be owned by fleet (not updated due to decommissioning)
			updatedDevice, err := deviceStore.Get(ctx, orgId, "decommissioning-device")
			Expect(err).ToNot(HaveOccurred())
			Expect(*updatedDevice.Metadata.Owner).To(Equal("Fleet/fleet"))

			// Should not have ownership change events
			events := getEventsForResource(api.DeviceKind, "decommissioning-device")
			ownershipChangeFound := false
			for _, event := range events {
				if event.Reason == api.EventReasonResourceUpdated {
					// Check if it's an owner update
					if event.Details != nil {
						if details, err := event.Details.AsResourceUpdatedDetails(); err == nil {
							for _, field := range details.UpdatedFields {
								if field == api.Owner {
									ownershipChangeFound = true
									break
								}
							}
						}
					}
				}
			}
			Expect(ownershipChangeFound).To(BeFalse())
		})

		It("Should handle pagination correctly", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", &map[string]string{"key": "value"}, nil)

			// Create 5 devices to test pagination with itemsPerPage=2
			for i := 0; i < 5; i++ {
				testutil.CreateTestDevice(ctx, deviceStore, orgId, fmt.Sprintf("device-%d", i), lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key": "value"})
			}

			err := logic.FleetSelectorUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			// All devices should still be owned by fleet
			listParams := store.ListParams{Limit: 0}
			devices, err := deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(5))

			for _, device := range devices.Items {
				Expect(*device.Metadata.Owner).To(Equal("Fleet/fleet"))
			}

		})

		It("Should handle device with no labels in DeviceLabelsUpdated", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet1", &map[string]string{"key1": "val1"}, nil)

			// Create device with owner but no labels
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "no-labels-device", lo.ToPtr("Fleet/fleet1"), nil, nil)

			resourceRef := tasks_client.ResourceReference{OrgID: orgId, Name: "no-labels-device", Kind: api.DeviceKind}
			deviceLogic := tasks.NewFleetSelectorMatchingLogic(callbackManager, log, serviceHandler, resourceRef)
			deviceLogic.SetItemsPerPage(2)

			err := deviceLogic.DeviceLabelsUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Device should have no owner
			updatedDevice, err := deviceStore.Get(ctx, orgId, "no-labels-device")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Owner).To(BeNil())

			// Validate ownership change event
			events := getEventsForResource(api.DeviceKind, "no-labels-device")
			validateResourceUpdatedEvent("no-labels-device", events, lo.ToPtr("Fleet/fleet1"), nil)
		})

		It("Should handle device with empty labels in DeviceLabelsUpdated", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet1", &map[string]string{"key1": "val1"}, nil)

			// Create device with owner but empty labels
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "empty-labels-device", lo.ToPtr("Fleet/fleet1"), nil, &map[string]string{})

			resourceRef := tasks_client.ResourceReference{OrgID: orgId, Name: "empty-labels-device", Kind: api.DeviceKind}
			deviceLogic := tasks.NewFleetSelectorMatchingLogic(callbackManager, log, serviceHandler, resourceRef)
			deviceLogic.SetItemsPerPage(2)

			err := deviceLogic.DeviceLabelsUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Device should have no owner
			updatedDevice, err := deviceStore.Get(ctx, orgId, "empty-labels-device")
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Owner).To(BeNil())

			// Validate ownership change event
			events := getEventsForResource(api.DeviceKind, "empty-labels-device")
			validateResourceUpdatedEvent("empty-labels-device", events, lo.ToPtr("Fleet/fleet1"), nil)
		})

		It("Should handle device with non-fleet owner in DeviceLabelsUpdated", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet1", &map[string]string{"key1": "val1"}, nil)

			// Create device with non-fleet owner
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "non-fleet-owner-device", lo.ToPtr("User/user1"), nil, &map[string]string{"key1": "val1"})

			resourceRef := tasks_client.ResourceReference{OrgID: orgId, Name: "non-fleet-owner-device", Kind: api.DeviceKind}
			deviceLogic := tasks.NewFleetSelectorMatchingLogic(callbackManager, log, serviceHandler, resourceRef)
			deviceLogic.SetItemsPerPage(2)

			err := deviceLogic.DeviceLabelsUpdated(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Device should still have non-fleet owner (no change)
			updatedDevice, err := deviceStore.Get(ctx, orgId, "non-fleet-owner-device")
			Expect(err).ToNot(HaveOccurred())
			Expect(*updatedDevice.Metadata.Owner).To(Equal("User/user1"))

			// Should not have ownership change events
			events := getEventsForResource(api.DeviceKind, "non-fleet-owner-device")
			ownershipChangeFound := false
			for _, event := range events {
				if event.Reason == api.EventReasonResourceUpdated {
					// Check if it's an owner update
					if event.Details != nil {
						if details, err := event.Details.AsResourceUpdatedDetails(); err == nil {
							for _, field := range details.UpdatedFields {
								if field == api.Owner {
									ownershipChangeFound = true
									break
								}
							}
						}
					}
				}
			}
			Expect(ownershipChangeFound).To(BeFalse())
		})

		It("Should handle context cancellation gracefully", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "fleet", &map[string]string{"key": "value"}, nil)

			// Create multiple devices
			for i := 0; i < 3; i++ {
				testutil.CreateTestDevice(ctx, deviceStore, orgId, fmt.Sprintf("device-%d", i), lo.ToPtr("Fleet/fleet"), nil, &map[string]string{"key": "value"})
			}

			// Test with cancelled context
			cancelledCtx, cancel := context.WithCancel(ctx)
			cancel() // Cancel immediately

			err := logic.FleetSelectorUpdated(cancelledCtx)
			Expect(err).To(HaveOccurred())
			Expect(strings.Contains(err.Error(), "context canceled")).To(BeTrue())
		})
	})
})
