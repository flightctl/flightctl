package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

// FileSystemStorage implements provider.Storage
type FileSystemStorage struct {
	CertPath         string
	KeyPath          string
	deviceReadWriter fileio.ReadWriter
	log              *log.PrefixLogger
}

// NewFileSystemStorage returns a FileSystemStorage with injected file I/O and logger.
func NewFileSystemStorage(certPath, keyPath string, rw fileio.ReadWriter, log *log.PrefixLogger) *FileSystemStorage {
	return &FileSystemStorage{
		CertPath:         certPath,
		KeyPath:          keyPath,
		deviceReadWriter: rw,
		log:              log,
	}
}

func (fs *FileSystemStorage) Load(ctx context.Context) ([]byte, []byte, error) {
	certPath := fs.deviceReadWriter.PathFor(fs.CertPath)
	keyPath := fs.deviceReadWriter.PathFor(fs.KeyPath)

	cert, err := fs.deviceReadWriter.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading cert file: %w", err)
	}

	key, err := fs.deviceReadWriter.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading key file: %w", err)
	}

	return cert, key, nil
}

func (fs *FileSystemStorage) Writer(ctx context.Context) (StorageWriter, error) {
	if err := fs.deviceReadWriter.MkdirAll(filepath.Dir(fs.CertPath), 0700); err != nil {
		return nil, fmt.Errorf("mkdir for cert path: %w", err)
	}
	if err := fs.deviceReadWriter.MkdirAll(filepath.Dir(fs.KeyPath), 0700); err != nil {
		return nil, fmt.Errorf("mkdir for key path: %w", err)
	}

	return &fsWriter{
		rw:       fs.deviceReadWriter,
		certPath: fs.CertPath,
		keyPath:  fs.KeyPath,
		log:      fs.log,
	}, nil
}

func (fs *FileSystemStorage) Delete(ctx context.Context) error {
	// TODO: handle specific errors (e.g., log if not exists vs. permission issue)
	_ = fs.deviceReadWriter.RemoveFile(fs.CertPath)
	_ = fs.deviceReadWriter.RemoveFile(fs.KeyPath)
	return nil
}

type fsWriter struct {
	rw       fileio.ReadWriter
	certPath string
	keyPath  string
	log      *log.PrefixLogger
}

func (w *fsWriter) Write(certPEM, keyPEM []byte) error {
	return w.WriteFrom(
		bytes.NewReader(certPEM),
		bytes.NewReader(keyPEM),
	)
}
func (w *fsWriter) WriteFrom(cert io.Reader, key io.Reader) error {
	if err := w.writeStreamBase64(w.certPath, cert); err != nil {
		w.log.Errorf("failed to stream-write cert to %s: %v", w.certPath, err)
		return fmt.Errorf("write cert stream: %w", err)
	}

	if err := w.writeStreamBase64(w.keyPath, key); err != nil {
		w.log.Errorf("failed to stream-write key to %s: %v", w.keyPath, err)
		return fmt.Errorf("write key stream: %w", err)
	}

	w.log.Infof("successfully streamed cert and key to %s and %s", w.certPath, w.keyPath)
	return nil
}

func (w *fsWriter) writeStreamBase64(path string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading from reader: %w", err)
	}

	content := base64.StdEncoding.EncodeToString(data)
	mode := 0600
	encoding := v1alpha1.EncodingBase64

	mf, err := w.rw.CreateManagedFile(v1alpha1.FileSpec{
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
