package fileio

import (
	"io/fs"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
)

type Writer interface {
	WriteIgnitionFiles(files ...ign3types.File) error
	WriteFile(name string, data []byte, perm fs.FileMode) error
}

type Reader interface {
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

type Option func(*readWriter)

// WithTestRootDir sets the root directory for the reader and writer, useful for testing.
func WithTestRootDir(testRootDir string) Option {
	return func(rw *readWriter) {
		if testRootDir != "" {
			rw.FileReader.SetRootdir(testRootDir)
			rw.FileWriter.SetRootdir(testRootDir)
		}
	}
}
