package device

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/spec/audit"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSync(t *testing.T) {
	deviceName := "test-device"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testCases := []struct {
		name       string
		current    *v1beta1.Device
		desired    *v1beta1.Device
		setupMocks func(
			currentSpec *v1beta1.Device,
			desiredSpec *v1beta1.Device,
			mockOSClient *os.MockClient,
			mockManagementClient *client.MockManagement,
			mockSystemInfoManager *systeminfo.MockManager,
			mockExec *executer.MockExecuter,
			mockRouterService *console.MockRouterServiceClient,
			mockResourceManager *resource.MockManager,
			mockSystemdManager *systemd.MockManager,
			mockHookManager *hook.MockManager,
			mockAppManager *applications.MockManager,
			mockLifecycleManager *lifecycle.MockManager,
			mockPolicyManager *policy.MockManager,
			mockSpecManager *spec.MockManager,
			mockPrefetchManager *dependency.MockPrefetchManager,
			mockOSManager *os.MockManager,
		)
	}{
		{
			name:    "sync with error and rollback with error",
			current: newVersionedDevice("0"),
			desired: newVersionedDevice("1"),
			setupMocks: func(
				current *v1beta1.Device,
				desired *v1beta1.Device,
				mockOSClient *os.MockClient,
				mockManagementClient *client.MockManagement,
				mockSystemInfoManager *systeminfo.MockManager,
				mockExec *executer.MockExecuter,
				mockRouterService *console.MockRouterServiceClient,
				mockResourceManager *resource.MockManager,
				mockSystemdManager *systemd.MockManager,
				mockHookManager *hook.MockManager,
				mockAppManager *applications.MockManager,
				mockLifecycleManager *lifecycle.MockManager,
				mockPolicyManager *policy.MockManager,
				mockSpecManager *spec.MockManager,
				mockPrefetchManager *dependency.MockPrefetchManager,
				mockOSManager *os.MockManager,
			) {
				nonRetryableHookError := errors.New("hook error")
				gomock.InOrder(
					mockSpecManager.EXPECT().GetDesired(ctx).Return(desired, false, nil),
					mockSpecManager.EXPECT().Read(spec.Current).Return(current, nil),
					mockResourceManager.EXPECT().BeforeUpdate(ctx, desired.Spec).Return(nil),
					mockSpecManager.EXPECT().CheckPolicy(ctx, policy.Download, desired.Version()).Return(nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(true),
					mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil),
					mockPrefetchManager.EXPECT().RegisterOCICollector(gomock.Any()),
					mockSpecManager.EXPECT().IsOSUpdate().Return(false),
					mockPrefetchManager.EXPECT().BeforeUpdate(ctx, current.Spec, desired.Spec).Return(nil),
					mockAppManager.EXPECT().BeforeUpdate(ctx, desired.Spec).Return(nil),
					mockHookManager.EXPECT().OnBeforeUpdating(ctx, current.Spec, desired.Spec).Return(nil),
					mockSpecManager.EXPECT().CheckPolicy(ctx, policy.Update, desired.Version()).Return(nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(true),
					mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(true),
					mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil),
					mockHookManager.EXPECT().Sync(current.Spec, desired.Spec).Return(nil),
					mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil),
					mockLifecycleManager.EXPECT().Sync(ctx, current.Spec, desired.Spec).Return(nil),
					mockLifecycleManager.EXPECT().AfterUpdate(ctx, current.Spec, desired.Spec).Return(nil),
					mockSpecManager.EXPECT().CheckOsReconciliation(ctx).Return("", true, nil),
					mockHookManager.EXPECT().OnAfterUpdating(ctx, current.Spec, desired.Spec, false).Return(nonRetryableHookError),
					//
					// rollback switch current and desired spec ordering
					//
					mockSpecManager.EXPECT().IsUpgrading().Return(true),
					mockSpecManager.EXPECT().SetUpgradeFailed(desired.Version()).Return(nil),
					mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil),
					mockSpecManager.EXPECT().Rollback(ctx).Return(nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockHookManager.EXPECT().Sync(desired.Spec, current.Spec).Return(nil),
					mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil),
					mockLifecycleManager.EXPECT().Sync(ctx, desired.Spec, current.Spec).Return(nil),
					mockLifecycleManager.EXPECT().AfterUpdate(ctx, desired.Spec, current.Spec).Return(nil),
					mockSpecManager.EXPECT().CheckOsReconciliation(ctx).Return("", true, nil),
					mockHookManager.EXPECT().OnAfterUpdating(ctx, desired.Spec, current.Spec, false).Return(nil),
					mockAppManager.EXPECT().AfterUpdate(ctx).Return(nil),
					mockPrefetchManager.EXPECT().Cleanup(),
					mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil),
					//
					// resync steady state current 0 desired 0
					//
					mockSpecManager.EXPECT().GetDesired(ctx).Return(desired, false, nil),
					mockSpecManager.EXPECT().Read(spec.Current).Return(current, nil),
					mockResourceManager.EXPECT().BeforeUpdate(ctx, desired.Spec).Return(nil),
					mockSpecManager.EXPECT().CheckPolicy(ctx, policy.Download, desired.Version()).Return(nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockPrefetchManager.EXPECT().RegisterOCICollector(gomock.Any()),
					mockSpecManager.EXPECT().IsOSUpdate().Return(false),
					mockPrefetchManager.EXPECT().BeforeUpdate(ctx, current.Spec, current.Spec).Return(nil),
					mockAppManager.EXPECT().BeforeUpdate(ctx, current.Spec).Return(nil),
					mockHookManager.EXPECT().OnBeforeUpdating(ctx, current.Spec, current.Spec).Return(nil),
					mockSpecManager.EXPECT().CheckPolicy(ctx, policy.Update, desired.Version()).Return(nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockHookManager.EXPECT().Sync(current.Spec, current.Spec).Return(nil),
					mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil),
					mockLifecycleManager.EXPECT().Sync(ctx, current.Spec, current.Spec).Return(nil),
					mockLifecycleManager.EXPECT().AfterUpdate(ctx, current.Spec, current.Spec).Return(nil),
					mockSpecManager.EXPECT().CheckOsReconciliation(ctx).Return("", true, nil),
					mockHookManager.EXPECT().OnAfterUpdating(ctx, current.Spec, current.Spec, false).Return(nil),
					mockAppManager.EXPECT().AfterUpdate(ctx).Return(nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockPrefetchManager.EXPECT().Cleanup(),
				)
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// mocks
			ctrl := gomock.NewController(t)
			mockOSClient := os.NewMockClient(ctrl)
			mockManagementClient := client.NewMockManagement(ctrl)
			mockSystemInfoManager := systeminfo.NewMockManager(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			mockRouterService := console.NewMockRouterServiceClient(ctrl)
			mockResourceManager := resource.NewMockManager(ctrl)
			mockSystemdManager := systemd.NewMockManager(ctrl)
			mockHookManager := hook.NewMockManager(ctrl)
			mockAppManager := applications.NewMockManager(ctrl)
			mockLifecycleManager := lifecycle.NewMockManager(ctrl)
			mockPolicyManager := policy.NewMockManager(ctrl)
			mockSpecManager := spec.NewMockManager(ctrl)
			mockPrefetchManager := dependency.NewMockPrefetchManager(ctrl)
			mockOSManager := os.NewMockManager(ctrl)
			tc.setupMocks(
				tc.current,
				tc.desired,
				mockOSClient,
				mockManagementClient,
				mockSystemInfoManager,
				mockExec,
				mockRouterService,
				mockResourceManager,
				mockSystemdManager,
				mockHookManager,
				mockAppManager,
				mockLifecycleManager,
				mockPolicyManager,
				mockSpecManager,
				mockPrefetchManager,
				mockOSManager,
			)

			// setup
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)

			podmanClient := client.NewPodman(log, mockExec, readWriter, testutil.NewPollConfig())
			mockWatcher := spec.NewMockWatcher(ctrl)
			consoleManager := console.NewManager(mockRouterService, deviceName, mockExec, mockWatcher, log)
			appController := applications.NewController(podmanClient, mockAppManager, readWriter, log, "2025-01-01T00:00:00Z")
			statusManager := status.NewManager(deviceName, log)
			statusManager.SetClient(mockManagementClient)
			configController := config.NewController(readWriter, log)

			agent := Agent{
				log:                    log,
				deviceWriter:           readWriter,
				specManager:            mockSpecManager,
				policyManager:          mockPolicyManager,
				statusManager:          statusManager,
				appManager:             mockAppManager,
				applicationsController: appController,
				hookManager:            mockHookManager,
				consoleManager:         consoleManager,
				configController:       configController,
				resourceManager:        mockResourceManager,
				systemdManager:         mockSystemdManager,
				lifecycleManager:       mockLifecycleManager,
				prefetchManager:        mockPrefetchManager,
				osManager:              mockOSManager,
			}

			// initial sync
			agent.syncDeviceSpec(ctx)
			// resync the previously reconciled state
			agent.syncDeviceSpec(ctx)
			// TODO add validations
		})
	}

}

