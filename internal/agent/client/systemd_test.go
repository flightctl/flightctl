package client

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestSystemdUserClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testcases := []struct {
		expectedArgs []any
		run          func(m executer.Executer) error
	}{
		{
			expectedArgs: []any{"--user", "-M", "flightctl@", "restart", "testunit"},
			run: func(m executer.Executer) error {
				systemd := NewSystemd(m, "flightctl")
				return systemd.Restart(t.Context(), "testunit")
			},
		},
		{
			expectedArgs: []any{"restart", "testunit"},
			run: func(m executer.Executer) error {
				systemd := NewSystemd(m, v1beta1.RootUsername)
				return systemd.Restart(t.Context(), "testunit")
			},
		},
	}

	for _, tt := range testcases {
		mockExec := executer.NewMockExecuter(ctrl)
		mockExec.EXPECT().
			ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", tt.expectedArgs...).
			Return("", "", 0)

		err := tt.run(mockExec)
		require.NoError(t, err)
	}
}
