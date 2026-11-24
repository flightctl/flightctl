package imagepruning

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestManager_getImageReferencesFromSpecs(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	rootPodmanClient := client.NewPodman(log, mockExec, readWriter, poll.Config{})
	podmanClientFactory := func(user v1beta1.Username) (*client.Podman, error) {
		return rootPodmanClient, nil
	}
	rwFactory := func(user v1beta1.Username) (fileio.ReadWriter, error) {
		return readWriter, nil
	}
	mockSpecManager := spec.NewMockManager(ctrl)
	enabled := true
	config := config.ImagePruning{Enabled: &enabled}

	m := New(podmanClientFactory, rootPodmanClient, nil, mockSpecManager, rwFactory, readWriter, log, config, "/tmp").(*manager)

	// Helper to mock image existence checks for nested target extraction
	// For most tests, we'll mock that images don't exist locally (so nested extraction is skipped)
	mockImageNotExists := func(imageRef string) {
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", imageRef}).
			Return("", "", 1).AnyTimes() // exit code 1 = image doesn't exist
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", imageRef}).
			Return("", "", 1).AnyTimes() // exit code 1 = artifact doesn't exist
	}

	testCases := []struct {
		name       string
		setupMocks func(*executer.MockExecuter, *spec.MockManager)
		want       []ImageRef
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "success with current and desired specs",
			setupMocks: func(mockExec *executer.MockExecuter, mock *spec.MockManager) {
				// Mock nested target extraction - images don't exist locally (so extraction is skipped)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:current"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:current"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:desired"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:desired"}).
					Return("", "", 1).AnyTimes()
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:current"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
					},
				}

				desiredContainerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(desiredContainerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:desired"}))
				var desiredAppSpec v1beta1.ApplicationProviderSpec
				require.NoError(desiredAppSpec.FromContainerApplication(desiredContainerApp))
				desiredDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{desiredAppSpec}),
					},
				}

				mock.EXPECT().Read(spec.Current).Return(currentDevice, nil)
				mock.EXPECT().Read(spec.Desired).Return(desiredDevice, nil)
			},
			want: []ImageRef{
				{Owner: v1beta1.CurrentProcessUsername, Image: "quay.io/example/app:current", Type: RefTypePodman},
				{Owner: v1beta1.CurrentProcessUsername, Image: "quay.io/example/app:desired", Type: RefTypePodman},
			},
		},
		{
			name: "success with current spec only (no desired)",
			setupMocks: func(mockExec *executer.MockExecuter, mock *spec.MockManager) {
				// Mock nested target extraction - image doesn't exist locally (so extraction is skipped)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:current"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:current"}).
					Return("", "", 1).AnyTimes()
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:current"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
					},
				}

				// Mock nested target extraction - image doesn't exist locally (so extraction is skipped)
				mockImageNotExists("quay.io/example/app:current")

				mock.EXPECT().Read(spec.Current).Return(currentDevice, nil)
				mock.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found"))
			},
			want: []ImageRef{{Owner: v1beta1.CurrentProcessUsername, Image: "quay.io/example/app:current", Type: RefTypePodman}},
		},
		{
			name: "error reading current spec",
			setupMocks: func(mockExec *executer.MockExecuter, mock *spec.MockManager) {
				mock.EXPECT().Read(spec.Current).Return(nil, errors.New("failed to read current spec"))
				// Desired is not read if current fails (since current is required)
			},
			wantErr:    true,
			wantErrMsg: "failed to read current spec",
		},
		{
			name: "error extracting images from current spec",
			setupMocks: func(mockExec *executer.MockExecuter, mock *spec.MockManager) {
				// Return a device with invalid spec structure (empty ApplicationProviderSpec with no union set)
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{}, // Empty spec - no From* method called, will fail extraction
						}),
					},
				}
				mock.EXPECT().Read(spec.Current).Return(currentDevice, nil)
				// Desired is read but extraction fails on current, so desired read happens but is not processed
				mock.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found"))
			},
			wantErr:    true,
			wantErrMsg: "extracting references from spec",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setupMocks(mockExec, mockSpecManager)

			// Read current device
			currentDevice, currentErr := mockSpecManager.Read(spec.Current)
			if currentErr != nil {
				if tc.wantErr {
					// Expected error reading current - verify it matches
					require.Error(currentErr)
					if tc.wantErrMsg != "" {
						// Check if error message contains the expected substring
						require.Contains(currentErr.Error(), tc.wantErrMsg, "error message should contain expected substring")
					}
					return
				}
				require.NoError(currentErr)
			}

			// Read desired device only if current read succeeded
			var desiredDevice *v1beta1.Device
			if currentErr == nil {
				desiredDevice, _ = mockSpecManager.Read(spec.Desired)
			}

			var got []ImageRef
			var err error

			// Extract from current device
			if currentDevice != nil {
				currentRefs, extractErr := m.getImageReferencesFromSpecs(context.Background(), currentDevice)
				if extractErr != nil {
					err = extractErr
					if tc.wantErr {
						if tc.wantErrMsg != "" {
							require.Contains(extractErr.Error(), tc.wantErrMsg)
						}
						return
					}
					require.NoError(extractErr)
				}
				got = append(got, currentRefs...)
			}

			// Extract from desired device (if available and no error yet)
			if desiredDevice != nil && err == nil {
				desiredRefs, extractErr := m.getImageReferencesFromSpecs(context.Background(), desiredDevice)
				if extractErr != nil {
					err = extractErr
					if tc.wantErr {
						if tc.wantErrMsg != "" {
							require.Contains(extractErr.Error(), tc.wantErrMsg)
						}
						return
					}
					require.NoError(extractErr)
				}
				got = append(got, desiredRefs...)
			}

			got = lo.Uniq(got)

			if tc.wantErr {
				require.Error(err)
				if tc.wantErrMsg != "" {
					require.Contains(err.Error(), tc.wantErrMsg)
				}
			} else {
				require.NoError(err)
				require.ElementsMatch(tc.want, got)
			}
		})
	}
}

