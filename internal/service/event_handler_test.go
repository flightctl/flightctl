package service

import (
	"context"
	"crypto"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"
)

// MockKVStore implements kvstore.KVStore for testing
type MockKVStore struct{}

func (m *MockKVStore) Close() {}
func (m *MockKVStore) SetNX(ctx context.Context, key string, value []byte) (bool, error) {
	return true, nil
}
func (m *MockKVStore) SetIfGreater(ctx context.Context, key string, newVal int64) (bool, error) {
	return true, nil
}
func (m *MockKVStore) Get(ctx context.Context, key string) ([]byte, error) { return nil, nil }
func (m *MockKVStore) GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error) {
	return value, nil
}
func (m *MockKVStore) DeleteKeysForTemplateVersion(ctx context.Context, key string) error { return nil }
func (m *MockKVStore) DeleteAllKeys(ctx context.Context) error                            { return nil }
func (m *MockKVStore) PrintAllKeys(ctx context.Context)                                   {}
func (m *MockKVStore) Delete(ctx context.Context, key string) error                       { return nil }
func (m *MockKVStore) StreamAdd(ctx context.Context, key string, value []byte) (string, error) {
	return "0-0", nil
}
func (m *MockKVStore) StreamRange(ctx context.Context, key string, start, stop string) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (m *MockKVStore) StreamRead(ctx context.Context, key string, lastID string, block time.Duration, count int64) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (m *MockKVStore) SetExpire(ctx context.Context, key string, expiration time.Duration) error {
	return nil
}

func serviceHandler() *ServiceHandler {
	testStore := &TestStore{}
	return &ServiceHandler{
		eventHandler: NewEventHandler(testStore, nil, logrus.New()),
		store:        testStore,
		workerClient: &DummyWorkerClient{},
		kvStore:      &MockKVStore{},
		log:          logrus.New(),
		agentGate:    semaphore.NewWeighted(MaxConcurrentAgents),
	}
}

func prepareDevice(orgId uuid.UUID, name string) *domain.Device {
	deviceStatus := domain.NewDeviceStatus()
	deviceStatus.LastSeen = lo.ToPtr(time.Now())
	deviceStatus.SystemInfo = domain.DeviceSystemInfo{
		AgentVersion:    "1",
		Architecture:    "2",
		BootID:          "3",
		OperatingSystem: "4",
	}
	return &domain.Device{
		ApiVersion: "v1",
		Kind:       "Device",
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: &domain.DeviceSpec{
			Os: &domain.DeviceOsSpec{Image: "img"},
		},
		Status: &deviceStatus,
	}
}

