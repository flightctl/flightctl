package fileio

import (
	"io/fs"
	"os"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
)

type ManagedFile interface {
	Path() string
	Exists() (bool, error)
	IsUpToDate() (bool, error)
	Write() error
}

type Writer interface {
	SetRootdir(path string)
	WriteFileBytes(name string, data []byte, perm os.FileMode) error
	WriteFile(name string, data []byte, perm fs.FileMode) error
	RemoveFile(file string) error
	CreateManagedFile(file ign3types.File) ManagedFile
}

type Reader interface {
	SetRootdir(path string)
	PathFor(filePath string) string
	ReadFile(filePath string) ([]byte, error)
	FileExists(filePath string) (bool, error)
}

type ReadWriter interface {
	Reader
	Writer
}

type readWriter struct {
	*FileReader
	*FileWriter
}

func NewReadWriter(opts ...Option) ReadWriter {
	rw := &readWriter{
		FileReader: NewReader(),
		FileWriter: NewWriter(),
	}
	for _, opt := range opts {
		opt(rw)
	}
	return rw
}

func (rw *readWriter) SetRootdir(path string) {
	rw.FileReader.SetRootdir(path)
	rw.FileWriter.SetRootdir(path)
}

type Option func(*readWriter)

// WithTestRootDir sets the root directory for the reader and writer, useful for testing.
func WithTestRootDir(testRootDir string) Option {
	return func(rw *readWriter) {
		if testRootDir != "" {
			rw.SetRootdir(testRootDir)
		}
	}
}
