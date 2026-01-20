package config

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSync(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name         string
		current      *v1beta1.DeviceSpec
		desired      *v1beta1.DeviceSpec
		wantErr      error
		createdFiles []string
		removedFiles []string
		removedDirs  []string
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
			desired:      &v1beta1.DeviceSpec{},
			removedFiles: []string{"/etc/example/file1.txt", "/etc/example/file2.txt", "/etc/example/file3.txt"},
			removedDirs:  []string{"/etc/example", "/etc"},
		},
		{
			name: "validate removal of files",
			current: &v1beta1.DeviceSpec{
				Config: testConfigProvider(require, 3),
			},
			desired: &v1beta1.DeviceSpec{
				Config: testConfigProvider(require, 2),
			},
			createdFiles: []string{"/etc/example/file1.txt", "/etc/example/file2.txt"},
			removedFiles: []string{"/etc/example/file3.txt"},
			removedDirs:  []string{}, // directories still needed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWriter := fileio.NewMockWriter(ctrl)
			mockManagedFile := fileio.NewMockManagedFile(ctrl)
			controller := NewController(mockWriter, log.NewPrefixLogger("test"))

			for range tt.createdFiles {
				mockWriter.EXPECT().CreateManagedFile(gomock.Any()).Return(mockManagedFile, nil)
				mockManagedFile.EXPECT().IsUpToDate().Return(false, nil)
				mockManagedFile.EXPECT().Exists().Return(false, nil)
				mockManagedFile.EXPECT().Write().Return(nil)
			}

			for _, f := range tt.removedFiles {
				mockWriter.EXPECT().RemoveFile(f).Return(nil)
			}

			for _, d := range tt.removedDirs {
				mockWriter.EXPECT().RemoveFile(d).Return(nil)
			}

			err := controller.Sync(ctx, tt.current, tt.desired)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
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
			desired:  []v1beta1.FileSpec{},
			expected: []string{"/etc/example/file1.txt", "/etc/example/file2.txt"},
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
			expected: []string{"/etc/example/file2.txt"},
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

func TestCollectParentDirs(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		files    []v1beta1.FileSpec
		expected map[string]struct{}
	}{
		{
			name:     "empty files",
			files:    []v1beta1.FileSpec{},
			expected: map[string]struct{}{},
		},
		{
			name:     "single file",
			files:    []v1beta1.FileSpec{{Path: "/etc/example/file.txt"}},
			expected: map[string]struct{}{"/etc/example": {}, "/etc": {}},
		},
		{
			name: "files in subdirectories",
			files: []v1beta1.FileSpec{
				{Path: "/etc/drone-tracker/index.html"},
				{Path: "/etc/drone-tracker/images/logo.png"},
			},
			expected: map[string]struct{}{
				"/etc/drone-tracker":        {},
				"/etc/drone-tracker/images": {},
				"/etc":                      {},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := collectParentDirs(tt.files)
			require.Equal(tt.expected, actual)
		})
	}
}

func TestSyncWithSubdirectoryCleanup(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWriter := fileio.NewMockWriter(ctrl)
	controller := NewController(mockWriter, log.NewPrefixLogger("test"))

	// Current: files in subdirectory
	var currentProvider v1beta1.ConfigProviderSpec
	currentFiles := []v1beta1.FileSpec{
		{Path: "/etc/drone-tracker/index.html", Content: "html", Mode: lo.ToPtr(0o644)},
		{Path: "/etc/drone-tracker/images/logo.png", Content: "png", Mode: lo.ToPtr(0o644)},
	}
	_ = currentProvider.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{Inline: currentFiles})
	current := &v1beta1.DeviceSpec{Config: &[]v1beta1.ConfigProviderSpec{currentProvider}}

	// Desired: empty (remove all)
	desired := &v1beta1.DeviceSpec{}

	// Expect files to be removed
	mockWriter.EXPECT().RemoveFile("/etc/drone-tracker/index.html").Return(nil)
	mockWriter.EXPECT().RemoveFile("/etc/drone-tracker/images/logo.png").Return(nil)

	// Expect empty directories to be removed (deepest first)
	mockWriter.EXPECT().RemoveFile("/etc/drone-tracker/images").Return(nil)
	mockWriter.EXPECT().RemoveFile("/etc/drone-tracker").Return(nil)
	mockWriter.EXPECT().RemoveFile("/etc").Return(nil)

	err := controller.Sync(ctx, current, desired)
	require.NoError(err)
}

func testConfigProvider(require *require.Assertions, fileCount int) *[]v1beta1.ConfigProviderSpec {
	var provider v1beta1.ConfigProviderSpec
	files := make([]v1beta1.FileSpec, 0, fileCount)

	for i := 0; i < fileCount; i++ {
		files = append(files, v1beta1.FileSpec{
			Path:    fmt.Sprintf("/etc/example/file%d.txt", i+1),
			Content: fmt.Sprintf("File %d contents", i+1),
			Mode:    lo.ToPtr(0o420),
		})
	}

	err := provider.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{Inline: files})
	require.NoError(err)

	return &[]v1beta1.ConfigProviderSpec{provider}
}
