
package container

import (
	"context"
	"errors"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/go-ole/go-ole"
	"github.com/stretchr/testify/require"
	"github.com/containers/common/libnetwork/types/container"
	types "github.com/containers/podman/v4/pkg/bindings/types"
)

func TestGetPodmanComposeStatus(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("-")
	spec := &api.Compose{Path: "/tmp/somepath"}

	tests := []struct {
		name           string
		psout          string
		inspects       map[string]types.InspectContainerData
		pserr          error
		inspecterr     error
		expectedStatus api.ApplicationStatus
	}{
		{
			name:           "no containers",
			expectedStatus: api.ApplicationStatusCompleted,
		},
		{
			name:  "one container running",
			psout: "c1",
			inspects: map[string]types.InspectContainerData{
				"c1": {Id: "c1", Name: "c1", State: &types.InspectContainerState{Status: "running"}},
			},
			expectedStatus: api.ApplicationStatusRunning,
		},
		{
			name:  "one container completed",
			psout: "c1",
			inspects: map[string]types.InspectContainerData{
				"c1": {Id: "c1", Name: "c1", State: &types.InspectContainerState{Status: "exited", ExitCode: 0}},
			},
			expectedStatus: api.ApplicationStatusCompleted,
		},
		{
			name:  "one container failed",
			psout: "c1",
			inspects: map[string]types.InspectContainerData{
				"c1": {Id: "c1", Name: "c1", State: &types.InspectContainerState{Status: "exited", ExitCode: 1}},
			},
			expectedStatus: api.ApplicationStatusFailed,
		},
		{
			name:  "container with restart policy always and exit code 0",
			psout: "c1",
			inspects: map[string]types.InspectContainerData{
				"c1": {
					Id:    "c1",
					Name:  "c1",
					State: &types.InspectContainerState{Status: "exited", ExitCode: 0},
					HostConfig: &container.HostConfig{
						RestartPolicy: container.RestartPolicy{Name: "always"},
					},
				},
			},
			expectedStatus: api.ApplicationStatusFailed,
		},
		{
			name:  "container with restart policy unless-stopped and exit code 0",
			psout: "c1",
			inspects: map[string]types.InspectContainerData{
				"c1": {
					Id:    "c1",
					Name:  "c1",
					State: &types.InspectContainerState{Status: "exited", ExitCode: 0},
					HostConfig: &container.HostConfig{
						RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
					},
				},
			},
			expectedStatus: api.ApplicationStatusFailed,
		},
		{
			name:  "container with restart policy on-failure and exit code 0",
			psout: "c1",
			inspects: map[string]types.InspectContainerData{
				"c1": {
					Id:    "c1",
					Name:  "c1",
					State: &types.InspectContainerState{Status: "exited", ExitCode: 0},
					HostConfig: &container.HostConfig{
						RestartPolicy: container.RestartPolicy{Name: "on-failure"},
					},
				},
			},
			expectedStatus: api.ApplicationStatusCompleted,
		},
		{
			name:  "container with no restart policy and exit code 0",
			psout: "c1",
			inspects: map[string]types.InspectContainerData{
				"c1": {
					Id:         "c1",
					Name:       "c1",
					State:      &types.InspectContainerState{Status: "exited", ExitCode: 0},
					HostConfig: &container.HostConfig{},
				},
			},
			expectedStatus: api.ApplicationStatusCompleted,
		},
		{
			name:  "one container stopped with exit code 0",
			psout: "c1",
			inspects: map[string]types.InspectContainerData{
				"c1": {Id: "c1", Name: "c1", State: &types.InspectContainerState{Status: "stopped", ExitCode: 0}},
			},
			expectedStatus: api.ApplicationStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewMockRunner()
			runner.EXPECT().Compose(context.Background(), "test", spec.Path, "ps", "-q").Return([]byte(tt.psout), tt.pserr)
			for cid, inspect := range tt.inspects {
				inspect := inspect
				runner.EXPECT().Inspect(context.Background(), cid).Return(&inspect, tt.inspecterr)
			}

			compose := NewCompose(runner, log, "/tmp")
			status, err := compose.GetPodmanComposeStatus(context.Background(), "test", spec)
			if tt.pserr != nil || tt.inspecterr != nil {
				require.Error(err)
				return
			}
			require.NoError(err)
			require.Equal(tt.expectedStatus, status.Summary.Status)

		})
	}

}

func TestExists(t *testing.T) {
	log := log.NewPrefixLogger("-")
	spec := &api.Compose{Path: "/tmp/somepath"}
	require := require.New(t)

	t.Run("application exists", func(t *testing.T) {
		runner := NewMockRunner()
		runner.EXPECT().Compose(context.Background(), "test", spec.Path, "ps").Return([]byte("container id"), nil)
		compose := NewCompose(runner, log, "/tmp")
		exists, err := compose.Exists(context.Background(), "test", spec)
		require.NoError(err)
		require.True(exists)
	})

	t.Run("application does not exist", func(t *testing.T) {
		runner := NewMockRunner()
		runner.EXPECT().Compose(context.Background(), "test", spec.Path, "ps").Return(nil, util.NewNoContainersFoundError(errors.New("no containers found")))
		compose := NewCompose(runner, log, "/tmp")
		exists, err := compose.Exists(context.Background(), "test", spec)
		require.NoError(err)
		require.False(exists)
	})

	t.Run("error checking for application", func(t *testing.T) {
		runner := NewMockRunner()
		runner.EXPECT().Compose(context.Background(), "test", spec.Path, "ps").Return(nil, errors.New("some error"))
		compose := NewCompose(runner, log, "/tmp")
		exists, err := compose.Exists(context.Background(), "test", spec)
		require.Error(err)
		require.False(exists)
	})
}
