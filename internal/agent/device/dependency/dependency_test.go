package dependency

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
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
	testOSImage = "quay.io/flightctl-tests/os:latest"
)

func TestEnsureScheduled(t *testing.T) {
	tests := []struct {
		name          string
		targets       []OCIPullTarget
		setupMocks    func(*executer.MockExecuter, *resource.MockManager)
		expectedError error
	}{
		{
			name:       "empty target list returns ready immediately",
			targets:    []OCIPullTarget{},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {},
		},
		{
			name: "single image already exists returns ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/existing:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/existing:latest"},
				).Return("", "", 0)
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(1)
			},
		},
		{
			name: "single image needs pulling returns not ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/missing:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/missing:latest"},
				).Return("", "", 1)
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(1)
			},
			expectedError: errors.ErrPrefetchNotReady,
		},
		{
			name: "multiple images some missing returns not ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/ready1:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/missing1:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {
				// first exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/ready1:latest"},
				).Return("", "", 0)
				// second doesn't exist
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/missing1:latest"},
				).Return("", "", 1)
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(1)
			},
			expectedError: errors.ErrPrefetchNotReady,
		},
		{
			name: "artifact target needs pulling returns not ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeArtifact,
					Reference:  "quay.io/test/artifact:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/artifact:latest"},
				).Return("", "", 1)
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(1)
			},
			expectedError: errors.ErrPrefetchNotReady,
		},
		{
			name: "artifact already exists returns ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeArtifact,
					Reference:  "quay.io/test/existing-artifact:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/existing-artifact:latest"},
				).Return("", "", 0)
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(1)
			},
		},
		{
			name: "mixed image and artifact targets with some missing",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/existing-image:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
				{
					Type:       OCITypeArtifact,
					Reference:  "quay.io/test/missing-artifact:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {
				// image exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/existing-image:latest"},
				).Return("", "", 0)
				// artifact doesn't exist
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/missing-artifact:latest"},
				).Return("", "", 1)
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(1)
			},
			expectedError: errors.ErrPrefetchNotReady,
		},
		{
			name: "mixed image and artifact targets all existing",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/existing-image:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
				{
					Type:       OCITypeArtifact,
					Reference:  "quay.io/test/existing-artifact:latest",
					PullPolicy: v1beta1.PullIfNotPresent,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {
				// image exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/existing-image:latest"},
				).Return("", "", 0)
				// artifact exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/existing-artifact:latest"},
				).Return("", "", 0)
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(1)
			},
		},
		{
			name: "pull always policy with existing artifact returns ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeArtifact,
					Reference:  "quay.io/test/always-artifact:latest",
					PullPolicy: v1beta1.PullAlways,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/always-artifact:latest"},
				).Return("", "", 0)
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(1)
			},
		},
		{
			name: "pull always policy with existing image returns ready",
			targets: []OCIPullTarget{
				{
					Type:       OCITypeImage,
					Reference:  "quay.io/test/always:latest",
					PullPolicy: v1beta1.PullAlways,
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter, mockResourceManager *resource.MockManager) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/always:latest"},
				).Return("", "", 0)
				mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(1)
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
			mockResourceManager := resource.NewMockManager(ctrl)
			tt.setupMocks(mockExec, mockResourceManager)

			rw := fileio.NewReadWriter()
			podman := client.NewPodman(log, mockExec, rw, poll.Config{})

			timeout := util.Duration(5 * time.Second)
			manager := NewPrefetchManager(log, podman, client.NewSkopeo(log, mockExec, rw), rw, timeout, mockResourceManager, poll.Config{})

			// register a collector that returns the test targets
			manager.RegisterOCICollector(newTestOCICollector(func(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*OCICollection, error) {
				return &OCICollection{Targets: tt.targets}, nil
			}))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := manager.BeforeUpdate(ctx, &v1beta1.DeviceSpec{}, &v1beta1.DeviceSpec{})
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
			mockResourceManager := resource.NewMockManager(ctrl)
			rw := fileio.NewReadWriter()
			podman := client.NewPodman(log, mockExec, rw, poll.Config{})

			timeout := util.Duration(5 * time.Second)
			manager := NewPrefetchManager(log, podman, client.NewSkopeo(log, mockExec, rw), rw, timeout, mockResourceManager, poll.Config{})

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

			// Check if target is ready
			manager.mu.Lock()
			task, ok := manager.tasks[tt.imageToCheck]
			ready := ok && task.err == nil && task.done
			manager.mu.Unlock()

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

	mockResourceManager := resource.NewMockManager(ctrl)
	mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()

	rw := fileio.NewReadWriter()
	podman := client.NewPodman(log, mockExec, rw, poll.Config{})

	timeout := util.Duration(5 * time.Second)
	manager := NewPrefetchManager(log, podman, client.NewSkopeo(log, mockExec, rw), rw, timeout, mockResourceManager, poll.Config{})

	targets := []OCIPullTarget{
		{
			Type:       OCITypeImage,
			Reference:  "quay.io/test/existing:latest",
			PullPolicy: v1beta1.PullIfNotPresent,
		},
		{
			Type:       OCITypeImage,
			Reference:  "quay.io/test/missing:latest",
			PullPolicy: v1beta1.PullIfNotPresent,
		},
	}

	manager.RegisterOCICollector(newTestOCICollector(func(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*OCICollection, error) {
		return &OCICollection{Targets: targets}, nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := manager.BeforeUpdate(ctx, &v1beta1.DeviceSpec{}, &v1beta1.DeviceSpec{})
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
		collectors []func() (*OCICollection, error)
		setupMocks func(*executer.MockExecuter)
		wantErr    error
	}{
		{
			name: "no collectors registered",
			setupMocks: func(mockExec *executer.MockExecuter) {
				// no mock calls expected
			},
		},
		{
			name: "empty device specs with registered collectors",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{}}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				// no mock calls expected since no targets returned
			},
		},
		{
			name: "single collector with missing images",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1beta1.PullIfNotPresent,
						},
						{
							Type:       OCITypeImage,
							Reference:  testImageV2,
							PullPolicy: v1beta1.PullIfNotPresent,
						},
					}}, nil
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
			name: "single collector with all images present",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1beta1.PullIfNotPresent,
						},
						{
							Type:       OCITypeImage,
							Reference:  testImageV2,
							PullPolicy: v1beta1.PullIfNotPresent,
						},
					}}, nil
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
			name: "single collector with mixed image availability",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1beta1.PullIfNotPresent,
						},
						{
							Type:       OCITypeImage,
							Reference:  testImageV2,
							PullPolicy: v1beta1.PullIfNotPresent,
						},
					}}, nil
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
			name: "multiple collectors with different image sets",
			collectors: []func() (*OCICollection, error){
				// app collector
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1beta1.PullIfNotPresent,
						},
						{
							Type:       OCITypeImage,
							Reference:  testImageV2,
							PullPolicy: v1beta1.PullIfNotPresent,
						},
					}}, nil
				},
				// os collector
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testOSImage,
							PullPolicy: v1beta1.PullIfNotPresent,
						},
					}}, nil
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
			name: "collector returns error",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return nil, fmt.Errorf("failed to collect OCI targets")
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				// no mock calls expected since collector fails
			},
			wantErr: fmt.Errorf("prefetch collector 0 failed: failed to collect OCI targets"),
		},
		{
			name: "artifact target with missing image",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeArtifact,
							Reference:  "quay.io/test/artifact:latest",
							PullPolicy: v1beta1.PullIfNotPresent,
						},
					}}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/artifact:latest"},
				).Return("", "", 1) // artifact doesn't exist
			},
			wantErr: errors.ErrPrefetchNotReady,
		},
		{
			name: "artifact target with existing artifact",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeArtifact,
							Reference:  "quay.io/test/existing-artifact:latest",
							PullPolicy: v1beta1.PullIfNotPresent,
						},
					}}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/existing-artifact:latest"},
				).Return("", "", 0) // artifact exists
			},
		},
		{
			name: "mixed image and artifact targets with some missing",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  "quay.io/test/existing-image:latest",
							PullPolicy: v1beta1.PullIfNotPresent,
						},
						{
							Type:       OCITypeArtifact,
							Reference:  "quay.io/test/missing-artifact:latest",
							PullPolicy: v1beta1.PullIfNotPresent,
						},
					}}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/existing-image:latest"},
				).Return("", "", 0) // image exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/missing-artifact:latest"},
				).Return("", "", 1) // artifact doesn't exist
			},
			wantErr: errors.ErrPrefetchNotReady,
		},
		{
			name: "mixed image and artifact targets all existing",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  "quay.io/test/existing-image:latest",
							PullPolicy: v1beta1.PullIfNotPresent,
						},
						{
							Type:       OCITypeArtifact,
							Reference:  "quay.io/test/existing-artifact:latest",
							PullPolicy: v1beta1.PullIfNotPresent,
						},
					}}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"image", "exists", "quay.io/test/existing-image:latest"},
				).Return("", "", 0) // image exists
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/existing-artifact:latest"},
				).Return("", "", 0) // artifact exists
			},
		},
		{
			name: "pull always policy with existing artifact",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeArtifact,
							Reference:  "quay.io/test/always-artifact:latest",
							PullPolicy: v1beta1.PullAlways,
						},
					}}, nil
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(
					gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/test/always-artifact:latest"},
				).Return("", "", 0) // artifact exists, but PullAlways means we still need to pull
			},
		},
		{
			name: "pull always policy with existing image",
			collectors: []func() (*OCICollection, error){
				func() (*OCICollection, error) {
					return &OCICollection{Targets: []OCIPullTarget{
						{
							Type:       OCITypeImage,
							Reference:  testImageV1,
							PullPolicy: v1beta1.PullAlways,
						},
					}}, nil
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

			mockResourceManager := resource.NewMockManager(ctrl)
			mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()

			rw := fileio.NewReadWriter()
			podman := client.NewPodman(log, mockExec, rw, poll.Config{})

			timeout := util.Duration(5 * time.Second)
			manager := NewPrefetchManager(log, podman, client.NewSkopeo(log, mockExec, rw), rw, timeout, mockResourceManager, poll.Config{})

			// Register collectors
			for _, collector := range tt.collectors {
				manager.RegisterOCICollector(newTestOCICollector(func(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*OCICollection, error) {
					return collector()
				}))
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := manager.BeforeUpdate(ctx, &v1beta1.DeviceSpec{}, &v1beta1.DeviceSpec{})
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
					"image3": {done: false, err: nil},
				}
			},
			expected: "1/3 images complete, pending: image2, image3",
		},
		{
			name: "some images retrying with reason",
			setupManager: func(m *prefetchManager) {
				m.tasks = map[string]*prefetchTask{
					"image1": {done: true, err: nil},
					"image2": {done: false, err: nil},
					"image3": {done: false, err: errors.ErrNetwork},
				}
			},
			expected: "1/3 images complete, retrying: image3: network, and 1 more pending",
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

	mockResourceManager := resource.NewMockManager(ctrl)
	mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()

	rw := fileio.NewReadWriter()
	podman := client.NewPodman(log, mockExec, rw, poll.Config{})
	timeout := util.Duration(5 * time.Second)
	manager := NewPrefetchManager(log, podman, client.NewSkopeo(log, mockExec, rw), rw, timeout, mockResourceManager, poll.Config{})

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
	appCollector := func(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*OCICollection, error) {
		return &OCICollection{Targets: []OCIPullTarget{
			{
				Type:       OCITypeImage,
				Reference:  "registry.example.com/app:latest",
				PullPolicy: v1beta1.PullIfNotPresent,
				PullSecret: appPullSecret,
			},
		}}, nil
	}

	// register the collector
	manager.RegisterOCICollector(newTestOCICollector(appCollector))

	// simulate the device update flow
	current := &v1beta1.DeviceSpec{}
	desired := &v1beta1.DeviceSpec{
		Applications: &[]v1beta1.ApplicationProviderSpec{
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

// testOCICollector is a test helper that implements OCICollector interface
type testOCICollector struct {
	collectFn func(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*OCICollection, error)
}

func (t *testOCICollector) CollectOCITargets(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*OCICollection, error) {
	return t.collectFn(ctx, current, desired)
}

func newTestOCICollector(fn func(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*OCICollection, error)) OCICollector {
	return &testOCICollector{collectFn: fn}
}

func TestCleanupPartialLayers(t *testing.T) {
	tests := []struct {
		name            string
		setupDirs       []string
		setupDirTimes   []time.Time
		activePulls     bool
		emptyTmpDir     bool
		expectedRemoved []string
		expectedKept    []string
	}{
		{
			name: "removes all container_images_storage directories",
			setupDirs: []string{
				"container_images_storage123",
				"container_images_storage456",
				"container_images_storage789",
			},
			setupDirTimes: []time.Time{
				time.Now().Add(-2 * time.Hour),
				time.Now().Add(-1 * time.Hour),
				time.Now().Add(-5 * time.Minute),
			},
			expectedRemoved: []string{
				"container_images_storage123",
				"container_images_storage456",
				"container_images_storage789",
			},
		},
		{
			name: "cleanup proceeds even with active pulls",
			setupDirs: []string{
				"container_images_storage123",
				"container_images_storage456",
			},
			setupDirTimes: []time.Time{
				time.Now().Add(-2 * time.Hour),
				time.Now().Add(-1 * time.Hour),
			},
			activePulls: true,
			expectedRemoved: []string{
				"container_images_storage123",
				"container_images_storage456",
			},
		},
		{
			name: "removes single directory",
			setupDirs: []string{
				"container_images_storage123",
			},
			setupDirTimes: []time.Time{
				time.Now().Add(-1 * time.Hour),
			},
			expectedRemoved: []string{
				"container_images_storage123",
			},
		},
		{
			name: "ignores non-container_images_storage directories",
			setupDirs: []string{
				"container_images_storage123",
				"container_images_storage456",
				"some_other_dir",
				"random_temp",
			},
			setupDirTimes: []time.Time{
				time.Now().Add(-2 * time.Hour),
				time.Now().Add(-1 * time.Hour),
				time.Now().Add(-30 * time.Minute),
				time.Now().Add(-15 * time.Minute),
			},
			expectedRemoved: []string{
				"container_images_storage123",
				"container_images_storage456",
			},
			expectedKept: []string{
				"some_other_dir",
				"random_temp",
			},
		},
		{
			name:      "handles empty directory",
			setupDirs: []string{},
		},
		{
			name: "sorts directories correctly by time",
			setupDirs: []string{
				"container_images_storage_newer",
				"container_images_storage_oldest",
				"container_images_storage_middle",
			},
			setupDirTimes: []time.Time{
				time.Now().Add(-1 * time.Minute),
				time.Now().Add(-3 * time.Hour),
				time.Now().Add(-30 * time.Minute),
			},
			expectedRemoved: []string{
				"container_images_storage_oldest",
				"container_images_storage_middle",
				"container_images_storage_newer",
			},
		},
		{
			name:        "skips cleanup when tmpdir is empty",
			setupDirs:   []string{},
			emptyTmpDir: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			// create temp directory and setup fileio
			rootDir := t.TempDir()
			tmpDir := filepath.Join(rootDir, "var", "tmp")
			err := os.MkdirAll(tmpDir, 0755)
			require.NoError(err)

			// create test directories
			for i, dir := range tt.setupDirs {
				dirPath := filepath.Join(tmpDir, dir)
				err := os.MkdirAll(dirPath, 0755)
				require.NoError(err)

				// set modification time if provided
				if i < len(tt.setupDirTimes) {
					err = os.Chtimes(dirPath, tt.setupDirTimes[i], tt.setupDirTimes[i])
					require.NoError(err)
				}
			}

			// setup fileio with root dir
			rw := fileio.NewReadWriter()
			rw.SetRootdir(rootDir)

			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)

			// setup expectations for GetImageCopyTmpDir
			// return /var/tmp or empty based on test case
			tmpDirResponse := "/var/tmp"
			if tt.emptyTmpDir {
				tmpDirResponse = ""
			}
			mockExec.EXPECT().
				ExecuteWithContext(gomock.Any(), "podman", "info", "--format", "{{.Store.ImageCopyTmpDir}}").
				Return(tmpDirResponse, "", 0).
				AnyTimes()

			// create podman client
			podmanClient := client.NewPodman(log, mockExec, rw, poll.Config{})

			// create prefetch manager
			pm := &prefetchManager{
				log:          log,
				podmanClient: podmanClient,
				readWriter:   rw,
				pullTimeout:  5 * time.Minute,
				tasks:        make(map[string]*prefetchTask),
				tmpDir:       "", // always start without cache to test fetching
			}

			// setup active pulls if needed
			if tt.activePulls {
				pm.tasks["test-image"] = &prefetchTask{
					cancelFn: func() {},
					done:     false,
				}
			}

			// run cleanup
			ctx := context.Background()
			err = pm.cleanupPartialLayers(ctx)
			require.NoError(err)

			// verify expected directories exist
			if tt.expectedKept != nil {
				for _, dir := range tt.expectedKept {
					dirPath := filepath.Join(tmpDir, dir)
					_, err := os.Stat(dirPath)
					require.NoError(err, "expected directory %s to exist", dir)
				}
			}

			// verify expected directories were removed
			for _, dir := range tt.expectedRemoved {
				dirPath := filepath.Join(tmpDir, dir)
				_, err := os.Stat(dirPath)
				require.True(os.IsNotExist(err), "expected directory %s to be removed", dir)
			}
		})
	}
}

func TestSetResultAfterCleanup(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	logger.SetLevel(logrus.DebugLevel)

	mockExec := executer.NewMockExecuter(ctrl)
	mockResourceManager := resource.NewMockManager(ctrl)
	mockResourceManager.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()

	readWriter := fileio.NewReadWriter()
	podmanClient := client.NewPodman(logger, mockExec, readWriter, poll.Config{})
	skopeoClient := client.NewSkopeo(logger, mockExec, readWriter)
	pullTimeout := util.Duration(5 * time.Minute)

	pm := NewPrefetchManager(logger, podmanClient, skopeoClient, readWriter, pullTimeout, mockResourceManager, poll.Config{})

	testImage := "quay.io/test/image:latest"
	pm.tasks[testImage] = &prefetchTask{
		ociType: OCITypeImage,
		done:    false,
	}

	pm.Cleanup()

	require.Empty(pm.tasks)
	pm.setResult(testImage, nil)
	// ensure still empty
	require.Empty(pm.tasks)
	pm.setResult(testImage, fmt.Errorf("test error"))
	// ensure still empty
	require.Empty(pm.tasks)
}

func TestSelectiveCleanup(t *testing.T) {
	logger := log.NewPrefixLogger("test")
	pullTimeout := util.Duration(5 * time.Minute)

	manager := &prefetchManager{
		log:         logger,
		pullTimeout: time.Duration(pullTimeout),
		tasks:       make(map[string]*prefetchTask),
		queue:       make(chan string, maxQueueSize),
	}

	initialImages := []string{
		"registry.example.com/app:v1",
		"registry.example.com/sidecar:v1",
		"registry.example.com/database:v1",
	}
	for _, ref := range initialImages {
		manager.tasks[ref] = &prefetchTask{
			done:     false,
			cancelFn: func() {},
		}
	}

	newTargets := []OCIPullTarget{
		{Type: OCITypeImage, Reference: "registry.example.com/app:v1"},
		{Type: OCITypeImage, Reference: "registry.example.com/sidecar:v1"},
		{Type: OCITypeImage, Reference: "registry.example.com/cache:v1"},
	}

	newRefs := make(map[string]struct{}, len(newTargets))
	for _, target := range newTargets {
		newRefs[target.Reference] = struct{}{}
	}

	manager.mu.Lock()
	manager.cleanupStaleTasks(newRefs)
	manager.mu.Unlock()

	// app and sidecar tasks should still exist
	_, appExists := manager.tasks["registry.example.com/app:v1"]
	require.True(t, appExists, "app:v1 task should be preserved")

	_, sidecarExists := manager.tasks["registry.example.com/sidecar:v1"]
	require.True(t, sidecarExists, "sidecar:v1 task should be preserved")

	// database should be removed
	_, databaseExists := manager.tasks["registry.example.com/database:v1"]
	require.False(t, databaseExists, "database:v1 task should be removed")

	// cache task should NOT exist yet
	_, cacheExists := manager.tasks["registry.example.com/cache:v1"]
	require.False(t, cacheExists, "cache:v1 task should not exist (not scheduled)")

	require.Equal(t, 2, len(manager.tasks), "should have exactly 2 tasks remaining")
}

func TestTargetChangeCleanup(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name           string
		initialTargets []string
		newTargets     []string
		expectCleanup  bool
	}{
		{
			name:           "different images triggers cleanup",
			initialTargets: []string{"registry.example.com/app:v1"},
			newTargets:     []string{"registry.example.com/app:v2"},
			expectCleanup:  true,
		},
		{
			name:           "same images does not trigger cleanup",
			initialTargets: []string{"registry.example.com/app:v1"},
			newTargets:     []string{"registry.example.com/app:v1"},
			expectCleanup:  false,
		},
		{
			name:           "first call with no previous targets triggers change detection",
			initialTargets: nil,
			newTargets:     []string{"registry.example.com/app:v1"},
			expectCleanup:  true, // targets changed but selective cleanup won't remove anything
		},
		{
			name:           "adding image triggers cleanup",
			initialTargets: []string{"registry.example.com/app:v1"},
			newTargets:     []string{"registry.example.com/app:v1", "registry.example.com/sidecar:v1"},
			expectCleanup:  true,
		},
		{
			name:           "removing image triggers cleanup",
			initialTargets: []string{"registry.example.com/app:v1", "registry.example.com/sidecar:v1"},
			newTargets:     []string{"registry.example.com/app:v1"},
			expectCleanup:  true,
		},
		{
			name:           "version bump with same images does not cleanup",
			initialTargets: []string{"registry.example.com/app:stable"},
			newTargets:     []string{"registry.example.com/app:stable"},
			expectCleanup:  false,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			logger := log.NewPrefixLogger("test")
			pullTimeout := util.Duration(5 * time.Minute)

			manager := &prefetchManager{
				log:         logger,
				pullTimeout: time.Duration(pullTimeout),
				tasks:       make(map[string]*prefetchTask),
				queue:       make(chan string, maxQueueSize),
			}

			if len(tt.initialTargets) > 0 {
				for _, ref := range tt.initialTargets {
					manager.tasks[ref] = &prefetchTask{done: false}
				}
			}

			initialTaskCount := len(manager.tasks)

			newTargets := make([]OCIPullTarget, len(tt.newTargets))
			for i, ref := range tt.newTargets {
				newTargets[i] = OCIPullTarget{
					Type:       OCITypeImage,
					Reference:  ref,
					PullPolicy: v1beta1.PullIfNotPresent,
				}
			}

			newRefs := make(map[string]struct{}, len(newTargets))
			for _, target := range newTargets {
				newRefs[target.Reference] = struct{}{}
			}

			manager.mu.Lock()
			targetsChanged := manager.isTargetsChanged(newRefs)
			manager.mu.Unlock()

			require.Equal(tt.expectCleanup, targetsChanged,
				"target change detection should match expectation")

			// simulate selective cleanup if targets changed
			if targetsChanged {
				manager.mu.Lock()
				manager.cleanupStaleTasks(newRefs)
				manager.mu.Unlock()

				// ensure only stale tasks were removed
				for _, ref := range tt.newTargets {
					if slices.Contains(tt.initialTargets, ref) {
						_, exists := manager.tasks[ref]
						require.True(exists, "existing task for %s should be preserved", ref)
					}
				}

				// tasks for removed targets should be gone
				for _, ref := range tt.initialTargets {
					if !slices.Contains(tt.newTargets, ref) {
						_, exists := manager.tasks[ref]
						require.False(exists, "stale task for %s should be removed", ref)
					}
				}
			} else {
				require.Equal(initialTaskCount, len(manager.tasks),
					"tasks should not be cleared when targets don't change")
			}
		})
	}
}

