package fileio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// sysWith returns a copy of the default kernel calls changed by mutate.
func sysWith(mutate func(s *sysOps)) *sysOps {
	sys := *defaultSysOps
	mutate(&sys)
	return &sys
}

// statAs reports the real lstat result of every entry, adjusted by mutate for
// the entry called name.
func statAs(name string, mutate func(st *unix.Stat_t)) func(int, string, *unix.Stat_t, int) error {
	return func(dirfd int, p string, st *unix.Stat_t, flags int) error {
		if err := unix.Fstatat(dirfd, p, st, flags); err != nil {
			return err
		}
		if p == name {
			mutate(st)
		}
		return nil
	}
}

// statFails fails the lstat of the entry called name with errno.
func statFails(name string, errno error) func(int, string, *unix.Stat_t, int) error {
	return func(dirfd int, p string, st *unix.Stat_t, flags int) error {
		if p == name {
			return errno
		}
		return unix.Fstatat(dirfd, p, st, flags)
	}
}

func asDirectory(st *unix.Stat_t) { st.Mode = unix.S_IFDIR | 0o700 }
func otherInode(st *unix.Stat_t)  { st.Ino++ }
func otherDevice(st *unix.Stat_t) { st.Dev++ }

func skipAsRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
}

func requireAbsent(t *testing.T, p string) {
	t.Helper()
	_, err := os.Lstat(p)
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func requireExists(t *testing.T, p string) {
	t.Helper()
	_, err := os.Lstat(p)
	require.NoError(t, err)
}

func findBlockDevice() string {
	entries, err := os.ReadDir("/dev")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Type()&fs.ModeDevice != 0 && e.Type()&fs.ModeCharDevice == 0 {
			return e.Name()
		}
	}
	return ""
}

func TestOpenAnchorRequests(t *testing.T) {
	testCases := []struct {
		name string
		path string
		opts DirOptions
		want error
	}{
		{name: "When creation is requested it should refuse to create the anchored directory", path: "/data/new", opts: DirOptions{Create: true, Trusted: true}},
		{name: "When exclusive creation is requested it should refuse to create the anchored directory", path: "/data/new", opts: DirOptions{Exclusive: true}},
		{name: "When the path is empty it should fail with ErrInvalidPath", path: "", want: errors.ErrInvalidPath},
		{name: "When the path contains a NUL byte it should fail with ErrInvalidPath", path: "/data/a\x00b", opts: DirOptions{Trusted: true}, want: errors.ErrInvalidPath},
		{name: "When an untrusted path is absent it should fail with a not-exist error", path: "/data/new", want: fs.ErrNotExist},
		{name: "When a trusted path is absent it should fail with a not-exist error", path: "/data/new", opts: DirOptions{Trusted: true}, want: fs.ErrNotExist},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			rw, root := anchorFixture(t)
			a, err := rw.OpenAnchor(tc.path, tc.opts)
			require.Error(err)
			require.Nil(a)
			if tc.want != nil {
				require.ErrorIs(err, tc.want)
			}
			requireAbsent(t, filepath.Join(root, "data", "new"))
		})
	}
}

func TestOpenAnchorRootDirectory(t *testing.T) {
	t.Run("When the root directory is absent it should fail to open a trusted anchor", func(t *testing.T) {
		require := require.New(t)
		root := filepath.Join(t.TempDir(), "absent")
		rw := NewReadWriter(NewReader(WithReaderRootDir(root)), NewWriter(WithWriterRootDir(root)))
		_, err := rw.OpenAnchor("/data", DirOptions{Trusted: true})
		require.ErrorIs(err, fs.ErrNotExist)
		requireAbsent(t, root)
	})
	t.Run("When the root directory is writable by others it should refuse a trusted anchor but open an untrusted one", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		require.NoError(os.Chmod(root, 0o777))
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
		_, err := rw.OpenAnchor("/data", DirOptions{Trusted: true})
		require.ErrorIs(err, errors.ErrUntrustedDirectory)
		a, err := rw.OpenAnchor("/data", DirOptions{ReadOnly: true})
		require.NoError(err)
		require.NoError(a.Close())
	})
}

func TestAnchorOpenDirBoundaries(t *testing.T) {
	t.Run("When no creation mode is given it should create the directory with mode 0700", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		data := openTrusted(t, rw, "/data")
		dir, err := data.OpenDir("new", DirOptions{Create: true, Trusted: true})
		require.NoError(err)
		defer dir.Close()
		info, err := os.Lstat(filepath.Join(root, "data", "new"))
		require.NoError(err)
		require.True(info.IsDir())
		require.Equal(fs.FileMode(0o700), info.Mode().Perm())
	})
	t.Run("When an untrusted name is a symlink it should fail with ErrUnsafeFileType without following it", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		sentinel := sentinelDir(t, root)
		require.NoError(os.Symlink(sentinel, filepath.Join(root, "data", "link")))
		before := treeSnapshot(t, sentinel)
		data := openTrusted(t, rw, "/data")
		_, err := data.OpenDir("link", DirOptions{})
		require.ErrorIs(err, errors.ErrUnsafeFileType)
		require.Equal(before, treeSnapshot(t, sentinel))
	})
	t.Run("When the directory is absent and creation is not requested it should fail with a not-exist error", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		data := openTrusted(t, rw, "/data")
		_, err := data.OpenDir("missing", DirOptions{Trusted: true})
		require.ErrorIs(err, fs.ErrNotExist)
		requireAbsent(t, filepath.Join(root, "data", "missing"))
	})
}

func TestAnchorLstat(t *testing.T) {
	t.Run("When asked about the anchor itself it should describe the anchored directory", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		data := openTrusted(t, rw, "/data")
		e, err := data.Lstat(".")
		require.NoError(err)
		require.Equal(".", e.Name)
		require.True(e.IsDir())
		require.EqualValues(os.Geteuid(), e.UID)
		var st unix.Stat_t
		require.NoError(unix.Lstat(filepath.Join(root, "data"), &st))
		require.EqualValues(st.Ino, e.Ino)
		require.EqualValues(st.Dev, e.Dev)
	})
	t.Run("When an entry is a device it should report its device type and refuse to open it", func(t *testing.T) {
		require := require.New(t)
		rw := NewReadWriter(NewReader(), NewWriter())
		dev, err := rw.OpenAnchor("/dev", DirOptions{ReadOnly: true})
		require.NoError(err)
		defer dev.Close()
		e, err := dev.Lstat("null")
		require.NoError(err)
		require.Equal(fs.ModeDevice|fs.ModeCharDevice, e.Type)
		require.False(e.IsRegular())
		_, err = dev.OpenFile("null")
		require.ErrorIs(err, errors.ErrUnsafeFileType)
		_, state, err := dev.GetXattr("null", "user.flightctl", 64)
		require.ErrorIs(err, errors.ErrUnsafeFileType)
		require.Equal(XattrUnknown, state)
		if block := findBlockDevice(); block != "" {
			e, err = dev.Lstat(block)
			require.NoError(err)
			require.Equal(fs.ModeDevice, e.Type)
		}
	})
}