func TestManager_determineEligibleImages(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")
	enabled := true
	config := config.ImagePruning{Enabled: &enabled}

	testCases := []struct {
		name       string
		setupMocks func(*executer.MockExecuter, *spec.MockManager, fileio.ReadWriter, string)
		want       *EligibleItems
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "success with previously referenced but now unused images",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with references that are no longer referenced
				// Using References field (new format) - all references in a single list
				previousRefs := ImageArtifactReferences{
					Timestamp: "2025-01-01T00:00:00Z",
					References: []ImageRef{
						{Image: "quay.io/example/old-app:v1.0", Type: RefTypePodman},
						{Image: "quay.io/example/unused:v1.0", Type: RefTypePodman},
						{Image: "quay.io/example/artifact:v1.0", Type: RefTypeArtifact},
					},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Mock spec manager - current spec only references app:v1.0, not the old ones
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v1.0"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}

				// Mock nested target extraction - image doesn't exist locally (so extraction is skipped)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes()

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// During categorization, we check each eligible reference based on its Type
				// Eligible references are: old-app:v1.0 (podman), unused:v1.0 (podman), artifact:v1.0 (artifact)
				// Mock ImageExists for podman references
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/old-app:v1.0"}).
					Return("", "", 0) // Exists as image
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/unused:v1.0"}).
					Return("", "", 0) // Exists as image
				// Mock ArtifactExists for artifact reference (called directly because Type is RefTypeArtifact)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/artifact:v1.0"}).
					Return("", "", 0) // Exists as artifact
			},
			want: &EligibleItems{
				Images:    []ImageRef{{Image: "quay.io/example/unused:v1.0", Type: RefTypePodman}, {Image: "quay.io/example/old-app:v1.0", Type: RefTypePodman}},
				Artifacts: []ImageRef{{Image: "quay.io/example/artifact:v1.0", Type: RefTypeArtifact}},
				CRI:       []ImageRef{},
				Helm:      []ImageRef{},
			},
		},
		{
			name: "all images in use - no eligible images",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file - all images are still referenced
				previousRefs := ImageArtifactReferences{
					Timestamp:  "2025-01-01T00:00:00Z",
					References: []ImageRef{{Image: "quay.io/example/app:v1.0", Type: RefTypePodman}},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Mock spec manager FIRST - needed for getImageReferencesFromSpecs
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v1.0"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}

				// Podman version check happens when Podman client methods are called
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"--version"}).
					Return("podman version 5.5.0", "", 0).AnyTimes()

				// Mock nested target extraction - images don't exist locally (so extraction is skipped)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes() // exit code 1 = image doesn't exist
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes() // exit code 1 = artifact doesn't exist
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/os:v1.0"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/os:v1.0"}).
					Return("", "", 1).AnyTimes()

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// No eligible references (app:v1.0 is still in current specs), so no categorization calls
			},
			want: &EligibleItems{Images: []ImageRef{}, Artifacts: []ImageRef{}, CRI: []ImageRef{}, Helm: []ImageRef{}}, // All images are in use
		},
		{
			name: "OS images can be pruned if they lose references",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with unused image and OS image
				previousRefs := ImageArtifactReferences{
					Timestamp: "2025-01-01T00:00:00Z",
					References: []ImageRef{
						{Image: "quay.io/example/unused:v1.0", Type: RefTypePodman},
						{Image: "quay.io/example/old-os:v1.0", Type: RefTypePodman},
					},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Mock spec manager FIRST - needed for getImageReferencesFromSpecs
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v1.0"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/new-os:v1.0", // Different OS image
						},
					},
				}

				// Mock nested target extraction - image doesn't exist locally (so extraction is skipped)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes()

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// During categorization, we check each eligible reference individually
				// Eligible references are: unused:v1.0, old-os:v1.0 (not in current specs)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/unused:v1.0"}).
					Return("", "", 0) // Exists as image
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/old-os:v1.0"}).
					Return("", "", 0) // Exists as image
			},
			want: &EligibleItems{Images: []ImageRef{{Image: "quay.io/example/unused:v1.0", Type: RefTypePodman}, {Image: "quay.io/example/old-os:v1.0", Type: RefTypePodman}}, Artifacts: []ImageRef{}, CRI: []ImageRef{}, Helm: []ImageRef{}}, // Both unused image and old OS image are eligible
		},
		{
			name: "desired images preserved",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with unused image (not in current or desired)
				previousRefs := ImageArtifactReferences{
					Timestamp:  "2025-01-01T00:00:00Z",
					References: []ImageRef{{Image: "quay.io/example/unused:v1.0", Type: RefTypePodman}},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))
				// Mock spec manager - current and desired
				currentContainerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(currentContainerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v2.0"})) // Current version
				var currentAppSpec v1beta1.ApplicationProviderSpec
				require.NoError(currentAppSpec.FromContainerApplication(currentContainerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{currentAppSpec}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}

				desiredContainerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(desiredContainerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v1.0"})) // Desired version
				var desiredAppSpec v1beta1.ApplicationProviderSpec
				require.NoError(desiredAppSpec.FromContainerApplication(desiredContainerApp))
				desiredDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{desiredAppSpec}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}

				// Mock nested target extraction FIRST - images don't exist locally (so extraction is skipped)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v2.0"}).
					Return("", "", 1).AnyTimes() // exit code 1 = image doesn't exist
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:v2.0"}).
					Return("", "", 1).AnyTimes() // exit code 1 = artifact doesn't exist
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes() // exit code 1 = image doesn't exist
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes() // exit code 1 = artifact doesn't exist

				// During categorization, we check each eligible reference individually
				// Eligible reference is: unused:v1.0 (not in current or desired specs)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/unused:v1.0"}).
					Return("", "", 0) // Exists as image

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(desiredDevice, nil).Times(1)
			},
			want: &EligibleItems{Images: []ImageRef{{Image: "quay.io/example/unused:v1.0", Type: RefTypePodman}}, Artifacts: []ImageRef{}, CRI: []ImageRef{}, Helm: []ImageRef{}}, // Both current and desired app images preserved
		},
		{
			name: "empty device - all images eligible",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with images that are no longer referenced
				previousRefs := ImageArtifactReferences{
					Timestamp: "2025-01-01T00:00:00Z",
					References: []ImageRef{
						{Image: "quay.io/example/unused1:v1.0", Type: RefTypePodman},
						{Image: "quay.io/example/unused2:v1.0", Type: RefTypePodman},
					},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))
				// During categorization, we check each eligible reference individually
				// Eligible references are: unused1:v1.0, unused2:v1.0 (not in current specs)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/unused1:v1.0"}).
					Return("", "", 0) // Exists as image
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/unused2:v1.0"}).
					Return("", "", 0) // Exists as image

				// Mock spec manager - device with no applications
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: nil,
						Os:           nil, // No OS spec
					},
				}

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)
			},
			want: &EligibleItems{Images: []ImageRef{
				{Image: "quay.io/example/unused1:v1.0", Type: RefTypePodman},
				{Image: "quay.io/example/unused2:v1.0", Type: RefTypePodman},
			}, Artifacts: []ImageRef{}, CRI: []ImageRef{}, Helm: []ImageRef{}},
		},
		{
			name: "partial failure - continues with available data",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with unused image
				previousRefs := ImageArtifactReferences{
					Timestamp:  "2025-01-01T00:00:00Z",
					References: []ImageRef{{Image: "quay.io/example/unused:v1.0", Type: RefTypePodman}},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Mock nested target extraction FIRST - image doesn't exist locally (so extraction is skipped)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes()

				// Mock spec manager
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v1.0"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// During categorization, we check each eligible reference individually
				// Eligible reference is: unused:v1.0 (not in current specs)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/unused:v1.0"}).
					Return("", "", 0) // Exists as image
			},
			want: &EligibleItems{Images: []ImageRef{{Image: "quay.io/example/unused:v1.0", Type: RefTypePodman}}, Artifacts: []ImageRef{}, CRI: []ImageRef{}, Helm: []ImageRef{}}, // Continues with partial results
		},
		{
			name: "image with multiple type references - not eligible when one type remains",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with the same image referenced by both podman and artifact types
				// This simulates a scenario where an image was used as both a container image and an artifact
				previousRefs := ImageArtifactReferences{
					Timestamp: "2025-01-01T00:00:00Z",
					References: []ImageRef{
						{Image: "quay.io/example/shared:v1.0", Type: RefTypePodman},
						{Image: "quay.io/example/shared:v1.0", Type: RefTypeArtifact},
					},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Mock spec manager - current spec still has a podman reference to the same image
				// Even though the artifact reference is dropped, image should NOT be eligible
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/shared:v1.0"})) // Same image, still used as container
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
					},
				}

				// Mock nested target extraction - image doesn't exist locally
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/shared:v1.0"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/shared:v1.0"}).
					Return("", "", 1).AnyTimes()

				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// No image existence checks should be called because the image is still referenced by podman type
			},
			want: &EligibleItems{Images: []ImageRef{}, Artifacts: []ImageRef{}, CRI: []ImageRef{}, Helm: []ImageRef{}}, // Image still referenced by podman - not eligible even though artifact ref dropped
		},
		{
			name: "same image different owners - prune only from owner that dropped reference",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				previousRefs := ImageArtifactReferences{
					Timestamp: "2025-01-01T00:00:00Z",
					References: []ImageRef{
						{Image: "quay.io/example/nginx:1.25", Owner: "userA", Type: RefTypePodman},
						{Image: "quay.io/example/nginx:1.25", Owner: "userB", Type: RefTypePodman},
					},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Current spec only has userB's app - userA's app was removed
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app-b"),
					AppType: v1beta1.AppTypeContainer,
					RunAs:   "userB",
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/nginx:1.25"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
					},
				}

				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// userA's image is eligible - verify it exists during categorization
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/nginx:1.25"}).
					Return("", "", 0)
			},
			want: &EligibleItems{
				Images:    []ImageRef{{Image: "quay.io/example/nginx:1.25", Owner: "userA", Type: RefTypePodman}},
				Artifacts: []ImageRef{},
				CRI:       []ImageRef{},
				Helm:      []ImageRef{},
			},
		},
		{
			name: "explicit root and empty owner treated as same store",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// One app recorded with RunAs:"root", another with default (empty owner)
				// Both share the same podman store, so removing one must NOT prune the image
				previousRefs := ImageArtifactReferences{
					Timestamp: "2025-01-01T00:00:00Z",
					References: []ImageRef{
						{Image: "quay.io/example/nginx:1.25", Owner: "root", Type: RefTypePodman},
						{Image: "quay.io/example/nginx:1.25", Owner: "", Type: RefTypePodman},
					},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Current spec only has the default-owner (empty) app
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app-default"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/nginx:1.25"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
					},
				}

				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)
			},
			want: &EligibleItems{Images: []ImageRef{}, Artifacts: []ImageRef{}, CRI: []ImageRef{}, Helm: []ImageRef{}},
		},
		{
			name: "no previous references file - nothing eligible",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Don't create previous references file - simulates first run
				// No mocks needed since we return early
			},
			want: &EligibleItems{Images: []ImageRef{}, Artifacts: []ImageRef{}, CRI: []ImageRef{}, Helm: []ImageRef{}}, // No previous file, so nothing to prune
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a new controller for each test case to avoid shared mock state
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			mockSpecManager := spec.NewMockManager(ctrl)

			// Create a temporary directory for the test
			tmpDir := t.TempDir()
			// Create a new readWriter for each test to avoid shared state
			testReadWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)
			tc.setupMocks(mockExec, mockSpecManager, testReadWriter, tmpDir)

			rootPodmanClient := client.NewPodman(log, mockExec, testReadWriter, poll.Config{})
			podmanClientFactory := func(user v1beta1.Username) (*client.Podman, error) {
				return rootPodmanClient, nil
			}
			rwFactory := func(user v1beta1.Username) (fileio.ReadWriter, error) {
				return testReadWriter, nil
			}
			m := New(podmanClientFactory, rootPodmanClient, nil, mockSpecManager, rwFactory, testReadWriter, log, config, tmpDir).(*manager)

			got, err := m.determineEligibleImages(context.Background())
			if tc.wantErr {
				require.Error(err)
				if tc.wantErrMsg != "" {
					require.Contains(err.Error(), tc.wantErrMsg)
				}
			} else {
				require.NoError(err)
				require.NotNil(got)
				require.ElementsMatch(tc.want.Images, got.Images)
				require.ElementsMatch(tc.want.Artifacts, got.Artifacts)
			}
		})
	}
}

