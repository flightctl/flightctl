package status

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestInvalidateLastStatus_CausesNextSyncToPush verifies that when InvalidateLastStatus is called,
// the next Sync pushes status again (UpdateDeviceStatus called twice total). We call InvalidateLastStatus
// ourselves here because this test is about the StatusManager's behavior. The condition under which
// it gets called (GetRenderedDevice returns 200 with ConflictPaused) is tested in spec/publisher_test.go
// (TestDevicePublisher_ConflictPausedCallback).
func TestInvalidateLastStatus_CausesNextSyncToPush(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deviceName := "test-device"
	ctx := context.Background()
	mockClient := client.NewMockManagement(ctrl)
	mockExporter := NewMockExporter(ctrl)

	mgr := NewManager(deviceName, log.NewPrefixLogger(""))
	mgr.SetClient(mockClient)
	mgr.RegisterStatusExporter(mockExporter)

	mockExporter.EXPECT().Status(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
	mockClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil).Times(2)

	require.NoError(mgr.Sync(ctx))
	mgr.InvalidateLastStatus()
	require.NoError(mgr.Sync(ctx))
}

// populateRealisticStatus fills a DeviceStatus with realistic data across
// multiple fields: applications, config, OS, system info, resources, summary,
// integrity, and conditions. This ensures diff tests prove that only the
// changed fields appear in PATCH ops.
func populateRealisticStatus(s *v1beta1.DeviceStatus) {
	s.Applications = []v1beta1.DeviceApplicationStatus{
		{Name: "web-server", Status: v1beta1.ApplicationStatusRunning},
		{Name: "monitoring-agent", Status: v1beta1.ApplicationStatusRunning},
		{Name: "log-collector", Status: v1beta1.ApplicationStatusPreparing},
	}
	s.ApplicationsSummary = v1beta1.DeviceApplicationsSummaryStatus{
		Status: v1beta1.ApplicationsSummaryStatusHealthy,
	}
	s.Config = v1beta1.DeviceConfigStatus{
		RenderedVersion: "v42",
	}
	s.Os = v1beta1.DeviceOsStatus{
		Image:       "quay.io/flightctl/rhel-bootc:9.4",
		ImageDigest: "sha256:abc123def456",
	}
	s.SystemInfo = v1beta1.DeviceSystemInfo{
		Architecture:    "aarch64",
		OperatingSystem: "linux",
	}
	s.Resources.Cpu = v1beta1.DeviceResourceStatusHealthy
	s.Resources.Disk = v1beta1.DeviceResourceStatusHealthy
	s.Resources.Memory = v1beta1.DeviceResourceStatusHealthy
	s.Summary.Status = v1beta1.DeviceSummaryStatusOnline
	s.Summary.Info = nil
	s.Integrity = v1beta1.DeviceIntegrityStatus{
		Status: v1beta1.DeviceIntegrityStatusUnknown,
	}
	s.Conditions = []v1beta1.Condition{
		{Type: "Available", Status: "True"},
	}
}

func TestUpdateCritical_SingleCPUAlertChange(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deviceName := "test-device"
	ctx := context.Background()
	mockClient := client.NewMockManagement(ctrl)

	mgr := NewManager(deviceName, log.NewPrefixLogger(""))
	mgr.SetClient(mockClient)

	// Pre-populate device status with realistic data
	populateRealisticStatus(mgr.device.Status)

	// Register a critical exporter that only changes CPU resource status
	exporter := NewMockExporter(ctrl)
	mgr.RegisterCriticalExporter(exporter)

	exporter.EXPECT().Status(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, s *v1beta1.DeviceStatus, _ ...CollectorOpt) error {
			s.Resources.Cpu = v1beta1.DeviceResourceStatusCritical
			s.Summary.Status = v1beta1.DeviceSummaryStatusError
			s.Summary.Info = lo.ToPtr("Critical resource alert: CPU")
			return nil
		},
	)

	var capturedPatch v1beta1.PatchRequest
	mockClient.EXPECT().PatchDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Do(
		func(_ context.Context, _ string, patch v1beta1.PatchRequest, _ ...any) {
			capturedPatch = patch
		},
	).Return(nil)

	require.NoError(mgr.UpdateCritical(ctx))

	// Only resources and summary should be in the patch — NOT applications,
	// config, OS, systemInfo, integrity, conditions, etc.
	patchPaths := make(map[string]bool)
	for _, op := range capturedPatch {
		require.Equal(v1beta1.Replace, op.Op, "all ops should be replace")
		patchPaths[op.Path] = true
	}
	require.True(patchPaths["/status/resources"], "patch must include /status/resources")
	require.True(patchPaths["/status/summary"], "patch must include /status/summary")
	require.False(patchPaths["/status/applications"], "applications must NOT be in patch")
	require.False(patchPaths["/status/applicationsSummary"], "applicationsSummary must NOT be in patch")
	require.False(patchPaths["/status/config"], "config must NOT be in patch")
	require.False(patchPaths["/status/os"], "os must NOT be in patch")
	require.False(patchPaths["/status/systemInfo"], "systemInfo must NOT be in patch")
	require.False(patchPaths["/status/integrity"], "integrity must NOT be in patch")
	require.False(patchPaths["/status/conditions"], "conditions must NOT be in patch")
}

