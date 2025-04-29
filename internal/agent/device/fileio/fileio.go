package fileio

import (
	"io/fs"
	"os"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

const (
	// DefaultDirectoryPermissions houses the default mode to use when no directory permissions are provided
	DefaultDirectoryPermissions os.FileMode = 0o755
	// defaultFilePermissions houses the default mode to use when no file permissions are provided
	DefaultFilePermissions os.FileMode = 0o644
	// DefaultExecutablePermissions houses the default mode to use for executable files
	DefaultExecutablePermissions os.FileMode = 0o755
)

type ManagedFile interface {
	Path() string
	Exists() (bool, error)
	IsUpToDate() (bool, error)
	Write() error
}

type Writer interface {
	SetRootdir(path string)
	PathFor(filePath string) string
	WriteFile(name string, data []byte, perm fs.FileMode, opts ...FileOption) error
	RemoveFile(file string) error
	RemoveAll(path string) error
	MkdirAll(path string, perm fs.FileMode) error
	MkdirTemp(prefix string) (string, error)
	CopyFile(src, dst string) error
	CreateManagedFile(file v1alpha1.FileSpec) (ManagedFile, error)
	OverwriteAndWipe(file string) error
}

type Reader interface {
	SetRootdir(path string)
	PathFor(filePath string) string
	ReadFile(filePath string) ([]byte, error)
	ReadDir(dirPath string) ([]fs.DirEntry, error)
	PathExists(path string) (bool, error)
}

type ReadWriter interface {
	Reader
	Writer
}

type readWriter struct {
	*reader
	*writer
}

func NewReadWriter(opts ...Option) ReadWriter {
	rw := &readWriter{
		reader: NewReader(),
		writer: NewWriter(),
	}
	for _, opt := range opts {
		opt(rw)
	}
	return rw
}

func (rw *readWriter) SetRootdir(path string) {
	rw.reader.SetRootdir(path)
	rw.writer.SetRootdir(path)
}

func (rw *readWriter) PathFor(path string) string {
	return rw.writer.PathFor(path)
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

type fileOptions struct {
	uid int
	gid int
}

type FileOption func(*fileOptions)

// WithUid sets the uid for the file.
func WithUid(uid int) FileOption {
	return func(o *fileOptions) {
		o.uid = uid
	}
}

// WithGid sets the gid for the file.
func WithGid(gid int) FileOption {
	return func(o *fileOptions) {
		o.gid = gid
	}
}

// IsNotExist returns a boolean indicating whether the error is known to report that a file or directory does not exist.
func IsNotExist(err error) bool {
	return os.IsNotExist(err)
}
