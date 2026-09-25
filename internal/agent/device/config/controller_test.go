package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSync(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name       string
		current    *v1beta1.DeviceSpec
		desired    *v1beta1.DeviceSpec
		setupMocks func(mockWriter *fileio.MockReadWriter, mockManagedFile *fileio.MockManagedFile, f string)
		wantErr    error
		// files which are created via the sync operation
		createdFiles []string
		// files which are removed via the sync operation
		removedFiles []string
		// directories that become empty and are cleaned up via the sync operation
		removedDirs []string
		// directories the agent previously created (loaded from the ownership
		// manifest); only these are eligible for cleanup
		ownedDirs []string
	}{
		{
			name:    "no desired config",
			current: &v1beta1.DeviceSpec{},
			desired: &v1beta1.DeviceSpec{},
		},
		{
			name: "desired config is valid current is nil",
			current: &v1beta1.DeviceSpec{
				Config: nil,
			},
			desired: &v1beta1.DeviceSpec{
				Config: testConfigProvider(require, 2),
			},
			createdFiles: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
			},
		},
		{
			name: "current config is valid desired is nil",
			current: &v1beta1.DeviceSpec{
				Config: testConfigProvider(require, 3),
			},
			desired: &v1beta1.DeviceSpec{},
			removedFiles: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
				"/etc/example/file3.txt",
			},
			removedDirs: []string{
				"/etc/example",
			},
			ownedDirs: []string{
				"/etc/example",
			},
		},
		{
			name: "validate removal of files",
			current: &v1beta1.DeviceSpec{
				Config: testConfigProvider(require, 3),
			},
			desired: &v1beta1.DeviceSpec{
				Config: testConfigProvider(require, 2),
			},
			createdFiles: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
			},
			removedFiles: []string{
				"/etc/example/file3.txt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWriter := fileio.NewMockReadWriter(ctrl)
			mockManagedFile := fileio.NewMockManagedFile(ctrl)
			controller := NewController(
				mockWriter,
				"/var/lib/flightctl",
				log.NewPrefixLogger("test"),
			)

			expectManagedDirectories(mockWriter, tt.ownedDirs)

			for _, f := range tt.createdFiles {
				expectCreateFile(mockWriter, mockManagedFile, f)
			}

			for _, f := range tt.removedFiles {
				expectRemoveFile(mockWriter, f)
			}

			for _, d := range tt.removedDirs {
				expectRemoveEmptyDir(mockWriter, d)
			}

			err := controller.Sync(ctx, tt.current, tt.desired)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
		})
	}
}

func TestComputeRemoval(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		current  []v1beta1.FileSpec
		desired  []v1beta1.FileSpec
		expected []string
	}{
		{
			name: "no desired files",
			current: []v1beta1.FileSpec{
				{Path: "/etc/example/file1.txt"},
				{Path: "/etc/example/file2.txt"},
			},
			desired: []v1beta1.FileSpec{},
			expected: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
			},
		},
		{
			name:    "no current files",
			current: []v1beta1.FileSpec{},
			desired: []v1beta1.FileSpec{
				{Path: "/etc/example/file1.txt"},
				{Path: "/etc/example/file2.txt"},
			},
			expected: []string{},
		},
		{
			name: "remove diff",
			current: []v1beta1.FileSpec{
				{Path: "/etc/example/file1.txt"},
				{Path: "/etc/example/file2.txt"},
				{Path: "/etc/example/file3.txt"},
			},
			desired: []v1beta1.FileSpec{
				{Path: "/etc/example/file1.txt"},
				{Path: "/etc/example/file3.txt"},
			},
			expected: []string{
				"/etc/example/file2.txt",
			},
		},
		{
			name:     "no files",
			current:  []v1beta1.FileSpec{},
			desired:  []v1beta1.FileSpec{},
			expected: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := computeRemoval(tt.current, tt.desired)
			require.Equal(tt.expected, actual)
		})
	}
}

func expectCreateFile(mockWriter *fileio.MockReadWriter, mockManagedFile *fileio.MockManagedFile, _ string) {
	mockWriter.EXPECT().CreateManagedFile(gomock.Any()).Return(mockManagedFile, nil)
	mockManagedFile.EXPECT().IsUpToDate().Return(false, nil)
	mockManagedFile.EXPECT().Exists().Return(false, nil)
	mockManagedFile.EXPECT().Write().Return(nil)
}

func expectRemoveFile(mockWriter *fileio.MockReadWriter, f string) {
	mockWriter.EXPECT().RemoveFile(f).Return(nil)
}

func expectRemoveEmptyDir(mockWriter *fileio.MockReadWriter, d string) {
	mockWriter.EXPECT().RemoveEmptyDir(d).Return(true, nil)
}

// expectManagedDirectories wires the ownership-manifest I/O the controller
// performs each sync: reading the previously owned set (ownedDirs, or a missing
// manifest when empty), probing whether about-to-be-created directories exist,
// and persisting the updated set. These are tolerant (AnyTimes) so individual
// test cases only assert on file and directory operations.
func expectManagedDirectories(mockWriter *fileio.MockReadWriter, ownedDirs []string) {
	if len(ownedDirs) == 0 {
		mockWriter.EXPECT().ReadFile(gomock.Any()).Return(nil, os.ErrNotExist).AnyTimes()
	} else {
		data, _ := json.Marshal(managedDirectories{Directories: ownedDirs})
		mockWriter.EXPECT().ReadFile(gomock.Any()).Return(data, nil).AnyTimes()
	}
	// About-to-be-created directories are reported absent so they are tracked.
	mockWriter.EXPECT().PathExists(gomock.Any(), gomock.Any()).Return(false, nil).AnyTimes()
	mockWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
}

func testConfigProvider(require *require.Assertions, fileCount int) *[]v1beta1.ConfigProviderSpec {
	var provider v1beta1.ConfigProviderSpec
	files := make([]v1beta1.FileSpec, 0, fileCount)

	for i := 0; i < fileCount; i++ {
		files = append(files, v1beta1.FileSpec{ // Appending new elements
			Path:    fmt.Sprintf("/etc/example/file%d.txt", i+1),
			Content: fmt.Sprintf("File %d contents", i+1),
			Mode:    lo.ToPtr(0o420),
		})
	}

	err := provider.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{Inline: files})
	require.NoError(err)

	return &[]v1beta1.ConfigProviderSpec{provider}
}
