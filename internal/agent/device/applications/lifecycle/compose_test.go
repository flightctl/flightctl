package lifecycle

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
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

func TestArtifactVolumeRecordingPreservesOwnerOCITypeAndIncarnation(t *testing.T) {
	const (
		application = "app"
		volumeID    = "app-data"
		createdAt   = "2026-10-12T12:00:00Z"
		reference   = "quay.io/acme/data-bundle:v1"
		digestValue = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: "spec"}))
	lifetime := store.ApplicationLifetime(application)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", volumeID, "--format", "{{json .}}").Return(
		fmt.Sprintf(`{"Name":%q,"Driver":"local","CreatedAt":%q,"Labels":{},"Options":{}}`, volumeID, createdAt), "", 0,
	)
	podman := client.NewPodman(logger, mockExec, rw, testutil.NewPollConfig())
	action := Action{Name: application, Lifetime: lifetime, User: "alice"}
	volume := Volume{ID: volumeID, Reference: reference, OCIType: string(dependency.OCITypePodmanArtifact)}

	recordArtifactVolumeImage(context.Background(), logger, store, action, volume, podman, digestValue)
	image, found := store.ApplicationVolumeImage(application, lifetime, volumeID, createdAt)
	require.True(t, found)
	require.Equal(t, deltastatus.ApplicationImage{
		Image: reference, Owner: "alice", OCIType: string(dependency.OCITypePodmanArtifact), Digest: digestValue,
	}, image)
	_, found = store.ApplicationVolumeImage(application, lifetime, volumeID, "another-volume-incarnation")
	require.False(t, found)
	require.NoError(t, store.RemoveApplication(application))
	newLifetime, err := store.EnsureApplicationLifetime(application)
	require.NoError(t, err)
	_, found = store.ApplicationVolumeImage(application, newLifetime, volumeID, createdAt)
	require.False(t, found)
}

func TestArtifactVolumeRecordingWithInvalidInspectionWithholdsIdentity(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{"app": "spec"}))
	lifetime := store.ApplicationLifetime("app")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "app-data", "--format", "{{json .}}").Return("null", "", 0)
	podman := client.NewPodman(logger, mockExec, rw, testutil.NewPollConfig())
	action := Action{Name: "app", Lifetime: lifetime, User: "alice"}
	volume := Volume{ID: "app-data", Reference: "quay.io/acme/data:v1", OCIType: string(dependency.OCITypePodmanArtifact)}

	recordArtifactVolumeImage(context.Background(), logger, store, action, volume, podman, "sha256:artifact")
	_, found := store.ApplicationVolumeImage("app", lifetime, volume.ID, "2026-10-12T12:00:00Z")
	require.False(t, found)
}

func TestWhenComposeAddsArtifactVolumeWithoutLifetimeItShouldNotWarnAboutMissingDigest(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	appPath := "app"
	require.NoError(t, os.MkdirAll(filepath.Join(root, appPath), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, appPath, "docker-compose.yaml"), []byte("services: {}\n"), 0o600))

	logger := log.NewPrefixLogger("test")
	logger.SetLevel(logrus.WarnLevel)
	var logs bytes.Buffer
	logger.SetOutput(&logs)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().CommandContext(gomock.Any(), "podman", gomock.Any()).Return(exec.Command("podman")).AnyTimes()
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "exists", "data").Return("", "", 1)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "create", "data", "--label", client.ComposeDockerProjectLabelKey+"=app-id").Return("data", "", 0)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "data", "--format", "{{.Mountpoint}}").Return("/volume-data", "", 0)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "extract", "artifact:seed", "/volume-data").Return("", "", 0)
	mockExec.EXPECT().ExecuteWithContextFromDir(
		gomock.Any(), appPath, "podman",
		[]string{"compose", "-p", "app-id", "-f", "docker-compose.yaml", "up", "-d", "--no-recreate"},
		gomock.Any(),
	).Return("", "", 0)

	compose := NewCompose(logger, func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil }, func(v1beta1.Username) (*client.Podman, error) {
		return client.NewPodman(logger, mockExec, rw, testutil.NewPollConfig()), nil
	})
	err := compose.Execute(context.Background(), Actions{{
		Type: ActionAdd,
		ID:   "app-id",
		Name: "app",
		Path: appPath,
		Volumes: []Volume{{
			ID: "data", Reference: "artifact:seed", OCIType: string(dependency.OCITypePodmanArtifact),
		}},
	}})
	require.NoError(t, err)
	require.NotContains(t, logs.String(), "Unable to pin artifact identity")
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
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-img-vol-data", "--format", "{{.Options.type}}")).
					Return("", "", 0)
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
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("volume", "inspect", "app-local-vol-data", "--format", "{{.Options.type}}")).
					Return("", "", 0)
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
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "vol1", "--format", "{{json .}}").Return(`{"Name":"vol1","Driver":"local","CreatedAt":"2026-10-12T00:00:00Z"}`, "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "vol1", "--format", "{{.Mountpoint}}").Return(mountPath, "", 0),
		mockWriter.EXPECT().RemoveContents(mountPath).Return(nil),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("--version")).Return("podman version 5.5", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", newMatcher("artifact", "extract", "artifact:seed")).Return("", "", 0),
	)

	err := compose.ensurePodmanVolume(
		context.Background(),
		&Action{Name: "app", ID: "app", User: api.RootUsername},
		Volume{
			ID:            "vol1",
			Reference:     "artifact:seed",
			OCIType:       "PodmanArtifact",
			ReclaimPolicy: api.Retain,
		},
		nil,
		podman,
		mockWriter,
	)
	require.NoError(t, err)
}