// TestManager_validateRequiredImages was removed - validateRequiredImages function was redundant
// as determineEligibleImages already handles all validation correctly by only considering
// images that exist locally and building a preserve set from required images in specs.

func TestManager_validateCapability(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")
	enabled := true
	config := config.ImagePruning{Enabled: &enabled}

	testCases := []struct {
		name       string
		setupMocks func(*executer.MockExecuter, *spec.MockManager)
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "success - all images exist",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager) {
				// Mock spec manager - current spec
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v1.0"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// Mock Podman ImageExists calls
				// extractImageReferences now includes OS images, so app image is checked first, then OS image
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 0)
				// OS image is also checked as part of extractImageReferences validation
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/os:v1.0"}).
					Return("", "", 0).AnyTimes() // May be called multiple times (in extractImageReferences and validateCapability)
			},
			wantErr: false,
		},
		{
			name: "failure - current image missing",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager) {
				// Mock spec manager - current spec
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v1.0"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
					},
				}

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// Mock Podman ImageExists - image doesn't exist
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 1)
				// Try as artifact (uses artifact inspect, not artifact exists)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:v1.0"}).
					Return("", "", 1)
			},
			wantErr:    true,
			wantErrMsg: "capability compromised",
		},
		{
			name: "success - no rollback spec",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager) {
				// Mock spec manager - current spec only
				containerApp := v1beta1.ContainerApplication{
					Name:    lo.ToPtr("app1"),
					AppType: v1beta1.AppTypeContainer,
				}
				require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v1.0"}))
				var appSpec v1beta1.ApplicationProviderSpec
				require.NoError(appSpec.FromContainerApplication(containerApp))
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
					},
				}

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// Mock Podman ImageExists calls
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 0)
			},
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a new controller for each test case to avoid shared mock state
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			mockSpecManager := spec.NewMockManager(ctrl)

			tc.setupMocks(mockExec, mockSpecManager)

			rootPodmanClient := client.NewPodman(log, mockExec, readWriter, poll.Config{})
			podmanClientFactory := func(user v1beta1.Username) (*client.Podman, error) {
				return rootPodmanClient, nil
			}
			rwFactory := func(user v1beta1.Username) (fileio.ReadWriter, error) {
				return readWriter, nil
			}
			m := New(podmanClientFactory, rootPodmanClient, nil, mockSpecManager, rwFactory, readWriter, log, config, "/tmp").(*manager)

			err := m.validateCapability(context.Background())
			if tc.wantErr {
				require.Error(err)
				if tc.wantErrMsg != "" {
					require.Contains(err.Error(), tc.wantErrMsg)
				}
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestManager_removeEligibleImages(t *testing.T) {
	testCases := []struct {
		name       string
		setupMocks func(*executer.MockExecuter)
		images     []ImageRef
		wantCount  int
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "success - all images removed",
			setupMocks: func(mockExec *executer.MockExecuter) {
				// First image: check exists, then remove
				call1 := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 0) // Image exists
				call2 := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "rm", "quay.io/example/app:v1.0"}).
					Return("", "", 0) // Image removal succeeds
				// Second image: check exists, then remove
				call3 := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v2.0"}).
					Return("", "", 0) // Image exists
				call4 := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "rm", "quay.io/example/app:v2.0"}).
					Return("", "", 0) // Image removal succeeds
				gomock.InOrder(call1, call2, call3, call4)
			},
			images:    []ImageRef{{Image: "quay.io/example/app:v1.0"}, {Image: "quay.io/example/app:v2.0"}},
			wantCount: 2, // Two images removed
			wantErr:   false,
		},
		{
			name: "success - image doesn't exist (skipped)",
			setupMocks: func(mockExec *executer.MockExecuter) {
				// Image doesn't exist - should be skipped (but removedRefs still includes it)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 1) // Image doesn't exist
			},
			images:    []ImageRef{{Image: "quay.io/example/app:v1.0"}},
			wantCount: 0, // No removal (image doesn't exist), but removedRefs will still contain the reference
			wantErr:   false,
		},
		{
			name: "all removals fail",
			setupMocks: func(mockExec *executer.MockExecuter) {
				// Image exists but removal fails
				call1 := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 0) // Image exists
				call2 := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "rm", "quay.io/example/app:v1.0"}).
					Return("", "error: image is in use by container", 1) // Image removal fails
				gomock.InOrder(call1, call2)
			},
			images:     []ImageRef{{Image: "quay.io/example/app:v1.0"}},
			wantCount:  0, // No removals succeeded
			wantErr:    true,
			wantErrMsg: "all image removals failed",
		},
		{
			name: "empty list - no removals",
			setupMocks: func(mockExec *executer.MockExecuter) {
			},
			images:    []ImageRef{},
			wantCount: 0,
			wantErr:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			mockSpecManager := spec.NewMockManager(ctrl)
			enabled := true
			config := config.ImagePruning{Enabled: &enabled}

			tc.setupMocks(mockExec)

			rootPodmanClient := client.NewPodman(log, mockExec, readWriter, poll.Config{})
			podmanClientFactory := func(user v1beta1.Username) (*client.Podman, error) {
				return rootPodmanClient, nil
			}
			rwFactory := func(user v1beta1.Username) (fileio.ReadWriter, error) {
				return readWriter, nil
			}
			m := New(podmanClientFactory, rootPodmanClient, nil, mockSpecManager, rwFactory, readWriter, log, config, "/tmp").(*manager)

			count, removedRefs, _, err := m.removeEligibleImages(context.Background(), tc.images)
			require.Equal(tc.wantCount, count)
			if tc.wantErr {
				require.Error(err)
				if tc.wantErrMsg != "" {
					require.Contains(err.Error(), tc.wantErrMsg)
				}
			} else {
				require.NoError(err)
				// Verify that removedRefs contains all attempted removals (even if they didn't exist)
				// The count represents successful removals, but removedRefs tracks all attempts
				require.Equal(len(tc.images), len(removedRefs), "removedRefs should contain all attempted removals")
			}
		})
	}
}

