package device

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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
	"k8s.io/apimachinery/pkg/util/wait"
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
			mockManagement *client.MockManagement,
		)
		expectedError error
	}{
		{
			name: "initialization enrolled no OS upgrade",
			setupMocks: func(
				mockStatusManager *status.MockManager,
				mockSpecManager *spec.MockManager,
				_ *fileio.MockReadWriter,
				_ *hook.MockManager,
				_ *client.MockEnrollment,
				mockSystemInfoManager *systeminfo.MockManager,
				mockLifecycleInitializer *lifecycle.MockInitializer,
				mockExecutor *executer.MockExecuter,
				mockIdentityProvider *identity.MockProvider,
				mockManagement *client.MockManagement,
			) {
				gomock.InOrder(
					mockExecutor.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.4.2", "", 0),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockStatusManager.EXPECT().Collect(gomock.Any()).Return(nil),
					mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{}),
					mockLifecycleInitializer.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(nil),
					mockIdentityProvider.EXPECT().CreateManagementClient(gomock.Any(), gomock.Any()).Return(mockManagement, nil),
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					// ensurePostEnrollmentHooks uses waitEnrollmentHooksReadyFn (set in test body)
					mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(false),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockSpecManager.EXPECT().GetRollbackInfo().Return(spec.RollbackInfo{}, nil),
					mockSystemInfoManager.EXPECT().IsRebooted().Return(false),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockSpecManager.EXPECT().GetRollbackInfo().Return(spec.RollbackInfo{}, nil),
					mockSpecManager.EXPECT().RenderedVersion(spec.Current).Return("1"),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil),
				)
			},
		},
		{
			name: "initialization enrolled with OS upgrade",
			setupMocks: func(
				mockStatusManager *status.MockManager,
				mockSpecManager *spec.MockManager,
				_ *fileio.MockReadWriter,
				_ *hook.MockManager,
				_ *client.MockEnrollment,
				mockSystemInfoManager *systeminfo.MockManager,
				mockLifecycleInitializer *lifecycle.MockInitializer,
				mockExecutor *executer.MockExecuter,
				mockIdentityProvider *identity.MockProvider,
				mockManagement *client.MockManagement,
			) {
				bootedOSVersion := "2.0.0"
				gomock.InOrder(
					mockExecutor.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.4.2", "", 0),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockStatusManager.EXPECT().Collect(gomock.Any()).Return(nil),
					mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{}),
					mockLifecycleInitializer.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(nil),
					mockIdentityProvider.EXPECT().CreateManagementClient(gomock.Any(), gomock.Any()).Return(mockManagement, nil),
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					// ensurePostEnrollmentHooks uses waitEnrollmentHooksReadyFn (set in test body)
					mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(true),
					mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return(bootedOSVersion, true, nil),
					mockSystemInfoManager.EXPECT().IsRebooted().Return(false),
					mockSpecManager.EXPECT().IsUpgrading().Return(true),
					mockSpecManager.EXPECT().RenderedVersion(spec.Current).Return("2"),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil),
				)
			},
		},
		{
			name: "initialization not enrolled",
			setupMocks: func(
				mockStatusManager *status.MockManager,
				mockSpecManager *spec.MockManager,
				_ *fileio.MockReadWriter,
				_ *hook.MockManager,
				_ *client.MockEnrollment,
				mockSystemInfoManager *systeminfo.MockManager,
				mockLifecycleInitializer *lifecycle.MockInitializer,
				mockExecutor *executer.MockExecuter,
				mockIdentityProvider *identity.MockProvider,
				mockManagement *client.MockManagement,
			) {
				gomock.InOrder(
					mockExecutor.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.4.2", "", 0),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockStatusManager.EXPECT().Collect(gomock.Any()).Return(nil),
					mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{}),
					mockLifecycleInitializer.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(nil),
					mockIdentityProvider.EXPECT().CreateManagementClient(gomock.Any(), gomock.Any()).Return(mockManagement, nil),
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					// ensurePostEnrollmentHooks uses waitEnrollmentHooksReadyFn (set in test body)
					mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(false),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockSpecManager.EXPECT().GetRollbackInfo().Return(spec.RollbackInfo{}, nil),
					mockSystemInfoManager.EXPECT().IsRebooted().Return(false),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockSpecManager.EXPECT().GetRollbackInfo().Return(spec.RollbackInfo{}, nil),
					mockSpecManager.EXPECT().RenderedVersion(spec.Current).Return("2"),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil),
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
			mockManagement := client.NewMockManagement(ctrl)

			log := log.NewPrefixLogger("test")
			podmanClient := client.NewPodman(log, mockExecutor, mockReadWriter, util.NewPollConfig())
			systemdClient := client.NewSystemd(mockExecutor, v1beta1.RootUsername)

			// Fast backoff for tests so ensurePostEnrollmentHooks doesn't sleep.
			fastBackoff := &wait.Backoff{
				Steps:    3,
				Duration: 1 * time.Millisecond,
				Factor:   1.0,
				Cap:      10 * time.Millisecond,
			}

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
				enrollmentHooksBackoff:  fastBackoff,
				waitEnrollmentHooksReadyFn: func(context.Context) (*enrollmentHooksState, error) {
					return &enrollmentHooksState{conditionAbsent: true, condIdx: -1}, nil
				},
				log: log,
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
				mockManagement,
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

func TestUpdateStatus(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testCases := []struct {
		name           string
		upgrading      bool
		rollbackInfo   spec.RollbackInfo
		rollbackErr    error
		wantReason     string
		wantMessage    string
		wantMsgContain string
	}{
		{
			name:       "When upgrading it should report Rebooting",
			upgrading:  true,
			wantReason: string(v1beta1.UpdateStateRebooting),
		},
		{
			name:       "When not upgrading and no rollback info it should report Updated",
			wantReason: string(v1beta1.UpdateStateUpdated),
		},
		{
			name: "When rollback info has a persisted error it should report Error with that message",
			rollbackInfo: spec.RollbackInfo{
				Version:      "6",
				SpecHash:     "abc123",
				ErrorMessage: "[2026-04-23 12:00:00] While Preparing: prefetch failed for quay.io/flightctl/images:doesnotexist: required resource not found",
			},
			wantReason:     string(v1beta1.UpdateStateError),
			wantMsgContain: "prefetch failed for quay.io/flightctl/images:doesnotexist",
		},
		{
			name:         "When rollback info has a version but no error it should report Error for that version",
			rollbackInfo: spec.RollbackInfo{Version: "6", SpecHash: "abc123"},
			wantReason:   string(v1beta1.UpdateStateError),
			wantMessage:  "Failed to update to renderedVersion: 6",
		},
		{
			name:        "When rollback info cannot be read it should not report Updated",
			rollbackErr: errors.New("disk error"),
			wantReason:  string(v1beta1.UpdateStateError),
			wantMessage: "Failed to read rollback info after update",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusManager := status.NewMockManager(ctrl)
			mockSpecManager := spec.NewMockManager(ctrl)

			mockSpecManager.EXPECT().IsUpgrading().Return(tt.upgrading)
			if !tt.upgrading {
				mockSpecManager.EXPECT().GetRollbackInfo().Return(tt.rollbackInfo, tt.rollbackErr)
			}
			mockSpecManager.EXPECT().RenderedVersion(spec.Current).Return("1")

			var gotCondition v1beta1.Condition
			mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, fns ...status.UpdateStatusFn) (*v1beta1.DeviceStatus, error) {
					ds := &v1beta1.DeviceStatus{}
					for _, fn := range fns {
						require.NoError(fn(ds))
					}
					cond := v1beta1.FindStatusCondition(ds.Conditions, v1beta1.ConditionTypeDeviceUpdating)
					require.NotNil(cond)
					gotCondition = *cond
					return ds, nil
				},
			)

			b := &Bootstrap{
				statusManager: mockStatusManager,
				specManager:   mockSpecManager,
				log:           log.NewPrefixLogger("test"),
			}
			b.updateStatus(ctx)

			require.Equal(tt.wantReason, gotCondition.Reason)
			if tt.wantMessage != "" {
				require.Equal(tt.wantMessage, gotCondition.Message)
			}
			if tt.wantMsgContain != "" {
				require.Contains(gotCondition.Message, tt.wantMsgContain)
			}
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
				mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(false)
				mockSpecManager.EXPECT().IsUpgrading().Return(false)
				mockSpecManager.EXPECT().GetRollbackInfo().Return(spec.RollbackInfo{}, nil)
			},
			expectedError: nil,
		},
		{
			name: "no OS update - rollback completed marks version as failed",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(false)
				mockSpecManager.EXPECT().IsUpgrading().Return(false)
				mockSpecManager.EXPECT().GetRollbackInfo().Return(spec.RollbackInfo{Version: "2", SpecHash: "abc123"}, nil)
				mockSpecManager.EXPECT().SetUpgradeFailed("2", "abc123").Return(nil)
			},
			expectedError: nil,
		},
		{
			name: "no OS update - still upgrading does not mark as failed",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(false)
				mockSpecManager.EXPECT().IsUpgrading().Return(true)
			},
			expectedError: nil,
		},
		{
			name: "OS image reconciliation failure",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(true)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("", false, specErr)
			},
			expectedError: specErr,
		},
		{
			name: "OS image not reconciled triggers rollback",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().OSVersion(gomock.Any()).Return("desired-image")
				mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(true)
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
				mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(true)
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

// patchOpsForCondition builds a gomock.Cond-compatible matcher (func(any) bool)
// that validates the JSON Patch contains test+replace ops for an enrollment
// hooks condition at the given index with the expected status and reason.
func patchOpsForCondition(idx int, expectedStatus v1beta1.ConditionStatus, expectedReason string) func(x any) bool {
	return func(x any) bool {
		patch, ok := x.(v1beta1.PatchRequest)
		if !ok {
			return false
		}
		if len(patch) < 2 {
			return false
		}
		// First op must be a test at the correct index
		testOp := patch[0]
		if testOp.Op != "test" {
			return false
		}
		expectedPath := fmt.Sprintf("/status/conditions/%d/type", idx)
		if testOp.Path != expectedPath {
			return false
		}
		typeVal, ok := testOp.Value.(string)
		if !ok || typeVal != string(v1beta1.ConditionTypeDeviceEnrollmentHooks) {
			return false
		}

		// Remaining ops must be replace ops at the condition index
		for _, op := range patch[1:] {
			if op.Op != "replace" {
				return false
			}
		}

		// Verify reason and status are being set
		foundReason := false
		foundStatus := false
		for _, op := range patch[1:] {
			reasonPath := fmt.Sprintf("/status/conditions/%d/reason", idx)
			statusPath := fmt.Sprintf("/status/conditions/%d/status", idx)
			if op.Path == reasonPath {
				if r, ok := op.Value.(string); ok && r == expectedReason {
					foundReason = true
				}
			}
			if op.Path == statusPath {
				if s, ok := op.Value.(string); ok && s == string(expectedStatus) {
					foundStatus = true
				}
			}
		}
		return foundReason && foundStatus
	}
}

func TestEnsurePostEnrollmentHooks(t *testing.T) {
	require := require.New(t)

	testDeviceName := "test-device-fp"
	testLabels := map[string]string{"env": "prod", "role": "edge"}
	testCertPEM := []byte("") // empty cert = nil CertificateMetadata, acceptable for tests
	const condIdx = 1

	stateFor := func(reason string, condStatus v1beta1.ConditionStatus, failurePolicy v1beta1.FailurePolicyType) *enrollmentHooksState {
		return &enrollmentHooksState{
			reason:        reason,
			status:        condStatus,
			condIdx:       condIdx,
			failurePolicy: failurePolicy,
			labels:        testLabels,
		}
	}

	testCases := []struct {
		name       string
		readyState *enrollmentHooksState
		readyErr   error
		setupMocks func(
			mockManagement *client.MockManagement,
			mockHookManager *hook.MockManager,
			mockStatusManager *status.MockManager,
			mockIdentityProvider *identity.MockProvider,
		)
		expectedError  error
		expectContinue bool
	}{
		{
			name:       "When no enrollment hooks condition exists it should proceed without running hooks",
			readyState: &enrollmentHooksState{conditionAbsent: true, condIdx: -1},
			setupMocks: func(
				_ *client.MockManagement,
				_ *hook.MockManager,
				_ *status.MockManager,
				_ *identity.MockProvider,
			) {
			},
			expectContinue: true,
		},
		{
			name:       "When condition transitions to Pending it should run hooks and PATCH Succeeded",
			readyState: stateFor(v1beta1.EnrollmentHooksReasonPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock),
			setupMocks: func(
				mockManagement *client.MockManagement,
				mockHookManager *hook.MockManager,
				mockStatusManager *status.MockManager,
				mockIdentityProvider *identity.MockProvider,
			) {
				mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{
					SystemInfo: v1beta1.DeviceSystemInfo{Architecture: "x86_64"},
				})
				mockIdentityProvider.EXPECT().GetCertificate().Return(testCertPEM, nil)
				mockHookManager.EXPECT().OnAfterEnrolling(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, enrollCtx *hook.EnrollmentContext) error {
						require.Equal(testDeviceName, enrollCtx.DeviceName)
						require.NotNil(enrollCtx.Labels)
						require.Equal("prod", enrollCtx.Labels["env"])
						require.NotNil(enrollCtx.SystemInfo)
						return nil
					})
				mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
					gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)),
				).Return(nil)
			},
			expectContinue: true,
		},
		{
			name:          "When condition transitions to Failed it should skip hooks and halt",
			readyState:    stateFor(v1beta1.EnrollmentHooksReasonFailed, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock),
			setupMocks:    func(*client.MockManagement, *hook.MockManager, *status.MockManager, *identity.MockProvider) {},
			expectedError: errEnrollmentHooksFailed,
		},
		{
			name:          "When watch times out it should error and halt",
			readyErr:      errEnrollmentHooksTimeout,
			setupMocks:    func(*client.MockManagement, *hook.MockManager, *status.MockManager, *identity.MockProvider) {},
			expectedError: errEnrollmentHooksTimeout,
		},
		{
			name:       "When condition is Pending it should run hooks and PATCH Succeeded on success",
			readyState: stateFor(v1beta1.EnrollmentHooksReasonPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock),
			setupMocks: func(
				mockManagement *client.MockManagement,
				mockHookManager *hook.MockManager,
				mockStatusManager *status.MockManager,
				mockIdentityProvider *identity.MockProvider,
			) {
				mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{
					SystemInfo: v1beta1.DeviceSystemInfo{Architecture: "x86_64"},
				})
				mockIdentityProvider.EXPECT().GetCertificate().Return(testCertPEM, nil)
				mockHookManager.EXPECT().OnAfterEnrolling(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, enrollCtx *hook.EnrollmentContext) error {
						require.Equal(testDeviceName, enrollCtx.DeviceName)
						require.NotNil(enrollCtx.Labels)
						require.Equal("prod", enrollCtx.Labels["env"])
						require.NotNil(enrollCtx.SystemInfo)
						return nil
					})
				mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
					gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)),
				).Return(nil)
			},
			expectContinue: true,
		},
		{
			name:       "When PATCH fails once then succeeds it should retry and proceed",
			readyState: stateFor(v1beta1.EnrollmentHooksReasonPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock),
			setupMocks: func(
				mockManagement *client.MockManagement,
				mockHookManager *hook.MockManager,
				mockStatusManager *status.MockManager,
				mockIdentityProvider *identity.MockProvider,
			) {
				mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{
					SystemInfo: v1beta1.DeviceSystemInfo{Architecture: "x86_64"},
				})
				mockIdentityProvider.EXPECT().GetCertificate().Return(testCertPEM, nil)
				mockHookManager.EXPECT().OnAfterEnrolling(gomock.Any(), gomock.Any()).Return(nil)
				gomock.InOrder(
					mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
						gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)),
					).Return(errors.New("temporary patch failure")),
					mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
						gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)),
					).Return(nil),
				)
			},
			expectContinue: true,
		},
		{
			name:       "When condition is Pending and hook fails with Block policy it should PATCH Failed and halt",
			readyState: stateFor(v1beta1.EnrollmentHooksReasonPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock),
			setupMocks: func(
				mockManagement *client.MockManagement,
				mockHookManager *hook.MockManager,
				mockStatusManager *status.MockManager,
				mockIdentityProvider *identity.MockProvider,
			) {
				mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{
					SystemInfo: v1beta1.DeviceSystemInfo{Architecture: "x86_64"},
				})
				mockIdentityProvider.EXPECT().GetCertificate().Return(testCertPEM, nil)
				mockHookManager.EXPECT().OnAfterEnrolling(gomock.Any(), gomock.Any()).
					Return(errors.New("hook script exited with code 1"))
				mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
					gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonFailed)),
				).Return(nil)
			},
			expectedError: errEnrollmentHooksFailed,
		},
		{
			name:       "When condition is Pending and hook fails with Continue policy it should PATCH Continued and proceed",
			readyState: stateFor(v1beta1.EnrollmentHooksReasonPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyContinue),
			setupMocks: func(
				mockManagement *client.MockManagement,
				mockHookManager *hook.MockManager,
				mockStatusManager *status.MockManager,
				mockIdentityProvider *identity.MockProvider,
			) {
				mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{
					SystemInfo: v1beta1.DeviceSystemInfo{Architecture: "x86_64"},
				})
				mockIdentityProvider.EXPECT().GetCertificate().Return(testCertPEM, nil)
				mockHookManager.EXPECT().OnAfterEnrolling(gomock.Any(), gomock.Any()).
					Return(errors.New("hook script exited with code 1"))
				mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
					gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonContinued)),
				).Return(nil)
			},
			expectContinue: true,
		},
		{
			name:          "When condition is already Failed it should skip hooks and halt",
			readyState:    stateFor(v1beta1.EnrollmentHooksReasonFailed, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock),
			setupMocks:    func(*client.MockManagement, *hook.MockManager, *status.MockManager, *identity.MockProvider) {},
			expectedError: errEnrollmentHooksFailed,
		},
		{
			name:           "When condition is already Succeeded it should proceed without running hooks",
			readyState:     stateFor(v1beta1.EnrollmentHooksReasonSucceeded, v1beta1.ConditionStatusTrue, v1beta1.FailurePolicyBlock),
			setupMocks:     func(*client.MockManagement, *hook.MockManager, *status.MockManager, *identity.MockProvider) {},
			expectContinue: true,
		},
		{
			name:           "When condition is already Continued it should proceed without running hooks",
			readyState:     stateFor(v1beta1.EnrollmentHooksReasonContinued, v1beta1.ConditionStatusTrue, v1beta1.FailurePolicyContinue),
			setupMocks:     func(*client.MockManagement, *hook.MockManager, *status.MockManager, *identity.MockProvider) {},
			expectContinue: true,
		},
		{
			name:           "When condition is ManualOverride it should proceed without running hooks",
			readyState:     stateFor(v1beta1.EnrollmentHooksReasonManualOverride, v1beta1.ConditionStatusTrue, v1beta1.FailurePolicyBlock),
			setupMocks:     func(*client.MockManagement, *hook.MockManager, *status.MockManager, *identity.MockProvider) {},
			expectContinue: true,
		},
		{
			name:          "When condition has unknown reason with status False it should halt",
			readyState:    stateFor("SomeFutureReason", v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock),
			setupMocks:    func(*client.MockManagement, *hook.MockManager, *status.MockManager, *identity.MockProvider) {},
			expectedError: errEnrollmentHooksFailed,
		},
		{
			name:           "When condition has unknown reason with status True it should proceed without running hooks",
			readyState:     stateFor("SomeFutureReason", v1beta1.ConditionStatusTrue, v1beta1.FailurePolicyBlock),
			setupMocks:     func(*client.MockManagement, *hook.MockManager, *status.MockManager, *identity.MockProvider) {},
			expectContinue: true,
		},
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockManagement := client.NewMockManagement(ctrl)
			mockHookManager := hook.NewMockManager(ctrl)
			mockStatusManager := status.NewMockManager(ctrl)
			mockIdentityProvider := identity.NewMockProvider(ctrl)

			fastBackoff := &wait.Backoff{
				Steps:    3,
				Duration: 1 * time.Millisecond,
				Factor:   1.0,
				Cap:      10 * time.Millisecond,
			}

			b := &Bootstrap{
				deviceName:             testDeviceName,
				managementClient:       mockManagement,
				hookManager:            mockHookManager,
				statusManager:          mockStatusManager,
				identityProvider:       mockIdentityProvider,
				enrollmentHooksBackoff: fastBackoff,
				waitEnrollmentHooksReadyFn: func(context.Context) (*enrollmentHooksState, error) {
					if tt.readyErr != nil {
						return nil, tt.readyErr
					}
					return tt.readyState, nil
				},
				log: log.NewPrefixLogger("test"),
			}

			tt.setupMocks(
				mockManagement,
				mockHookManager,
				mockStatusManager,
				mockIdentityProvider,
			)

			err := b.ensurePostEnrollmentHooks(context.TODO())
			if tt.expectedError != nil {
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
		})
	}
}