func prepareFleet(owner string) domain.Fleet {
	return domain.Fleet{
		ApiVersion: "v1",
		Kind:       "Fleet",
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr(owner),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: domain.FleetSpec{
			Selector: &domain.LabelSelector{
				MatchLabels: &map[string]string{"devKey": "devValue"},
			},
			Template: struct {
				Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
				Spec     domain.DeviceSpec  "json:\"spec\""
			}{
				Spec: domain.DeviceSpec{
					Os: &domain.DeviceOsSpec{
						Image: "img",
					},
				},
			},
		},
		Status: &domain.FleetStatus{
			Conditions: []domain.Condition{
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
	testOrgId := uuid.New()
	device := prepareDevice(uuid.New(), "foo")

	// Create device
	expectedEvents := []common.ResourceUpdate{
		{Reason: domain.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, testOrgId, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []common.ResourceUpdate{
		{Reason: domain.EventReasonResourceCreated, Details: "Fleet was created successfully."},
		{Reason: domain.EventReasonDeviceContentOutOfDate, Details: "Device has not yet been scheduled for update to the fleet's latest spec."},
		{Reason: domain.EventReasonResourceUpdated, Details: "Device was updated successfully (owner)."},
	}...)
	fleet := prepareFleet(newOwner1)
	_, retStatus = serviceHandler.CreateFleet(ctx, testOrgId, fleet)
	require.Equal(statusCreatedCode, retStatus.Code)
	device.Metadata.Owner = util.SetResourceOwner(domain.DeviceKind, newOwner1)
	device, retStatus = serviceHandler.ReplaceDevice(ctx, testOrgId, *device.Metadata.Name, *device, nil)
	require.Equal(statusSuccessCode, retStatus.Code)
	require.Equal(*device.Metadata.Owner, util.ResourceOwner(device.Kind, newOwner1))
	events, err = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []common.ResourceUpdate{
		{Reason: domain.EventReasonResourceCreated, Details: "Fleet was created successfully."},
		{Reason: domain.EventReasonResourceUpdated, Details: "Device was updated successfully (owner)."},
	}...)
	fleet = prepareFleet(newOwner2)
	_, retStatus = serviceHandler.CreateFleet(ctx, testOrgId, fleet)
	require.Equal(statusCreatedCode, retStatus.Code)
	device.Metadata.Owner = util.SetResourceOwner(domain.DeviceKind, newOwner2)
	device, retStatus = serviceHandler.ReplaceDevice(ctx, testOrgId, *device.Metadata.Name, *device, nil)
	require.Equal(statusSuccessCode, retStatus.Code)
	require.Equal(*device.Metadata.Owner, util.ResourceOwner(device.Kind, newOwner2))
	events, err = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDeviceReplaceDeviceStatus(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	ctx := context.Background()
	testOrgId := uuid.New()
	device := prepareDevice(uuid.New(), "foo")

	// Create device
	expectedEvents := []common.ResourceUpdate{
		{Reason: domain.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, testOrgId, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []common.ResourceUpdate{
		{Reason: domain.EventReasonDeviceConnected, Details: "Device's system resources are healthy."},
		{Reason: domain.EventReasonDeviceApplicationHealthy, Details: "Device has not reported any application workloads yet."},
	}...)
	device, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, testOrgId, *device.Metadata.Name, *device)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	_, retStatus = serviceHandler.ReplaceDeviceStatus(ctx, testOrgId, *device.Metadata.Name, *device)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDeviceReplaceDeviceStatus1(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	serviceHandler := serviceHandler()
	testOrgId := uuid.New()

	device := prepareDevice(uuid.New(), "foo")
	result, status := serviceHandler.CreateDevice(ctx, testOrgId, *device)
	assert.Equal(t, statusCreatedCode, status.Code)
	assert.NotNil(t, result)

	newDevice := prepareDevice(uuid.New(), "foo")
	newDevice.Status.LastSeen = lo.ToPtr(time.Now())
	newDevice.Status.Resources.Cpu = "Healthy"
	newDevice.Status.Resources.Disk = "Healthy"
	newDevice.Status.Resources.Memory = "Healthy"
	newDevice.Status.Summary.Status = "Online"
	newDevice.Status.Lifecycle.Status = "Unknown"

	result, status = serviceHandler.ReplaceDeviceStatus(ctx, testOrgId, "foo", *newDevice)
	assert.Equal(t, statusSuccessCode, status.Code)
	assert.NotNil(t, result)

	events, err := serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	assert.NoError(t, err)
	assert.Equal(t, 3, len(events.Items))
}

func TestEventHandler_HandleDeviceUpdatedEmptyOldDevice(t *testing.T) {
	serviceHandler := serviceHandler()

	ctx := context.Background()
	testOrgId := uuid.New()

	device := prepareDevice(uuid.New(), "foo")
	device.Status.Updated.Status = domain.DeviceUpdatedStatusUpToDate
	oldDevice := &domain.Device{}
	serviceHandler.eventHandler.HandleDeviceUpdatedEvents(ctx, domain.DeviceKind, testOrgId, "foo", oldDevice, device, false, nil)

	events, err := serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(events.Items))
}

func TestEventHandler_DeviceDisconnectedEventDeduplication(t *testing.T) {
	serviceHandler := serviceHandler()

	ctx := context.Background()
	testOrgId := uuid.New()

	// Create old device with all statuses as non-Unknown
	oldDevice := prepareDevice(uuid.New(), "foo")
	oldDevice.Status.Summary.Status = domain.DeviceSummaryStatusOnline
	oldDevice.Status.Updated.Status = domain.DeviceUpdatedStatusUpToDate
	oldDevice.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusHealthy

	// Create new device with all statuses as Unknown (simulating disconnection)
	newDevice := prepareDevice(uuid.New(), "foo")
	newDevice.Status.Summary.Status = domain.DeviceSummaryStatusUnknown
	newDevice.Status.Updated.Status = domain.DeviceUpdatedStatusUnknown
	newDevice.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusUnknown

	// This should trigger multiple DeviceDisconnected events, but only one should be emitted
	serviceHandler.eventHandler.HandleDeviceUpdatedEvents(ctx, domain.DeviceKind, testOrgId, "foo", oldDevice, newDevice, false, nil)

	events, err := serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	assert.NoError(t, err)

	// Should only have 1 DeviceDisconnected event, not 3
	deviceDisconnectedCount := 0
	for _, event := range events.Items {
		if event.Reason == domain.EventReasonDeviceDisconnected {
			deviceDisconnectedCount++
		}
	}
	assert.Equal(t, 1, deviceDisconnectedCount, "Should only emit one DeviceDisconnected event when multiple status fields change to Unknown")
}

func TestEventDevicePatchDeviceStatus(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	ctx := context.Background()
	testOrgId := uuid.New()
	device := prepareDevice(uuid.New(), "foo")

	// Create device
	expectedEvents := []common.ResourceUpdate{
		{Reason: domain.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, testOrgId, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	infoMap, err := util.StructToMap(domain.DeviceSystemInfo{
		AgentVersion:    "a",
		Architecture:    "b",
		BootID:          "c",
		OperatingSystem: "d",
	})
	require.NoError(err)

	var value interface{} = infoMap
	patchRequest := domain.PatchRequest{
		{Op: "replace", Path: "/status/systemInfo", Value: &value},
	}
	device, retStatus = serviceHandler.PatchDeviceStatus(ctx, testOrgId, *device.Metadata.Name, patchRequest)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	value, err = util.StructToMap(domain.DeviceSystemInfo{
		AgentVersion:    "a",
		Architecture:    "b",
		BootID:          "2",
		OperatingSystem: "3",
	})
	require.NoError(err)
	_, retStatus = serviceHandler.PatchDeviceStatus(ctx, testOrgId, *device.Metadata.Name, patchRequest)
	require.Equal(statusSuccessCode, retStatus.Code)
	events, err = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func compareEvents(expectedEvents []common.ResourceUpdate, events []domain.Event, require *require.Assertions) {
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
	testOrgId := uuid.New()
	device := prepareDevice(uuid.New(), "foo")

	// Create device
	expectedEvents := []common.ResourceUpdate{
		{Reason: domain.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, testOrgId, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	// Device I-am-alive
	expectedEvents = append(expectedEvents, []common.ResourceUpdate{
		{Reason: domain.EventReasonDeviceConnected, Details: "Device's system resources are healthy."},
		{Reason: domain.EventReasonDeviceApplicationHealthy, Details: "Device has not reported any application workloads yet."},
	}...)
	device.Status.LastSeen = lo.ToPtr(time.Now())
	device, err = serviceHandler.UpdateDevice(ctx, testOrgId, *device.Metadata.Name, *device, nil)
	require.NoError(err)
	events, _ = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	compareEvents(expectedEvents, events.Items, require)

	// Device I-am-alive
	// No new expected events
	device.Status.LastSeen = lo.ToPtr(time.Now())
	_, err = serviceHandler.UpdateDevice(ctx, testOrgId, *device.Metadata.Name, *device, nil)
	require.NoError(err)
	events, err = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestEventDeviceUpdated(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	ctx := context.Background()
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	testOrgId := uuid.New()
	device := prepareDevice(uuid.New(), "foo")

	// Create device
	expectedEvents := []common.ResourceUpdate{
		{Reason: domain.EventReasonResourceCreated, Details: "Device was created successfully."},
	}
	device, retStatus := serviceHandler.CreateDevice(ctx, testOrgId, *device)
	require.Equal(statusCreatedCode, retStatus.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	expectedEvents = append(expectedEvents, []common.ResourceUpdate{
		{Reason: domain.EventReasonDeviceConnected, Details: "Device's system resources are healthy."},
		{Reason: domain.EventReasonDeviceApplicationHealthy, Details: "Device has not reported any application workloads yet."},
	}...)
	device.Status.Resources = domain.DeviceResourceStatus{
		Cpu:    domain.DeviceResourceStatusHealthy,
		Memory: domain.DeviceResourceStatusHealthy,
		Disk:   domain.DeviceResourceStatusHealthy,
	}
	device.Status.LastSeen = lo.ToPtr(time.Now())
	device, err = serviceHandler.UpdateDevice(ctx, testOrgId, *device.Metadata.Name, *device, nil)
	require.NoError(err)
	events, err = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)

	_, err = serviceHandler.UpdateDevice(ctx, testOrgId, *device.Metadata.Name, *device, nil)
	require.NoError(err)
	events, err = serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

// =============================== FLEET ====================================

func TestEventHandler_EmitFleetValidEvents(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	testOrgId := uuid.New()
	handler := serviceHandler()
	fleetName := "test-fleet"

	// Test case 1: Fleet becomes valid
	t.Run("FleetBecomesValid", func(t *testing.T) {
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
			Status: &domain.FleetStatus{
				Conditions: []domain.Condition{
					{
						Type:    domain.ConditionTypeFleetValid,
						Status:  domain.ConditionStatusFalse,
						Reason:  string(domain.EventReasonFleetInvalid),
						Message: "Invalid configuration",
					},
				},
			},
		}

		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
			Status: &domain.FleetStatus{
				Conditions: []domain.Condition{
					{
						Type:    domain.ConditionTypeFleetValid,
						Status:  domain.ConditionStatusTrue,
						Reason:  string(domain.EventReasonFleetValid),
						Message: "",
					},
				},
			},
		}

		handler.eventHandler.emitFleetValidEvents(ctx, testOrgId, fleetName, oldFleet, newFleet)

		// Verify that a valid event was created
		events := *handler.store.(*TestStore).events.events
		require.Len(events, 1)
		event := events[0]
		require.Equal(domain.FleetKind, event.InvolvedObject.Kind)
		require.Equal(fleetName, event.InvolvedObject.Name)
		require.Equal(domain.EventReasonFleetValid, event.Reason)
		require.Equal(domain.Normal, event.Type)
		require.Equal("Fleet specification is valid.", event.Message)
	})

	// Test case 2: Fleet becomes invalid
	t.Run("FleetBecomesInvalid", func(t *testing.T) {
		*handler.store.(*TestStore).events.events = nil // Clear previous events

		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
			Status: &domain.FleetStatus{
				Conditions: []domain.Condition{
					{
						Type:    domain.ConditionTypeFleetValid,
						Status:  domain.ConditionStatusTrue,
						Reason:  string(domain.EventReasonFleetValid),
						Message: "",
					},
				},
			},
		}

		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
			Status: &domain.FleetStatus{
				Conditions: []domain.Condition{
					{
						Type:    domain.ConditionTypeFleetValid,
						Status:  domain.ConditionStatusFalse,
						Reason:  string(domain.EventReasonFleetInvalid),
						Message: "Invalid configuration",
					},
				},
			},
		}

		handler.eventHandler.emitFleetValidEvents(ctx, testOrgId, fleetName, oldFleet, newFleet)

		// Verify that an invalid event was created
		events := *handler.store.(*TestStore).events.events
		require.Len(events, 1)
		event := events[0]
		require.Equal(domain.FleetKind, event.InvolvedObject.Kind)
		require.Equal(fleetName, event.InvolvedObject.Name)
		require.Equal(domain.EventReasonFleetInvalid, event.Reason)
		require.Equal(domain.Warning, event.Type)
		require.Equal("Fleet specification is invalid: Invalid configuration.", event.Message)
	})

	// Test case 3: No condition change
	t.Run("NoConditionChange", func(t *testing.T) {
		*handler.store.(*TestStore).events.events = nil // Clear previous events

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
			Status: &domain.FleetStatus{
				Conditions: []domain.Condition{
					{
						Type:    domain.ConditionTypeFleetValid,
						Status:  domain.ConditionStatusTrue,
						Reason:  string(domain.EventReasonFleetValid),
						Message: "",
					},
				},
			},
		}

		handler.eventHandler.emitFleetValidEvents(ctx, testOrgId, fleetName, fleet, fleet)

		// Verify that no event was created
		events := *handler.store.(*TestStore).events.events
		require.Len(events, 0)
	})

	// Test case 4: Fleet with no status
	t.Run("FleetWithNoStatus", func(t *testing.T) {
		*handler.store.(*TestStore).events.events = nil // Clear previous events

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
			Status:   nil,
		}

		handler.eventHandler.emitFleetValidEvents(ctx, testOrgId, fleetName, fleet, fleet)

		// Verify that no event was created
		events := *handler.store.(*TestStore).events.events
		require.Len(events, 0)
	})
}

// =============================== ENROLLMENT REQUEST ========================
func TestEventEnrollmentRequestApproved(t *testing.T) {
	require := require.New(t)

	serviceHandler := serviceHandler()
	testOrgId := uuid.New()
	testDirPath := t.TempDir()
	cfg := ca.NewDefault(testDirPath)
	ca, _, err := fcrypto.EnsureCA(cfg)
	require.NoError(err)
	serviceHandler.ca = ca
	ctx := context.Background()

	// create ER
	name := uuid.New().String()
	approval := domain.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
	}
	status := domain.EnrollmentRequestStatus{}
	deviceStatus := domain.NewDeviceStatus()
	tmpDir := t.TempDir()
	deviceReadWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	_, privateKey, _, err := fccrypto.EnsureKey(deviceReadWriter.PathFor("TestCSR"))
	require.NoError(err)
	csr, err := fccrypto.MakeCSR(privateKey.(crypto.Signer), name)
	require.NoError(err)
	er := domain.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: domain.EnrollmentRequestSpec{
			Csr:          string(csr),
			DeviceStatus: &deviceStatus,
			Labels:       &map[string]string{"labelKey": "labelValue"}},
		Status: &status,
	}

	eventCallback := func(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
		if err != nil {
			status := StoreErrorToApiStatus(err, created, domain.EnrollmentRequestKind, &name)
			serviceHandler.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, domain.EnrollmentRequestKind, name, status, nil))
		} else {
			serviceHandler.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, domain.EnrollmentRequestKind, name, nil, serviceHandler.log, nil))
		}
	}
	_, err = serviceHandler.store.EnrollmentRequest().Create(ctx, testOrgId, &er, eventCallback)
	require.NoError(err)

	mappedIdentity := identity.NewMappedIdentity("bar", "", []*model.Organization{}, map[string][]string{}, false, nil)
	ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)
	_, stat := serviceHandler.ApproveEnrollmentRequest(ctx, testOrgId, name, approval)
	require.Equal(statusSuccessCode, stat.Code)
	expectedEvents := []common.ResourceUpdate{
		{Reason: domain.EventReasonResourceCreated, Details: "EnrollmentRequest was created successfully."},
		{Reason: domain.EventReasonResourceCreated, Details: "Device was created successfully."},
		{Reason: domain.EventReasonEnrollmentRequestApproved, Details: "EnrollmentRequest was approved successfully."},
	}
	require.Equal(statusSuccessCode, stat.Code)
	events, err := serviceHandler.store.Event().List(context.Background(), testOrgId, store.ListParams{})
	require.NoError(err)
	compareEvents(expectedEvents, events.Items, require)
}

