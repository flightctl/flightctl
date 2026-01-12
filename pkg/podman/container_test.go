package podman_test

import (
	"context"
	"os"
	"testing"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/podman"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestListContainers_Options(t *testing.T) {
	testCases := []struct {
		name                string
		options             podman.ListContainersOptions
		expectedExecCommand []string
	}{
		{
			name:                "no options",
			options:             podman.ListContainersOptions{},
			expectedExecCommand: []string{"ps", "--format", "json"},
		},
		{
			name:                "all containers",
			options:             podman.ListContainersOptions{All: true},
			expectedExecCommand: []string{"ps", "--format", "json", "-a"},
		},
		{
			name:                "filtered containers",
			options:             podman.ListContainersOptions{Filter: "name=flightctl-"},
			expectedExecCommand: []string{"ps", "--format", "json", "--filter", "name=flightctl-"},
		},
		{
			name:                "all and filtered containers",
			options:             podman.ListContainersOptions{All: true, Filter: "name=flightctl-"},
			expectedExecCommand: []string{"ps", "--format", "json", "-a", "--filter", "name=flightctl-"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", tc.expectedExecCommand).Return("[]", "", 0)
			client := podman.NewClient(mockExec)
			_, err := client.ListContainers(context.Background(), tc.options)
			require.NoError(t, err)
		})
	}
}

func TestListContainers_ExecCommandCalls(t *testing.T) {
	testData, err := os.ReadFile("testdata/ps_result.json")
	require.NoError(t, err)

	testCases := []struct {
		name           string
		options        podman.ListContainersOptions
		execReturn     ExecReturn
		expectedError  error
		expextedResult []podman.Container
	}{
		{
			name:           "success with no containers",
			options:        podman.ListContainersOptions{},
			execReturn:     NewExecReturn("[]", "", 0),
			expectedError:  nil,
			expextedResult: []podman.Container{},
		},
		{
			name:          "success with multiple containers",
			options:       podman.ListContainersOptions{},
			execReturn:    NewExecReturn(string(testData), "", 0),
			expectedError: nil,
			expextedResult: []podman.Container{
				{
					ID: "300a734d0a3d66cfee538e3ad79a7cb8ec0e1807e1851a1e11140628b5f40c42",
				},
				{
					ID: "a40112b4ab01f7671e9ad9e214cb1af2e4d338c17523e3445e7573607e0a38a8",
				},
			},
		},
		{
			name:           "failure from exec command",
			options:        podman.ListContainersOptions{},
			execReturn:     NewExecReturn("", "error", 1),
			expectedError:  podman.ErrListContainers,
			expextedResult: []podman.Container{},
		},
		{
			name:           "failure from unmarshal",
			options:        podman.ListContainersOptions{},
			execReturn:     NewExecReturn("invalid json", "", 0),
			expectedError:  podman.ErrUnmarshalContainers,
			expextedResult: []podman.Container{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return(tc.execReturn.stdout, tc.execReturn.stderr, tc.execReturn.exitCode)
			client := podman.NewClient(mockExec)
			containers, err := client.ListContainers(context.Background(), tc.options)
			if tc.expectedError != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.expectedError)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expextedResult, containers)
			}
		})
	}
}
