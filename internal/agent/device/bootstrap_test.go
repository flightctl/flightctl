package device

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// deviceWithNoEnrollmentHooksCondition returns a Device with no EnrollmentHooks
// condition, used by TestInitialization to satisfy the ensurePostEnrollmentHooks
// call in Initialize.
func deviceWithNoEnrollmentHooksCondition() *v1beta1.Device {
	name := "test-device"
	return &v1beta1.Device{
		Metadata: v1beta1.ObjectMeta{Name: &name},
		Status:   &v1beta1.DeviceStatus{},
	}
}

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
					// ensurePostEnrollmentHooks: GetDevice returns no condition → proceed
					mockManagement.EXPECT().GetDevice(gomock.Any(), gomock.Any()).Return(deviceWithNoEnrollmentHooksCondition(), http.StatusOK, nil),
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
					// ensurePostEnrollmentHooks: GetDevice returns no condition → proceed
					mockManagement.EXPECT().GetDevice(gomock.Any(), gomock.Any()).Return(deviceWithNoEnrollmentHooksCondition(), http.StatusOK, nil),
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
					// ensurePostEnrollmentHooks: GetDevice returns no condition → proceed
					mockManagement.EXPECT().GetDevice(gomock.Any(), gomock.Any()).Return(deviceWithNoEnrollmentHooksCondition(), http.StatusOK, nil),
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