func TestGetEnrollmentRequestApprovedEvent(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	resourceName := "test-enrollment-request"

	event := common.GetEnrollmentRequestApprovedEvent(ctx, resourceName, nil)

	require.NotNil(event)
	require.Equal(domain.EnrollmentRequestKind, event.InvolvedObject.Kind)
	require.Equal(resourceName, event.InvolvedObject.Name)
	require.Equal(domain.EventReasonEnrollmentRequestApproved, event.Reason)
	require.Equal(domain.Normal, event.Type)
	require.Equal("EnrollmentRequest was approved successfully.", event.Message)
	require.NotNil(event.Metadata.Name)
}

func TestGetEnrollmentRequestApprovalFailedEvent(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	resourceName := "test-enrollment-request"
	status := domain.StatusBadRequest("approval failed")

	event := common.GetEnrollmentRequestApprovalFailedEvent(ctx, resourceName, status, nil)

	require.NotNil(event)
	require.Equal(domain.EnrollmentRequestKind, event.InvolvedObject.Kind)
	require.Equal(resourceName, event.InvolvedObject.Name)
	require.Equal(domain.EventReasonEnrollmentRequestApprovalFailed, event.Reason)
	require.Equal(domain.Warning, event.Type)
	require.Equal("EnrollmentRequest approval failed: approval failed.", event.Message)
	require.NotNil(event.Metadata.Name)
}

