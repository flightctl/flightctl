package device

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
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
	svc := NewDeviceServiceHandler(st.device, st.catalog, st.fleet, ev, nil, "agent.example.com", logrus.New())
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

func TestHealthcheckDevices(t *testing.T) {
	t.Run("When the store succeeds it should delegate names to the store", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		names := []string{"d1", "d2"}
		require.NoError(t, svc.HealthcheckDevices(ctx, orgId, names))
		require.Equal(t, [][]string{names}, st.device.healthcheckCalls)
	})

	t.Run("When the store fails it should return the error", func(t *testing.T) {
		st, _, svc := newTestHandler()
		st.device.healthcheckErr = errors.New("db down")
		err := svc.HealthcheckDevices(context.Background(), uuid.New(), []string{"d1"})
		require.ErrorContains(t, err, "db down")
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
		_, status := svc.ReplaceDevice(ctx, orgId, "bar", device, nil, true, true)
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
		result, status := svc.ReplaceDevice(ctx, orgId, "foo", device, nil, true, true)
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

		_, status := ReplaceDeviceFromUntrusted(ctx, svc, orgId, "replace-untrusted", device, nil, true, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Nil(t, st.device.devices["replace-untrusted"].Metadata.Owner)
		require.Nil(t, st.device.devices["replace-untrusted"].Metadata.Generation)
	})

	t.Run("When ReplaceDeviceFromUntrusted updates an existing device it should preserve renderedVersion annotations", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		existing := domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr("rendered-device"),
				Annotations: &map[string]string{
					domain.DeviceAnnotationRenderedVersion: "1",
				},
			},
			Spec: &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		}
		_, err := st.device.Create(ctx, orgId, &existing, nil)
		require.NoError(t, err)

		updated := domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr("rendered-device"),
				Annotations: &map[string]string{
					domain.DeviceAnnotationRenderedVersion: "should-be-cleared-by-sanitize",
				},
			},
			Spec: &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img-updated"}},
		}
		result, status := ReplaceDeviceFromUntrusted(ctx, svc, orgId, "rendered-device", updated, nil, true, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Equal(t, "img-updated", result.Spec.Os.Image)
		require.Equal(t, "1", (*st.device.devices["rendered-device"].Metadata.Annotations)[domain.DeviceAnnotationRenderedVersion])
		require.Equal(t, "1", (*result.Metadata.Annotations)[domain.DeviceAnnotationRenderedVersion])
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

		_, status := svc.ReplaceDevice(ctx, orgId, "replace-trusted", device, nil, true, true)
		require.Equal(t, int32(http.StatusCreated), status.Code)
		require.Equal(t, "Fleet/f1", lo.FromPtr(st.device.devices["replace-trusted"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(st.device.devices["replace-trusted"].Metadata.Generation))
	})
}

// TestReplaceDeviceOwnership mirrors fleet.TestReplaceFleetOwnership: an external caller
// (enforceOwnership=true) must be denied a spec change on an owned device, while an
// internal/ResourceSync caller (enforceOwnership=false) must still be allowed through.
func TestReplaceDeviceOwnership(t *testing.T) {
	owner := "Fleet/f1"

	t.Run("When replacing an owned device with a changed spec it should return conflict", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		existing := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("owned-device"), Owner: lo.ToPtr(owner)},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		}
		_, err := st.device.Create(ctx, orgId, &existing, nil)
		require.NoError(t, err)

		updated := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("owned-device")},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img-updated"}},
		}
		_, status := svc.ReplaceDevice(ctx, orgId, "owned-device", updated, nil, true, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
		require.Equal(t, "img", st.device.devices["owned-device"].Spec.Os.Image)
	})

	t.Run("When enforceOwnership is false it should allow updating an owned device", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		existing := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("owned-device"), Owner: lo.ToPtr(owner)},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		}
		_, err := st.device.Create(ctx, orgId, &existing, nil)
		require.NoError(t, err)

		updated := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("owned-device")},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img-updated"}},
		}
		result, status := svc.ReplaceDevice(ctx, orgId, "owned-device", updated, nil, false, false)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Equal(t, "img-updated", st.device.devices["owned-device"].Spec.Os.Image)
		require.Equal(t, owner, lo.FromPtr(st.device.devices["owned-device"].Metadata.Owner))
	})

	t.Run("When replacing an unowned device with a changed spec it should allow the update", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		existing := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("unowned-device")},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		}
		_, err := st.device.Create(ctx, orgId, &existing, nil)
		require.NoError(t, err)

		updated := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("unowned-device")},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img-updated"}},
		}
		result, status := svc.ReplaceDevice(ctx, orgId, "unowned-device", updated, nil, true, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Equal(t, "img-updated", st.device.devices["unowned-device"].Spec.Os.Image)
	})
}

