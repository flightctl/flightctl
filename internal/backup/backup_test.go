package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGenerateArchiveFilename(t *testing.T) {
	tests := []struct {
		name      string
		timestamp time.Time
		want      string
	}{
		{
			name:      "When timestamp is 2026-05-21 14:30:22 UTC it should format as YYYYMMDDTHHMMSSZ",
			timestamp: time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
			want:      "flightctl-backup-20260521T143022Z.tar.gz",
		},
		{
			name:      "When timestamp has single-digit month and day it should zero-pad",
			timestamp: time.Date(2026, 1, 5, 9, 5, 3, 0, time.UTC),
			want:      "flightctl-backup-20260105T090503Z.tar.gz",
		},
		{
			name:      "When timestamp is midnight it should format correctly",
			timestamp: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
			want:      "flightctl-backup-20261231T000000Z.tar.gz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateArchiveFilename(tt.timestamp)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestBackupMetadata_JSONMarshaling(t *testing.T) {
	tests := []struct {
		name     string
		metadata BackupMetadata
		wantJSON string
	}{
		{
			name: "When metadata has all fields it should marshal correctly",
			metadata: BackupMetadata{
				Timestamp:        time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
				Version:          "1.2.0",
				DeploymentType:   DeploymentTypeKubernetes,
				DatabaseIncluded: true,
			},
			wantJSON: `{
  "timestamp": "2026-05-21T14:30:22Z",
  "version": "1.2.0",
  "deploymentType": "kubernetes",
  "databaseIncluded": true
}`,
		},
		{
			name: "When database is not included it should marshal false",
			metadata: BackupMetadata{
				Timestamp:        time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
				Version:          "1.2.0",
				DeploymentType:   DeploymentTypePodman,
				DatabaseIncluded: false,
			},
			wantJSON: `{
  "timestamp": "2026-05-21T14:30:22Z",
  "version": "1.2.0",
  "deploymentType": "podman",
  "databaseIncluded": false
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal with indentation
			gotBytes, err := json.MarshalIndent(tt.metadata, "", "  ")
			require.NoError(t, err)

			require.Equal(t, tt.wantJSON, string(gotBytes))

			// Test roundtrip: unmarshal and verify
			var unmarshaled BackupMetadata
			err = json.Unmarshal(gotBytes, &unmarshaled)
			require.NoError(t, err)
			require.Equal(t, tt.metadata, unmarshaled)
		})
	}
}

func TestWriteMetadataFile(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (stagingDir string, metadata BackupMetadata)
		wantErr bool
	}{
		{
			name: "When staging directory exists it should write metadata.json",
			setup: func(t *testing.T) (string, BackupMetadata) {
				stagingDir := t.TempDir()
				metadata := BackupMetadata{
					Timestamp:        time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
					Version:          "1.2.0",
					DeploymentType:   DeploymentTypeKubernetes,
					DatabaseIncluded: true,
				}
				return stagingDir, metadata
			},
			wantErr: false,
		},
		{
			name: "When staging directory does not exist it should return error",
			setup: func(t *testing.T) (string, BackupMetadata) {
				stagingDir := filepath.Join(t.TempDir(), "nonexistent")
				metadata := BackupMetadata{
					Timestamp:        time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
					Version:          "1.2.0",
					DeploymentType:   DeploymentTypePodman,
					DatabaseIncluded: false,
				}
				return stagingDir, metadata
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stagingDir, metadata := tt.setup(t)

			err := writeMetadataFile(stagingDir, metadata)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Verify file exists
			metadataPath := filepath.Join(stagingDir, "metadata.json")
			require.FileExists(t, metadataPath)

			// Verify file permissions are 0600
			stat, err := os.Stat(metadataPath)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0600), stat.Mode().Perm())

			// Verify content matches expected JSON
			content, err := os.ReadFile(metadataPath)
			require.NoError(t, err)

			// Unmarshal and verify
			var unmarshaled BackupMetadata
			err = json.Unmarshal(content, &unmarshaled)
			require.NoError(t, err)
			require.Equal(t, metadata, unmarshaled)
		})
	}
}

func TestCreateTarGz(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (stagingDir string, archivePath string)
		wantErr bool
	}{
		{
			name: "When staging directory has files it should create tar.gz archive",
			setup: func(t *testing.T) (string, string) {
				stagingDir := t.TempDir()

				// Create mock files
				require.NoError(t, os.WriteFile(filepath.Join(stagingDir, "file1.txt"), []byte("content1"), 0644)) //nolint:gosec
				subdir := filepath.Join(stagingDir, "subdir")
				require.NoError(t, os.MkdirAll(subdir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(subdir, "file2.txt"), []byte("content2"), 0600))

				outputDir := t.TempDir()
				archivePath := filepath.Join(outputDir, "test.tar.gz")
				return stagingDir, archivePath
			},
			wantErr: false,
		},
		{
			name: "When staging directory does not exist it should return error",
			setup: func(t *testing.T) (string, string) {
				stagingDir := filepath.Join(t.TempDir(), "nonexistent")
				outputDir := t.TempDir()
				archivePath := filepath.Join(outputDir, "test.tar.gz")
				return stagingDir, archivePath
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stagingDir, archivePath := tt.setup(t)
			log, _ := testLogger()

			ctx := context.Background()
			err := createTarGz(ctx, stagingDir, archivePath, log)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Verify archive file exists
			require.FileExists(t, archivePath)

			// Verify archive permissions are 0600
			stat, err := os.Stat(archivePath)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0600), stat.Mode().Perm())

			// Verify archive can be extracted and contains expected files
			extractDir := t.TempDir()
			extractTarGz(t, archivePath, extractDir)

			// Check extracted files exist
			require.FileExists(t, filepath.Join(extractDir, "file1.txt"))
			require.FileExists(t, filepath.Join(extractDir, "subdir", "file2.txt"))

			// Verify content
			content1, err := os.ReadFile(filepath.Join(extractDir, "file1.txt"))
			require.NoError(t, err)
			require.Equal(t, "content1", string(content1))

			content2, err := os.ReadFile(filepath.Join(extractDir, "subdir", "file2.txt"))
			require.NoError(t, err)
			require.Equal(t, "content2", string(content2))
		})
	}
}

// extractTarGz is a test helper to extract a tar.gz archive
func extractTarGz(t *testing.T, archivePath string, destDir string) {
	t.Helper()

	file, err := os.Open(archivePath)
	require.NoError(t, err)
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer gzr.Close()

	tarReader := tar.NewReader(gzr)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		target := filepath.Join(destDir, header.Name) //nolint:gosec

		switch header.Typeflag {
		case tar.TypeDir:
			require.NoError(t, os.MkdirAll(target, os.FileMode(header.Mode)))
		case tar.TypeReg:
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
			outFile, err := os.Create(target)
			require.NoError(t, err)
			_, err = io.Copy(outFile, tarReader) //nolint:gosec
			outFile.Close()
			require.NoError(t, err)
		}
	}
}

func TestGenerateChecksum(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (archivePath string, checksumPath string)
		wantErr bool
	}{
		{
			name: "When archive file exists it should generate SHA256 checksum",
			setup: func(t *testing.T) (string, string) {
				outputDir := t.TempDir()
				archivePath := filepath.Join(outputDir, "test.tar.gz")

				// Create a test archive with known content
				content := []byte("test content for checksum")
				require.NoError(t, os.WriteFile(archivePath, content, 0600))

				checksumPath := archivePath + ".sha256"
				return archivePath, checksumPath
			},
			wantErr: false,
		},
		{
			name: "When archive file does not exist it should return error",
			setup: func(t *testing.T) (string, string) {
				outputDir := t.TempDir()
				archivePath := filepath.Join(outputDir, "nonexistent.tar.gz")
				checksumPath := archivePath + ".sha256"
				return archivePath, checksumPath
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archivePath, checksumPath := tt.setup(t)

			err := generateChecksum(archivePath, checksumPath)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Verify checksum file exists
			require.FileExists(t, checksumPath)

			// Read checksum file
			checksumContent, err := os.ReadFile(checksumPath)
			require.NoError(t, err)

			// Verify format: <hash>  <filename> (two spaces)
			checksumStr := string(checksumContent)
			parts := strings.Split(checksumStr, "  ")
			require.Len(t, parts, 2, "checksum format should be '<hash>  <filename>'")

			// Verify hash is valid hex
			hashStr := parts[0]
			require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), hashStr, "hash should be 64-character hex string")

			// Verify filename matches archive basename
			filename := strings.TrimSpace(parts[1])
			require.Equal(t, filepath.Base(archivePath), filename)

			// Verify hash is correct by recomputing
			archiveContent, err := os.ReadFile(archivePath)
			require.NoError(t, err)
			expectedHash := sha256.Sum256(archiveContent)
			expectedHashStr := hex.EncodeToString(expectedHash[:])
			require.Equal(t, expectedHashStr, hashStr, "hash should match actual file content")
		})
	}
}

func TestCreateArchive(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (stagingDir string, outputDir string, metadata BackupMetadata)
		wantErr bool
	}{
		{
			name: "When staging directory has db and pki it should create complete archive",
			setup: func(t *testing.T) (string, string, BackupMetadata) {
				stagingDir := t.TempDir()

				// Create mock backup files
				dbDir := filepath.Join(stagingDir, "db")
				require.NoError(t, os.MkdirAll(dbDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dbDir, "dump.sql"), []byte("SQL dump"), 0600))

				pkiDir := filepath.Join(stagingDir, "pki")
				require.NoError(t, os.MkdirAll(pkiDir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(pkiDir, "ca.crt"), []byte("cert"), 0600))

				outputDir := t.TempDir()
				metadata := BackupMetadata{
					Timestamp:        time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
					Version:          "1.2.0",
					DeploymentType:   DeploymentTypeKubernetes,
					DatabaseIncluded: true,
				}
				return stagingDir, outputDir, metadata
			},
			wantErr: false,
		},
		{
			name: "When staging directory is empty it should create archive with metadata only",
			setup: func(t *testing.T) (string, string, BackupMetadata) {
				stagingDir := t.TempDir()
				outputDir := t.TempDir()
				metadata := BackupMetadata{
					Timestamp:        time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
					Version:          "1.2.0",
					DeploymentType:   DeploymentTypePodman,
					DatabaseIncluded: false,
				}
				return stagingDir, outputDir, metadata
			},
			wantErr: false,
		},
		{
			name: "When staging directory does not exist it should return error",
			setup: func(t *testing.T) (string, string, BackupMetadata) {
				stagingDir := filepath.Join(t.TempDir(), "nonexistent")
				outputDir := t.TempDir()
				metadata := BackupMetadata{
					Timestamp:        time.Date(2026, 5, 21, 14, 30, 22, 0, time.UTC),
					Version:          "1.2.0",
					DeploymentType:   DeploymentTypeKubernetes,
					DatabaseIncluded: true,
				}
				return stagingDir, outputDir, metadata
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stagingDir, outputDir, metadata := tt.setup(t)
			log, _ := testLogger()

			ctx := context.Background()
			archivePath, checksumPath, err := CreateArchive(ctx, stagingDir, outputDir, metadata, log)

			if tt.wantErr {
				require.Error(t, err)
				// Verify cleanup: no partial artifacts left behind
				entries, _ := os.ReadDir(outputDir)
				require.Empty(t, entries, "output directory should be empty after error")
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, archivePath)
			require.NotEmpty(t, checksumPath)

			// Verify archive file exists with correct permissions
			stat, err := os.Stat(archivePath)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0600), stat.Mode().Perm())

			// Verify checksum file exists
			require.FileExists(t, checksumPath)

			// Verify archive filename format (YYYYMMDDTHHMMSSZ)
			basename := filepath.Base(archivePath)
			require.Regexp(t, regexp.MustCompile(`^flightctl-backup-\d{8}T\d{6}Z\.tar\.gz$`), basename)

			// Verify archive contains metadata.json
			extractDir := t.TempDir()
			extractTarGz(t, archivePath, extractDir)
			require.FileExists(t, filepath.Join(extractDir, "metadata.json"))

			// Verify metadata content
			metadataBytes, err := os.ReadFile(filepath.Join(extractDir, "metadata.json"))
			require.NoError(t, err)
			var extractedMetadata BackupMetadata
			require.NoError(t, json.Unmarshal(metadataBytes, &extractedMetadata))
			require.Equal(t, metadata, extractedMetadata)
		})
	}
}

// mockDeployer is a test implementation of the Deployer interface
type mockDeployer struct {
	deploymentType             DeploymentType
	backupDBError              error
	backupPKIError             error
	backupEncryptionKeysError  error
	backupConfigError          error
	backupDBCalled             bool
	backupPKICalled            bool
	backupEncryptionKeysCalled bool
	backupConfigCalled         bool
}

func (m *mockDeployer) Type() DeploymentType {
	return m.deploymentType
}

func (m *mockDeployer) BackupDatabase(ctx context.Context, outputDir string) error {
	m.backupDBCalled = true
	if m.backupDBError != nil {
		return m.backupDBError
	}
	// Create mock database backup
	dbDir := filepath.Join(outputDir, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dbDir, "dump.sql"), []byte("mock db dump"), 0600)
}

func (m *mockDeployer) BackupPKI(ctx context.Context, outputDir string) error {
	m.backupPKICalled = true
	if m.backupPKIError != nil {
		return m.backupPKIError
	}
	// Create mock PKI backup
	pkiDir := filepath.Join(outputDir, "pki")
	if err := os.MkdirAll(pkiDir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(pkiDir, "ca.crt"), []byte("mock cert"), 0600)
}

func (m *mockDeployer) BackupEncryptionKeys(ctx context.Context, outputDir string) error {
	m.backupEncryptionKeysCalled = true
	if m.backupEncryptionKeysError != nil {
		return m.backupEncryptionKeysError
	}
	// Create mock encryption key backup
	encDir := filepath.Join(outputDir, "encryption")
	if err := os.MkdirAll(encDir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(encDir, "flightctl-encryption-key.yaml"), []byte("mock encryption key"), 0600)
}

func (m *mockDeployer) BackupConfig(ctx context.Context, outputDir string) error {
	m.backupConfigCalled = true
	if m.backupConfigError != nil {
		return m.backupConfigError
	}
	// Stub implementation (as per plan)
	return nil
}

func TestPerformBackup(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) (deployer *mockDeployer, outputDir string)
		wantErr  bool
		validate func(t *testing.T, deployer *mockDeployer, archivePath string)
	}{
		{
			name: "When backup succeeds it should create archive with all components",
			setup: func(t *testing.T) (*mockDeployer, string) {
				deployer := &mockDeployer{
					deploymentType: DeploymentTypeKubernetes,
				}
				outputDir := t.TempDir()
				return deployer, outputDir
			},
			wantErr: false,
			validate: func(t *testing.T, deployer *mockDeployer, archivePath string) {
				// Verify all deployer methods were called
				require.True(t, deployer.backupDBCalled, "BackupDatabase should be called")
				require.True(t, deployer.backupPKICalled, "BackupPKI should be called")
				require.True(t, deployer.backupEncryptionKeysCalled, "BackupEncryptionKeys should be called")
				require.True(t, deployer.backupConfigCalled, "BackupConfig should be called")

				// Verify archive was created
				require.FileExists(t, archivePath)
				require.FileExists(t, archivePath+".sha256")

				// Verify archive contains expected files
				extractDir := t.TempDir()
				extractTarGz(t, archivePath, extractDir)
				require.FileExists(t, filepath.Join(extractDir, "metadata.json"))
				require.FileExists(t, filepath.Join(extractDir, "db", "dump.sql"))
				require.FileExists(t, filepath.Join(extractDir, "pki", "ca.crt"))
				require.FileExists(t, filepath.Join(extractDir, "encryption", "flightctl-encryption-key.yaml"))

				// Verify metadata includes database
				metadataBytes, err := os.ReadFile(filepath.Join(extractDir, "metadata.json"))
				require.NoError(t, err)
				var metadata BackupMetadata
				require.NoError(t, json.Unmarshal(metadataBytes, &metadata))
				require.True(t, metadata.DatabaseIncluded)
				require.Equal(t, DeploymentTypeKubernetes, metadata.DeploymentType)
			},
		},
		{
			name: "When database is external it should create archive without database",
			setup: func(t *testing.T) (*mockDeployer, string) {
				deployer := &mockDeployer{
					deploymentType: DeploymentTypePodman,
					backupDBError:  ErrExternalDatabase,
				}
				outputDir := t.TempDir()
				return deployer, outputDir
			},
			wantErr: false,
			validate: func(t *testing.T, deployer *mockDeployer, archivePath string) {
				// Verify archive was created
				require.FileExists(t, archivePath)

				// Verify archive does NOT contain db directory
				extractDir := t.TempDir()
				extractTarGz(t, archivePath, extractDir)
				require.FileExists(t, filepath.Join(extractDir, "metadata.json"))
				require.NoFileExists(t, filepath.Join(extractDir, "db", "dump.sql"))

				// Verify metadata shows database not included
				metadataBytes, err := os.ReadFile(filepath.Join(extractDir, "metadata.json"))
				require.NoError(t, err)
				var metadata BackupMetadata
				require.NoError(t, json.Unmarshal(metadataBytes, &metadata))
				require.False(t, metadata.DatabaseIncluded)
			},
		},
		{
			name: "When BackupDatabase fails with non-external error it should return error",
			setup: func(t *testing.T) (*mockDeployer, string) {
				deployer := &mockDeployer{
					deploymentType: DeploymentTypeKubernetes,
					backupDBError:  errors.New("database connection failed"),
				}
				outputDir := t.TempDir()
				return deployer, outputDir
			},
			wantErr: true,
			validate: func(t *testing.T, deployer *mockDeployer, archivePath string) {
				// Verify no archive was created (staging dir cleaned up)
				require.Equal(t, "", archivePath)
			},
		},
		{
			name: "When BackupPKI fails it should return error and cleanup",
			setup: func(t *testing.T) (*mockDeployer, string) {
				deployer := &mockDeployer{
					deploymentType: DeploymentTypePodman,
					backupPKIError: errors.New("PKI backup failed"),
				}
				outputDir := t.TempDir()
				return deployer, outputDir
			},
			wantErr: true,
			validate: func(t *testing.T, deployer *mockDeployer, archivePath string) {
				require.Equal(t, "", archivePath)
			},
		},
		{
			name: "When BackupEncryptionKeys fails it should return error and skip config",
			setup: func(t *testing.T) (*mockDeployer, string) {
				deployer := &mockDeployer{
					deploymentType:            DeploymentTypeKubernetes,
					backupEncryptionKeysError: errors.New("encryption key access denied"),
				}
				outputDir := t.TempDir()
				return deployer, outputDir
			},
			wantErr: true,
			validate: func(t *testing.T, deployer *mockDeployer, archivePath string) {
				require.Equal(t, "", archivePath)
				require.True(t, deployer.backupDBCalled, "BackupDatabase should be called before encryption keys")
				require.True(t, deployer.backupPKICalled, "BackupPKI should be called before encryption keys")
				require.True(t, deployer.backupEncryptionKeysCalled, "BackupEncryptionKeys should be called")
				require.False(t, deployer.backupConfigCalled, "BackupConfig should NOT be called after encryption key failure")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployer, outputDir := tt.setup(t)
			log, _ := testLogger()

			ctx := context.Background()
			archivePath, err := PerformBackup(ctx, deployer, outputDir, log)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			tt.validate(t, deployer, archivePath)
		})
	}
}
