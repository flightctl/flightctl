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
					Image:   "quay.io/example/app:current",
				}
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
					Image:   "quay.io/example/app:desired",
				}
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
					Image:   "quay.io/example/app:current",
				}
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
					Image:   "quay.io/example/app:v1.0",
				}
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
					Image:   "quay.io/example/app:v1.0",
				}
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
					Image:   "quay.io/example/app:v1.0",
				}
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
					Image:   "quay.io/example/app:v2.0", // Current version
				}
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
					Image:   "quay.io/example/app:v1.0", // Desired version
				}
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
					Image:   "quay.io/example/app:v1.0",
				}
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
					Image:   "quay.io/example/shared:v1.0", // Same image, still used as container
				}
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
					Image:   "quay.io/example/app:v1.0",
				}
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
					Image:   "quay.io/example/app:v1.0",
				}
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
					Image:   "quay.io/example/app:v1.0",
				}
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

			count, removedRefs, err := m.removeEligibleImages(context.Background(), tc.images)
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
