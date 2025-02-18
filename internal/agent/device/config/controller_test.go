package config

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
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
		current    *v1alpha1.DeviceSpec
		desired    *v1alpha1.DeviceSpec
		setupMocks func(mockWriter *fileio.MockWriter, mockManagedFile *fileio.MockManagedFile, f string)
		wantErr    error
		// files which are created via the sync operation
		createdFiles []string
		// files which are removed via the sync operation
		removedFiles []string
	}{
		{
			name:    "no desired config",
			current: &v1alpha1.DeviceSpec{},
			desired: &v1alpha1.DeviceSpec{},
		},
		{
			name: "desired config is valid current is nil",
			current: &v1alpha1.DeviceSpec{
				Config: nil,
			},
			desired: &v1alpha1.DeviceSpec{
				Config: testConfigProvider(require, 2),
			},
			createdFiles: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
			},
		},
		{
			name: "current config is valid desired is nil",
			current: &v1alpha1.DeviceSpec{
				Config: testConfigProvider(require, 3),
			},
			desired: &v1alpha1.DeviceSpec{},
			removedFiles: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
				"/etc/example/file3.txt",
			},
		},
		{
			name: "validate removal of files",
			current: &v1alpha1.DeviceSpec{
				Config: testConfigProvider(require, 3),
			},
			desired: &v1alpha1.DeviceSpec{
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

			mockWriter := fileio.NewMockWriter(ctrl)
			mockManagedFile := fileio.NewMockManagedFile(ctrl)
			controller := NewController(
				mockWriter,
				log.NewPrefixLogger("test"),
			)

			for _, f := range tt.createdFiles {
				expectCreateFile(mockWriter, mockManagedFile, f)
			}

			for _, f := range tt.removedFiles {
				expectRemoveFile(mockWriter, f)
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
		current  []v1alpha1.FileSpec
		desired  []v1alpha1.FileSpec
		expected []string
	}{
		{
			name: "no desired files",
			current: []v1alpha1.FileSpec{
				{Path: "/etc/example/file1.txt"},
				{Path: "/etc/example/file2.txt"},
			},
			desired: []v1alpha1.FileSpec{},
			expected: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
			},
		},
		{
			name:    "no current files",
			current: []v1alpha1.FileSpec{},
			desired: []v1alpha1.FileSpec{
				{Path: "/etc/example/file1.txt"},
				{Path: "/etc/example/file2.txt"},
			},
			expected: []string{},
		},
		{
			name: "remove diff",
			current: []v1alpha1.FileSpec{
				{Path: "/etc/example/file1.txt"},
				{Path: "/etc/example/file2.txt"},
				{Path: "/etc/example/file3.txt"},
			},
			desired: []v1alpha1.FileSpec{
				{Path: "/etc/example/file1.txt"},
				{Path: "/etc/example/file3.txt"},
			},
			expected: []string{
				"/etc/example/file2.txt",
			},
		},
		{
			name:     "no files",
			current:  []v1alpha1.FileSpec{},
			desired:  []v1alpha1.FileSpec{},
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

func expectCreateFile(mockWriter *fileio.MockWriter, mockManagedFile *fileio.MockManagedFile, _ string) {
	mockWriter.EXPECT().CreateManagedFile(gomock.Any()).Return(mockManagedFile, nil)
	mockManagedFile.EXPECT().IsUpToDate().Return(false, nil)
	mockManagedFile.EXPECT().Exists().Return(false, nil)
	mockManagedFile.EXPECT().Write().Return(nil)
}

func expectRemoveFile(mockWriter *fileio.MockWriter, f string) {
	mockWriter.EXPECT().RemoveFile(f).Return(nil)
}

func testConfigProvider(require *require.Assertions, fileCount int) *[]v1alpha1.ConfigProviderSpec {
	var provider v1alpha1.ConfigProviderSpec
	files := make([]v1alpha1.FileSpec, 0, fileCount)

	for i := 0; i < fileCount; i++ {
		files = append(files, v1alpha1.FileSpec{ // Appending new elements
			Path:    fmt.Sprintf("/etc/example/file%d.txt", i+1),
			Content: fmt.Sprintf("File %d contents", i+1),
			Mode:    lo.ToPtr(0o420),
		})
	}

	err := provider.FromInlineConfigProviderSpec(v1alpha1.InlineConfigProviderSpec{Inline: files})
	require.NoError(err)

	return &[]v1alpha1.ConfigProviderSpec{provider}
}
