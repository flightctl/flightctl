package renderer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	return filePath
}

func createTempDir(t *testing.T, baseDir string) string {
	t.Helper()

	dirPath := filepath.Join(baseDir, "testdir")
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	// Create some files in the directory
	createTempFile(t, dirPath, "file1.txt", "content1")
	createTempFile(t, dirPath, "file2.txt", "content2")

	// Create a subdirectory with a file
	subDir := filepath.Join(dirPath, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdirectory: %v", err)
	}
	createTempFile(t, subDir, "file3.txt", "content3")

	return dirPath
}

func verifyFileContent(t *testing.T, path, expectedContent string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read file %s: %v", path, err)
	}

	if string(content) != expectedContent {
		t.Errorf("File content mismatch.\nExpected: %q\nGot: %q", expectedContent, string(content))
	}
}

func verifyFileMode(t *testing.T, path string, expectedMode os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Failed to stat file %s: %v", path, err)
	}

	if info.Mode().Perm() != expectedMode {
		t.Errorf("File mode mismatch for %s.\nExpected: %o\nGot: %o", path, expectedMode, info.Mode().Perm())
	}
}

func verifyDirExists(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Directory does not exist: %s: %v", path, err)
	}

	if !info.IsDir() {
		t.Errorf("Path exists but is not a directory: %s", path)
	}
}

func verifyFileExists(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("File does not exist: %s: %v", path, err)
	}

	if info.IsDir() {
		t.Errorf("Path exists but is a directory: %s", path)
	}
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
				if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
					t.Fatalf("Failed to create directory: %v", err)
				}
				if err := os.WriteFile(destPath, []byte("existing content"), 0600); err != nil {
					t.Fatalf("Failed to create existing file: %v", err)
				}

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
				if err := os.MkdirAll(destPath, 0755); err != nil {
					t.Fatalf("Failed to create existing directory: %v", err)
				}

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

			err := processInstallManifest(manifest, config)

			if tt.expectError {
				if err == nil {
					t.Fatalf("Expected error but got none")
				}
				if tt.errorSubstring != "" && !strings.Contains(err.Error(), tt.errorSubstring) {
					t.Errorf("Expected error to contain %q, got: %v", tt.errorSubstring, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			tt.verify(t, config)
		})
	}
}