// requestPruner is a subset of the Manager interface used for retry tests.
// Tests skip when the concrete manager does not implement RequestPrune (retry support not built).
type requestPruner interface {
	RequestPrune()
}

// TestPrunePending_and_RequestPrune tests the retry-related API: PrunePending and RequestPrune.
func TestPrunePending_and_RequestPrune(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	mockSpecManager := spec.NewMockManager(ctrl)
	enabled := true
	cfg := config.ImagePruning{Enabled: &enabled}

	rootPodmanClient := client.NewPodman(log, mockExec, readWriter, poll.Config{})
	podmanClientFactory := func(user v1beta1.Username) (*client.Podman, error) {
		return rootPodmanClient, nil
	}
	rwFactory := func(user v1beta1.Username) (fileio.ReadWriter, error) {
		return readWriter, nil
	}
	m := New(podmanClientFactory, rootPodmanClient, nil, mockSpecManager, rwFactory, readWriter, log, cfg, "/tmp")

	// New manager: PrunePending is false (no retries requested yet).
	require.False(m.PrunePending(), "new manager should not have prune pending")

	rp, ok := m.(requestPruner)
	if !ok {
		t.Skip("manager does not implement RequestPrune (retry support not in manager)")
	}
	rp.RequestPrune()
	require.True(m.PrunePending(), "after RequestPrune, PrunePending should be true")
}

