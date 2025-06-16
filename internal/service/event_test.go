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
	"github.com/stretchr/testify/require"
)

func prepareDevice() *api.Device {
	deviceStatus := api.NewDeviceStatus()
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

	const newOwner = "new.owner"

	serviceHandler := &ServiceHandler{
		store:           &TestStore{},
		callbackManager: dummyCallbackManager(),
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	device := prepareDevice()

	// Create device
	expectedEvents := []api.EventReason{
		api.DeviceContentUpToDate,
		api.ResourceCreated,
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []api.EventReason{
		api.ResourceCreated,
		api.DeviceContentOutOfDate,
		api.ResourceUpdated,
	}...)
	fleet := prepareFleet(newOwner)
	_, retStatus = serviceHandler.CreateFleet(ctx, fleet)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	device.Metadata.Owner = util.SetResourceOwner(api.DeviceKind, newOwner)
	device, retStatus = serviceHandler.ReplaceDevice(ctx, *device.Metadata.Name, *device, nil)
	require.Equal(int32(http.StatusOK), retStatus.Code)
	require.Equal(*device.Metadata.Owner, util.ResourceOwner(device.Kind, newOwner))
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
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	device := prepareDevice()

	// Create device
	expectedEvents := []api.EventReason{
		api.DeviceContentUpToDate,
		api.ResourceCreated,
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, *device)
	require.Equal(int32(http.StatusCreated), retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), uuid.New(), store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	// Device I-am-alive
	expectedEvents = append(expectedEvents, []api.EventReason{
		api.ResourceUpdated,
		api.DeviceApplicationHealthy,
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
