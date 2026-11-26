package fileio

import (
	"context"
	"fmt"
	"io"
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
	// WriteFileDurable syncs replacement bytes and newly created directory entries before success.
	WriteFileDurable(name string, data []byte, dirMode, fileMode fs.FileMode, opts ...FileOption) error
	// RemoveFile removes the file at the given path
	RemoveFile(file string) error
	// RemoveEmptyDir removes the directory at the given path only if it is empty,
	// reporting whether it was actually removed. It is a no-op (removed=false) if
	// the directory does not exist, is a symlink or non-directory, or still
	// contains entries.
	RemoveEmptyDir(dir string) (bool, error)
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
	ReadFileLimited(filePath string, limit int64) ([]byte, error)
	// ReadFileInRoot reads relPath beneath root without following a symbolic link
	// in any component of relPath. It fails for absolute or escaping paths,
	// non-regular files, and files larger than limit.
	ReadFileInRoot(root, relPath string, limit int64) ([]byte, error)
	ReadDir(dirPath string) ([]fs.DirEntry, error)
	PathExists(path string, opts ...PathExistsOption) (bool, error)
}

type ReadWriter interface {
	Reader
	Writer
	// OpenAnchor opens path (relative to the configured root directory) as a
	// directory handle. With Trusted set, every component from the root down to
	// path is opened without following symbolic links and must be a directory
	// owned by root or the effective user without group or other write access.
	OpenAnchor(path string, opts DirOptions) (Anchor, error)
}

// EntryInfo describes a directory entry without following symbolic links.
type EntryInfo struct {
	Name string
	// Type holds the fs.ModeType bits of the entry, or 0 for a regular file.
	Type fs.FileMode
	// Perm holds the permission bits plus fs.ModeSetuid, fs.ModeSetgid and
	// fs.ModeSticky when set.
	Perm  fs.FileMode
	Nlink uint64
	Size  int64
	UID   uint32
	Dev   uint64
	Ino   uint64
}

// IsDir reports whether the entry is a directory.
func (e EntryInfo) IsDir() bool { return e.Type == fs.ModeDir }

// IsRegular reports whether the entry is a regular file.
func (e EntryInfo) IsRegular() bool { return e.Type == 0 }

// DirOptions controls how a directory handle is opened.
type DirOptions struct {
	// Create makes the directory when it is absent. An existing entry is
	// opened and validated, never replaced.
	Create bool
	// Exclusive, with Create, fails with ErrDestinationExists instead of
	// opening an existing entry.
	Exclusive bool
	// Perm is the creation mode.
	Perm fs.FileMode
	// Trusted requires a real directory owned by root or the effective user
	// without group or other write access.
	Trusted bool
	// ReadOnly makes every mutating method fail with ErrReadOnlyAnchor.
	ReadOnly bool
}

// WalkBounds limits a read-only streaming walk.
type WalkBounds struct {
	// MaxEntries is the number of entries visited before the walk fails with
	// ErrWalkLimitExceeded.
	MaxEntries   int
	MaxDepth     int
	MaxNameBytes int
	// BatchSize is the number of names read per directory read; 0 means 128.
	BatchSize int
}

// RemoveBounds limits a streaming postorder removal. Every cap is checked
// before the work it counts.
type RemoveBounds struct {
	// MaxNodes counts successful unlink and rmdir calls, including the tree root.
	MaxNodes int
	// MaxWork counts directory reads, returned entries, stats, directory
	// opens, every unlink, rmdir and rename attempt (successful or not) and
	// rewinds.
	MaxWork int
	// MaxDetach caps renames of directories deeper than MaxDepth and the
	// number of names reported in RemoveResult.Detached.
	MaxDetach int
	// MaxDirRetries caps rewinds of a single directory after ENOTEMPTY.
	MaxDirRetries int
	// MaxDepth caps the number of open directory handles.
	MaxDepth int
	// BatchSize is the number of names read per directory read; 0 means 128.
	BatchSize int
}