func TestGetDeviceSpecInvalidEvent(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	deviceName := "test-device"
	message := "validation failed"

	event := common.GetDeviceSpecInvalidEvent(ctx, deviceName, message)

	require.NotNil(event)
	require.Equal(domain.DeviceKind, event.InvolvedObject.Kind)
	require.Equal(deviceName, event.InvolvedObject.Name)
	require.Equal(domain.EventReasonDeviceSpecInvalid, event.Reason)
	require.Equal(domain.Warning, event.Type)
	require.Equal("Device specification is invalid: validation failed.", event.Message)
	require.NotNil(event.Metadata.Name)
}

func TestGetDeviceSpecValidEvent(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	deviceName := "test-device"

	event := common.GetDeviceSpecValidEvent(ctx, deviceName)

	require.NotNil(event)
	require.Equal(domain.DeviceKind, event.InvolvedObject.Kind)
	require.Equal(deviceName, event.InvolvedObject.Name)
	require.Equal(domain.EventReasonDeviceSpecValid, event.Reason)
	require.Equal(domain.Normal, event.Type)
	require.Equal("Device specification is valid.", event.Message)
	require.NotNil(event.Metadata.Name)
}

func TestGetDeviceMultipleOwnersDetectedEvent(t *testing.T) {
	require := require.New(t)
	logger := logrus.New()

	ctx := context.Background()
	deviceName := "test-device"
	matchingFleets := []string{"fleet1", "fleet2", "fleet3"}

	event := common.GetDeviceMultipleOwnersDetectedEvent(ctx, deviceName, matchingFleets, logger)

	require.NotNil(event)
	require.Equal(string(domain.DeviceKind), event.InvolvedObject.Kind)
	require.Equal(deviceName, event.InvolvedObject.Name)
	require.Equal(domain.EventReasonDeviceMultipleOwnersDetected, event.Reason)
	require.Equal(domain.Warning, event.Type)
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
		resolutionType domain.DeviceMultipleOwnersResolvedDetailsResolutionType
		assignedOwner  *string
		expectedMsg    string
	}{
		{
			name:           "SingleMatch",
			resolutionType: domain.SingleMatch,
			assignedOwner:  lo.ToPtr("fleet1"),
			expectedMsg:    "Device multiple owners conflict was resolved: single fleet match, assigned to fleet 'fleet1'.",
		},
		{
			name:           "NoMatch",
			resolutionType: domain.NoMatch,
			assignedOwner:  nil,
			expectedMsg:    "Device multiple owners conflict was resolved: no fleet matches, owner was removed.",
		},
		{
			name:           "FleetDeleted",
			resolutionType: domain.FleetDeleted,
			assignedOwner:  nil,
			expectedMsg:    "Device multiple owners conflict was resolved: fleet was deleted.",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			previousFleets := []string{"fleet1", "fleet2"}

			event := common.GetDeviceMultipleOwnersResolvedEvent(ctx, deviceName, tc.resolutionType, tc.assignedOwner, previousFleets, logger)

			require.NotNil(event)
			require.Equal(string(domain.DeviceKind), event.InvolvedObject.Kind)
			require.Equal(deviceName, event.InvolvedObject.Name)
			require.Equal(domain.EventReasonDeviceMultipleOwnersResolved, event.Reason)
			require.Equal(domain.Normal, event.Type)
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

func TestGetResourceCreatedOrUpdatedEvent(t *testing.T) {
	require := require.New(t)
	logger := logrus.New()

	ctx := context.Background()
	resourceKind := domain.ResourceKind(domain.DeviceKind)
	resourceName := "test-device"
	updateDetails := &domain.ResourceUpdatedDetails{
		NewOwner:      lo.ToPtr("fleet2"),
		PreviousOwner: lo.ToPtr("fleet1"),
		UpdatedFields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.Owner},
	}

	t.Run("Created", func(t *testing.T) {
		event := common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, resourceKind, resourceName, nil, logger, nil)

		require.NotNil(event)
		require.Equal(resourceKind, domain.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(domain.EventReasonResourceCreated, event.Reason)
		require.Equal(domain.Normal, event.Type)
		require.Equal("Device was created successfully.", event.Message)
		require.NotNil(event.Metadata.Name)
		require.Nil(event.Details)
	})

	t.Run("Updated", func(t *testing.T) {
		event := common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, resourceKind, resourceName, updateDetails, logger, nil)

		require.NotNil(event)
		require.Equal(resourceKind, domain.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(domain.EventReasonResourceUpdated, event.Reason)
		require.Equal(domain.Normal, event.Type)
		require.Equal("Device was updated successfully (owner).", event.Message)
		require.NotNil(event.Metadata.Name)
		require.NotNil(event.Details)

		// Verify the event details
		detailsStruct, err := event.Details.AsResourceUpdatedDetails()
		require.NoError(err)
		require.Equal(updateDetails.NewOwner, detailsStruct.NewOwner)
		require.Equal(updateDetails.PreviousOwner, detailsStruct.PreviousOwner)
	})

	t.Run("UpdatedWithNilDetails", func(t *testing.T) {
		event := common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, resourceKind, resourceName, nil, logger, nil)

		require.Nil(event)
	})

	t.Run("UpdatedWithEmptyDetails", func(t *testing.T) {
		emptyUpdateDetails := &domain.ResourceUpdatedDetails{
			UpdatedFields: []domain.ResourceUpdatedDetailsUpdatedFields{},
		}
		event := common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, resourceKind, resourceName, emptyUpdateDetails, logger, nil)

		require.Nil(event)
	})

	t.Run("CreatedFailed", func(t *testing.T) {
		event := common.GetResourceCreatedOrUpdatedFailureEvent(ctx, true, resourceKind, resourceName, domain.StatusInternalServerError("creation failed"), nil)

		require.NotNil(event)
		require.Equal(resourceKind, domain.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(domain.EventReasonResourceCreationFailed, event.Reason)
		require.Equal(domain.Warning, event.Type)
		require.Equal("Device creation failed: creation failed.", event.Message)
		require.NotNil(event.Metadata.Name)
		require.Nil(event.Details)
	})

	t.Run("UpdatedFailed", func(t *testing.T) {
		event := common.GetResourceCreatedOrUpdatedFailureEvent(ctx, false, resourceKind, resourceName, domain.StatusInternalServerError("update failed"), &domain.ResourceUpdatedDetails{})

		require.NotNil(event)
		require.Equal(resourceKind, domain.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(domain.EventReasonResourceUpdateFailed, event.Reason)
		require.Equal(domain.Warning, event.Type)
		require.Equal("Device update failed: update failed.", event.Message)
		require.NotNil(event.Metadata.Name)
		require.NotNil(event.Details)
	})
}

func TestGetResourceDeletedEvent(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	resourceKind := domain.ResourceKind(domain.DeviceKind)
	resourceName := "test-device"

	t.Run("Success", func(t *testing.T) {
		event := common.GetResourceDeletedSuccessEvent(ctx, resourceKind, resourceName)

		require.NotNil(event)
		require.Equal(resourceKind, domain.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(domain.EventReasonResourceDeleted, event.Reason)
		require.Equal(domain.Normal, event.Type)
		require.Equal("Device was deleted successfully.", event.Message)
		require.NotNil(event.Metadata.Name)
	})

	t.Run("Failed", func(t *testing.T) {
		event := common.GetResourceDeletedFailureEvent(ctx, resourceKind, resourceName, domain.StatusInternalServerError("deletion failed"))

		require.NotNil(event)
		require.Equal(resourceKind, domain.ResourceKind(event.InvolvedObject.Kind))
		require.Equal(resourceName, event.InvolvedObject.Name)
		require.Equal(domain.EventReasonResourceDeletionFailed, event.Reason)
		require.Equal(domain.Warning, event.Type)
		require.Equal("Device deletion failed: deletion failed.", event.Message)
		require.NotNil(event.Metadata.Name)
	})
}

func TestGetBaseEvent(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()
	event := domain.GetBaseEvent(ctx, domain.DeviceKind, "test-device", domain.EventReasonResourceCreated, "Test message", nil)

	require.NotNil(event)
	require.Equal(string(domain.DeviceKind), event.InvolvedObject.Kind)
	require.Equal("test-device", event.InvolvedObject.Name)
	require.Equal(domain.EventReasonResourceCreated, event.Reason)
	require.Equal("Test message", event.Message)
	require.Equal(domain.Normal, event.Type)
}

func TestComputeResourceUpdatedDetails(t *testing.T) {
	require := require.New(t)
	logger := logrus.New()
	handler := NewEventHandler(nil, nil, logger)

	t.Run("DeviceWithSpecChange", func(t *testing.T) {
		oldDevice := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("test-device"),
				Generation: lo.ToPtr(int64(1)),
				Labels:     lo.ToPtr(map[string]string{"env": "prod"}),
				Owner:      lo.ToPtr("fleet1"),
			},
		}
		newDevice := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("test-device"),
				Generation: lo.ToPtr(int64(2)),
				Labels:     lo.ToPtr(map[string]string{"env": "prod"}),
				Owner:      lo.ToPtr("fleet1"),
			},
		}

		updateDetails := handler.computeResourceUpdatedDetails(oldDevice.Metadata, newDevice.Metadata)
		require.NotNil(updateDetails)
		require.Contains(updateDetails.UpdatedFields, domain.Spec)
		require.Len(updateDetails.UpdatedFields, 1)
	})

	t.Run("FleetWithLabelsChange", func(t *testing.T) {
		oldFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("test-fleet"),
				Generation: lo.ToPtr(int64(1)),
				Labels:     lo.ToPtr(map[string]string{"env": "prod"}),
				Owner:      lo.ToPtr("user1"),
			},
		}
		newFleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("test-fleet"),
				Generation: lo.ToPtr(int64(1)),
				Labels:     lo.ToPtr(map[string]string{"env": "dev"}),
				Owner:      lo.ToPtr("user1"),
			},
		}

		updateDetails := handler.computeResourceUpdatedDetails(oldFleet.Metadata, newFleet.Metadata)
		require.NotNil(updateDetails)
		require.Contains(updateDetails.UpdatedFields, domain.Labels)
		require.Len(updateDetails.UpdatedFields, 1)
	})

	t.Run("RepositoryWithOwnerChange", func(t *testing.T) {
		oldRepo := &domain.Repository{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("test-repo"),
				Generation: lo.ToPtr(int64(1)),
				Labels:     lo.ToPtr(map[string]string{"type": "git"}),
				Owner:      lo.ToPtr("user1"),
			},
		}
		newRepo := &domain.Repository{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("test-repo"),
				Generation: lo.ToPtr(int64(1)),
				Labels:     lo.ToPtr(map[string]string{"type": "git"}),
				Owner:      lo.ToPtr("user2"),
			},
		}

		updateDetails := handler.computeResourceUpdatedDetails(oldRepo.Metadata, newRepo.Metadata)
		require.NotNil(updateDetails)
		require.Contains(updateDetails.UpdatedFields, domain.Owner)
		require.Equal(lo.ToPtr("user1"), updateDetails.PreviousOwner)
		require.Equal(lo.ToPtr("user2"), updateDetails.NewOwner)
		require.Len(updateDetails.UpdatedFields, 1)
	})

	t.Run("NoChanges", func(t *testing.T) {
		oldResource := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("test-device"),
				Generation: lo.ToPtr(int64(1)),
				Labels:     lo.ToPtr(map[string]string{"env": "prod"}),
				Owner:      lo.ToPtr("fleet1"),
			},
		}
		newResource := &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("test-device"),
				Generation: lo.ToPtr(int64(1)),
				Labels:     lo.ToPtr(map[string]string{"env": "prod"}),
				Owner:      lo.ToPtr("fleet1"),
			},
		}

		updateDetails := handler.computeResourceUpdatedDetails(oldResource.Metadata, newResource.Metadata)
		require.Nil(updateDetails)
	})

	t.Run("NilResources", func(t *testing.T) {
		updateDetails := handler.computeResourceUpdatedDetails(domain.ObjectMeta{}, domain.ObjectMeta{})
		require.Nil(updateDetails)

		updateDetails = handler.computeResourceUpdatedDetails(domain.ObjectMeta{}, domain.ObjectMeta{})
		require.Nil(updateDetails)
	})
}
