package renderer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

// Helper function to create a test config with temporary directories
func createTestConfig(t *testing.T) *RendererConfig {
	t.Helper()

	baseDir := t.TempDir()

	return &RendererConfig{
		ReadOnlyConfigOutputDir:  filepath.Join(baseDir, "readonly"),
		WriteableConfigOutputDir: filepath.Join(baseDir, "writeable"),
		QuadletFilesOutputDir:    filepath.Join(baseDir, "quadlet"),
		SystemdUnitOutputDir:     filepath.Join(baseDir, "systemd"),
		BinOutputDir:             filepath.Join(baseDir, "bin"),
		Api: ImageConfig{
			Image: "test-api-image",
			Tag:   "v1.0",
		},
		Periodic: ImageConfig{
			Image: "test-periodic-image",
			Tag:   "v2.0",
		},
	}
}

func createTempFile(t *testing.T, dir, filename, content string) string {
	t.Helper()

	filePath := filepath.Join(dir, filename)
	err := os.MkdirAll(filepath.Dir(filePath), 0755)
	require.NoError(t, err, "Failed to create directory")

	err = os.WriteFile(filePath, []byte(content), 0600)
	require.NoError(t, err, "Failed to write temp file")

	return filePath
}

func createTempDir(t *testing.T, baseDir string) string {
	t.Helper()

	dirPath := filepath.Join(baseDir, "testdir")
	err := os.MkdirAll(dirPath, 0755)
	require.NoError(t, err, "Failed to create directory")

	// Create some files in the directory
	createTempFile(t, dirPath, "file1.txt", "content1")
	createTempFile(t, dirPath, "file2.txt", "content2")

	// Create a subdirectory with a file
	subDir := filepath.Join(dirPath, "subdir")
	err = os.MkdirAll(subDir, 0755)
	require.NoError(t, err, "Failed to create subdirectory")
	createTempFile(t, subDir, "file3.txt", "content3")

	return dirPath
}

func verifyFileContent(t *testing.T, path, expectedContent string) {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err, "Failed to read file %s", path)
	require.Equal(t, expectedContent, string(content), "File content mismatch for %s", path)
}

func verifyFileMode(t *testing.T, path string, expectedMode os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err, "Failed to stat file %s", path)
	require.Equal(t, expectedMode, info.Mode().Perm(), "File mode mismatch for %s", path)
}

func verifyDirExists(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err, "Directory does not exist: %s", path)
	require.True(t, info.IsDir(), "Path exists but is not a directory: %s", path)
}

func verifyFileExists(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err, "File does not exist: %s", path)
	require.False(t, info.IsDir(), "Path exists but is a directory: %s", path)
}