// RemoveStop says why a removal call ended.
type RemoveStop string

const (
	RemoveComplete     RemoveStop = "Complete"
	RemoveNodeBudget   RemoveStop = "NodeBudget"
	RemoveWorkBudget   RemoveStop = "WorkBudget"
	RemoveDetachBudget RemoveStop = "DetachBudget"
	RemoveChurn        RemoveStop = "Churn"
	RemoveCrossDevice  RemoveStop = "CrossDevice"
	RemoveCanceled     RemoveStop = "Canceled"
	RemoveError        RemoveStop = "Error"
)

// RemoveResult reports bounded removal progress.
type RemoveResult struct {
	Complete bool
	Stop     RemoveStop
	Removed  int
	Work     int
	Retries  int
	// Detached names the .quarantine-* entries created in the anchor for
	// directories deeper than MaxDepth; at most MaxDetach names.
	Detached []string
	// LastErr is a sanitized description of the failure, at most 256 bytes.
	LastErr string
}

// XattrState classifies an extended-attribute read.
type XattrState string

const (
	XattrPresent     XattrState = "present"
	XattrAbsent      XattrState = "absent"
	XattrUnsupported XattrState = "unsupported"
	XattrUnknown     XattrState = "unknown"
)

// CopyBounds limits a streaming copy or hash.
type CopyBounds struct {
	// MaxBytes is the largest accepted size; reading stops at MaxBytes+1
	// with ErrFileLimitExceeded.
	MaxBytes int64
	// ChunkSize is the read size; 0 means 64 KiB. The context is checked
	// before each chunk read and write.
	ChunkSize int
}

// FileDigest is the size, SHA-256 and permission bits of a file.
type FileDigest struct {
	Size   int64
	SHA256 string
	Mode   fs.FileMode
}

// File is an open regular file.
type File interface {
	io.ReaderAt
	io.Closer
	Size() int64
	// Info describes the opened file.
	Info() EntryInfo
}

// Anchor is an open directory handle. Every operation is relative to it and
// never follows symbolic links. Names are single path components; relative
// paths ("rel") may contain several components separated by "/".
type Anchor interface {
	Path() string
	OpenDir(name string, opts DirOptions) (Anchor, error)
	Lstat(rel string) (EntryInfo, error)
	// Walk visits the entries beneath rel in batches, calling fn with paths
	// relative to rel. Returning fs.SkipDir for a directory skips it.
	Walk(ctx context.Context, rel string, b WalkBounds, fn func(rel string, e EntryInfo) error) error
	OpenFile(rel string) (File, error)
	ReadFile(rel string, limit int64) ([]byte, error)
	Hash(ctx context.Context, rel string, b CopyBounds) (FileDigest, error)
	// CopyFrom copies the regular file srcRel of src into a new file name
	// with mode perm, and syncs it.
	CopyFrom(ctx context.Context, src Anchor, srcRel, name string, perm fs.FileMode, b CopyBounds) (FileDigest, error)
	WriteFileExclusive(name string, data []byte, perm fs.FileMode) error
	ReplaceFileDurable(name string, data []byte, perm fs.FileMode) error
	// RenameNoReplace atomically renames oldName to newName in newDir and
	// fails with ErrDestinationExists when newName exists. newDir must be an
	// Anchor from this package or wrap one and implement Unwrap() Anchor.
	RenameNoReplace(oldName string, newDir Anchor, newName string) error
	RemoveTree(ctx context.Context, name string, b RemoveBounds) (RemoveResult, error)
	GetXattr(rel, attr string, limit int) ([]byte, XattrState, error)
	Sync() error
	AvailableBytes() (uint64, error)
	ReadOnlyMount() (bool, error)
	// Revalidate fails with ErrAnchorMoved when the path no longer leads to
	// the opened directory.
	Revalidate() error
	Close() error
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
				return nil, fmt.Errorf("failed to lookup user %s: %w", username, err)
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
