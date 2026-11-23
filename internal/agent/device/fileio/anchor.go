package fileio

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"golang.org/x/sys/unix"
)

const (
	defaultBatchSize     = 128
	defaultChunkSize     = 64 << 10
	maxNameBytes         = 255
	maxLastErrBytes      = 256
	quarantinePrefix     = ".quarantine-"
	capabilityXattr      = "security.capability"
	capabilityXattrLimit = 64
	tempNameAttempts     = 8
)

// sysOps holds the kernel calls used by anchors so that tests in this package
// can inject failures at the operating-system boundary.
type sysOps struct {
	fstat     func(fd int, stat *unix.Stat_t) error
	unlinkat  func(dirfd int, path string, flags int) error
	renameat2 func(olddirfd int, oldpath string, newdirfd int, newpath string, flags uint) error
	fstatat   func(dirfd int, path string, stat *unix.Stat_t, flags int) error
	fgetxattr func(fd int, attr string, dest []byte) (int, error)
}

var defaultSysOps = &sysOps{
	fstat:     unix.Fstat,
	unlinkat:  unix.Unlinkat,
	renameat2: unix.Renameat2,
	fstatat:   unix.Fstatat,
	fgetxattr: unix.Fgetxattr,
}

type chainLink struct {
	name string
	dev  uint64
	ino  uint64
}

// anchor is a directory handle. It remembers how it was reached (start
// directory plus the links opened below it) so that Revalidate can detect a
// replaced path component.
type anchor struct {
	fd       int
	path     string
	start    string
	startDev uint64
	startIno uint64
	chain    []chainLink
	dev      uint64
	ino      uint64
	readOnly bool
	trusted  bool
	euid     int
	sys      *sysOps
}

func (rw *readWriter) OpenAnchor(path string, opts DirOptions) (Anchor, error) {
	return openAnchorWithSys(rw, path, opts, defaultSysOps)
}

func openAnchorWithSys(rw ReadWriter, p string, opts DirOptions, sys *sysOps) (Anchor, error) {
	if opts.Create || opts.Exclusive {
		return nil, fmt.Errorf("open anchor %w: creating the anchor directory is not supported", errors.WithElement(p))
	}
	if p == "" || strings.ContainsRune(p, 0) {
		return nil, fmt.Errorf("open anchor %w: %w", errors.WithElement(p), errors.ErrInvalidPath)
	}
	logical := path.Clean("/" + p)
	a := &anchor{path: logical, readOnly: opts.ReadOnly, trusted: opts.Trusted, euid: os.Geteuid(), sys: sys}
	if !opts.Trusted {
		start := rw.PathFor(logical)
		fd, err := unix.Open(start, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("open anchor %w: %w", errors.WithElement(p), err)
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("stat anchor %w: %w", errors.WithElement(p), err)
		}
		a.fd, a.start = fd, start
		a.startDev, a.startIno = uint64(st.Dev), uint64(st.Ino) //nolint:unconvert // Stat_t widths differ by architecture
		a.dev, a.ino = a.startDev, a.startIno
		return a, nil
	}

	a.start = rw.PathFor("/")
	fd, err := unix.Open(a.start, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open anchor root %w: %w", errors.WithElement(a.start), err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("stat anchor root %w: %w", errors.WithElement(a.start), err)
	}
	if err := a.checkTrusted(&st, "/"); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	a.startDev, a.startIno = uint64(st.Dev), uint64(st.Ino) //nolint:unconvert // Stat_t widths differ by architecture
	a.dev, a.ino = a.startDev, a.startIno
	if logical != "/" {
		for _, name := range strings.Split(strings.TrimPrefix(logical, "/"), "/") {
			next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			_ = unix.Close(fd)
			if err != nil {
				return nil, a.openDirError(logical, name, true, err)
			}
			fd = next
			if err := unix.Fstat(fd, &st); err != nil {
				_ = unix.Close(fd)
				return nil, fmt.Errorf("stat anchor %w: %w", errors.WithElement(logical), err)
			}
			if err := a.checkTrusted(&st, logical); err != nil {
				_ = unix.Close(fd)
				return nil, err
			}
			a.chain = append(a.chain, chainLink{name: name, dev: uint64(st.Dev), ino: uint64(st.Ino)}) //nolint:unconvert // Stat_t widths differ by architecture
		}
	}
	a.fd = fd
	a.dev, a.ino = uint64(st.Dev), uint64(st.Ino) //nolint:unconvert // Stat_t widths differ by architecture
	return a, nil
}

func (a *anchor) checkTrusted(st *unix.Stat_t, display string) error {
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("%w %w: not a directory", errors.ErrUntrustedDirectory, errors.WithElement(display))
	case st.Uid != 0 && int64(st.Uid) != int64(a.euid):
		return fmt.Errorf("%w %w: owned by uid %d", errors.ErrUntrustedDirectory, errors.WithElement(display), st.Uid)
	case st.Mode&0o022 != 0:
		return fmt.Errorf("%w %w: writable by group or others", errors.ErrUntrustedDirectory, errors.WithElement(display))
	}
	return nil
}

func (a *anchor) openDirError(display, name string, trusted bool, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		if trusted {
			return fmt.Errorf("%w %w: %q is not a real directory: %w", errors.ErrUntrustedDirectory, errors.WithElement(display), name, err)
		}
		return fmt.Errorf("%w %w: %q is not a real directory: %w", errors.ErrUnsafeFileType, errors.WithElement(display), name, err)
	}
	return fmt.Errorf("open directory %w: %w", errors.WithElement(path.Join(display, name)), err)
}

