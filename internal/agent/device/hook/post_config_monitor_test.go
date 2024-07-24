package hook

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestPostConfigMonitor(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	tests := []struct {
		name string
	}{
		{
			name: "test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			log := log.NewPrefixLogger("test")
			fm, err := newPostConfigMonitor(log, mockExec)
			require.NoError(err)

			go fm.Run(ctx)

			require.Eventuallyf(func() bool {
				err := fm.AddWatch(tmpDir)
				t.Log(err)
				return err == nil
			}, testRetryTimeout, testRetryInterval, "hook not updated")

			err = fm.RemoveWatch(tmpDir)
			require.NoError(err)

			items := fm.ListWatches()
			require.Len(items, 0)

		})
	}
}