// TestReloadConfig_enablingPruningSetsRetries verifies that enabling pruning via ReloadConfig
// sets the retry count so Prune will run on the next cycle.
func TestReloadConfig_enablingPruningSetsRetries(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	mockSpecManager := spec.NewMockManager(ctrl)
	enabledFalse := false
	cfgDisabled := config.ImagePruning{Enabled: &enabledFalse}

	rootPodmanClient := client.NewPodman(log, mockExec, readWriter, poll.Config{})
	podmanClientFactory := func(user v1beta1.Username) (*client.Podman, error) {
		return rootPodmanClient, nil
	}
	rwFactory := func(user v1beta1.Username) (fileio.ReadWriter, error) {
		return readWriter, nil
	}
	m := New(podmanClientFactory, rootPodmanClient, nil, mockSpecManager, rwFactory, readWriter, log, cfgDisabled, "/tmp")

	require.False(m.PrunePending(), "with pruning disabled, PrunePending should be false")

	enabledTrue := true
	cfgEnabled := config.Config{
		ImagePruning: config.ImagePruning{Enabled: &enabledTrue},
	}
	err := m.ReloadConfig(context.Background(), &cfgEnabled)
	require.NoError(err)
	require.True(m.PrunePending(), "after ReloadConfig enabling pruning, PrunePending should be true")
}

// TestPrune_clearsRetriesWhenNoEligible verifies that when Prune runs and there is nothing
// eligible to remove, retries are cleared so PrunePending becomes false.
func TestPrune_clearsRetriesWhenNoEligible(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	tmpDir := t.TempDir()
	testReadWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	mockSpecManager := spec.NewMockManager(ctrl)
	enabled := true
	cfg := config.ImagePruning{Enabled: &enabled}

	// No previous references file -> determineEligibleImages returns empty.
	currentDevice := &v1beta1.Device{
		Spec: &v1beta1.DeviceSpec{
			Applications: nil,
			Os:           nil,
		},
	}
	mockSpecManager.EXPECT().Read(spec.Current).Return(currentDevice, nil).AnyTimes()
	mockSpecManager.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).AnyTimes()

	rootPodmanClient := client.NewPodman(log, mockExec, testReadWriter, poll.Config{})
	podmanClientFactory := func(user v1beta1.Username) (*client.Podman, error) {
		return rootPodmanClient, nil
	}
	rwFactory := func(user v1beta1.Username) (fileio.ReadWriter, error) {
		return testReadWriter, nil
	}
	// Prune calls removeEligibleCRIImages and removeEligibleHelmCharts which use m.clients; pass non-nil CLIClients.
	cliClients := client.NewCLIClients()
	m := New(podmanClientFactory, rootPodmanClient, cliClients, mockSpecManager, rwFactory, testReadWriter, log, cfg, tmpDir)

	rp, ok := m.(requestPruner)
	if !ok {
		t.Skip("manager does not implement RequestPrune (retry support not in manager)")
	}
	rp.RequestPrune()
	require.True(m.PrunePending(), "after RequestPrune, PrunePending should be true")

	err := m.Prune(context.Background())
	require.NoError(err)
	// No eligible items -> no removal errors -> defer clears retries.
	require.False(m.PrunePending(), "after Prune with no eligible, retries should be cleared")
}

// TestPrune_keepsPrunePendingOnRemovalErrors verifies that when removal fails, PrunePending stays true
// so the caller will retry Prune on the next cycle (timeout-based retry: we keep retrying until PruneRetryTimeout or success).
func TestPrune_keepsPrunePendingOnRemovalErrors(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	log := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	tmpDir := t.TempDir()
	testReadWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	require.NoError(testReadWriter.MkdirAll(tmpDir, fileio.DefaultDirectoryPermissions))
	previousRefs := ImageArtifactReferences{
		Timestamp:  "2025-01-01T00:00:00Z",
		References: []ImageRef{{Image: "quay.io/example/unused:v1.0", Type: RefTypePodman}},
	}
	jsonData, err := json.Marshal(previousRefs)
	require.NoError(err)
	require.NoError(testReadWriter.WriteFile(filepath.Join(tmpDir, ReferencesFileName), jsonData, fileio.DefaultFilePermissions))

	mockSpecManager := spec.NewMockManager(ctrl)
	containerApp := v1beta1.ContainerApplication{
		Name:    lo.ToPtr("app1"),
		AppType: v1beta1.AppTypeContainer,
	}
	require.NoError(containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/example/app:v1.0"}))
	var appSpec v1beta1.ApplicationProviderSpec
	require.NoError(appSpec.FromContainerApplication(containerApp))
	currentDevice := &v1beta1.Device{
		Spec: &v1beta1.DeviceSpec{
			Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{appSpec}),
			Os:           &v1beta1.DeviceOsSpec{Image: "quay.io/example/os:v1.0"},
		},
	}

	// determineEligibleImages: read refs, read current/desired, check unused image exists.
	mockSpecManager.EXPECT().Read(spec.Current).Return(currentDevice, nil).AnyTimes()
	mockSpecManager.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).AnyTimes()
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).Return("", "", 1).AnyTimes()
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:v1.0"}).Return("", "", 1).AnyTimes()
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/os:v1.0"}).Return("", "", 1).AnyTimes()
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/os:v1.0"}).Return("", "", 1).AnyTimes()
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/unused:v1.0"}).Return("", "", 0).AnyTimes()

	// removeEligibleImages: image exists, image rm fails -> hasRemovalErrors true.
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "rm", "quay.io/example/unused:v1.0"}).
		Return("", "in use", 1).AnyTimes()

	enabled := true
	cfg := config.ImagePruning{Enabled: &enabled}
	rootPodmanClient := client.NewPodman(log, mockExec, testReadWriter, poll.Config{})
	podmanClientFactory := func(user v1beta1.Username) (*client.Podman, error) {
		return rootPodmanClient, nil
	}
	rwFactory := func(user v1beta1.Username) (fileio.ReadWriter, error) {
		return testReadWriter, nil
	}
	cliClients := client.NewCLIClients()
	m := New(podmanClientFactory, rootPodmanClient, cliClients, mockSpecManager, rwFactory, testReadWriter, log, cfg, tmpDir)

	rp, ok := m.(requestPruner)
	if !ok {
		t.Skip("manager does not implement RequestPrune (retry support not in manager)")
	}
	rp.RequestPrune()
	require.True(m.PrunePending(), "after RequestPrune, PrunePending should be true")

	// Prune runs but removal fails -> defer does not clear pending (hasRemovalErrors is true).
	err = m.Prune(context.Background())
	require.NoError(err)
	require.True(m.PrunePending(), "after Prune with removal errors, PrunePending should stay true (retry until timeout or success)")
}