func TestAnchorRelativePaths(t *testing.T) {
	content := []byte("payload")
	sum := sha256.Sum256(content)
	ctx := context.Background()
	walkBounds := WalkBounds{MaxEntries: 10, MaxDepth: 4, MaxNameBytes: 255}
	ops := []struct {
		name string
		run  func(a Anchor, rel string) error
	}{
		{name: "Lstat", run: func(a Anchor, rel string) error { _, err := a.Lstat(rel); return err }},
		{name: "OpenFile", run: func(a Anchor, rel string) error {
			f, err := a.OpenFile(rel)
			if err == nil {
				_ = f.Close()
			}
			return err
		}},
		{name: "ReadFile", run: func(a Anchor, rel string) error { _, err := a.ReadFile(rel, 1024); return err }},
		{name: "Hash", run: func(a Anchor, rel string) error { _, err := a.Hash(ctx, rel, CopyBounds{MaxBytes: 1024}); return err }},
		{name: "GetXattr", run: func(a Anchor, rel string) error { _, _, err := a.GetXattr(rel, "user.flightctl", 64); return err }},
		{name: "Walk", run: func(a Anchor, rel string) error {
			return a.Walk(ctx, rel, walkBounds, func(string, EntryInfo) error { return nil })
		}},
	}
	setup := func(t *testing.T) (Anchor, string) {
		t.Helper()
		rw, root := anchorFixture(t)
		sentinel := sentinelDir(t, root)
		dir := filepath.Join(root, "data", "a", "b")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "f"), content, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(root, "data", "a", "file"), content, 0o600))
		require.NoError(t, os.Symlink(sentinel, filepath.Join(root, "data", "a", "link")))
		return openTrusted(t, rw, "/data"), sentinel
	}

	t.Run("When a path has several components it should resolve each of them beneath the anchor", func(t *testing.T) {
		require := require.New(t)
		data, _ := setup(t)
		e, err := data.Lstat("a/b/f")
		require.NoError(err)
		require.Equal("f", e.Name)
		require.True(e.IsRegular())
		require.Equal(int64(len(content)), e.Size)
		f, err := data.OpenFile("a/b/f")
		require.NoError(err)
		require.Equal(int64(len(content)), f.Size())
		require.NoError(f.Close())
		got, err := data.ReadFile("a/b/f", int64(len(content)))
		require.NoError(err)
		require.Equal(content, got)
		d, err := data.Hash(ctx, "a/b/f", CopyBounds{MaxBytes: int64(len(content))})
		require.NoError(err)
		require.Equal(hex.EncodeToString(sum[:]), d.SHA256)
		_, state, err := data.GetXattr("a/b/f", "user.flightctl-absent", 64)
		require.NoError(err)
		require.Contains([]XattrState{XattrAbsent, XattrUnsupported}, state)
		var seen []string
		require.NoError(data.Walk(ctx, "a/b", walkBounds, func(rel string, _ EntryInfo) error {
			seen = append(seen, rel)
			return nil
		}))
		require.Equal([]string{"f"}, seen)
	})

	failures := []struct {
		name string
		rels []string
		want error
	}{
		{name: "When an intermediate component is a symlink it should fail with ErrUnsafeFileType without following it", rels: []string{"a/link/keep"}, want: errors.ErrUnsafeFileType},
		{name: "When an intermediate component is a regular file it should fail with ErrUnsafeFileType", rels: []string{"a/file/x"}, want: errors.ErrUnsafeFileType},
		{name: "When an intermediate component is absent it should fail with a not-exist error", rels: []string{"a/missing/x"}, want: fs.ErrNotExist},
		{name: "When the last component is absent it should fail with a not-exist error", rels: []string{"a/b/missing", "missing"}, want: fs.ErrNotExist},
		{name: "When the path is not a clean relative path it should fail with ErrInvalidPath", rels: []string{"", "/a", "..", "../a", "a/../a", "a//b", "./a", "a/", "a\x00b"}, want: errors.ErrInvalidPath},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			data, sentinel := setup(t)
			before := treeSnapshot(t, sentinel)
			for _, op := range ops {
				for _, rel := range tc.rels {
					require.ErrorIs(op.run(data, rel), tc.want, "%s(%q)", op.name, rel)
				}
			}
			require.Equal(before, treeSnapshot(t, sentinel))
		})
	}
}

func TestAnchorWalkBoundaries(t *testing.T) {
	ctx := context.Background()
	bounds := WalkBounds{MaxEntries: 100, MaxDepth: 4, MaxNameBytes: 255}
	collect := func(seen *[]string) func(string, EntryInfo) error {
		return func(rel string, _ EntryInfo) error {
			*seen = append(*seen, rel)
			return nil
		}
	}
	setup := func(t *testing.T, sys *sysOps, files ...string) Anchor {
		t.Helper()
		rw, root := anchorFixture(t)
		for _, f := range files {
			p := filepath.Join(root, "data", "w", f)
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
			require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		}
		if sys == nil {
			sys = defaultSysOps
		}
		return openWithSys(t, rw, "/data", sys)
	}

	t.Run("When the bounds are invalid it should fail without visiting any entry", func(t *testing.T) {
		require := require.New(t)
		data := setup(t, nil, "f")
		for _, b := range []WalkBounds{
			{MaxEntries: 0, MaxDepth: 4, MaxNameBytes: 255},
			{MaxEntries: 10, MaxDepth: 0, MaxNameBytes: 255},
			{MaxEntries: 10, MaxDepth: 4, MaxNameBytes: 0},
			{MaxEntries: 10, MaxDepth: 4, MaxNameBytes: 255, BatchSize: -1},
		} {
			var seen []string
			require.Error(data.Walk(ctx, "w", b, collect(&seen)), "%+v", b)
			require.Empty(seen)
		}
	})
	t.Run("When the path is the anchor itself it should walk the anchored directory", func(t *testing.T) {
		require := require.New(t)
		data := setup(t, nil, "x", "d/y")
		var seen []string
		require.NoError(data.Walk(ctx, ".", bounds, collect(&seen)))
		sort.Strings(seen)
		require.Equal([]string{"w", "w/d", "w/d/y", "w/x"}, seen)
	})
	t.Run("When the callback skips a directory it should not descend into it and continue with the rest", func(t *testing.T) {
		require := require.New(t)
		data := setup(t, nil, "skip/inner/deep", "keep/k", "f")
		var seen []string
		require.NoError(data.Walk(ctx, "w", bounds, func(rel string, e EntryInfo) error {
			seen = append(seen, rel)
			if rel == "skip" {
				require.True(e.IsDir())
				return fs.SkipDir
			}
			return nil
		}))
		sort.Strings(seen)
		require.Equal([]string{"f", "keep", "keep/k", "skip"}, seen)
	})
	t.Run("When the walked path is not a real directory it should fail with ErrUnsafeFileType", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		sentinel := sentinelDir(t, root)
		require.NoError(os.WriteFile(filepath.Join(root, "data", "file"), []byte("x"), 0o600))
		require.NoError(os.Symlink(sentinel, filepath.Join(root, "data", "dirlink")))
		data := openTrusted(t, rw, "/data")
		for _, rel := range []string{"file", "dirlink"} {
			var seen []string
			require.ErrorIs(data.Walk(ctx, rel, bounds, collect(&seen)), errors.ErrUnsafeFileType, rel)
			require.Empty(seen)
		}
	})
	t.Run("When the context is already canceled it should not visit any entry", func(t *testing.T) {
		require := require.New(t)
		data := setup(t, nil, "f")
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		var seen []string
		require.ErrorIs(data.Walk(canceled, "w", bounds, collect(&seen)), context.Canceled)
		require.Empty(seen)
	})
	t.Run("When an entry cannot be inspected it should fail naming that entry", func(t *testing.T) {
		require := require.New(t)
		data := setup(t, sysWith(func(s *sysOps) { s.fstatat = statFails("bad", unix.EIO) }), "bad")
		var seen []string
		err := data.Walk(ctx, "w", bounds, collect(&seen))
		require.ErrorIs(err, unix.EIO)
		require.Contains(err.Error(), "bad")
		require.Empty(seen)
	})
	t.Run("When a subdirectory is on another device it should fail with ErrCrossDevice without descending", func(t *testing.T) {
		require := require.New(t)
		data := setup(t, sysWith(func(s *sysOps) { s.fstatat = statAs("mnt", otherDevice) }), "mnt/keep")
		var seen []string
		require.ErrorIs(data.Walk(ctx, "w", bounds, collect(&seen)), errors.ErrCrossDevice)
		require.Equal([]string{"mnt"}, seen)
	})
	t.Run("When a directory is replaced by a file after inspection it should fail with ErrUnsafeFileType", func(t *testing.T) {
		require := require.New(t)
		data := setup(t, sysWith(func(s *sysOps) { s.fstatat = statAs("f", asDirectory) }), "f")
		var seen []string
		require.ErrorIs(data.Walk(ctx, "w", bounds, collect(&seen)), errors.ErrUnsafeFileType)
		require.Equal([]string{"f"}, seen)
	})
	t.Run("When a directory is swapped after inspection it should fail with ErrUnsafeFileType without descending", func(t *testing.T) {
		require := require.New(t)
		data := setup(t, sysWith(func(s *sysOps) { s.fstatat = statAs("sub", otherInode) }), "sub/inner")
		var seen []string
		require.ErrorIs(data.Walk(ctx, "w", bounds, collect(&seen)), errors.ErrUnsafeFileType)
		require.Equal([]string{"sub"}, seen)
	})
}

