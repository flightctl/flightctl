package dependency

import (
	"context"
	"fmt"
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

const (
	testImageV1 = "quay.io/flightctl-tests/alpine:v1"
	testImageV2 = "quay.io/flightctl-tests/alpine:v2"
	testImageV3 = "quay.io/flightctl-tests/alpine:v3"
	testOSImage = "quay.io/flightctl-tests/os:latest"
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
			expectedError: errors.ErrPrefetchNotReady,
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
			expectedError: errors.ErrPrefetchNotReady,
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
			expectedError: errors.ErrPrefetchNotReady,
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

			// register a collector that returns the test targets
			manager.RegisterOCICollector(func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
				return tt.targets, nil
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := manager.BeforeUpdate(ctx, &v1alpha1.DeviceSpec{}, &v1alpha1.DeviceSpec{})
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
				"quay.io/test/failed:latest": {done: true, err: errors.ErrNetwork},
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

			ready := manager.isTargetReady(tt.imageToCheck)
			require.Equal(tt.expectedReady, ready)

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

	manager.RegisterOCICollector(func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
		return targets, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := manager.BeforeUpdate(ctx, &v1alpha1.DeviceSpec{}, &v1alpha1.DeviceSpec{})
	require.Error(err)
	require.ErrorIs(err, errors.ErrPrefetchNotReady)

	// ensure prefetch status
	status := manager.status(ctx)
	require.Equal(2, status.TotalImages)
	require.Equal(1, len(status.PendingImages))
	require.Contains(status.PendingImages, "quay.io/test/missing:latest")

	manager.Cleanup()
}

func TestBeforeUpdate(t *testing.T) {

	tests := []struct {
		name       string
		current    *v1alpha1.DeviceSpec
		desired    *v1alpha1.DeviceSpec
		collectors []OCICollectorFn
		setupMocks func(*executer.MockExecuter)
		wantErr    error
	}{
		{
			name:    "no collectors registered",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			setupMocks: func(mockExec *executer.MockExecuter) {
				// no mock calls expected
			},
		},
		{
			name:    "empty device specs with registered collectors",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			collectors: []OCICollectorFn{
				func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
					return []OCIPullTarget{}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				// no mock calls expected since no targets returned
			},
		},
		{
			name:    "single collector with missing images",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			collectors: []OCICollectorFn{
				func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
					return []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
						{
							Type:       OCITypeImage,
							Reference:  testImageV2,
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
					}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testImageV1},
				).Return("", "", 1) // image doesn't exist
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testImageV2},
				).Return("", "", 1) // image doesn't exist
			},
			wantErr: errors.ErrPrefetchNotReady,
		},
		{
			name:    "single collector with all images present",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			collectors: []OCICollectorFn{
				func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
					return []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
						{
							Type:       OCITypeImage,
							Reference:  testImageV2,
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
					}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testImageV1},
				).Return("", "", 0) // image exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testImageV2},
				).Return("", "", 0) // image exists
			},
		},
		{
			name:    "single collector with mixed image availability",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			collectors: []OCICollectorFn{
				func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
					return []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
						{
							Type:       OCITypeImage,
							Reference:  testImageV2,
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
					}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testImageV1},
				).Return("", "", 0) // image exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testImageV2},
				).Return("", "", 1) // image doesn't exist
			},
			wantErr: errors.ErrPrefetchNotReady,
		},
		{
			name:    "multiple collectors with different image sets",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			collectors: []OCICollectorFn{
				// app collector
				func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
					return []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
						{
							Type:       OCITypeImage,
							Reference:  testImageV2,
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
					}, nil
				},
				// os collector
				func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
					return []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testOSImage,
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
					}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testImageV1},
				).Return("", "", 0) // image exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testImageV2},
				).Return("", "", 0) // image exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testOSImage},
				).Return("", "", 0) // image exists
			},
		},
		{
			name:    "collector returns error",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			collectors: []OCICollectorFn{
				func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
					return nil, fmt.Errorf("failed to collect OCI targets")
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				// no mock calls expected since collector fails
			},
			wantErr: fmt.Errorf("prefetch function 0 failed: failed to collect OCI targets"),
		},
		{
			name:    "artifact target with missing image",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			collectors: []OCICollectorFn{
				func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
					return []OCIPullTarget{
						{
							Type:       OCITypeArtifact,
							Reference:  "quay.io/test/artifact:latest",
							PullPolicy: v1alpha1.PullIfNotPresent,
						},
					}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/artifact:latest"},
				).Return("", "", 1) // artifact doesn't exist
			},
			wantErr: errors.ErrPrefetchNotReady,
		},
		{
			name:    "pull always policy with existing image",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
			collectors: []OCICollectorFn{
				func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
					return []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1alpha1.PullAlways,
						},
					}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", testImageV1},
				).Return("", "", 0) // image exists, but PullAlways means we still need to pull
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

			// Register collectors
			for _, collector := range tt.collectors {
				manager.RegisterOCICollector(collector)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := manager.BeforeUpdate(ctx, tt.current, tt.desired)
			if tt.wantErr != nil {
				require.Error(err)
				if errors.Is(tt.wantErr, errors.ErrPrefetchNotReady) {
					require.ErrorIs(err, errors.ErrPrefetchNotReady)
				} else {
					require.Contains(err.Error(), tt.wantErr.Error())
				}
				return
			}
			require.NoError(err)

			manager.Cleanup()
		})
	}
}

