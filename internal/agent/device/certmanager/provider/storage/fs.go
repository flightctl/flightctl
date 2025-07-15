package storage

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	oscrypto "github.com/openshift/library-go/pkg/crypto"
)

// FileSystemStorageConfig defines configuration for filesystem-based certificate storage.
// It specifies where certificates and private keys should be stored on the filesystem
// and what permissions should be applied to the files.
type FileSystemStorageConfig struct {
	// CertPath is the path where the certificate will be stored
	CertPath string `json:"cert-path"`
	// KeyPath is the path where the private key will be stored
	KeyPath string `json:"key-path"`
	// Permissions for the certificate and key files
	Permissions *int `json:"permissions,omitempty"`
}

// FileSystemStorage handles certificate storage on the local filesystem.
// It stores certificates and private keys as managed files with appropriate permissions
// and supports loading existing certificates from the filesystem.
type FileSystemStorage struct {
	CertPath         string            // Path where the certificate file will be stored
	KeyPath          string            // Path where the private key file will be stored
	Permissions      int               // File permissions for certificate and key files
	deviceReadWriter fileio.ReadWriter // File I/O interface for reading and writing files
	log              provider.Logger   // Logger for storage operations
}

// NewFileSystemStorage creates a new filesystem storage provider with the specified configuration.
// It uses the provided file I/O interface and logger for operations.
func NewFileSystemStorage(certPath, keyPath string, permissions int, rw fileio.ReadWriter, log provider.Logger) *FileSystemStorage {
	return &FileSystemStorage{
		CertPath:         certPath,
		KeyPath:          keyPath,
		Permissions:      permissions,
		deviceReadWriter: rw,
		log:              log,
	}
}

// LoadCertificate loads a certificate from the filesystem.
// It reads the certificate file and parses it as a PEM-encoded X.509 certificate.
func (fs *FileSystemStorage) LoadCertificate(ctx context.Context) (*x509.Certificate, error) {
	certPath := fs.deviceReadWriter.PathFor(fs.CertPath)
	certPEM, err := fs.deviceReadWriter.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("reading cert file: %w", err)
	}

	cert, err := fccrypto.ParsePEMCertificate(certPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PEM certificate: %w", err)
	}
	return cert, nil
}

// Write stores a certificate and private key to the filesystem.
// It creates the necessary directories and writes both files with appropriate permissions.
// The files are stored as base64-encoded managed files for consistency with the agent's file management.
func (fs *FileSystemStorage) Write(cert *x509.Certificate, keyPEM []byte) error {
	certPEM, err := oscrypto.EncodeCertificates(cert)
	if err != nil {
		return err
	}

	if err := fs.deviceReadWriter.MkdirAll(filepath.Dir(fs.CertPath), 0700); err != nil {
		return fmt.Errorf("mkdir for cert path: %w", err)
	}
	if err := fs.deviceReadWriter.MkdirAll(filepath.Dir(fs.KeyPath), 0700); err != nil {
		return fmt.Errorf("mkdir for key path: %w", err)
	}

	if err := fs.writeBase64(fs.CertPath, certPEM); err != nil {
		fs.log.Errorf("failed to write cert to %s: %v", fs.CertPath, err)
		return fmt.Errorf("write cert: %w", err)
	}

	if err := fs.writeBase64(fs.KeyPath, keyPEM); err != nil {
		fs.log.Errorf("failed to write key to %s: %v", fs.KeyPath, err)
		return fmt.Errorf("write key: %w", err)
	}

	fs.log.Infof("successfully wrote cert and key to %s and %s", fs.CertPath, fs.KeyPath)
	return nil
}

// writeBase64 writes data to a file as a base64-encoded managed file.
// This ensures consistency with the agent's file management system and proper permissions.
func (fs *FileSystemStorage) writeBase64(path string, data []byte) error {
	content := base64.StdEncoding.EncodeToString(data)
	mode := fs.Permissions
	encoding := v1alpha1.EncodingBase64

	mf, err := fs.deviceReadWriter.CreateManagedFile(v1alpha1.FileSpec{
		Path:            path,
		Content:         content,
		ContentEncoding: &encoding,
		Mode:            &mode,
	})
	if err != nil {
		return fmt.Errorf("creating managed file: %w", err)
	}

	if err := mf.Write(); err != nil {
		return fmt.Errorf("writing managed file: %w", err)
	}

	return nil
}

// Delete removes certificate and private key files from the filesystem.
// It logs warnings if files cannot be deleted but doesn't return errors
// since deletion is a cleanup operation.
func (fs *FileSystemStorage) Delete(ctx context.Context) error {
	if err := fs.deviceReadWriter.RemoveFile(fs.CertPath); err != nil {
		fs.log.Warnf("failed to delete cert file %s: %v", fs.CertPath, err)
	}
	if err := fs.deviceReadWriter.RemoveFile(fs.KeyPath); err != nil {
		fs.log.Warnf("failed to delete key file %s: %v", fs.KeyPath, err)
	}
	return nil
}

// FileSystemStorageFactory implements StorageFactory for filesystem-based certificate storage.
// It creates filesystem storage providers that store certificates and keys as files on disk.
type FileSystemStorageFactory struct {
	rw fileio.ReadWriter // File I/O interface for reading and writing files
}

// NewFileSystemStorageFactory creates a new filesystem storage factory with the specified file I/O interface.
func NewFileSystemStorageFactory(rw fileio.ReadWriter) *FileSystemStorageFactory {
	return &FileSystemStorageFactory{
		rw: rw,
	}
}

// Type returns the storage type string used as map key in the certificate manager.
func (f *FileSystemStorageFactory) Type() string {
	return "filesystem"
}

// New creates a new FileSystemStorage instance from the certificate configuration.
// It decodes the filesystem-specific configuration and sets appropriate default values.
func (f *FileSystemStorageFactory) New(log provider.Logger, cc provider.CertificateConfig) (provider.StorageProvider, error) {
	storage := cc.Storage

	var fsConfig FileSystemStorageConfig
	if err := json.Unmarshal(storage.Config, &fsConfig); err != nil {
		return nil, fmt.Errorf("failed to decode filesystem Storage config for certificate %q: %w", cc.Name, err)
	}

	permissions := 0600
	if fsConfig.Permissions != nil {
		permissions = *fsConfig.Permissions
	}

	return NewFileSystemStorage(fsConfig.CertPath, fsConfig.KeyPath, permissions, f.rw, log), nil
}

// Validate checks whether the provided configuration is valid for filesystem storage.
// It ensures required fields are present and the configuration is properly formatted.
func (f *FileSystemStorageFactory) Validate(log provider.Logger, cc provider.CertificateConfig) error {
	storage := cc.Storage

	if storage.Type != "filesystem" {
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
