package provider

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/certmanager"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

const (
	StorageTypeFilesystem certmanager.StorageType = "filesystem"
)

// FileSystemStorageConfig defines configuration for filesystem-based certificate storage.
// It specifies where certificates and private keys should be stored on the filesystem
// and what permissions should be applied to the files.
type FileSystemStorageConfig struct {
	// CertPath is the path where the certificate will be stored
	CertPath string `json:"cert-path"`
	// KeyPath is the path where the private key will be stored
	KeyPath string `json:"key-path"`
}

// FileSystemStorage handles certificate storage on the local filesystem.
// It stores certificates and private keys as managed files with appropriate permissions
// and supports loading existing certificates from the filesystem.
type FileSystemStorage struct {
	// Path where the certificate file will be stored
	CertPath string
	// Path where the private key file will be stored
	KeyPath string
	// File I/O interface for reading and writing files
	deviceReadWriter fileio.ReadWriter
	// Logger for storage operations
	log certmanager.Logger
}

// NewFileSystemStorage creates a new filesystem storage provider with the specified configuration.
// It uses the provided file I/O interface and logger for operations.
func NewFileSystemStorage(certPath, keyPath string, rw fileio.ReadWriter, log certmanager.Logger) *FileSystemStorage {
	return &FileSystemStorage{
		CertPath:         certPath,
		KeyPath:          keyPath,
		deviceReadWriter: rw,
		log:              log,
	}
}

// LoadCertificate loads a certificate from the filesystem.
// It reads the certificate file and parses it as a PEM-encoded X.509 certificate.
func (fs *FileSystemStorage) LoadCertificate(_ context.Context) (*x509.Certificate, error) {
	certPEM, err := fs.deviceReadWriter.ReadFile(fs.CertPath)
	if err != nil {
		return nil, fmt.Errorf("reading cert file: %w", err)
	}

	cert, err := fccrypto.ParsePEMCertificate(certPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PEM certificate: %w", err)
	}
	return cert, nil
}

// Store stores a certificate and private key to the filesystem.
// It creates the necessary directories and writes both files with appropriate permissions.
func (fs *FileSystemStorage) Store(ctx context.Context, req certmanager.StoreRequest) error {
	if req.Result.Cert == nil {
		return fmt.Errorf("filesystem storage: nil certificate")
	}

	certPEM := req.Result.Cert

	if err := fs.deviceReadWriter.MkdirAll(filepath.Dir(fs.CertPath), 0o700); err != nil {
		return fmt.Errorf("mkdir for cert path: %w", err)
	}
	// write certificate (0644)
	if err := fs.deviceReadWriter.WriteFile(fs.CertPath, certPEM, fileio.DefaultFilePermissions); err != nil {
		fs.log.Errorf("failed to write cert to %s: %v", fs.CertPath, err)
		return fmt.Errorf("write cert: %w", err)
	}

	if req.Result.Key != nil {
		if err := fs.deviceReadWriter.MkdirAll(filepath.Dir(fs.KeyPath), 0o700); err != nil {
			return fmt.Errorf("mkdir for key path: %w", err)
		}
		if err := fs.deviceReadWriter.WriteFile(fs.KeyPath, req.Result.Key, 0o600); err != nil {
			fs.log.Errorf("failed to write key to %s: %v", fs.KeyPath, err)
			return fmt.Errorf("write key: %w", err)
		}
		fs.log.Debugf("Successfully wrote cert and key to %s and %s", fs.CertPath, fs.KeyPath)
	} else {
		fs.log.Debugf("Successfully wrote cert to %s", fs.CertPath)
	}

	// Best-effort cleanup. Never fail Store if cleanup fails.
	fs.deleteOldBestEffort(req)

	return nil
}

func (fs *FileSystemStorage) deleteOldBestEffort(req certmanager.StoreRequest) {
	if req.LastApplied.IsEmpty() {
		return
	}

	var lastCfg FileSystemStorageConfig
	if err := json.Unmarshal(req.LastApplied.Config, &lastCfg); err != nil {
		fs.log.Debugf("filesystem storage: cannot decode last-applied config for cleanup: %v", err)
		return
	}

	// Remove old cert if path changed.
	if lastCfg.CertPath != "" && lastCfg.CertPath != fs.CertPath {
		if err := fs.deviceReadWriter.RemoveFile(lastCfg.CertPath); err != nil {
			fs.log.Warnf("filesystem storage: failed to delete old cert %s: %v", lastCfg.CertPath, err)
		}
	}

	// Remove old key if path changed.
	if lastCfg.KeyPath != "" && lastCfg.KeyPath != fs.KeyPath {
		if err := fs.deviceReadWriter.RemoveFile(lastCfg.KeyPath); err != nil {
			fs.log.Warnf("filesystem storage: failed to delete old key %s: %v", lastCfg.KeyPath, err)
		}
	}
}

// FileSystemStorageFactory implements StorageFactory for filesystem-based certificate storage.
// It creates filesystem storage providers that store certificates and keys as files on disk.
type FileSystemStorageFactory struct {
	// File I/O interface for reading and writing files
	rw fileio.ReadWriter
}

// NewFileSystemStorageFactory creates a new filesystem storage factory with the specified file I/O interface.
func NewFileSystemStorageFactory(rw fileio.ReadWriter) *FileSystemStorageFactory {
	return &FileSystemStorageFactory{
		rw: rw,
	}
}

// Type returns the storage type string used as map key in the certificate manager.
func (f *FileSystemStorageFactory) Type() string {
	return string(StorageTypeFilesystem)
}

// New creates a new FileSystemStorage instance from the certificate configuration.
// It decodes the filesystem-specific configuration and sets appropriate default values.
func (f *FileSystemStorageFactory) New(log certmanager.Logger, cc certmanager.CertificateConfig) (certmanager.StorageProvider, error) {
	storage := cc.Storage

	var fsConfig FileSystemStorageConfig
	if err := json.Unmarshal(storage.Config, &fsConfig); err != nil {
		return nil, fmt.Errorf("failed to decode filesystem Storage config for certificate %q: %w", cc.Name, err)
	}

	return NewFileSystemStorage(fsConfig.CertPath, fsConfig.KeyPath, f.rw, log), nil
}

// Validate checks whether the provided configuration is valid for filesystem storage.
// It ensures required fields are present and the configuration is properly formatted.
func (f *FileSystemStorageFactory) Validate(log certmanager.Logger, cc certmanager.CertificateConfig) error {
	storage := cc.Storage

	if storage.Type != StorageTypeFilesystem {
		return fmt.Errorf("not a filesystem Storage")
	}

	var fsConfig FileSystemStorageConfig
	if err := json.Unmarshal(storage.Config, &fsConfig); err != nil {
		return fmt.Errorf("failed to decode filesystem Storage config for certificate %q: %w", cc.Name, err)
	}

	if fsConfig.CertPath == "" {
		return fmt.Errorf("cert-path is required for filesystem storage, certificate %s", cc.Name)
	}
	if fsConfig.KeyPath == "" {
		return fmt.Errorf("key-path is required for filesystem storage, certificate %s", cc.Name)
	}

	return nil
}