func TestRollbackDevice(t *testing.T) {
	deviceName := "test-device"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testCases := []struct {
		name       string
		current    *v1beta1.Device
		desired    *v1beta1.Device
		setupMocks func(
			currentSpec *v1beta1.Device,
			desiredSpec *v1beta1.Device,
			mockManagementClient *client.MockManagement,
		)
		wantSyncErr error
	}{
		{
			name:    "rollback from one version",
			current: newVersionedDevice("0"),
			desired: newVersionedDevice("1"),
			setupMocks: func(
				current *v1beta1.Device,
				desired *v1beta1.Device,
				mockManagementClient *client.MockManagement,
			) {
				gomock.InOrder(
					mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil),
				)
			},
		},
		{
			name:    "rollback multiple versions",
			current: newVersionedDevice("1"),
			desired: newVersionedDevice("5"),
			setupMocks: func(
				current *v1beta1.Device,
				desired *v1beta1.Device,
				mockManagementClient *client.MockManagement,
			) {
				gomock.InOrder(
					mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil),
				)
			},
		},
		{
			name:    "rollback multiple versions with sync error",
			current: newVersionedDevice("1"),
			desired: newVersionedDevice("5"),
			setupMocks: func(
				current *v1beta1.Device,
				desired *v1beta1.Device,
				mockManagementClient *client.MockManagement,
			) {
				gomock.InOrder(
					// mockSystemInfoManager.EXPECT().BootID().Return("boot-id"),
					mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil),
				)
			},
			wantSyncErr: errors.New("sync error"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			// mocks
			ctrl := gomock.NewController(t)
			mockOSClient := os.NewMockClient(ctrl)
			mockManagementClient := client.NewMockManagement(ctrl)
			tc.setupMocks(
				tc.current,
				tc.desired,
				mockManagementClient,
			)

			// setup
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)
			dataDir := filepath.Join(tmpDir, "data")

			policyManager := policy.NewManager(log)
			statusManager := status.NewManager(deviceName, log)
			statusManager.SetClient(mockManagementClient)
			mockAuditLogger := audit.NewMockLogger(ctrl)
			mockAuditLogger.EXPECT().Close().Return(nil).AnyTimes()
			mockAuditLogger.EXPECT().LogEvent(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			specManager := spec.NewManager(
				"test-device",
				dataDir,
				policyManager,
				readWriter,
				mockOSClient,
				poll.NewConfig(time.Second, 1.5),
				func() error { return nil },
				mockAuditLogger,
				log,
			)

			err := specManager.Initialize(ctx)
			require.NoError(err)
			currentBytes, err := json.Marshal(tc.current)
			require.NoError(err)
			err = readWriter.WriteFile(filepath.Join(dataDir, "current.json"), currentBytes, fileio.DefaultFilePermissions)
			require.NoError(err)
			desiredBytes, err := json.Marshal(tc.desired)
			require.NoError(err)
			err = readWriter.WriteFile(filepath.Join(dataDir, "desired.json"), desiredBytes, fileio.DefaultFilePermissions)
			require.NoError(err)

			agent := Agent{
				log:           log,
				statusManager: statusManager,
				specManager:   specManager,
			}

			mockSync := &mockSync{
				desiredVersion: tc.desired.Version(),
				currentVersion: tc.current.Version(),
				wantErr:        tc.wantSyncErr,
			}

			// verify the spec on disk is as expected
			currentVersionOnDisk, err := specManager.Read(spec.Current)
			require.NoError(err)
			require.Equal(tc.current.Version(), currentVersionOnDisk.Version())
			desiredVersionOnDisk, err := specManager.Read(spec.Desired)
			require.NoError(err)
			require.Equal(tc.desired.Version(), desiredVersionOnDisk.Version())

			err = agent.rollbackDevice(ctx, tc.current, tc.desired, mockSync.sync)
			if tc.wantSyncErr != nil {
				require.Error(err)
				require.Equal(tc.wantSyncErr.Error(), err.Error())
				return
			}
			require.NoError(err)

			// verify cached the desired version should be the same as the current version
			desiredCacheVersion := specManager.RenderedVersion(spec.Desired)
			require.Equal(tc.current.Version(), desiredCacheVersion, "cached desired should be the same as current")
			currentCacheVersion := specManager.RenderedVersion(spec.Current)
			require.Equal(tc.current.Version(), currentCacheVersion, "cached current version should be unchanged")

			// verify the spec on disk is as expected
			currentVersionOnDisk, err = specManager.Read(spec.Current)
			require.NoError(err)
			require.Equal(tc.current.Version(), currentVersionOnDisk.Version(), "current version should be unchanged on disk after rollback")
			desiredVersionOnDisk, err = specManager.Read(spec.Desired)
			require.NoError(err)
			require.Equal(tc.current.Version(), desiredVersionOnDisk.Version(), "desired version should be the same as current after rollback")

		})
	}
}

func newVersionedDevice(version string) *v1beta1.Device {
	device := &v1beta1.Device{
		Metadata: v1beta1.ObjectMeta{
			Annotations: lo.ToPtr(map[string]string{
				v1beta1.DeviceAnnotationRenderedVersion: version,
			}),
		},
	}
	device.Spec = &v1beta1.DeviceSpec{}
	return device
}

type mockSync struct {
	desiredVersion string
	currentVersion string
	wantErr        error
}

func (m *mockSync) sync(ctx context.Context, currentSpec *v1beta1.Device, desiredSpec *v1beta1.Device) error {
	if m.wantErr != nil {
		return m.wantErr
	}

	if desiredSpec.Version() != m.currentVersion {
		return errors.New("current version mismatch")
	}
	if currentSpec.Version() != m.desiredVersion {
		return errors.New("desired version mismatch")
	}
	return nil
}
