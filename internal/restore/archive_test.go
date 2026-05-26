package restore

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/stretchr/testify/require"
)

// buildTestArchiveWithDirs creates a tar.gz archive with explicit directory
// entries and file entries. dirNames are added as tar.TypeDir entries first,
// then files (map of relative path → content) as tar.TypeReg entries.
func buildTestArchiveWithDirs(t *testing.T, dirNames []string, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "test-archive.tar.gz")

	f, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	require.NoError(t, err)

	gzw := gzip.NewWriter(f)
	tw := tar.NewWriter(gzw)

	for _, name := range dirNames {
		hdr := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeDir,
			Mode:     0700,
		}
		require.NoError(t, tw.WriteHeader(hdr))
	}

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0600,
			Size: int64(len(content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	require.NoError(t, f.Close())

	return archivePath
}

// buildMaliciousArchive creates a tar.gz archive with a path traversal entry.
func buildMaliciousArchive(t *testing.T, maliciousPath string) string {
	t.Helper()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "malicious.tar.gz")

	f, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	require.NoError(t, err)

	gzw := gzip.NewWriter(f)
	tw := tar.NewWriter(gzw)

	content := "evil content"
	hdr := &tar.Header{
		Name: maliciousPath,
		Mode: 0600,
		Size: int64(len(content)),
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err = tw.Write([]byte(content))
	require.NoError(t, err)

	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	require.NoError(t, f.Close())

	return archivePath
}

// buildTestArchive creates a real tar.gz archive containing the provided files
// (map of relative path → content) and returns the path to the archive.
// The archive is created in t.TempDir() and cleaned up by the test.
func buildTestArchive(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "test-archive.tar.gz")

	f, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	require.NoError(t, err)

	gzw := gzip.NewWriter(f)
	tw := tar.NewWriter(gzw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0600,
			Size: int64(len(content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	require.NoError(t, f.Close())

	return archivePath
}

// buildTestMetadataJSON returns the JSON encoding of a BackupMetadata value.
func buildTestMetadataJSON(t *testing.T, m backup.BackupMetadata) string {
	t.Helper()
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return string(data)
}

// TestExtractArchive validates the behavioral contract of ExtractArchive.
func TestExtractArchive(t *testing.T) {
	validMetadata := backup.BackupMetadata{
		Timestamp:        time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
		Version:          "1.2.0",
		DeploymentType:   backup.DeploymentTypePodman,
		DatabaseIncluded: true,
	}

	tests := []struct {
		name        string
		setup       func(t *testing.T) string // returns archivePath
		setupCtx    func() context.Context    // nil means context.Background()
		wantErr     bool
		errContains string
		validate    func(t *testing.T, extractDir string)
	}{
		{
			name: "When archive is a valid tar.gz it should extract and return temp dir",
			setup: func(t *testing.T) string {
				return buildTestArchive(t, map[string]string{
					"metadata.json": buildTestMetadataJSON(t, validMetadata),
					"db/dump.sql":   "-- sql dump",
				})
			},
			validate: func(t *testing.T, extractDir string) {
				require.DirExists(t, extractDir)
				require.FileExists(t, filepath.Join(extractDir, "metadata.json"))
				require.FileExists(t, filepath.Join(extractDir, "db", "dump.sql"))
			},
		},
		{
			name: "When archive path does not exist it should return error",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent.tar.gz")
			},
			wantErr:     true,
			errContains: "nonexistent.tar.gz",
		},
		{
			name: "When archive path is a directory it should return error",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr:     true,
			errContains: "not a regular file",
		},
		{
			name: "When archive contains invalid gzip data it should return error",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				p := filepath.Join(dir, "bad.tar.gz")
				require.NoError(t, os.WriteFile(p, []byte("this is not gzip"), 0600))
				return p
			},
			wantErr: true,
		},
		{
			name: "When context is already cancelled it should return context error",
			setup: func(t *testing.T) string {
				return buildTestArchive(t, map[string]string{
					"metadata.json": buildTestMetadataJSON(t, validMetadata),
				})
			},
			setupCtx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: true,
		},
		{
			name: "When archive contains a path traversal entry it should neutralize the path within extraction dir",
			setup: func(t *testing.T) string {
				return buildMaliciousArchive(t, "../../../etc/evil")
			},
			validate: func(t *testing.T, extractDir string) {
				// ../../../etc/evil is cleaned to /etc/evil then joined under extractDir,
				// so the file must land inside extractDir, never outside it.
				require.FileExists(t, filepath.Join(extractDir, "etc", "evil"))
			},
		},
		{
			name: "When archive has explicit directory entries it should create directories",
			setup: func(t *testing.T) string {
				return buildTestArchiveWithDirs(t,
					[]string{"subdir/"},
					map[string]string{
						"metadata.json":          buildTestMetadataJSON(t, validMetadata),
						"subdir/nested-file.txt": "content",
					},
				)
			},
			validate: func(t *testing.T, extractDir string) {
				require.DirExists(t, filepath.Join(extractDir, "subdir"))
				require.FileExists(t, filepath.Join(extractDir, "subdir", "nested-file.txt"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archivePath := tt.setup(t)

			ctx := context.Background()
			if tt.setupCtx != nil {
				ctx = tt.setupCtx()
			}

			extractDir, err := ExtractArchive(ctx, archivePath)

			// extractDir must always be "" on error (caller cleanup safety)
			if err != nil {
				require.Empty(t, extractDir, "extractDir must be empty string on error")
			}

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.ErrorContains(t, err, tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, extractDir)

			// Caller cleanup responsibility
			defer os.RemoveAll(extractDir)

			if tt.validate != nil {
				tt.validate(t, extractDir)
			}
		})
	}
}

// TestReadMetadata validates the behavioral contract of ReadMetadata.
func TestReadMetadata(t *testing.T) {
	validMetadata := backup.BackupMetadata{
		Timestamp:        time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
		Version:          "1.2.0",
		DeploymentType:   backup.DeploymentTypeKubernetes,
		DatabaseIncluded: false,
	}

	tests := []struct {
		name        string
		setup       func(t *testing.T) string // returns extractDir
		wantErr     bool
		errContains string
		validate    func(t *testing.T, got *backup.BackupMetadata)
	}{
		{
			name: "When metadata.json exists with all fields it should return parsed metadata",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				data, err := json.MarshalIndent(validMetadata, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0600))
				return dir
			},
			validate: func(t *testing.T, got *backup.BackupMetadata) {
				require.Equal(t, validMetadata.Version, got.Version)
				require.Equal(t, validMetadata.DeploymentType, got.DeploymentType)
				require.Equal(t, validMetadata.DatabaseIncluded, got.DatabaseIncluded)
				require.True(t, validMetadata.Timestamp.Equal(got.Timestamp))
			},
		},
		{
			name: "When metadata.json is missing it should return error",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr:     true,
			errContains: "metadata.json",
		},
		{
			name: "When metadata.json contains invalid JSON it should return error",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("{not valid json"), 0600))
				return dir
			},
			wantErr: true,
		},
		{
			name: "When metadata.json has unknown deploymentType it should parse without error",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				m := validMetadata
				m.DeploymentType = backup.DeploymentType("future-type")
				data, err := json.Marshal(m)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0600))
				return dir
			},
			validate: func(t *testing.T, got *backup.BackupMetadata) {
				require.Equal(t, backup.DeploymentType("future-type"), got.DeploymentType)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractDir := tt.setup(t)

			got, err := ReadMetadata(extractDir)

			if tt.wantErr {
				require.Error(t, err)
				require.Nil(t, got)
				if tt.errContains != "" {
					require.ErrorContains(t, err, tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			if tt.validate != nil {
				tt.validate(t, got)
			}
		})
	}
}

