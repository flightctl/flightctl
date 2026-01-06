package podman_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/podman"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRemoveSecret(t *testing.T) {
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
			name:           "error from exec command",
			execReturn:     NewExecReturn("", "error", 125),
			expectedReturn: podman.ErrRemoveSecret,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "secret", "rm", "db-password").Return(tc.execReturn.stdout, tc.execReturn.stderr, tc.execReturn.exitCode)
			client := podman.NewClient(mockExec)
			err := client.RemoveSecret(context.Background(), "db-password", podman.RemoveSecretOptions{
				Ignore: false,
			})

			if tc.expectedReturn != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.expectedReturn)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
