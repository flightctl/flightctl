package storage

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/common"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	oscrypto "github.com/openshift/library-go/pkg/crypto"
)

// FileSystemStorageConfig defines filesystem-specific storage configuration
type FileSystemStorageConfig struct {
	// CertPath is the path where the certificate will be stored
	CertPath string `json:"cert-path"`
	// KeyPath is the path where the private key will be stored
	KeyPath string `json:"key-path"`
	// Permissions for the certificate and key files
	Permissions *int `json:"permissions,omitempty"`
}

type FileSystemStorage struct {
	CertPath         string
	KeyPath          string
	deviceReadWriter fileio.ReadWriter
	log              common.Logger
}

// NewFileSystemStorage returns a FileSystemStorage with injected file I/O and logger.
func NewFileSystemStorage(certPath, keyPath string, rw fileio.ReadWriter, log common.Logger) *FileSystemStorage {
	return &FileSystemStorage{
		CertPath:         certPath,
		KeyPath:          keyPath,
		deviceReadWriter: rw,
		log:              log,
	}
}

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

func (fs *FileSystemStorage) writeBase64(path string, data []byte) error {
	content := base64.StdEncoding.EncodeToString(data)
	mode := 0600
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

func (fs *FileSystemStorage) Delete(ctx context.Context) error {
	if err := fs.deviceReadWriter.RemoveFile(fs.CertPath); err != nil {
		fs.log.Warnf("failed to delete cert file %s: %v", fs.CertPath, err)
	}
	if err := fs.deviceReadWriter.RemoveFile(fs.KeyPath); err != nil {
		fs.log.Warnf("failed to delete key file %s: %v", fs.KeyPath, err)
	}
	return nil
}

// FileSystemStorageFactory implements StorageFactory for filesystem storage.
type FileSystemStorageFactory struct {
	rw fileio.ReadWriter
}

// NewFileSystemStorageFactory creates a new FileSystemStorageFactory.
func NewFileSystemStorageFactory(rw fileio.ReadWriter) *FileSystemStorageFactory {
	return &FileSystemStorageFactory{
		rw: rw,
	}
}

// Type returns the type string used as the map key.
func (f *FileSystemStorageFactory) Type() string {
	return "filesystem"
}

// New creates a new FileSystemStorage instance from the given config.
func (f *FileSystemStorageFactory) New(log common.Logger, cc common.CertificateConfig) (common.StorageProvider, error) {
	storage := cc.Storage

	var fsConfig FileSystemStorageConfig
	if err := json.Unmarshal(storage.Config, &fsConfig); err != nil {
		return nil, fmt.Errorf("failed to decode CSR config for certificate %q: %w", cc.Name, err)
	}
	return NewFileSystemStorage(fsConfig.CertPath, fsConfig.KeyPath, f.rw, log), nil
}

// Validate validates the storage configuration.
func (f *FileSystemStorageFactory) Validate(log common.Logger, cc common.CertificateConfig) error {
	storage := cc.Storage

	if storage.Type != "filesystem" {
		return fmt.Errorf("not a filesystem Storage")
	}

	var fsConfig FileSystemStorageConfig
	if err := json.Unmarshal(storage.Config, &fsConfig); err != nil {
		return fmt.Errorf("failed to decode CSR config for certificate %q: %w", cc.Name, err)
	}

	if fsConfig.CertPath == "" {
		return fmt.Errorf("cert-path is required for filesystem storage, certificate %s", cc.Name)
	}
	if fsConfig.KeyPath == "" {
		return fmt.Errorf("key-path is required for filesystem storage, certificate %s", cc.Name)
	}

	return nil
}
