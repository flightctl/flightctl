package fileio

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"syscall"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/google/renameio"
	"github.com/vincent-petithory/dataurl"
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

	var dstTarget string
	dstInfo, err := os.Stat(dst)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to stat destination: %w", err)
		}
		dstTarget = dst
	} else {
		if dstInfo.IsDir() {
			// destination is a directory, append the source file's base name
			dstTarget = filepath.Join(dst, filepath.Base(src))
		}
	}

	dstFile, err := os.Create(dstTarget)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
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

func (w *writer) CreateManagedFile(file ign3types.File) (ManagedFile, error) {
	return newManagedFile(file, w)
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

// This is essentially ResolveNodeUidAndGid() from Ignition; XXX should dedupe
func getFileOwnership(file ign3types.File) (int, int, error) {
	uid, gid := 0, 0 // default to root
	var err error    // create default error var
	if file.User.ID != nil {
		uid = *file.User.ID
	} else if file.User.Name != nil && *file.User.Name != "" {
		uid, err = lookupUID(*file.User.Name)
		if err != nil {
			return uid, gid, err
		}
	}

	if file.Group.ID != nil {
		gid = *file.Group.ID
	} else if file.Group.Name != nil && *file.Group.Name != "" {
		gid, err = lookupGID(*file.Group.Name)
		if err != nil {
			return uid, gid, err
		}
	}
	return uid, gid, nil
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

func decodeIgnitionFileContents(source, compression *string) ([]byte, error) {
	var contentsBytes []byte

	// To allow writing of "empty" files we'll allow source to be nil
	if source != nil {
		source, err := dataurl.DecodeString(*source)
		if err != nil {
			return []byte{}, fmt.Errorf("could not decode file content string: %w", err)
		}
		if compression != nil {
			switch *compression {
			case "":
				contentsBytes = source.Data
			case "gzip":
				reader, err := gzip.NewReader(bytes.NewReader(source.Data))
				if err != nil {
					return []byte{}, fmt.Errorf("could not create gzip reader: %w", err)
				}
				defer reader.Close()
				contentsBytes, err = io.ReadAll(reader)
				if err != nil {
					return []byte{}, fmt.Errorf("failed decompressing: %w", err)
				}
			default:
				return []byte{}, fmt.Errorf("unsupported compression type %q", *compression)
			}
		} else {
			contentsBytes = source.Data
		}
	}
	return contentsBytes, nil
}