func TestAnchorOpenFileAndReadFile(t *testing.T) {
	content := []byte("0123456789")
	setup := func(t *testing.T, sys *sysOps) (Anchor, string) {
		t.Helper()
		rw, root := anchorFixture(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, "data", "f"), content, 0o600))
		if sys == nil {
			sys = defaultSysOps
		}
		return openWithSys(t, rw, "/data", sys), root
	}

	t.Run("When the path is the anchor itself it should refuse to open it as a file", func(t *testing.T) {
		require := require.New(t)
		data, _ := setup(t, nil)
		_, err := data.OpenFile(".")
		require.ErrorIs(err, errors.ErrUnsafeFileType)
		_, err = data.ReadFile(".", 10)
		require.ErrorIs(err, errors.ErrUnsafeFileType)
		_, err = data.Hash(context.Background(), ".", CopyBounds{MaxBytes: 10})
		require.ErrorIs(err, errors.ErrUnsafeFileType)
	})
	t.Run("When the entry is not a regular file it should refuse to open it", func(t *testing.T) {
		require := require.New(t)
		data, root := setup(t, nil)
		require.NoError(os.Mkdir(filepath.Join(root, "data", "dir"), 0o700))
		require.NoError(os.Symlink("f", filepath.Join(root, "data", "link")))
		require.NoError(unix.Mkfifo(filepath.Join(root, "data", "fifo"), 0o600))
		for _, rel := range []string{"dir", "link", "fifo"} {
			_, err := data.OpenFile(rel)
			require.ErrorIs(err, errors.ErrUnsafeFileType, rel)
			_, err = data.ReadFile(rel, 10)
			require.ErrorIs(err, errors.ErrUnsafeFileType, rel)
		}
	})
	t.Run("When the file is swapped while it is opened it should refuse it", func(t *testing.T) {
		require := require.New(t)
		data, _ := setup(t, sysWith(func(s *sysOps) { s.fstatat = statAs("f", otherInode) }))
		_, err := data.OpenFile("f")
		require.ErrorIs(err, errors.ErrUnsafeFileType)
		_, err = data.ReadFile("f", 10)
		require.ErrorIs(err, errors.ErrUnsafeFileType)
	})
	t.Run("When the file cannot be opened it should fail with the permission error", func(t *testing.T) {
		skipAsRoot(t)
		require := require.New(t)
		data, root := setup(t, nil)
		require.NoError(os.Chmod(filepath.Join(root, "data", "f"), 0))
		_, err := data.OpenFile("f")
		require.ErrorIs(err, fs.ErrPermission)
	})
	t.Run("When the read limit is invalid it should fail before reading", func(t *testing.T) {
		require := require.New(t)
		data, _ := setup(t, nil)
		for _, limit := range []int64{0, -1, math.MaxInt64} {
			got, err := data.ReadFile("f", limit)
			require.Error(err, limit)
			require.NotErrorIs(err, errors.ErrFileLimitExceeded)
			require.Nil(got)
		}
	})
	t.Run("When the file is larger than the limit it should fail with ErrFileLimitExceeded", func(t *testing.T) {
		require := require.New(t)
		data, _ := setup(t, nil)
		_, err := data.ReadFile("f", int64(len(content))-1)
		require.ErrorIs(err, errors.ErrFileLimitExceeded)
		got, err := data.ReadFile("f", int64(len(content)))
		require.NoError(err)
		require.Equal(content, got)
	})
}

// swappedSource opens a different file than the one it reports through
// Lstat, as if the source were replaced between inspection and opening.
type swappedSource struct {
	Anchor
	open string
}

func (s swappedSource) OpenFile(string) (File, error) { return s.Anchor.OpenFile(s.open) }

// refusingSource fails every attempt to open a file.
type refusingSource struct {
	Anchor
	err error
}

func (s refusingSource) OpenFile(string) (File, error) { return nil, s.err }

// faultSource serves files whose reads go through readAt.
type faultSource struct {
	Anchor
	readAt func(f File, p []byte, off int64) (int, error)
}

func (s faultSource) OpenFile(rel string) (File, error) {
	f, err := s.Anchor.OpenFile(rel)
	if err != nil {
		return nil, err
	}
	return faultFile{File: f, readAt: s.readAt}, nil
}

type faultFile struct {
	File
	readAt func(f File, p []byte, off int64) (int, error)
}

func (f faultFile) ReadAt(p []byte, off int64) (int, error) { return f.readAt(f.File, p, off) }