// podmanStore fakes one podman storage: the images and artifacts it holds and
// the removals the pruner dispatches to it.
type podmanStore struct {
	images    map[string]bool
	artifacts map[string]bool
	removed   []string
}

func newPodmanStore() *podmanStore {
	return &podmanStore{images: map[string]bool{}, artifacts: map[string]bool{}}
}

func (s *podmanStore) addImages(images ...string) {
	for _, image := range images {
		s.images[image] = true
	}
}

// podman answers the podman commands the pruner issues against this store.
func (s *podmanStore) podman(ctrl *gomock.Controller) *executer.MockExecuter {
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, args ...string) (string, string, int) {
			if len(args) != 3 {
				return "", "unexpected podman call", 125
			}
			name := args[2]
			switch args[0] + " " + args[1] {
			case "image exists":
				return "", "", exitCodeFor(s.images[name])
			case "artifact inspect":
				return "", "", exitCodeFor(s.artifacts[name])
			case "image rm":
				delete(s.images, name)
			case "artifact rm":
				delete(s.artifacts, name)
			default:
				return "", "unexpected podman call", 125
			}
			s.removed = append(s.removed, name)
			return "", "", 0
		}).AnyTimes()
	return exec
}

func exitCodeFor(present bool) int {
	if present {
		return 0
	}
	return 1
}

// prunerUser owns the second, rootless podman store of a prunerFixture.
const prunerUser v1beta1.Username = "alice"

// prunerFixture is a pruning manager over a temporary data directory, a root
// and a rootless podman store, and current and desired specs the test controls.
type prunerFixture struct {
	m       *manager
	dir     string
	root    *podmanStore
	user    *podmanStore
	current *v1beta1.Device
	desired *v1beta1.Device
	// onSpecRead, when set, runs as the current spec is read.
	onSpecRead func()
}

// newPrunerFixture starts with the given reference history; a nil history
// leaves no references file.
func newPrunerFixture(t *testing.T, history []ImageRef, opts ...Option) *prunerFixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	f := &prunerFixture{
		dir:     t.TempDir(),
		root:    newPodmanStore(),
		user:    newPodmanStore(),
		current: &v1beta1.Device{Spec: &v1beta1.DeviceSpec{}},
	}
	logger := log.NewPrefixLogger("test")
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(f.dir)), fileio.NewWriter(fileio.WithWriterRootDir(f.dir)))
	require.NoError(t, rw.MkdirAll(f.dir, fileio.DefaultDirectoryPermissions))
	if history != nil {
		data, err := json.Marshal(ImageArtifactReferences{Timestamp: "2025-01-01T00:00:00Z", References: history})
		require.NoError(t, err)
		require.NoError(t, rw.WriteFile(filepath.Join(f.dir, ReferencesFileName), data, fileio.DefaultFilePermissions))
	}
	specs := spec.NewMockManager(ctrl)
	specs.EXPECT().Read(spec.Current).DoAndReturn(func(spec.Type) (*v1beta1.Device, error) {
		if f.onSpecRead != nil {
			f.onSpecRead()
		}
		return f.current, nil
	}).AnyTimes()
	specs.EXPECT().Read(spec.Desired).DoAndReturn(func(spec.Type) (*v1beta1.Device, error) {
		if f.desired == nil {
			return nil, errors.New("desired not found")
		}
		return f.desired, nil
	}).AnyTimes()
	root := client.NewPodman(logger, f.root.podman(ctrl), rw, poll.Config{})
	user := client.NewPodman(logger, f.user.podman(ctrl), rw, poll.Config{})
	factory := func(owner v1beta1.Username) (*client.Podman, error) {
		switch {
		case owner.IsCurrentProcessUser() || owner.IsRootUser():
			return root, nil
		case owner == prunerUser:
			return user, nil
		}
		return nil, errors.New("no podman store for " + string(owner))
	}
	rwFactory := func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil }
	enabled := true
	cfg := config.ImagePruning{Enabled: &enabled}
	f.m = New(factory, root, client.NewCLIClients(), specs, rwFactory, rw, logger, cfg, f.dir, opts...).(*manager)
	return f
}

// prune requests and runs one pruning pass and reports whether a retry is
// still pending.
func (f *prunerFixture) prune(t *testing.T) bool {
	t.Helper()
	f.m.RequestPrune()
	require.NoError(t, f.m.Prune(context.Background()))
	return f.m.PrunePending()
}

func (f *prunerFixture) history(t *testing.T) []ImageRef {
	t.Helper()
	data, err := f.m.readWriter.ReadFile(filepath.Join(f.dir, ReferencesFileName))
	require.NoError(t, err)
	var recorded ImageArtifactReferences
	require.NoError(t, json.Unmarshal(data, &recorded))
	return recorded.References
}

// requiredSource is a required reference source whose snapshot can change
// between passes. It counts how often it is queried.
type requiredSource struct {
	refs  []ImageRef
	err   error
	calls int
}

func (s *requiredSource) snapshot(context.Context) ([]ImageRef, error) {
	s.calls++
	return s.refs, s.err
}

