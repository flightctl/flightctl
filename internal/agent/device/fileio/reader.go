package fileio

import (
	"fmt"
	"os"
	"path"
)

// Reader is a struct for reading files from the device
type FileReader struct {
	// rootDir is the root directory for the device writer useful for testing
	rootDir string
}

// New creates a new writer
func NewReader() *FileReader {
	return &FileReader{}
}

// SetRootdir sets the root directory for the reader, useful for testing
func (r *FileReader) SetRootdir(path string) {
	r.rootDir = path
}

// PathFor returns the full path for the provided file, useful for using functions
// and libraries that don't work with the fileio.Reader
func (r *FileReader) PathFor(filePath string) string {
	return path.Join(r.rootDir, filePath)
}

// ReadFile reads the file at the provided path
func (r *FileReader) ReadFile(filePath string) ([]byte, error) {
	return os.ReadFile(r.PathFor(filePath))
}

// FileExists checks if a path exists and returns a boolean indicating existence,
// and an error only if there was a problem checking the path.
func (r *FileReader) FileExists(filePath string) (bool, error) {
	return checkPathExists(r.PathFor(filePath))
}

func checkPathExists(filePath string) (bool, error) {
	_, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("error checking path: %w", err)
	}
	return true, nil
}