func validName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > maxNameBytes ||
		strings.ContainsRune(name, '/') || strings.ContainsRune(name, 0) {
		return fmt.Errorf("%w: %q is not a single path component", errors.ErrInvalidPath, name)
	}
	return nil
}

func validRel(rel string) error {
	if rel == "." {
		return nil
	}
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." ||
		strings.HasPrefix(rel, "../") || strings.ContainsRune(rel, 0) {
		return fmt.Errorf("%w: %q is not a clean relative path", errors.ErrInvalidPath, rel)
	}
	return nil
}

func (a *anchor) Path() string { return a.path }

func (a *anchor) child(fd int, name string, st *unix.Stat_t, readOnly, trusted bool) *anchor {
	chain := make([]chainLink, len(a.chain), len(a.chain)+1)
	copy(chain, a.chain)
	link := chainLink{name: name, dev: uint64(st.Dev), ino: uint64(st.Ino)} //nolint:unconvert // Stat_t widths differ by architecture
	return &anchor{
		fd:       fd,
		path:     path.Join(a.path, name),
		start:    a.start,
		startDev: a.startDev,
		startIno: a.startIno,
		chain:    append(chain, link),
		dev:      link.dev,
		ino:      link.ino,
		readOnly: a.readOnly || readOnly,
		trusted:  a.trusted && trusted,
		euid:     a.euid,
		sys:      a.sys,
	}
}

func (a *anchor) OpenDir(name string, opts DirOptions) (Anchor, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	created := false
	if opts.Create {
		if a.readOnly {
			return nil, fmt.Errorf("create directory %w: %w", errors.WithElement(path.Join(a.path, name)), errors.ErrReadOnlyAnchor)
		}
		perm := opts.Perm.Perm()
		if perm == 0 {
			perm = 0o700
		}
		err := unix.Mkdirat(a.fd, name, uint32(perm))
		switch {
		case err == nil:
			created = true
		case errors.Is(err, unix.EEXIST):
			if opts.Exclusive {
				return nil, fmt.Errorf("create directory %w: %w: %w", errors.WithElement(path.Join(a.path, name)), errors.ErrDestinationExists, fs.ErrExist)
			}
		default:
			return nil, fmt.Errorf("create directory %w: %w", errors.WithElement(path.Join(a.path, name)), err)
		}
	}
	fd, err := unix.Openat(a.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, a.openDirError(a.path, name, opts.Trusted, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("stat directory %w: %w", errors.WithElement(path.Join(a.path, name)), err)
	}
	if created {
		perm := opts.Perm.Perm()
		if perm == 0 {
			perm = 0o700
		}
		if err := unix.Fchmod(fd, uint32(perm)); err != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("set directory mode %w: %w", errors.WithElement(path.Join(a.path, name)), err)
		}
		if err := unix.Fstat(fd, &st); err != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("stat directory %w: %w", errors.WithElement(path.Join(a.path, name)), err)
		}
	}
	if opts.Create {
		if err := unix.Fsync(a.fd); err != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("sync directory %w: %w", errors.WithElement(a.path), err)
		}
	}
	if opts.Trusted {
		if err := a.checkTrusted(&st, path.Join(a.path, name)); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
	}
	return a.child(fd, name, &st, opts.ReadOnly, opts.Trusted), nil
}

// openParent resolves every component of rel except the last one without
// following links. The returned release function closes any opened handle.
func (a *anchor) openParent(rel string) (int, string, func(), error) {
	if err := validRel(rel); err != nil {
		return -1, "", nil, err
	}
	parts := strings.Split(rel, "/")
	base := parts[len(parts)-1]
	if len(parts) == 1 {
		return a.fd, base, func() {}, nil
	}
	start, err := unix.Openat(a.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", nil, fmt.Errorf("open directory %w: %w", errors.WithElement(a.path), err)
	}
	dir, err := openComponentsNoFollow(start, parts[:len(parts)-1])
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return -1, "", nil, fmt.Errorf("%w %w: %w", errors.ErrUnsafeFileType, errors.WithElement(path.Join(a.path, rel)), err)
		}
		return -1, "", nil, fmt.Errorf("resolve %w: %w", errors.WithElement(path.Join(a.path, rel)), err)
	}
	return dir, base, func() { _ = unix.Close(dir) }, nil
}

func entryInfo(name string, st *unix.Stat_t) EntryInfo {
	var typ fs.FileMode
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFDIR:
		typ = fs.ModeDir
	case unix.S_IFLNK:
		typ = fs.ModeSymlink
	case unix.S_IFIFO:
		typ = fs.ModeNamedPipe
	case unix.S_IFSOCK:
		typ = fs.ModeSocket
	case unix.S_IFCHR:
		typ = fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFBLK:
		typ = fs.ModeDevice
	default:
		typ = fs.ModeIrregular
	}
	perm := fs.FileMode(st.Mode & 0o777)
	if st.Mode&unix.S_ISUID != 0 {
		perm |= fs.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		perm |= fs.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		perm |= fs.ModeSticky
	}
	return EntryInfo{
		Name:  name,
		Type:  typ,
		Perm:  perm,
		Nlink: uint64(st.Nlink), //nolint:unconvert // Stat_t widths differ by architecture
		Size:  int64(st.Size),   //nolint:unconvert // Stat_t widths differ by architecture
		UID:   st.Uid,
		Dev:   uint64(st.Dev), //nolint:unconvert // Stat_t widths differ by architecture
		Ino:   uint64(st.Ino), //nolint:unconvert // Stat_t widths differ by architecture
	}
}

