//go:build !windows

package fileio

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ccoveille/go-safecast"
	"github.com/google/renameio"
)

func setFileInfo(name string, fUid int, fGid int, fPerms os.FileMode) (bool, error) {
	fileInfo, err := os.Stat(name)
	if err != nil {
		return false, err
	}
	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("failed to retrieve UID and GID")
	}

	uid, err := safecast.ToUint32(fUid)
	if err != nil {
		return false, err
	}

	gid, err := safecast.ToUint32(fGid)
	if err != nil {
		return false, err
	}

	// compare file ownership
	if stat.Uid != uid || stat.Gid != gid {
		return false, nil
	}

	// compare file permissions
	if fileInfo.Mode().Perm() != fPerms.Perm() {
		return false, nil
	}

	return true, nil
}

func setChown(srcFileInfo os.FileInfo, dstTarget string) error {
	stat, ok := srcFileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("failed to retrieve UID and GID")
	}

	// set file ownership
	if err := os.Chown(dstTarget, int(stat.Uid), int(stat.Gid)); err != nil {
		return fmt.Errorf("failed to set UID and GID: %w", err)
	}

	return nil
}

// writeFileAtomically uses the renameio package to provide atomic file writing, we can't use renameio.WriteFile
// directly since we need to 1) Chown 2) go through a buffer since files provided can be big
func writeFileAtomically(fpath string, b []byte, dirMode, fileMode os.FileMode, uid, gid int) error {
	dir := filepath.Dir(fpath)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}
	t, err := renameio.TempFile(dir, fpath)
	if err != nil {
		return err
	}
	defer func() {
		_ = t.Cleanup()
	}()
	// Set permissions before writing data, in case the data is sensitive.
	if err := t.Chmod(fileMode); err != nil {
		return err
	}
	w := bufio.NewWriter(t)
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if uid != -1 && gid != -1 {
		if err := t.Chown(uid, gid); err != nil {
			return err
		}
	}
	return t.CloseAtomicallyReplace()
}

func GetDirUsage(dir string) (uint64, uint64, uint64, uint64, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(dir, &stat)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	bsize, err := safecast.ToUint64(stat.Bsize)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	return stat.Files, stat.Blocks * bsize, stat.Bavail * bsize, (stat.Blocks - stat.Bfree) * bsize, nil
}
