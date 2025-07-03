package dependency

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestEnsureScheduled(t *testing.T) {
	tests := []struct {
		name          string
		targets       []OCIPullTarget
		setupMocks    func(*executer.MockExecuter)
		expectedError error
	}{
		{
			name:       "empty target list returns ready immediately",
			targets:    []OCIPullTarget{},
			setupMocks: func(mockExec *executer.MockExecuter) {},
		},
		{
			name: "single image already exists returns ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/existing:latest",
					PullPolicy: v1alpha1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/existing:latest"},
				).Return("", "", 0)
			},
		},
		{
			name: "single image needs pulling returns not ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/missing:latest",
					PullPolicy: v1alpha1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/missing:latest"},
				).Return("", "", 1)
			},
			expectedError: errors.ErrImagePrefetchNotReady,
		},
		{
			name: "multiple images some missing returns not ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/ready1:latest",
					PullPolicy: v1alpha1.PullIfNotPresent,
				},
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/missing1:latest",
					PullPolicy: v1alpha1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				// first exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/ready1:latest"},
				).Return("", "", 0)
				// second doesn't exist
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/missing1:latest"},
				).Return("", "", 1)
			},
			expectedError: errors.ErrImagePrefetchNotReady,
		},
		{
			name: "artifact target needs pulling returns not ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeArtifact,
					Reference:  "quay.io/test/artifact:latest",
					PullPolicy: v1alpha1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/artifact:latest"},
				).Return("", "", 1)
			},
			expectedError: errors.ErrImagePrefetchNotReady,
		},
		{
			name: "pull always policy with existing image returns ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/always:latest",
					PullPolicy: v1alpha1.PullAlways,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/always:latest"},
				).Return("", "", 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			tt.setupMocks(mockExec)

			rw := fileio.NewReadWriter()
			podman := client.NewPodman(log, mockExec, rw, poll.Config{})

			timeout := util.Duration(5 * time.Second)
			manager := NewPrefetchManager(log, podman, timeout)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := manager.EnsureScheduled(ctx, tt.targets)
			if tt.expectedError != nil {
				require.Error(err)
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)

			manager.Cleanup()
		})
	}
}

func TestIsReady(t *testing.T) {
	tests := []struct {
		name            string
		imageToCheck    string
		scheduledImages []string
		imageStates     map[string]taskState
		expectedReady   bool
	}{
		{
			name:          "image not scheduled",
			imageToCheck:  "quay.io/test/unscheduled:latest",
			expectedReady: false,
		},
		{
			name:            "image scheduled and completed successfully",
			imageToCheck:    "quay.io/test/ready:latest",
			scheduledImages: []string{"quay.io/test/ready:latest"},
			imageStates: map[string]taskState{
				"quay.io/test/ready:latest": {done: true, err: nil},
			},
			expectedReady: true,
		},
		{
			name:            "image scheduled but still in progress",
			imageToCheck:    "quay.io/test/inprogress:latest",
			scheduledImages: []string{"quay.io/test/inprogress:latest"},
			imageStates: map[string]taskState{
				"quay.io/test/inprogress:latest": {done: false, err: nil},
			},
			expectedReady: false,
		},
		{
			name:            "image scheduled but failed",
			imageToCheck:    "quay.io/test/failed:latest",
			scheduledImages: []string{"quay.io/test/failed:latest"},
			imageStates: map[string]taskState{
				"quay.io/test/failed:latest": {done: true, err: errors.ErrImagePrefetchNotReady},
			},
			expectedReady: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			rw := fileio.NewReadWriter()
			podman := client.NewPodman(log, mockExec, rw, poll.Config{})

			timeout := util.Duration(5 * time.Second)
			manager := NewPrefetchManager(log, podman, timeout)

			ctx := context.Background()

			for _, image := range tt.scheduledImages {
				state := tt.imageStates[image]
				manager.mu.Lock()
				manager.tasks[image] = &prefetchTask{
					ociType: OCITypeImage,
					done:    state.done,
					err:     state.err,
				}
				manager.mu.Unlock()
			}

			ready := manager.IsReady(ctx, tt.imageToCheck)
			require.Equal(tt.expectedReady, ready)

			manager.Cleanup()
		})
	}
}

func TestCheckReady(t *testing.T) {
	tests := []struct {
		name           string
		scheduledTasks map[string]taskState
		expectedError  error
	}{
		{
			name:          "no scheduled tasks",
			expectedError: nil,
		},
		{
			name: "all tasks completed successfully",
			scheduledTasks: map[string]taskState{
				"quay.io/test/image1:latest": {done: true, err: nil},
				"quay.io/test/image2:latest": {done: true, err: nil},
			},
			expectedError: nil,
		},
		{
			name: "some tasks still in progress",
			scheduledTasks: map[string]taskState{
				"quay.io/test/image1:latest": {done: true, err: nil},
				"quay.io/test/image2:latest": {done: false, err: nil},
			},
			expectedError: errors.ErrImagePrefetchNotReady,
		},
		{
			name: "some tasks failed with retryable error",
			scheduledTasks: map[string]taskState{
				"quay.io/test/image1:latest": {done: true, err: nil},
				"quay.io/test/image2:latest": {done: true, err: errors.ErrImagePrefetchNotReady},
			},
			expectedError: errors.ErrImagePrefetchNotReady,
		},
		{
			name: "some tasks failed with non retryable error",
			scheduledTasks: map[string]taskState{
				"quay.io/test/image1:latest": {done: true, err: nil},
				"quay.io/test/image2:latest": {done: true, err: errors.ErrNoRetry},
			},
			expectedError: errors.ErrNoRetry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			rw := fileio.NewReadWriter()
			podman := client.NewPodman(log, mockExec, rw, poll.Config{})

			timeout := util.Duration(30 * time.Second)
			manager := NewPrefetchManager(log, podman, timeout)

			ctx := context.Background()

			manager.mu.Lock()
			for image, state := range tt.scheduledTasks {
				manager.tasks[image] = &prefetchTask{
					ociType: OCITypeImage,
					done:    state.done,
					err:     state.err,
				}
			}
			manager.mu.Unlock()

			err := manager.CheckReady(ctx)
			if tt.expectedError != nil {
				require.Error(err)
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)

			manager.Cleanup()
		})
	}
}

func TestStatus(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	mockExec := executer.NewMockExecuter(ctrl)

	mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/existing:latest"},
	).Return("", "", 0)
	mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/missing:latest"},
	).Return("", "", 1)

	rw := fileio.NewReadWriter()
	podman := client.NewPodman(log, mockExec, rw, poll.Config{})

	timeout := util.Duration(5 * time.Second)
	manager := NewPrefetchManager(log, podman, timeout)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	targets := []OCIPullTarget{
		{
			Type:       OCITypeImage,
			Reference:  "quay.io/test/existing:latest",
			PullPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			Type:       OCITypeImage,
			Reference:  "quay.io/test/missing:latest",
			PullPolicy: v1alpha1.PullIfNotPresent,
		},
	}

	err := manager.EnsureScheduled(ctx, targets)
	require.Error(err)
	require.ErrorIs(err, errors.ErrImagePrefetchNotReady)

	// ensure prefetch status
	status := manager.status(ctx)
	require.Equal(2, status.TotalImages)
	require.Equal(1, len(status.PendingImages))
	require.Contains(status.PendingImages, "quay.io/test/missing:latest")

	manager.Cleanup()
}

type taskState struct {
	done bool
	err  error
}
