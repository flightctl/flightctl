package device

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
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

			) {
				gomock.InOrder(
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).Times(1),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).Times(1),
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).Times(1),
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().IsOSUpdate().Return(false, nil),
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
			) {
				bootedOSVersion := "2.0.0"
				gomock.InOrder(
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).Times(1),
					mockSpecManager.EXPECT().Ensure().Return(nil),
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).Times(1),
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).Times(1),
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().IsOSUpdate().Return(true, nil),
					mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return(bootedOSVersion, true, nil),
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
			) {
				mockEnrollmentRequest := mockEnrollmentRequest(v1alpha1.ConditionStatusTrue)
				gomock.InOrder(
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(false, nil).Times(1),
					mockSpecManager.EXPECT().Initialize().Return(nil),
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(false, nil).Times(1),
					mockStatusManager.EXPECT().Collect(gomock.Any()).Return(nil),
					mockStatusManager.EXPECT().Get(gomock.Any()).Return(nil),
					mockEnrollmentClient.EXPECT().CreateEnrollmentRequest(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockEnrollmentClient.EXPECT().GetEnrollmentRequest(gomock.Any(), gomock.Any()).Return(mockEnrollmentRequest, nil),
					mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil),
					mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(true, nil).Times(1),
					mockStatusManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().SetClient(gomock.Any()),
					mockSpecManager.EXPECT().IsOSUpdate().Return(false, nil),
					mockSpecManager.EXPECT().RenderedVersion(spec.Current).Return("2"),
					mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil),
					mockSpecManager.EXPECT().IsUpgrading().Return(false),
					mockStatusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil),
				)
			},
		},
	}
	for _, tt := range testCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusManager := status.NewMockManager(ctrl)
			mockSpecManager := spec.NewMockManager(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockHookManager := hook.NewMockManager(ctrl)
			mockEnrollmentClient := client.NewMockEnrollment(ctrl)

			backoff := wait.Backoff{
				Steps: 1,
			}

			b := &Bootstrap{
				statusManager:           mockStatusManager,
				specManager:             mockSpecManager,
				hookManager:             mockHookManager,
				deviceReadWriter:        mockReadWriter,
				managementServiceConfig: &client.Config{},
				enrollmentClient:        mockEnrollmentClient,
				log:                     log.NewPrefixLogger("test"),
				backoff:                 backoff,
			}

			ctx := context.TODO()

			tt.setupMocks(
				mockStatusManager,
				mockSpecManager,
				mockReadWriter,
				mockHookManager,
				mockEnrollmentClient,
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
					mockSpecManager.EXPECT().Rollback().Return(nil),
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
					mockSpecManager.EXPECT().Rollback().Return(mockErr),
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
		tt := tt
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
				mockSpecManager.EXPECT().IsOSUpdate().Return(false, nil)
			},
			expectedError: nil,
		},
		{
			name: "OS image reconciliation failure",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().IsOSUpdate().Return(true, nil)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("", false, specErr)
			},
			expectedError: specErr,
		},
		{
			name: "OS image not reconciled triggers rollback",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().OSVersion(gomock.Any()).Return("desired-image")
				mockSpecManager.EXPECT().IsOSUpdate().Return(true, nil)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("unexpected-booted-image", false, nil)
				mockSpecManager.EXPECT().IsRollingBack(gomock.Any()).Return(true, nil)
				mockSpecManager.EXPECT().Rollback().Return(nil)
				mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil)
				mockSpecManager.EXPECT().RenderedVersion(spec.Desired).Return("2")
				mockStatusManager.EXPECT().UpdateCondition(gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError: nil,
		},
		{
			name: "OS image reconciled",
			setupMocks: func(mockStatusManager *status.MockManager, mockSpecManager *spec.MockManager) {
				mockSpecManager.EXPECT().IsOSUpdate().Return(true, nil)
				mockSpecManager.EXPECT().CheckOsReconciliation(gomock.Any()).Return("desired-image", true, nil)
			},
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

			err := b.ensureBootedOS(ctx)
			if tt.expectedError != nil {
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
		})
	}
}

func mockEnrollmentRequest(status v1alpha1.ConditionStatus) *v1alpha1.EnrollmentRequest {
	condition := v1alpha1.Condition{
		Type:               v1alpha1.EnrollmentRequestApproved,
		LastTransitionTime: time.Now(),
		Status:             status,
		Reason:             "reason",
		Message:            "message",
	}
	return &v1alpha1.EnrollmentRequest{
		Metadata: v1alpha1.ObjectMeta{
			Name: util.StrToPtr("mock-request"),
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr: "different csr string",
		},
		Status: &v1alpha1.EnrollmentRequestStatus{
			Conditions:  []v1alpha1.Condition{condition},
			Certificate: util.StrToPtr(mockManagementCert),
		},
	}
}

var mockManagementCert = `-----BEGIN CERTIFICATE-----
MIIBjDCCATGgAwIBAgIINoR3ImoPCTEwCgYIKoZIzj0EAwIwDTELMAkGA1UEAxMC
Y2EwHhcNMjQwMjI3MjEwODUxWhcNMzQwMjI0MjEwODUyWjANMQswCQYDVQQDEwJj
YTBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABFv6UUQcSOlh5KeccQZrgRNG9na3
kLnS+ujwMQyFqqpMVez+oiED+601q572Cs/SCqsdoszGhw5+kj3OchYkREGjezB5
MA4GA1UdDwEB/wQEAwICpDAPBgNVHRMBAf8EBTADAQH/MCkGA1UdDgQiBCBjMAN8
gDGCoybdkHp5RcjIxHlF/AJ6j1f8OjLrU8r4ZzArBgNVHSMEJDAigCBjMAN8gDGC
oybdkHp5RcjIxHlF/AJ6j1f8OjLrU8r4ZzAKBggqhkjOPQQDAgNJADBGAiEA/h5w
CHzlbDp2BUZwuOuYowGj4Npzvaw56bZy/6gcorYCIQC3gp8uwlVHK10n+q7NcrqD
Ip8s5o3V6ts3shDNpknA/Q==
-----END CERTIFICATE-----`
