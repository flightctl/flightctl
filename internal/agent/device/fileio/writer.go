package fileio

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
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
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/google/renameio"
)

// writer is responsible for writing files to the device
type writer struct {
	// rootDir is the root directory for the device writer useful for testing
	rootDir string
	// Default UID of the owner of any files created or moved. -1 disables uid handling.
	uid int
	// Default GID of the owner of any files created or moved. -1 disabled gid handling.
	gid int
}

type symlinkBehavior int

const (
	symlinkSkip symlinkBehavior = iota
	symlinkError
	symlinkPreserve
	symlinkFollow
	symlinkFollowWithinRoot
	symlinkPreserveWithinRoot
)

type copyDirOptions struct {
	symlinkBehavior symlinkBehavior
	rootDir         string
}

// CopyDirOption is a functional option for CopyDir
type CopyDirOption func(*copyDirOptions)

// WithSkipSymlink skips symlinks during directory copy
func WithSkipSymlink() CopyDirOption {
	return func(opts *copyDirOptions) {
		opts.symlinkBehavior = symlinkSkip
	}
}

// WithErrorOnSymlink returns an error if a symlink is encountered during directory copy
func WithErrorOnSymlink() CopyDirOption {
	return func(opts *copyDirOptions) {
		opts.symlinkBehavior = symlinkError
	}
}

// WithPreserveSymlink preserves symlinks as-is during directory copy
func WithPreserveSymlink() CopyDirOption {
	return func(opts *copyDirOptions) {
		opts.symlinkBehavior = symlinkPreserve
	}
}

// WithFollowSymlink follows symlinks during directory copy with validation
func WithFollowSymlink() CopyDirOption {
	return func(opts *copyDirOptions) {
		opts.symlinkBehavior = symlinkFollow
	}
}

// WithFollowSymlinkWithinRoot follows symlinks only if they resolve within the source root directory
func WithFollowSymlinkWithinRoot() CopyDirOption {
	return func(opts *copyDirOptions) {
		opts.symlinkBehavior = symlinkFollowWithinRoot
	}
}

// WithPreserveSymlinkWithinRoot preserves symlinks only if they resolve within the source root directory
func WithPreserveSymlinkWithinRoot() CopyDirOption {
	return func(opts *copyDirOptions) {
		opts.symlinkBehavior = symlinkPreserveWithinRoot
	}
}

type writerOptions struct {
	uid     int
	gid     int
	rootDir string
}

type WriterOption func(*writerOptions)

func WithUID(uid uint32) WriterOption {
	return func(wo *writerOptions) {
		wo.uid = int(uid)
	}
}

func WithGID(gid uint32) WriterOption {
	return func(wo *writerOptions) {
		wo.gid = int(gid)
	}
}

func WithWriterRootDir(rootDir string) WriterOption {
	return func(wo *writerOptions) {
		wo.rootDir = rootDir
	}
}

// New creates a new writer
func NewWriter(options ...WriterOption) *writer {
	opts := writerOptions{
		// -1 means "don't change ownership" when passed through to the fchown syscall on
		// Linux.
		uid:     -1,
		gid:     -1,
		rootDir: "",
	}
	for _, o := range options {
		o(&opts)
	}
	return &writer{
		uid:     opts.uid,
		gid:     opts.gid,
		rootDir: opts.rootDir,
	}
}

func (w *writer) PathFor(filePath string) string {
	return path.Join(w.rootDir, filePath)
}

// WriteFile writes the provided data to the file at the path with the provided permissions and ownership information
func (w *writer) WriteFile(name string, data []byte, perm fs.FileMode, opts ...FileOption) error {
	fopts := &fileOptions{uid: w.uid, gid: w.gid}
	for _, opt := range opts {
		opt(fopts)
	}

	// TODO: implement createOrigFile
	// if err := createOrigFile(file.Path, file.Path); err != nil {
	// 	return err
	// }

	return writeFileAtomically(filepath.Join(w.rootDir, name), data, DefaultDirectoryPermissions, perm, fopts.uid, fopts.gid)
}

func (w *writer) RemoveFile(file string) error {
	if err := os.Remove(filepath.Join(w.rootDir, file)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file %q: %w", file, err)
	}
	return nil
}

