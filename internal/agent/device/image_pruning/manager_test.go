package imagepruning

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
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
	readWriter := fileio.NewReadWriter()
	podmanClient := client.NewPodman(log, mockExec, readWriter, poll.Config{})
	mockSpecManager := spec.NewMockManager(ctrl)
	enabled := true
	config := config.ImagePruning{Enabled: &enabled}

	m := New(podmanClient, mockSpecManager, readWriter, log, config, "/tmp").(*manager)

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
		want       []string
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
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:current",
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(imageSpec))

				desiredDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
					},
				}
				desiredApps := lo.FromPtr(desiredDevice.Spec.Applications)
				desiredImageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:desired",
				}
				require.NoError(desiredApps[0].FromImageApplicationProviderSpec(desiredImageSpec))

				mock.EXPECT().Read(spec.Current).Return(currentDevice, nil)
				mock.EXPECT().Read(spec.Desired).Return(desiredDevice, nil)
			},
			want: []string{"quay.io/example/app:current", "quay.io/example/app:desired"},
		},
		{
			name: "success with current spec only (no desired)",
			setupMocks: func(mockExec *executer.MockExecuter, mock *spec.MockManager) {
				// Mock nested target extraction - image doesn't exist locally (so extraction is skipped)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:current"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:current"}).
					Return("", "", 1).AnyTimes()
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:current",
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(imageSpec))

				// Mock nested target extraction - image doesn't exist locally (so extraction is skipped)
				mockImageNotExists("quay.io/example/app:current")

				mock.EXPECT().Read(spec.Current).Return(currentDevice, nil)
				mock.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found"))
			},
			want: []string{"quay.io/example/app:current"},
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
				// Return a device with invalid spec structure (missing app type will cause error during extraction)
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: "", // Invalid: missing app type
							},
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

			var got []string
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
					Timestamp:  "2025-01-01T00:00:00Z",
					References: []string{"quay.io/example/old-app:v1.0", "quay.io/example/unused:v1.0", "quay.io/example/artifact:v1.0"},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Mock spec manager - current spec only references app:v1.0, not the old ones
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:v1.0",
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(imageSpec))

				// Mock nested target extraction - image doesn't exist locally (so extraction is skipped)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes()
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/app:v1.0"}).
					Return("", "", 1).AnyTimes()

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// During categorization, we check each eligible reference individually
				// Eligible references are: old-app:v1.0, unused:v1.0, artifact:v1.0 (not in current specs)
				// Mock ImageExists for each eligible reference
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/old-app:v1.0"}).
					Return("", "", 0) // Exists as image
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/unused:v1.0"}).
					Return("", "", 0) // Exists as image
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/artifact:v1.0"}).
					Return("", "", 1) // Doesn't exist as image
				// Mock ArtifactExists for artifact reference (only called if ImageExists returns false)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"artifact", "inspect", "quay.io/example/artifact:v1.0"}).
					Return("", "", 0) // Exists as artifact
			},
			want: &EligibleItems{
				Images:    []string{"quay.io/example/unused:v1.0", "quay.io/example/old-app:v1.0"},
				Artifacts: []string{"quay.io/example/artifact:v1.0"},
			},
		},
		{
			name: "all images in use - no eligible images",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file - all images are still referenced
				previousRefs := ImageArtifactReferences{
					Timestamp:  "2025-01-01T00:00:00Z",
					References: []string{"quay.io/example/app:v1.0"},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Mock spec manager FIRST - needed for getImageReferencesFromSpecs
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:v1.0",
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(imageSpec))

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
			want: &EligibleItems{Images: []string{}, Artifacts: []string{}}, // All images are in use
		},
		{
			name: "OS images can be pruned if they lose references",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with unused image and OS image
				previousRefs := ImageArtifactReferences{
					Timestamp:  "2025-01-01T00:00:00Z",
					References: []string{"quay.io/example/unused:v1.0", "quay.io/example/old-os:v1.0"},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))

				// Mock spec manager FIRST - needed for getImageReferencesFromSpecs
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/new-os:v1.0", // Different OS image
						},
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:v1.0",
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(imageSpec))

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
			want: &EligibleItems{Images: []string{"quay.io/example/unused:v1.0", "quay.io/example/old-os:v1.0"}, Artifacts: []string{}}, // Both unused image and old OS image are eligible
		},
		{
			name: "desired images preserved",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with unused image (not in current or desired)
				previousRefs := ImageArtifactReferences{
					Timestamp:  "2025-01-01T00:00:00Z",
					References: []string{"quay.io/example/unused:v1.0"},
				}
				jsonData, err := json.Marshal(previousRefs)
				require.NoError(err)
				// Ensure directory exists
				require.NoError(readWriter.MkdirAll(dataDir, fileio.DefaultDirectoryPermissions))
				// Write file using readWriter to match how the manager reads it
				filePath := filepath.Join(dataDir, ReferencesFileName)
				require.NoError(readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions))
				// Mock spec manager - current and desired
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				currentImageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:v2.0", // Current version
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(currentImageSpec))

				desiredDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}
				desiredApps := lo.FromPtr(desiredDevice.Spec.Applications)
				desiredImageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:v1.0", // Desired version
				}
				require.NoError(desiredApps[0].FromImageApplicationProviderSpec(desiredImageSpec))

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
			want: &EligibleItems{Images: []string{"quay.io/example/unused:v1.0"}, Artifacts: []string{}}, // Both current and desired app images preserved
		},
		{
			name: "empty device - all images eligible",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with images that are no longer referenced
				previousRefs := ImageArtifactReferences{
					Timestamp:  "2025-01-01T00:00:00Z",
					References: []string{"quay.io/example/unused1:v1.0", "quay.io/example/unused2:v1.0"},
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
			want: &EligibleItems{Images: []string{"quay.io/example/unused1:v1.0", "quay.io/example/unused2:v1.0"}, Artifacts: []string{}},
		},
		{
			name: "partial failure - continues with available data",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Create previous references file with unused image
				previousRefs := ImageArtifactReferences{
					Timestamp:  "2025-01-01T00:00:00Z",
					References: []string{"quay.io/example/unused:v1.0"},
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
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:v1.0",
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(imageSpec))

				// getImageReferencesFromSpecs now includes OS images via extractImageReferences, so only one Read per spec
				mockSpec.EXPECT().Read(spec.Current).Return(currentDevice, nil).Times(1)
				mockSpec.EXPECT().Read(spec.Desired).Return(nil, errors.New("desired not found")).Times(1)

				// During categorization, we check each eligible reference individually
				// Eligible reference is: unused:v1.0 (not in current specs)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", []string{"image", "exists", "quay.io/example/unused:v1.0"}).
					Return("", "", 0) // Exists as image
			},
			want: &EligibleItems{Images: []string{"quay.io/example/unused:v1.0"}, Artifacts: []string{}}, // Continues with partial results
		},
		{
			name: "no previous references file - nothing eligible",
			setupMocks: func(mockExec *executer.MockExecuter, mockSpec *spec.MockManager, readWriter fileio.ReadWriter, dataDir string) {
				// Don't create previous references file - simulates first run
				// No mocks needed since we return early
			},
			want: &EligibleItems{Images: []string{}, Artifacts: []string{}}, // No previous file, so nothing to prune
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
			testReadWriter := fileio.NewReadWriter()
			testReadWriter.SetRootdir(tmpDir)
			tc.setupMocks(mockExec, mockSpecManager, testReadWriter, tmpDir)

			podmanClient := client.NewPodman(log, mockExec, testReadWriter, poll.Config{})
			m := New(podmanClient, mockSpecManager, testReadWriter, log, config, tmpDir).(*manager)

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
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
						Os: &v1beta1.DeviceOsSpec{
							Image: "quay.io/example/os:v1.0",
						},
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:v1.0",
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(imageSpec))

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
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:v1.0",
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(imageSpec))

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
				currentDevice := &v1beta1.Device{
					Spec: &v1beta1.DeviceSpec{
						Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{
							{
								Name:    lo.ToPtr("app1"),
								AppType: v1beta1.AppTypeContainer,
							},
						}),
					},
				}
				apps := lo.FromPtr(currentDevice.Spec.Applications)
				imageSpec := v1beta1.ImageApplicationProviderSpec{
					Image: "quay.io/example/app:v1.0",
				}
				require.NoError(apps[0].FromImageApplicationProviderSpec(imageSpec))

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
			readWriter := fileio.NewReadWriter()
			mockSpecManager := spec.NewMockManager(ctrl)

			tc.setupMocks(mockExec, mockSpecManager)

			podmanClient := client.NewPodman(log, mockExec, readWriter, poll.Config{})
			m := New(podmanClient, mockSpecManager, readWriter, log, config, "/tmp").(*manager)

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
		images     []string
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
			images:    []string{"quay.io/example/app:v1.0", "quay.io/example/app:v2.0"},
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
			images:    []string{"quay.io/example/app:v1.0"},
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
			images:     []string{"quay.io/example/app:v1.0"},
			wantCount:  0, // No removals succeeded
			wantErr:    true,
			wantErrMsg: "all image removals failed",
		},
		{
			name: "empty list - no removals",
			setupMocks: func(mockExec *executer.MockExecuter) {
			},
			images:    []string{},
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
			readWriter := fileio.NewReadWriter()
			mockSpecManager := spec.NewMockManager(ctrl)
			enabled := true
			config := config.ImagePruning{Enabled: &enabled}

			tc.setupMocks(mockExec)

			podmanClient := client.NewPodman(log, mockExec, readWriter, poll.Config{})
			m := New(podmanClient, mockSpecManager, readWriter, log, config, "/tmp").(*manager)

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
