package fileio

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/google/renameio"
	"github.com/samber/lo"
	"k8s.io/klog/v2"
)

// writer is responsible for writing files to the device
type writer struct {
	// rootDir is the root directory for the device writer useful for testing
	rootDir string
}

// New creates a new writer
func NewWriter() *writer {
	return &writer{}
}

// SetRootdir sets the root directory for the writer, useful for testing
func (w *writer) SetRootdir(path string) {
	w.rootDir = path
}

func (w *writer) PathFor(filePath string) string {
	return path.Join(w.rootDir, filePath)
}

// WriteFile writes the provided data to the file at the path with the provided permissions and ownership information
func (w *writer) WriteFile(name string, data []byte, perm fs.FileMode, opts ...FileOption) error {
	fopts := &fileOptions{}
	for _, opt := range opts {
		opt(fopts)
	}

	var uid, gid int
	// if rootDir is set use the default UID and GID
	if w.rootDir != "" {
		defaultUID, defaultGID, err := getUserIdentity()
		if err != nil {
			return err
		}
		uid = defaultUID
		gid = defaultGID
	} else {
		uid = fopts.uid
		gid = fopts.gid
	}

	// TODO: implement createOrigFile
	// if err := createOrigFile(file.Path, file.Path); err != nil {
	// 	return err
	// }

	return writeFileAtomically(filepath.Join(w.rootDir, name), data, DefaultDirectoryPermissions, perm, uid, gid)
}

func (w *writer) RemoveFile(file string) error {
	if err := os.Remove(filepath.Join(w.rootDir, file)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove file %q: %w", file, err)
	}
	return nil
}

func (w *writer) RemoveAll(path string) error {
	if err := os.RemoveAll(filepath.Join(w.rootDir, path)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove path %q: %w", path, err)
	}
	return nil
}

func (w *writer) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(filepath.Join(w.rootDir, path), perm)
}

func (w *writer) CopyFile(src, dst string) error {
	return w.copyFile(filepath.Join(w.rootDir, src), filepath.Join(w.rootDir, dst))
}

func (w *writer) copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	dstTarget := dst
	dstInfo, err := os.Stat(dst)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to stat destination: %w", err)
		}
	} else {
		if dstInfo.IsDir() {
			// destination is a directory, append the source file's base name
			dstTarget = filepath.Join(dst, filepath.Base(src))
		}
	}

	dstFile, err := os.Create(dstTarget)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", dstTarget, err)
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return fmt.Errorf("failed to copy file content: %w", err)
	}

	// read file info metadata from src
	srcFileInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to get source file info: %w", err)
	}

	// set file permissions
	if err := os.Chmod(dstTarget, srcFileInfo.Mode()); err != nil {
		return fmt.Errorf("failed to set file permissions: %w", err)
	}

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

func (w *writer) CreateManagedFile(file v1alpha1.FileSpec) (ManagedFile, error) {
	return newManagedFile(file, w)
}

func (w *writer) OverwriteAndWipe(file string) error {
	if err := w.overwriteFileWithRandomData(file); err != nil {
		return fmt.Errorf("could not overwrite file %s with random data: %w", file, err)
	}
	if err := w.RemoveFile(file); err != nil {
		return fmt.Errorf("could not remove file %s: %w", file, err)
	}
	return nil
}

func (w *writer) overwriteFileWithRandomData(file string) error {
	f, err := os.OpenFile(filepath.Join(w.rootDir, file), os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	fileInfo, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}
	fileSize := fileInfo.Size()

	randomData := make([]byte, fileSize)
	if _, err := rand.Read(randomData); err != nil {
		return fmt.Errorf("failed to generate random data: %w", err)
	}

	if _, err := f.WriteAt(randomData, 0); err != nil {
		return fmt.Errorf("failed to write random data: %w", err)
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
	// Set permissions before writing data to prevent potential exposure of sensitive data
	// through temporarily incorrect permissions.
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

// This is essentially ResolveNodeUidAndGid() from Ignition; XXX should dedupe
func getFileOwnership(file v1alpha1.FileSpec) (int, int, error) {
	uid, gid := 0, 0 // default to root
	var err error
	user := lo.FromPtr(file.User)
	if user != "" {
		uid, err = userToUID(user)
		if err != nil {
			return uid, gid, err
		}
	}

	group := lo.FromPtr(file.Group)
	if group != "" {
		gid, err = groupToGID(*file.Group)
		if err != nil {
			return uid, gid, err
		}
	}
	return uid, gid, nil
}

func userToUID(user string) (int, error) {
	userID, err := strconv.Atoi(user)
	if err != nil {
		uid, err := lookupUID(user)
		if err != nil {
			return 0, fmt.Errorf("failed to convert user to UID: %w", err)
		}
		return uid, nil
	}
	return userID, nil
}

func groupToGID(group string) (int, error) {
	groupID, err := strconv.Atoi(group)
	if err != nil {
		gid, err := lookupGID(group)
		if err != nil {
			return 0, fmt.Errorf("failed to convert group to GID: %w", err)
		}
		return gid, nil
	}
	return groupID, nil
}

func getUserIdentity() (int, int, error) {
	currentUser, err := user.Current()
	if err != nil {
		return 0, 0, fmt.Errorf("failed retrieving current user: %w", err)
	}
	gid, err := strconv.Atoi(currentUser.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("failed converting GID to int: %w", err)
	}
	uid, err := strconv.Atoi(currentUser.Uid)
	if err != nil {
		return 0, 0, fmt.Errorf("failed converting UID to int: %w", err)
	}
	return uid, gid, nil
}

func lookupUID(username string) (int, error) {
	osUser, err := user.Lookup(username)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve UserID for username: %s", username)
	}
	klog.V(2).Infof("Retrieved UserId: %s for username: %s", osUser.Uid, username)
	uid, _ := strconv.Atoi(osUser.Uid)
	return uid, nil
}

func lookupGID(group string) (int, error) {
	osGroup, err := user.LookupGroup(group)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve GroupID for group: %v", group)
	}
	klog.V(2).Infof("Retrieved GroupID: %s for group: %s", osGroup.Gid, group)
	gid, _ := strconv.Atoi(osGroup.Gid)
	return gid, nil
}

func decodeFileContents(content string, encoding *v1alpha1.FileSpecContentEncoding) ([]byte, error) {
	if encoding == nil || *encoding == "plain" {
		return []byte(content), nil
	}

	switch *encoding {
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64 content: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("unsupported content encoding: %q", *encoding)
	}
}