func TestEnsurePostEnrollmentHooks_ContextCanceled(t *testing.T) {
	require := require.New(t)

	b := &Bootstrap{
		deviceName: "test-device",
		enrollmentHooksBackoff: &wait.Backoff{
			Steps:    10,
			Duration: 50 * time.Millisecond,
			Factor:   1.0,
			Cap:      50 * time.Millisecond,
		},
		waitEnrollmentHooksReadyFn: func(ctx context.Context) (*enrollmentHooksState, error) {
			return nil, fmt.Errorf("waiting for enrollment hooks readiness: %w", ctx.Err())
		},
		log: log.NewPrefixLogger("test"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := b.ensurePostEnrollmentHooks(ctx)
	require.ErrorIs(err, context.Canceled)
	require.NotErrorIs(err, errEnrollmentHooksTimeout)
}

func TestBootstrapInitializePostEnrollmentOrdering(t *testing.T) {
	require := require.New(t)
	testDeviceName := "test-device"

	testCases := []struct {
		name       string
		setupMocks func(
			mockStatusManager *status.MockManager,
			mockSpecManager *spec.MockManager,
			mockHookManager *hook.MockManager,
			mockSystemInfoManager *systeminfo.MockManager,
			mockLifecycleInitializer *lifecycle.MockInitializer,
			mockExecutor *executer.MockExecuter,
			mockIdentityProvider *identity.MockProvider,
			mockManagement *client.MockManagement,
			mockReadWriter *fileio.MockReadWriter,
		)
		expectedError error
	}{
		{
			name: "When post-enrollment hooks succeed it should proceed through ensureBootstrap in order",
			setupMocks: func(
				mockStatusManager *status.MockManager,
				mockSpecManager *spec.MockManager,
				_ *hook.MockManager,
				mockSystemInfoManager *systeminfo.MockManager,
				mockLifecycleInitializer *lifecycle.MockInitializer,
				mockExecutor *executer.MockExecuter,
				mockIdentityProvider *identity.MockProvider,
				mockManagement *client.MockManagement,
				_ *fileio.MockReadWriter,
			) {
				// Verify the ordering: SetClient → waitEnrollmentHooks → ShouldApplyOSImageUpdate (ensureBootstrap)
				gomock.InOrder(
					mockExecutor.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.4.2", "", 0),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockStatusManager.EXPECT().Collect(gomock.Any()).Return(nil),
					mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{}),
					mockLifecycleInitializer.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(nil),
					mockIdentityProvider.EXPECT().CreateManagementClient(gomock.Any(), gomock.Any()).Return(mockManagement, nil),
					// setManagementClient
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					// ensurePostEnrollmentHooks uses waitEnrollmentHooksReadyFn (set in test body)
					// ensureBootstrap: ensureBootedOS is reached
					mockSpecManager.EXPECT().ShouldApplyOSImageUpdate().Return(false),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockSpecManager.EXPECT().GetRollbackInfo().Return(spec.RollbackInfo{}, nil),
					mockSystemInfoManager.EXPECT().IsRebooted().Return(false),
					// updateStatus
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockSpecManager.EXPECT().GetRollbackInfo().Return(spec.RollbackInfo{}, nil),
					mockSpecManager.EXPECT().RenderedVersion(spec.Current).Return("1"),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil),
				)
			},
		},
		{
			name: "When post-enrollment hooks condition is Failed with Block policy it should prevent ensureBootstrap from running",
			setupMocks: func(
				mockStatusManager *status.MockManager,
				mockSpecManager *spec.MockManager,
				_ *hook.MockManager,
				_ *systeminfo.MockManager,
				mockLifecycleInitializer *lifecycle.MockInitializer,
				mockExecutor *executer.MockExecuter,
				mockIdentityProvider *identity.MockProvider,
				mockManagement *client.MockManagement,
				_ *fileio.MockReadWriter,
			) {
				gomock.InOrder(
					mockExecutor.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.4.2", "", 0),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockStatusManager.EXPECT().Collect(gomock.Any()).Return(nil),
					mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{}),
					mockLifecycleInitializer.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(nil),
					mockIdentityProvider.EXPECT().CreateManagementClient(gomock.Any(), gomock.Any()).Return(mockManagement, nil),
					// setManagementClient
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
				)
				// ShouldApplyOSImageUpdate is NOT expected — ensureBootstrap must not be reached
			},
			expectedError: errEnrollmentHooksFailed,
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
			mockSystemInfoManager := systeminfo.NewMockManager(ctrl)
			mockLifecycleInitializer := lifecycle.NewMockInitializer(ctrl)
			mockExecutor := executer.NewMockExecuter(ctrl)
			mockIdentityProvider := identity.NewMockProvider(ctrl)
			mockManagement := client.NewMockManagement(ctrl)

			log := log.NewPrefixLogger("test")
			podmanClient := client.NewPodman(log, mockExecutor, mockReadWriter, util.NewPollConfig())
			systemdClient := client.NewSystemd(mockExecutor, v1beta1.RootUsername)

			fastBackoff := &wait.Backoff{
				Steps:    3,
				Duration: 1 * time.Millisecond,
				Factor:   1.0,
				Cap:      10 * time.Millisecond,
			}

			waitFn := func(context.Context) (*enrollmentHooksState, error) {
				return &enrollmentHooksState{conditionAbsent: true, condIdx: -1}, nil
			}
			if tt.expectedError != nil {
				waitFn = func(context.Context) (*enrollmentHooksState, error) {
					return &enrollmentHooksState{
						reason:        v1beta1.EnrollmentHooksReasonFailed,
						status:        v1beta1.ConditionStatusFalse,
						condIdx:       0,
						failurePolicy: v1beta1.FailurePolicyBlock,
					}, nil
				}
			}

			b := &Bootstrap{
				deviceName:                 testDeviceName,
				statusManager:              mockStatusManager,
				specManager:                mockSpecManager,
				hookManager:                mockHookManager,
				lifecycle:                  mockLifecycleInitializer,
				deviceReadWriter:           mockReadWriter,
				managementServiceConfig:    &baseclient.Config{},
				systemInfoManager:          mockSystemInfoManager,
				podmanClient:               podmanClient,
				systemdClient:              systemdClient,
				identityProvider:           mockIdentityProvider,
				enrollmentHooksBackoff:     fastBackoff,
				waitEnrollmentHooksReadyFn: waitFn,
				log:                        log,
			}

			ctx := context.TODO()

			tt.setupMocks(
				mockStatusManager,
				mockSpecManager,
				mockHookManager,
				mockSystemInfoManager,
				mockLifecycleInitializer,
				mockExecutor,
				mockIdentityProvider,
				mockManagement,
				mockReadWriter,
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