func TestStatusMessage(t *testing.T) {
	tests := []struct {
		name         string
		setupManager func(*prefetchManager)
		expected     string
	}{
		{
			name: "no images",
			setupManager: func(m *prefetchManager) {
				// no setup needed
			},
			expected: "No images to prefetch",
		},
		{
			name: "all images ready",
			setupManager: func(m *prefetchManager) {
				m.tasks = map[string]*prefetchTask{
					"image1": {done: true, err: nil},
					"image2": {done: true, err: nil},
				}
			},
			expected: "All 2 images ready",
		},
		{
			name: "some images pending",
			setupManager: func(m *prefetchManager) {
				m.tasks = map[string]*prefetchTask{
					"image1": {done: true, err: nil},
					"image2": {done: false, err: nil},
					"image3": {done: true, err: errors.ErrNetwork},
				}
			},
			expected: "1/3 images complete, pending: image2, image3",
		},
		{
			name: "many images pending",
			setupManager: func(m *prefetchManager) {
				m.tasks = map[string]*prefetchTask{
					"image1": {done: true, err: nil},
					"image2": {done: false, err: nil},
					"image3": {done: false, err: nil},
					"image4": {done: false, err: nil},
					"image5": {done: false, err: nil},
				}
			},
			expected: "1/5 images complete, pending: image2, image3, image4 and 1 more",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			log := log.NewPrefixLogger("test")
			timeout := util.Duration(5 * time.Second)
			manager := &prefetchManager{
				log:         log,
				pullTimeout: time.Duration(timeout),
				tasks:       make(map[string]*prefetchTask),
			}

			tt.setupManager(manager)

			ctx := context.Background()
			message := manager.StatusMessage(ctx)
			require.Equal(tt.expected, message)
		})
	}
}

func TestPullSecretCleanup(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	// simulate an application image that needs to be pulled
	mockExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", []string{"image", "exists", "registry.example.com/app:latest"},
	).Return("", "", 1)

	rw := fileio.NewReadWriter()
	podman := client.NewPodman(log, mockExec, rw, poll.Config{})
	timeout := util.Duration(5 * time.Second)
	manager := NewPrefetchManager(log, podman, timeout)

	// simulate the complete lifecycle of pull secret cleanup
	var cleanupCalls []string

	// create multiple pull secrets to test comprehensive cleanup
	appPullSecret := &client.PullSecret{
		Path: "/tmp/app-auth.json",
		Cleanup: func() {
			cleanupCalls = append(cleanupCalls, "app-auth-cleanup")
		},
	}

	// simulate a collector that would be called by applications manager
	appCollector := func(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error) {
		return []OCIPullTarget{
			{
				Type:       OCITypeImage,
				Reference:  "registry.example.com/app:latest",
				PullPolicy: v1alpha1.PullIfNotPresent,
				PullSecret: appPullSecret,
			},
		}, nil
	}

	// register the collector
	manager.RegisterOCICollector(appCollector)

	// simulate the device update flow
	current := &v1alpha1.DeviceSpec{}
	desired := &v1alpha1.DeviceSpec{
		Applications: &[]v1alpha1.ApplicationProviderSpec{
			{
				// simulated application spec
			},
		},
	}

	// not ready
	err := manager.BeforeUpdate(ctx, current, desired)
	require.ErrorIs(err, errors.ErrPrefetchNotReady)

	// no cleanup
	require.Empty(cleanupCalls)

	// simulate successful pull completion
	manager.setResult("registry.example.com/app:latest", nil)

	// verify the manager is now ready
	require.True(manager.IsReady(ctx))

	// verify calling BeforeUpdate again is now ready
	err = manager.BeforeUpdate(ctx, current, desired)
	require.NoError(err)

	// ensure cleanup
	manager.Cleanup()
	require.NotEmpty(cleanupCalls)
}

type taskState struct {
	done bool
	err  error
}
