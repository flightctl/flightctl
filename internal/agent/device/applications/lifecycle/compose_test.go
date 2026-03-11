package lifecycle

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
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
			result := GenerateAppID(tc.input, v1beta1.CurrentProcessUsername)
			require.Equal(tc.expected, result)
		})
	}
}

func TestComposeRemoveImageBackedVolumes(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name       string
		action     Action
		setupMocks func(*executer.MockExecuter)
		wantErr    bool
	}{
		{
			name: "removes image-backed volumes on app removal",
			action: Action{
				Name: "test-app",
				ID:   "app-img-vol",
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "ls")).
					Return(`[{"Name":"app-img-vol-html"},{"Name":"app-img-vol-data"}]`, "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-img-vol-html", "--format", "{{.Driver}}")).
					Return("image", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-img-vol-data", "--format", "{{.Driver}}")).
					Return("local", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "rm", "app-img-vol-html")).
					Return("", "", 0)
			},
		},
		{
			name: "no image-backed volumes to remove",
			action: Action{
				Name: "test-app",
				ID:   "app-no-vol",
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "ls")).
					Return("[]", "", 0)
			},
		},
		{
			name: "skips non-image volumes during cleanup",
			action: Action{
				Name: "test-app",
				ID:   "app-local-vol",
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "ls")).
					Return(`[{"Name":"app-local-vol-data"}]`, "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-local-vol-data", "--format", "{{.Driver}}")).
					Return("local", "", 0)
			},
		},
		{
			name: "volume inspect failure is non-fatal",
			action: Action{
				Name: "test-app",
				ID:   "app-inspect-fail",
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("ps")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("rm")).Return("", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("network")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("pod")).Return("[]", "", 0).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("stop")).Return("", "", 0).AnyTimes()

				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "ls")).
					Return(`[{"Name":"app-inspect-fail-gone"}]`, "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-inspect-fail-gone", "--format", "{{.Driver}}")).
					Return("", "Error: no such volume", 1)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			tc.setupMocks(mockExec)

			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)

			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, mockExec, readWriter, testutil.NewPollConfig())
			podmanFactory := func(user api.Username) (*client.Podman, error) {
				return podman, nil
			}

			var rwFactory fileio.ReadWriterFactory = func(username api.Username) (fileio.ReadWriter, error) {
				return fileio.NewMockReadWriter(ctrl), nil
			}
			compose := NewCompose(logger, rwFactory, podmanFactory)

			action := tc.action
			err := compose.remove(context.Background(), &action)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestComposeEnsurePodmanVolumeRetainReseeds(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWriter := fileio.NewMockReadWriter(ctrl)
	mockExec := executer.NewMockExecuter(ctrl)
	tmpDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	logger := log.NewPrefixLogger("test")
	podman := client.NewPodman(logger, mockExec, readWriter, testutil.NewPollConfig())
	podmanFactory := func(user api.Username) (*client.Podman, error) {
		return podman, nil
	}

	var rwFactory fileio.ReadWriterFactory = func(username api.Username) (fileio.ReadWriter, error) {
		return mockWriter, nil
	}
	compose := NewCompose(logger, rwFactory, podmanFactory)

	mountPath := "/var/lib/containers/storage/volumes/app-123-vol1/_data"

	gomock.InOrder(
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "exists", "vol1").Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "vol1")).Return(mountPath, "", 0),
		mockWriter.EXPECT().RemoveContents(mountPath).Return(nil),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:seed")).Return("", "", 0),
	)

	err := compose.ensurePodmanVolume(
		context.Background(),
		Volume{
			ID:            "vol1",
			Reference:     "artifact:seed",
			ReclaimPolicy: api.Retain,
		},
		nil,
		podman,
		mockWriter,
	)
	require.NoError(t, err)
}
