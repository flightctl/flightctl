package applications

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
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

func TestApplicationStatus(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name                  string
		workloads             []Workload
		expectedReady         string
		expectedRestarts      int
		expectedStatus        v1beta1.ApplicationStatusType
		expectedSummaryStatus v1beta1.ApplicationsSummaryStatusType
		appType               v1beta1.AppType
	}{
		{
			name:                  "app created no workloads",
			expectedReady:         "0/0",
			expectedStatus:        v1beta1.ApplicationStatusUnknown,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusUnknown,
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
		},
		{
			name: "app single container preparing to start created",
			workloads: []Workload{
				{
					Status: StatusCreated,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1beta1.ApplicationStatusPreparing,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusUnknown,
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
		},
		{
			name: "app multiple workloads starting created",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusCreated,
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusStarting,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "app errored",
			workloads: []Workload{
				{
					Name:     "container1",
					Status:   StatusDie,
					ExitCode: lo.ToPtr(1),
				},
				{
					Name:     "container2",
					Status:   StatusDie,
					ExitCode: lo.ToPtr(1),
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusError,
		},
		{
			name: "app running degraded",
			workloads: []Workload{
				{
					Name:     "container1",
					Status:   StatusDie,
					ExitCode: lo.ToPtr(1),
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "app running degraded after exit",
			workloads: []Workload{
				{
					Name:     "container1",
					Status:   StatusDied,
					ExitCode: lo.ToPtr(1),
				},
				{
					Name:   "container2",
					Status: StatusRunning,
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
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
					Name:     "container1",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(1),
				},
				{
					Name:     "container2",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(1),
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusError,
		},
		{
			name: "app has one workloads exited",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusRunning,
				},
				{
					Name:     "container2",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(0),
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "app with single container has exited",
			workloads: []Workload{
				{
					Name:     "container1",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(1),
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusError,
		},
		{
			name: "app has all workloads exited with code 0",
			workloads: []Workload{
				{
					Name:     "container1",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(0),
				},
				{
					Name:     "container2",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(0),
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusError,
			appType:               v1beta1.AppTypeCompose,
		},
		{
			name: "run-to-completion app has all workloads exited with code 0",
			workloads: []Workload{
				{
					Name:     "container1",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(0),
				},
				{
					Name:     "container2",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(0),
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1beta1.ApplicationStatusCompleted,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusHealthy,
			appType:               "another-app-type",
		},
		{
			name: "app has all workloads exited with one non-zero",
			workloads: []Workload{
				{
					Name:     "container1",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(0),
				},
				{
					Name:     "container2",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(1),
				},
			},
			expectedReady:         "0/2",
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusError,
		},
		{
			name: "app has one workload running and one exited with code 0",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusRunning,
				},
				{
					Name:     "container2",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(0),
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "app has one workload running and one exited with non-zero",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusRunning,
				},
				{
					Name:     "container2",
					Status:   StatusExited,
					ExitCode: lo.ToPtr(1),
				},
			},
			expectedReady:         "1/2",
			expectedStatus:        v1beta1.ApplicationStatusRunning,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
		{
			name: "app has one workload stopped",
			workloads: []Workload{
				{
					Name:   "container1",
					Status: StatusStopped,
				},
			},
			expectedReady:         "0/1",
			expectedStatus:        v1beta1.ApplicationStatusError,
			expectedSummaryStatus: v1beta1.ApplicationsSummaryStatusDegraded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)

			mockExec := executer.NewMockExecuter(ctrl)
			podman := client.NewPodman(log, mockExec, readWriter, util.NewPollConfig())

			spec := v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{
					{
						Content: lo.ToPtr(util.NewComposeSpec()),
						Path:    "docker-compose.yml",
					},
				},
			}

			appType := tt.appType
			if appType == "" {
				appType = v1beta1.AppTypeCompose
			}

			providerSpec := v1beta1.ApplicationProviderSpec{
				Name:    lo.ToPtr("app"),
				AppType: appType,
			}
			err := providerSpec.FromInlineApplicationProviderSpec(spec)
			require.NoError(err)
			desired := v1beta1.DeviceSpec{
				Applications: &[]v1beta1.ApplicationProviderSpec{
					providerSpec,
				},
			}
			providers, err := provider.FromDeviceSpec(context.Background(), log, podman, readWriter, &desired)
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

func TestApplicationStatusReasonStaysClean(t *testing.T) {
	require := require.New(t)

	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter()
	readWriter.SetRootdir(tmpDir)

	mockExec := executer.NewMockExecuter(ctrl)
	podman := client.NewPodman(log, mockExec, readWriter, util.NewPollConfig())

	spec := v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Content: lo.ToPtr(util.NewComposeSpec()),
				Path:    "docker-compose.yml",
			},
		},
	}

	providerSpec := v1beta1.ApplicationProviderSpec{
		Name:    lo.ToPtr("app"),
		AppType: v1beta1.AppTypeCompose,
	}
	err := providerSpec.FromInlineApplicationProviderSpec(spec)
	require.NoError(err)
	desired := v1beta1.DeviceSpec{
		Applications: &[]v1beta1.ApplicationProviderSpec{
			providerSpec,
		},
	}
	providers, err := provider.FromDeviceSpec(context.Background(), log, podman, readWriter, &desired)
	require.NoError(err)
	require.Len(providers, 1)
	application := NewApplication(providers[0])

	// Start with an error state
	application.workloads = []Workload{
		{
			Name:     "container1",
			Status:   StatusExited,
			ExitCode: lo.ToPtr(1),
		},
	}
	status, _, err := application.Status()
	require.NoError(err)
	require.Equal(v1beta1.ApplicationStatusError, status.Status)
	require.Contains(status.Info, "Reason", "Reason should be set in error state")

	// Transition to a healthy state
	application.workloads = []Workload{
		{
			Name:   "container1",
			Status: StatusRunning,
		},
	}
	status, _, err = application.Status()
	require.NoError(err)
	require.Equal(v1beta1.ApplicationStatusRunning, status.Status)
	require.NotContains(status.Info, "Reason", "Reason should be cleared in healthy state")
}