func TestReplaceDeviceSpec_PreservesOnlineWhenRecentlySeen(t *testing.T) {
	st, _, svc := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	lastSeen := time.Now().UTC().Add(-time.Minute)
	status := domain.NewDeviceStatus()
	status.Summary.Status = domain.DeviceSummaryStatusOnline
	status.Summary.Info = lo.ToPtr("Device's system resources are healthy.")
	status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusHealthy
	status.Applications = []domain.DeviceApplicationStatus{{
		Name:   "app1",
		Status: domain.ApplicationStatusRunning,
	}}
	status.LastSeen = lo.ToPtr(lastSeen)
	_, err := st.device.Create(ctx, orgId, &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("live")},
		Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "old"}},
		Status:   &status,
	}, nil)
	require.NoError(t, err)

	result, apiStatus := svc.ReplaceDeviceSpec(ctx, orgId, "live", nil, domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "new"}}, nil, nil)
	require.Equal(t, int32(http.StatusOK), apiStatus.Code)
	require.Equal(t, domain.DeviceSummaryStatusOnline, result.Status.Summary.Status)
	require.Equal(t, domain.ApplicationsSummaryStatusHealthy, result.Status.ApplicationsSummary.Status)
	require.NotNil(t, result.Status.LastSeen)
	require.Equal(t, "new", result.Spec.Os.Image)
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
		result, status := svc.PatchDevice(context.Background(), orgId, "foo", patch, true, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Equal(t, "newimg", result.Spec.Os.Image)
	})

	t.Run("When patching an immutable field it should return bad request", func(t *testing.T) {
		_, svc, orgId := setup(t)
		var value interface{} = "bar"
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/metadata/name", Value: &value},
		}
		_, status := svc.PatchDevice(context.Background(), orgId, "foo", patch, true, true)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})

	t.Run("When the device does not exist it should return not found", func(t *testing.T) {
		_, svc, orgId := setup(t)
		var value interface{} = "labelValue1"
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
		}
		_, status := svc.PatchDevice(context.Background(), orgId, "bar", patch, true, true)
		require.Equal(t, int32(http.StatusNotFound), status.Code)
		require.Equal(t, domain.StatusResourceNotFound("Device", "bar"), status)
	})
}

// TestPatchDeviceOwnership mirrors fleet.TestPatchFleetOwnership: an external caller
// (enforceOwnership=true) must be denied a spec-changing patch on an owned device, while an
// internal/ResourceSync caller (enforceOwnership=false) must still be allowed through.
func TestPatchDeviceOwnership(t *testing.T) {
	owner := "Fleet/f1"

	setup := func(t *testing.T) (*fakeStore, Service, uuid.UUID) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("owned-device"), Owner: lo.ToPtr(owner)},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		}
		_, err := st.device.Create(ctx, orgId, &device, nil)
		require.NoError(t, err)
		return st, svc, orgId
	}

	t.Run("When patching an owned device spec it should return conflict", func(t *testing.T) {
		st, svc, orgId := setup(t)
		var value interface{} = "img-updated"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/os/image", Value: &value}}

		_, status := svc.PatchDevice(context.Background(), orgId, "owned-device", patch, true, true)
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
		require.Equal(t, "img", st.device.devices["owned-device"].Spec.Os.Image)
	})

	t.Run("When enforceOwnership is false it should allow patching an owned device", func(t *testing.T) {
		st, svc, orgId := setup(t)
		var value interface{} = "img-updated"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/os/image", Value: &value}}

		result, status := svc.PatchDevice(context.Background(), orgId, "owned-device", patch, false, false)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result)
		require.Equal(t, "img-updated", st.device.devices["owned-device"].Spec.Os.Image)
		require.Equal(t, owner, lo.FromPtr(st.device.devices["owned-device"].Metadata.Owner))
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
		svc := NewDeviceServiceHandler(st.device, st.catalog, st.fleet, nil, nil, "agent.example.com", logrus.New())
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
// (fleet-owned) device, which requires looking up the owning fleet via fleet.Service.
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
	require.Equal(t, 1, st.fleet.getCalls, "expected UpdateServiceSideStatus to reach fleet.Service.GetFleet() for a managed device")
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

	t.Run("When updating an owned device it should not deny the change", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		st.device.devices["owned-device"] = &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("owned-device"), Owner: lo.ToPtr("Fleet/f1")},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		}

		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("owned-device")},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img-updated"}},
		}
		result, err := svc.UpdateDevice(ctx, orgId, "owned-device", device, nil)
		require.NoError(t, err)
		require.Equal(t, "img-updated", result.Spec.Os.Image)
		require.Equal(t, "Fleet/f1", lo.FromPtr(st.device.devices["owned-device"].Metadata.Owner))
	})

	t.Run("When fieldsToUnset includes owner it should clear the owner", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		st.device.devices["owned-device"] = &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr("owned-device"),
				Owner:       lo.ToPtr("Fleet/f1"),
				Annotations: &map[string]string{"k": "v"},
			},
			Spec: &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		}

		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("owned-device")},
			Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img-updated"}},
		}
		result, err := svc.UpdateDevice(ctx, orgId, "owned-device", device, []string{"owner"})
		require.NoError(t, err)
		require.Nil(t, result.Metadata.Owner)
		require.Nil(t, st.device.devices["owned-device"].Metadata.Owner)
		require.Equal(t, "v", (*st.device.devices["owned-device"].Metadata.Annotations)["k"])
	})
}

