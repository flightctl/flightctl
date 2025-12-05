package lifecycle

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewComposeID(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple",
			input:    "app1",
			expected: "app1-229522",
		},
		{
			name:     "with @ special character",
			input:    "app1@2",
			expected: "app1_2-819634",
		},
		{
			name:     "with : special characters",
			input:    "app-2:v2",
			expected: "app-2_v2-721985",
		},
		{
			name:     "with multiple !! special characters",
			input:    "app!!",
			expected: "app__-260528",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := client.NewComposeID(tc.input)
			require.Equal(tc.expected, result)
		})
	}
}

func TestComposeEnsurePodmanVolumeRetainSkipsReseed(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWriter := fileio.NewMockWriter(ctrl)
	mockExec := executer.NewMockExecuter(ctrl)
	readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(t.TempDir()))

	logger := log.NewPrefixLogger("test")
	podman := client.NewPodman(logger, mockExec, readWriter, testutil.NewPollConfig())
	compose := NewCompose(logger, mockWriter, podman)

	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "exists", "vol1").Return("", "", 0)

	err := compose.ensurePodmanVolume(
		context.Background(),
		Volume{
			ID:            "vol1",
			Reference:     "artifact:seed",
			ReclaimPolicy: VolumeReclaimPolicyRetain,
		},
		nil,
	)
	require.NoError(t, err)
}
