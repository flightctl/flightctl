package device

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func newTestHandler() (*fakeStore, *fakeEvents, Service) {
	st := newFakeStore()
	ev := &fakeEvents{}
	svc := NewDeviceServiceHandler(st.device, st.fleet, ev, nil, "agent.example.com", logrus.New())
	return st, ev, svc
}

func TestNewDeviceServiceHandler(t *testing.T) {
	_, _, svc := newTestHandler()
	require.NotNil(t, svc)
}

func TestCreateDevice(t *testing.T) {
	t.Run("When creating a valid device it should succeed", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{},
		}
		result, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Equal(t, "foo", lo.FromPtr(result.Metadata.Name))
	})

	t.Run("When creating an already-decommissioned device it should return bad request", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{Decommissioning: &domain.DeviceDecommission{}},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})

	t.Run("When managed metadata fields are set by the caller CreateDeviceFromUntrusted should clear them before creation", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("untrusted"),
				Owner:      lo.ToPtr("Fleet/f1"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: &domain.DeviceSpec{},
		}

		_, status := CreateDeviceFromUntrusted(ctx, svc, orgId, device)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Nil(t, st.device.devices["untrusted"].Metadata.Owner)
		require.Nil(t, st.device.devices["untrusted"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller CreateDevice (trusted) should preserve them", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("trusted"),
				Owner:      lo.ToPtr("Fleet/f1"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: &domain.DeviceSpec{},
		}

		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Equal(t, "Fleet/f1", lo.FromPtr(st.device.devices["trusted"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(st.device.devices["trusted"].Metadata.Generation))
	})
}

func TestGetDevice(t *testing.T) {
	t.Run("When the device does not exist it should return not found", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, status := svc.GetDevice(ctx, orgId, "missing")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When the device exists it should be returned", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
		require.NoError(t, err)
		result, status := svc.GetDevice(ctx, orgId, "foo")
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Equal(t, "foo", lo.FromPtr(result.Metadata.Name))
	})
}

func TestReplaceDevice(t *testing.T) {
	t.Run("When the path name does not match metadata.name it should return bad request", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{},
		}
		_, status := svc.ReplaceDevice(ctx, orgId, "bar", device, nil, true)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})

	t.Run("When replacing a nonexistent device it should create it", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{},
		}
		result, status := svc.ReplaceDevice(ctx, orgId, "foo", device, nil, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Equal(t, "foo", lo.FromPtr(result.Metadata.Name))
	})

	t.Run("When managed metadata fields are set by the caller ReplaceDeviceFromUntrusted should clear them before replacing", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("replace-untrusted"),
				Owner:      lo.ToPtr("Fleet/f1"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: &domain.DeviceSpec{},
		}

		_, status := ReplaceDeviceFromUntrusted(ctx, svc, orgId, "replace-untrusted", device, nil, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Nil(t, st.device.devices["replace-untrusted"].Metadata.Owner)
		require.Nil(t, st.device.devices["replace-untrusted"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceDevice (trusted) should preserve them", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("replace-trusted"),
				Owner:      lo.ToPtr("Fleet/f1"),
				Generation: lo.ToPtr(int64(5)),
			},
			Spec: &domain.DeviceSpec{},
		}

		_, status := svc.ReplaceDevice(ctx, orgId, "replace-trusted", device, nil, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Equal(t, "Fleet/f1", lo.FromPtr(st.device.devices["replace-trusted"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(st.device.devices["replace-trusted"].Metadata.Generation))
	})
}

func TestDeleteDevice(t *testing.T) {
	t.Run("When the device does not exist it should return not found", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		status := svc.DeleteDevice(ctx, orgId, "missing")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When the device exists it should be deleted", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
		require.NoError(t, err)
		status := svc.DeleteDevice(ctx, orgId, "foo")
		require.Equal(t, int32(http.StatusOK), status.Code)
		_, status = svc.GetDevice(ctx, orgId, "foo")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})
}

func TestPatchDevice(t *testing.T) {
	setup := func(t *testing.T) (*fakeStore, Service, uuid.UUID) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr("foo"),
				Labels: &map[string]string{"labelKey": "labelValue"},
			},
			Spec: &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		}
		_, err := st.device.Create(ctx, orgId, &device, nil)
		require.NoError(t, err)
		return st, svc, orgId
	}

	t.Run("When patching a mutable field it should succeed", func(t *testing.T) {
		_, svc, orgId := setup(t)
		var value interface{} = "newimg"
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/spec/os/image", Value: &value},
		}
		result, status := svc.PatchDevice(context.Background(), orgId, "foo", patch, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Equal(t, "newimg", result.Spec.Os.Image)
	})

	t.Run("When patching an immutable field it should return bad request", func(t *testing.T) {
		_, svc, orgId := setup(t)
		var value interface{} = "bar"
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/metadata/name", Value: &value},
		}
		_, status := svc.PatchDevice(context.Background(), orgId, "foo", patch, true)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})

	t.Run("When the device does not exist it should return not found", func(t *testing.T) {
		_, svc, orgId := setup(t)
		var value interface{} = "labelValue1"
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
		}
		_, status := svc.PatchDevice(context.Background(), orgId, "bar", patch, true)
		require.Equal(t, int32(http.StatusNotFound), status.Code)
		require.Equal(t, domain.StatusResourceNotFound("Device", "bar"), status)
	})
}