func TestDecommissionDevice(t *testing.T) {
	t.Run("When decommissioning a device it should set decom spec, lifecycle, clear owner and labels, and emit success event", func(t *testing.T) {
		st, ev, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		labels := map[string]string{"fleet": "f1"}
		_, err := st.device.Create(ctx, orgId, &domain.Device{
			Metadata: domain.ObjectMeta{
				Name:       lo.ToPtr("foo"),
				Owner:      lo.ToPtr("Fleet/f1"),
				Labels:     &labels,
				Generation: lo.ToPtr(int64(3)),
			},
			Spec:   &domain.DeviceSpec{},
			Status: lo.ToPtr(domain.NewDeviceStatus()),
		}, nil)
		require.NoError(t, err)

		decom := domain.DeviceDecommission{}
		result, status := svc.DecommissionDevice(ctx, orgId, "foo", decom)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result.Spec.Decommissioning)
		require.Equal(t, domain.DeviceLifecycleStatusDecommissioning, result.Status.Lifecycle.Status)
		require.Nil(t, result.Metadata.Owner)
		require.Nil(t, result.Metadata.Labels)
		require.Equal(t, int64(3), lo.FromPtr(result.Metadata.Generation))

		stored := st.device.devices["foo"]
		require.NotNil(t, stored.Spec.Decommissioning)
		require.Equal(t, domain.DeviceLifecycleStatusDecommissioning, stored.Status.Lifecycle.Status)
		require.Nil(t, stored.Metadata.Owner)
		require.Nil(t, stored.Metadata.Labels)
		require.Equal(t, int64(3), lo.FromPtr(stored.Metadata.Generation))

		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonDeviceDecommissioned, ev.created[0].Reason)
	})

	t.Run("When the device is already decommissioning it should return conflict without success event", func(t *testing.T) {
		st, ev, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		st.device.devices["foo"] = &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{Decommissioning: &domain.DeviceDecommission{}},
		}

		_, status := svc.DecommissionDevice(ctx, orgId, "foo", domain.DeviceDecommission{})
		require.Equal(t, int32(http.StatusConflict), status.Code)
		require.Equal(t, flterrors.ErrResourceVersionConflict.Error(), status.Message)
		require.Empty(t, ev.created)
	})

	t.Run("When the device does not exist it should return not found", func(t *testing.T) {
		_, ev, svc := newTestHandler()
		_, status := svc.DecommissionDevice(context.Background(), uuid.New(), "missing", domain.DeviceDecommission{})
		require.Equal(t, int32(http.StatusNotFound), status.Code)
		require.Empty(t, ev.created)
	})
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
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("foo"),
			Annotations: &map[string]string{
				domain.DeviceAnnotationRenderedSpecHash: "hash",
				domain.DeviceAnnotationRenderedVersion:  "1",
			},
		},
		Spec:   &domain.DeviceSpec{},
		Status: lo.ToPtr(domain.NewDeviceStatus()),
	}, nil)
	require.NoError(t, err)
	status := svc.UpdateRenderedDevice(ctx, orgId, "foo", "config", "apps", "hash", "", nil, false)
	require.Equal(t, int32(http.StatusOK), status.Code)
}

func TestServiceConditionsFromDevice(t *testing.T) {
	t.Run("When status is nil it should return nil", func(t *testing.T) {
		require.Nil(t, serviceConditionsFromDevice(&domain.Device{}))
	})

	t.Run("When status mixes agent and service conditions it should return only service types", func(t *testing.T) {
		device := &domain.Device{
			Status: &domain.DeviceStatus{
				Conditions: []domain.Condition{
					{Type: domain.ConditionTypeDeviceUpdating, Status: domain.ConditionStatusTrue},
					{Type: domain.ConditionTypeDeviceSpecValid, Status: domain.ConditionStatusFalse},
					{Type: domain.ConditionTypeDeviceMultipleOwners, Status: domain.ConditionStatusTrue},
				},
			},
		}
		got := serviceConditionsFromDevice(device)
		require.Len(t, got, 2)
		require.Equal(t, domain.ConditionTypeDeviceSpecValid, got[0].Type)
		require.Equal(t, domain.ConditionTypeDeviceMultipleOwners, got[1].Type)
	})
}

