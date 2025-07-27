package service

import (
	"context"
	"crypto"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	authcommon "github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	devicecommon "github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func serviceHandler() *ServiceHandler {
	return &ServiceHandler{
		store:           &TestStore{},
		callbackManager: dummyCallbackManager(),
		log:             logrus.New(),
	}
}

func prepareDevice() *api.Device {
	deviceStatus := api.NewDeviceStatus()
	deviceStatus.LastSeen = time.Now()
	deviceStatus.SystemInfo = api.DeviceSystemInfo{
		AgentVersion:    "1",
		Architecture:    "2",
		BootID:          "3",
		OperatingSystem: "4",
	}
	return &api.Device{
		ApiVersion: "v1",
		Kind:       "Device",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOsSpec{Image: "img"},
		},
		Status: &deviceStatus,
	}
}

func prepareFleet(owner string) api.Fleet {
	return api.Fleet{
		ApiVersion: "v1",
		Kind:       "Fleet",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr(owner),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: api.FleetSpec{
			Selector: &api.LabelSelector{
				MatchLabels: &map[string]string{"devKey": "devValue"},
			},
			Template: struct {
				Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
				Spec     api.DeviceSpec  "json:\"spec\""
			}{
				Spec: api.DeviceSpec{
					Os: &api.DeviceOsSpec{
						Image: "img",
					},
				},
			},
		},
		Status: &api.FleetStatus{
			Conditions: []api.Condition{
				{
					Type:   "Approved",
					Status: "True",
				},
			},
		},
	}
}