func BenchmarkPrefetchTargetChange(b *testing.B) {
	benchmarks := []struct {
		name         string
		initialCount int
		newCount     int
		overlap      int
	}{
		{"small_partial_change", 5, 5, 3},
		{"medium_partial_change", 20, 20, 10},
		{"large_partial_change", 100, 100, 50},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			logger := log.NewPrefixLogger("bench")
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				manager := &prefetchManager{
					log:         logger,
					pullTimeout: 5 * time.Minute,
					tasks:       make(map[string]*prefetchTask),
					queue:       make(chan string, maxQueueSize),
				}

				for j := 0; j < bm.initialCount; j++ {
					ref := fmt.Sprintf("registry.example.com/image-%d:v1", j)
					manager.tasks[ref] = &prefetchTask{
						done:     false,
						cancelFn: func() {},
					}
				}

				// new targets
				allTargets := make([]OCIPullTarget, bm.newCount)
				for j := 0; j < bm.newCount; j++ {
					var ref string
					if j < bm.overlap {
						ref = fmt.Sprintf("registry.example.com/image-%d:v1", j)
					} else {
						ref = fmt.Sprintf("registry.example.com/new-image-%d:v1", j)
					}
					allTargets[j] = OCIPullTarget{
						Type:      OCITypeImage,
						Reference: ref,
					}
				}

				b.StartTimer()
				manager.mu.Lock()
				newRefs := make(map[string]struct{}, len(allTargets))
				for _, target := range allTargets {
					newRefs[target.Reference] = struct{}{}
				}
				if manager.isTargetsChanged(newRefs) {
					manager.cleanupStaleTasks(newRefs)
				}
				manager.mu.Unlock()
			}
		})
	}
}