func TestSetDeviceServiceConditions(t *testing.T) {
	t.Run("When a service condition changes it should persist and emit events", func(t *testing.T) {
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
		stored := st.device.devices["foo"]
		require.NotNil(t, domain.FindStatusCondition(stored.Status.Conditions, domain.ConditionTypeDeviceSpecValid))
		require.Len(t, ev.created, 1)
	})

	t.Run("When a service condition is updated it should preserve agent-owned conditions", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		agentCond := domain.Condition{
			Type:    domain.ConditionTypeDeviceUpdating,
			Status:  domain.ConditionStatusTrue,
			Reason:  "Applying",
			Message: "applying spec",
		}
		serviceCond := domain.Condition{
			Type:    domain.ConditionTypeDeviceSpecValid,
			Status:  domain.ConditionStatusTrue,
			Reason:  "ok",
			Message: "ok",
		}
		_, err := st.device.Create(ctx, orgId, &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Status: &domain.DeviceStatus{
				Conditions: []domain.Condition{agentCond, serviceCond},
			},
		}, nil)
		require.NoError(t, err)

		status := svc.SetDeviceServiceConditions(ctx, orgId, "foo", []domain.Condition{
			{Type: domain.ConditionTypeDeviceSpecValid, Status: domain.ConditionStatusFalse, Message: "bad spec"},
		})
		require.Equal(t, int32(http.StatusOK), status.Code)

		stored := st.device.devices["foo"]
		gotAgent := domain.FindStatusCondition(stored.Status.Conditions, domain.ConditionTypeDeviceUpdating)
		require.Equal(t, &agentCond, gotAgent)
		gotService := domain.FindStatusCondition(stored.Status.Conditions, domain.ConditionTypeDeviceSpecValid)
		require.NotNil(t, gotService)
		require.Equal(t, domain.ConditionStatusFalse, gotService.Status)
		require.Equal(t, "bad spec", gotService.Message)
	})

	t.Run("When merge changes nothing it should skip write and not emit events", func(t *testing.T) {
		st, ev, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		cond := domain.Condition{
			Type:    domain.ConditionTypeDeviceSpecValid,
			Status:  domain.ConditionStatusTrue,
			Reason:  "ok",
			Message: "ok",
		}
		_, err := st.device.Create(ctx, orgId, &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Status: &domain.DeviceStatus{
				Conditions: []domain.Condition{cond},
			},
		}, nil)
		require.NoError(t, err)
		beforeRV := lo.FromPtr(st.device.devices["foo"].Metadata.ResourceVersion)

		status := svc.SetDeviceServiceConditions(ctx, orgId, "foo", []domain.Condition{cond})
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Equal(t, beforeRV, lo.FromPtr(st.device.devices["foo"].Metadata.ResourceVersion))
		require.Empty(t, ev.created)
	})

	t.Run("When the device does not exist it should return a not-found status", func(t *testing.T) {
		_, _, svc := newTestHandler()
		status := svc.SetDeviceServiceConditions(context.Background(), uuid.New(), "missing", []domain.Condition{
			{Type: domain.ConditionTypeDeviceSpecValid, Status: domain.ConditionStatusTrue},
		})
		require.Equal(t, int32(http.StatusNotFound), status.Code)
	})
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

	t.Run("When refreshLastSeen is true it should stamp LastSeen with the server's current time", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, err := st.device.Create(ctx, orgId, &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{},
			Status:   lo.ToPtr(domain.NewDeviceStatus()),
		}, nil)
		require.NoError(t, err)

		callerProvidedLastSeen := time.Now().Add(-1 * time.Hour)
		incomingStatus := domain.NewDeviceStatus()
		incomingStatus.LastSeen = lo.ToPtr(callerProvidedLastSeen)
		incoming := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Status:   &incomingStatus,
		}
		before := time.Now()
		result, status := svc.ReplaceDeviceStatus(ctx, orgId, "foo", incoming, true)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result.Status.LastSeen)
		require.False(t, result.Status.LastSeen.Before(before))
		require.WithinDuration(t, time.Now(), *result.Status.LastSeen, 5*time.Second)
	})

	t.Run("When refreshLastSeen is false it should preserve the caller-provided LastSeen", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, err := st.device.Create(ctx, orgId, &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Spec:     &domain.DeviceSpec{},
			Status:   lo.ToPtr(domain.NewDeviceStatus()),
		}, nil)
		require.NoError(t, err)

		callerProvidedLastSeen := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
		incomingStatus := domain.NewDeviceStatus()
		incomingStatus.LastSeen = lo.ToPtr(callerProvidedLastSeen)
		incoming := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
			Status:   &incomingStatus,
		}
		result, status := svc.ReplaceDeviceStatus(ctx, orgId, "foo", incoming, false)
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, result.Status.LastSeen)
		require.True(t, result.Status.LastSeen.Equal(callerProvidedLastSeen))
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