func TestProcessInstallManifest(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction
		verify         func(t *testing.T, config *RendererConfig)
		expectError    bool
		errorSubstring string
	}{
		{
			name: "copy non-templated file",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				srcFile := createTempFile(t, sourceDir, "test.txt", "test content")
				return []InstallAction{
					{
						Action:      ActionCopyFile,
						Source:      srcFile,
						Destination: filepath.Join(config.QuadletFilesOutputDir, "test.txt"),
						Template:    false,
						Mode:        RegularFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				destPath := filepath.Join(config.QuadletFilesOutputDir, "test.txt")
				verifyFileExists(t, destPath)
				verifyFileContent(t, destPath, "test content")
				verifyFileMode(t, destPath, RegularFileMode)
			},
		},
		{
			name: "copy templated file with variable substitution",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				srcFile := createTempFile(t, sourceDir, "template.txt", "Image: {{.Api.Image}}:{{.Api.Tag}}\nPeriodic: {{.Periodic.Image}}:{{.Periodic.Tag}}")
				return []InstallAction{
					{
						Action:      ActionCopyFile,
						Source:      srcFile,
						Destination: filepath.Join(config.QuadletFilesOutputDir, "template.txt"),
						Template:    true,
						Mode:        RegularFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				destPath := filepath.Join(config.QuadletFilesOutputDir, "template.txt")
				verifyFileExists(t, destPath)
				verifyFileContent(t, destPath, "Image: test-api-image:v1.0\nPeriodic: test-periodic-image:v2.0")
				verifyFileMode(t, destPath, RegularFileMode)
			},
		},
		{
			name: "copy file with executable permissions",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				srcFile := createTempFile(t, sourceDir, "script.sh", "#!/bin/bash\necho hello")
				return []InstallAction{
					{
						Action:      ActionCopyFile,
						Source:      srcFile,
						Destination: filepath.Join(config.BinOutputDir, "script.sh"),
						Template:    false,
						Mode:        ExecutableFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				destPath := filepath.Join(config.BinOutputDir, "script.sh")
				verifyFileExists(t, destPath)
				verifyFileContent(t, destPath, "#!/bin/bash\necho hello")
				verifyFileMode(t, destPath, ExecutableFileMode)
			},
		},
		{
			name: "copy directory",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				srcDir := createTempDir(t, sourceDir)
				return []InstallAction{
					{
						Action:      ActionCopyDir,
						Source:      srcDir,
						Destination: filepath.Join(config.ReadOnlyConfigOutputDir, "copied_dir"),
						Mode:        RegularFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				destDir := filepath.Join(config.ReadOnlyConfigOutputDir, "copied_dir")
				verifyDirExists(t, destDir)
				verifyFileContent(t, filepath.Join(destDir, "file1.txt"), "content1")
				verifyFileContent(t, filepath.Join(destDir, "file2.txt"), "content2")
				verifyFileContent(t, filepath.Join(destDir, "subdir", "file3.txt"), "content3")
			},
		},
		{
			name: "create empty file",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				return []InstallAction{
					{
						Action:      ActionCreateEmptyFile,
						Destination: filepath.Join(config.WriteableConfigOutputDir, "empty.txt"),
						Mode:        RegularFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				destPath := filepath.Join(config.WriteableConfigOutputDir, "empty.txt")
				verifyFileExists(t, destPath)
				verifyFileContent(t, destPath, "")
				verifyFileMode(t, destPath, RegularFileMode)
			},
		},
		{
			name: "create empty directory",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				return []InstallAction{
					{
						Action:      ActionCreateEmptyDir,
						Destination: filepath.Join(config.WriteableConfigOutputDir, "emptydir"),
						Mode:        ExecutableFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				destPath := filepath.Join(config.WriteableConfigOutputDir, "emptydir")
				verifyDirExists(t, destPath)
				verifyFileMode(t, destPath, ExecutableFileMode)
			},
		},
		{
			name: "multiple actions in sequence",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				srcFile1 := createTempFile(t, sourceDir, "file1.txt", "content1")
				srcFile2 := createTempFile(t, sourceDir, "file2.txt", "template: {{.Api.Image}}")

				return []InstallAction{
					{
						Action:      ActionCopyFile,
						Source:      srcFile1,
						Destination: filepath.Join(config.QuadletFilesOutputDir, "file1.txt"),
						Template:    false,
						Mode:        RegularFileMode,
					},
					{
						Action:      ActionCopyFile,
						Source:      srcFile2,
						Destination: filepath.Join(config.QuadletFilesOutputDir, "file2.txt"),
						Template:    true,
						Mode:        RegularFileMode,
					},
					{
						Action:      ActionCreateEmptyFile,
						Destination: filepath.Join(config.WriteableConfigOutputDir, "empty.txt"),
						Mode:        RegularFileMode,
					},
					{
						Action:      ActionCreateEmptyDir,
						Destination: filepath.Join(config.WriteableConfigOutputDir, "emptydir"),
						Mode:        ExecutableFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				verifyFileContent(t, filepath.Join(config.QuadletFilesOutputDir, "file1.txt"), "content1")
				verifyFileContent(t, filepath.Join(config.QuadletFilesOutputDir, "file2.txt"), "template: test-api-image")
				verifyFileExists(t, filepath.Join(config.WriteableConfigOutputDir, "empty.txt"))
				verifyDirExists(t, filepath.Join(config.WriteableConfigOutputDir, "emptydir"))
			},
		},
		{
			name: "create empty file that already exists",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				destPath := filepath.Join(config.WriteableConfigOutputDir, "existing.txt")
				err := os.MkdirAll(filepath.Dir(destPath), 0755)
				require.NoError(t, err, "Failed to create directory")
				err = os.WriteFile(destPath, []byte("existing content"), 0600)
				require.NoError(t, err, "Failed to create existing file")

				return []InstallAction{
					{
						Action:      ActionCreateEmptyFile,
						Destination: destPath,
						Mode:        RegularFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				destPath := filepath.Join(config.WriteableConfigOutputDir, "existing.txt")
				verifyFileExists(t, destPath)
				// Should preserve existing content (not overwrite)
				verifyFileContent(t, destPath, "existing content")
			},
		},
		{
			name: "create empty directory that already exists",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				destPath := filepath.Join(config.WriteableConfigOutputDir, "existingdir")
				err := os.MkdirAll(destPath, 0755)
				require.NoError(t, err, "Failed to create existing directory")

				return []InstallAction{
					{
						Action:      ActionCreateEmptyDir,
						Destination: destPath,
						Mode:        ExecutableFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				destPath := filepath.Join(config.WriteableConfigOutputDir, "existingdir")
				verifyDirExists(t, destPath)
			},
		},
		{
			name: "unknown action type returns error",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				return []InstallAction{
					{
						Action:      ActionType(999), // Invalid action type
						Destination: filepath.Join(config.WriteableConfigOutputDir, "test.txt"),
						Mode:        RegularFileMode,
					},
				}
			},
			verify:         func(t *testing.T, config *RendererConfig) {},
			expectError:    true,
			errorSubstring: "unknown action type",
		},
		{
			name: "copy file with nested directory creation",
			setup: func(t *testing.T, sourceDir string, config *RendererConfig) []InstallAction {
				srcFile := createTempFile(t, sourceDir, "nested.txt", "nested content")
				return []InstallAction{
					{
						Action:      ActionCopyFile,
						Source:      srcFile,
						Destination: filepath.Join(config.QuadletFilesOutputDir, "deep", "nested", "path", "nested.txt"),
						Template:    false,
						Mode:        RegularFileMode,
					},
				}
			},
			verify: func(t *testing.T, config *RendererConfig) {
				destPath := filepath.Join(config.QuadletFilesOutputDir, "deep", "nested", "path", "nested.txt")
				verifyFileExists(t, destPath)
				verifyFileContent(t, destPath, "nested content")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceDir := t.TempDir()
			config := createTestConfig(t)

			manifest := tt.setup(t, sourceDir, config)

			logger := log.NewPrefixLogger("test")
			err := processInstallManifest(manifest, config, logger)

			if tt.expectError {
				require.Error(t, err, "Expected error but got none")
				if tt.errorSubstring != "" {
					require.Contains(t, err.Error(), tt.errorSubstring, "Expected error to contain %q", tt.errorSubstring)
				}
				return
			}

			require.NoError(t, err, "Unexpected error")

			tt.verify(t, config)
		})
	}
}