func TestUpdateCritical_MultipleAlertChanges(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deviceName := "test-device"
	ctx := context.Background()
	mockClient := client.NewMockManagement(ctrl)

	mgr := NewManager(deviceName, log.NewPrefixLogger(""))
	mgr.SetClient(mockClient)
	populateRealisticStatus(mgr.device.Status)

	exporter := NewMockExporter(ctrl)
	mgr.RegisterCriticalExporter(exporter)

	exporter.EXPECT().Status(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, s *v1beta1.DeviceStatus, _ ...CollectorOpt) error {
			s.Resources.Cpu = v1beta1.DeviceResourceStatusCritical
			s.Resources.Memory = v1beta1.DeviceResourceStatusWarning
			s.Summary.Status = v1beta1.DeviceSummaryStatusError
			s.Summary.Info = lo.ToPtr("Critical resource alert: CPU, Memory")
			return nil
		},
	)

	var capturedPatch v1beta1.PatchRequest
	mockClient.EXPECT().PatchDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Do(
		func(_ context.Context, _ string, patch v1beta1.PatchRequest, _ ...any) {
			capturedPatch = patch
		},
	).Return(nil)

	require.NoError(mgr.UpdateCritical(ctx))

	patchPaths := make(map[string]bool)
	for _, op := range capturedPatch {
		patchPaths[op.Path] = true
	}
	require.True(patchPaths["/status/resources"], "resources changed")
	require.True(patchPaths["/status/summary"], "summary changed")
	require.False(patchPaths["/status/applications"], "applications unchanged")
	require.False(patchPaths["/status/config"], "config unchanged")
	require.False(patchPaths["/status/os"], "os unchanged")
}

func TestUpdateCritical_NoDiffNoPatch(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deviceName := "test-device"
	ctx := context.Background()
	mockClient := client.NewMockManagement(ctrl)

	mgr := NewManager(deviceName, log.NewPrefixLogger(""))
	mgr.SetClient(mockClient)
	populateRealisticStatus(mgr.device.Status)

	exporter := NewMockExporter(ctrl)
	mgr.RegisterCriticalExporter(exporter)

	// Exporter changes nothing
	exporter.EXPECT().Status(gomock.Any(), gomock.Any()).Return(nil)
	// PatchDeviceStatus must NOT be called

	require.NoError(mgr.UpdateCritical(ctx))
}

func TestUpdateCritical_SummaryChangeOnly(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deviceName := "test-device"
	ctx := context.Background()
	mockClient := client.NewMockManagement(ctrl)

	mgr := NewManager(deviceName, log.NewPrefixLogger(""))
	mgr.SetClient(mockClient)
	populateRealisticStatus(mgr.device.Status)

	exporter := NewMockExporter(ctrl)
	mgr.RegisterCriticalExporter(exporter)

	// Only change summary, not resources
	exporter.EXPECT().Status(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, s *v1beta1.DeviceStatus, _ ...CollectorOpt) error {
			s.Summary.Status = v1beta1.DeviceSummaryStatusDegraded
			s.Summary.Info = lo.ToPtr("Degraded resource alert: Disk")
			return nil
		},
	)

	var capturedPatch v1beta1.PatchRequest
	mockClient.EXPECT().PatchDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Do(
		func(_ context.Context, _ string, patch v1beta1.PatchRequest, _ ...any) {
			capturedPatch = patch
		},
	).Return(nil)

	require.NoError(mgr.UpdateCritical(ctx))

	patchPaths := make(map[string]bool)
	for _, op := range capturedPatch {
		patchPaths[op.Path] = true
	}
	require.True(patchPaths["/status/summary"], "summary should be in patch")
	require.False(patchPaths["/status/resources"], "resources must NOT be in patch — unchanged")
	require.False(patchPaths["/status/applications"], "applications must NOT be in patch")
}

