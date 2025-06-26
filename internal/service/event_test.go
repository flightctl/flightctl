package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

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

func TestEventDeviceReplaced(t *testing.T) {
	require := require.New(t)

	const newOwner1 = "new.owner1"
	const newOwner2 = "new.owner2"

	serviceHandler := &ServiceHandler{
		store:           &TestStore{},
		callbackManager: dummyCallbackManager(),
		log:             logrus.New(),
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	device := prepareDevice()

	// Create device
	expectedEvents := []api.EventReason{
		api.EventReasonDeviceContentUpToDate,
		api.EventReasonResourceCreated,
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{
		api.EventReasonResourceCreated,
		api.EventReasonDeviceContentOutOfDate,
		api.EventReasonResourceUpdated,
	}...)
	fleet := prepareFleet(newOwner1)
	_, retStatus = serviceHandler.CreateFleet(ctx, fleet)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	device.Metadata.Owner = util.SetResourceOwner(api.DeviceKind, newOwner1)
	device, retStatus = serviceHandler.ReplaceDevice(ctx, *device.Metadata.Name, *device, nil)
	require.Equal(int32(http.StatusOK), retStatus.Code)
	require.Equal(*device.Metadata.Owner, util.ResourceOwner(device.Kind, newOwner1))
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{
		api.EventReasonResourceUpdated,
	}...)
	fleet = prepareFleet(newOwner2)
	_, retStatus = serviceHandler.CreateFleet(ctx, fleet)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	device.Metadata.Owner = util.SetResourceOwner(api.DeviceKind, newOwner2)
	device, retStatus = serviceHandler.ReplaceDevice(ctx, *device.Metadata.Name, *device, nil)
	require.Equal(int32(http.StatusOK), retStatus.Code)
	require.Equal(*device.Metadata.Owner, util.ResourceOwner(device.Kind, newOwner2))
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDeviceReplaceDeviceStatus(t *testing.T) {
	require := require.New(t)

	serviceHandler := &ServiceHandler{
		store:           &TestStore{},
		callbackManager: dummyCallbackManager(),
		log:             logrus.New(),
	}
	ctx := context.Background()
	device := prepareDevice()

	// Create device
	expectedEvents := []api.EventReason{
		api.EventReasonDeviceContentUpToDate,
		api.EventReasonResourceCreated,
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{
		api.EventReasonDeviceApplicationHealthy,
	}...)
	device, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *device)
	require.Equal(int32(http.StatusOK), retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{}...)
	_, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *device)
	require.Equal(int32(http.StatusOK), retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDeviceReplaceDeviceStatus1(t *testing.T) {
	require := require.New(t)

	serviceHandler := &ServiceHandler{
		store:           &TestStore{},
		callbackManager: dummyCallbackManager(),
		log:             logrus.New(),
	}
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
	expectedEvents := []api.EventReason{
		api.EventReasonDeviceContentUpToDate,
		api.EventReasonResourceCreated,
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{
		api.EventReasonDeviceApplicationHealthy,
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
	require.Equal(int32(http.StatusOK), retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{}...)
	_, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *device)
	require.Equal(int32(http.StatusOK), retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{}...)
	_, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, *device)
	require.Equal(int32(http.StatusOK), retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDevicePatchDeviceStatus(t *testing.T) {
	require := require.New(t)

	serviceHandler := &ServiceHandler{
		store:           &TestStore{},
		callbackManager: dummyCallbackManager(),
		log:             logrus.New(),
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	device := prepareDevice()

	// Create device
	expectedEvents := []api.EventReason{
		api.EventReasonDeviceContentUpToDate,
		api.EventReasonResourceCreated,
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{}...)
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
	require.Equal(int32(http.StatusOK), retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{}...)

	value, err = util.StructToMap(api.DeviceSystemInfo{
		AgentVersion:    "a",
		Architecture:    "b",
		BootID:          "2",
		OperatingSystem: "3",
	})
	require.NoError(err)
	_, retStatus = serviceHandler.PatchDeviceStatus(ctx, *device.Metadata.Name, patchRequest)
	require.Equal(int32(http.StatusOK), retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func compareEvents(expectedEvents []api.EventReason, events []api.Event, require *require.Assertions) {
	require.Len(events, len(expectedEvents))
	found := 0
eventsLoop:
	for _, event := range events {
		for _, expectedReason := range expectedEvents {
			if event.Reason == expectedReason {
				found++
				continue eventsLoop
			}
		}
	}
	require.Equal(len(expectedEvents), found)
}

func TestEventDeviceCreatedAndIsAlive(t *testing.T) {
	require := require.New(t)

	serviceHandler := &ServiceHandler{
		store:           &TestStore{},
		callbackManager: dummyCallbackManager(),
		log:             logrus.New(),
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	device := prepareDevice()

	// Create device
	expectedEvents := []api.EventReason{
		api.EventReasonDeviceContentUpToDate,
		api.EventReasonResourceCreated,
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	// Device I-am-alive
	expectedEvents = append(expectedEvents, []api.EventReason{
		api.EventReasonResourceUpdated,
		api.EventReasonDeviceApplicationHealthy,
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

	serviceHandler := &ServiceHandler{
		store:           &TestStore{},
		callbackManager: dummyCallbackManager(),
		log:             logrus.New(),
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	device := prepareDevice()

	// Create device
	expectedEvents := []api.EventReason{
		api.EventReasonDeviceContentUpToDate,
		api.EventReasonResourceCreated,
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{
		api.EventReasonDeviceApplicationHealthy,
		api.EventReasonResourceUpdated,
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

	expectedEvents = append(expectedEvents, []api.EventReason{}...)
	_, err = serviceHandler.UpdateDevice(ctx, *device.Metadata.Name, *device, nil)
	require.NoError(err)
	events, err = serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}