func TestAnchorCopyFailures(t *testing.T) {
	payload := []byte(strings.Repeat("abcdefgh", 1000))
	bounds := CopyBounds{MaxBytes: int64(len(payload)), ChunkSize: 1000}
	setup := func(t *testing.T, srcSys *sysOps) (Anchor, Anchor, string) {
		t.Helper()
		rw, root := anchorFixture(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "data", "src"), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "data", "dst"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "data", "src", "file"), payload, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(root, "data", "src", "other"), payload, 0o600))
		if srcSys == nil {
			srcSys = defaultSysOps
		}
		src, err := openAnchorWithSys(rw, "/data/src", DirOptions{ReadOnly: true}, srcSys)
		require.NoError(t, err)
		t.Cleanup(func() { _ = src.Close() })
		return src, openTrusted(t, rw, "/data/dst"), root
	}
	requireNoCopy := func(t *testing.T, root string) {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(root, "data", "dst"))
		require.NoError(t, err)
		require.Empty(t, entries)
	}

	t.Run("When the copy bound is invalid it should fail before reading and create nothing", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t, nil)
		for _, maxBytes := range []int64{-1, math.MaxInt64} {
			_, err := src.Hash(context.Background(), "file", CopyBounds{MaxBytes: maxBytes})
			require.Error(err, maxBytes)
			_, err = dst.CopyFrom(context.Background(), src, "file", "copy", 0o400, CopyBounds{MaxBytes: maxBytes})
			require.Error(err, maxBytes)
		}
		requireNoCopy(t, root)
	})
	t.Run("When the context is already canceled it should fail with the context error and create nothing", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := src.Hash(ctx, "file", bounds)
		require.ErrorIs(err, context.Canceled)
		_, err = dst.CopyFrom(ctx, src, "file", "copy", 0o400, bounds)
		require.ErrorIs(err, context.Canceled)
		requireNoCopy(t, root)
	})
	t.Run("When the source is absent it should fail with a not-exist error and create nothing", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t, nil)
		_, err := dst.CopyFrom(context.Background(), src, "missing", "copy", 0o400, bounds)
		require.ErrorIs(err, fs.ErrNotExist)
		requireNoCopy(t, root)
	})
	t.Run("When the destination name is not a single component it should fail and create nothing", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t, nil)
		for _, name := range []string{"", ".", "..", "a/b", "a\x00b", strings.Repeat("n", 256)} {
			_, err := dst.CopyFrom(context.Background(), src, "file", name, 0o400, bounds)
			require.ErrorIs(err, errors.ErrInvalidPath, name)
			require.ErrorIs(dst.WriteFileExclusive(name, []byte("x"), 0o400), errors.ErrInvalidPath, name)
			require.ErrorIs(dst.ReplaceFileDurable(name, []byte("x"), 0o400), errors.ErrInvalidPath, name)
		}
		requireNoCopy(t, root)
	})
	t.Run("When the capabilities of the source cannot be read it should refuse the source", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t, sysWith(func(s *sysOps) {
			s.fgetxattr = func(fd int, attr string, dest []byte) (int, error) {
				if attr == capabilityXattr {
					return 0, unix.EIO
				}
				return unix.Fgetxattr(fd, attr, dest)
			}
		}))
		_, err := dst.CopyFrom(context.Background(), src, "file", "copy", 0o400, bounds)
		require.ErrorIs(err, unix.EIO)
		require.ErrorIs(err, errors.ErrFileCapabilityUnknown)
		require.NotErrorIs(err, errors.ErrFileCapability)
		_, err = src.Hash(context.Background(), "file", bounds)
		require.ErrorIs(err, unix.EIO)
		require.ErrorIs(err, errors.ErrFileCapabilityUnknown)
		requireNoCopy(t, root)
	})
	t.Run("When the source is replaced between inspection and opening it should refuse it", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t, nil)
		_, err := dst.CopyFrom(context.Background(), swappedSource{Anchor: src, open: "other"}, "file", "copy", 0o400, bounds)
		require.ErrorIs(err, errors.ErrUnsafeFileType)
		requireNoCopy(t, root)
	})
	t.Run("When the source cannot be opened it should fail with that error and create nothing", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t, nil)
		refused := errors.New("open refused")
		_, err := dst.CopyFrom(context.Background(), refusingSource{Anchor: src, err: refused}, "file", "copy", 0o400, bounds)
		require.ErrorIs(err, refused)
		requireNoCopy(t, root)
	})
	t.Run("When reading the source fails it should fail and remove the partial copy", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t, nil)
		failing := faultSource{Anchor: src, readAt: func(f File, p []byte, off int64) (int, error) {
			if off > 0 {
				return 0, unix.EIO
			}
			return f.ReadAt(p, off)
		}}
		_, err := dst.CopyFrom(context.Background(), failing, "file", "copy", 0o400, bounds)
		require.ErrorIs(err, unix.EIO)
		requireNoCopy(t, root)
	})
	t.Run("When the context is canceled after the last chunk it should fail and remove the copy", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		canceling := faultSource{Anchor: src, readAt: func(f File, p []byte, off int64) (int, error) {
			n, err := f.ReadAt(p, off)
			if n == 0 && errors.Is(err, io.EOF) {
				cancel()
			}
			return n, err
		}}
		_, err := dst.CopyFrom(ctx, canceling, "file", "copy", 0o400, bounds)
		require.ErrorIs(err, context.Canceled)
		requireNoCopy(t, root)
	})
	t.Run("When the destination directory refuses new entries it should fail with the permission error and create nothing", func(t *testing.T) {
		skipAsRoot(t)
		require := require.New(t)
		src, dst, root := setup(t, nil)
		dir := filepath.Join(root, "data", "dst")
		require.NoError(os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		_, err := dst.CopyFrom(context.Background(), src, "file", "copy", 0o400, bounds)
		require.ErrorIs(err, fs.ErrPermission)
		require.ErrorIs(dst.WriteFileExclusive("copy", []byte("x"), 0o400), fs.ErrPermission)
		require.ErrorIs(dst.ReplaceFileDurable("copy", []byte("x"), 0o400), fs.ErrPermission)
		_, err = dst.OpenDir("sub", DirOptions{Create: true, Perm: 0o700})
		require.ErrorIs(err, fs.ErrPermission)
		requireNoCopy(t, root)
	})
	t.Run("When the replaced name is a directory it should fail and leave no temporary file", func(t *testing.T) {
		require := require.New(t)
		_, dst, root := setup(t, nil)
		dir := filepath.Join(root, "data", "dst", "dir")
		require.NoError(os.Mkdir(dir, 0o700))
		require.NoError(os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o600))
		require.ErrorIs(dst.ReplaceFileDurable("dir", []byte("new"), 0o600), unix.EISDIR)
		entries, err := os.ReadDir(filepath.Join(root, "data", "dst"))
		require.NoError(err)
		require.Len(entries, 1)
		require.Equal("dir", entries[0].Name())
		requireExists(t, filepath.Join(dir, "keep"))
	})
}

// foreignAnchor is a directory handle that does not come from this package.
type foreignAnchor struct{ Anchor }

// loopingAnchor unwraps to itself.
type loopingAnchor struct{ Anchor }

func (l *loopingAnchor) Unwrap() Anchor { return l }