func (a *anchor) Lstat(rel string) (EntryInfo, error) {
	var st unix.Stat_t
	if rel == "." {
		if err := unix.Fstat(a.fd, &st); err != nil {
			return EntryInfo{}, fmt.Errorf("stat %w: %w", errors.WithElement(a.path), err)
		}
		return entryInfo(".", &st), nil
	}
	dir, base, release, err := a.openParent(rel)
	if err != nil {
		return EntryInfo{}, err
	}
	defer release()
	if err := a.sys.fstatat(dir, base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return EntryInfo{}, fmt.Errorf("stat %w: %w", errors.WithElement(path.Join(a.path, rel)), err)
	}
	return entryInfo(base, &st), nil
}

func (a *anchor) openDirRel(rel string) (*os.File, *unix.Stat_t, error) {
	var fd int
	if rel == "." {
		var err error
		fd, err = unix.Openat(a.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("open directory %w: %w", errors.WithElement(a.path), err)
		}
	} else {
		dir, base, release, err := a.openParent(rel)
		if err != nil {
			return nil, nil, err
		}
		fd, err = unix.Openat(dir, base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		release()
		if err != nil {
			return nil, nil, a.openDirError(a.path, rel, false, err)
		}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("stat directory %w: %w", errors.WithElement(path.Join(a.path, rel)), err)
	}
	return os.NewFile(uintptr(fd), path.Join(a.path, rel)), &st, nil
}

func (a *anchor) Walk(ctx context.Context, rel string, b WalkBounds, fn func(rel string, e EntryInfo) error) error {
	if b.MaxEntries <= 0 || b.MaxDepth <= 0 || b.MaxNameBytes <= 0 || b.BatchSize < 0 {
		return fmt.Errorf("invalid walk bounds %+v", b)
	}
	if b.BatchSize == 0 {
		b.BatchSize = defaultBatchSize
	}
	f, st, err := a.openDirRel(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	w := &walker{a: a, b: b, fn: fn, dev: uint64(st.Dev)} //nolint:unconvert // Stat_t widths differ by architecture
	return w.walk(ctx, f, "", 1)
}

type walker struct {
	a     *anchor
	b     WalkBounds
	fn    func(rel string, e EntryInfo) error
	dev   uint64
	count int
}

func (w *walker) walk(ctx context.Context, dir *os.File, prefix string, depth int) error {
	dirfd := int(dir.Fd())
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, err := dir.Readdirnames(w.b.BatchSize)
		if len(names) == 0 {
			if err == nil || errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read directory %w: %w", errors.WithElement(path.Join(w.a.path, prefix)), err)
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			rel := path.Join(prefix, name)
			w.count++
			switch {
			case w.count > w.b.MaxEntries:
				return fmt.Errorf("%w: more than %d entries", errors.ErrWalkLimitExceeded, w.b.MaxEntries)
			case len(name) > w.b.MaxNameBytes:
				return fmt.Errorf("%w %w: name longer than %d bytes", errors.ErrWalkLimitExceeded, errors.WithElement(rel), w.b.MaxNameBytes)
			case depth > w.b.MaxDepth:
				return fmt.Errorf("%w %w: deeper than %d levels", errors.ErrWalkLimitExceeded, errors.WithElement(rel), w.b.MaxDepth)
			}
			var st unix.Stat_t
			if err := w.a.sys.fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return fmt.Errorf("stat %w: %w", errors.WithElement(rel), err)
			}
			e := entryInfo(name, &st)
			err := w.fn(rel, e)
			if err == fs.SkipDir && e.IsDir() { //nolint:errorlint // fs.SkipDir is returned unwrapped by convention
				continue
			}
			if err != nil {
				return err
			}
			if !e.IsDir() {
				continue
			}
			if e.Dev != w.dev {
				return fmt.Errorf("%w %w", errors.ErrCrossDevice, errors.WithElement(rel))
			}
			fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return w.a.openDirError(path.Join(w.a.path, prefix), name, false, err)
			}
			var opened unix.Stat_t
			if err := w.a.sys.fstat(fd, &opened); err != nil {
				_ = unix.Close(fd)
				return fmt.Errorf("stat opened directory %w: %w", errors.WithElement(rel), err)
			}
			if uint64(opened.Dev) != e.Dev || uint64(opened.Ino) != e.Ino { //nolint:unconvert // Stat_t widths differ by architecture
				_ = unix.Close(fd)
				return fmt.Errorf("%w %w: directory changed during walk", errors.ErrUnsafeFileType, errors.WithElement(rel))
			}
			child := os.NewFile(uintptr(fd), rel)
			err = w.walk(ctx, child, rel, depth+1)
			_ = child.Close()
			if err != nil {
				return err
			}
		}
	}
}

type file struct {
	f    *os.File
	info EntryInfo
}

func (f *file) ReadAt(p []byte, off int64) (int, error) { return f.f.ReadAt(p, off) }
func (f *file) Close() error                            { return f.f.Close() }
func (f *file) Size() int64                             { return f.info.Size }
func (f *file) Info() EntryInfo                         { return f.info }

