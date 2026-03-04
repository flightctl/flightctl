package device

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	imagepruning "github.com/flightctl/flightctl/internal/agent/device/image_pruning"
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
			mockPruningManager *imagepruning.MockManager,
			mockPullConfigResolver *dependency.MockPullConfigResolver,
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
				mockPruningManager *imagepruning.MockManager,
				mockPullConfigResolver *dependency.MockPullConfigResolver,
			) {
				mockPullConfigResolver.EXPECT().BeforeUpdate(gomock.Any()).AnyTimes()
				mockPullConfigResolver.EXPECT().Cleanup().AnyTimes()
				nonRetryableHookError := errors.New("hook error")
				// IsCriticalAlert is called at the start of each syncDeviceSpec call
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
				// Set flexible expectations for methods called multiple times or in different contexts
				// These are called but may not be in the exact InOrder sequence due to error handling
				mockSpecManager.EXPECT().IsUpgrading().Return(true).AnyTimes()
				mockSpecManager.EXPECT().IsUpgrading().Return(false).AnyTimes()
				mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil).AnyTimes()
				mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil).AnyTimes()
				mockPrefetchManager.EXPECT().RegisterOCICollector(gomock.Any()).AnyTimes()
				mockSpecManager.EXPECT().IsOSUpdate().Return(false).AnyTimes()
				mockSpecManager.EXPECT().IsOSUpdatePending(gomock.Any()).Return(false, nil).AnyTimes()
				mockSpecManager.EXPECT().CheckPolicy(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

				// GetDesired, Read, and BeforeUpdate may be called multiple times if syncDeviceSpec is called again
				mockSpecManager.EXPECT().GetDesired(ctx).Return(desired, false, nil).AnyTimes()
				mockSpecManager.EXPECT().Read(spec.Current).Return(current, nil).AnyTimes()
				mockResourceManager.EXPECT().BeforeUpdate(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

				// Set specific expectations for calls that must happen with specific parameters
				// Use AnyTimes() for flexibility since execution order may vary with error handling
				mockPruningManager.EXPECT().RecordReferences(ctx, gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockPrefetchManager.EXPECT().BeforeUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockAppManager.EXPECT().BeforeUpdate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockHookManager.EXPECT().OnBeforeUpdating(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockHookManager.EXPECT().Sync(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockLifecycleManager.EXPECT().Sync(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockLifecycleManager.EXPECT().AfterUpdate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("", true, nil).AnyTimes()
				// Mock systemctl for boot success check (via systemd client)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "is-active", "boot-complete.target").Return("active\n", "", 0).AnyTimes()
				// OnAfterUpdating is called twice - once with error, once without during rollback
				mockHookManager.EXPECT().OnAfterUpdating(ctx, current.Spec, desired.Spec, false).Return(nonRetryableHookError).AnyTimes()
				mockHookManager.EXPECT().OnAfterUpdating(ctx, desired.Spec, current.Spec, false).Return(nil).AnyTimes()
				mockAppManager.EXPECT().AfterUpdate(ctx).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().SetUpgradeFailed(desired.Version()).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().IsOSRollback(gomock.Any()).Return(false, nil).AnyTimes()
				mockSpecManager.EXPECT().Rollback(gomock.Any()).Return(nil).AnyTimes()
				mockPrefetchManager.EXPECT().Cleanup().AnyTimes()
				// Upgrade should NOT be called when sync fails, but allow it for flexibility
				mockSpecManager.EXPECT().Upgrade(gomock.Any()).Return(nil).AnyTimes()
			},
		},
		{
			name:    "os rollback on non-retryable error after os upgrade",
			current: newVersionedDeviceWithOS("5", "quay.io/org/os:v1"),
			desired: newVersionedDeviceWithOS("6", "quay.io/org/os:v2"),
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
				mockPruningManager *imagepruning.MockManager,
				mockPullConfigResolver *dependency.MockPullConfigResolver,
			) {
				hookErr := errors.New("hook error")

				mockPullConfigResolver.EXPECT().BeforeUpdate(gomock.Any()).AnyTimes()
				mockPullConfigResolver.EXPECT().Cleanup().AnyTimes()
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
				mockResourceManager.EXPECT().BeforeUpdate(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().IsUpgrading().Return(true).AnyTimes()
				mockSpecManager.EXPECT().GetDesired(ctx).Return(desired, false, nil).AnyTimes()
				mockSpecManager.EXPECT().Read(spec.Current).Return(current, nil).AnyTimes()
				mockSpecManager.EXPECT().CheckPolicy(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().IsOSUpdate().Return(true).AnyTimes()
				mockSpecManager.EXPECT().IsOSUpdatePending(gomock.Any()).Return(false, nil).AnyTimes()
				mockSpecManager.EXPECT().SetUpgradeFailed(desired.Version()).Return(nil).AnyTimes()
				mockPrefetchManager.EXPECT().RegisterOCICollector(gomock.Any()).AnyTimes()
				mockPrefetchManager.EXPECT().BeforeUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockPrefetchManager.EXPECT().Cleanup().AnyTimes()
				mockPruningManager.EXPECT().RecordReferences(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockAppManager.EXPECT().BeforeUpdate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockAppManager.EXPECT().AfterUpdate(gomock.Any()).Return(nil).AnyTimes()
				mockHookManager.EXPECT().OnBeforeUpdating(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockHookManager.EXPECT().Sync(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockLifecycleManager.EXPECT().Sync(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockLifecycleManager.EXPECT().AfterUpdate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "is-active", "boot-complete.target").Return("active\n", "", 0).AnyTimes()

				// OS is reconciled (booted into desired imageB after reboot)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("", true, nil).AnyTimes()

				// forward sync: afterUpdate hook fails with non-retryable error
				mockHookManager.EXPECT().OnAfterUpdating(ctx, current.Spec, desired.Spec, false).Return(hookErr).AnyTimes()
				// rollback sync: afterUpdate hook succeeds
				mockHookManager.EXPECT().OnAfterUpdating(ctx, desired.Spec, current.Spec, false).Return(nil).AnyTimes()

				// OS rollback path
				mockSpecManager.EXPECT().IsOSRollback(gomock.Any()).Return(true, nil).AnyTimes()
				mockHookManager.EXPECT().OnBeforeRebooting(gomock.Any()).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().Rollback(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			},
		},
		{
			name:    "update deferred when CPU critical alert is firing",
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
				mockPruningManager *imagepruning.MockManager,
				mockPullConfigResolver *dependency.MockPullConfigResolver,
			) {
				mockPullConfigResolver.EXPECT().BeforeUpdate(gomock.Any()).AnyTimes()
				mockPullConfigResolver.EXPECT().Cleanup().AnyTimes()
				mockSpecManager.EXPECT().IsUpgrading().Return(true).AnyTimes()
				mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().GetDesired(ctx).Return(desired, false, nil).AnyTimes()
				mockSpecManager.EXPECT().Read(spec.Current).Return(current, nil).AnyTimes()
				mockResourceManager.EXPECT().BeforeUpdate(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(true).AnyTimes()
				mockSpecManager.EXPECT().IsOSRollback(gomock.Any()).Return(false, nil).AnyTimes()
				mockSpecManager.EXPECT().Rollback(gomock.Any()).Return(nil).AnyTimes()
				mockPrefetchManager.EXPECT().Cleanup().AnyTimes()
				mockHookManager.EXPECT().Sync(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockLifecycleManager.EXPECT().Sync(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockLifecycleManager.EXPECT().AfterUpdate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("", true, nil).AnyTimes()
				mockHookManager.EXPECT().OnAfterUpdating(gomock.Any(), gomock.Any(), gomock.Any(), false).Return(nil).AnyTimes()
				mockAppManager.EXPECT().AfterUpdate(gomock.Any()).Return(nil).AnyTimes()
				mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil).AnyTimes()
			},
		},
		{
			name:    "update deferred when memory critical alert is firing",
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
				mockPruningManager *imagepruning.MockManager,
				mockPullConfigResolver *dependency.MockPullConfigResolver,
			) {
				mockPullConfigResolver.EXPECT().BeforeUpdate(gomock.Any()).AnyTimes()
				mockPullConfigResolver.EXPECT().Cleanup().AnyTimes()
				mockSpecManager.EXPECT().IsUpgrading().Return(true).AnyTimes()
				mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().GetDesired(ctx).Return(desired, false, nil).AnyTimes()
				mockSpecManager.EXPECT().Read(spec.Current).Return(current, nil).AnyTimes()
				mockResourceManager.EXPECT().BeforeUpdate(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).DoAndReturn(func(monitorType resource.MonitorType) bool {
					return monitorType == resource.MemoryMonitorType
				}).AnyTimes()
				mockSpecManager.EXPECT().IsOSRollback(gomock.Any()).Return(false, nil).AnyTimes()
				mockSpecManager.EXPECT().Rollback(gomock.Any()).Return(nil).AnyTimes()
				mockPrefetchManager.EXPECT().Cleanup().AnyTimes()
				mockHookManager.EXPECT().Sync(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockLifecycleManager.EXPECT().Sync(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockLifecycleManager.EXPECT().AfterUpdate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("", true, nil).AnyTimes()
				mockHookManager.EXPECT().OnAfterUpdating(gomock.Any(), gomock.Any(), gomock.Any(), false).Return(nil).AnyTimes()
				mockAppManager.EXPECT().AfterUpdate(gomock.Any()).Return(nil).AnyTimes()
				mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil).AnyTimes()
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// mocks
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
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
			mockPruningManager := imagepruning.NewMockManager(ctrl)
			mockPullConfigResolver := dependency.NewMockPullConfigResolver(ctrl)
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
				mockPruningManager,
				mockPullConfigResolver,
			)

			// setup
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			tempDir := t.TempDir()
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
			)

			podmanClient := client.NewPodman(log, mockExec, readWriter, testutil.NewPollConfig())
			systemdClient := client.NewSystemd(mockExec, v1beta1.RootUsername)
			mockWatcher := spec.NewMockWatcher(ctrl)
			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return podmanClient, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return readWriter, nil
			}
			consoleManager := console.NewManager(mockRouterService, deviceName, "root", mockExec, mockWatcher, log)
			appController := applications.NewController(podmanFactory, nil, mockAppManager, rwFactory, log, "2025-01-01T00:00:00Z")
			statusManager := status.NewManager(deviceName, log)
			statusManager.SetClient(mockManagementClient)
			configController := config.NewController(readWriter, log)

			agent := Agent{
				log:                    log,
				systemdClient:          systemdClient,
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
				pruningManager:         mockPruningManager,
				pullConfigResolver:     mockPullConfigResolver,
			}

			// initial sync
			agent.syncDeviceSpec(ctx)
			// resync the previously reconciled state
			// Note: The mocks above already include expectations for the second syncDeviceSpec call
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
			name:    "rollback returns sync error",
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
			wantSyncErr: errors.New("sync error"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			// mocks
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockOSClient := os.NewMockClient(ctrl)
			mockManagementClient := client.NewMockManagement(ctrl)
			mockHookManager := hook.NewMockManager(ctrl)
			tc.setupMocks(
				tc.current,
				tc.desired,
				mockManagementClient,
			)

			// setup
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
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
				hookManager:   mockHookManager,
				specManager:   specManager,
			}

			mockSync := &mockSync{
				desiredVersion: tc.desired.Version(),
				currentVersion: tc.current.Version(),
				wantErr:        tc.wantSyncErr,
			}

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

			currentVersionOnDisk, err := specManager.Read(spec.Current)
			require.NoError(err)
			require.Equal(tc.current.Version(), currentVersionOnDisk.Version(), "current version should be unchanged on disk")
			desiredVersionOnDisk, err := specManager.Read(spec.Desired)
			require.NoError(err)
			require.Equal(tc.current.Version(), desiredVersionOnDisk.Version(), "desired should match current on disk")
		})
	}
}

func TestRollbackDeviceCallOrder(t *testing.T) {
	deviceName := "test-device"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSpecManager := spec.NewMockManager(ctrl)
	mockManagementClient := client.NewMockManagement(ctrl)

	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)
	statusManager := status.NewManager(deviceName, log)
	statusManager.SetClient(mockManagementClient)

	current := newVersionedDevice("5")
	desired := newVersionedDevice("6")

	var callOrder []string
	mockManagementClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil)
	mockSpecManager.EXPECT().IsOSRollback(gomock.Any()).Return(false, nil)
	mockSpecManager.EXPECT().Rollback(gomock.Any()).DoAndReturn(func(_ context.Context, _ ...spec.RollbackOption) error {
		callOrder = append(callOrder, "rollback")
		return nil
	})

	mockHookManager := hook.NewMockManager(ctrl)

	agent := Agent{
		log:           log,
		specManager:   mockSpecManager,
		statusManager: statusManager,
		hookManager:   mockHookManager,
	}

	mockSyncFn := func(ctx context.Context, c, d *v1beta1.Device) error {
		callOrder = append(callOrder, "syncFn")
		return nil
	}

	err := agent.rollbackDevice(ctx, current, desired, mockSyncFn)
	require.NoError(err)
	require.Equal([]string{"rollback", "syncFn"}, callOrder)
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

func newVersionedDeviceWithOS(version, osImage string) *v1beta1.Device {
	device := newVersionedDevice(version)
	device.Spec.Os = &v1beta1.DeviceOsSpec{Image: osImage}
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