func TestReplaceDevicePackageModeOsReject(t *testing.T) {
	tests := []struct {
		name                string
		capabilities        *domain.DeviceCapabilities
		existingOs          *domain.DeviceOsSpec
		incomingOs          *domain.DeviceOsSpec
		owner               *string
		enforceOwnership    bool
		enforceCapabilities bool
		wantCode            int32
		wantMessage         string
	}{
		{
			name:                "When package-mode device gets os.image with enforceCapabilities it should return 400",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			incomingOs:          &domain.DeviceOsSpec{Image: "quay.io/img:latest"},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusBadRequest,
			wantMessage:         flterrors.ErrOsTargetNotSupportedOnPackageMode.Error(),
		},
		{
			name:                "When package-mode device gets os.image with enforceCapabilities=false it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			incomingOs:          &domain.DeviceOsSpec{Image: "quay.io/img:latest"},
			enforceOwnership:    true,
			enforceCapabilities: false,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device gets os.image with enforceOwnership=false and enforceCapabilities=true it should return 400",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			incomingOs:          &domain.DeviceOsSpec{Image: "quay.io/img:latest"},
			enforceOwnership:    false,
			enforceCapabilities: true,
			wantCode:            http.StatusBadRequest,
			wantMessage:         flterrors.ErrOsTargetNotSupportedOnPackageMode.Error(),
		},
		{
			name:                "When owned package-mode device gets os.image with enforceOwnership=true and enforceCapabilities=false it should return ownership conflict",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{Image: "quay.io/fleet-img:latest"},
			incomingOs:          &domain.DeviceOsSpec{Image: "quay.io/other-img:latest"},
			owner:               lo.ToPtr("Fleet/test"),
			enforceOwnership:    true,
			enforceCapabilities: false,
			wantCode:            http.StatusConflict,
			wantMessage:         flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(),
		},
		{
			name:                "When image-mode device gets os.image it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			incomingOs:          &domain.DeviceOsSpec{Image: "quay.io/img:latest"},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When device has nil capabilities and gets os.image it should allow",
			capabilities:        nil,
			incomingOs:          &domain.DeviceOsSpec{Image: "quay.io/img:latest"},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device gets no os spec it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			incomingOs:          nil,
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When capabilities has nil osMode and gets os.image it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: nil},
			incomingOs:          &domain.DeviceOsSpec{Image: "quay.io/img:latest"},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device retains the same os.image it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{Image: "quay.io/fleet-img:latest"},
			incomingOs:          &domain.DeviceOsSpec{Image: "quay.io/fleet-img:latest"},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device changes os.image it should return 400",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{Image: "quay.io/fleet-img:latest"},
			incomingOs:          &domain.DeviceOsSpec{Image: "quay.io/other-img:latest"},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusBadRequest,
			wantMessage:         flterrors.ErrOsTargetNotSupportedOnPackageMode.Error(),
		},
		{
			name:                "When package-mode device gets catalogItemRef it should return 400",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			incomingOs:          &domain.DeviceOsSpec{CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"}},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusBadRequest,
			wantMessage:         flterrors.ErrOsTargetNotSupportedOnPackageMode.Error(),
		},
		{
			name:                "When image-mode device gets catalogItemRef it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			incomingOs:          &domain.DeviceOsSpec{CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"}},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When device has nil capabilities and gets catalogItemRef it should allow",
			capabilities:        nil,
			incomingOs:          &domain.DeviceOsSpec{CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"}},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device retains the same catalogItemRef it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"}},
			incomingOs:          &domain.DeviceOsSpec{CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"}},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device changes catalogItemRef version it should return 400",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"}},
			incomingOs:          &domain.DeviceOsSpec{CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v2"}},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusBadRequest,
			wantMessage:         flterrors.ErrOsTargetNotSupportedOnPackageMode.Error(),
		},
		{
			name:                "When package-mode device clears existing os.image it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{Image: "quay.io/fleet-img:latest"},
			incomingOs:          nil,
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device clears existing catalogItemRef it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"}},
			incomingOs:          nil,
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, svc := newTestHandler()
			ctx := context.Background()
			orgId := uuid.New()

			seedCatalogItemsForOs(st, tt.existingOs)
			seedCatalogItemsForOs(st, tt.incomingOs)

			existing := domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("pkg-dev"), Owner: tt.owner},
				Spec:     &domain.DeviceSpec{Os: tt.existingOs},
				Status:   &domain.DeviceStatus{Capabilities: tt.capabilities},
			}
			_, err := st.device.Create(ctx, orgId, &existing, nil)
			require.NoError(t, err)

			incoming := domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("pkg-dev")},
				Spec:     &domain.DeviceSpec{Os: tt.incomingOs},
			}
			_, status := svc.ReplaceDevice(ctx, orgId, "pkg-dev", incoming, nil, tt.enforceOwnership, tt.enforceCapabilities)
			require.Equal(t, tt.wantCode, status.Code)
			if tt.wantMessage != "" {
				require.Equal(t, tt.wantMessage, status.Message)
			}
		})
	}
}

