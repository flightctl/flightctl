package device

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/identity"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestInitialization(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	config := config.NewDefault()
	config.Service.CertStore = tmpDir

	testCases := []struct {
		name       string
		setupMocks func(
			mockStatusManager *status.MockManager,
			mockSpecManager *spec.MockManager,
			mockReadWriter *fileio.MockReadWriter,
			mockHookManager *hook.MockManager,
			mockEnrollmentClient *client.MockEnrollment,
			mockSystemInfoManager *systeminfo.MockManager,
			mockLifecycleInitializer *lifecycle.MockInitializer,
			mockExecutor *executer.MockExecuter,
			mockIdentityProvider *identity.MockProvider,
		)
		expectedError error
	}{
		{
			name: "initialization enrolled no OS upgrade",
			setupMocks: func(
				mockStatusManager *status.MockManager,
				mockSpecManager *spec.MockManager,
				mockReadWriter *fileio.MockReadWriter,
				mockHookManager *hook.MockManager,
				_ *client.MockEnrollment,
				mockSystemInfoManager *systeminfo.MockManager,
				mockLifecycleInitializer *lifecycle.MockInitializer,
				mockExecutor *executer.MockExecuter,
				mockIdentityProvider *identity.MockProvider,
			) {
				gomock.InOrder(
					mockExecutor.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.4.2", "", 0),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockStatusManager.EXPECT().Collect(gomock.Any()).Return(nil),
					mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{}),
					mockLifecycleInitializer.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(nil),
					mockIdentityProvider.EXPECT().CreateManagementClient(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().IsOSUpdate().Return(false),
					mockSystemInfoManager.EXPECT().IsRebooted().Return(false),
					mockSpecManager.EXPECT().RenderedVersion(spec.Current).Return("1"),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockStatusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil),
				)
			},
		},
		{
			name: "initialization enrolled with OS upgrade",
			setupMocks: func(
				mockStatusManager *status.MockManager,
				mockSpecManager *spec.MockManager,
				mockReadWriter *fileio.MockReadWriter,
				mockHookManager *hook.MockManager,
				_ *client.MockEnrollment,
				mockSystemInfoManager *systeminfo.MockManager,
				mockLifecycleInitializer *lifecycle.MockInitializer,
				mockExecutor *executer.MockExecuter,
				mockIdentityProvider *identity.MockProvider,
			) {
				bootedOSVersion := "2.0.0"
				gomock.InOrder(
					mockExecutor.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.4.2", "", 0),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockStatusManager.EXPECT().Collect(gomock.Any()).Return(nil),
					mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{}),
					mockLifecycleInitializer.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(nil),
					mockIdentityProvider.EXPECT().CreateManagementClient(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().IsOSUpdate().Return(true),
					mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return(bootedOSVersion, true, nil),
					mockSystemInfoManager.EXPECT().IsRebooted().Return(false),
					mockSpecManager.EXPECT().RenderedVersion(spec.Current).Return("2"),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(true),
					mockStatusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil),
				)
			},
		},
		{
			name: "initialization not enrolled",
			setupMocks: func(
				mockStatusManager *status.MockManager,
				mockSpecManager *spec.MockManager,
				mockReadWriter *fileio.MockReadWriter,
				mockHookManager *hook.MockManager,
				mockEnrollmentClient *client.MockEnrollment,
				mockSystemInfoManager *systeminfo.MockManager,
				mockLifecycleInitializer *lifecycle.MockInitializer,
				mockExecutor *executer.MockExecuter,
				mockIdentityProvider *identity.MockProvider,
			) {
				gomock.InOrder(
					mockExecutor.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.4.2", "", 0),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockStatusManager.EXPECT().Collect(gomock.Any()).Return(nil),
					mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{}),
					mockLifecycleInitializer.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(nil),
					mockIdentityProvider.EXPECT().CreateManagementClient(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().IsOSUpdate().Return(false),
					mockSystemInfoManager.EXPECT().IsRebooted().Return(false),
					mockSpecManager.EXPECT().RenderedVersion(spec.Current).Return("2"),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockStatusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil),
				)
			},
		},
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusManager := status.NewMockManager(ctrl)
			mockSpecManager := spec.NewMockManager(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockHookManager := hook.NewMockManager(ctrl)
			mockEnrollmentClient := client.NewMockEnrollment(ctrl)
			mockSystemInfoManager := systeminfo.NewMockManager(ctrl)
			mockLifecycleInitializer := lifecycle.NewMockInitializer(ctrl)
			mockExecutor := executer.NewMockExecuter(ctrl)
			mockIdentityProvider := identity.NewMockProvider(ctrl)

			log := log.NewPrefixLogger("test")
			podmanClient := client.NewPodman(log, mockExecutor, mockReadWriter, util.NewPollConfig())
			systemdClient := client.NewSystemd(mockExecutor, v1beta1.RootUsername)

			b := &Bootstrap{
				statusManager:           mockStatusManager,
				specManager:             mockSpecManager,
				hookManager:             mockHookManager,
				lifecycle:               mockLifecycleInitializer,
				deviceReadWriter:        mockReadWriter,
				managementServiceConfig: &baseclient.Config{},
				systemInfoManager:       mockSystemInfoManager,
				podmanClient:            podmanClient,
				systemdClient:           systemdClient,
				identityProvider:        mockIdentityProvider,
				log:                     log,
			}

			ctx := context.TODO()

			tt.setupMocks(
				mockStatusManager,
				mockSpecManager,
				mockReadWriter,
				mockHookManager,
				mockEnrollmentClient,
				mockSystemInfoManager,
				mockLifecycleInitializer,
				mockExecutor,
				mockIdentityProvider,
			)

			err := b.Initialize(ctx)
			if tt.expectedError != nil {
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
		})
	}
}

