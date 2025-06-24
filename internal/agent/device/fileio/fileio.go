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
	// SetRootdir sets the root directory for the writer, useful for testing
	SetRootdir(path string)
	// PathFor returns the full path for the given filePath, prepending the rootDir if set
	// This is useful for testing to ensure that the file is written to the correct location
	PathFor(filePath string) string
	// WriteFile writes the provided data to the file at the path with the provided permissions and ownership information
	WriteFile(name string, data []byte, perm fs.FileMode, opts ...FileOption) error
	// RemoveFile removes the file at the given path
	RemoveFile(file string) error
	// RemoveAll removes the file or directory at the given path
	RemoveAll(path string) error
	// RemoveContents removes all files and subdirectories within the given path,
	// but leaves the directory itself intact. It is a no-op if the path does not exist.
	RemoveContents(path string) error
	// MkdirAll creates a directory at the given path with the specified permissions.
	MkdirAll(path string, perm fs.FileMode) error
	// MkdirTemp creates a temporary directory with the given prefix and returns its path.
	MkdirTemp(prefix string) (string, error)
	// CopyFile copies a file from src to dst, creating the destination directory if it does not exist.
	CopyFile(src, dst string) error
	// CreateManagedFile creates a managed file with the given spec.
	CreateManagedFile(file v1alpha1.FileSpec) (ManagedFile, error)
	// OverwriteAndWipe overwrites the file at the given path with zeros and then deletes it.
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