func TestAnchorRenameNoReplaceFailures(t *testing.T) {
	setup := func(t *testing.T, sys *sysOps) (Anchor, string) {
		t.Helper()
		rw, root := anchorFixture(t)
		require.NoError(t, os.Mkdir(filepath.Join(root, "data", "staging"), 0o700))
		if sys == nil {
			sys = defaultSysOps
		}
		return openWithSys(t, rw, "/data", sys), root
	}
	requireUnchanged := func(t *testing.T, root string) {
		t.Helper()
		requireExists(t, filepath.Join(root, "data", "staging"))
		requireAbsent(t, filepath.Join(root, "data", "final"))
	}

	t.Run("When a name is not a single component it should fail without renaming", func(t *testing.T) {
		require := require.New(t)
		data, root := setup(t, nil)
		require.ErrorIs(data.RenameNoReplace("staging/x", data, "final"), errors.ErrInvalidPath)
		require.ErrorIs(data.RenameNoReplace("staging", data, "a/final"), errors.ErrInvalidPath)
		requireUnchanged(t, root)
	})
	t.Run("When the target handle is not from this package it should fail without renaming", func(t *testing.T) {
		require := require.New(t)
		data, root := setup(t, nil)
		require.Error(data.RenameNoReplace("staging", foreignAnchor{}, "final"))
		requireUnchanged(t, root)
	})
	t.Run("When the target handle unwraps to itself it should fail instead of looping", func(t *testing.T) {
		require := require.New(t)
		data, root := setup(t, nil)
		require.Error(data.RenameNoReplace("staging", &loopingAnchor{}, "final"))
		requireUnchanged(t, root)
	})
	t.Run("When the source is absent it should fail with a not-exist error", func(t *testing.T) {
		require := require.New(t)
		data, root := setup(t, nil)
		require.ErrorIs(data.RenameNoReplace("missing", data, "final"), fs.ErrNotExist)
		requireUnchanged(t, root)
	})
	for _, errno := range []unix.Errno{unix.ENOSYS, unix.EINVAL} {
		t.Run("When the kernel does not support no-replace renames it should fail without renaming: "+errno.Error(), func(t *testing.T) {
			require := require.New(t)
			data, root := setup(t, sysWith(func(s *sysOps) {
				s.renameat2 = func(int, string, int, string, uint) error { return errno }
			}))
			err := data.RenameNoReplace("staging", data, "final")
			require.ErrorIs(err, errno)
			require.Contains(err.Error(), "not supported")
			requireUnchanged(t, root)
		})
	}
}

func TestAnchorGetXattrBoundaries(t *testing.T) {
	setup := func(t *testing.T, sys *sysOps) (Anchor, string) {
		t.Helper()
		rw, root := anchorFixture(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, "data", "file"), []byte("x"), 0o600))
		require.NoError(t, os.Mkdir(filepath.Join(root, "data", "dir"), 0o700))
		if sys == nil {
			sys = defaultSysOps
		}
		return openWithSys(t, rw, "/data", sys), root
	}
	setxattr := func(t *testing.T, p, value string) {
		t.Helper()
		if err := unix.Lsetxattr(p, "user.flightctl", []byte(value), 0); err != nil {
			t.Skipf("filesystem does not support user xattrs: %v", err)
		}
	}

	t.Run("When the limit is not positive it should fail with an unknown state", func(t *testing.T) {
		require := require.New(t)
		data, _ := setup(t, nil)
		for _, limit := range []int{0, -1} {
			_, state, err := data.GetXattr("file", "user.flightctl", limit)
			require.Error(err, limit)
			require.Equal(XattrUnknown, state)
		}
	})
	t.Run("When the entry is a directory it should read the attribute of that directory", func(t *testing.T) {
		require := require.New(t)
		data, root := setup(t, nil)
		setxattr(t, filepath.Join(root, "data", "dir"), "dir-label")
		v, state, err := data.GetXattr("dir", "user.flightctl", 64)
		require.NoError(err)
		require.Equal(XattrPresent, state)
		require.Equal([]byte("dir-label"), v)
	})
	t.Run("When the value is exactly one byte longer than the limit it should fail with an unknown state", func(t *testing.T) {
		require := require.New(t)
		data, root := setup(t, nil)
		setxattr(t, filepath.Join(root, "data", "file"), "label")
		_, state, err := data.GetXattr("file", "user.flightctl", len("label")-1)
		require.Error(err)
		require.Equal(XattrUnknown, state)
		v, state, err := data.GetXattr("file", "user.flightctl", len("label"))
		require.NoError(err)
		require.Equal(XattrPresent, state)
		require.Equal([]byte("label"), v)
	})
	t.Run("When the entry is swapped while it is opened it should fail with ErrUnsafeFileType", func(t *testing.T) {
		require := require.New(t)
		data, _ := setup(t, sysWith(func(s *sysOps) { s.fstatat = statAs("file", otherInode) }))
		_, state, err := data.GetXattr("file", "user.flightctl", 64)
		require.ErrorIs(err, errors.ErrUnsafeFileType)
		require.Equal(XattrUnknown, state)
	})
	t.Run("When the entry cannot be opened it should fail with an unknown state", func(t *testing.T) {
		skipAsRoot(t)
		require := require.New(t)
		data, root := setup(t, nil)
		require.NoError(os.Chmod(filepath.Join(root, "data", "file"), 0))
		_, state, err := data.GetXattr("file", "user.flightctl", 64)
		require.ErrorIs(err, fs.ErrPermission)
		require.Equal(XattrUnknown, state)
	})
}

func TestAnchorRevalidateDetectsChanges(t *testing.T) {
	moveAway := func(t *testing.T, dir string) {
		require.NoError(t, os.Rename(dir, dir+".moved"))
	}
	replace := func(t *testing.T, dir string) {
		moveAway(t, dir)
		require.NoError(t, os.Mkdir(dir, 0o700))
	}
	testCases := []struct {
		name    string
		trusted bool
		change  func(t *testing.T, dir string)
		want    error
	}{
		{name: "When an untrusted anchor's directory is moved away it should report ErrAnchorMoved", change: moveAway},
		{name: "When an untrusted anchor's directory is replaced it should report ErrAnchorMoved", change: replace},
		{name: "When a trusted anchor's directory is replaced by another directory it should report ErrAnchorMoved", trusted: true, change: replace},
		{
			name:    "When a trusted anchor's directory becomes writable by others it should report ErrAnchorMoved",
			trusted: true,
			change:  func(t *testing.T, dir string) { require.NoError(t, os.Chmod(dir, 0o777)) },
			want:    errors.ErrUntrustedDirectory,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			rw, root := anchorFixture(t)
			dir := filepath.Join(root, "data", "x")
			require.NoError(os.Mkdir(dir, 0o700))
			a, err := rw.OpenAnchor("/data/x", DirOptions{Trusted: tc.trusted})
			require.NoError(err)
			defer a.Close()
			require.NoError(a.Revalidate())
			tc.change(t, dir)
			err = a.Revalidate()
			require.ErrorIs(err, errors.ErrAnchorMoved)
			if tc.want != nil {
				require.ErrorIs(err, tc.want)
			}
		})
	}
}