// =============================== DEVICE ====================================
func TestEventDeviceReplaced(t *testing.T) {
	require := require.New(t)

	const newOwner1 = "new.owner1"
	const newOwner2 = "new.owner2"

	serviceHandler := serviceHandler()
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	device := prepareDevice()

	// Create device
	expectedEvents := []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonResourceCreated, Details: "Fleet was created successfully."},
		{Reason: api.EventReasonDeviceContentOutOfDate, Details: "Device has not yet been scheduled for update to the fleet's latest spec."},
		{Reason: api.EventReasonResourceUpdated, Details: "Device was updated successfully."},
	}...)
	fleet := prepareFleet(newOwner1)
	_, retStatus = serviceHandler.CreateFleet(ctx, fleet)
	require.Equal(statusCreatedCode, retStatus.Code)
	device.Metadata.Owner = util.SetResourceOwner(api.DeviceKind, newOwner1)
	device, retStatus = serviceHandler.ReplaceDevice(ctx, *device.Metadata.Name, *device, nil)
	require.Equal(statusSuccessCode, retStatus.Code)
	require.Equal(*device.Metadata.Owner, util.ResourceOwner(device.Kind, newOwner1))
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonResourceCreated, Details: "Fleet was created successfully."},
	}...)
	fleet = prepareFleet(newOwner2)
	_, retStatus = serviceHandler.CreateFleet(ctx, fleet)
	require.Equal(statusCreatedCode, retStatus.Code)
	device.Metadata.Owner = util.SetResourceOwner(api.DeviceKind, newOwner2)
	device, retStatus = serviceHandler.ReplaceDevice(ctx, *device.Metadata.Name, *device, nil)
	require.Equal(statusSuccessCode, retStatus.Code)
	require.Equal(*device.Metadata.Owner, util.ResourceOwner(device.Kind, newOwner2))
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDeviceReplaceDeviceStatus(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	ctx := context.Background()
	device := prepareDevice()

	// Create device
	expectedEvents := []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonDeviceApplicationHealthy, Details: "No application workloads are defined."},
		{Reason: api.EventReasonResourceUpdated, Details: "Device was updated successfully."},
	}...)
	device, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *device)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	_, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *device)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDeviceReplaceDeviceStatus1(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	ctx := context.Background()
	device := &api.Device{
		ApiVersion: "flightctl.io/v1alpha1",
		Kind:       "Device",
		Metadata: api.ObjectMeta{
			Generation:      lo.ToPtr(int64(1)),
			Name:            lo.ToPtr("vgq5kiugbcrg6u6t1r01eogcdjmmn1njcgk2v8bp3kf5b4hqkc20"),
			ResourceVersion: lo.ToPtr("3"),
		},
		Status: &api.DeviceStatus{
			Applications: []api.DeviceApplicationStatus{},
			ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
				Status: "Unknown",
			},
			Conditions: []api.Condition{
				{
					Status: "Unknown",
					Type:   "Updating",
				},
				{
					LastTransitionTime: time.Now(),
					Reason:             "Valid",
					Status:             "True",
					Type:               "SpecValid",
				},
			},
			Integrity: api.DeviceIntegrityStatus{
				Status: "Unknown",
			},
			Lifecycle: api.DeviceLifecycleStatus{
				Status: "Enrolled",
			},
			Resources: api.DeviceResourceStatus{
				Cpu:    "Unknown",
				Disk:   "Unknown",
				Memory: "Unknown",
			},
			Summary: api.DeviceSummaryStatus{
				Status: "Unknown",
			},
			Updated: api.DeviceUpdatedStatus{
				Status: "Unknown",
			},
		},
	}

	// Create device
	expectedEvents := []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonDeviceApplicationHealthy, Details: "No application workloads are defined."},
		{Reason: api.EventReasonResourceUpdated, Details: "Device was updated successfully."},
	}...)
	newDevice := &api.Device{
		ApiVersion: "",
		Kind:       "",
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("vgq5kiugbcrg6u6t1r01eogcdjmmn1njcgk2v8bp3kf5b4hqkc20"),
		},
		Status: &api.DeviceStatus{
			Applications: []api.DeviceApplicationStatus{},
			ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
				Status: "Unknown",
			},
			Conditions: []api.Condition{
				{
					Status: "Unknown",
					Type:   "Updating",
				},
			},
			Config: api.DeviceConfigStatus{
				RenderedVersion: "0",
			},
			Integrity: api.DeviceIntegrityStatus{
				Status: "Unknown",
			},
			LastSeen: time.Now(),
			Lifecycle: api.DeviceLifecycleStatus{
				Status: "Unknown",
			},
			Os: api.DeviceOsStatus{
				Image:       "192.168.1.243:5000/flightctl-device:base",
				ImageDigest: "sha256:aa72b6c2164f53a36a004f00471bcde801f67eb4772c659aa75cd9e8b0ac8ba4",
			},
			Resources: api.DeviceResourceStatus{
				Cpu:    "Healthy",
				Disk:   "Healthy",
				Memory: "Healthy",
			},
			Summary: api.DeviceSummaryStatus{
				Status: "Online",
			},
			SystemInfo: api.DeviceSystemInfo{
				AgentVersion:    "0.9.0-main-16-g60275ce8",
				Architecture:    "amd64",
				BootID:          "90663118-7ba1-48c2-87c0-b91cd5c8b526",
				OperatingSystem: "linux",
				AdditionalProperties: map[string]string{
					"productSerial":       "distroVersion: 9",
					"netMacDefault":       "52:54:00:77:ed:4c",
					"productUuid":         "15fcaff1-1901-488a-a79e-62a27bbd2c2d",
					"kernel":              "5.14.0-584.el9.x86_64",
					"productName":         "Standard PC (Q35 + ICH9, 2009)",
					"hostname":            "localhost.localdomain",
					"netIpDefault":        "192.168.122.19/24",
					"netInterfaceDefault": "enp1s0",
					"distroName":          "CentOS Stream",
				},
			},
			Updated: api.DeviceUpdatedStatus{
				Status: "Unknown",
			},
		},
	}
	device, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *newDevice)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	_, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *device)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	_, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *device)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDevicePatchDeviceStatus(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	ctx := context.Background()
	device := prepareDevice()

	// Create device
	expectedEvents := []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	infoMap, err := util.StructToMap(api.DeviceSystemInfo{
		AgentVersion:    "a",
		Architecture:    "b",
		BootID:          "c",
		OperatingSystem: "d",
	})
	require.NoError(err)

	var value interface{} = infoMap
	patchRequest := api.PatchRequest{
		{Op: "replace", Path: "/status/systemInfo", Value: &value},
	}
	device, retStatus = serviceHandler.PatchDeviceStatus(ctx, *device.Metadata.Name, patchRequest)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	value, err = util.StructToMap(api.DeviceSystemInfo{
		AgentVersion:    "a",
		Architecture:    "b",
		BootID:          "2",
		OperatingSystem: "3",
	})
	require.NoError(err)
	_, retStatus = serviceHandler.PatchDeviceStatus(ctx, *device.Metadata.Name, patchRequest)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func compareEvents(expectedEvents []devicecommon.ResourceUpdate, events []api.Event, require *require.Assertions) {
	require.Len(events, len(expectedEvents))
	for i, event := range events {
		expected := expectedEvents[i]
		require.Equal(event.Reason, expected.Reason)
		require.Equal(event.Message, expected.Details)
	}
}

func TestEventDeviceCreatedAndIsAlive(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	device := prepareDevice()

	// Create device
	expectedEvents := []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	// Device I-am-alive
	expectedEvents = append(expectedEvents, []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonDeviceApplicationHealthy, Details: "No application workloads are defined."},
		{Reason: api.EventReasonResourceUpdated, Details: "Device was updated successfully."},
	}...)
	device.Status.LastSeen = time.Now()
	device, err = serviceHandler.UpdateDevice(ctx, *device.Metadata.Name, *device, nil)
	require.NoError(err)
	events, _ = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	compareEvents(expectedEvents, events.Items, require)

	// Device I-am-alive
	// No new expected events
	device.Status.LastSeen = time.Now()
	_, err = serviceHandler.UpdateDevice(ctx, *device.Metadata.Name, *device, nil)
	require.NoError(err)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDeviceUpdated(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	device := prepareDevice()

	// Create device
	expectedEvents := []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonDeviceApplicationHealthy, Details: "No application workloads are defined."},
		{Reason: api.EventReasonResourceUpdated, Details: "Device was updated successfully."},
	}...)
	device.Status.Resources = api.DeviceResourceStatus{
		Cpu:    api.DeviceResourceStatusHealthy,
		Memory: api.DeviceResourceStatusHealthy,
		Disk:   api.DeviceResourceStatusHealthy,
	}
	device.Status.LastSeen = time.Now()
	device, err = serviceHandler.UpdateDevice(ctx, *device.Metadata.Name, *device, nil)
	require.NoError(err)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	_, err = serviceHandler.UpdateDevice(ctx, *device.Metadata.Name, *device, nil)
	require.NoError(err)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

