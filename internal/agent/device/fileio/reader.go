package fileio

import (
	"fmt"
	"os"
	"path"
)

// Reader is a struct for reading files from the device
type Reader struct {
	// rootDir is the root directory for the device writer useful for testing
	rootDir string
}

// New creates a new writer
func NewReader() *Reader {
	return &Reader{}
}

// SetRootdir sets the root directory for the reader, useful for testing
func (r *Reader) SetRootdir(path string) {
	r.rootDir = path
}

// PathFor returns the full path for the provided file, useful for using functions
// and libraries that don't work with the fileio.Reader
func (r *Reader) PathFor(filePath string) string {
	return path.Join(r.rootDir, filePath)
}

// ReadFile reads the file at the provided path
func (r *Reader) ReadFile(filePath string) ([]byte, error) {
	return os.ReadFile(r.PathFor(filePath))
}

// CheckPathExists checks if a path exists and will return an error if either
// the file does not exist or if there is an error checking the path.
func (r *Reader) CheckPathExists(filePath string) error {
	return checkPathExists(r.PathFor(filePath))
}

func checkPathExists(filePath string) error {
	_, err := os.Stat(filePath)
	if err != nil && os.IsNotExist(err) {
		return fmt.Errorf("path does not exist: %s", filePath)
	}
	if err != nil {
		return fmt.Errorf("error checking path: %w", err)
	}

	return nil
}