func TestPatchDevicePackageModeOsReject(t *testing.T) {
	tests := []struct {
		name         string
		capabilities *domain.DeviceCapabilities
		existingOs   *domain.DeviceOsSpec
		// patchOs seeds catalog items required for patch validation when the
		// patch introduces a catalogItemRef (typed; not reverse-parsed from the patch).
		patchOs             *domain.DeviceOsSpec
		owner               *string
		patch               domain.PatchRequest
		enforceOwnership    bool
		enforceCapabilities bool
		wantCode            int32
		wantMessage         string
	}{
		{
			name:                "When package-mode device gets patch adding os.image with enforceCapabilities it should return 400",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			patch:               patchAddOsImage("quay.io/img:latest"),
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusBadRequest,
			wantMessage:         flterrors.ErrOsTargetNotSupportedOnPackageMode.Error(),
		},
		{
			name:                "When package-mode device gets non-OS patch it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			patch:               patchAddLabel("env", "prod"),
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device already has os.image and gets non-OS patch it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{Image: "quay.io/fleet-img:latest"},
			patch:               patchAddLabel("env", "prod"),
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device gets patch adding os.image with enforceCapabilities=false it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			patch:               patchAddOsImage("quay.io/img:latest"),
			enforceOwnership:    true,
			enforceCapabilities: false,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device gets patch adding os.image with enforceOwnership=false and enforceCapabilities=true it should return 400",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			patch:               patchAddOsImage("quay.io/img:latest"),
			enforceOwnership:    false,
			enforceCapabilities: true,
			wantCode:            http.StatusBadRequest,
			wantMessage:         flterrors.ErrOsTargetNotSupportedOnPackageMode.Error(),
		},
		{
			name:         "When owned package-mode device gets OS patch with enforceOwnership=true and enforceCapabilities=false it should return ownership conflict",
			capabilities: &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:   &domain.DeviceOsSpec{Image: "quay.io/fleet-img:latest"},
			owner:        lo.ToPtr("Fleet/test"),
			patch: func() domain.PatchRequest {
				var value interface{} = "quay.io/other-img:latest"
				return domain.PatchRequest{{Op: "replace", Path: "/spec/os/image", Value: &value}}
			}(),
			enforceOwnership:    true,
			enforceCapabilities: false,
			wantCode:            http.StatusConflict,
			wantMessage:         flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(),
		},
		{
			name:         "When package-mode device gets patch adding catalogItemRef it should return 400",
			capabilities: &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			patchOs: &domain.DeviceOsSpec{
				CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"},
			},
			patch:               patchAddCatalogItemRef("cat", "os", "v1"),
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusBadRequest,
			wantMessage:         flterrors.ErrOsTargetNotSupportedOnPackageMode.Error(),
		},
		{
			name:                "When package-mode device already has catalogItemRef and gets non-OS patch it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{CatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"}},
			patch:               patchAddLabel("env", "prod"),
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
		{
			name:                "When package-mode device gets patch removing existing os it should allow",
			capabilities:        &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			existingOs:          &domain.DeviceOsSpec{Image: "quay.io/fleet-img:latest"},
			patch:               domain.PatchRequest{{Op: "remove", Path: "/spec/os"}},
			enforceOwnership:    true,
			enforceCapabilities: true,
			wantCode:            http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, svc := newTestHandler()
			ctx := context.Background()
			orgId := uuid.New()

			seedCatalogItemsForOs(st, tt.existingOs)
			seedCatalogItemsForOs(st, tt.patchOs)

			status := domain.NewDeviceStatus()
			status.Capabilities = tt.capabilities
			existing := domain.Device{
				Metadata: domain.ObjectMeta{
					Name:   lo.ToPtr("pkg-dev"),
					Owner:  tt.owner,
					Labels: &map[string]string{"env": "staging"},
				},
				Spec:   &domain.DeviceSpec{Os: tt.existingOs},
				Status: &status,
			}
			_, err := st.device.Create(ctx, orgId, &existing, nil)
			require.NoError(t, err)

			_, st2 := svc.PatchDevice(ctx, orgId, "pkg-dev", tt.patch, tt.enforceOwnership, tt.enforceCapabilities)
			require.Equal(t, tt.wantCode, st2.Code)
			if tt.wantMessage != "" {
				require.Equal(t, tt.wantMessage, st2.Message)
			}
		})
	}
}

func patchAddOsImage(image string) domain.PatchRequest {
	var value interface{} = map[string]interface{}{"image": image}
	return domain.PatchRequest{{Op: "add", Path: "/spec/os", Value: &value}}
}

func patchAddCatalogItemRef(catalog, item, version string) domain.PatchRequest {
	var value interface{} = map[string]interface{}{
		"catalogItemRef": map[string]interface{}{
			"catalog": catalog,
			"item":    item,
			"version": version,
		},
	}
	return domain.PatchRequest{{Op: "add", Path: "/spec/os", Value: &value}}
}

func patchAddLabel(key, value string) domain.PatchRequest {
	var v interface{} = value
	return domain.PatchRequest{{Op: "replace", Path: "/metadata/labels/" + key, Value: &v}}
}

func seedCatalogItemsForOs(st *fakeStore, os *domain.DeviceOsSpec) {
	if os == nil || os.CatalogItemRef == nil {
		return
	}
	ref := os.CatalogItemRef
	key := ref.Catalog + "/" + ref.Item
	existing, ok := st.catalog.items[key]
	if !ok {
		st.catalog.items[key] = makeCatalogItem(domain.CatalogItemTypeOS, ref.Version)
		return
	}
	for _, v := range existing.Spec.Versions {
		if v.Version == ref.Version {
			return
		}
	}
	existing.Spec.Versions = append(existing.Spec.Versions, domain.CatalogItemVersion{Version: ref.Version})
}

