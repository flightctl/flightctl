package state

import (
	"fmt"
	"io"
	"os"
)

// FileStorage implements the Storage interface, using a local file for persistence.
type FileStorage struct {
	filePath string
}

// NewFileStorage creates a new FileStorage with the given file path.
func NewFileStorage(path string) *FileStorage {
	return &FileStorage{
		filePath: path,
	}
}

// StoreState reads data from the provided reader and writes it to the file.
func (f *FileStorage) StoreState(r io.Reader) error {
	file, err := os.Create(f.filePath)
	if err != nil {
		return fmt.Errorf("failed to create state file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, r); err != nil {
		return fmt.Errorf("failed to write state to file: %w", err)
	}

	return nil
}

// LoadState reads data from the file and writes it to the provided writer.
func (f *FileStorage) LoadState(w io.Writer) error {
	file, err := os.Open(f.filePath)
	if err != nil {
		// If file doesn't exist, return nil so loading can continue with empty state.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to open state file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(w, file); err != nil {
		return fmt.Errorf("failed to read state from file: %w", err)
	}

	return nil
}
