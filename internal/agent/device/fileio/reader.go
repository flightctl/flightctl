package fileio

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"golang.org/x/sys/unix"
)

const (
	entryTypeFile = "file"
	entryTypeDir  = "dir"
)

// Reader is a struct for reading files from the device
type reader struct {
	// rootDir is the root directory for the device writer useful for testing
	rootDir string
}

type readerOptions struct {
	rootDir string
}

type ReaderOption func(*readerOptions)

func WithReaderRootDir(rootDir string) ReaderOption {
	return func(wo *readerOptions) {
		wo.rootDir = rootDir
	}
}

// New creates a new writer
func NewReader(options ...ReaderOption) *reader {
	opts := readerOptions{
		rootDir: "",
	}
	for _, o := range options {
		o(&opts)
	}
	return &reader{
		rootDir: opts.rootDir,
	}
}

// PathFor returns the full path for the provided file, useful for using functions
// and libraries that don't work with the fileio.Reader
func (r *reader) PathFor(filePath string) string {
	return path.Join(r.rootDir, filePath)
}

// ReadFile reads the file at the provided path
func (r *reader) ReadFile(filePath string) ([]byte, error) {
	return os.ReadFile(r.PathFor(filePath))
}

// ReadDir reads the directory at the provided path and returns a slice of fs.DirEntry. If the directory
// does not exist, it returns an empty slice and no error.
func (r *reader) ReadDir(dirPath string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(r.PathFor(dirPath))
	if err != nil {
		if os.IsNotExist(err) {
			return []fs.DirEntry{}, nil
		}
		return nil, err
	}
	return entries, nil
}

// PathExistsOption represents options for PathExists function
type PathExistsOption func(*pathExistsOptions)

type pathExistsOptions struct {
	skipContentCheck bool
}

// WithSkipContentCheck configures PathExists to skip content verification
// and only check if the path can be opened
func WithSkipContentCheck() PathExistsOption {
	return func(opts *pathExistsOptions) {
		opts.skipContentCheck = true
	}
}

// PathExists checks if a path exists and is readable and returns a boolean
// indicating existence, and an error only if there was a problem checking the
// path.
func (r *reader) PathExists(path string, opts ...PathExistsOption) (bool, error) {
	options := &pathExistsOptions{}
	for _, opt := range opts {
		opt(options)
	}
	return checkPathExists(r.PathFor(path), options)
}

func checkPathExists(path string, options *pathExistsOptions) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("error checking path: %w", err)
	}
	pathType := entryTypeFile
	if info.IsDir() {
		pathType = entryTypeDir
	}

	// Open the file/directory once
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("%s exists but %w: %w", pathType, errors.ErrReadingPath, err)
	}
	defer file.Close()

	// If we only need to check if it can be opened, we're done
	if options.skipContentCheck {
		return true, nil
	}

	if err = validateContents(file, info.IsDir()); err != nil {
		return false, fmt.Errorf("%s exists but %w", pathType, err)
	}
	return true, nil
}

func validateContents(file *os.File, isDir bool) error {
	if isDir {
		// read a single entry from the directory to confirm readability
		_, err := file.Readdirnames(1)
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: %w", errors.ErrReadingPath, err)
		}
		return nil
	}

	// read a single byte from the file to ensure permissions are correct
	buffer := make([]byte, 1)
	_, err := file.Read(buffer)
	if err != nil {
		return fmt.Errorf("%w, %w", errors.ErrReadingPath, err)
	}
	return nil
}

func (r *reader) ReadFileLimited(filePath string, limit int64) ([]byte, error) {
	if limit <= 0 || limit == int64(^uint64(0)>>1) {
		return nil, fmt.Errorf("invalid file read limit")
	}
	f, err := os.Open(r.PathFor(filePath))
	if err != nil {
		return nil, err
	}
	// This file is read-only: read errors are returned below, and closing it
	// cannot lose buffered writes or change the bytes already read.
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds read limit")
	}
	return data, nil
}

// ReadFileInRoot opens root normally, then resolves each component of relPath
// relative to its parent directory with O_NOFOLLOW. The final entry is checked
// before it is opened so that device nodes and FIFOs are never opened.
func (r *reader) ReadFileInRoot(root, relPath string, limit int64) ([]byte, error) {
	if limit <= 0 || limit == int64(^uint64(0)>>1) {
		return nil, fmt.Errorf("invalid file read limit")
	}
	if relPath == "" || path.IsAbs(relPath) || path.Clean(relPath) != relPath || relPath == "." ||
		relPath == ".." || strings.HasPrefix(relPath, "../") || strings.ContainsRune(relPath, 0) {
		return nil, fmt.Errorf("invalid path %q beneath root", relPath)
	}
	dir, err := unix.Open(r.PathFor(root), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	components := strings.Split(relPath, "/")
	dir, err = openComponentsNoFollow(dir, components[:len(components)-1])
	if err != nil {
		return nil, err
	}
	// The descriptor is read-only; closing it after traversal cannot lose data.
	defer func() { _ = unix.Close(dir) }()

	name := components[len(components)-1]
	var before unix.Stat_t
	if err := unix.Fstatat(dir, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("stat %q: %w", relPath, err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%q is not a regular file", relPath)
	}
	pathFd, err := unix.Openat(dir, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %q without following links: %w", relPath, err)
	}
	var pinned unix.Stat_t
	if err := unix.Fstat(pathFd, &pinned); err != nil {
		_ = unix.Close(pathFd)
		return nil, fmt.Errorf("stat %q: %w", relPath, err)
	}
	if pinned.Mode&unix.S_IFMT != unix.S_IFREG || pinned.Dev != before.Dev || pinned.Ino != before.Ino {
		_ = unix.Close(pathFd)
		return nil, fmt.Errorf("%q is not a regular file", relPath)
	}
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(pathFd), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	// The pinned descriptor is no longer needed once the read descriptor is open.
	_ = unix.Close(pathFd)
	if err != nil {
		return nil, fmt.Errorf("reopen %q: %w", relPath, err)
	}
	f := os.NewFile(uintptr(fd), relPath)
	// The file is read-only and fully consumed before return; close cannot lose writes.
	defer func() { _ = f.Close() }()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, fmt.Errorf("stat %q: %w", relPath, err)
	}
	if opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Dev != before.Dev || opened.Ino != before.Ino {
		return nil, fmt.Errorf("%q is not a regular file", relPath)
	}
	if opened.Size > limit {
		return nil, fmt.Errorf("%q exceeds read limit", relPath)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%q exceeds read limit", relPath)
	}
	return data, nil
}

// openComponentsNoFollow opens each directory component beneath dir without
// following symbolic links. It takes ownership of dir and returns the handle
// of the last component.
func openComponentsNoFollow(dir int, components []string) (int, error) {
	for _, component := range components {
		next, err := unix.Openat(dir, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		// The parent descriptor is no longer needed after opening the child;
		// a close failure cannot affect the already-open child.
		_ = unix.Close(dir)
		if err != nil {
			return -1, fmt.Errorf("open directory %q without following links: %w", component, err)
		}
		dir = next
	}
	return dir, nil
}