func makeCatalogItem(itemType domain.CatalogItemType, versions ...string) *domain.CatalogItem {
	var versionList []domain.CatalogItemVersion
	for _, v := range versions {
		versionList = append(versionList, domain.CatalogItemVersion{Version: v})
	}
	return &domain.CatalogItem{
		Spec: domain.CatalogItemSpec{
			Type:     itemType,
			Versions: versionList,
		},
	}
}

func makeAppSpec(t *testing.T, catalog, item, version string) domain.ApplicationProviderSpec {
	t.Helper()
	container := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    lo.ToPtr("myapp"),
	}
	err := container.FromCatalogItemRefApplicationProviderSpec(domain.CatalogItemRefApplicationProviderSpec{
		CatalogItemRef: domain.CatalogItemRefSpec{
			Catalog: catalog,
			Item:    item,
			Version: version,
		},
	})
	require.NoError(t, err)
	var spec domain.ApplicationProviderSpec
	err = spec.FromContainerApplication(container)
	require.NoError(t, err)
	return spec
}

func TestCreateDevice_CatalogItemRefValidation(t *testing.T) {
	t.Run("When OS refs a valid catalog item version it should succeed", func(t *testing.T) {
		st, _, svc := newTestHandler()
		st.catalog.items["mycat/myitem"] = makeCatalogItem(domain.CatalogItemTypeOS, "1.0.0", "2.0.0")
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec: &domain.DeviceSpec{
				Os: &domain.DeviceOsSpec{
					CatalogItemRef: &domain.CatalogItemRefSpec{
						Catalog: "mycat",
						Item:    "myitem",
						Version: "1.0.0",
					},
				},
			},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusCreated), status.Code)
	})

	t.Run("When OS refs a nonexistent catalog item it should return bad request", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec: &domain.DeviceSpec{
				Os: &domain.DeviceOsSpec{
					CatalogItemRef: &domain.CatalogItemRefSpec{
						Catalog: "mycat",
						Item:    "missing",
						Version: "1.0.0",
					},
				},
			},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
		require.Contains(t, status.Message, "mycat/missing")
	})

	t.Run("When OS refs a nonexistent version it should return bad request", func(t *testing.T) {
		st, _, svc := newTestHandler()
		st.catalog.items["mycat/myitem"] = makeCatalogItem(domain.CatalogItemTypeOS, "1.0.0")
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec: &domain.DeviceSpec{
				Os: &domain.DeviceOsSpec{
					CatalogItemRef: &domain.CatalogItemRefSpec{
						Catalog: "mycat",
						Item:    "myitem",
						Version: "9.9.9",
					},
				},
			},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
		require.Contains(t, status.Message, "9.9.9")
	})

	t.Run("When app refs a valid catalog item version it should succeed", func(t *testing.T) {
		st, _, svc := newTestHandler()
		st.catalog.items["mycat/myapp"] = makeCatalogItem(domain.CatalogItemTypeContainer, "1.0.0")
		ctx := context.Background()
		orgId := uuid.New()
		app := makeAppSpec(t, "mycat", "myapp", "1.0.0")
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec: &domain.DeviceSpec{
				Applications: &[]domain.ApplicationProviderSpec{app},
			},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusCreated), status.Code)
	})

	t.Run("When app refs a nonexistent catalog item it should return bad request", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		app := makeAppSpec(t, "mycat", "badapp", "1.0.0")
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec: &domain.DeviceSpec{
				Applications: &[]domain.ApplicationProviderSpec{app},
			},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
		require.Contains(t, status.Message, "mycat/badapp")
	})

	t.Run("When device has no catalog refs it should succeed without catalog lookup", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec:     &domain.DeviceSpec{},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusCreated), status.Code)
	})

	t.Run("When catalog store is nil it should skip validation", func(t *testing.T) {
		st := newFakeStore()
		ev := &fakeEvents{}
		svc := NewDeviceServiceHandler(st.device, nil, st.fleet, ev, nil, "agent.example.com", logrus.New())
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec: &domain.DeviceSpec{
				Os: &domain.DeviceOsSpec{
					CatalogItemRef: &domain.CatalogItemRefSpec{
						Catalog: "missing",
						Item:    "missing",
						Version: "1.0.0",
					},
				},
			},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusCreated), status.Code)
	})
}

func makeCatalogItemWithSchema(itemType domain.CatalogItemType, version string, configSchema *map[string]interface{}) *domain.CatalogItem {
	return &domain.CatalogItem{
		Spec: domain.CatalogItemSpec{
			Type: itemType,
			Versions: []domain.CatalogItemVersion{
				{
					Version:      version,
					ConfigSchema: configSchema,
					Channels:     []string{"stable"},
					References:   map[domain.CatalogItemArtifactType]string{},
				},
			},
		},
	}
}