func TestCompose_LifecycleHandler(t *testing.T) {
	const testAppID = "test-id"
	const testPath = "/test/compose/path"

	testAction := Action{
		ID:   testAppID,
		Name: "test-app",
		Path: testPath,
	}

	testCases := []struct {
		name       string
		operation  func(c *Compose, action Action) error
		setupMocks func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr    bool
	}{
		{
			name: "When Stop is called it should run podman compose stop without removing containers",
			operation: func(c *Compose, action Action) error {
				return c.Stop(context.Background(), action)
			},
			setupMocks: func(mockExec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), testPath, "podman",
					[]string{"compose", "-p", testAppID, "stop"},
				).Return("", "", 0)
			},
		},
		{
			name: "When Stop fails it should propagate the error",
			operation: func(c *Compose, action Action) error {
				return c.Stop(context.Background(), action)
			},
			setupMocks: func(mockExec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), testPath, "podman",
					[]string{"compose", "-p", testAppID, "stop"},
				).Return("", "error stopping", 1)
			},
			wantErr: true,
		},
		{
			name: "When Start is called it should run podman compose up",
			operation: func(c *Compose, action Action) error {
				return c.Start(context.Background(), action)
			},
			setupMocks: func(mockExec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), testPath, "podman", gomock.Any(),
					gomock.Any(),
				).Return("", "", 0)
			},
		},
		{
			name: "When Restart is called it should stop then start",
			operation: func(c *Compose, action Action) error {
				return c.Restart(context.Background(), action)
			},
			setupMocks: func(mockExec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				// Stop
				mockExec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), testPath, "podman",
					[]string{"compose", "-p", testAppID, "stop"},
				).Return("", "", 0)
				// Start (UpFromWorkDir)
				mockExec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), testPath, "podman", gomock.Any(),
					gomock.Any(),
				).Return("", "", 0)
			},
		},
		{
			name: "When Restart stop phase fails it should return error without starting",
			operation: func(c *Compose, action Action) error {
				return c.Restart(context.Background(), action)
			},
			setupMocks: func(mockExec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), testPath, "podman",
					[]string{"compose", "-p", testAppID, "stop"},
				).Return("", "stop failed", 1)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().CommandContext(gomock.Any(), "podman", gomock.Any()).Return(exec.Command("podman")).AnyTimes()
			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMocks(mockExec, mockRW)

			// Create a docker-compose.yaml in the test path so UpFromWorkDir can discover it.
			tmpDir := t.TempDir()
			composeDir := tmpDir + testPath
			require.NoError(os.MkdirAll(composeDir, 0o755))
			require.NoError(os.WriteFile(composeDir+"/docker-compose.yaml", []byte("services: {}"), 0o600))

			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)

			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, mockExec, readWriter, testutil.NewPollConfig())
			podmanFactory := func(user api.Username) (*client.Podman, error) {
				return podman, nil
			}
			rwFactory := func(user api.Username) (fileio.ReadWriter, error) {
				return mockRW, nil
			}

			compose := NewCompose(logger, rwFactory, podmanFactory)
			err := tc.operation(compose, testAction)
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}

	t.Run("When podman factory fails Stop should return error", func(t *testing.T) {
		require := require.New(t)
		podmanFactory := func(user api.Username) (*client.Podman, error) {
			return nil, fmt.Errorf("factory error")
		}
		compose := NewCompose(log.NewPrefixLogger("test"), nil, podmanFactory)
		err := compose.Stop(context.Background(), testAction)
		require.Error(err)
	})
}