func TestUpdateCritical_NoCriticalExporters(t *testing.T) {
	require := require.New(t)

	deviceName := "test-device"
	ctx := context.Background()

	mgr := NewManager(deviceName, log.NewPrefixLogger(""))
	require.NoError(mgr.UpdateCritical(ctx))
}

func TestCriticalChangeNotifier(t *testing.T) {
	require := require.New(t)

	ch := make(chan struct{}, 1)
	mgr := NewManager("test-device", log.NewPrefixLogger(""), WithCriticalCh(ch))

	notifier := mgr.CriticalChangeNotifier()
	notifier()

	select {
	case <-ch:
	default:
		t.Fatal("expected signal on critical change channel")
	}

	// Coalescing: multiple calls produce at most one pending signal
	notifier()
	notifier()
	notifier()

	select {
	case <-ch:
	default:
		t.Fatal("expected at least one coalesced signal")
	}
	select {
	case <-ch:
		t.Fatal("expected only one coalesced signal")
	default:
	}

	require.True(true)
}

func TestCriticalChangeNotifier_NoCh(t *testing.T) {
	mgr := NewManager("test-device", log.NewPrefixLogger(""))
	notifier := mgr.CriticalChangeNotifier()
	// Should be a no-op — must not panic
	notifier()
}

func TestDiffStatusToPatch_RealisticOnlyResourcesChanged(t *testing.T) {
	require := require.New(t)

	before := &v1beta1.DeviceStatus{}
	populateRealisticStatus(before)

	after := &v1beta1.DeviceStatus{}
	populateRealisticStatus(after)
	after.Resources.Cpu = v1beta1.DeviceResourceStatusCritical

	ops, err := diffStatusToPatch("/status", before, after)
	require.NoError(err)
	require.Len(ops, 1, "only resources field changed")
	require.Equal("/status/resources", ops[0].Path)
	require.Equal(v1beta1.Replace, ops[0].Op)
}

func TestUpdateCritical_UpdatesLastStatusPreventingRedundantSync(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deviceName := "test-device"
	ctx := context.Background()
	mockClient := client.NewMockManagement(ctrl)

	mgr := NewManager(deviceName, log.NewPrefixLogger(""))
	mgr.SetClient(mockClient)
	populateRealisticStatus(mgr.device.Status)

	// Register a regular exporter that reproduces the same state as the
	// critical exporter will set (resource alert). This simulates what
	// happens when the periodic Sync collects the same resource status
	// that was already pushed via PATCH.
	regularExporter := NewMockExporter(ctrl)
	mgr.RegisterStatusExporter(regularExporter)

	criticalExporter := NewMockExporter(ctrl)
	mgr.RegisterCriticalExporter(criticalExporter)

	// Step 1: UpdateCritical — critical exporter sets CPU to Critical
	criticalExporter.EXPECT().Status(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, s *v1beta1.DeviceStatus, _ ...CollectorOpt) error {
			s.Resources.Cpu = v1beta1.DeviceResourceStatusCritical
			s.Summary.Status = v1beta1.DeviceSummaryStatusError
			s.Summary.Info = lo.ToPtr("Critical resource alert: CPU")
			return nil
		},
	)
	mockClient.EXPECT().PatchDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil)
	require.NoError(mgr.UpdateCritical(ctx))

	// Step 2: Sync — the regular exporter writes the SAME resource/summary
	// state. Because UpdateCritical already updated lastStatus, the Sync
	// should see no diff and NOT call UpdateDeviceStatus.
	regularExporter.EXPECT().Status(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, s *v1beta1.DeviceStatus, _ ...CollectorOpt) error {
			populateRealisticStatus(s)
			s.Resources.Cpu = v1beta1.DeviceResourceStatusCritical
			s.Summary.Status = v1beta1.DeviceSummaryStatusError
			s.Summary.Info = lo.ToPtr("Critical resource alert: CPU")
			return nil
		},
	)
	// UpdateDeviceStatus must NOT be called — lastStatus already matches
	require.NoError(mgr.Sync(ctx))
}

func TestDiffStatusToPatch_NoChanges(t *testing.T) {
	require := require.New(t)

	s := &v1beta1.DeviceStatus{}
	populateRealisticStatus(s)

	ops, err := diffStatusToPatch("/status", s, s)
	require.NoError(err)
	require.Empty(ops, "identical status should produce no ops")
}
