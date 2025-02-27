package device

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/sosreport"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestSync(t *testing.T) {
	require := require.New(t)
	deviceName := "test-device"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testCases := []struct {
		name       string
		current    *v1alpha1.Device
		desired    *v1alpha1.Device
		setupMocks func(
			currentSpec *v1alpha1.Device,
			desiredSpec *v1alpha1.Device,
			mockOSClient *os.MockClient,
			mockManagementClient *client.MockManagement,
			mockSystemClient *client.MockSystem,
			mockExec *executer.MockExecuter,
			mockRouterService *console.MockRouterServiceClient,
			mockResourceManager *resource.MockManager,
			mockSystemdManager *systemd.MockManager,
			mockHookManager *hook.MockManager,
			mockAppManager *applications.MockManager,
			mockLifecycleManager *lifecycle.MockManager,
			mockSosreportManager *sosreport.MockManager,
		)
	}{
		{
			name:    "sync with error and rollback with error",
			current: newVersionedDevice("0"),
			desired: newVersionedDevice("1"),
			setupMocks: func(
				current *v1alpha1.Device,
				desired *v1alpha1.Device,
				mockOSClient *os.MockClient,
				mockManagementClient *client.MockManagement,
				mockSystemClient *client.MockSystem,
				mockExec *executer.MockExecuter,
				mockRouterService *console.MockRouterServiceClient,
				mockResourceManager *resource.MockManager,
				mockSystemdManager *systemd.MockManager,
				mockHookManager *hook.MockManager,
				mockAppManager *applications.MockManager,
				mockLifecycleManager *lifecycle.MockManager,
				mockSosreportManager *sosreport.MockManager,
			) {
				nonRetryableHookError := errors.New("hook error")
				gomock.InOrder(
					mockSystemClient.EXPECT().BootID().Return("boot-id"),
					mockManagementClient.EXPECT().GetRenderedDevice(ctx, deviceName, gomock.Any()).Return(desired, 200, nil),
					mockManagementClient.EXPECT().UpdateDeviceStatus(ctx, deviceName, gomock.Any()).Return(nil),
					mockAppManager.EXPECT().BeforeUpdate(ctx, desired.Spec).Return(nil),
					mockHookManager.EXPECT().OnBeforeUpdating(ctx, current.Spec, desired.Spec).Return(nil),
					mockManagementClient.EXPECT().UpdateDeviceStatus(ctx, deviceName, gomock.Any()).Return(nil),
					mockManagementClient.EXPECT().UpdateDeviceStatus(ctx, deviceName, gomock.Any()).Return(nil),
					mockHookManager.EXPECT().Sync(current.Spec, desired.Spec).Return(nil),
					mockResourceManager.EXPECT().ResetAlertDefaults().Return(nil),
					mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil),
					mockLifecycleManager.EXPECT().Sync(ctx, current.Spec, desired.Spec).Return(nil),
					mockSosreportManager.EXPECT().Sync(ctx, gomock.Any(), gomock.Any()).Return(nil),
					mockLifecycleManager.EXPECT().AfterUpdate(ctx, current.Spec, desired.Spec).Return(nil),
					mockOSClient.EXPECT().Status(ctx).Return(&os.Status{}, nil),
					mockHookManager.EXPECT().OnAfterUpdating(ctx, current.Spec, desired.Spec, false).Return(nonRetryableHookError),
					mockManagementClient.EXPECT().UpdateDeviceStatus(ctx, deviceName, gomock.Any()).Return(nil),
					//
					// rollback switch current and desired spec ordering
					//
					mockAppManager.EXPECT().BeforeUpdate(ctx, current.Spec).Return(nil),
					mockHookManager.EXPECT().OnBeforeUpdating(ctx, desired.Spec, current.Spec).Return(nil),
					mockHookManager.EXPECT().Sync(desired.Spec, current.Spec).Return(nil),
					mockResourceManager.EXPECT().ResetAlertDefaults().Return(nil),
					mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil),
					mockLifecycleManager.EXPECT().Sync(ctx, desired.Spec, current.Spec).Return(nil),
					mockSosreportManager.EXPECT().Sync(ctx, gomock.Any(), gomock.Any()).Return(nil),
					mockLifecycleManager.EXPECT().AfterUpdate(ctx, desired.Spec, current.Spec).Return(nil),
					mockOSClient.EXPECT().Status(ctx).Return(&os.Status{}, nil),
					mockHookManager.EXPECT().OnAfterUpdating(ctx, desired.Spec, current.Spec, false).Return(nonRetryableHookError),
					mockManagementClient.EXPECT().UpdateDeviceStatus(ctx, deviceName, gomock.Any()).Return(nil),
					mockManagementClient.EXPECT().UpdateDeviceStatus(ctx, deviceName, gomock.Any()).Return(nil),
					//
					// resync steady state current 0 desired 0
					//
					mockManagementClient.EXPECT().GetRenderedDevice(ctx, deviceName, gomock.Any()).Return(desired, 200, nil),
					mockAppManager.EXPECT().BeforeUpdate(ctx, current.Spec).Return(nil),
					mockHookManager.EXPECT().OnBeforeUpdating(ctx, current.Spec, current.Spec).Return(nil),
					mockHookManager.EXPECT().Sync(current.Spec, current.Spec).Return(nil),
					mockResourceManager.EXPECT().ResetAlertDefaults().Return(nil),
					mockSystemdManager.EXPECT().EnsurePatterns(gomock.Any()).Return(nil),
					mockLifecycleManager.EXPECT().Sync(ctx, current.Spec, current.Spec).Return(nil),
					mockSosreportManager.EXPECT().Sync(ctx, gomock.Any(), gomock.Any()).Return(nil),
					mockLifecycleManager.EXPECT().AfterUpdate(ctx, current.Spec, current.Spec).Return(nil),
					mockOSClient.EXPECT().Status(ctx).Return(&os.Status{}, nil),
					mockHookManager.EXPECT().OnAfterUpdating(ctx, current.Spec, current.Spec, false).Return(nonRetryableHookError),
				)
			},
		},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// mocks
			ctrl := gomock.NewController(t)
			mockOSClient := os.NewMockClient(ctrl)
			mockManagementClient := client.NewMockManagement(ctrl)
			mockSystemClient := client.NewMockSystem(ctrl)
			mockExec := executer.NewMockExecuter(ctrl)
			mockRouterService := console.NewMockRouterServiceClient(ctrl)
			mockResourceManager := resource.NewMockManager(ctrl)
			mockSystemdManager := systemd.NewMockManager(ctrl)
			mockHookManager := hook.NewMockManager(ctrl)
			mockAppManager := applications.NewMockManager(ctrl)
			mockLifecycleManager := lifecycle.NewMockManager(ctrl)
			mockSosreportManager := sosreport.NewMockManager(ctrl)
			tc.setupMocks(
				tc.current,
				tc.desired,
				mockOSClient,
				mockManagementClient,
				mockSystemClient,
				mockExec,
				mockRouterService,
				mockResourceManager,
				mockSystemdManager,
				mockHookManager,
				mockAppManager,
				mockLifecycleManager,
				mockSosreportManager,
			)

			// setup
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)
			dataDir := filepath.Join(tmpDir, "data")
			backoff := wait.Backoff{
				Steps: 1,
			}

			podmanClient := client.NewPodman(log, mockExec, readWriter, backoff)
			policyManager := policy.NewManager(log)
			consoleController := console.NewController(mockRouterService, deviceName, mockExec, log)
			appController := applications.NewController(podmanClient, mockAppManager, readWriter, log)
			statusManager := status.NewManager(deviceName, mockSystemClient, log)
			statusManager.SetClient(mockManagementClient)
			configController := config.NewController(readWriter, log)
			resourceController := resource.NewController(log, mockResourceManager)
			specManager := spec.NewManager(
				deviceName,
				dataDir,
				policyManager,
				readWriter,
				mockOSClient,
				backoff,
				log,
			)

			specManager.SetClient(mockManagementClient)
			err := specManager.Initialize(ctx)
			require.NoError(err)

			agent := Agent{
				log:                    log,
				deviceWriter:           readWriter,
				specManager:            specManager,
				policyManager:          policyManager,
				statusManager:          statusManager,
				appManager:             mockAppManager,
				applicationsController: appController,
				hookManager:            mockHookManager,
				consoleController:      consoleController,
				configController:       configController,
				resourceController:     resourceController,
				systemdManager:         mockSystemdManager,
				lifecycleManager:       mockLifecycleManager,
				commandsManager:        mockSosreportManager,
			}

			// initial sync
			agent.syncDeviceSpec(ctx)
			// resync the previously reconciled state
			agent.syncDeviceSpec(ctx)
			// TODO add validations
		})
	}

}

func newVersionedDevice(version string) *v1alpha1.Device {
	deice := &v1alpha1.Device{
		Metadata: v1alpha1.ObjectMeta{
			Annotations: lo.ToPtr(map[string]string{
				v1alpha1.DeviceAnnotationRenderedVersion: version,
			}),
		},
	}
	deice.Spec = &v1alpha1.DeviceSpec{}
	return deice
}