func (w *writer) RemoveAll(path string) error {
	if err := os.RemoveAll(filepath.Join(w.rootDir, path)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove path %q: %w", path, err)
	}
	return nil
}

func (w *writer) Rename(oldpath, newpath string) error {
	return os.Rename(filepath.Join(w.rootDir, oldpath), filepath.Join(w.rootDir, newpath))
}

func (w *writer) RemoveContents(path string) error {
	fullPath := filepath.Join(w.rootDir, path)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			// nothing to do
			return nil
		}
		return fmt.Errorf("read contents of %q: %w", fullPath, err)
	}

	for _, entry := range entries {
		entryPath := filepath.Join(fullPath, entry.Name())
		if err := os.RemoveAll(entryPath); err != nil {
			return fmt.Errorf("remove entry %q: %w", entryPath, err)
		}
	}

	return nil
}

func (w *writer) CreateFile(path string, flag int, perm fs.FileMode) (*os.File, error) {
	f, err := os.OpenFile(w.PathFor(path), os.O_CREATE|flag, perm)
	if err != nil {
		return nil, fmt.Errorf("opening file %s: %w", path, err)
	}
	if err := f.Chown(w.uid, w.gid); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("setting owner of %s: %w", path, err)
	}
	return f, nil
}

func (w *writer) MkdirAll(path string, perm os.FileMode) error {
	finalPath := filepath.Join(w.rootDir, path)
	if err := os.MkdirAll(finalPath, perm); err != nil {
		return err
	}
	// Change the owner for only the last dir in the path, not every created dir.
	return os.Chown(finalPath, w.uid, w.gid)
}

func (w *writer) MkdirTemp(prefix string) (string, error) {
	baseDir := filepath.Join(w.rootDir, os.TempDir())
	if err := os.MkdirAll(baseDir, DefaultDirectoryPermissions); err != nil {
		return "", err
	}
	path, err := os.MkdirTemp(baseDir, prefix)
	if err != nil {
		return "", err
	}
	finalPath := strings.TrimPrefix(path, w.rootDir)
	if err := os.Chown(path, w.uid, w.gid); err != nil {
		return finalPath, err
	}
	return finalPath, nil
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

	// set file ownership
	if err := os.Chown(dstTarget, w.uid, w.gid); err != nil {
		return fmt.Errorf("failed to set UID and GID: %w", err)
	}

	return nil
}

func (w *writer) CopyDir(src, dst string, opts ...CopyDirOption) error {
	options := &copyDirOptions{
		symlinkBehavior: symlinkSkip,
	}
	for _, opt := range opts {
		opt(options)
	}
	fullSrc := filepath.Join(w.rootDir, src)
	fullDst := filepath.Join(w.rootDir, dst)
	absSrc, err := filepath.Abs(fullSrc)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for source: %w", err)
	}
	options.rootDir = absSrc
	return w.copyDirWithVisited(fullSrc, fullDst, options, make(map[string]bool))
}