// conditionIndex returns the index of a condition with the given type in the
// conditions slice, or -1 if not found.
func conditionIndex(conditions []v1beta1.Condition, ct v1beta1.ConditionType) int {
	for i := range conditions {
		if conditions[i].Type == ct {
			return i
		}
	}
	return -1
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
	testLabels := &map[string]string{"env": "prod", "role": "edge"}
	testCertPEM := []byte("") // empty cert = nil CertificateMetadata, acceptable for tests

	// Helper: build a device with an EnrollmentHooks condition at the given
	// reason/status, and with the given failurePolicy in the snapshot.
	buildDevice := func(reason string, condStatus v1beta1.ConditionStatus, failurePolicy v1beta1.FailurePolicyType) *v1beta1.Device {
		return &v1beta1.Device{
			Metadata: v1beta1.ObjectMeta{
				Name:   &testDeviceName,
				Labels: testLabels,
			},
			Status: &v1beta1.DeviceStatus{
				Conditions: []v1beta1.Condition{
					{Type: v1beta1.ConditionTypeDeviceUpdating, Status: v1beta1.ConditionStatusFalse, Reason: "Updated"},
					{Type: v1beta1.ConditionTypeDeviceEnrollmentHooks, Status: condStatus, Reason: reason},
				},
				EnrollmentHooks: &v1beta1.DeviceEnrollmentHooksStatus{
					Snapshot: &v1beta1.EnrollmentHookSnapshot{
						FailurePolicy: failurePolicy,
					},
				},
			},
		}
	}

	// Helper: build a device with no EnrollmentHooks condition at all.
	buildDeviceNoCondition := func() *v1beta1.Device {
		return &v1beta1.Device{
			Metadata: v1beta1.ObjectMeta{
				Name:   &testDeviceName,
				Labels: testLabels,
			},
			Status: &v1beta1.DeviceStatus{
				Conditions: []v1beta1.Condition{
					{Type: v1beta1.ConditionTypeDeviceUpdating, Status: v1beta1.ConditionStatusFalse, Reason: "Updated"},
				},
			},
		}
	}

	testCases := []struct {
		name       string
		setupMocks func(
			mockManagement *client.MockManagement,
			mockHookManager *hook.MockManager,
			mockStatusManager *status.MockManager,
			mockIdentityProvider *identity.MockProvider,
		)
		expectedError  error
		expectContinue bool // true if bootstrap should proceed after hooks
	}{
		{
			name: "When no enrollment hooks condition exists it should proceed without running hooks",
			setupMocks: func(
				mockManagement *client.MockManagement,
				_ *hook.MockManager,
				_ *status.MockManager,
				_ *identity.MockProvider,
			) {
				// GetDevice returns a device with no EnrollmentHooks condition
				mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
					Return(buildDeviceNoCondition(), http.StatusOK, nil)
				// No hook execution, no PATCH
			},
			expectContinue: true,
		},
		{
			name: "When condition is NotifyPending then transitions to Pending it should run hooks and PATCH Succeeded",
			setupMocks: func(
				mockManagement *client.MockManagement,
				mockHookManager *hook.MockManager,
				mockStatusManager *status.MockManager,
				mockIdentityProvider *identity.MockProvider,
			) {
				pendingDevice := buildDevice(v1beta1.EnrollmentHooksReasonPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock)

				// First poll returns NotifyPending
				notifyPendingDevice := buildDevice(v1beta1.EnrollmentHooksReasonNotifyPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock)
				gomock.InOrder(
					mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
						Return(notifyPendingDevice, http.StatusOK, nil),
					// Second poll returns Pending (server notification done)
					mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
						Return(pendingDevice, http.StatusOK, nil),
				)

				// Expect enrollment context population and hook execution
				mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{
					SystemInfo: v1beta1.DeviceSystemInfo{Architecture: "x86_64"},
				})
				mockIdentityProvider.EXPECT().GetCertificate().Return(testCertPEM, nil)
				mockHookManager.EXPECT().OnAfterEnrolling(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, enrollCtx *hook.EnrollmentContext) error {
						// Verify enrollment context population
						require.Equal(testDeviceName, enrollCtx.DeviceName)
						require.NotNil(enrollCtx.Labels)
						require.Equal("prod", enrollCtx.Labels["env"])
						require.NotNil(enrollCtx.SystemInfo)
						return nil
					})

				// Expect PATCH with Succeeded status
				condIdx := conditionIndex(pendingDevice.Status.Conditions, v1beta1.ConditionTypeDeviceEnrollmentHooks)
				mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
					gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)),
				).Return(nil)
			},
			expectContinue: true,
		},
		{
			name: "When condition is NotifyPending then transitions to Failed it should skip hooks and halt",
			setupMocks: func(
				mockManagement *client.MockManagement,
				_ *hook.MockManager,
				_ *status.MockManager,
				_ *identity.MockProvider,
			) {
				// First poll returns NotifyPending
				notifyPendingDevice := buildDevice(v1beta1.EnrollmentHooksReasonNotifyPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock)
				failedDevice := buildDevice(v1beta1.EnrollmentHooksReasonFailed, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock)
				gomock.InOrder(
					mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
						Return(notifyPendingDevice, http.StatusOK, nil),
					// Second poll returns Failed (server-side notification failed)
					mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
						Return(failedDevice, http.StatusOK, nil),
				)
				// No hook execution, no PATCH — halt
			},
			expectedError: errEnrollmentHooksFailed,
		},
		{
			name: "When condition stays NotifyPending until timeout it should error and halt",
			setupMocks: func(
				mockManagement *client.MockManagement,
				_ *hook.MockManager,
				_ *status.MockManager,
				_ *identity.MockProvider,
			) {
				// All polls return NotifyPending — timeout will be exhausted
				notifyPendingDevice := buildDevice(v1beta1.EnrollmentHooksReasonNotifyPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock)
				mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
					Return(notifyPendingDevice, http.StatusOK, nil).
					AnyTimes()
			},
			expectedError: errEnrollmentHooksTimeout,
		},
		{
			name: "When condition is Pending it should run hooks and PATCH Succeeded on success",
			setupMocks: func(
				mockManagement *client.MockManagement,
				mockHookManager *hook.MockManager,
				mockStatusManager *status.MockManager,
				mockIdentityProvider *identity.MockProvider,
			) {
				pendingDevice := buildDevice(v1beta1.EnrollmentHooksReasonPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock)
				mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
					Return(pendingDevice, http.StatusOK, nil)

				// Expect enrollment context population
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

				// Expect PATCH with Succeeded (True)
				condIdx := conditionIndex(pendingDevice.Status.Conditions, v1beta1.ConditionTypeDeviceEnrollmentHooks)
				mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
					gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)),
				).Return(nil)
			},
			expectContinue: true,
		},
		{
			name: "When condition is Pending and hook fails with Block policy it should PATCH Failed and halt",
			setupMocks: func(
				mockManagement *client.MockManagement,
				mockHookManager *hook.MockManager,
				mockStatusManager *status.MockManager,
				mockIdentityProvider *identity.MockProvider,
			) {
				pendingDevice := buildDevice(v1beta1.EnrollmentHooksReasonPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock)
				mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
					Return(pendingDevice, http.StatusOK, nil)

				mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{
					SystemInfo: v1beta1.DeviceSystemInfo{Architecture: "x86_64"},
				})
				mockIdentityProvider.EXPECT().GetCertificate().Return(testCertPEM, nil)
				mockHookManager.EXPECT().OnAfterEnrolling(gomock.Any(), gomock.Any()).
					Return(errors.New("hook script exited with code 1"))

				// Expect PATCH with Failed (False) because policy is Block
				condIdx := conditionIndex(pendingDevice.Status.Conditions, v1beta1.ConditionTypeDeviceEnrollmentHooks)
				mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
					gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonFailed)),
				).Return(nil)
			},
			expectedError: errEnrollmentHooksFailed,
		},
		{
			name: "When condition is Pending and hook fails with Continue policy it should PATCH Continued and proceed",
			setupMocks: func(
				mockManagement *client.MockManagement,
				mockHookManager *hook.MockManager,
				mockStatusManager *status.MockManager,
				mockIdentityProvider *identity.MockProvider,
			) {
				pendingDevice := buildDevice(v1beta1.EnrollmentHooksReasonPending, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyContinue)
				mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
					Return(pendingDevice, http.StatusOK, nil)

				mockStatusManager.EXPECT().Get(gomock.Any()).Return(&v1beta1.DeviceStatus{
					SystemInfo: v1beta1.DeviceSystemInfo{Architecture: "x86_64"},
				})
				mockIdentityProvider.EXPECT().GetCertificate().Return(testCertPEM, nil)
				mockHookManager.EXPECT().OnAfterEnrolling(gomock.Any(), gomock.Any()).
					Return(errors.New("hook script exited with code 1"))

				// Expect PATCH with Continued (True) because policy is Continue
				condIdx := conditionIndex(pendingDevice.Status.Conditions, v1beta1.ConditionTypeDeviceEnrollmentHooks)
				mockManagement.EXPECT().PatchDeviceStatus(gomock.Any(), testDeviceName,
					gomock.Cond(patchOpsForCondition(condIdx, v1beta1.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonContinued)),
				).Return(nil)
			},
			expectContinue: true,
		},
		{
			name: "When condition is already Failed it should skip hooks and halt",
			setupMocks: func(
				mockManagement *client.MockManagement,
				_ *hook.MockManager,
				_ *status.MockManager,
				_ *identity.MockProvider,
			) {
				failedDevice := buildDevice(v1beta1.EnrollmentHooksReasonFailed, v1beta1.ConditionStatusFalse, v1beta1.FailurePolicyBlock)
				mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
					Return(failedDevice, http.StatusOK, nil)
				// No hook execution, no PATCH — halt
			},
			expectedError: errEnrollmentHooksFailed,
		},
		{
			name: "When condition is already Succeeded it should proceed without running hooks",
			setupMocks: func(
				mockManagement *client.MockManagement,
				_ *hook.MockManager,
				_ *status.MockManager,
				_ *identity.MockProvider,
			) {
				succeededDevice := buildDevice(v1beta1.EnrollmentHooksReasonSucceeded, v1beta1.ConditionStatusTrue, v1beta1.FailurePolicyBlock)
				mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
					Return(succeededDevice, http.StatusOK, nil)
				// No hook execution, no PATCH — proceed
			},
			expectContinue: true,
		},
		{
			name: "When condition is already Continued it should proceed without running hooks",
			setupMocks: func(
				mockManagement *client.MockManagement,
				_ *hook.MockManager,
				_ *status.MockManager,
				_ *identity.MockProvider,
			) {
				continuedDevice := buildDevice(v1beta1.EnrollmentHooksReasonContinued, v1beta1.ConditionStatusTrue, v1beta1.FailurePolicyContinue)
				mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
					Return(continuedDevice, http.StatusOK, nil)
				// No hook execution, no PATCH — proceed
			},
			expectContinue: true,
		},
		{
			name: "When condition is ManualOverride it should proceed without running hooks",
			setupMocks: func(
				mockManagement *client.MockManagement,
				_ *hook.MockManager,
				_ *status.MockManager,
				_ *identity.MockProvider,
			) {
				overrideDevice := buildDevice(v1beta1.EnrollmentHooksReasonManualOverride, v1beta1.ConditionStatusTrue, v1beta1.FailurePolicyBlock)
				mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).
					Return(overrideDevice, http.StatusOK, nil)
				// No hook execution, no PATCH — proceed
			},
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

			// Use a fast backoff for tests so they don't sleep.
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
				log:                    log.NewPrefixLogger("test"),
			}

			ctx := context.TODO()

			tt.setupMocks(
				mockManagement,
				mockHookManager,
				mockStatusManager,
				mockIdentityProvider,
			)

			err := b.ensurePostEnrollmentHooks(ctx)
			if tt.expectedError != nil {
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
		})
	}
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
				// Verify the ordering: SetClient → GetDevice → ShouldApplyOSImageUpdate (ensureBootstrap)
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
					// ensurePostEnrollmentHooks: GetDevice returns no condition → proceed
					mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).Return(deviceWithNoEnrollmentHooksCondition(), http.StatusOK, nil),
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
				failedDeviceName := testDeviceName
				failedDevice := &v1beta1.Device{
					Metadata: v1beta1.ObjectMeta{Name: &failedDeviceName},
					Status: &v1beta1.DeviceStatus{
						Conditions: []v1beta1.Condition{
							{Type: v1beta1.ConditionTypeDeviceEnrollmentHooks, Status: v1beta1.ConditionStatusFalse, Reason: v1beta1.EnrollmentHooksReasonFailed},
						},
						EnrollmentHooks: &v1beta1.DeviceEnrollmentHooksStatus{
							Snapshot: &v1beta1.EnrollmentHookSnapshot{
								FailurePolicy: v1beta1.FailurePolicyBlock,
							},
						},
					},
				}

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
					// ensurePostEnrollmentHooks: GetDevice returns Failed → halt
					mockManagement.EXPECT().GetDevice(gomock.Any(), testDeviceName).Return(failedDevice, http.StatusOK, nil),
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

			b := &Bootstrap{
				deviceName:              testDeviceName,
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
				log:                     log,
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