func makeAppSpecWithEnvVars(t *testing.T, catalog, item, version string, envVars *map[string]string) domain.ApplicationProviderSpec {
	t.Helper()
	container := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    lo.ToPtr("myapp"),
		EnvVars: envVars,
	}
	err := container.FromCatalogItemRefApplicationProviderSpec(domain.CatalogItemRefApplicationProviderSpec{
		CatalogItemRef: domain.CatalogItemRefSpec{
			Catalog: catalog,
			Item:    item,
			Version: version,
		},
	})
	require.NoError(t, err)
	var spec domain.ApplicationProviderSpec
	err = spec.FromContainerApplication(container)
	require.NoError(t, err)
	return spec
}

func TestCreateDevice_ConfigSchemaValidation(t *testing.T) {
	requireEnvVarsSchema := &map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"envVars"},
		"properties": map[string]interface{}{
			"envVars": map[string]interface{}{"type": "object"},
		},
	}

	t.Run("When app conforms to configSchema it should succeed", func(t *testing.T) {
		st, _, svc := newTestHandler()
		st.catalog.items["mycat/myitem"] = makeCatalogItemWithSchema(domain.CatalogItemTypeContainer, "1.0.0", requireEnvVarsSchema)
		ctx := context.Background()
		orgId := uuid.New()
		app := makeAppSpecWithEnvVars(t, "mycat", "myitem", "1.0.0", &map[string]string{"KEY": "val"})
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec:     &domain.DeviceSpec{Applications: &[]domain.ApplicationProviderSpec{app}},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusCreated), status.Code)
	})

	t.Run("When app violates configSchema it should return bad request", func(t *testing.T) {
		st, _, svc := newTestHandler()
		st.catalog.items["mycat/myitem"] = makeCatalogItemWithSchema(domain.CatalogItemTypeContainer, "1.0.0", requireEnvVarsSchema)
		ctx := context.Background()
		orgId := uuid.New()
		app := makeAppSpecWithEnvVars(t, "mycat", "myitem", "1.0.0", nil)
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec:     &domain.DeviceSpec{Applications: &[]domain.ApplicationProviderSpec{app}},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
		require.Contains(t, status.Message, "configSchema")
	})

	t.Run("When catalog item has no configSchema it should succeed", func(t *testing.T) {
		st, _, svc := newTestHandler()
		st.catalog.items["mycat/myitem"] = makeCatalogItem(domain.CatalogItemTypeContainer, "1.0.0")
		ctx := context.Background()
		orgId := uuid.New()
		app := makeAppSpecWithEnvVars(t, "mycat", "myitem", "1.0.0", nil)
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec:     &domain.DeviceSpec{Applications: &[]domain.ApplicationProviderSpec{app}},
		}
		_, status := svc.CreateDevice(ctx, orgId, device)
		require.Equal(t, int32(http.StatusCreated), status.Code)
	})
}

func TestReplaceDevice_CatalogItemRefValidation(t *testing.T) {
	t.Run("When replacing with a valid catalog ref it should succeed", func(t *testing.T) {
		st, _, svc := newTestHandler()
		st.catalog.items["mycat/myitem"] = makeCatalogItem(domain.CatalogItemTypeOS, "1.0.0")
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec: &domain.DeviceSpec{
				Os: &domain.DeviceOsSpec{
					CatalogItemRef: &domain.CatalogItemRefSpec{
						Catalog: "mycat",
						Item:    "myitem",
						Version: "1.0.0",
					},
				},
			},
		}
		_, status := svc.ReplaceDevice(ctx, orgId, "dev1", device, nil, false, false)
		require.True(t, status.Code == http.StatusOK || status.Code == http.StatusCreated)
	})

	t.Run("When replacing with a nonexistent catalog item it should return bad request", func(t *testing.T) {
		_, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		device := domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec: &domain.DeviceSpec{
				Os: &domain.DeviceOsSpec{
					CatalogItemRef: &domain.CatalogItemRefSpec{
						Catalog: "mycat",
						Item:    "missing",
						Version: "1.0.0",
					},
				},
			},
		}
		_, status := svc.ReplaceDevice(ctx, orgId, "dev1", device, nil, false, false)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
	})
}

func TestPatchDevice_CatalogItemRefValidation(t *testing.T) {
	t.Run("When patching to add an invalid catalog ref it should return bad request", func(t *testing.T) {
		st, _, svc := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()

		existing := &domain.Device{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")},
			Spec:     &domain.DeviceSpec{},
			Status:   lo.ToPtr(domain.NewDeviceStatus()),
		}
		_, err := st.device.Create(ctx, orgId, existing, nil)
		require.NoError(t, err)

		var value interface{} = map[string]any{
			"catalogItemRef": map[string]any{
				"catalog": "mycat",
				"item":    "noexist",
				"version": "1.0.0",
			},
		}
		patch := domain.PatchRequest{
			{Op: "add", Path: "/spec/os", Value: &value},
		}
		_, status := svc.PatchDevice(ctx, orgId, "dev1", patch, false, false)
		require.Equal(t, int32(http.StatusBadRequest), status.Code)
		require.Contains(t, status.Message, "mycat/noexist")
	})
}
