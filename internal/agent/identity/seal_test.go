package identity

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestPasswordSealer_VerifyFromPath tests verification of sealed credentials
func TestPasswordSealer_VerifyFromPath(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("successful verification", func(t *testing.T) {
		mockRW := fileio.NewMockReadWriter(ctrl)
		mockExec := executer.NewMockExecuter(ctrl)
		log := log.NewPrefixLogger("test")

		mockExec.EXPECT().LookPath(gomock.Any()).Return("/usr/bin/systemd-creds", nil).AnyTimes()
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), gomock.Any(), "--version").
			Return("systemd 252\n", "", 0).AnyTimes()
		mockRW.EXPECT().PathExists("/test/sealed").Return(true, nil)
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), gomock.Any(), "decrypt", "/test/sealed", "-").
			Return("password", "", 0)

		sealer, _ := NewSealer(log, mockRW, mockExec)
		err := sealer.VerifyFromPath(context.Background(), "/test/sealed")
		require.NoError(err)
	})

	t.Run("file not found", func(t *testing.T) {
		mockRW := fileio.NewMockReadWriter(ctrl)
		mockExec := executer.NewMockExecuter(ctrl)
		log := log.NewPrefixLogger("test")

		mockExec.EXPECT().LookPath(gomock.Any()).Return("/usr/bin/systemd-creds", nil).AnyTimes()
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), gomock.Any(), "--version").
			Return("systemd 252\n", "", 0).AnyTimes()
		mockRW.EXPECT().PathExists("/test/sealed").Return(false, nil)

		sealer, _ := NewSealer(log, mockRW, mockExec)
		err := sealer.VerifyFromPath(context.Background(), "/test/sealed")
		require.Error(err)
		require.Contains(err.Error(), "sealed file not found")
	})
}
