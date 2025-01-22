//go:build windows

package fileio

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func setFileInfo(name string, fUid int, fGid int, fPerms os.FileMode) (bool, error) {
	return true, nil
}

func setChown(srcFileInfo os.FileInfo, dstTarget string) error {
	return nil
}

// writeFileAtomically uses the renameio package to provide atomic file writing, we can't use renameio.WriteFile
// directly since we need to 1) Chown 2) go through a buffer since files provided can be big
func writeFileAtomically(fpath string, b []byte, dirMode, fileMode os.FileMode, uid, gid int) error {
	dir := filepath.Dir(fpath)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}
	tempFile := fpath + ".tmp"
	// Create and write to the temp file
	f, err := os.OpenFile(tempFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return fmt.Errorf("failed to create temp file %q: %w", tempFile, err)
	}
	defer os.Remove(tempFile) // Ensure cleanup on failure
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to write to temp file %q: %w", tempFile, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close temp file %q: %w", tempFile, err)
	}

	// Replace the target file atomically
	if err := os.Rename(tempFile, fpath); err != nil {
		return fmt.Errorf("failed to replace target file %q with temp file %q: %w", fpath, tempFile, err)
	}

	return nil
}

func GetDirUsage(dir string) (uint64, uint64, uint64, uint64, error) {
	// Convert the directory path to an absolute path
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	// Use Windows API to get disk usage
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	err = windows.GetDiskFreeSpaceEx(
		windows.StringToUTF16Ptr(absDir),
		&freeBytesAvailable,
		&totalNumberOfBytes,
		&totalNumberOfFreeBytes,
	)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	// Calculate used space
	usedBytes := totalNumberOfBytes - totalNumberOfFreeBytes

	return 0, totalNumberOfBytes, totalNumberOfFreeBytes, usedBytes, nil
}