func (a *anchor) OpenFile(rel string) (File, error) {
	if rel == "." {
		return nil, fmt.Errorf("%w %w: not a regular file", errors.ErrUnsafeFileType, errors.WithElement(a.path))
	}
	dir, base, release, err := a.openParent(rel)
	if err != nil {
		return nil, err
	}
	defer release()
	display := path.Join(a.path, rel)
	var before unix.Stat_t
	if err := a.sys.fstatat(dir, base, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("stat %w: %w", errors.WithElement(display), err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%w %w: not a regular file", errors.ErrUnsafeFileType, errors.WithElement(display))
	}
	fd, err := unix.Openat(dir, base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %w: %w", errors.WithElement(display), err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("stat %w: %w", errors.WithElement(display), err)
	}
	if opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Dev != before.Dev || opened.Ino != before.Ino {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w %w: file changed while opening", errors.ErrUnsafeFileType, errors.WithElement(display))
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open %w: %w", errors.WithElement(display), err)
	}
	return &file{f: os.NewFile(uintptr(fd), display), info: entryInfo(base, &opened)}, nil
}

func (a *anchor) ReadFile(rel string, limit int64) ([]byte, error) {
	if limit <= 0 || limit == math.MaxInt64 {
		return nil, fmt.Errorf("invalid file read limit %d", limit)
	}
	f, err := a.OpenFile(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if f.Size() > limit {
		return nil, fmt.Errorf("%w %w: larger than %d bytes", errors.ErrFileLimitExceeded, errors.WithElement(path.Join(a.path, rel)), limit)
	}
	data, err := io.ReadAll(io.NewSectionReader(f, 0, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %w: %w", errors.WithElement(path.Join(a.path, rel)), err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w %w: larger than %d bytes", errors.ErrFileLimitExceeded, errors.WithElement(path.Join(a.path, rel)), limit)
	}
	return data, nil
}

// checkCopySource refuses anything but a singly linked regular file without
// special mode bits or file capabilities.
func checkCopySource(src Anchor, rel string) (EntryInfo, error) {
	e, err := src.Lstat(rel)
	if err != nil {
		return EntryInfo{}, err
	}
	display := path.Join(src.Path(), rel)
	switch {
	case !e.IsRegular():
		return EntryInfo{}, fmt.Errorf("%w %w: type %s", errors.ErrUnsafeFileType, errors.WithElement(display), e.Type)
	case e.Nlink != 1:
		return EntryInfo{}, fmt.Errorf("%w %w: %d links", errors.ErrHardLinkedFile, errors.WithElement(display), e.Nlink)
	case e.Perm&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0:
		return EntryInfo{}, fmt.Errorf("%w %w: mode %s", errors.ErrUnsafeFileMode, errors.WithElement(display), e.Perm)
	}
	_, state, err := src.GetXattr(rel, capabilityXattr, capabilityXattrLimit)
	switch state {
	case XattrPresent:
		return EntryInfo{}, fmt.Errorf("%w %w", errors.ErrFileCapability, errors.WithElement(display))
	case XattrUnknown:
		return EntryInfo{}, fmt.Errorf("%w %w: %w", errors.ErrFileCapabilityUnknown, errors.WithElement(display), err)
	}
	return e, nil
}

func openCheckedSource(src Anchor, rel string) (File, EntryInfo, error) {
	e, err := checkCopySource(src, rel)
	if err != nil {
		return nil, EntryInfo{}, err
	}
	f, err := src.OpenFile(rel)
	if err != nil {
		return nil, EntryInfo{}, err
	}
	if info := f.Info(); info.Dev != e.Dev || info.Ino != e.Ino || info.Nlink != 1 {
		_ = f.Close()
		return nil, EntryInfo{}, fmt.Errorf("%w %w: file changed while opening", errors.ErrUnsafeFileType, errors.WithElement(path.Join(src.Path(), rel)))
	}
	return f, e, nil
}

func chunkSize(b CopyBounds) int {
	if b.ChunkSize <= 0 {
		return defaultChunkSize
	}
	return b.ChunkSize
}

// streamFile reads at most b.MaxBytes+1 bytes of f in chunks, checking ctx
// before each read and before each call to sink.
func streamFile(ctx context.Context, f File, display string, b CopyBounds, sink func([]byte) error) (int64, string, error) {
	if b.MaxBytes < 0 || b.MaxBytes == math.MaxInt64 {
		return 0, "", fmt.Errorf("invalid copy bound %d", b.MaxBytes)
	}
	h := sha256.New()
	buf := make([]byte, chunkSize(b))
	var off int64
	for {
		if err := ctx.Err(); err != nil {
			return off, "", err
		}
		want := int64(len(buf))
		if remaining := b.MaxBytes + 1 - off; remaining < want {
			want = remaining
		}
		n, err := f.ReadAt(buf[:want], off)
		if n > 0 {
			off += int64(n)
			if off > b.MaxBytes {
				return off, "", fmt.Errorf("%w %w: larger than %d bytes", errors.ErrFileLimitExceeded, errors.WithElement(display), b.MaxBytes)
			}
			_, _ = h.Write(buf[:n])
			if sink != nil {
				if err := ctx.Err(); err != nil {
					return off, "", err
				}
				if err := sink(buf[:n]); err != nil {
					return off, "", err
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return off, hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return off, "", fmt.Errorf("read %w: %w", errors.WithElement(display), err)
		}
	}
}

func (a *anchor) Hash(ctx context.Context, rel string, b CopyBounds) (FileDigest, error) {
	f, e, err := openCheckedSource(a, rel)
	if err != nil {
		return FileDigest{}, err
	}
	defer f.Close()
	size, sum, err := streamFile(ctx, f, path.Join(a.path, rel), b, nil)
	if err != nil {
		return FileDigest{}, err
	}
	return FileDigest{Size: size, SHA256: sum, Mode: e.Perm.Perm()}, nil
}

func (a *anchor) createExclusive(name string) (*os.File, error) {
	fd, err := unix.Openat(a.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create %w: %w: %w", errors.WithElement(path.Join(a.path, name)), errors.ErrDestinationExists, fs.ErrExist)
		}
		return nil, fmt.Errorf("create %w: %w", errors.WithElement(path.Join(a.path, name)), err)
	}
	return os.NewFile(uintptr(fd), path.Join(a.path, name)), nil
}

// finishFile sets the final mode, syncs and closes a file this anchor created.
func finishFile(f *os.File, perm fs.FileMode) error {
	if err := f.Chmod(perm.Perm()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (a *anchor) CopyFrom(ctx context.Context, src Anchor, srcRel, name string, perm fs.FileMode, b CopyBounds) (FileDigest, error) {
	if a.readOnly {
		return FileDigest{}, fmt.Errorf("copy into %w: %w", errors.WithElement(a.path), errors.ErrReadOnlyAnchor)
	}
	if err := validName(name); err != nil {
		return FileDigest{}, err
	}
	in, _, err := openCheckedSource(src, srcRel)
	if err != nil {
		return FileDigest{}, err
	}
	defer in.Close()
	out, err := a.createExclusive(name)
	if err != nil {
		return FileDigest{}, err
	}
	fail := func(err error) (FileDigest, error) {
		_ = out.Close()
		_ = a.sys.unlinkat(a.fd, name, 0)
		return FileDigest{}, err
	}
	size, sum, err := streamFile(ctx, in, path.Join(src.Path(), srcRel), b, func(p []byte) error {
		if _, err := out.Write(p); err != nil {
			return fmt.Errorf("write %w: %w", errors.WithElement(path.Join(a.path, name)), err)
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := finishFile(out, perm); err != nil {
		_ = a.sys.unlinkat(a.fd, name, 0)
		return FileDigest{}, fmt.Errorf("finish %w: %w", errors.WithElement(path.Join(a.path, name)), err)
	}
	return FileDigest{Size: size, SHA256: sum, Mode: perm.Perm()}, nil
}

func (a *anchor) WriteFileExclusive(name string, data []byte, perm fs.FileMode) error {
	if a.readOnly {
		return fmt.Errorf("write into %w: %w", errors.WithElement(a.path), errors.ErrReadOnlyAnchor)
	}
	if err := validName(name); err != nil {
		return err
	}
	out, err := a.createExclusive(name)
	if err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		_ = out.Close()
		_ = a.sys.unlinkat(a.fd, name, 0)
		return fmt.Errorf("write %w: %w", errors.WithElement(path.Join(a.path, name)), err)
	}
	if err := finishFile(out, perm); err != nil {
		_ = a.sys.unlinkat(a.fd, name, 0)
		return fmt.Errorf("finish %w: %w", errors.WithElement(path.Join(a.path, name)), err)
	}
	return nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (a *anchor) ReplaceFileDurable(name string, data []byte, perm fs.FileMode) error {
	if a.readOnly {
		return fmt.Errorf("replace in %w: %w", errors.WithElement(a.path), errors.ErrReadOnlyAnchor)
	}
	if err := validName(name); err != nil {
		return err
	}
	var (
		out *os.File
		tmp string
	)
	for i := 0; i < tempNameAttempts && out == nil; i++ {
		suffix, err := randomHex(8)
		if err != nil {
			return err
		}
		tmp = ".tmp-" + suffix
		out, err = a.createExclusive(tmp)
		if err != nil && !errors.Is(err, errors.ErrDestinationExists) {
			return err
		}
	}
	if out == nil {
		return fmt.Errorf("replace %w: no unused temporary name", errors.WithElement(path.Join(a.path, name)))
	}
	if _, err := out.Write(data); err != nil {
		_ = out.Close()
		_ = a.sys.unlinkat(a.fd, tmp, 0)
		return fmt.Errorf("write %w: %w", errors.WithElement(path.Join(a.path, name)), err)
	}
	if err := finishFile(out, perm); err != nil {
		_ = a.sys.unlinkat(a.fd, tmp, 0)
		return fmt.Errorf("finish %w: %w", errors.WithElement(path.Join(a.path, name)), err)
	}
	if err := unix.Renameat(a.fd, tmp, a.fd, name); err != nil {
		_ = a.sys.unlinkat(a.fd, tmp, 0)
		return fmt.Errorf("replace %w: %w", errors.WithElement(path.Join(a.path, name)), err)
	}
	if err := unix.Fsync(a.fd); err != nil {
		return fmt.Errorf("sync %w: %w", errors.WithElement(a.path), err)
	}
	return nil
}

func resolveAnchor(x Anchor) (*anchor, error) {
	for i := 0; i < 8; i++ {
		switch v := x.(type) {
		case *anchor:
			return v, nil
		case interface{ Unwrap() Anchor }:
			x = v.Unwrap()
		default:
			return nil, fmt.Errorf("directory handle %T is not provided by this package", x)
		}
	}
	return nil, fmt.Errorf("directory handle wrapping is too deep")
}

func (a *anchor) RenameNoReplace(oldName string, newDir Anchor, newName string) error {
	if err := validName(oldName); err != nil {
		return err
	}
	if err := validName(newName); err != nil {
		return err
	}
	nd, err := resolveAnchor(newDir)
	if err != nil {
		return err
	}
	if a.readOnly || nd.readOnly {
		return fmt.Errorf("rename %w: %w", errors.WithElement(path.Join(a.path, oldName)), errors.ErrReadOnlyAnchor)
	}
	err = a.sys.renameat2(a.fd, oldName, nd.fd, newName, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST):
		return fmt.Errorf("rename to %w: %w: %w", errors.WithElement(path.Join(nd.path, newName)), errors.ErrDestinationExists, fs.ErrExist)
	case errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL):
		return fmt.Errorf("rename to %w: no-replace rename is not supported: %w", errors.WithElement(path.Join(nd.path, newName)), err)
	default:
		return fmt.Errorf("rename %w: %w", errors.WithElement(path.Join(a.path, oldName)), err)
	}
}

func (a *anchor) GetXattr(rel, attr string, limit int) ([]byte, XattrState, error) {
	if limit <= 0 {
		return nil, XattrUnknown, fmt.Errorf("invalid attribute limit %d", limit)
	}
	fd := a.fd
	display := path.Join(a.path, rel)
	if rel != "." {
		dir, base, release, err := a.openParent(rel)
		if err != nil {
			return nil, XattrUnknown, err
		}
		defer release()
		var before unix.Stat_t
		if err := a.sys.fstatat(dir, base, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, XattrUnknown, fmt.Errorf("stat %w: %w", errors.WithElement(display), err)
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
		switch before.Mode & unix.S_IFMT {
		case unix.S_IFREG:
		case unix.S_IFDIR:
			flags |= unix.O_DIRECTORY
		default:
			return nil, XattrUnknown, fmt.Errorf("%w %w: not a regular file or directory", errors.ErrUnsafeFileType, errors.WithElement(display))
		}
		opened, err := unix.Openat(dir, base, flags, 0)
		if err != nil {
			return nil, XattrUnknown, fmt.Errorf("open %w: %w", errors.WithElement(display), err)
		}
		defer func() { _ = unix.Close(opened) }()
		var st unix.Stat_t
		if err := a.sys.fstat(opened, &st); err != nil {
			return nil, XattrUnknown, fmt.Errorf("stat opened entry %w: %w", errors.WithElement(display), err)
		}
		if st.Dev != before.Dev || st.Ino != before.Ino {
			return nil, XattrUnknown, fmt.Errorf("%w %w: entry changed while opening", errors.ErrUnsafeFileType, errors.WithElement(display))
		}
		fd = opened
	}
	buf := make([]byte, limit+1)
	n, err := a.sys.fgetxattr(fd, attr, buf)
	switch {
	case err == nil && n > limit:
		return nil, XattrUnknown, fmt.Errorf("attribute %s of %w is longer than %d bytes", attr, errors.WithElement(display), limit)
	case err == nil:
		return append([]byte(nil), buf[:n]...), XattrPresent, nil
	case errors.Is(err, unix.ENODATA):
		return nil, XattrAbsent, nil
	case errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP):
		return nil, XattrUnsupported, nil
	case errors.Is(err, unix.ERANGE):
		return nil, XattrUnknown, fmt.Errorf("attribute %s of %w is longer than %d bytes", attr, errors.WithElement(display), limit)
	default:
		return nil, XattrUnknown, fmt.Errorf("read attribute %s of %w: %w", attr, errors.WithElement(display), err)
	}
}

func (a *anchor) Sync() error {
	if err := unix.Fsync(a.fd); err != nil {
		return fmt.Errorf("sync %w: %w", errors.WithElement(a.path), err)
	}
	return nil
}

func (a *anchor) AvailableBytes() (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(a.fd, &st); err != nil {
		return 0, fmt.Errorf("statfs %w: %w", errors.WithElement(a.path), err)
	}
	if st.Bsize <= 0 {
		return 0, fmt.Errorf("statfs %w: invalid block size %d", errors.WithElement(a.path), st.Bsize)
	}
	bsize := uint64(st.Bsize)
	if st.Bavail > math.MaxUint64/bsize {
		return math.MaxUint64, nil
	}
	return st.Bavail * bsize, nil
}

func (a *anchor) ReadOnlyMount() (bool, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(a.fd, &st); err != nil {
		return false, fmt.Errorf("statfs %w: %w", errors.WithElement(a.path), err)
	}
	return int64(st.Flags)&unix.ST_RDONLY != 0, nil //nolint:unconvert // Statfs_t widths differ by architecture
}

func (a *anchor) Revalidate() error {
	moved := func(reason string, err error) error {
		if err != nil {
			return fmt.Errorf("%w %w: %s: %w", errors.ErrAnchorMoved, errors.WithElement(a.path), reason, err)
		}
		return fmt.Errorf("%w %w: %s", errors.ErrAnchorMoved, errors.WithElement(a.path), reason)
	}
	fd, err := unix.Open(a.start, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return moved("start directory unavailable", err)
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return moved("start directory unavailable", err)
	}
	if uint64(st.Dev) != a.startDev || uint64(st.Ino) != a.startIno { //nolint:unconvert // Stat_t widths differ by architecture
		return moved("start directory replaced", nil)
	}
	for _, link := range a.chain {
		next, err := unix.Openat(fd, link.name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return moved(fmt.Sprintf("component %q unavailable", link.name), err)
		}
		_ = unix.Close(fd)
		fd = next
		if err := unix.Fstat(fd, &st); err != nil {
			return moved(fmt.Sprintf("component %q unavailable", link.name), err)
		}
		if uint64(st.Dev) != link.dev || uint64(st.Ino) != link.ino { //nolint:unconvert // Stat_t widths differ by architecture
			return moved(fmt.Sprintf("component %q replaced", link.name), nil)
		}
		if a.trusted {
			if err := a.checkTrusted(&st, a.path); err != nil {
				return moved(fmt.Sprintf("component %q no longer trusted", link.name), err)
			}
		}
	}
	return nil
}

func (a *anchor) Close() error {
	if a.fd < 0 {
		return nil
	}
	err := unix.Close(a.fd)
	a.fd = -1
	return err
}

type removeFrame struct {
	f         *os.File
	fd        int
	parentFD  int
	name      string
	batch     []string
	next      int
	confirmed bool
	retries   int
}

type remover struct {
	a     *anchor
	b     RemoveBounds
	res   RemoveResult
	stack []*removeFrame
}

type removeStopErr struct {
	stop RemoveStop
	err  error
}

func (r *remover) halt(stop RemoveStop, err error) *removeStopErr {
	return &removeStopErr{stop: stop, err: err}
}

// spend accounts for one unit of work if the budget allows it.
func (r *remover) spend() bool {
	if r.res.Work >= r.b.MaxWork {
		return false
	}
	r.res.Work++
	return true
}

func (r *remover) canRemove() *removeStopErr {
	if r.res.Removed >= r.b.MaxNodes {
		return r.halt(RemoveNodeBudget, fmt.Errorf("removed %d entries", r.res.Removed))
	}
	if !r.spend() {
		return r.workExhausted()
	}
	return nil
}

func (r *remover) workExhausted() *removeStopErr {
	return r.halt(RemoveWorkBudget, fmt.Errorf("used %d work units", r.res.Work))
}

func isDirStat(st *unix.Stat_t) bool { return st.Mode&unix.S_IFMT == unix.S_IFDIR }

func (a *anchor) RemoveTree(ctx context.Context, name string, b RemoveBounds) (RemoveResult, error) {
	if a.readOnly {
		return RemoveResult{Stop: RemoveError}, fmt.Errorf("remove from %w: %w", errors.WithElement(a.path), errors.ErrReadOnlyAnchor)
	}
	if err := validName(name); err != nil {
		return RemoveResult{Stop: RemoveError}, err
	}
	if b.MaxNodes <= 0 || b.MaxWork <= 0 || b.MaxDetach < 0 || b.MaxDirRetries < 0 || b.MaxDepth <= 0 || b.BatchSize < 0 {
		return RemoveResult{Stop: RemoveError}, fmt.Errorf("invalid removal bounds %+v", b)
	}
	if b.BatchSize == 0 {
		b.BatchSize = defaultBatchSize
	}
	r := &remover{a: a, b: b}
	halt := r.run(ctx, name)
	for _, f := range r.stack {
		_ = f.f.Close()
	}
	r.stack = nil
	if halt == nil {
		r.res.Complete = true
		r.res.Stop = RemoveComplete
		return r.res, nil
	}
	r.res.Stop = halt.stop
	var err error
	switch halt.stop {
	case RemoveNodeBudget, RemoveWorkBudget, RemoveDetachBudget:
		err = fmt.Errorf("%w: %s: %w", errors.ErrWalkLimitExceeded, halt.stop, halt.err)
	case RemoveChurn:
		err = fmt.Errorf("%w: %w", errors.ErrRemoveChurn, halt.err)
	case RemoveCrossDevice:
		err = fmt.Errorf("%w: %w", errors.ErrCrossDevice, halt.err)
	default:
		err = halt.err
	}
	r.res.LastErr = sanitizeError(err.Error())
	return r.res, fmt.Errorf("remove %w: %w", errors.WithElement(path.Join(a.path, name)), err)
}

func (r *remover) ctxHalt(ctx context.Context) *removeStopErr {
	if err := ctx.Err(); err != nil {
		return r.halt(RemoveCanceled, err)
	}
	return nil
}

func (r *remover) run(ctx context.Context, name string) *removeStopErr {
	a := r.a
	if h := r.ctxHalt(ctx); h != nil {
		return h
	}
	if !r.spend() {
		return r.workExhausted()
	}
	var st unix.Stat_t
	if err := a.sys.fstatat(a.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return r.halt(RemoveError, fmt.Errorf("stat %q: %w", name, err))
	}
	if !isDirStat(&st) {
		return r.unlink(a.fd, name, 0)
	}
	if uint64(st.Dev) != a.dev { //nolint:unconvert // Stat_t widths differ by architecture
		return r.halt(RemoveCrossDevice, fmt.Errorf("%q is on device %d", name, st.Dev))
	}
	root, h := r.open(a.fd, name, &st)
	if h != nil || root == nil {
		return h
	}
	r.stack = append(r.stack, root)
	for len(r.stack) > 0 {
		top := r.stack[len(r.stack)-1]
		if top.next >= len(top.batch) {
			if h := r.readBatch(ctx, top); h != nil {
				return h
			}
			continue
		}
		entry := top.batch[top.next]
		top.next++
		if h := r.ctxHalt(ctx); h != nil {
			return h
		}
		if !r.spend() {
			return r.workExhausted()
		}
		var est unix.Stat_t
		if err := a.sys.fstatat(top.fd, entry, &est, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return r.halt(RemoveError, fmt.Errorf("stat %q: %w", entry, err))
		}
		if uint64(est.Dev) != a.dev { //nolint:unconvert // Stat_t widths differ by architecture
			return r.halt(RemoveCrossDevice, fmt.Errorf("%q is on device %d", entry, est.Dev))
		}
		if !isDirStat(&est) {
			if h := r.unlink(top.fd, entry, 0); h != nil {
				return h
			}
			continue
		}
		if len(r.stack) < r.b.MaxDepth {
			child, h := r.open(top.fd, entry, &est)
			if h != nil {
				return h
			}
			if child != nil {
				r.stack = append(r.stack, child)
			}
			continue
		}
		if h := r.detach(top.fd, entry); h != nil {
			return h
		}
	}
	return nil
}

func (r *remover) unlink(dirfd int, name string, flags int) *removeStopErr {
	if h := r.canRemove(); h != nil {
		return h
	}
	err := r.a.sys.unlinkat(dirfd, name, flags)
	switch {
	case err == nil:
		r.res.Removed++
	case errors.Is(err, unix.ENOENT):
	default:
		return r.halt(RemoveError, fmt.Errorf("unlink %q: %w", name, err))
	}
	return nil
}

func (r *remover) open(parentFD int, name string, st *unix.Stat_t) (*removeFrame, *removeStopErr) {
	if !r.spend() {
		return nil, r.workExhausted()
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, r.halt(RemoveError, fmt.Errorf("open directory %q: %w", name, err))
	}
	var opened unix.Stat_t
	if err := r.a.sys.fstat(fd, &opened); err != nil {
		_ = unix.Close(fd)
		return nil, r.halt(RemoveError, fmt.Errorf("stat opened directory %q: %w", name, err))
	}
	if opened.Dev != st.Dev || opened.Ino != st.Ino {
		_ = unix.Close(fd)
		return nil, r.halt(RemoveError, fmt.Errorf("directory %q changed during removal", name))
	}
	return &removeFrame{f: os.NewFile(uintptr(fd), name), fd: fd, parentFD: parentFD, name: name}, nil
}

func (r *remover) pop() {
	top := r.stack[len(r.stack)-1]
	_ = top.f.Close()
	r.stack = r.stack[:len(r.stack)-1]
}

func (r *remover) rewind(top *removeFrame) *removeStopErr {
	if !r.spend() {
		return r.workExhausted()
	}
	if _, err := top.f.Seek(0, io.SeekStart); err != nil {
		return r.halt(RemoveError, fmt.Errorf("rewind %q: %w", top.name, err))
	}
	top.confirmed = true
	return nil
}

// readBatch reads the next names of the top directory. An empty read is
// confirmed by one rewind and re-read before the directory itself is removed.
func (r *remover) readBatch(ctx context.Context, top *removeFrame) *removeStopErr {
	if h := r.ctxHalt(ctx); h != nil {
		return h
	}
	remaining := r.b.MaxWork - r.res.Work
	if remaining < 2 {
		return r.workExhausted()
	}
	n := r.b.BatchSize
	if remaining-1 < n {
		n = remaining - 1
	}
	r.res.Work++
	names, err := top.f.Readdirnames(n)
	r.res.Work += len(names)
	if err != nil && !errors.Is(err, io.EOF) {
		return r.halt(RemoveError, fmt.Errorf("read directory %q: %w", top.name, err))
	}
	if len(names) > 0 {
		top.batch, top.next, top.confirmed = names, 0, false
		return nil
	}
	top.batch, top.next = nil, 0
	if !top.confirmed {
		return r.rewind(top)
	}
	if h := r.canRemove(); h != nil {
		return h
	}
	err = r.a.sys.unlinkat(top.parentFD, top.name, unix.AT_REMOVEDIR)
	switch {
	case err == nil:
		r.res.Removed++
		r.pop()
	case errors.Is(err, unix.ENOENT):
		r.pop()
	case errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST):
		if top.retries >= r.b.MaxDirRetries {
			return r.halt(RemoveChurn, fmt.Errorf("directory %q stayed non-empty after %d retries", top.name, top.retries))
		}
		top.retries++
		r.res.Retries++
		return r.rewind(top)
	default:
		return r.halt(RemoveError, fmt.Errorf("remove directory %q: %w", top.name, err))
	}
	return nil
}

// detach moves a directory that is deeper than the handle budget into the
// anchor under a fresh .quarantine-* name so a later call can remove it.
func (r *remover) detach(dirfd int, name string) *removeStopErr {
	if len(r.res.Detached) >= r.b.MaxDetach {
		return r.halt(RemoveDetachBudget, fmt.Errorf("detached %d directories", len(r.res.Detached)))
	}
	for attempt := 0; attempt < 2; attempt++ {
		if !r.spend() {
			return r.workExhausted()
		}
		suffix, err := randomHex(8)
		if err != nil {
			return r.halt(RemoveError, err)
		}
		target := quarantinePrefix + suffix
		err = r.a.sys.renameat2(dirfd, name, r.a.fd, target, unix.RENAME_NOREPLACE)
		switch {
		case err == nil:
			r.res.Detached = append(r.res.Detached, target)
			return nil
		case errors.Is(err, unix.EEXIST):
			continue
		case errors.Is(err, unix.EXDEV):
			return r.halt(RemoveCrossDevice, fmt.Errorf("detach %q: %w", name, err))
		case errors.Is(err, unix.ENOENT):
			return nil
		default:
			return r.halt(RemoveError, fmt.Errorf("detach %q: %w", name, err))
		}
	}
	return r.halt(RemoveError, fmt.Errorf("detach %q: no unused quarantine name", name))
}

// sanitizeError keeps printable runes and truncates to maxLastErrBytes.
func sanitizeError(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsPrint(r) {
			r = '?'
		}
		if b.Len()+utf8.RuneLen(r) > maxLastErrBytes {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