func TestDetectOCIType(t *testing.T) {
	tests := []struct {
		name         string
		manifest     *client.OCIManifest
		expectedType OCIType
		expectedErr  bool
	}{
		{
			name:         "nil manifest returns error",
			manifest:     nil,
			expectedType: "",
			expectedErr:  true,
		},
		{
			name: "standard OCI image",
			manifest: &client.OCIManifest{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Config: &client.OCIDescriptor{
					MediaType: "application/vnd.oci.image.config.v1+json",
					Digest:    "sha256:abc123",
					Size:      677,
				},
			},
			expectedType: OCITypeImage,
			expectedErr:  false,
		},
		{
			name: "OCI artifact with artifactType field",
			manifest: &client.OCIManifest{
				MediaType:    "application/vnd.oci.image.manifest.v1+json",
				ArtifactType: "application/vnd.example.artifact",
				Config: &client.OCIDescriptor{
					MediaType: "application/vnd.oci.empty.v1+json",
					Digest:    "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
					Size:      2,
				},
			},
			expectedType: OCITypeArtifact,
			expectedErr:  false,
		},
		{
			name: "OCI artifact with empty config",
			manifest: &client.OCIManifest{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Config: &client.OCIDescriptor{
					MediaType: "application/vnd.oci.empty.v1+json",
					Digest:    "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
					Size:      2,
				},
			},
			expectedType: OCITypeArtifact,
			expectedErr:  false,
		},
		{
			name: "OCI artifact with custom config media type",
			manifest: &client.OCIManifest{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Config: &client.OCIDescriptor{
					MediaType: "application/vnd.example.config",
					Digest:    "sha256:xyz789",
					Size:      100,
				},
			},
			expectedType: OCITypeArtifact,
			expectedErr:  false,
		},
		{
			name: "manifest index (multi-platform image)",
			manifest: &client.OCIManifest{
				MediaType: "application/vnd.oci.image.index.v1+json",
				Manifests: []byte(`[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aaa111","size":2567}]`),
			},
			expectedType: OCITypeImage,
			expectedErr:  false,
		},
		{
			name: "ML model artifact (from research)",
			manifest: &client.OCIManifest{
				MediaType:    "application/vnd.oci.image.manifest.v1+json",
				ArtifactType: "application/vnd.oci.image.index.v1+json",
				Config: &client.OCIDescriptor{
					MediaType: "application/vnd.oci.empty.v1+json",
					Digest:    "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
					Size:      2,
				},
			},
			expectedType: OCITypeArtifact,
			expectedErr:  false,
		},
		{
			name: "text artifact (from research)",
			manifest: &client.OCIManifest{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Config: &client.OCIDescriptor{
					MediaType: "application/vnd.oci.empty.v1+json",
					Digest:    "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
					Size:      2,
				},
			},
			expectedType: OCITypeArtifact,
			expectedErr:  false,
		},
		{
			name: "manifest without config defaults to image",
			manifest: &client.OCIManifest{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
			},
			expectedType: OCITypeImage,
			expectedErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := detectOCIType(tt.manifest)

			if tt.expectedErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedType, result)
			}
		})
	}
}
