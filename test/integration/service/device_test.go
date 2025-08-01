package service_test

import (
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Device Application Status Events Integration Tests", func() {
	var (
		suite *ServiceTestSuite
		orgId uuid.UUID
	)

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		orgId = store.NullOrgId // Use the same orgId as the service
	})

	AfterEach(func() {
		suite.Teardown()
	})

	// Helper function to get events for a specific device
	getEventsForDevice := func(deviceName string) []api.Event {
		listParams := store.ListParams{
			Limit:       100,
			SortColumns: []store.SortColumn{store.SortByCreatedAt, store.SortByName},
			SortOrder:   lo.ToPtr(store.SortDesc),
		}
		eventList, err := suite.Store.Event().List(suite.Ctx, orgId, listParams)
		Expect(err).ToNot(HaveOccurred())

		var matchingEvents []api.Event
		for _, event := range eventList.Items {
			if event.InvolvedObject.Kind == api.DeviceKind && event.InvolvedObject.Name == deviceName {
				matchingEvents = append(matchingEvents, event)
			}
		}
		return matchingEvents
	}

	// Helper function to check for specific event reason
	findEventByReason := func(events []api.Event, reason api.EventReason) *api.Event {
		for _, event := range events {
			if event.Reason == reason {
				return &event
			}
		}
		return nil
	}

	Context("New device application status transitions", func() {
		It("should generate DeviceApplicationError event when new device reports error applications", func() {
			deviceName := "new-device-with-error-apps"

			// Step 1: Create a new device (simulating enrollment)
			app := api.ApplicationProviderSpec{
				Name:    lo.ToPtr("test-app"),
				AppType: lo.ToPtr(api.AppTypeCompose),
			}

			// Create proper ImageApplicationProviderSpec
			imageProvider := api.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:v1",
			}
			err := app.FromImageApplicationProviderSpec(imageProvider)
			Expect(err).ToNot(HaveOccurred())

			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Spec: &api.DeviceSpec{
					Applications: &[]api.ApplicationProviderSpec{app},
				},
			}

			// Create the device through the service (this simulates device enrollment)
			_, status := suite.Handler.CreateDevice(suite.Ctx, device)
			if status.Code != 201 {
				GinkgoWriter.Printf("CreateDevice failed with status: %+v\n", status)
			}
			Expect(status.Code).To(Equal(int32(201)))

			// Verify no application events exist yet
			initialEvents := getEventsForDevice(deviceName)
			appErrorEvent := findEventByReason(initialEvents, api.EventReasonDeviceApplicationError)
			Expect(appErrorEvent).To(BeNil())

			// Step 2: Agent reports back with applications in Error state (simulating invalid application)
			updatedDevice := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Status: &api.DeviceStatus{
					LastSeen: time.Now(),
					Applications: []api.DeviceApplicationStatus{
						{
							Name:     "test-app",
							Status:   api.ApplicationStatusError,
							Ready:    "0/1",
							Restarts: 0,
						},
					},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusError,
						Info:   lo.ToPtr("test-app is in status Error"),
					},
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline,
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUpToDate,
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusUnknown,
						Memory: api.DeviceResourceStatusUnknown,
						Disk:   api.DeviceResourceStatusUnknown,
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Os: api.DeviceOsStatus{
						Image:       "test-image",
						ImageDigest: "sha256:1234",
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "1",
					},
					SystemInfo: api.DeviceSystemInfo{
						OperatingSystem: "linux",
						Architecture:    "amd64",
						BootID:          "boot-123",
						AgentVersion:    "v1.0.0",
					},
				},
			}

			// Update device status through the service (this simulates agent reporting back)
			resultDevice, status := suite.Handler.ReplaceDeviceStatus(suite.Ctx, deviceName, updatedDevice)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(resultDevice).ToNot(BeNil())
			Expect(resultDevice.Status.ApplicationsSummary.Status).To(Equal(api.ApplicationsSummaryStatusError))

			// Step 3: Verify that DeviceApplicationError event was generated
			finalEvents := getEventsForDevice(deviceName)
			GinkgoWriter.Printf("All events for device %s: %d events\n", deviceName, len(finalEvents))
			for i, event := range finalEvents {
				GinkgoWriter.Printf("Event %d: Type=%s, Reason=%s, Message=%s\n", i, event.Type, event.Reason, event.Message)
			}

			appErrorEvent = findEventByReason(finalEvents, api.EventReasonDeviceApplicationError)
			Expect(appErrorEvent).ToNot(BeNil(), "DeviceApplicationError event should be generated when transitioning from Unknown to Error")
			Expect(appErrorEvent.Type).To(Equal(api.Warning))
			Expect(appErrorEvent.Message).To(ContainSubstring("test-app is in status Error"))
		})

		It("should generate DeviceApplicationHealthy event when new device reports healthy applications", func() {
			deviceName := "new-device-with-healthy-apps"

			// Step 1: Create a new device
			app := api.ApplicationProviderSpec{
				Name:    lo.ToPtr("test-app"),
				AppType: lo.ToPtr(api.AppTypeCompose),
			}

			// Create proper ImageApplicationProviderSpec
			imageProvider := api.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:v1",
			}
			err := app.FromImageApplicationProviderSpec(imageProvider)
			Expect(err).ToNot(HaveOccurred())

			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Spec: &api.DeviceSpec{
					Applications: &[]api.ApplicationProviderSpec{app},
				},
			}

			// Create the device through the service
			_, status := suite.Handler.CreateDevice(suite.Ctx, device)
			Expect(status.Code).To(Equal(int32(201)))

			// Step 2: Agent reports back with applications in Healthy state
			updatedDevice := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Status: &api.DeviceStatus{
					LastSeen: time.Now(),
					Applications: []api.DeviceApplicationStatus{
						{
							Name:     "test-app",
							Status:   api.ApplicationStatusRunning,
							Ready:    "1/1",
							Restarts: 0,
						},
					},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusHealthy,
						Info:   lo.ToPtr("All application workloads are healthy."),
					},
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline,
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUpToDate,
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusUnknown,
						Memory: api.DeviceResourceStatusUnknown,
						Disk:   api.DeviceResourceStatusUnknown,
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Os: api.DeviceOsStatus{
						Image:       "test-image",
						ImageDigest: "sha256:1234",
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "1",
					},
					SystemInfo: api.DeviceSystemInfo{
						OperatingSystem: "linux",
						Architecture:    "amd64",
						BootID:          "boot-123",
						AgentVersion:    "v1.0.0",
					},
				},
			}

			// Update device status through the service
			resultDevice, status := suite.Handler.ReplaceDeviceStatus(suite.Ctx, deviceName, updatedDevice)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(resultDevice).ToNot(BeNil())
			Expect(resultDevice.Status.ApplicationsSummary.Status).To(Equal(api.ApplicationsSummaryStatusHealthy))

			// Step 3: Verify that DeviceApplicationHealthy event was generated
			finalEvents := getEventsForDevice(deviceName)
			appHealthyEvent := findEventByReason(finalEvents, api.EventReasonDeviceApplicationHealthy)
			Expect(appHealthyEvent).ToNot(BeNil(), "DeviceApplicationHealthy event should be generated when transitioning from Unknown to Healthy")
			Expect(appHealthyEvent.Type).To(Equal(api.Normal))
			Expect(appHealthyEvent.Message).To(ContainSubstring("All application workloads are healthy"))
		})
	})

	Context("Device resource monitor status transitions", func() {
		It("should generate resource monitor events for problematic transitions but not for healthy startup", func() {
			deviceName := "new-device-with-resource-issues"

			// Step 1: Create a device (without status)
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Spec: &api.DeviceSpec{},
			}

			// Create the device
			_, status := suite.Handler.CreateDevice(suite.Ctx, device)
			Expect(status.Code).To(Equal(int32(201)))

			// Step 2: Set the device status with critical resource status
			deviceWithCriticalResources := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Status: &api.DeviceStatus{
					LastSeen: time.Now(),
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusCritical,
						Memory: api.DeviceResourceStatusWarning,
						Disk:   api.DeviceResourceStatusHealthy, // This should NOT generate an event (Unknown -> Healthy)
					},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusHealthy,
					},
					Conditions: []api.Condition{},
				},
			}

			// Update the device status
			_, status = suite.Handler.ReplaceDeviceStatus(suite.Ctx, deviceName, deviceWithCriticalResources)
			Expect(status.Code).To(Equal(int32(200)))

			// Verify events were generated for CPU and Memory issues but NOT for Disk
			// We should have: ResourceCreated + DeviceCPUCritical + DeviceMemoryWarning + DeviceApplicationHealthy + DeviceContentUpToDate
			events := getEventsForDevice(deviceName)
			Expect(len(events)).To(Equal(4))

			// Check that we have the right events
			eventReasons := make([]string, len(events))
			for i, event := range events {
				eventReasons[i] = string(event.Reason)
			}
			Expect(eventReasons).To(ContainElements(
				"ResourceCreated",
				"DeviceCPUCritical",
				"DeviceMemoryWarning",
				"DeviceApplicationHealthy",
			))
			// Should NOT contain DeviceDiskNormal since that's Unknown -> Healthy
			Expect(eventReasons).ToNot(ContainElement("DeviceDiskNormal"))

			// Step 3: Update the device to have all resources healthy
			deviceWithHealthyResources := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Status: &api.DeviceStatus{
					LastSeen: time.Now(),
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusHealthy,
						Memory: api.DeviceResourceStatusHealthy,
						Disk:   api.DeviceResourceStatusHealthy,
					},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusHealthy,
					},
					Conditions: []api.Condition{},
				},
			}

			// Update the device
			_, status = suite.Handler.ReplaceDeviceStatus(suite.Ctx, deviceName, deviceWithHealthyResources)
			Expect(status.Code).To(Equal(int32(200)))

			// Verify events were generated for CPU and Memory recovery
			// We should now have: ResourceCreated + DeviceCPUCritical + DeviceMemoryWarning + DeviceApplicationHealthy + ResourceUpdated + DeviceCPUNormal + DeviceMemoryNormal
			events = getEventsForDevice(deviceName)
			Expect(len(events)).To(Equal(7))

			// Check that we have the recovery events
			eventReasons = make([]string, len(events))
			for i, event := range events {
				eventReasons[i] = string(event.Reason)
			}
			Expect(eventReasons).To(ContainElements(
				"ResourceCreated",
				"DeviceCPUCritical",
				"DeviceMemoryWarning",
				"DeviceApplicationHealthy",
				"DeviceConnected",
				"DeviceCPUNormal",
				"DeviceMemoryNormal",
			))
			// Still should NOT contain DeviceDiskNormal since disk was already healthy
			Expect(eventReasons).ToNot(ContainElement("DeviceDiskNormal"))
		})

		It("should generate resource monitor warning events when device reports warning resource status", func() {
			deviceName := "device-with-warning-resources"

			// Step 1: Create a new device
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Spec: &api.DeviceSpec{},
			}

			// Create the device through the service
			_, status := suite.Handler.CreateDevice(suite.Ctx, device)
			Expect(status.Code).To(Equal(int32(201)))

			// Step 2: Agent reports back with warning resource status
			updatedDevice := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Status: &api.DeviceStatus{
					LastSeen:     time.Now(),
					Applications: []api.DeviceApplicationStatus{},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusHealthy,
						Info:   lo.ToPtr("No application workloads are defined."),
					},
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusDegraded, // This should be set by the service for warnings
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUpToDate,
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusWarning,
						Memory: api.DeviceResourceStatusHealthy,
						Disk:   api.DeviceResourceStatusHealthy,
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Os: api.DeviceOsStatus{
						Image:       "test-image",
						ImageDigest: "sha256:1234",
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "1",
					},
					SystemInfo: api.DeviceSystemInfo{
						OperatingSystem: "linux",
						Architecture:    "amd64",
						BootID:          "boot-123",
						AgentVersion:    "v1.0.0",
					},
				},
			}

			// Update device status through the service
			resultDevice, status := suite.Handler.ReplaceDeviceStatus(suite.Ctx, deviceName, updatedDevice)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(resultDevice).ToNot(BeNil())

			// Verify that the service sets the summary status to Degraded due to warning resource status
			Expect(resultDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusDegraded))

			// Step 3: Verify that CPU warning event was generated
			finalEvents := getEventsForDevice(deviceName)
			cpuWarningEvent := findEventByReason(finalEvents, api.EventReasonDeviceCPUWarning)
			Expect(cpuWarningEvent).ToNot(BeNil(), "DeviceCPUWarning event should be generated when transitioning from Unknown to Warning")
			Expect(cpuWarningEvent.Type).To(Equal(api.Warning))
			Expect(cpuWarningEvent.Message).To(ContainSubstring("CPU utilization has reached a warning level"))
		})

		It("should generate DeviceConnected event when device summary status transitions from unknown to online", func() {
			deviceName := "device-connection-test"

			// Step 1: Create a device (without status - this will have unknown summary status)
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Spec: &api.DeviceSpec{},
			}

			// Create the device
			_, status := suite.Handler.CreateDevice(suite.Ctx, device)
			Expect(status.Code).To(Equal(int32(201)))

			// Step 2: First set device status to unknown explicitly
			deviceWithUnknownStatus := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Status: &api.DeviceStatus{
					LastSeen: time.Now().Add(-10 * time.Minute), // More than 5 minutes ago to trigger disconnected state
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusUnknown,
						Info:   lo.ToPtr("The device is disconnected (last seen more than 5m0s)."),
					},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusUnknown,
						Info:   lo.ToPtr("The device is disconnected (last seen more than 5m0s)."),
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUnknown,
						Info:   lo.ToPtr("The device is disconnected (last seen more than 5m0s)."),
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusUnknown,
						Memory: api.DeviceResourceStatusUnknown,
						Disk:   api.DeviceResourceStatusUnknown,
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Os: api.DeviceOsStatus{
						Image:       "test-image",
						ImageDigest: "sha256:1234",
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "1",
					},
					SystemInfo: api.DeviceSystemInfo{
						OperatingSystem: "linux",
						Architecture:    "amd64",
						BootID:          "boot-123",
						AgentVersion:    "v1.0.0",
					},
				},
			}

			// Set the device status to unknown
			resultDevice, err := suite.Handler.UpdateDevice(suite.Ctx, deviceName, deviceWithUnknownStatus, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(resultDevice).ToNot(BeNil())
			Expect(resultDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusUnknown))

			// Verify no connection events exist yet
			initialEvents := getEventsForDevice(deviceName)
			connectedEvent := findEventByReason(initialEvents, api.EventReasonDeviceConnected)
			Expect(connectedEvent).To(BeNil())

			// Step 3: Update device status to online (simulating device coming online)
			deviceWithOnlineStatus := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Status: &api.DeviceStatus{
					LastSeen: time.Now(),
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline,
						Info:   lo.ToPtr("All system resources are healthy."),
					},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusHealthy,
						Info:   lo.ToPtr("No application workloads are defined."),
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUpToDate,
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusHealthy,
						Memory: api.DeviceResourceStatusHealthy,
						Disk:   api.DeviceResourceStatusHealthy,
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Os: api.DeviceOsStatus{
						Image:       "test-image",
						ImageDigest: "sha256:1234",
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "1",
					},
					SystemInfo: api.DeviceSystemInfo{
						OperatingSystem: "linux",
						Architecture:    "amd64",
						BootID:          "boot-123",
						AgentVersion:    "v1.0.0",
					},
				},
			}

			// Update the device status to online
			resultDevice, status = suite.Handler.ReplaceDeviceStatus(suite.Ctx, deviceName, deviceWithOnlineStatus)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(resultDevice).ToNot(BeNil())
			Expect(resultDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))

			// Step 4: Verify that DeviceConnected event was generated
			finalEvents := getEventsForDevice(deviceName)
			GinkgoWriter.Printf("Events for device %s: %d events\n", deviceName, len(finalEvents))
			for i, event := range finalEvents {
				GinkgoWriter.Printf("Event %d: Type=%s, Reason=%s, Message=%s\n", i, event.Type, event.Reason, event.Message)
			}

			connectedEvent = findEventByReason(finalEvents, api.EventReasonDeviceConnected)
			Expect(connectedEvent).ToNot(BeNil(), "DeviceConnected event should be generated when transitioning from Unknown to Online")
			Expect(connectedEvent.Type).To(Equal(api.Normal))
			Expect(connectedEvent.Message).To(ContainSubstring("All system resources are healthy"))
		})
	})
})