func TestAnchorAfterClose(t *testing.T) {
	rw, root := anchorFixture(t)
	dataDir := filepath.Join(root, "data")
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "a"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "a", "f"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "f"), []byte("x"), 0o600))
	src := openTrusted(t, rw, "/data/a")
	before := treeSnapshot(t, dataDir)

	data, err := rw.OpenAnchor("/data", DirOptions{Trusted: true})
	require.NoError(t, err)
	require.NoError(t, data.Close())
	t.Run("When the anchor is closed again it should succeed", func(t *testing.T) {
		require.NoError(t, data.Close())
	})

	ctx := context.Background()
	walk := func(rel string) error {
		return data.Walk(ctx, rel, WalkBounds{MaxEntries: 10, MaxDepth: 4, MaxNameBytes: 255}, func(string, EntryInfo) error { return nil })
	}
	ops := []struct {
		name string
		run  func() error
	}{
		{name: "Lstat of the anchor", run: func() error { _, err := data.Lstat("."); return err }},
		{name: "Lstat of a name", run: func() error { _, err := data.Lstat("f"); return err }},
		{name: "Lstat of a path", run: func() error { _, err := data.Lstat("a/f"); return err }},
		{name: "OpenFile of a name", run: func() error { _, err := data.OpenFile("f"); return err }},
		{name: "OpenFile of a path", run: func() error { _, err := data.OpenFile("a/f"); return err }},
		{name: "ReadFile", run: func() error { _, err := data.ReadFile("f", 10); return err }},
		{name: "Hash", run: func() error { _, err := data.Hash(ctx, "f", CopyBounds{MaxBytes: 10}); return err }},
		{name: "Walk of the anchor", run: func() error { return walk(".") }},
		{name: "Walk of a name", run: func() error { return walk("a") }},
		{name: "GetXattr of the anchor", run: func() error { _, _, err := data.GetXattr(".", "user.flightctl", 64); return err }},
		{name: "GetXattr of a name", run: func() error { _, _, err := data.GetXattr("f", "user.flightctl", 64); return err }},
		{name: "OpenDir", run: func() error { _, err := data.OpenDir("a", DirOptions{}); return err }},
		{name: "OpenDir with creation", run: func() error { _, err := data.OpenDir("new", DirOptions{Create: true, Perm: 0o700}); return err }},
		{name: "WriteFileExclusive", run: func() error { return data.WriteFileExclusive("new", []byte("x"), 0o600) }},
		{name: "ReplaceFileDurable", run: func() error { return data.ReplaceFileDurable("f", []byte("y"), 0o600) }},
		{name: "CopyFrom", run: func() error {
			_, err := data.CopyFrom(ctx, src, "f", "new", 0o600, CopyBounds{MaxBytes: 10})
			return err
		}},
		{name: "RenameNoReplace", run: func() error { return data.RenameNoReplace("f", data, "g") }},
		{name: "RemoveTree", run: func() error { _, err := data.RemoveTree(ctx, "a", validRemoveBounds()); return err }},
		{name: "Sync", run: data.Sync},
		{name: "AvailableBytes", run: func() error { _, err := data.AvailableBytes(); return err }},
		{name: "ReadOnlyMount", run: func() error { _, err := data.ReadOnlyMount(); return err }},
	}
	for _, op := range ops {
		t.Run("When the anchor is closed "+op.name+" should fail instead of using a stale descriptor", func(t *testing.T) {
			require.ErrorIs(t, op.run(), unix.EBADF)
		})
	}
	require.Equal(t, before, treeSnapshot(t, dataDir))
}

