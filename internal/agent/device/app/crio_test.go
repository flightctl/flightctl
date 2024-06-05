package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestCrioList(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		containerIds  []string
		matchPatterns []string
		appCount      int
		result        []string
	}{
		{
			name:          "happy path",
			containerIds:  []string{"24ca73d0b8b02093e6ec2217546a97f919f33718ec38cfa81e589eeb4f8e7040"},
			matchPatterns: []string{"sbd"},
			appCount:      1,
			result:        []string{crioListResult, crioctlInspectResult},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Second)
			defer cancel()

			ctrl := gomock.NewController(t)
			execMock := executer.NewMockExecuter(ctrl)
			defer ctrl.Finish()

			// list containers
			listArgs := []string{"ps", "-a", "--output", "json"}
			for _, pattern := range tt.matchPatterns {
				listArgs = append(listArgs, "--name")
				listArgs = append(listArgs, fmt.Sprintf("^%s$", pattern)) // crio uses regex for matching
			}
			execMock.EXPECT().ExecuteWithContext(ctx, CrictlCmd, listArgs).Return(tt.result[0], "", 0)

			// inspect containers
			for _, id := range tt.containerIds {
				statusArgs := []string{
					"inspect",
					id,
				}
				execMock.EXPECT().ExecuteWithContext(ctx, CrictlCmd, statusArgs).Return(tt.result[1], "", 0)
			}

			crio := NewCrioClient(execMock)
			apps, err := crio.List(ctx, tt.matchPatterns...)
			require.NoError(err)
			require.Len(apps, tt.appCount)
			require.Equal(tt.containerIds[0], *apps[0].Id)
			require.Equal("sbdb", *apps[0].Name)
			require.Equal(4, *apps[0].Restarts)
			require.Equal(v1alpha1.ApplicationStateRunning, *apps[0].State)
		})
	}
}

func TestCrioGetStatus(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name        string
		containerId string
		result      string
	}{
		{
			name:        "happy path",
			containerId: "24ca73d0b8b02093e6ec2217546a97f919f33718ec38cfa81e589eeb4f8e7040",
			result:      crioctlInspectResult,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Second)
			defer cancel()

			ctrl := gomock.NewController(t)
			execMock := executer.NewMockExecuter(ctrl)
			defer ctrl.Finish()

			statusArgs := []string{
				"inspect",
				tt.containerId,
			}
			execMock.EXPECT().ExecuteWithContext(ctx, CrictlCmd, statusArgs).Return(tt.result, "", 0)

			crio := NewCrioClient(execMock)
			status, err := crio.GetStatus(ctx, tt.containerId)
			require.NoError(err)
			require.Equal(tt.containerId, *status.Id)
			require.Equal("sbdb", *status.Name)
			require.Equal(4, *status.Restarts)
			require.Equal(v1alpha1.ApplicationStateRunning, *status.State)
		})
	}
}

const (
	crioListResult = `{
  "containers": [
    {
      "id": "24ca73d0b8b02093e6ec2217546a97f919f33718ec38cfa81e589eeb4f8e7040",
      "podSandboxId": "d9795a1474428ec74f70a83a03df842e1c9f540855592a584c668a885cc32b7b",
      "metadata": {
        "name": "sbdb",
        "attempt": 4
      },
      "image": {
        "image": "c5f7433404c64c5dc7851faa3b0e75c99903bb0a2ec024f5f063b8b928813704",
        "annotations": {
        },
        "userSpecifiedImage": ""
      },
      "imageRef": "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:622a44033b669976ebf4f95b9d5f5629c565dc296897bad34301cdd2075844ac",
      "state": "CONTAINER_RUNNING",
      "createdAt": "1717161223054406170",
      "labels": {
        "io.kubernetes.container.name": "sbdb",
        "io.kubernetes.pod.name": "ovnkube-node-dv5hp",
        "io.kubernetes.pod.namespace": "openshift-ovn-kubernetes",
        "io.kubernetes.pod.uid": "7619a1d7-08c5-4254-8c78-0c478e8c38f4"
      },
      "annotations": {
        "io.kubernetes.container.hash": "b572debd",
        "io.kubernetes.container.restartCount": "0",
        "io.kubernetes.container.terminationMessagePath": "/dev/termination-log",
        "io.kubernetes.container.terminationMessagePolicy": "FallbackToLogsOnError",
        "io.kubernetes.pod.terminationGracePeriod": "30"
      }
    }
  ]
}`

	crioctlInspectResult = `{
  "status": {
    "id": "24ca73d0b8b02093e6ec2217546a97f919f33718ec38cfa81e589eeb4f8e7040",
    "metadata": {
      "attempt": 4,
      "name": "sbdb"
    },
    "state": "CONTAINER_RUNNING",
    "createdAt": "2024-05-31T13:13:43.095763407Z",
    "startedAt": "2024-05-31T13:13:43.192788279Z",
    "finishedAt": "0001-01-01T00:00:00Z",
    "exitCode": 0,
    "image": {
      "annotations": {},
      "image": "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:622a44033b669976ebf4f95b9d5f5629c565dc296897bad34301cdd2075844ac",
      "userSpecifiedImage": ""
    },
    "imageRef": "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:622a44033b669976ebf4f95b9d5f5629c565dc296897bad34301cdd2075844ac"
   }
}`
)
