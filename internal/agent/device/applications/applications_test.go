package applications

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// newMockVMProvider returns a MockProvider whose Spec() returns a minimal ApplicationSpec
// for a VM workload with the given app name.
func newMockVMProvider(ctrl *gomock.Controller, appName string) *provider.MockProvider {
	// VolumeManager with nil logger is acceptable in tests (used by podman_monitor_test.go).
	vol, err := provider.NewVolumeManager(nil, appName, v1beta1.AppTypeQuadlet, v1beta1.CurrentProcessUsername, nil)
	if err != nil {
		ctrl.T.Fatalf("newMockVMProvider: NewVolumeManager: %v", err)
	}
	spec := &provider.ApplicationSpec{
		Name:         appName,
		ID:           appName,
		AppType:      v1beta1.AppTypeQuadlet,
		User:         v1beta1.CurrentProcessUsername,
		IsVMWorkload: true,
		Volume:       vol,
	}
	mock := provider.NewMockProvider(ctrl)
	mock.EXPECT().Spec().Return(spec).AnyTimes()
	return mock
}

func TestApplicationStatus(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name                  string
		workloads             []Workload
		expectedReady         string
		expectedRestarts      int
		expectedStatus        v1beta1.ApplicationStatusType
		expectedSummaryStatus v1beta1.ApplicationsSummaryStatusType
		expected              v1beta1.AppType
	}{
		{
			name:                  "app created no workloads",
			expectedReady:         "0/0",
			expectedStatus:        v1beta1.ApplicationStatusUnknown,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusUnknown,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app single container preparing to start init",
			workloads: []Workload{
				{
					Status: StatusInit,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1beta1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusUnknown,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app single container preparing to start created",
			workloads: []Workload{
				{
					Status: StatusCreate,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1beta1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusUnknown,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app multiple workloads starting init",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusInit,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusStarting,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app multiple workloads starting created",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusCreate,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusStarting,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app errored",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusDie,
				},
				{
					Name:   "container2",
					Status: StatusDie,
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusError,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app running degraded",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusDie,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app running degraded",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusDied,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expected:              v1beta1.AppTypeCompose,
		},
		{
			name: "app running healthy",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusRunning,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "2/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app running healthy with restarts",
			workloads: []Workload{
				{
					Name:     "container1",
					Status:   StatusRunning,
					Restarts: 1,
				},
				{
					Name:     "container2",
					Status:   StatusRunning,
					Restarts: 2,
				},
			},
			expectedReady:         "2/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
			expectedRestarts:      3,
		},
		{
			name: "app has all workloads exited",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusExited,
				},
				{
					Name:   "container2",
					Status: StatusExited,
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1beta1.ApplicationStatusCompleted,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app has one workloads exited",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusRunning,
				},
				{
					Name:   "container2",
					Status: StatusExited,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
		},
		{
			name: "app with single container has exited",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusExited,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1beta1.ApplicationStatusCompleted,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			tempDir := t.TempDir()
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
			)

			mockExec := executer.NewMockExecuter(ctrl)
			podman := client.NewPodman(log, mockExec, readWriter, util.NewPollConfig())

			inlineSpec := v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{
					{
						Content: lo.ToPtr(util.NewComposeSpec()),
						Path:    "docker-compose.yml",
					},
				},
			}

			composeApp := v1beta1.ComposeApplication{
				Name:    lo.ToPtr("app"),
				AppType: v1beta1.AppTypeCompose,
			}
			err := composeApp.FromInlineApplicationProviderSpec(inlineSpec)
			require.NoError(err)

			var providerSpec v1beta1.ApplicationProviderSpec
			err = providerSpec.FromComposeApplication(composeApp)
			require.NoError(err)
			desired := v1beta1.DeviceSpec{
				Applications: &[]v1beta1.ApplicationProviderSpec{
					providerSpec,
				},
			}
			var podmanFactory client.PodmanFactory = func(user v1beta1.Username) (*client.Podman, error) {
				return podman, nil
			}
			var rwFactory fileio.ReadWriterFactory = func(username v1beta1.Username) (fileio.ReadWriter, error) {
				return readWriter, nil
			}
			providers, err := provider.FromDeviceSpec(context.Background(), log, podmanFactory, nil, rwFactory, &desired)
			require.NoError(err)
			require.Len(providers, 1)
			application := NewApplication(providers[0])
			if len(tt.workloads) > 0 {
				application.workloads = tt.workloads
			}
			status, summary, err := application.Status()
			require.NoError(err)

			require.Equal(tt.expectedReady, status.Ready)
			require.Equal(tt.expectedRestarts, status.Restarts)
			require.Equal(tt.expectedStatus, status.Status)
			require.Equal(tt.expectedSummaryStatus, summary.Status)
		})
	}
}