// removeFixture creates data/tree holding entries (a trailing slash makes a
// directory) and returns the anchor for data and the tree path.
func removeFixture(t *testing.T, sys *sysOps, entries ...string) (Anchor, string) {
	t.Helper()
	rw, root := anchorFixture(t)
	tree := filepath.Join(root, "data", "tree")
	require.NoError(t, os.Mkdir(tree, 0o700))
	for _, e := range entries {
		p := filepath.Join(tree, e)
		if strings.HasSuffix(e, "/") {
			require.NoError(t, os.MkdirAll(p, 0o700))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}
	if sys == nil {
		sys = defaultSysOps
	}
	return openWithSys(t, rw, "/data", sys), tree
}

func TestRemoveTreeBoundaries(t *testing.T) {
	ctx := context.Background()

	t.Run("When the name is not a single component it should fail without removing anything", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, nil, "f")
		res, err := data.RemoveTree(ctx, "tree/f", validRemoveBounds())
		require.ErrorIs(err, errors.ErrInvalidPath)
		require.Equal(RemoveError, res.Stop)
		requireExists(t, filepath.Join(tree, "f"))
	})
	t.Run("When the bounds are invalid it should fail without removing anything", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, nil, "f")
		for _, mutate := range []func(b *RemoveBounds){
			func(b *RemoveBounds) { b.MaxNodes = 0 },
			func(b *RemoveBounds) { b.MaxWork = 0 },
			func(b *RemoveBounds) { b.MaxDetach = -1 },
			func(b *RemoveBounds) { b.MaxDirRetries = -1 },
			func(b *RemoveBounds) { b.MaxDepth = 0 },
			func(b *RemoveBounds) { b.BatchSize = -1 },
		} {
			b := validRemoveBounds()
			mutate(&b)
			res, err := data.RemoveTree(ctx, "tree", b)
			require.Error(err, "%+v", b)
			require.Equal(RemoveError, res.Stop)
			require.Zero(res.Work)
		}
		requireExists(t, filepath.Join(tree, "f"))
	})
	t.Run("When the batch size is zero it should use the default batch size and complete", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, nil, "f", "sub/g")
		b := validRemoveBounds()
		b.BatchSize = 0
		res, err := data.RemoveTree(ctx, "tree", b)
		require.NoError(err)
		require.True(res.Complete)
		require.Equal(4, res.Removed)
		requireAbsent(t, tree)
	})
	t.Run("When the context is already canceled it should stop before doing any work", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, nil, "f")
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		res, err := data.RemoveTree(canceled, "tree", validRemoveBounds())
		require.ErrorIs(err, context.Canceled)
		require.Equal(RemoveCanceled, res.Stop)
		require.Zero(res.Work)
		requireExists(t, filepath.Join(tree, "f"))
	})
	t.Run("When the name is a regular file it should remove only that file", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, nil, "f")
		plain := filepath.Join(filepath.Dir(tree), "plain")
		require.NoError(os.WriteFile(plain, []byte("x"), 0o600))
		res, err := data.RemoveTree(ctx, "plain", validRemoveBounds())
		require.NoError(err)
		require.True(res.Complete)
		require.Equal(1, res.Removed)
		requireAbsent(t, plain)
		requireExists(t, filepath.Join(tree, "f"))
	})
	t.Run("When the name is a symlink to a directory it should remove the link and keep its target", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, nil, "f")
		link := filepath.Join(filepath.Dir(tree), "link")
		require.NoError(os.Symlink(tree, link))
		res, err := data.RemoveTree(ctx, "link", validRemoveBounds())
		require.NoError(err)
		require.Equal(1, res.Removed)
		requireAbsent(t, link)
		requireExists(t, filepath.Join(tree, "f"))
	})
	t.Run("When the named directory is on another device it should stop without removing it", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, sysWith(func(s *sysOps) { s.fstatat = statAs("tree", otherDevice) }), "f")
		res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
		require.ErrorIs(err, errors.ErrCrossDevice)
		require.Equal(RemoveCrossDevice, res.Stop)
		requireExists(t, filepath.Join(tree, "f"))
	})
	t.Run("When the named directory disappears before it is opened it should complete without counting it", func(t *testing.T) {
		require := require.New(t)
		var tree string
		data, tree := removeFixture(t, sysWith(func(s *sysOps) {
			s.fstatat = func(dirfd int, p string, st *unix.Stat_t, flags int) error {
				err := unix.Fstatat(dirfd, p, st, flags)
				if err == nil && p == "tree" {
					require.NoError(os.Remove(tree))
				}
				return err
			}
		}))
		res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
		require.NoError(err)
		require.True(res.Complete)
		require.Zero(res.Removed)
		requireAbsent(t, tree)
	})
	t.Run("When an entry cannot be inspected it should stop with an error and keep it", func(t *testing.T) {
		require := require.New(t)
		for _, name := range []string{"tree", "f"} {
			data, tree := removeFixture(t, sysWith(func(s *sysOps) { s.fstatat = statFails(name, unix.EIO) }), "f")
			res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
			require.ErrorIs(err, unix.EIO, name)
			require.Equal(RemoveError, res.Stop, name)
			require.NotEmpty(res.LastErr, name)
			requireExists(t, filepath.Join(tree, "f"))
		}
	})
	t.Run("When an entry disappears while it is inspected it should skip it and complete", func(t *testing.T) {
		require := require.New(t)
		var tree string
		data, tree := removeFixture(t, sysWith(func(s *sysOps) {
			s.fstatat = func(dirfd int, p string, st *unix.Stat_t, flags int) error {
				if p == "gone" {
					require.NoError(os.Remove(filepath.Join(tree, "gone")))
				}
				return unix.Fstatat(dirfd, p, st, flags)
			}
		}), "gone", "f")
		res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
		require.NoError(err)
		require.True(res.Complete)
		require.Equal(2, res.Removed)
		requireAbsent(t, tree)
	})
	t.Run("When an entry is replaced by a file after inspection it should stop with an error and keep it", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, sysWith(func(s *sysOps) { s.fstatat = statAs("f", asDirectory) }), "f")
		res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
		require.ErrorIs(err, unix.ENOTDIR)
		require.Equal(RemoveError, res.Stop)
		requireExists(t, filepath.Join(tree, "f"))
	})
	t.Run("When a directory is swapped after inspection it should stop with an error and keep it", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, sysWith(func(s *sysOps) { s.fstatat = statAs("sub", otherInode) }), "sub/f")
		res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
		require.Error(err)
		require.Contains(err.Error(), "changed during removal")
		require.Equal(RemoveError, res.Stop)
		requireExists(t, filepath.Join(tree, "sub", "f"))
	})
	t.Run("When unlinking an entry fails it should stop with an error and keep it", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, sysWith(func(s *sysOps) {
			s.unlinkat = func(dirfd int, p string, flags int) error {
				if p == "f" {
					return unix.EPERM
				}
				return unix.Unlinkat(dirfd, p, flags)
			}
		}), "f")
		res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
		require.ErrorIs(err, unix.EPERM)
		require.Equal(RemoveError, res.Stop)
		requireExists(t, filepath.Join(tree, "f"))
	})
	t.Run("When removing a directory fails it should stop with an error and keep it", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, sysWith(func(s *sysOps) {
			s.unlinkat = func(dirfd int, p string, flags int) error {
				if p == "sub" && flags&unix.AT_REMOVEDIR != 0 {
					return unix.EBUSY
				}
				return unix.Unlinkat(dirfd, p, flags)
			}
		}), "sub/")
		res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
		require.ErrorIs(err, unix.EBUSY)
		require.Equal(RemoveError, res.Stop)
		requireExists(t, filepath.Join(tree, "sub"))
	})
	t.Run("When a directory is removed concurrently it should complete without counting it", func(t *testing.T) {
		require := require.New(t)
		data, tree := removeFixture(t, sysWith(func(s *sysOps) {
			s.unlinkat = func(dirfd int, p string, flags int) error {
				if p == "sub" && flags&unix.AT_REMOVEDIR != 0 {
					require.NoError(unix.Unlinkat(dirfd, p, flags))
					return unix.ENOENT
				}
				return unix.Unlinkat(dirfd, p, flags)
			}
		}), "sub/")
		res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
		require.NoError(err)
		require.True(res.Complete)
		require.Equal(1, res.Removed)
		requireAbsent(t, tree)
	})
	t.Run("When the context is canceled after a directory is emptied it should stop before removing the directory", func(t *testing.T) {
		require := require.New(t)
		canceled, cancel := context.WithCancel(ctx)
		defer cancel()
		data, tree := removeFixture(t, sysWith(func(s *sysOps) {
			s.unlinkat = func(dirfd int, p string, flags int) error {
				err := unix.Unlinkat(dirfd, p, flags)
				if p == "f" {
					cancel()
				}
				return err
			}
		}), "f")
		res, err := data.RemoveTree(canceled, "tree", validRemoveBounds())
		require.ErrorIs(err, context.Canceled)
		require.Equal(RemoveCanceled, res.Stop)
		require.Equal(1, res.Removed)
		requireExists(t, tree)
	})
}

func TestRemoveTreeWorkBudgetChecks(t *testing.T) {
	testCases := []struct {
		name     string
		entries  []string
		maxWork  int
		maxDepth int
		refill   bool
		check    func(t *testing.T, tree string, res RemoveResult)
	}{
		{
			name:    "When the budget ends before the named directory is opened it should stop without removing anything",
			maxWork: 1,
			check: func(t *testing.T, tree string, res RemoveResult) {
				require.Zero(t, res.Removed)
				requireExists(t, tree)
			},
		},
		{
			name:    "When the budget ends before an entry is inspected it should stop and keep the entry",
			entries: []string{"f"},
			maxWork: 4,
			check: func(t *testing.T, tree string, res RemoveResult) {
				require.Zero(t, res.Removed)
				requireExists(t, filepath.Join(tree, "f"))
			},
		},
		{
			name:    "When the budget ends before a refilled directory is read again it should stop after one retry",
			maxWork: 6,
			refill:  true,
			check: func(t *testing.T, tree string, res RemoveResult) {
				require.Equal(t, 1, res.Retries)
				requireExists(t, filepath.Join(tree, "late"))
			},
		},
		{
			name:     "When the budget ends before a directory deeper than the handle budget is detached it should keep it in place",
			entries:  []string{"sub/"},
			maxWork:  5,
			maxDepth: 1,
			check: func(t *testing.T, tree string, res RemoveResult) {
				require.Empty(t, res.Detached)
				requireExists(t, filepath.Join(tree, "sub"))
				entries, err := os.ReadDir(filepath.Dir(tree))
				require.NoError(t, err)
				require.Len(t, entries, 1)
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			var tree string
			refilled := false
			data, tree := removeFixture(t, sysWith(func(s *sysOps) {
				s.unlinkat = func(dirfd int, p string, flags int) error {
					if tc.refill && !refilled && p == "tree" && flags&unix.AT_REMOVEDIR != 0 {
						refilled = true
						require.NoError(os.WriteFile(filepath.Join(tree, "late"), nil, 0o600))
					}
					return unix.Unlinkat(dirfd, p, flags)
				}
			}), tc.entries...)
			b := validRemoveBounds()
			b.MaxWork = tc.maxWork
			if tc.maxDepth > 0 {
				b.MaxDepth = tc.maxDepth
			}
			res, err := data.RemoveTree(context.Background(), "tree", b)
			require.ErrorIs(err, errors.ErrWalkLimitExceeded)
			require.Equal(RemoveWorkBudget, res.Stop)
			require.False(res.Complete)
			require.LessOrEqual(res.Work, tc.maxWork)
			tc.check(t, tree, res)
		})
	}
}

