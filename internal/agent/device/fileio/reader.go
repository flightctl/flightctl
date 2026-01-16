package fileio

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
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