func TestPatchDeviceStatus(t *testing.T) {
	setup := func(t *testing.T) (Service, uuid.UUID) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		status := domain.NewDeviceStatus()
		status.SystemInfo = domain.DeviceSystemInfo{AgentVersion: "1", Architecture: "2", BootID: "3", OperatingSystem: "4"}
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo"), Labels: &map[string]string{"labelKey": "labelValue"}},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
			Status:   &status,
		}
		_, err := st.device.Create(ctx, orgId, &device, nil)
		require.NoError(t, err)
		return svc, orgId
	}

	t.Run("When patching status.systemInfo it should succeed", func(t *testing.T) {
		svc, orgId := setup(t)
		infoMap, err := util.StructToMap(domain.DeviceSystemInfo{AgentVersion: "a", Architecture: "b", BootID: "c", OperatingSystem: "d"})
		require.NoError(t, err)
		var value interface{} = infoMap
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/status/systemInfo", Value: &value},
		}
		result, status := svc.PatchDeviceStatus(context.Background(), orgId, "foo", patch)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Equal(t, "a", result.Status.SystemInfo.AgentVersion)
	})

	t.Run("When patching an immutable field it should return bad request", func(t *testing.T) {
		svc, orgId := setup(t)
		var value interface{} = "newname"
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/metadata/name", Value: &value},
		}
		_, status := svc.PatchDeviceStatus(context.Background(), orgId, "foo", patch)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})

	t.Run("When the device does not exist it should return not found", func(t *testing.T) {
		svc, orgId := setup(t)
		var value interface{} = "a"
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/status/systemInfo/agentVersion", Value: &value},
		}
		_, status := svc.PatchDeviceStatus(context.Background(), orgId, "bar", patch)
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})
}

// TestDeviceRepositoryRefs directly exercises AC-2: GetDeviceRepositoryRefs and
// OverwriteDeviceRepositoryRefs must be present on device.Service and must delegate to (and
// translate errors from) the Device store's own repository-association methods, with no
// separate repositorystore.Store dependency.
func TestDeviceRepositoryRefs(t *testing.T) {
	t.Run("When overwriting refs for a nonexistent device it should return not found", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		status := svc.OverwriteDeviceRepositoryRefs(ctx, orgId, "missing", "repo1")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When overwriting and then reading refs it should round-trip", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
		require.NoError(t, err)

		status := svc.OverwriteDeviceRepositoryRefs(ctx, orgId, "foo", "repo1", "repo2")
		require.Equal(t, int32(http.StatusOK), status.Code)

		refs, status := svc.GetDeviceRepositoryRefs(ctx, orgId, "foo")
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Len(t, refs.Items, 2)
	})

	t.Run("When reading refs for a nonexistent device it should return not found", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, status := svc.GetDeviceRepositoryRefs(ctx, orgId, "missing")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})
}