func TestRemoveTreeDetachOutcomes(t *testing.T) {
	rename := func(dirfd int, oldpath string, newdirfd int, newpath string, flags uint) error {
		return unix.Renameat2(dirfd, oldpath, newdirfd, newpath, flags)
	}
	testCases := []struct {
		name   string
		rename func(tree string, calls int) error
		check  func(t *testing.T, data string, tree string, res RemoveResult, err error)
	}{
		{
			name: "When the first quarantine name is taken it should detach under another name",
			rename: func(_ string, calls int) error {
				if calls == 1 {
					return unix.EEXIST
				}
				return nil
			},
			check: func(t *testing.T, data, tree string, res RemoveResult, err error) {
				require.NoError(t, err)
				require.True(t, res.Complete)
				require.Len(t, res.Detached, 1)
				requireExists(t, filepath.Join(data, res.Detached[0], "f"))
				requireAbsent(t, tree)
			},
		},
		{
			name:   "When no quarantine name is free it should stop with an error and keep the directory",
			rename: func(string, int) error { return unix.EEXIST },
			check: func(t *testing.T, _, tree string, res RemoveResult, err error) {
				require.Error(t, err)
				require.Contains(t, err.Error(), "no unused quarantine name")
				require.Equal(t, RemoveError, res.Stop)
				require.Empty(t, res.Detached)
				requireExists(t, filepath.Join(tree, "sub", "f"))
			},
		},
		{
			name:   "When the quarantine is on another device it should stop with ErrCrossDevice and keep the directory",
			rename: func(string, int) error { return unix.EXDEV },
			check: func(t *testing.T, _, tree string, res RemoveResult, err error) {
				require.ErrorIs(t, err, errors.ErrCrossDevice)
				require.Equal(t, RemoveCrossDevice, res.Stop)
				requireExists(t, filepath.Join(tree, "sub", "f"))
			},
		},
		{
			name: "When the directory disappears before it is detached it should continue and complete",
			rename: func(tree string, _ int) error {
				if err := os.RemoveAll(filepath.Join(tree, "sub")); err != nil {
					return err
				}
				return unix.ENOENT
			},
			check: func(t *testing.T, _, tree string, res RemoveResult, err error) {
				require.NoError(t, err)
				require.True(t, res.Complete)
				require.Empty(t, res.Detached)
				require.Equal(t, 1, res.Removed)
				requireAbsent(t, tree)
			},
		},
		{
			name:   "When the rename fails it should stop with an error and keep the directory",
			rename: func(string, int) error { return unix.EPERM },
			check: func(t *testing.T, _, tree string, res RemoveResult, err error) {
				require.ErrorIs(t, err, unix.EPERM)
				require.Equal(t, RemoveError, res.Stop)
				requireExists(t, filepath.Join(tree, "sub", "f"))
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var tree string
			calls := 0
			data, tree := removeFixture(t, sysWith(func(s *sysOps) {
				s.renameat2 = func(dirfd int, oldpath string, newdirfd int, newpath string, flags uint) error {
					calls++
					if err := tc.rename(tree, calls); err != nil {
						return err
					}
					return rename(dirfd, oldpath, newdirfd, newpath, flags)
				}
			}), "sub/f")
			b := validRemoveBounds()
			b.MaxDepth = 1
			res, err := data.RemoveTree(context.Background(), "tree", b)
			tc.check(t, filepath.Dir(tree), tree, res, err)
		})
	}
}

func TestRemoveTreeLastError(t *testing.T) {
	t.Run("When the failure description is longer than 256 bytes it should truncate LastErr", func(t *testing.T) {
		require := require.New(t)
		long := strings.Repeat("n", 255)
		data, _ := removeFixture(t, sysWith(func(s *sysOps) { s.fstatat = statFails(long, unix.EIO) }), long)
		res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
		require.ErrorIs(err, unix.EIO)
		require.Len(res.LastErr, 256)
		require.True(strings.HasPrefix(res.LastErr, `stat "nnn`))
		require.Contains(err.Error(), res.LastErr)
	})
	t.Run("When the failure description has control characters it should replace them in LastErr", func(t *testing.T) {
		require := require.New(t)
		data, _ := removeFixture(t, sysWith(func(s *sysOps) {
			s.unlinkat = func(dirfd int, p string, flags int) error {
				if p == "f" {
					return errors.New("busy\x00\nretry")
				}
				return unix.Unlinkat(dirfd, p, flags)
			}
		}), "f")
		res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
		require.Error(err)
		require.Contains(res.LastErr, "busy??retry")
		require.NotContains(res.LastErr, "\n")
	})
}

func TestAnchorHashChecksFileCapabilities(t *testing.T) {
	capabilityRead := func(results ...error) func(fd int, attr string, dest []byte) (int, error) {
		calls := 0
		return func(fd int, attr string, dest []byte) (int, error) {
			if attr != capabilityXattr {
				return unix.Fgetxattr(fd, attr, dest)
			}
			res := results[min(calls, len(results)-1)]
			calls++
			if res == nil {
				return copy(dest, []byte{0, 0, 0, 2}), nil
			}
			return 0, res
		}
	}
	testCases := []struct {
		name    string
		xattr   func(fd int, attr string, dest []byte) (int, error)
		scan    bool
		want    []error
		notWant []error
	}{
		{
			name:    "When the hashed file carries file capabilities it should refuse it",
			xattr:   capabilityRead(nil),
			want:    []error{errors.ErrFileCapability},
			notWant: []error{errors.ErrFileCapabilityUnknown},
		},
		{
			name:    "When the capabilities of the hashed file cannot be read it should refuse it as unknown with the cause",
			xattr:   capabilityRead(unix.EIO),
			want:    []error{errors.ErrFileCapabilityUnknown, unix.EIO},
			notWant: []error{errors.ErrFileCapability},
		},
		{
			name:    "When reading the capabilities of the hashed file is denied it should refuse it as unknown with the cause",
			xattr:   capabilityRead(unix.EACCES),
			want:    []error{errors.ErrFileCapabilityUnknown, unix.EACCES},
			notWant: []error{errors.ErrFileCapability},
		},
		{
			name:    "When an earlier capability read succeeded but the read while hashing fails it should refuse it as unknown with the cause",
			xattr:   capabilityRead(unix.ENODATA, unix.EIO),
			scan:    true,
			want:    []error{errors.ErrFileCapabilityUnknown, unix.EIO},
			notWant: []error{errors.ErrFileCapability},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			rw, root := anchorFixture(t)
			require.NoError(os.WriteFile(filepath.Join(root, "data", "f"), []byte("payload"), 0o600))
			data := openWithSys(t, rw, "/data", sysWith(func(s *sysOps) { s.fgetxattr = tc.xattr }))
			if tc.scan {
				_, state, err := data.GetXattr("f", capabilityXattr, capabilityXattrLimit)
				require.NoError(err)
				require.Equal(XattrAbsent, state)
			}
			_, err := data.Hash(context.Background(), "f", CopyBounds{MaxBytes: 64})
			for _, e := range tc.want {
				require.ErrorIs(err, e)
			}
			for _, e := range tc.notWant {
				require.NotErrorIs(err, e)
			}
		})
	}
}
