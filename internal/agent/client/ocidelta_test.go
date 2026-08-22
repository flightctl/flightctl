package client

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestOCIDeltaApply(t *testing.T) {
	const (
		deltaRef    = "quay.io/acme/os@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		destArchive = "/tmp/os-delta/image.tar"
	)

	tests := []struct {
		name          string
		setupMocks    func(*executer.MockExecuter)
		expectedError bool
	}{
		{
			name: "When apply succeeds it should run oci-delta apply --ostree-repo without signing flags",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(
						gomock.Any(),
						"oci-delta",
						"apply",
						"--ostree-repo",
						"/ostree/repo",
						deltaRef,
						"oci-archive:"+destArchive,
					).
					Return("", "", 0)
			},
		},
		{
			name: "When apply fails it should return an error wrapping stderr",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().
					ExecuteWithContext(
						gomock.Any(),
						"oci-delta",
						"apply",
						"--ostree-repo",
						"/ostree/repo",
						deltaRef,
						"oci-archive:"+destArchive,
					).
					Return("", "Error: diff_id mismatch", 1)
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			logger.SetLevel(logrus.ErrorLevel)
			tt.setupMocks(mockExec)

			delta := NewOCIDelta(logger, mockExec)
			err := delta.Apply(context.Background(), deltaRef, destArchive)
			if tt.expectedError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "diff_id mismatch")
				return
			}
			require.NoError(t, err)
		})
	}
}