func TestResumeDevices(t *testing.T) {
	t.Run("When no devices are conflict-paused it should not emit events", func(t *testing.T) {
		_, ev, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		resp, status := svc.ResumeDevices(ctx, orgId, domain.DeviceResumeRequest{})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Equal(t, 0, resp.ResumedDevices)
		require.Empty(t, ev.created)
	})

	t.Run("When devices are conflict-paused it should resume them and emit one event per device", func(t *testing.T) {
		st, ev, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		annotations := map[string]string{domain.DeviceAnnotationConflictPaused: "true"}
		_, err := st.device.Create(ctx, orgId, &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo"), Annotations: &annotations},
		}, nil)
		require.NoError(t, err)

		resp, status := svc.ResumeDevices(ctx, orgId, domain.DeviceResumeRequest{})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Equal(t, 1, resp.ResumedDevices)
		require.Len(t, ev.created, 1)
	})

	t.Run("When events is nil it should not panic", func(t *testing.T) {
		st := newFakeStore()
		svc := NewDeviceServiceHandler(st.device, st.fleet, nil, nil, "agent.example.com", logrus.New())
		ctx := context.Background()
		orgId := uuid.New()
		annotations := map[string]string{domain.DeviceAnnotationConflictPaused: "true"}
		_, err := st.device.Create(ctx, orgId, &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo"), Annotations: &annotations},
		}, nil)
		require.NoError(t, err)

		require.NotPanics(t, func() {
			resp, status := svc.ResumeDevices(ctx, orgId, domain.DeviceResumeRequest{})
			require.Equal(t, int32(http.StatusOK), status.Code)
			require.Equal(t, 1, resp.ResumedDevices)
		})
	})
}

// TestUpdateServerSideDeviceStatus_ManagedDevice verifies status computation for a managed
// (fleet-owned) device, which requires looking up the owning fleet via fleetStore.
func TestUpdateServerSideDeviceStatus_ManagedDevice(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()

	st.fleet.fleets["myfleet"] = &domain.Fleet{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("myfleet")},
		Spec:     domain.FleetSpec{},
	}

	device := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name:  lo.ToPtr("foo"),
			Owner: lo.ToPtr("Fleet/myfleet"),
		},
		Spec:   &domain.DeviceSpec{},
		Status: lo.ToPtr(domain.NewDeviceStatus()),
	}
	_, err := st.device.Create(ctx, orgId, device, nil)
	require.NoError(t, err)

	err = svc.UpdateServerSideDeviceStatus(ctx, orgId, "foo")
	require.NoError(t, err)
	require.Equal(t, 1, st.fleet.getCalls, "expected common.UpdateServiceSideStatus to reach store.Store.Fleet().Get() for a managed device")
}

func TestUpdateServerSideDeviceStatus_UnmanagedDevice(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()

	device := &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
		Spec:     &domain.DeviceSpec{},
		Status:   lo.ToPtr(domain.NewDeviceStatus()),
	}
	_, err := st.device.Create(ctx, orgId, device, nil)
	require.NoError(t, err)

	err = svc.UpdateServerSideDeviceStatus(ctx, orgId, "foo")
	require.NoError(t, err)
	require.Equal(t, 0, st.fleet.getCalls, "an unmanaged device should never trigger a Fleet() lookup")
}

func TestListDevicesByServiceCondition(t *testing.T) {
	_, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	result, status := svc.ListDevicesByServiceCondition(ctx, orgId, "SomeCondition", "True", store.ListParams{})
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.NotNil(t, result)
}

func TestListDevices(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
	require.NoError(t, err)
	result, status := svc.ListDevices(ctx, orgId, domain.ListDevicesParams{}, nil)
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.Len(t, result.Items, 1)
}