func TestManager_requiredReferenceSources(t *testing.T) {
	const (
		updater = "quay.io/example/updater@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		shared  = "quay.io/example/shared:v1"
		unused  = "quay.io/example/unused:v1"
		osImage = "quay.io/example/os:v1"
		nextOS  = "quay.io/example/os:v2"
	)
	rootRef := func(image string) ImageRef { return ImageRef{Image: image, Type: RefTypePodman} }

	t.Run("When sources require overlapping images it should keep each image until no source requires it", func(t *testing.T) {
		require := require.New(t)
		a := &requiredSource{refs: []ImageRef{rootRef(updater), rootRef(shared)}}
		b := &requiredSource{refs: []ImageRef{rootRef(updater)}}
		f := newPrunerFixture(t, []ImageRef{rootRef(updater), rootRef(shared), rootRef(unused)},
			WithRequiredReferenceSource("a", a.snapshot), WithRequiredReferenceSource("b", b.snapshot))
		f.root.addImages(updater, shared, unused)

		require.False(f.prune(t))
		require.Equal([]string{unused}, f.root.removed, "only the image no source requires is removed")

		b.refs = nil
		require.False(f.prune(t))
		require.Equal([]string{unused}, f.root.removed, "the other source still requires the updater")

		a.refs = []ImageRef{rootRef(shared)}
		require.False(f.prune(t))
		require.Equal([]string{unused, updater}, f.root.removed, "the recorded updater is removed once no source requires it")
		require.ElementsMatch([]ImageRef{rootRef(shared)}, f.history(t))
	})

	t.Run("When the current and desired specs reference images it should still protect them", func(t *testing.T) {
		require := require.New(t)
		source := &requiredSource{refs: []ImageRef{rootRef(updater)}}
		f := newPrunerFixture(t, []ImageRef{rootRef(osImage), rootRef(nextOS), rootRef(unused)},
			WithRequiredReferenceSource("firmware", source.snapshot))
		f.current = &v1beta1.Device{Spec: &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: osImage}}}
		f.desired = &v1beta1.Device{Spec: &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: nextOS}}}
		f.root.addImages(osImage, nextOS, unused, updater)

		require.False(f.prune(t))
		require.Equal([]string{unused}, f.root.removed)
	})

	t.Run("When a source names root either way it should protect the image recorded for the current process", func(t *testing.T) {
		testCases := []struct {
			name     string
			required v1beta1.Username
			recorded v1beta1.Username
		}{
			{name: "root required, current process recorded", required: v1beta1.RootUsername, recorded: v1beta1.CurrentProcessUsername},
			{name: "current process required, root recorded", required: v1beta1.CurrentProcessUsername, recorded: v1beta1.RootUsername},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				require := require.New(t)
				source := &requiredSource{refs: []ImageRef{{Image: updater, Owner: tc.required, Type: RefTypePodman}}}
				f := newPrunerFixture(t, []ImageRef{{Image: updater, Owner: tc.recorded, Type: RefTypePodman}, rootRef(unused)},
					WithRequiredReferenceSource("firmware", source.snapshot))
				f.root.addImages(updater, unused)

				require.False(f.prune(t))
				require.Equal([]string{unused}, f.root.removed)
			})
		}
	})

	t.Run("When a source requires an image as one type it should protect the same image and owner recorded as another type", func(t *testing.T) {
		require := require.New(t)
		source := &requiredSource{refs: []ImageRef{rootRef(updater)}}
		f := newPrunerFixture(t, []ImageRef{{Image: updater, Type: RefTypeArtifact}, rootRef(unused)},
			WithRequiredReferenceSource("firmware", source.snapshot))
		f.root.artifacts[updater] = true
		f.root.addImages(unused)

		require.False(f.prune(t))
		require.Equal([]string{unused}, f.root.removed)
		require.True(f.root.artifacts[updater])
	})

	t.Run("When the same image is recorded in two podman stores it should protect only the store the source names", func(t *testing.T) {
		require := require.New(t)
		source := &requiredSource{refs: []ImageRef{{Image: shared, Owner: prunerUser, Type: RefTypePodman}}}
		f := newPrunerFixture(t, []ImageRef{rootRef(shared), {Image: shared, Owner: prunerUser, Type: RefTypePodman}},
			WithRequiredReferenceSource("user-cache", source.snapshot))
		f.root.addImages(shared)
		f.user.addImages(shared)

		require.False(f.prune(t))
		require.Equal([]string{shared}, f.root.removed, "the root copy is not required")
		require.Empty(f.user.removed, "the rootless copy is required")
	})

	t.Run("When no source is registered it should prune recorded images as before", func(t *testing.T) {
		require := require.New(t)
		f := newPrunerFixture(t, []ImageRef{rootRef(updater), rootRef(unused)})
		f.root.addImages(updater, unused)

		require.False(f.prune(t))
		require.ElementsMatch([]string{updater, unused}, f.root.removed)
		require.Empty(f.history(t))
	})

	t.Run("When only a source requires an image it should never record it or remove it after the source stops requiring it", func(t *testing.T) {
		require := require.New(t)
		source := &requiredSource{refs: []ImageRef{rootRef(updater)}}
		f := newPrunerFixture(t, nil, WithRequiredReferenceSource("firmware", source.snapshot))
		f.current = &v1beta1.Device{Spec: &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: osImage}}}
		f.root.addImages(updater, osImage)

		require.NoError(f.m.RecordReferences(context.Background(), f.current, nil))
		require.Equal([]ImageRef{rootRef(osImage)}, f.history(t))
		require.False(f.prune(t))

		source.refs = nil
		require.NoError(f.m.RecordReferences(context.Background(), f.current, nil))
		require.False(f.prune(t))
		require.Empty(f.root.removed, "an image no spec ever referenced is not a pruning candidate")
		require.Equal([]ImageRef{rootRef(osImage)}, f.history(t))
	})

	t.Run("When references are recorded it should keep the history spec-derived without querying sources", func(t *testing.T) {
		require := require.New(t)
		const old = "quay.io/example/old:v1"
		source := &requiredSource{refs: []ImageRef{rootRef(updater)}}
		f := newPrunerFixture(t, []ImageRef{rootRef(updater), rootRef(old)}, WithRequiredReferenceSource("firmware", source.snapshot))
		f.current = &v1beta1.Device{Spec: &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: osImage}}}
		f.root.addImages(updater, old, osImage)

		require.NoError(f.m.RecordReferences(context.Background(), f.current, nil))
		require.Zero(source.calls, "recording does not query required reference sources")
		require.ElementsMatch([]ImageRef{rootRef(updater), rootRef(old), rootRef(osImage)}, f.history(t), "existing rows are kept")

		require.False(f.prune(t))
		require.Equal([]string{old}, f.root.removed, "a recorded image that a source requires is kept")

		source.refs = nil
		require.False(f.prune(t))
		require.Equal([]string{old, updater}, f.root.removed, "the recorded image is removed by the normal rules once nothing requires it")
		require.Equal([]ImageRef{rootRef(osImage)}, f.history(t))
	})

	t.Run("When any source cannot provide a complete snapshot it should remove nothing and keep the prune pending", func(t *testing.T) {
		ok := func() *requiredSource { return &requiredSource{refs: []ImageRef{rootRef(updater)}} }
		failing := &requiredSource{err: errors.New("registry unreadable")}
		testCases := []struct {
			name string
			opts func(cancel context.CancelFunc) []Option
		}{
			{
				name: "a source fails after another succeeded",
				opts: func(context.CancelFunc) []Option {
					return []Option{WithRequiredReferenceSource("ok", ok().snapshot), WithRequiredReferenceSource("failing", failing.snapshot)}
				},
			},
			{
				name: "a source fails before another succeeds",
				opts: func(context.CancelFunc) []Option {
					return []Option{WithRequiredReferenceSource("failing", failing.snapshot), WithRequiredReferenceSource("ok", ok().snapshot)}
				},
			},
			{
				name: "the pass is cancelled while a source runs",
				opts: func(cancel context.CancelFunc) []Option {
					return []Option{
						WithRequiredReferenceSource("cancelled", func(context.Context) ([]ImageRef, error) {
							cancel()
							return []ImageRef{rootRef(updater)}, nil
						}),
					}
				},
			},
			{
				name: "a source returns a reference without an image",
				opts: func(context.CancelFunc) []Option {
					return []Option{WithRequiredReferenceSource("firmware", (&requiredSource{refs: []ImageRef{rootRef(updater), {Type: RefTypePodman}}}).snapshot)}
				},
			},
			{
				name: "a source returns a reference of an unknown type",
				opts: func(context.CancelFunc) []Option {
					return []Option{WithRequiredReferenceSource("firmware", (&requiredSource{refs: []ImageRef{{Image: updater, Type: "oci"}}}).snapshot)}
				},
			},
			{
				name: "a source is registered without a name",
				opts: func(context.CancelFunc) []Option {
					return []Option{WithRequiredReferenceSource(" ", ok().snapshot)}
				},
			},
			{
				name: "a source is registered without a function",
				opts: func(context.CancelFunc) []Option {
					return []Option{WithRequiredReferenceSource("firmware", nil)}
				},
			},
			{
				name: "two sources are registered with the same name",
				opts: func(context.CancelFunc) []Option {
					return []Option{WithRequiredReferenceSource("firmware", ok().snapshot), WithRequiredReferenceSource("firmware", ok().snapshot)}
				},
			},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				require := require.New(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				f := newPrunerFixture(t, []ImageRef{rootRef(updater), rootRef(unused)}, tc.opts(cancel)...)
				f.root.addImages(updater, unused)

				f.m.RequestPrune()
				require.NoError(f.m.Prune(ctx))
				require.Empty(f.root.removed, "no removal is attempted")
				require.True(f.m.PrunePending(), "the prune stays pending for a retry")
				require.ElementsMatch([]ImageRef{rootRef(updater), rootRef(unused)}, f.history(t))
			})
		}
	})

	t.Run("When the pass is cancelled while a source runs it should not query later sources", func(t *testing.T) {
		require := require.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		later := &requiredSource{}
		f := newPrunerFixture(t, []ImageRef{rootRef(unused)},
			WithRequiredReferenceSource("cancelled", func(context.Context) ([]ImageRef, error) {
				cancel()
				return nil, nil
			}),
			WithRequiredReferenceSource("later", later.snapshot))
		f.root.addImages(unused)

		f.m.RequestPrune()
		require.NoError(f.m.Prune(ctx))
		require.Zero(later.calls)
		require.Empty(f.root.removed)
		require.True(f.m.PrunePending())
	})

	t.Run("When the pass is cancelled before the sources are queried it should not query them or remove anything", func(t *testing.T) {
		require := require.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := &requiredSource{refs: []ImageRef{rootRef(updater)}}
		f := newPrunerFixture(t, []ImageRef{rootRef(updater), rootRef(unused)}, WithRequiredReferenceSource("firmware", source.snapshot))
		f.root.addImages(updater, unused)
		f.onSpecRead = cancel

		f.m.RequestPrune()
		require.NoError(f.m.Prune(ctx))
		require.Zero(source.calls, "a cancelled pass queries no source")
		require.Empty(f.root.removed)
		require.True(f.m.PrunePending())
	})

	t.Run("When a source fails it should report the source name and keep the cause", func(t *testing.T) {
		require := require.New(t)
		cause := errors.New("registry unreadable")
		f := newPrunerFixture(t, []ImageRef{rootRef(unused)},
			WithRequiredReferenceSource("firmware", (&requiredSource{err: cause}).snapshot))

		_, err := f.m.determineEligibleImages(context.Background())
		require.ErrorIs(err, errRequiredReferences)
		require.ErrorIs(err, cause)
		require.Equal("firmware", deviceerrors.GetElement(err))
		require.ErrorContains(err, "firmware")
	})

	t.Run("When a failed source recovers it should prune on the pending retry", func(t *testing.T) {
		require := require.New(t)
		source := &requiredSource{err: errors.New("registry unreadable")}
		f := newPrunerFixture(t, []ImageRef{rootRef(updater), rootRef(unused)}, WithRequiredReferenceSource("firmware", source.snapshot))
		f.root.addImages(updater, unused)

		require.True(f.prune(t))
		require.Empty(f.root.removed)

		source.err, source.refs = nil, []ImageRef{rootRef(updater)}
		require.NoError(f.m.Prune(context.Background()))
		require.Equal([]string{unused}, f.root.removed)
		require.False(f.m.PrunePending(), "the retried prune completes once every source answers")
	})
}
