package device

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestInitialization(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusManager := status.NewMockManager(ctrl)
	mockSpecManager := spec.NewMockManager(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockHookManager := hook.NewMockManager(ctrl)

	b := &Bootstrap{
		statusManager:           mockStatusManager,
		specManager:             mockSpecManager,
		hookManager:             mockHookManager,
		deviceReadWriter:        mockReadWriter,
		managementServiceConfig: &client.Config{},
		log:                     log.NewPrefixLogger("test"),
	}

	ctx := context.TODO()

	t.Run("initialization", func(t *testing.T) {
		mockSpecManager.EXPECT().Ensure().Return(nil)
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).Times(3)
		mockSpecManager.EXPECT().SetClient(gomock.Any())
		mockStatusManager.EXPECT().SetClient(gomock.Any())
		mockSpecManager.EXPECT().Read(spec.Desired).Return(&v1alpha1.RenderedDeviceSpec{}, nil)
		currentDeviceSpec := &v1alpha1.RenderedDeviceSpec{}
		mockSpecManager.EXPECT().Read(spec.Current).Return(currentDeviceSpec, nil)
		mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil)
		mockSpecManager.EXPECT().RenderedVersion(gomock.Any()).Return("1")
		mockSpecManager.EXPECT().IsUpgrading().Return(false)
		mockStatusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil)
		require.NoError(b.Initialize(ctx))
	})
}

func TestBootstrapCheckRollback(t *testing.T) {
	require := require.New(t)
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
	bootedOS := "1.0.0"
	desiredOS := "2.0.0"

	t.Run("happy path", func(t *testing.T) {
		err := b.checkRollback(ctx, desiredOS, desiredOS)
		require.NoError(err)
	})

	t.Run("successfully handles no rollback", func(t *testing.T) {
		isRollingBack := false

		mockStatusManager.EXPECT().Update(ctx, gomock.Any()).Return(nil, nil)
		mockSpecManager.EXPECT().IsRollingBack(ctx).Return(isRollingBack, nil)

		err := b.checkRollback(ctx, bootedOS, desiredOS)
		require.NoError(err)
	})

	t.Run("successfully handles rollback", func(t *testing.T) {
		isRollingBack := true

		mockStatusManager.EXPECT().Update(ctx, gomock.Any()).Return(nil, nil)
		mockSpecManager.EXPECT().IsRollingBack(ctx).Return(isRollingBack, nil)
		mockSpecManager.EXPECT().Rollback().Return(nil)

		err := b.checkRollback(ctx, bootedOS, desiredOS)
		require.NoError(err)
	})

	t.Run("error checking rollback status", func(t *testing.T) {
		isRollingBack := false

		mockStatusManager.EXPECT().Update(ctx, gomock.Any()).Return(nil, nil)
		mockSpecManager.EXPECT().IsRollingBack(ctx).Return(isRollingBack, errors.New("rollback check failed"))

		err := b.checkRollback(ctx, bootedOS, desiredOS)
		require.Error(err)
	})

	t.Run("error during rollback", func(t *testing.T) {
		isRollingBack := true

		mockStatusManager.EXPECT().Update(ctx, gomock.Any()).Return(nil, nil)
		mockSpecManager.EXPECT().IsRollingBack(ctx).Return(isRollingBack, nil)
		mockSpecManager.EXPECT().Rollback().Return(errors.New("rollback failed"))

		err := b.checkRollback(ctx, bootedOS, desiredOS)
		require.Error(err)
	})

	t.Run("error updating status", func(t *testing.T) {
		isRollingBack := false

		mockStatusManager.EXPECT().Update(ctx, gomock.Any()).Return(nil, errors.New("update failed"))
		mockSpecManager.EXPECT().IsRollingBack(ctx).Return(isRollingBack, nil)

		err := b.checkRollback(ctx, bootedOS, desiredOS)
		require.NoError(err)
	})
}

func TestEnsureBootedOS(t *testing.T) {
	require := require.New(t)
	desiredSpec := newTestDesiredSpec("desired-image", "1")
	specErr := errors.New("problem with spec")

	testCases := []struct {
		name          string
		setupMocks    func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager)
		desired       *v1alpha1.RenderedDeviceSpec
		expectedError error
	}{
		{
			name: "no desired OS image specified",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
			},
			desired:       &v1alpha1.RenderedDeviceSpec{},
			expectedError: nil,
		},
		{
			name: "no OS update in progress",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().IsOSUpdate().Return(false, nil)
			},
			desired:       desiredSpec,
			expectedError: nil,
		},
		{
			name: "OS image reconciliation failure",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().IsOSUpdate().Return(true, nil)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("", false, specErr)
			},
			desired:       desiredSpec,
			expectedError: specErr,
		},
		{
			name: "OS image not reconciled - triggers rollback",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().IsOSUpdate().Return(true, nil)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("unexpected-booted-image", false, nil)
				mockSpecManager.EXPECT().IsRollingBack(gomock.Any()).Return(true, nil)
				mockSpecManager.EXPECT().Rollback().Return(nil)
				mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil)
			},
			desired:       desiredSpec,
			expectedError: nil,
		},
		{
			name: "OS image reconciled",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().IsOSUpdate().Return(true, nil)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("desired-image", true, nil)
			},
			desired:       desiredSpec,
			expectedError: nil,
		},
	}

	for _, tt := range testCases {
		tt := tt
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

			err := b.ensureBootedOS(ctx, tt.desired)
			if tt.expectedError != nil {
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
		})
	}
}

func newTestDesiredSpec(image, version string) *v1alpha1.RenderedDeviceSpec {
	return &v1alpha1.RenderedDeviceSpec{
		Os: &v1alpha1.DeviceOSSpec{
			Image: image,
		},
		RenderedVersion: version,
	}
}
