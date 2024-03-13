package fileio

import (
	"fmt"
	"os"
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

// ReadFile reads the file at the provided path
func (r *Reader) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(r.rootDir + path)
}

// CheckPathExists checks if a path exists and will return an error if either
// the file does not exist or if there is an error checking the path.
func (r *Reader) CheckPathExists(path string) error {
	return checkPathExists(r.rootDir + path)
}

func checkPathExists(path string) error {
	_, err := os.Stat(path)
	if err != nil && os.IsNotExist(err) {
		return fmt.Errorf("path does not exist: %s", path)
	}
	if err != nil {
		return fmt.Errorf("error checking path: %w", err)
	}

	return nil
}