func TestListConnectivityChangedDevices(t *testing.T) {
	_, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	result, status := svc.ListConnectivityChangedDevices(ctx, orgId, domain.ListDevicesParams{}, time.Now())
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.NotNil(t, result)
}

func TestCountDevices(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
	require.NoError(t, err)
	count, status := svc.CountDevices(ctx, orgId, domain.ListDevicesParams{}, nil)
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.Equal(t, int64(1), count)
}

func TestUnmarkDevicesRolloutSelection(t *testing.T) {
	_, _, svc := newTestHandler()
	status := svc.UnmarkDevicesRolloutSelection(context.Background(), uuid.New(), "myfleet")
	require.Equal(t, int32(http.StatusOK), status.Code)
}

func TestMarkDevicesRolloutSelection(t *testing.T) {
	_, _, svc := newTestHandler()
	status := svc.MarkDevicesRolloutSelection(context.Background(), uuid.New(), domain.ListDevicesParams{}, nil, nil)
	require.Equal(t, int32(http.StatusOK), status.Code)
}

func TestGetDeviceCompletionCounts(t *testing.T) {
	_, _, svc := newTestHandler()
	result, status := svc.GetDeviceCompletionCounts(context.Background(), uuid.New(), "owner", "tv1", nil)
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.NotNil(t, result)
}

func TestCountDevicesByLabels(t *testing.T) {
	_, _, svc := newTestHandler()
	result, status := svc.CountDevicesByLabels(context.Background(), uuid.New(), domain.ListDevicesParams{}, nil, []string{"foo"})
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.NotNil(t, result)
}

func TestListLabels(t *testing.T) {
	t.Run("When kind is Device it should succeed", func(t *testing.T) {
		_, _, svc := newTestHandler()
		result, status := svc.ListLabels(context.Background(), uuid.New(), domain.ListLabelsParams{Kind: "Device"})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
	})

	t.Run("When kind is unsupported it should return a bad-request status", func(t *testing.T) {
		_, _, svc := newTestHandler()
		_, status := svc.ListLabels(context.Background(), uuid.New(), domain.ListLabelsParams{Kind: "Fleet"})
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})
}

func TestGetDevicesSummary(t *testing.T) {
	_, _, svc := newTestHandler()
	result, status := svc.GetDevicesSummary(context.Background(), uuid.New(), domain.ListDevicesParams{}, nil)
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.NotNil(t, result)
}

func TestGetDeviceStatus(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
	require.NoError(t, err)
	result, status := svc.GetDeviceStatus(ctx, orgId, "foo")
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.Equal(t, "foo", lo.FromPtr(result.Metadata.Name))
}

func TestGetDeviceLastSeen(t *testing.T) {
	t.Run("When the device does not exist it should return not found", func(t *testing.T) {
		_, _, svc := newTestHandler()
		_, status := svc.GetDeviceLastSeen(context.Background(), uuid.New(), "missing")
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})

	t.Run("When the device exists but has never reported it should return no content", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
		require.NoError(t, err)
		_, status := svc.GetDeviceLastSeen(ctx, orgId, "foo")
		require.Equal(t, int32(http.StatusNoContent), status.Code)
	})
}

func TestSetOutOfDate(t *testing.T) {
	_, _, svc := newTestHandler()
	err := svc.SetOutOfDate(context.Background(), uuid.New(), "owner")
	require.NoError(t, err)
}

func TestUpdateDeviceAnnotations(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
	require.NoError(t, err)
	status := svc.UpdateDeviceAnnotations(ctx, orgId, "foo", map[string]string{"k": "v"}, nil)
	require.Equal(t, int32(http.StatusOK), status.Code)
}

