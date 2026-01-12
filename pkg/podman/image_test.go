package podman_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/podman"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRemoveImage(t *testing.T) {
	testCases := []struct {
		name           string
		execReturn     ExecReturn
		expectedReturn error
	}{
		{
			name:           "success",
			execReturn:     NewExecReturn("", "", 0),
			expectedReturn: nil,
		},
		{
			name:           "image does not exist",
			execReturn:     NewExecReturn("", "error: no such image", 1),
			expectedReturn: podman.ErrImageDoesNotExist,
		},
		{
			name:           "error from exec command",
			execReturn:     NewExecReturn("", "error", 125),
			expectedReturn: podman.ErrRemoveImage,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "rmi", "quay.io/example/app:v1.0").Return(tc.execReturn.stdout, tc.execReturn.stderr, tc.execReturn.exitCode)
			client := podman.NewClient(mockExec)
			err := client.RemoveImage(context.Background(), "quay.io/example/app:v1.0")

			if tc.expectedReturn != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.expectedReturn)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