// TestValidateDeploymentType validates the behavioral contract of ValidateDeploymentType.
func TestValidateDeploymentType(t *testing.T) {
	tests := []struct {
		name        string
		archiveType backup.DeploymentType
		currentType backup.DeploymentType
		wantErr     bool
		errContains string
	}{
		{
			name:        "When archive type is podman and current is podman it should return nil",
			archiveType: backup.DeploymentTypePodman,
			currentType: backup.DeploymentTypePodman,
		},
		{
			name:        "When archive type is kubernetes and current is kubernetes it should return nil",
			archiveType: backup.DeploymentTypeKubernetes,
			currentType: backup.DeploymentTypeKubernetes,
		},
		{
			name:        "When archive type is podman and current is kubernetes it should return error naming both types",
			archiveType: backup.DeploymentTypePodman,
			currentType: backup.DeploymentTypeKubernetes,
			wantErr:     true,
			errContains: "podman",
		},
		{
			name:        "When archive type is kubernetes and current is podman it should return error naming both types",
			archiveType: backup.DeploymentTypeKubernetes,
			currentType: backup.DeploymentTypePodman,
			wantErr:     true,
			errContains: "kubernetes",
		},
		{
			name:        "When archive type is unknown it should return error",
			archiveType: backup.DeploymentTypeUnknown,
			currentType: backup.DeploymentTypePodman,
			wantErr:     true,
			errContains: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := &backup.BackupMetadata{
				Timestamp:      time.Now(),
				Version:        "1.2.0",
				DeploymentType: tt.archiveType,
			}

			err := ValidateDeploymentType(metadata, tt.currentType)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.ErrorContains(t, err, tt.errContains)
				}
				return
			}

			require.NoError(t, err)
		})
	}
}