func TestBootstrapCheckRollback(t *testing.T) {
	require := require.New(t)
	mockErr := errors.New("mock error")
	bootedOS := "1.0.0"
	desiredOS := "2.0.0"

	testCases := []struct {
		name          string
		setupMocks    func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager)
		expectedError error
	}{
		{
			name: "happy path",
			setupMocks: func(_ *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return(bootedOS, true, nil)
			},
		},
		{
			name: "successfully handles no rollback",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				gomock.InOrder(
					mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return(bootedOS, false, nil),
					mockSpecManager.EXPECT().OSVersion(spec.Desired).Return(desiredOS),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockSpecManager.EXPECT().IsRollingBack(gomock.Any()).Return(false, nil),
				)
			},
		},
		{
			name: "successfully handles rollback",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				gomock.InOrder(
					mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return(bootedOS, false, nil),
					mockSpecManager.EXPECT().OSVersion(spec.Desired).Return(desiredOS),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockSpecManager.EXPECT().IsRollingBack(gomock.Any()).Return(true, nil),
					mockSpecManager.EXPECT().Rollback(context.TODO(), gomock.Any()).Return(nil),
					mockSpecManager.EXPECT().RenderedVersion(spec.Desired).Return("2"),
					mockStatusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil),
				)
			},
		},
		{
			name: "error checking rollback status",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				gomock.InOrder(
					mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return(bootedOS, false, nil),
					mockSpecManager.EXPECT().OSVersion(spec.Desired).Return(desiredOS),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockSpecManager.EXPECT().IsRollingBack(gomock.Any()).Return(false, mockErr),
				)
			},
			expectedError: mockErr,
		},
		{
			name: "error during rollback",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				gomock.InOrder(
					mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return(bootedOS, false, nil),
					mockSpecManager.EXPECT().OSVersion(spec.Desired).Return(desiredOS),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockSpecManager.EXPECT().IsRollingBack(gomock.Any()).Return(true, nil),
					mockSpecManager.EXPECT().Rollback(context.TODO(), gomock.Any()).Return(mockErr),
				)
			},
			expectedError: mockErr,
		},
		{
			name: "error updating status",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				gomock.InOrder(
					mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return(bootedOS, false, nil),
					mockSpecManager.EXPECT().OSVersion(spec.Desired).Return(desiredOS),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, mockErr),
					mockSpecManager.EXPECT().IsRollingBack(gomock.Any()).Return(false, nil),
				)
			},
		},
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusManager := status.NewMockManager(ctrl)
			mockSpecManager := spec.NewMockManager(ctrl)

			b := &Bootstrap{
				statusManager: mockStatusManager,
				specManager:   mockSpecManager,
				log:           log.NewPrefixLogger("test"),
			}

			ctx := context.TODO()
			tt.setupMocks(mockStatusManager, mockSpecManager)

			err := b.checkRollback(ctx)
			if tt.expectedError != nil {
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
		})
	}
}

func TestEnsureBootedOS(t *testing.T) {
	require := require.New(t)
	specErr := errors.New("problem with spec")

	testCases := []struct {
		name          string
		setupMocks    func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager)
		expectedError error
	}{
		{
			name: "happy path - no OS update in progress",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().IsOSUpdate().Return(false)
			},
			expectedError: nil,
		},
		{
			name: "OS image reconciliation failure",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().IsOSUpdate().Return(true)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("", false, specErr)
			},
			expectedError: specErr,
		},
		{
			name: "OS image not reconciled triggers rollback",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().OSVersion(gomock.Any()).Return("desired-image")
				mockSpecManager.EXPECT().IsOSUpdate().Return(true)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("unexpected-booted-image", false, nil)
				mockSpecManager.EXPECT().IsRollingBack(gomock.Any()).Return(true, nil)
				mockSpecManager.EXPECT().Rollback(gomock.Any(), gomock.Any()).Return(nil)
				mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil)
				mockSpecManager.EXPECT().RenderedVersion(spec.Desired).Return("2")
				mockStatusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError: nil,
		},
		{
			name: "OS image reconciled",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().IsOSUpdate().Return(true)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("desired-image", true, nil)
			},
			expectedError: nil,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			log := log.NewPrefixLogger("test")
			mockStatusManager := status.NewMockManager(ctrl)
			mockSpecManager := spec.NewMockManager(ctrl)

			b := &Bootstrap{
				statusManager: mockStatusManager,
				specManager:   mockSpecManager,
				log:           log,
			}

			tt.setupMocks(mockStatusManager, mockSpecManager)

			err := b.ensureBootedOS(ctx)
			if tt.expectedError != nil {
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
		})
	}
}
