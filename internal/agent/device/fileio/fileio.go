package fileio

import (
	"io/fs"
	"os"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/userutil"
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
	// PathFor returns the full path for the given filePath, prepending the rootDir if set
	// This is useful for testing to ensure that the file is written to the correct location
	PathFor(filePath string) string
	// WriteFile writes the provided data to the file at the path with the provided permissions and ownership information
	WriteFile(name string, data []byte, perm fs.FileMode, opts ...FileOption) error
	// RemoveFile removes the file at the given path
	RemoveFile(file string) error
	// RemoveAll removes the file or directory at the given path
	RemoveAll(path string) error
	// Rename renames (moves) oldpath to newpath
	Rename(oldpath, newpath string) error
	// RemoveContents removes all files and subdirectories within the given path,
	// but leaves the directory itself intact. It is a no-op if the path does not exist.
	RemoveContents(path string) error
	// CreateFile creates or opens the file at the given path, ensuring that it is owned by the correct user.
	CreateFile(path string, flag int, perm fs.FileMode) (*os.File, error)
	// MkdirAll creates a directory at the given path with the specified permissions.
	MkdirAll(path string, perm fs.FileMode) error
	// MkdirTemp creates a temporary directory with the given prefix and returns its path.
	MkdirTemp(prefix string) (string, error)
	// CopyFile copies a file from src to dst, creating the destination directory if it does not exist.
	CopyFile(src, dst string) error
	// CopyDir recursively copies a directory from src to dst, preserving file permissions.
	CopyDir(src, dst string, opts ...CopyDirOption) error
	// CreateManagedFile creates a managed file with the given spec.
	CreateManagedFile(file v1beta1.FileSpec) (ManagedFile, error)
	// OverwriteAndWipe overwrites the file at the given path with zeros and then deletes it.
	OverwriteAndWipe(file string) error
}

type Reader interface {
	PathFor(filePath string) string
	ReadFile(filePath string) ([]byte, error)
	ReadDir(dirPath string) ([]fs.DirEntry, error)
	PathExists(path string, opts ...PathExistsOption) (bool, error)
}

type ReadWriter interface {
	Reader
	Writer
}

type readWriter struct {
	Reader
	Writer
}

type ReadWriterFactory func(username v1beta1.Username) (ReadWriter, error)

func NewReadWriterFactory(rootDir string) ReadWriterFactory {
	return func(username v1beta1.Username) (ReadWriter, error) {
		writerOptions := []WriterOption{
			WithWriterRootDir(rootDir),
		}

		if !username.IsCurrentProcessUser() {
			uid, gid, _, err := userutil.LookupUser(username)
			if err != nil {
				return nil, err
			}
			writerOptions = append(writerOptions,
				WithUID(uid),
				WithGID(gid),
			)
		}

		return NewReadWriter(
			NewReader(WithReaderRootDir(rootDir)),
			NewWriter(writerOptions...),
		), nil
	}
}

func NewReadWriter(reader Reader, writer Writer) ReadWriter {
	return &readWriter{
		Reader: reader,
		Writer: writer,
	}
}

func (rw *readWriter) PathFor(path string) string {
	return rw.Writer.PathFor(path)
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
