package hook

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

var (
	testRetryTimeout  = 5 * time.Second
	testRetryInterval = 100 * time.Millisecond
)

func TestPreHookFileMonitor(t *testing.T) {
	require := require.New(t)
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
			fm := newPreConfigMonitor(log, mockExec)
			go fm.Run(ctx)

			require.Eventuallyf(func() bool {
				err := fm.AddWatch("/var/lib/stuff")
				return err == nil
			}, testRetryTimeout, testRetryInterval, "hook not updated")

			err := fm.RemoveWatch("/var/lib/stuff")
			require.NoError(err)

			err = fm.RemoveWatch("/var/lib/stuff")
			require.NoError(err)

			items := fm.ListWatches()
			require.Len(items, 0)

		})
	}
}