// =============================== ENROLLMENT REQUEST ========================
func TestEventEnrollmentRequestApproved(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	testDirPath := t.TempDir()
	cfg := ca.NewDefault(testDirPath)
	ca, _, err := fcrypto.EnsureCA(cfg)
	require.NoError(err)
	serviceHandler.ca = ca
	ctx := context.Background()

	// create ER
	name := uuid.New().String()
	approval := api.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
	}
	status := api.EnrollmentRequestStatus{}
	deviceStatus := api.NewDeviceStatus()
	deviceReadWriter := fileio.NewReadWriter(fileio.WithTestRootDir(t.TempDir()))
	_, privateKey, _, err := fccrypto.EnsureKey(deviceReadWriter.PathFor("TestCSR"))
	require.NoError(err)
	csr, err := fccrypto.MakeCSR(privateKey.(crypto.Signer), name)
	require.NoError(err)
	er := api.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.EnrollmentRequestSpec{
			Csr:          string(csr),
			DeviceStatus: &deviceStatus,
			Labels:       &map[string]string{"labelKey": "labelValue"}},
		Status: &status,
	}

	eventCallback := func(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, updateDesc *api.ResourceUpdatedDetails, err error) {
		if err != nil {
			status := StoreErrorToApiStatus(err, created, api.EnrollmentRequestKind, &name)
			serviceHandler.CreateEvent(ctx, GetResourceCreatedOrUpdatedFailureEvent(ctx, created, api.EnrollmentRequestKind, name, status, nil))
		} else {
			serviceHandler.CreateEvent(ctx, GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, api.EnrollmentRequestKind, name, updateDesc, serviceHandler.log))
		}
	}
	_, err = serviceHandler.store.EnrollmentRequest().Create(ctx, store.NullOrgId, &er, eventCallback)
	require.NoError(err)
	identity := authcommon.Identity{
		Username: "bar",
	}
	ctx = context.WithValue(ctx, authcommon.IdentityCtxKey, &identity)
	_, stat := serviceHandler.ApproveEnrollmentRequest(ctx, name, approval)
	expectedEvents := []devicecommon.ResourceUpdate{
		{Reason: api.EventReasonResourceCreated, Details: "EnrollmentRequest was created successfully."},
		{Reason: api.EventReasonResourceCreated, Details: "Device was created successfully."},
		{Reason: api.EventReasonResourceUpdated, Details: "EnrollmentRequest was updated successfully."},
	}
	require.Equal(statusSuccessCode, stat.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), store.NullOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestGetDeviceSpecInvalidEvent(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	deviceName := "test-device"
	message := "validation failed"

	event := GetDeviceSpecInvalidEvent(ctx, deviceName, message)

	require.NotNil(event)
	require.Equal(api.DeviceKind, event.InvolvedObject.Kind)
	require.Equal(deviceName, event.InvolvedObject.Name)
	require.Equal(api.EventReasonDeviceSpecInvalid, event.Reason)
	require.Equal(api.Warning, event.Type)
	require.Equal("Device specification is invalid: validation failed.", event.Message)
	require.NotNil(event.Metadata.Name)
}

