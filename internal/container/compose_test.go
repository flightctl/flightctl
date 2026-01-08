package container

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestGetApplicationStatus(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)

	testCases := []struct {
		Name             string
		ContainerStatus  []ContainerStatus
		ExpectedStatus   string
		ExpectedMessage  string
		ExpectedExitCode int
	}{
		{
			Name: "all containers running",
			ContainerStatus: []ContainerStatus{
				{
					Name:   "container1",
					Status: "running",
				},
				{
					Name:   "container2",
					Status: "running",
				},
			},
			ExpectedStatus:  "Running",
			ExpectedMessage: "All containers are running",
		},
		{
			Name: "one container exited with error",
			ContainerStatus: []ContainerStatus{
				{
					Name:     "container1",
					Status:   "exited",
					ExitCode: 1,
				},
				{
					Name:   "container2",
					Status: "running",
				},
			},
			ExpectedStatus:   "Error",
			ExpectedMessage:  "Container container1 exited with code 1",
			ExpectedExitCode: 1,
		},
		{
			Name: "one container completed successfully",
			ContainerStatus: []ContainerStatus{
				{
					Name:     "container1",
					Status:   "exited",
					ExitCode: 0,
				},
				{
					Name:   "container2",
					Status: "running",
				},
			},
			ExpectedStatus:  "Running",
			ExpectedMessage: "All containers are running",
		},
		{
			Name: "all containers completed successfully",
			ContainerStatus: []ContainerStatus{
				{
					Name:     "container1",
					Status:   "exited",
					ExitCode: 0,
				},
				{
					Name:     "container2",
					Status:   "exited",
					ExitCode: 0,
				},
			},
			ExpectedStatus:  "Completed",
			ExpectedMessage: "All containers completed successfully",
		},
		{
			Name: "one container stopped with exit code 0",
			ContainerStatus: []ContainerStatus{
				{
					Name:     "container1",
					Status:   "exited",
					ExitCode: 0,
				},
			},
			ExpectedStatus:   "Error",
			ExpectedMessage:  "Container container1 exited with code 0",
			ExpectedExitCode: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			// setup
			podman := NewMockPodman(ctrl)
			compose := NewCompose(podman)
			ctx := context.Background()
			spec := &v1alpha1.DeviceSpec{
				Containers: &v1alpha1.Containers{
					MatchPatterns: []string{"flightctl-"},
				},
			}
			podman.EXPECT().ListContainers(ctx, gomock.Any()).Return(tc.ContainerStatus, nil)

			// test
			status, err := compose.GetApplicationStatus(ctx, spec)
			require.NoError(err)

			// verify
			require.Equal(tc.ExpectedStatus, status.Status)
			require.Equal(tc.ExpectedMessage, status.Message)
			if tc.ExpectedStatus == "Error" {
				require.Equal(util.Int32Ptr(int32(tc.ExpectedExitCode)), status.ExitCode)
			}
		})
	}
}

func TestGetApplicationStatusNoContainers(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)

	// setup
	podman := NewMockPodman(ctrl)
	compose := NewCompose(podman)
	ctx := context.Background()
	spec := &v1alpha1.DeviceSpec{
		Containers: &v1alpha1.Containers{
			MatchPatterns: []string{"flightctl-"},
		},
	}
	podman.EXPECT().ListContainers(ctx, gomock.Any()).Return([]ContainerStatus{}, nil)

	// test
	status, err := compose.GetApplicationStatus(ctx, spec)
	require.NoError(err)

	// verify
	require.Equal("Idle", status.Status)
	require.Equal("No containers found", status.Message)
}

func TestGetApplicationStatusListError(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)

	// setup
	podman := NewMockPodman(ctrl)
	compose := NewCompose(podman)
	ctx := context.Background()
	spec := &v1alpha1.DeviceSpec{
		Containers: &v1alpha1.Containers{
			MatchPatterns: []string{"flightctl-"},
		},
	}
	podman.EXPECT().ListContainers(ctx, gomock.Any()).Return(nil, fmt.Errorf("some error"))

	// test
	status, err := compose.GetApplicationStatus(ctx, spec)
	require.Error(t, err)
	require.Nil(t, status)
}