func TestVMApplicationStatus(t *testing.T) {
	const appName = "fedora-vm"
	const container = "virt-launcher-fedora-vm-compute"
	const domain = virtLauncherDomainNamespace + "_" + appName

	tests := []struct {
		name                  string
		virshStdout           string
		virshExitCode         int
		initialFailures       int
		expectedStatus        v1beta1.ApplicationStatusType
		expectedSummaryStatus v1beta1.ApplicationsSummaryStatusType
		expectedReady         string
	}{
		{
			name:                  "When virsh returns running it should report Running with Healthy summary",
			virshStdout:           "running\n",
			virshExitCode:         0,
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
			expectedReady:         "1/1",
		},
		{
			name:                  "When virsh returns shut off it should report Stopped with Healthy summary",
			virshStdout:           "shut off\n",
			virshExitCode:         0,
			expectedStatus:        v1beta1.ApplicationStatusStopped,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
			expectedReady:         "1/1",
		},
		{
			name:                  "When virsh returns in shutdown it should report Stopping with Degraded summary",
			virshStdout:           "in shutdown\n",
			virshExitCode:         0,
			expectedStatus:        v1beta1.ApplicationStatusStopping,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expectedReady:         "0/1",
		},
		{
			name:                  "When virsh exits non-zero below threshold it should report Starting with Degraded summary",
			virshStdout:           "",
			virshExitCode:         1,
			initialFailures:       0,
			expectedStatus:        v1beta1.ApplicationStatusStarting,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
			expectedReady:         "0/1",
		},
		{
			name:                  "When virsh exits non-zero at threshold it should report Error with Error summary",
			virshStdout:           "",
			virshExitCode:         1,
			initialFailures:       vmConsecutiveFailureThreshold - 1,
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusError,
			expectedReady:         "0/1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().
				ExecuteWithContext(gomock.Any(), "podman", "exec", container, "virsh", "domstate", domain).
				Return(tt.virshStdout, "", tt.virshExitCode)

			mockProvider := newMockVMProvider(ctrl, appName)
			app := NewVMApplication(mockProvider, mockExec, log.NewPrefixLogger(""))
			app.vmPoller.consecutiveFailures = tt.initialFailures

			appStatus, summary, err := app.Status()
			require.NoError(err)
			require.Equal(tt.expectedStatus, appStatus.Status)
			require.Equal(tt.expectedSummaryStatus, summary.Status)
			require.Equal(tt.expectedReady, appStatus.Ready)
		})
	}
}

func TestNewApplicationUsesWorkloadBasedLogic(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProvider := newMockVMProvider(ctrl, "regular-app")
	app := NewApplication(mockProvider)

	// Non-VM application has no vmPoller — workload-based logic applies.
	require.Nil(app.vmPoller)
	// With no workloads the workload-based logic returns Unknown.
	appStatus, summary, err := app.Status()
	require.NoError(err)
	require.Equal(v1beta1.ApplicationStatusUnknown, appStatus.Status)
	require.Equal(v1beta1.ApplicationsSummaryStatusUnknown, summary.Status)
}

func TestNewAppFromProvider(t *testing.T) {
	tests := []struct {
		name         string
		isVM         bool
		user         v1beta1.Username
		wantVMPoller bool
		wantErr      bool
	}{
		{
			name:         "When provider is not a VM workload it should return a regular application",
			isVM:         false,
			user:         v1beta1.CurrentProcessUsername,
			wantVMPoller: false,
		},
		{
			name:         "When provider is a VM workload with the current process user it should return a VM application",
			isVM:         true,
			user:         v1beta1.CurrentProcessUsername,
			wantVMPoller: true,
		},
		{
			name:    "When provider is a VM workload with a nonexistent user it should return an error",
			isVM:    true,
			user:    v1beta1.Username("nonexistent-user-emd4100-test"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			vol, err := provider.NewVolumeManager(nil, "app", v1beta1.AppTypeQuadlet, tt.user, nil)
			require.NoError(err)

			spec := &provider.ApplicationSpec{
				Name:         "app",
				ID:           "app",
				AppType:      v1beta1.AppTypeQuadlet,
				User:         tt.user,
				IsVMWorkload: tt.isVM,
				Volume:       vol,
			}
			mock := provider.NewMockProvider(ctrl)
			mock.EXPECT().Spec().Return(spec).AnyTimes()
			if tt.isVM && tt.wantErr {
				mock.EXPECT().Name().Return("app").AnyTimes()
			}

			m := &manager{log: log.NewPrefixLogger("")}
			app, err := m.newAppFromProvider(mock)

			if tt.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
			concrete := app.(*application)
			if tt.wantVMPoller {
				require.NotNil(concrete.vmPoller)
			} else {
				require.Nil(concrete.vmPoller)
			}
		})
	}
}