func TestGetDeviceSpecValidEvent(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	deviceName := "test-device"

	event := GetDeviceSpecValidEvent(ctx, deviceName)

	require.NotNil(event)
	require.Equal(api.DeviceKind, event.InvolvedObject.Kind)
	require.Equal(deviceName, event.InvolvedObject.Name)
	require.Equal(api.EventReasonDeviceSpecValid, event.Reason)
	require.Equal(api.Normal, event.Type)
	require.Equal("Device specification is valid.", event.Message)
	require.NotNil(event.Metadata.Name)
}

func TestGetDeviceMultipleOwnersDetectedEvent(t *testing.T) {
	require := require.New(t)
	logger := logrus.New()

	ctx := context.Background()
	deviceName := "test-device"
	matchingFleets := []string{"fleet1", "fleet2", "fleet3"}

	event := GetDeviceMultipleOwnersDetectedEvent(ctx, deviceName, matchingFleets, logger)

	require.NotNil(event)
	require.Equal(string(api.DeviceKind), event.InvolvedObject.Kind)
	require.Equal(deviceName, event.InvolvedObject.Name)
	require.Equal(api.EventReasonDeviceMultipleOwnersDetected, event.Reason)
	require.Equal(api.Warning, event.Type)
	require.Equal("Device matches multiple fleets: fleet1, fleet2, fleet3.", event.Message)
	require.NotNil(event.Metadata.Name)
	require.NotNil(event.Details)

	// Verify the event details
	detailsStruct, err := event.Details.AsDeviceMultipleOwnersDetectedDetails()
	require.NoError(err)
	require.Equal(matchingFleets, detailsStruct.MatchingFleets)
}

func TestGetDeviceMultipleOwnersResolvedEvent(t *testing.T) {
	require := require.New(t)
	logger := logrus.New()

	ctx := context.Background()
	deviceName := "test-device"

	testCases := []struct {
		name           string
		resolutionType api.DeviceMultipleOwnersResolvedDetailsResolutionType
		assignedOwner  *string
		expectedMsg    string
	}{
		{
			name:           "SingleMatch",
			resolutionType: api.SingleMatch,
			assignedOwner:  lo.ToPtr("fleet1"),
			expectedMsg:    "Device multiple owners conflict was resolved: single fleet match, assigned to fleet 'fleet1'.",
		},
		{
			name:           "NoMatch",
			resolutionType: api.NoMatch,
			assignedOwner:  nil,
			expectedMsg:    "Device multiple owners conflict was resolved: no fleet matches, owner was removed.",
		},
		{
			name:           "FleetDeleted",
			resolutionType: api.FleetDeleted,
			assignedOwner:  nil,
			expectedMsg:    "Device multiple owners conflict was resolved: fleet was deleted.",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			previousFleets := []string{"fleet1", "fleet2"}

			event := GetDeviceMultipleOwnersResolvedEvent(ctx, deviceName, tc.resolutionType, tc.assignedOwner, previousFleets, logger)

			require.NotNil(event)
			require.Equal(string(api.DeviceKind), event.InvolvedObject.Kind)
			require.Equal(deviceName, event.InvolvedObject.Name)
			require.Equal(api.EventReasonDeviceMultipleOwnersResolved, event.Reason)
			require.Equal(api.Normal, event.Type)
			require.Equal(tc.expectedMsg, event.Message)
			require.NotNil(event.Metadata.Name)
			require.NotNil(event.Details)

			// Verify the event details
			detailsStruct, err := event.Details.AsDeviceMultipleOwnersResolvedDetails()
			require.NoError(err)
			require.Equal(tc.resolutionType, detailsStruct.ResolutionType)
			require.Equal(tc.assignedOwner, detailsStruct.AssignedOwner)
			require.Equal(&previousFleets, detailsStruct.PreviousMatchingFleets)
		})
	}
}

