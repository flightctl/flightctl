package state

import (
	"fmt"
	"io"
	"os"
)

// FileStorage implements StateStorageProvider using a local file for certificate state persistence.
// It provides a simple way to persist certificate metadata and state across agent restarts
// by storing JSON data in a local file.
type FileStorage struct {
	filePath string // Path to the file where certificate state is stored
}

// NewFileStorage creates a new FileStorage with the given file path.
func NewFileStorage(path string) *FileStorage {
	return &FileStorage{
		filePath: path,
	}
}

// StoreState reads data from the provided reader and writes it to the file.
// This implements the StateStorageProvider interface for persisting certificate state.
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
// This implements the StateStorageProvider interface for loading certificate state.
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
