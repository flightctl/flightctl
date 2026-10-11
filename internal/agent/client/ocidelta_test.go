package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestOCIDeltaApply(t *testing.T) {
	const (
		deltaRef = "quay.io/acme/os@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		dest     = "/tmp/os-delta/image"
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
						"oci:"+dest,
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
						"oci:"+dest,
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

			delta := NewOCIDelta(logger, mockExec, time.Minute)
			err := delta.Apply(context.Background(), deltaRef, "oci:"+dest)
			if tt.expectedError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "diff_id mismatch")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestOCIDeltaImport(t *testing.T) {
	const (
		deltaRef  = "quay.io/acme/app-delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		targetRef = "quay.io/acme/app@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)

	tests := []struct {
		name          string
		setupMocks    func(*executer.MockExecuter)
		expectedError bool
	}{
		{
			name: "When import succeeds it should import into container storage",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(),
					"oci-delta",
					"import",
					"--tag",
					targetRef,
					deltaRef,
				).Return("", "", 0)
			},
		},
		{
			name: "When import fails it should return an error wrapping stderr",
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(),
					"oci-delta",
					"import",
					"--tag",
					targetRef,
					deltaRef,
				).Return("", "Error: diff_id mismatch", 1)
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

			delta := NewOCIDelta(logger, mockExec, time.Minute)
			err := delta.Import(context.Background(), deltaRef, targetRef)
			if tt.expectedError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "diff_id mismatch")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestOCIDeltaImportImage(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, output string
		code         int
	}{
		{name: "When import returns its storage image ID it should preserve that identity", output: id + "\n"},
		{name: "When import has no identity output it should leave the identity unknown"},
		{name: "When import fails it should preserve the original error", code: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", "quay.io/acme/app:target", "/delta.oci").Return(tt.output, "import failed", tt.code)
			delta := NewOCIDelta(log.NewPrefixLogger("test"), exec, time.Minute)
			actual, err := delta.ImportImage(t.Context(), "/delta.oci", "quay.io/acme/app:target")
			if tt.code != 0 {
				require.ErrorContains(err, "import failed")
				require.Empty(actual)
			} else {
				require.NoError(err)
				require.Equal(strings.TrimSpace(tt.output), actual)
			}
		})
	}
}