func TestGetInternalTaskFailedEvent(t *testing.T) {
	require := require.New(t)
	logger := logrus.New()

	ctx := context.Background()
	resourceKind := api.ResourceKind(api.DeviceKind)
	resourceName := "test-device"
	taskType := "sync"
	errorMessage := "connection timeout"
	retryCount := lo.ToPtr(3)
	taskParameters := map[string]string{"param1": "value1", "param2": "value2"}

	event := GetInternalTaskFailedEvent(ctx, resourceKind, resourceName, taskType, errorMessage, retryCount, taskParameters, logger)

	require.NotNil(event)
	require.Equal(resourceKind, api.ResourceKind(event.InvolvedObject.Kind))
	require.Equal(resourceName, event.InvolvedObject.Name)
	require.Equal(api.EventReasonInternalTaskFailed, event.Reason)
	require.Equal(api.Warning, event.Type)
	require.Equal("Device internal task failed: sync - connection timeout.", event.Message)
	require.NotNil(event.Metadata.Name)
	require.NotNil(event.Details)

	// Verify the event details
	detailsStruct, err := event.Details.AsInternalTaskFailedDetails()
	require.NoError(err)
	require.Equal(taskType, detailsStruct.TaskType)
	require.Equal(errorMessage, detailsStruct.ErrorMessage)
	require.Equal(retryCount, detailsStruct.RetryCount)
	require.Equal(&taskParameters, detailsStruct.TaskParameters)
}

func TestGetResourceCreatedOrUpdatedEvent(t *testing.T) {
	require := require.New(t)
	logger := logrus.New()

	ctx := context.Background()
	resourceKind := api.ResourceKind(api.DeviceKind)
	resourceName := "test-device"
	updateDetails := &api.ResourceUpdatedDetails{
		NewOwner:      lo.ToPtr("fleet2"),
		PreviousOwner: lo.ToPtr("fleet1"),
	}

	t.Run("Created", func(t *testing.T) {
		event := GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, resourceKind, resourceName, nil, logger)

		require.NotNil(event)
		require.Equal(resourceKind, api.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(api.EventReasonResourceCreated, event.Reason)
		require.Equal(api.Normal, event.Type)
		require.Equal("Device was created successfully.", event.Message)
		require.NotNil(event.Metadata.Name)
		require.Nil(event.Details)
	})

	t.Run("Updated", func(t *testing.T) {
		event := GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, resourceKind, resourceName, updateDetails, logger)

		require.NotNil(event)
		require.Equal(resourceKind, api.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(api.EventReasonResourceUpdated, event.Reason)
		require.Equal(api.Normal, event.Type)
		require.Equal("Device was updated successfully.", event.Message)
		require.NotNil(event.Metadata.Name)
		require.NotNil(event.Details)

		// Verify the event details
		detailsStruct, err := event.Details.AsResourceUpdatedDetails()
		require.NoError(err)
		require.Equal(updateDetails.NewOwner, detailsStruct.NewOwner)
		require.Equal(updateDetails.PreviousOwner, detailsStruct.PreviousOwner)
	})

	t.Run("CreatedFailed", func(t *testing.T) {
		event := GetResourceCreatedOrUpdatedFailureEvent(ctx, true, resourceKind, resourceName, api.StatusInternalServerError("creation failed"), nil)

		require.NotNil(event)
		require.Equal(resourceKind, api.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(api.EventReasonResourceCreationFailed, event.Reason)
		require.Equal(api.Warning, event.Type)
		require.Equal("Device creation failed: creation failed.", event.Message)
		require.NotNil(event.Metadata.Name)
		require.Nil(event.Details)
	})

	t.Run("UpdatedFailed", func(t *testing.T) {
		event := GetResourceCreatedOrUpdatedFailureEvent(ctx, false, resourceKind, resourceName, api.StatusInternalServerError("update failed"), &api.ResourceUpdatedDetails{})

		require.NotNil(event)
		require.Equal(resourceKind, api.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(api.EventReasonResourceUpdateFailed, event.Reason)
		require.Equal(api.Warning, event.Type)
		require.Equal("Device update failed: update failed.", event.Message)
		require.NotNil(event.Metadata.Name)
		require.NotNil(event.Details)
	})
}

func TestGetResourceDeletedEvent(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	resourceKind := api.ResourceKind(api.DeviceKind)
	resourceName := "test-device"

	t.Run("Success", func(t *testing.T) {
		event := GetResourceDeletedSuccessEvent(ctx, resourceKind, resourceName)

		require.NotNil(event)
		require.Equal(resourceKind, api.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(api.EventReasonResourceDeleted, event.Reason)
		require.Equal(api.Normal, event.Type)
		require.Equal("Device was deleted successfully.", event.Message)
		require.NotNil(event.Metadata.Name)
	})

	t.Run("Failed", func(t *testing.T) {
		event := GetResourceDeletedFailureEvent(ctx, resourceKind, resourceName, api.StatusInternalServerError("deletion failed"))

		require.NotNil(event)
		require.Equal(resourceKind, api.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(api.EventReasonResourceDeletionFailed, event.Reason)
		require.Equal(api.Warning, event.Type)
		require.Equal("Device deletion failed: deletion failed.", event.Message)
		require.NotNil(event.Metadata.Name)
	})
}