func TestUpdateDevice(t *testing.T) {
	t.Run("When updating a decommissioned device spec it should return an error", func(t *testing.T) {
		_, _, svc := newTestHandler()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{Decommissioning: &domain.DeviceDecommission{}},
		}
		_, err := svc.UpdateDevice(context.Background(), uuid.New(), "foo", device, nil)
		require.Error(t, err)
	})

	t.Run("When the path name does not match metadata.name it should return an error", func(t *testing.T) {
		_, _, svc := newTestHandler()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{},
		}
		_, err := svc.UpdateDevice(context.Background(), uuid.New(), "bar", device, nil)
		require.Error(t, err)
	})

	t.Run("When updating an existing device it should succeed", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}, Spec: &domain.DeviceSpec{}}, nil)
		require.NoError(t, err)
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		}
		result, err := svc.UpdateDevice(ctx, orgId, "foo", device, nil)
		require.NoError(t, err)
		require.Equal(t, "img", result.Spec.Os.Image)
	})
}

func TestDecommissionDevice(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
	require.NoError(t, err)
	result, status := svc.DecommissionDevice(ctx, orgId, "foo", domain.DeviceDecommission{})
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.NotNil(t, result.Spec.Decommissioning)
}

// TestUpdateRenderedDevice covers the "no change in rendered version" path only. The
// changed-version path additionally calls rendered.Bus.Instance().StoreAndNotify, a
// process-global singleton that requires integration-level initialization (see
// test/integration/service/device_test.go); exercising it here would not be hermetic.
func TestUpdateRenderedDevice(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, err := st.device.Create(ctx, orgId, &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
		Spec:     &domain.DeviceSpec{},
		Status:   lo.ToPtr(domain.NewDeviceStatus()),
	}, nil)
	require.NoError(t, err)
	status := svc.UpdateRenderedDevice(ctx, orgId, "foo", "config", "apps", "hash", nil, false)
	require.Equal(t, int32(http.StatusOK), status.Code)
}

func TestSetDeviceServiceConditions(t *testing.T) {
	st, ev, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, err := st.device.Create(ctx, orgId, &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
		Status:   lo.ToPtr(domain.NewDeviceStatus()),
	}, nil)
	require.NoError(t, err)

	status := svc.SetDeviceServiceConditions(ctx, orgId, "foo", []domain.Condition{
		{Type: domain.ConditionTypeDeviceSpecValid, Status: domain.ConditionStatusFalse, Message: "bad spec"},
	})
	require.Equal(t, int32(http.StatusOK), status.Code)
	// SpecValid transitioning from absent to invalid emits a DeviceSpecInvalid event.
	require.Len(t, ev.created, 1)
}

func TestUpdateServiceSideDeviceStatus(t *testing.T) {
	_, _, svc := newTestHandler()
	device := domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
		Status:   lo.ToPtr(domain.NewDeviceStatus()),
	}
	// A freshly-constructed status always changes at least once as it's computed for the
	// first time (e.g. Summary.Status moves from its zero value to a concrete state).
	changed := svc.UpdateServiceSideDeviceStatus(context.Background(), uuid.New(), device)
	require.True(t, changed)
}

func TestReplaceDeviceStatus(t *testing.T) {
	t.Run("When device status is missing it should return bad request", func(t *testing.T) {
		_, _, svc := newTestHandler()
		device := domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}
		_, status := svc.ReplaceDeviceStatus(context.Background(), uuid.New(), "foo", device, true)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})

	t.Run("When replacing status for an existing device it should succeed", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, err := st.device.Create(ctx, orgId, &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{},
			Status:   lo.ToPtr(domain.NewDeviceStatus()),
		}, nil)
		require.NoError(t, err)

		incoming := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Status:   lo.ToPtr(domain.NewDeviceStatus()),
		}
		result, status := svc.ReplaceDeviceStatus(ctx, orgId, "foo", incoming, false)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
	})
}

func TestGetRenderedDevice(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, err := st.device.Create(ctx, orgId, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")}}, nil)
	require.NoError(t, err)
	// Non-agent caller with no KnownRenderedVersion: skips the healthchecker/rendered.Bus
	// global singletons entirely, exercising only the store round-trip.
	result, status := svc.GetRenderedDevice(ctx, orgId, "foo", domain.GetRenderedDeviceParams{})
	require.Equal(t, int32(http.StatusOK), status.Code)
	require.Equal(t, "foo", lo.FromPtr(result.Metadata.Name))
}
