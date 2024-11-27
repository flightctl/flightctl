package fileio

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
)

// Reader is a struct for reading files from the device
type reader struct {
	// rootDir is the root directory for the device writer useful for testing
	rootDir string
}

// New creates a new writer
func NewReader() *reader {
	return &reader{}
}

// SetRootdir sets the root directory for the reader, useful for testing
func (r *reader) SetRootdir(path string) {
	r.rootDir = path
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

// PathExists checks if a path exists and is readable and returns a boolean
// indicating existence, and an error only if there was a problem checking the
// path.
func (r *reader) PathExists(path string) (bool, error) {
	return checkPathExists(r.PathFor(path))
}

func checkPathExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("error checking path: %w", err)
	}

	if info.IsDir() {
		dir, err := os.Open(path)
		if err != nil {
			return false, fmt.Errorf("directory exists but %w: %w", errors.ErrReadingPath, err)
		}
		defer dir.Close()
		// read a single entry from the directory to confirm readability
		_, err = dir.Readdirnames(1)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// readable but empty
				return true, nil
			}
			return false, fmt.Errorf("directory exists but %w: %w", errors.ErrReadingPath, err)
		}
	} else {
		file, err := os.Open(path)
		if err != nil {
			return false, fmt.Errorf("file exists but %w: %w", errors.ErrReadingPath, err)
		}
		defer file.Close()
		// read a single byte from the file to ensure permissions are correct
		buffer := make([]byte, 1)
		if _, err := file.Read(buffer); err != nil {
			return false, fmt.Errorf("file exists but %w: %w", errors.ErrReadingPath, err)
		}
	}

	return true, nil
}
