package applications

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMapDomainState(t *testing.T) {
	tests := []struct {
		name     string
		state    vmDomainState
		expected v1beta1.ApplicationStatusType
	}{
		{
			name:     "When domain state is running it should return Running",
			state:    vmDomainStateRunning,
			expected: v1beta1.ApplicationStatusRunning,
		},
		{
			name:     "When domain state is shut off it should return Stopped",
			state:    vmDomainStateShutOff,
			expected: v1beta1.ApplicationStatusStopped,
		},
		{
			name:     "When domain state is in shutdown it should return Stopping",
			state:    vmDomainStateInShutdown,
			expected: v1beta1.ApplicationStatusStopping,
		},
		{
			name:     "When domain state is paused it should return Error",
			state:    vmDomainStatePaused,
			expected: v1beta1.ApplicationStatusError,
		},
		{
			name:     "When domain state is crashed it should return Error",
			state:    vmDomainStateCrashed,
			expected: v1beta1.ApplicationStatusError,
		},
		{
			name:     "When domain state is unknown it should return Error",
			state:    vmDomainState("some-unexpected-state"),
			expected: v1beta1.ApplicationStatusError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			require.Equal(tt.expected, mapDomainState(tt.state))
		})
	}
}

func TestVMStatusPollerPoll(t *testing.T) {
	const appName = "fedora-vm"
	const expectedContainer = "virt-launcher-fedora-vm-compute"
	const expectedDomain = virtLauncherDomainNamespace + "_" + appName

	tests := []struct {
		name                  string
		setupMock             func(*executer.MockExecuter)
		initialFailures       int
		expectedStatus        v1beta1.ApplicationStatusType
		expectedFailuresAfter int
	}{
		{
			name: "When virsh exits zero with running it should return Running and reset failure counter",
			setupMock: func(m *executer.MockExecuter) {
				m.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "exec", expectedContainer, "virsh", "domstate", expectedDomain).
					Return("running\n", "", 0)
			},
			initialFailures:       2,
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedFailuresAfter: 0,
		},
		{
			name: "When virsh exits zero with shut off it should return Stopped and reset failure counter",
			setupMock: func(m *executer.MockExecuter) {
				m.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "exec", expectedContainer, "virsh", "domstate", expectedDomain).
					Return("shut off\n", "", 0)
			},
			expectedStatus:        v1beta1.ApplicationStatusStopped,
			expectedFailuresAfter: 0,
		},
		{
			name: "When virsh exits zero with in shutdown it should return Stopping",
			setupMock: func(m *executer.MockExecuter) {
				m.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "exec", expectedContainer, "virsh", "domstate", expectedDomain).
					Return("in shutdown\n", "", 0)
			},
			expectedStatus:        v1beta1.ApplicationStatusStopping,
			expectedFailuresAfter: 0,
		},
		{
			name: "When virsh exits non-zero on first failure it should return Starting and increment counter",
			setupMock: func(m *executer.MockExecuter) {
				m.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "exec", expectedContainer, "virsh", "domstate", expectedDomain).
					Return("", "error: failed to connect", 1)
			},
			initialFailures:       0,
			expectedStatus:        v1beta1.ApplicationStatusStarting,
			expectedFailuresAfter: 1,
		},
		{
			name: "When virsh exits non-zero on second consecutive failure it should return Starting",
			setupMock: func(m *executer.MockExecuter) {
				m.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "exec", expectedContainer, "virsh", "domstate", expectedDomain).
					Return("", "error: failed to connect", 1)
			},
			initialFailures:       vmConsecutiveFailureThreshold - 2,
			expectedStatus:        v1beta1.ApplicationStatusStarting,
			expectedFailuresAfter: vmConsecutiveFailureThreshold - 1,
		},
		{
			name: "When virsh exits non-zero on the Nth consecutive failure it should return Error",
			setupMock: func(m *executer.MockExecuter) {
				m.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "exec", expectedContainer, "virsh", "domstate", expectedDomain).
					Return("", "error: failed to connect", 1)
			},
			initialFailures:       vmConsecutiveFailureThreshold - 1,
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedFailuresAfter: vmConsecutiveFailureThreshold,
		},
		{
			name: "When virsh recovers after prior failures it should return mapped state and reset counter",
			setupMock: func(m *executer.MockExecuter) {
				m.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "exec", expectedContainer, "virsh", "domstate", expectedDomain).
					Return("running\n", "", 0)
			},
			initialFailures:       vmConsecutiveFailureThreshold - 1,
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedFailuresAfter: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			tt.setupMock(mockExec)

			poller := newVMStatusPoller(mockExec, log.NewPrefixLogger(""), appName)
			poller.consecutiveFailures = tt.initialFailures

			status := poller.Poll(context.Background())
			require.Equal(tt.expectedStatus, status)
			require.Equal(tt.expectedFailuresAfter, poller.consecutiveFailures)
		})
	}
}