func (w *writer) copyDirWithVisited(src, dst string, opts *copyDirOptions, visited map[string]bool) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("failed to stat source directory: %w", err)
	}

	if srcInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source is a symlink: %s", src)
	}

	if !srcInfo.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}

	absSrc, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for %s: %w", src, err)
	}

	if visited[absSrc] {
		return fmt.Errorf("circular symlink detected: %s (already being processed)", src)
	}
	visited[absSrc] = true
	defer delete(visited, absSrc)

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("failed to read source directory: %w", err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.Type()&os.ModeSymlink != 0 {
			if err := w.handleSymlink(srcPath, dstPath, opts, visited); err != nil {
				return err
			}
			continue
		}

		if entry.IsDir() {
			if err := w.copyDirWithVisited(srcPath, dstPath, opts, visited); err != nil {
				return err
			}
		} else {
			if err := w.copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

func (w *writer) handleSymlink(srcPath, dstPath string, opts *copyDirOptions, visited map[string]bool) error {
	switch opts.symlinkBehavior {
	case symlinkSkip:
		return nil
	case symlinkError:
		return fmt.Errorf("symlink encountered: %s", srcPath)
	case symlinkPreserve:
		return w.preserveSymlink(srcPath, dstPath)
	case symlinkFollow:
		return w.followSymlink(srcPath, dstPath, opts, visited)
	case symlinkFollowWithinRoot:
		return w.followSymlinkWithinRoot(srcPath, dstPath, opts, visited)
	case symlinkPreserveWithinRoot:
		return w.preserveSymlinkWithinRoot(srcPath, dstPath, opts)
	default:
		return fmt.Errorf("unknown symlink behavior: %d", opts.symlinkBehavior)
	}
}

func (w *writer) preserveSymlink(srcPath, dstPath string) error {
	linkTarget, err := os.Readlink(srcPath)
	if err != nil {
		return fmt.Errorf("failed to read symlink %s: %w", srcPath, err)
	}
	if err := os.Symlink(linkTarget, dstPath); err != nil {
		return fmt.Errorf("failed to create symlink %s: %w", dstPath, err)
	}
	return nil
}

func (w *writer) followSymlink(srcPath, dstPath string, opts *copyDirOptions, visited map[string]bool) error {
	resolved, err := filepath.EvalSymlinks(srcPath)
	if err != nil {
		return fmt.Errorf("failed to resolve symlink %s: %w", srcPath, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("failed to stat symlink target %s: %w", resolved, err)
	}

	if info.IsDir() {
		return w.copyDirWithVisited(resolved, dstPath, opts, visited)
	}
	return w.copyFile(resolved, dstPath)
}

func isWithinRoot(targetPath, rootDir string) (bool, error) {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return false, err
	}

	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return false, err
	}

	if absTarget == absRoot {
		return true, nil
	}

	// confirm that target is prefixed with the root directory
	if strings.HasPrefix(absTarget, fmt.Sprintf("%s/", absRoot)) {
		return true, nil
	}
	return false, nil
}

func (w *writer) followSymlinkWithinRoot(srcPath, dstPath string, opts *copyDirOptions, visited map[string]bool) error {
	resolved, err := filepath.EvalSymlinks(srcPath)
	if err != nil {
		return fmt.Errorf("resolve symlink %s: %w", srcPath, err)
	}

	within, err := isWithinRoot(resolved, opts.rootDir)
	if err != nil {
		return fmt.Errorf("symlink within root: %w", err)
	}
	if !within {
		return nil
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("stat symlink target %s: %w", resolved, err)
	}

	if info.IsDir() {
		return w.copyDirWithVisited(resolved, dstPath, opts, visited)
	}
	return w.copyFile(resolved, dstPath)
}

func (w *writer) preserveSymlinkWithinRoot(srcPath, dstPath string, opts *copyDirOptions) error {
	resolved, err := filepath.EvalSymlinks(srcPath)
	if err != nil {
		return fmt.Errorf("resolve symlink %s: %w", srcPath, err)
	}

	within, err := isWithinRoot(resolved, opts.rootDir)
	if err != nil {
		return fmt.Errorf("symlink within root: %w", err)
	}
	if !within {
		return nil
	}

	return w.preserveSymlink(srcPath, dstPath)
}

func (w *writer) CreateManagedFile(file v1beta1.FileSpec) (ManagedFile, error) {
	return newManagedFile(file, w)
}

func (w *writer) OverwriteAndWipe(file string) error {
	if err := w.overwriteFileWithRandomData(file); err != nil {
		return fmt.Errorf("could not overwrite file %w with random data: %w", errors.WithElement(file), err)
	}
	if err := w.RemoveFile(file); err != nil {
		return fmt.Errorf("could not remove file %w: %w", errors.WithElement(file), err)
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
		return fmt.Errorf("failed to create directory %w: %w", errors.WithElement(dir), err)
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
	if err := t.Chown(uid, gid); err != nil {
		return err
	}
	w := bufio.NewWriter(t)
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return t.CloseAtomicallyReplace()
}

// This is essentially ResolveNodeUidAndGid() from Ignition; XXX should dedupe
func getFileOwnership(file v1beta1.FileSpec) (int, int, error) {
	uid, gid := 0, 0 // default to root
	var err error
	user := file.User
	if !user.IsCurrentProcessUser() {
		uid, err = userToUID(user.String())
		if err != nil {
			return uid, gid, err
		}
	}

	group := file.Group
	if group != "" {
		gid, err = groupToGID(file.Group)
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
		return 0, 0, fmt.Errorf("%w: %w", errors.ErrFailedRetrievingCurrentUser, err)
	}
	gid, err := strconv.Atoi(currentUser.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("%w to int: %w", errors.ErrFailedConvertingGID, err)
	}
	uid, err := strconv.Atoi(currentUser.Uid)
	if err != nil {
		return 0, 0, fmt.Errorf("%w to int: %w", errors.ErrFailedConvertingUID, err)
	}
	return uid, gid, nil
}

func lookupUID(username string) (int, error) {
	osUser, err := user.Lookup(username)
	if err != nil {
		return 0, fmt.Errorf("%w for username %s: %w", errors.ErrFailedToRetrieveUserID, username, err)
	}
	uid, _ := strconv.Atoi(osUser.Uid)
	return uid, nil
}

func lookupGID(group string) (int, error) {
	osGroup, err := user.LookupGroup(group)
	if err != nil {
		return 0, fmt.Errorf("%w for group %v: %w", errors.ErrFailedToRetrieveGroupID, group, err)
	}
	gid, _ := strconv.Atoi(osGroup.Gid)
	return gid, nil
}

// DecodeContents decodes the content based on the encoding type and returns the
// decoded content as a byte slice.
func DecodeContent(content string, encoding *v1beta1.EncodingType) ([]byte,
	error) {
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

// WriteTmpFile writes the given content to a temporary file with the specified name prefix.
// It returns the path to the tmp file and a cleanup function to remove it.
func WriteTmpFile(rw ReadWriter, prefix, filename string, content []byte, perm os.FileMode) (path string, cleanup func(), err error) {
	tmpDir, err := rw.MkdirTemp(prefix)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", errors.ErrCreatingTmpDir, err)
	}

	tmpPath := filepath.Join(tmpDir, filename)
	if err := rw.WriteFile(tmpPath, content, perm); err != nil {
		_ = rw.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("writing tmp file: %w", err)
	}

	cleanup = func() {
		_ = rw.RemoveAll(tmpDir)
	}
	return tmpPath, cleanup, nil
}

// UnpackTar unpacks a tar or tar.gz file to the destination directory.
func UnpackTar(writer Writer, tarPath, destDir string) error {
	// Open the tar file for streaming
	file, err := os.Open(writer.PathFor(tarPath))
	if err != nil {
		return fmt.Errorf("opening tar file: %w", err)
	}
	defer file.Close()

	var tarReader *tar.Reader
	if strings.HasSuffix(tarPath, ".gz") || strings.HasSuffix(tarPath, ".tgz") {
		gzr, err := gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("creating gzip reader: %w", err)
		}
		defer gzr.Close()
		tarReader = tar.NewReader(gzr)
	} else {
		tarReader = tar.NewReader(file)
	}

	// Extract tar contents
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar header: %w", err)
		}

		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return fmt.Errorf("invalid file path in tar: %s", header.Name)
		}
		destPath := filepath.Join(destDir, cleanName)

		perm := header.FileInfo().Mode().Perm()

		switch header.Typeflag {
		case tar.TypeDir:
			if perm == 0 {
				perm = DefaultDirectoryPermissions
			}
			if err := writer.MkdirAll(destPath, perm); err != nil {
				return fmt.Errorf("creating directory %w: %w", errors.WithElement(destPath), err)
			}
		case tar.TypeReg:
			if perm == 0 {
				perm = DefaultFilePermissions
			}
			if err := writer.MkdirAll(filepath.Dir(destPath), DefaultDirectoryPermissions); err != nil {
				return fmt.Errorf("creating parent directory: %w", err)
			}
			destFile, err := writer.CreateFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return fmt.Errorf("creating file %w: %w", errors.WithElement(destPath), err)
			}
			// Use LimitReader to prevent decompression bombs
			limitedReader := io.LimitReader(tarReader, header.Size)
			if _, err := io.Copy(destFile, limitedReader); err != nil { // #nosec G110
				destFile.Close()
				return fmt.Errorf("writing file %w: %w", errors.WithElement(destPath), err)
			}
			destFile.Close()
		}
	}

	return nil
}
