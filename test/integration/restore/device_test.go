package restore_test

import (
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/restore"
	"github.com/flightctl/flightctl/internal/store"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Device restore operations", func() {
	var s *RestoreTestSuite

	BeforeEach(func() {
		s = &RestoreTestSuite{}
		s.Setup()
	})

	AfterEach(func() {
		s.Teardown()
	})

	Context("PrepareDevicesAfterRestore", func() {
		It("sets annotation, clears lastSeen, and sets status", func() {
			devStore := s.Store.Device()
			callback := store.EventCallback(nil)

			testDeviceName := "restore-test-device"
			testDevice := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(testDeviceName),
					Annotations: &map[string]string{
						"existing-annotation": "existing-value",
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline,
						Info:   lo.ToPtr("Device is online"),
					},
					Conditions:   []api.Condition{},
					Applications: []api.DeviceApplicationStatus{},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusUnknown,
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "test-version",
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusUnknown,
						Disk:   api.DeviceResourceStatusUnknown,
						Memory: api.DeviceResourceStatusUnknown,
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUnknown,
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusUnknown,
					},
				},
			}

			createdDevice, created, err := devStore.CreateOrUpdate(s.Ctx, s.OrgID, testDevice, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(createdDevice).ToNot(BeNil())
			Expect(created).To(BeTrue())

			Expect(createdDevice.Status.LastSeen.IsZero()).To(BeFalse())
			Expect(createdDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))

			devicesUpdated, err := s.RestoreStore.PrepareDevicesAfterRestore(s.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(devicesUpdated).To(BeNumerically(">=", int64(1)))

			device, err := devStore.Get(s.Ctx, s.OrgID, testDeviceName)
			Expect(err).ToNot(HaveOccurred())

			Expect(device.Metadata.Annotations).ToNot(BeNil())
			annotations := *device.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(Equal("true"))
			Expect(annotations["existing-annotation"]).To(Equal("existing-value"))

			Expect(device.Status).ToNot(BeNil())
			Expect(device.Status.LastSeen).To(BeNil())
			Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(device.Status.Summary.Info).ToNot(BeNil())
			Expect(*device.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))
			Expect(device.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusUnknown))
			Expect(device.Status.Config.RenderedVersion).To(Equal("test-version"))

			lastSeen, err := devStore.GetLastSeen(s.Ctx, s.OrgID, testDeviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(lastSeen).To(BeNil(), "last_seen column should be cleared after PrepareDevicesAfterRestore")
		})

		It("handles devices with no existing status", func() {
			devStore := s.Store.Device()
			callback := store.EventCallback(nil)

			deviceName := "test-device-no-status"
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: nil,
			}

			_, created, err := devStore.CreateOrUpdate(s.Ctx, s.OrgID, &device, nil, true, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			devicesUpdated, err := s.RestoreStore.PrepareDevicesAfterRestore(s.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(devicesUpdated).To(BeNumerically(">=", int64(1)))

			updatedDevice, err := devStore.Get(s.Ctx, s.OrgID, deviceName)
			Expect(err).ToNot(HaveOccurred())

			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			annotations := *updatedDevice.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(Equal("true"))

			Expect(updatedDevice.Status).ToNot(BeNil())
			Expect(updatedDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(updatedDevice.Status.Summary.Info).ToNot(BeNil())
			Expect(*updatedDevice.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))
			Expect(updatedDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusUnknown))
			Expect(updatedDevice.Status.LastSeen).To(BeNil())
		})

		It("excludes decommissioned and decommissioning devices", func() {
			devStore := s.Store.Device()
			callback := store.EventCallback(nil)

			decommissioningDeviceName := "decommissioning-device"
			decommissioningDevice := api.Device{
				Metadata: api.ObjectMeta{
					Name:        lo.ToPtr(decommissioningDeviceName),
					Annotations: &map[string]string{"existing-annotation": "existing-value"},
				},
				Spec: &api.DeviceSpec{
					Os:              &api.DeviceOsSpec{Image: "test-image"},
					Decommissioning: &api.DeviceDecommission{Target: api.DeviceDecommissionTargetTypeUnenroll},
				},
				Status: &api.DeviceStatus{
					LastSeen:            lo.ToPtr(time.Now()),
					Summary:             api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline, Info: lo.ToPtr("Device is online")},
					Lifecycle:           api.DeviceLifecycleStatus{Status: api.DeviceLifecycleStatusDecommissioning},
					Conditions:          []api.Condition{},
					Applications:        []api.DeviceApplicationStatus{},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: api.ApplicationsSummaryStatusUnknown},
					Config:              api.DeviceConfigStatus{RenderedVersion: "test-version"},
					Integrity:           api.DeviceIntegrityStatus{Status: api.DeviceIntegrityStatusUnknown},
					Resources:           api.DeviceResourceStatus{Cpu: api.DeviceResourceStatusUnknown, Disk: api.DeviceResourceStatusUnknown, Memory: api.DeviceResourceStatusUnknown},
					Updated:             api.DeviceUpdatedStatus{Status: api.DeviceUpdatedStatusUnknown},
				},
			}

			decommissionedDeviceName := "decommissioned-device"
			decommissionedDevice := api.Device{
				Metadata: api.ObjectMeta{
					Name:        lo.ToPtr(decommissionedDeviceName),
					Annotations: &map[string]string{"existing-annotation": "existing-value"},
				},
				Spec: &api.DeviceSpec{
					Os:              &api.DeviceOsSpec{Image: "test-image"},
					Decommissioning: &api.DeviceDecommission{Target: api.DeviceDecommissionTargetTypeUnenroll},
				},
				Status: &api.DeviceStatus{
					LastSeen:            lo.ToPtr(time.Now()),
					Summary:             api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline, Info: lo.ToPtr("Device is online")},
					Lifecycle:           api.DeviceLifecycleStatus{Status: api.DeviceLifecycleStatusDecommissioned},
					Conditions:          []api.Condition{},
					Applications:        []api.DeviceApplicationStatus{},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: api.ApplicationsSummaryStatusUnknown},
					Config:              api.DeviceConfigStatus{RenderedVersion: "test-version"},
					Integrity:           api.DeviceIntegrityStatus{Status: api.DeviceIntegrityStatusUnknown},
					Resources:           api.DeviceResourceStatus{Cpu: api.DeviceResourceStatusUnknown, Disk: api.DeviceResourceStatusUnknown, Memory: api.DeviceResourceStatusUnknown},
					Updated:             api.DeviceUpdatedStatus{Status: api.DeviceUpdatedStatusUnknown},
				},
			}

			normalDeviceName := "normal-device"
			normalDevice := api.Device{
				Metadata: api.ObjectMeta{
					Name:        lo.ToPtr(normalDeviceName),
					Annotations: &map[string]string{"existing-annotation": "existing-value"},
				},
				Spec: &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
				Status: &api.DeviceStatus{
					LastSeen:            lo.ToPtr(time.Now()),
					Summary:             api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline, Info: lo.ToPtr("Device is online")},
					Lifecycle:           api.DeviceLifecycleStatus{Status: api.DeviceLifecycleStatusEnrolled},
					Conditions:          []api.Condition{},
					Applications:        []api.DeviceApplicationStatus{},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: api.ApplicationsSummaryStatusUnknown},
					Config:              api.DeviceConfigStatus{RenderedVersion: "test-version"},
					Integrity:           api.DeviceIntegrityStatus{Status: api.DeviceIntegrityStatusUnknown},
					Resources:           api.DeviceResourceStatus{Cpu: api.DeviceResourceStatusUnknown, Disk: api.DeviceResourceStatusUnknown, Memory: api.DeviceResourceStatusUnknown},
					Updated:             api.DeviceUpdatedStatus{Status: api.DeviceUpdatedStatusUnknown},
				},
			}

			_, created, err := devStore.CreateOrUpdate(s.Ctx, s.OrgID, &decommissioningDevice, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			s.SetDeviceLastSeen(decommissioningDeviceName, *decommissioningDevice.Status.LastSeen)

			_, created, err = devStore.CreateOrUpdate(s.Ctx, s.OrgID, &decommissionedDevice, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			s.SetDeviceLastSeen(decommissionedDeviceName, *decommissionedDevice.Status.LastSeen)

			_, created, err = devStore.CreateOrUpdate(s.Ctx, s.OrgID, &normalDevice, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			s.SetDeviceLastSeen(normalDeviceName, *normalDevice.Status.LastSeen)

			devicesUpdated, err := s.RestoreStore.PrepareDevicesAfterRestore(s.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(devicesUpdated).To(BeNumerically(">=", int64(1)))

			// Decommissioning device should NOT be updated
			decommissioningDeviceAfter, err := devStore.Get(s.Ctx, s.OrgID, decommissioningDeviceName)
			Expect(err).ToNot(HaveOccurred())
			annotations := *decommissioningDeviceAfter.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(BeEmpty())
			Expect(annotations["existing-annotation"]).To(Equal("existing-value"))
			decommissioningLastSeen, err := devStore.GetLastSeen(s.Ctx, s.OrgID, decommissioningDeviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(decommissioningLastSeen).ToNot(BeNil())
			Expect(decommissioningDeviceAfter.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))

			// Decommissioned device should NOT be updated
			decommissionedDeviceAfter, err := devStore.Get(s.Ctx, s.OrgID, decommissionedDeviceName)
			Expect(err).ToNot(HaveOccurred())
			annotations = *decommissionedDeviceAfter.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(BeEmpty())
			Expect(annotations["existing-annotation"]).To(Equal("existing-value"))
			decommissionedLastSeen, err := devStore.GetLastSeen(s.Ctx, s.OrgID, decommissionedDeviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(decommissionedLastSeen).ToNot(BeNil())
			Expect(decommissionedDeviceAfter.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))

			// Normal device SHOULD be updated
			normalDeviceAfter, err := devStore.Get(s.Ctx, s.OrgID, normalDeviceName)
			Expect(err).ToNot(HaveOccurred())
			annotations = *normalDeviceAfter.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(Equal("true"))
			Expect(annotations["existing-annotation"]).To(Equal("existing-value"))
			normalLastSeen, err := devStore.GetLastSeen(s.Ctx, s.OrgID, normalDeviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(normalLastSeen).To(BeNil())
			Expect(normalDeviceAfter.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(normalDeviceAfter.Status.Summary.Info).ToNot(BeNil())
			Expect(*normalDeviceAfter.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))
		})

		It("properly clears last_seen column", func() {
			devStore := s.Store.Device()
			callback := store.EventCallback(nil)

			deviceName := "last-seen-column-test"
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName)},
				Spec:     &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
				Status: &api.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					Summary:  api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline},
				},
			}

			_, created, err := devStore.CreateOrUpdate(s.Ctx, s.OrgID, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			s.SetDeviceLastSeen(deviceName, *device.Status.LastSeen)

			lastSeenBefore, err := devStore.GetLastSeen(s.Ctx, s.OrgID, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(lastSeenBefore).ToNot(BeNil(), "last_seen column should have a value initially")

			devicesUpdated, err := s.RestoreStore.PrepareDevicesAfterRestore(s.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(devicesUpdated).To(BeNumerically(">=", int64(1)))

			lastSeenAfter, err := devStore.GetLastSeen(s.Ctx, s.OrgID, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(lastSeenAfter).To(BeNil(), "last_seen column should be cleared after PrepareDevicesAfterRestore")
		})
	})

	Context("PrepareDevices event creation", func() {
		It("should emit SystemRestored event when restore preparation completes", func() {
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr("restore-test-device")},
				Spec:     &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
			}

			createdDevice, status := s.Handler.CreateDevice(s.Ctx, s.OrgID, *device)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(createdDevice).ToNot(BeNil())

			initialEvents, err := s.Store.Event().List(s.Ctx, s.OrgID, store.ListParams{Limit: 1000})
			Expect(err).ToNot(HaveOccurred())
			initialEventCount := len(initialEvents.Items)

			_, err = restore.PrepareDevices(s.Ctx, s.RestoreStore, nil, s.Log)
			Expect(err).ToNot(HaveOccurred())

			finalEvents, err := s.Store.Event().List(s.Ctx, s.OrgID, store.ListParams{Limit: 1000})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(finalEvents.Items)).To(BeNumerically(">", initialEventCount))

			var systemRestoredEvent *api.Event
			for _, event := range finalEvents.Items {
				if event.Reason == api.EventReasonSystemRestored {
					systemRestoredEvent = &event
					break
				}
			}

			Expect(systemRestoredEvent).ToNot(BeNil(), "SystemRestored event should be created")
			Expect(systemRestoredEvent.Type).To(Equal(api.Normal))
			Expect(systemRestoredEvent.InvolvedObject.Kind).To(Equal(api.SystemKind))
			Expect(systemRestoredEvent.InvolvedObject.Name).To(Equal(api.SystemComponentDB))
			Expect(systemRestoredEvent.Message).To(ContainSubstring("System restored successfully"))
			Expect(systemRestoredEvent.Message).To(ContainSubstring("devices for post-restoration preparation"))
		})

		It("should be able to filter events by System kind", func() {
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr("filter-test-device")},
				Spec:     &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
			}

			createdDevice, status := s.Handler.CreateDevice(s.Ctx, s.OrgID, *device)
			Expect(status.Code).To(Equal(int32(201)))
			Expect(createdDevice).ToNot(BeNil())

			_, err := restore.PrepareDevices(s.Ctx, s.RestoreStore, nil, s.Log)
			Expect(err).ToNot(HaveOccurred())

			params := api.ListEventsParams{
				FieldSelector: lo.ToPtr("involvedObject.kind=System"),
				Limit:         lo.ToPtr(int32(100)),
			}

			eventList, status := s.Handler.ListEvents(s.Ctx, s.OrgID, params)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(eventList).ToNot(BeNil())
			Expect(len(eventList.Items)).To(BeNumerically(">=", 1))

			for _, event := range eventList.Items {
				Expect(event.InvolvedObject.Kind).To(Equal(api.SystemKind))
			}

			var systemRestoredEvent *api.Event
			for _, event := range eventList.Items {
				if event.Reason == api.EventReasonSystemRestored {
					systemRestoredEvent = &event
					break
				}
			}
			Expect(systemRestoredEvent).ToNot(BeNil(), "SystemRestored event should be found when filtering by System kind")

			deviceParams := api.ListEventsParams{
				FieldSelector: lo.ToPtr("involvedObject.kind=Device"),
				Limit:         lo.ToPtr(int32(100)),
			}

			deviceEventList, status := s.Handler.ListEvents(s.Ctx, s.OrgID, deviceParams)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(deviceEventList).ToNot(BeNil())
			Expect(len(deviceEventList.Items)).To(BeNumerically(">=", 1))

			for _, event := range deviceEventList.Items {
				Expect(event.InvolvedObject.Kind).To(Equal(api.DeviceKind))
				Expect(event.Reason).ToNot(Equal(api.EventReasonSystemRestored))
			}
		})
	})
})
