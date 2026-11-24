package fileio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func validRemoveBounds() RemoveBounds {
	return RemoveBounds{MaxNodes: 61443, MaxWork: 1 << 20, MaxDetach: 64, MaxDirRetries: 4, MaxDepth: 16, BatchSize: 128}
}

func anchorFixture(t *testing.T) (ReadWriter, string) {
	t.Helper()
	rw, root := newRootReader(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data"), 0o700))
	return rw, root
}

func openTrusted(t *testing.T, rw ReadWriter, path string) Anchor {
	t.Helper()
	a, err := rw.OpenAnchor(path, DirOptions{Trusted: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func openWithSys(t *testing.T, rw ReadWriter, path string, sys *sysOps) Anchor {
	t.Helper()
	a, err := openAnchorWithSys(rw, path, DirOptions{Trusted: true}, sys)
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func treeSnapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		out = append(out, fmt.Sprintf("%s %o %d", p, info.Mode(), st.Ino))
		return nil
	}))
	return out
}

func sentinelDir(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "sentinel")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep"), []byte("keep"), 0o600))
	return dir
}

func countNodes(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(string, fs.DirEntry, error) error { n++; return nil })
	return n
}

// buildDisjointTree creates root/manifest.json and files pairwise-disjoint
// payload paths with depth components each (payload counted), so the tree
// holds 3 + (depth-1)*files nodes.
func buildDisjointTree(t *testing.T, root string, files, depth int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "payload"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "manifest.json"), []byte("{}"), 0o600))
	for i := 0; i < files; i++ {
		parts := []string{root, "payload", "d" + strconv.Itoa(i)}
		for j := 3; j < depth; j++ {
			parts = append(parts, "x")
		}
		dir := filepath.Join(parts...)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600))
	}
}

func TestAnchorTrustChain(t *testing.T) {
	testCases := []struct {
		name  string
		setup func(t *testing.T, root string)
		open  func(rw ReadWriter) error
	}{
		{
			name: "When a component of the anchored path is a symlink it should refuse to open it",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.Symlink(filepath.Join(root, "sentinel"), filepath.Join(root, "data", "link")))
			},
			open: func(rw ReadWriter) error {
				_, err := rw.OpenAnchor("/data/link", DirOptions{Trusted: true})
				return err
			},
		},
		{
			name: "When the firmware directory is a symlink it should refuse it",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.Symlink(filepath.Join(root, "sentinel"), filepath.Join(root, "data", "firmware")))
			},
			open: func(rw ReadWriter) error {
				a, err := rw.OpenAnchor("/data", DirOptions{Trusted: true})
				if err != nil {
					return err
				}
				defer a.Close()
				_, err = a.OpenDir("firmware", DirOptions{Create: true, Perm: 0o700, Trusted: true})
				return err
			},
		},
		{
			name: "When the updaters directory is a symlink it should refuse it",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "data", "firmware"), 0o700))
				require.NoError(t, os.Symlink(filepath.Join(root, "sentinel"), filepath.Join(root, "data", "firmware", "updaters")))
			},
			open: func(rw ReadWriter) error {
				a, err := rw.OpenAnchor("/data/firmware", DirOptions{Trusted: true})
				if err != nil {
					return err
				}
				defer a.Close()
				_, err = a.OpenDir("updaters", DirOptions{Create: true, Perm: 0o700, Trusted: true})
				return err
			},
		},
		{
			name: "When the evidence directory is group writable it should refuse it",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, "data", "firmware", "evidence")
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.Chmod(dir, 0o770))
			},
			open: func(rw ReadWriter) error {
				a, err := rw.OpenAnchor("/data/firmware", DirOptions{Trusted: true})
				if err != nil {
					return err
				}
				defer a.Close()
				_, err = a.OpenDir("evidence", DirOptions{Create: true, Perm: 0o700, Trusted: true})
				return err
			},
		},
		{
			name: "When an ancestor of the anchored path is world writable it should refuse to open it",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, "data", "open")
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "inner"), 0o700))
				require.NoError(t, os.Chmod(dir, 0o777))
			},
			open: func(rw ReadWriter) error {
				_, err := rw.OpenAnchor("/data/open/inner", DirOptions{Trusted: true})
				return err
			},
		},
		{
			name: "When the target is a regular file it should refuse it",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.WriteFile(filepath.Join(root, "data", "firmware"), []byte("x"), 0o600))
			},
			open: func(rw ReadWriter) error {
				a, err := rw.OpenAnchor("/data", DirOptions{Trusted: true})
				if err != nil {
					return err
				}
				defer a.Close()
				_, err = a.OpenDir("firmware", DirOptions{Create: true, Perm: 0o700, Trusted: true})
				return err
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			rw, root := anchorFixture(t)
			sentinel := sentinelDir(t, root)
			tc.setup(t, root)
			before := treeSnapshot(t, sentinel)
			err := tc.open(rw)
			require.Error(err)
			require.ErrorIs(err, errors.ErrUntrustedDirectory)
			require.Equal(before, treeSnapshot(t, sentinel))
		})
	}
}

func TestAnchorForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing ownership requires root")
	}
	require := require.New(t)
	rw, root := anchorFixture(t)
	dir := filepath.Join(root, "data", "foreign")
	require.NoError(os.MkdirAll(dir, 0o700))
	require.NoError(os.Chown(dir, 12345, 12345))
	_, err := rw.OpenAnchor("/data/foreign", DirOptions{Trusted: true})
	require.ErrorIs(err, errors.ErrUntrustedDirectory)
}

func TestAnchorOwnership(t *testing.T) {
	current := Ownership{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())} //nolint:gosec // effective IDs are nonnegative
	owner := current
	if current.UID == 0 {
		owner = Ownership{UID: 65534, GID: 65534}
	}

	for _, tc := range []struct {
		name string
		run  func(t *testing.T, data Anchor, root string)
	}{
		{
			name: "When a directory is created it should use the requested owner and preserve existing ownership",
			run: func(t *testing.T, data Anchor, root string) {
				dir, err := data.OpenDir("new", DirOptions{Create: true, Owner: &owner})
				require.NoError(t, err)
				require.NoError(t, dir.Close())
				dir, err = data.OpenDir("new", DirOptions{Create: true, Owner: &current})
				require.NoError(t, err)
				require.NoError(t, dir.Close())
				assertOwnership(t, filepath.Join(root, "data", "new"), owner, 0o700)
			},
		},
		{
			name: "When replacing a file it should set ownership and mode before publication",
			run: func(t *testing.T, data Anchor, root string) {
				file := filepath.Join(root, "data", "auth.json")
				require.NoError(t, data.ReplaceFileDurable("auth.json", []byte("first"), 0o600, WithOwner(owner)))
				assertOwnership(t, file, owner, 0o600)
				require.NoError(t, data.ReplaceFileDurable("auth.json", []byte("second"), 0o640))
				assertOwnership(t, file, current, 0o640)
			},
		},
		{
			name: "When an owned directory path is a symlink it should reject it",
			run: func(t *testing.T, data Anchor, root string) {
				outside := sentinelDir(t, root)
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "data", "link")))
				_, err := data.OpenDir("link", DirOptions{Create: true, Owner: &owner})
				require.ErrorIs(t, err, errors.ErrUnsafeFileType)
				assertOwnership(t, outside, current, 0o700)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rw, root := anchorFixture(t)
			tc.run(t, openTrusted(t, rw, "/data"), root)
		})
	}
}

func assertOwnership(t *testing.T, path string, owner Ownership, mode fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	stat := info.Sys().(*syscall.Stat_t)
	require.Equal(t, owner, Ownership{UID: stat.Uid, GID: stat.Gid})
	require.Equal(t, mode, info.Mode().Perm())
}

func TestAnchorRemoveFile(t *testing.T) {
	rw, root := anchorFixture(t)
	data := openTrusted(t, rw, "/data")
	require.NoError(t, data.WriteFileExclusive("auth.json", []byte("old"), 0o600))
	require.NoError(t, data.RemoveFile("auth.json"))
	require.NoFileExists(t, filepath.Join(root, "data", "auth.json"))
	require.NoError(t, data.RemoveFile("auth.json"))
	require.NoError(t, os.Mkdir(filepath.Join(root, "data", "auth.json"), 0o700))
	keep := filepath.Join(root, "data", "auth.json", "keep")
	require.NoError(t, os.WriteFile(keep, []byte("keep"), 0o600))
	require.Error(t, data.RemoveFile("auth.json"))
	require.FileExists(t, keep)
	require.ErrorIs(t, data.RemoveFile("../auth.json"), errors.ErrInvalidPath)
}

func TestAnchorOpenDir(t *testing.T) {
	t.Run("When the directory is absent it should create it with the requested mode and open it", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		data := openTrusted(t, rw, "/data")
		fw, err := data.OpenDir("firmware", DirOptions{Create: true, Perm: 0o700, Trusted: true})
		require.NoError(err)
		defer fw.Close()
		info, err := os.Lstat(filepath.Join(root, "data", "firmware"))
		require.NoError(err)
		require.True(info.IsDir())
		require.Equal(fs.FileMode(0o700), info.Mode().Perm())
		require.Equal("/data/firmware", fw.Path())
	})
	t.Run("When the directory exists and creation is exclusive it should fail and leave it unchanged", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		dir := filepath.Join(root, "data", "staging")
		require.NoError(os.Mkdir(dir, 0o700))
		require.NoError(os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o600))
		before := treeSnapshot(t, dir)
		data := openTrusted(t, rw, "/data")
		_, err := data.OpenDir("staging", DirOptions{Create: true, Exclusive: true, Perm: 0o700, Trusted: true})
		require.ErrorIs(err, errors.ErrDestinationExists)
		require.ErrorIs(err, fs.ErrExist)
		require.Equal(before, treeSnapshot(t, dir))
	})
	t.Run("When the name is a symlink and creation is exclusive it should fail without following it", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		sentinel := sentinelDir(t, root)
		require.NoError(os.Symlink(sentinel, filepath.Join(root, "data", "staging")))
		before := treeSnapshot(t, sentinel)
		data := openTrusted(t, rw, "/data")
		_, err := data.OpenDir("staging", DirOptions{Create: true, Exclusive: true, Perm: 0o700, Trusted: true})
		require.Error(err)
		require.Equal(before, treeSnapshot(t, sentinel))
	})
	t.Run("When the directory exists and creation is not exclusive it should open it without changing it", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		dir := filepath.Join(root, "data", "firmware")
		require.NoError(os.Mkdir(dir, 0o700))
		info, err := os.Lstat(dir)
		require.NoError(err)
		data := openTrusted(t, rw, "/data")
		fw, err := data.OpenDir("firmware", DirOptions{Create: true, Perm: 0o755, Trusted: true})
		require.NoError(err)
		defer fw.Close()
		after, err := os.Lstat(dir)
		require.NoError(err)
		require.Equal(info.Mode(), after.Mode())
	})
	t.Run("When the name is not a single component it should fail", func(t *testing.T) {
		require := require.New(t)
		rw, _ := anchorFixture(t)
		data := openTrusted(t, rw, "/data")
		for _, name := range []string{"", ".", "..", "a/b", "/abs", "a\x00b"} {
			_, err := data.OpenDir(name, DirOptions{Create: true, Perm: 0o700})
			require.Error(err, name)
		}
	})
}

func TestAnchorReadOnly(t *testing.T) {
	require := require.New(t)
	rw, root := anchorFixture(t)
	require.NoError(os.MkdirAll(filepath.Join(root, "data", "src", "dir"), 0o700))
	require.NoError(os.WriteFile(filepath.Join(root, "data", "src", "file"), []byte("content"), 0o600))
	before := treeSnapshot(t, filepath.Join(root, "data", "src"))
	src, err := rw.OpenAnchor("/data/src", DirOptions{ReadOnly: true})
	require.NoError(err)
	defer src.Close()
	dst := openTrusted(t, rw, "/data")

	ctx := context.Background()
	_, err = src.OpenDir("new", DirOptions{Create: true, Perm: 0o700})
	require.ErrorIs(err, errors.ErrReadOnlyAnchor)
	require.ErrorIs(src.WriteFileExclusive("new", []byte("x"), 0o600), errors.ErrReadOnlyAnchor)
	require.ErrorIs(src.ReplaceFileDurable("file", []byte("x"), 0o600), errors.ErrReadOnlyAnchor)
	require.ErrorIs(src.RenameNoReplace("file", src, "moved"), errors.ErrReadOnlyAnchor)
	_, err = src.RemoveTree(ctx, "dir", validRemoveBounds())
	require.ErrorIs(err, errors.ErrReadOnlyAnchor)
	_, err = src.CopyFrom(ctx, dst, "x", "y", 0o600, CopyBounds{MaxBytes: 10})
	require.ErrorIs(err, errors.ErrReadOnlyAnchor)
	sub, err := src.OpenDir("dir", DirOptions{})
	require.NoError(err)
	defer sub.Close()
	require.ErrorIs(sub.WriteFileExclusive("new", []byte("x"), 0o600), errors.ErrReadOnlyAnchor)

	data, err := src.ReadFile("file", 100)
	require.NoError(err)
	require.Equal([]byte("content"), data)
	require.Equal(before, treeSnapshot(t, filepath.Join(root, "data", "src")))
}

func TestAnchorWriteAndRename(t *testing.T) {
	t.Run("When writing exclusively over an existing or symlinked name it should fail and keep the target", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		sentinel := sentinelDir(t, root)
		require.NoError(os.WriteFile(filepath.Join(root, "data", "existing"), []byte("old"), 0o600))
		require.NoError(os.Symlink(filepath.Join(sentinel, "keep"), filepath.Join(root, "data", "link")))
		before := treeSnapshot(t, sentinel)
		data := openTrusted(t, rw, "/data")
		require.Error(data.WriteFileExclusive("existing", []byte("new"), 0o400))
		require.Error(data.WriteFileExclusive("link", []byte("new"), 0o400))
		content, err := os.ReadFile(filepath.Join(root, "data", "existing"))
		require.NoError(err)
		require.Equal([]byte("old"), content)
		require.Equal(before, treeSnapshot(t, sentinel))
	})
	t.Run("When writing exclusively a new name it should create the file with the exact mode", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		data := openTrusted(t, rw, "/data")
		require.NoError(data.WriteFileExclusive("manifest.json", []byte("{}"), 0o400))
		info, err := os.Lstat(filepath.Join(root, "data", "manifest.json"))
		require.NoError(err)
		require.Equal(fs.FileMode(0o400), info.Mode().Perm())
	})
	t.Run("When replacing a file durably it should leave only the new content and no temporary files", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		data := openTrusted(t, rw, "/data")
		require.NoError(data.ReplaceFileDurable("retained.json", []byte("one"), 0o600))
		require.NoError(data.ReplaceFileDurable("retained.json", []byte("two"), 0o600))
		content, err := os.ReadFile(filepath.Join(root, "data", "retained.json"))
		require.NoError(err)
		require.Equal([]byte("two"), content)
		entries, err := os.ReadDir(filepath.Join(root, "data"))
		require.NoError(err)
		require.Len(entries, 1)
	})
	t.Run("When the rename destination exists it should fail with ErrDestinationExists and keep both entries", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		sentinel := sentinelDir(t, root)
		require.NoError(os.Mkdir(filepath.Join(root, "data", "staging"), 0o700))
		require.NoError(os.Symlink(sentinel, filepath.Join(root, "data", "final")))
		before := treeSnapshot(t, sentinel)
		data := openTrusted(t, rw, "/data")
		err := data.RenameNoReplace("staging", data, "final")
		require.ErrorIs(err, errors.ErrDestinationExists)
		target, err := os.Readlink(filepath.Join(root, "data", "final"))
		require.NoError(err)
		require.Equal(sentinel, target)
		_, err = os.Lstat(filepath.Join(root, "data", "staging"))
		require.NoError(err)

		require.NoError(data.RenameNoReplace("final", data, ".quarantine-1"))
		info, err := os.Lstat(filepath.Join(root, "data", ".quarantine-1"))
		require.NoError(err)
		require.Equal(fs.ModeSymlink, info.Mode().Type())
		require.Equal(before, treeSnapshot(t, sentinel))
		require.NoError(data.RenameNoReplace("staging", data, "final"))
	})
	t.Run("When the rename target directory is a wrapped anchor it should rename into it", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		require.NoError(os.Mkdir(filepath.Join(root, "data", "staging"), 0o700))
		data := openTrusted(t, rw, "/data")
		require.NoError(data.RenameNoReplace("staging", wrappedAnchor{Anchor: data}, "final"))
		_, err := os.Lstat(filepath.Join(root, "data", "final"))
		require.NoError(err)
	})
}

type wrappedAnchor struct{ Anchor }

func (w wrappedAnchor) Unwrap() Anchor { return w.Anchor }

func TestAnchorReplacementDuringOperation(t *testing.T) {
	require := require.New(t)
	rw, root := anchorFixture(t)
	sentinel := sentinelDir(t, root)
	updatersPath := filepath.Join(root, "data", "updaters")
	require.NoError(os.Mkdir(updatersPath, 0o700))
	updaters := openTrusted(t, rw, "/data/updaters")
	staging, err := updaters.OpenDir(".staging-1", DirOptions{Create: true, Exclusive: true, Perm: 0o700, Trusted: true})
	require.NoError(err)
	defer staging.Close()
	require.NoError(updaters.Revalidate())

	require.NoError(os.Rename(updatersPath, updatersPath+".moved"))
	require.NoError(os.Symlink(sentinel, updatersPath))
	before := treeSnapshot(t, sentinel)

	require.NoError(staging.WriteFileExclusive("manifest.json", []byte("{}"), 0o400))
	_, err = os.Lstat(filepath.Join(updatersPath+".moved", ".staging-1", "manifest.json"))
	require.NoError(err)
	require.Equal(before, treeSnapshot(t, sentinel))
	require.ErrorIs(updaters.Revalidate(), errors.ErrAnchorMoved)
	require.ErrorIs(staging.Revalidate(), errors.ErrAnchorMoved)
}

func TestAnchorWalk(t *testing.T) {
	t.Run("When a directory holds more entries than allowed it should stop after the limit", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		dir := filepath.Join(root, "data", "big")
		require.NoError(os.Mkdir(dir, 0o700))
		for i := 0; i < 20000; i++ {
			f, err := os.Create(filepath.Join(dir, strconv.Itoa(i)))
			require.NoError(err)
			require.NoError(f.Close())
		}
		data := openTrusted(t, rw, "/data")
		calls := 0
		err := data.Walk(context.Background(), "big", WalkBounds{MaxEntries: 100, MaxDepth: 4, MaxNameBytes: 255}, func(string, EntryInfo) error {
			calls++
			return nil
		})
		require.ErrorIs(err, errors.ErrWalkLimitExceeded)
		require.LessOrEqual(calls, 101)
	})
	t.Run("When the callback rejects an entry it should stop at once with that error", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		require.NoError(os.MkdirAll(filepath.Join(root, "data", "w", "a", "b"), 0o700))
		require.NoError(os.WriteFile(filepath.Join(root, "data", "w", "a", "b", "f"), []byte("x"), 0o600))
		data := openTrusted(t, rw, "/data")
		reject := fmt.Errorf("unexpected entry")
		seen := map[string]EntryInfo{}
		err := data.Walk(context.Background(), "w", WalkBounds{MaxEntries: 10, MaxDepth: 4, MaxNameBytes: 255}, func(rel string, e EntryInfo) error {
			seen[rel] = e
			if rel == "a/b/f" {
				return reject
			}
			return nil
		})
		require.ErrorIs(err, reject)
		require.True(seen["a"].IsDir())
		require.True(seen["a/b/f"].IsRegular())
		require.Equal(int64(1), seen["a/b/f"].Size)
	})
	t.Run("When the tree is deeper than allowed it should fail", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		require.NoError(os.MkdirAll(filepath.Join(root, "data", "w", "a", "b", "c"), 0o700))
		data := openTrusted(t, rw, "/data")
		err := data.Walk(context.Background(), "w", WalkBounds{MaxEntries: 10, MaxDepth: 2, MaxNameBytes: 255}, func(string, EntryInfo) error { return nil })
		require.ErrorIs(err, errors.ErrWalkLimitExceeded)
	})
	t.Run("When a name is longer than allowed it should fail", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		require.NoError(os.MkdirAll(filepath.Join(root, "data", "w"), 0o700))
		require.NoError(os.WriteFile(filepath.Join(root, "data", "w", strings.Repeat("n", 200)), nil, 0o600))
		data := openTrusted(t, rw, "/data")
		err := data.Walk(context.Background(), "w", WalkBounds{MaxEntries: 10, MaxDepth: 2, MaxNameBytes: 100}, func(string, EntryInfo) error { return nil })
		require.ErrorIs(err, errors.ErrWalkLimitExceeded)
	})
	t.Run("When the context is canceled between batches it should stop with the context error", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		dir := filepath.Join(root, "data", "w")
		require.NoError(os.MkdirAll(dir, 0o700))
		for i := 0; i < 300; i++ {
			require.NoError(os.WriteFile(filepath.Join(dir, strconv.Itoa(i)), nil, 0o600))
		}
		data := openTrusted(t, rw, "/data")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		err := data.Walk(ctx, "w", WalkBounds{MaxEntries: 1000, MaxDepth: 2, MaxNameBytes: 255, BatchSize: 128}, func(string, EntryInfo) error {
			calls++
			if calls == 1 {
				cancel()
			}
			return nil
		})
		require.ErrorIs(err, context.Canceled)
		require.Less(calls, 300)
	})
	t.Run("When a walked directory is a symlink it should report it without following it", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		sentinel := sentinelDir(t, root)
		require.NoError(os.MkdirAll(filepath.Join(root, "data", "w"), 0o700))
		require.NoError(os.Symlink(sentinel, filepath.Join(root, "data", "w", "link")))
		data := openTrusted(t, rw, "/data")
		var seen []string
		require.NoError(data.Walk(context.Background(), "w", WalkBounds{MaxEntries: 10, MaxDepth: 4, MaxNameBytes: 255}, func(rel string, e EntryInfo) error {
			seen = append(seen, rel)
			require.Equal(fs.ModeSymlink, e.Type)
			return nil
		}))
		require.Equal([]string{"link"}, seen)
	})
}

func TestAnchorCopyAndHash(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), 20000)
	sum := sha256.Sum256(payload)
	wantSHA := hex.EncodeToString(sum[:])

	setup := func(t *testing.T) (Anchor, Anchor, string) {
		t.Helper()
		rw, root := anchorFixture(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "data", "src"), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "data", "dst"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "data", "src", "file"), payload, 0o600))
		src, err := rw.OpenAnchor("/data/src", DirOptions{ReadOnly: true})
		require.NoError(t, err)
		t.Cleanup(func() { _ = src.Close() })
		return src, openTrusted(t, rw, "/data/dst"), root
	}

	t.Run("When the source is a regular file within the bound it should copy, hash and sync it with the requested mode", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t)
		d, err := dst.CopyFrom(context.Background(), src, "file", "copy", 0o500, CopyBounds{MaxBytes: int64(len(payload))})
		require.NoError(err)
		require.Equal(wantSHA, d.SHA256)
		require.Equal(int64(len(payload)), d.Size)
		require.Equal(fs.FileMode(0o500), d.Mode)
		info, err := os.Lstat(filepath.Join(root, "data", "dst", "copy"))
		require.NoError(err)
		require.Equal(fs.FileMode(0o500), info.Mode().Perm())
		got, err := os.ReadFile(filepath.Join(root, "data", "dst", "copy"))
		require.NoError(err)
		require.Equal(payload, got)

		h, err := dst.Hash(context.Background(), "copy", CopyBounds{MaxBytes: int64(len(payload))})
		require.NoError(err)
		require.Equal(wantSHA, h.SHA256)
		require.Equal(fs.FileMode(0o500), h.Mode)
	})
	t.Run("When the source is larger than the bound it should fail and remove the partial copy", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t)
		_, err := dst.CopyFrom(context.Background(), src, "file", "copy", 0o400, CopyBounds{MaxBytes: int64(len(payload)) - 1})
		require.ErrorIs(err, errors.ErrFileLimitExceeded)
		_, statErr := os.Lstat(filepath.Join(root, "data", "dst", "copy"))
		require.True(os.IsNotExist(statErr))
		_, err = src.Hash(context.Background(), "file", CopyBounds{MaxBytes: 10})
		require.ErrorIs(err, errors.ErrFileLimitExceeded)
	})
	t.Run("When the context is canceled mid-stream it should fail with the context error and remove the partial copy", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		decorated := &cancelingSource{Anchor: src, cancel: cancel, after: 2}
		_, err := dst.CopyFrom(ctx, decorated, "file", "copy", 0o400, CopyBounds{MaxBytes: int64(len(payload)), ChunkSize: 1024})
		require.ErrorIs(err, context.Canceled)
		_, statErr := os.Lstat(filepath.Join(root, "data", "dst", "copy"))
		require.True(os.IsNotExist(statErr))
	})
	t.Run("When the destination name exists it should fail and keep the existing file", func(t *testing.T) {
		require := require.New(t)
		src, dst, root := setup(t)
		require.NoError(os.WriteFile(filepath.Join(root, "data", "dst", "copy"), []byte("old"), 0o600))
		_, err := dst.CopyFrom(context.Background(), src, "file", "copy", 0o400, CopyBounds{MaxBytes: int64(len(payload))})
		require.Error(err)
		got, err := os.ReadFile(filepath.Join(root, "data", "dst", "copy"))
		require.NoError(err)
		require.Equal([]byte("old"), got)
	})

	unsafeCases := []struct {
		name    string
		prepare func(t *testing.T, dir string)
		src     func(a Anchor) Anchor
		want    error
	}{
		{
			name:    "When the source is a symlink it should refuse it",
			prepare: func(t *testing.T, dir string) { require.NoError(t, os.Symlink("file", filepath.Join(dir, "bad"))) },
			want:    errors.ErrUnsafeFileType,
		},
		{
			name: "When the source has a second hard link it should refuse it",
			prepare: func(t *testing.T, dir string) {
				require.NoError(t, os.Link(filepath.Join(dir, "file"), filepath.Join(dir, "bad")))
			},
			want: errors.ErrHardLinkedFile,
		},
		{
			name:    "When the source is a FIFO it should refuse it",
			prepare: func(t *testing.T, dir string) { require.NoError(t, unix.Mkfifo(filepath.Join(dir, "bad"), 0o600)) },
			want:    errors.ErrUnsafeFileType,
		},
		{
			name: "When the source is a socket it should refuse it",
			prepare: func(t *testing.T, dir string) {
				l, err := net.Listen("unix", filepath.Join(dir, "bad"))
				require.NoError(t, err)
				t.Cleanup(func() { _ = l.Close() })
			},
			want: errors.ErrUnsafeFileType,
		},
		{
			name:    "When the source is setuid it should refuse it",
			prepare: chmodFile(0o4755),
			want:    errors.ErrUnsafeFileMode,
		},
		{
			name:    "When the source is setgid it should refuse it",
			prepare: chmodFile(0o2755),
			want:    errors.ErrUnsafeFileMode,
		},
		{
			name:    "When the source is sticky it should refuse it",
			prepare: chmodFile(0o1755),
			want:    errors.ErrUnsafeFileMode,
		},
		{
			name: "When the source carries file capabilities it should refuse it",
			prepare: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "bad"), []byte("x"), 0o600))
			},
			src:  func(a Anchor) Anchor { return capabilitySource{Anchor: a} },
			want: errors.ErrFileCapability,
		},
	}
	for _, tc := range unsafeCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			src, dst, root := setup(t)
			tc.prepare(t, filepath.Join(root, "data", "src"))
			if tc.src != nil {
				src = tc.src(src)
			}
			_, err := dst.CopyFrom(context.Background(), src, "bad", "copy", 0o400, CopyBounds{MaxBytes: int64(len(payload))})
			require.ErrorIs(err, tc.want)
			_, statErr := os.Lstat(filepath.Join(root, "data", "dst", "copy"))
			require.True(os.IsNotExist(statErr))
			if tc.src == nil {
				_, err = src.Hash(context.Background(), "bad", CopyBounds{MaxBytes: int64(len(payload))})
				require.ErrorIs(err, tc.want)
			}
		})
	}
}

func chmodFile(mode fs.FileMode) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		p := filepath.Join(dir, "bad")
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		require.NoError(t, os.Chmod(p, mode&fs.ModePerm|toGoMode(mode)))
	}
}

func toGoMode(mode fs.FileMode) fs.FileMode {
	var m fs.FileMode
	if mode&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if mode&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if mode&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m
}

type cancelingSource struct {
	Anchor
	cancel context.CancelFunc
	after  int
}

func (c *cancelingSource) OpenFile(rel string) (File, error) {
	f, err := c.Anchor.OpenFile(rel)
	if err != nil {
		return nil, err
	}
	return &cancelingFile{File: f, c: c}, nil
}

type cancelingFile struct {
	File
	c     *cancelingSource
	reads int
}

func (f *cancelingFile) ReadAt(p []byte, off int64) (int, error) {
	f.reads++
	if f.reads == f.c.after {
		f.c.cancel()
	}
	return f.File.ReadAt(p, off)
}

type capabilitySource struct{ Anchor }

func (c capabilitySource) GetXattr(rel, attr string, limit int) ([]byte, XattrState, error) {
	if attr == "security.capability" {
		return []byte{1}, XattrPresent, nil
	}
	return c.Anchor.GetXattr(rel, attr, limit)
}

func TestAnchorGetXattr(t *testing.T) {
	t.Run("When the attribute is set it should return its value", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		p := filepath.Join(root, "data", "file")
		require.NoError(os.WriteFile(p, []byte("x"), 0o600))
		if err := unix.Setxattr(p, "user.flightctl", []byte("label"), 0); err != nil {
			t.Skipf("filesystem does not support user xattrs: %v", err)
		}
		data := openTrusted(t, rw, "/data")
		v, state, err := data.GetXattr("file", "user.flightctl", 1024)
		require.NoError(err)
		require.Equal(XattrPresent, state)
		require.Equal([]byte("label"), v)
		_, state, err = data.GetXattr("file", "user.flightctl", 2)
		require.Error(err)
		require.Equal(XattrUnknown, state)
	})
	t.Run("When the attribute is absent it should report absent", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		require.NoError(os.WriteFile(filepath.Join(root, "data", "file"), []byte("x"), 0o600))
		data := openTrusted(t, rw, "/data")
		v, state, err := data.GetXattr("file", "user.flightctl-absent", 1024)
		require.NoError(err)
		require.Equal(XattrAbsent, state)
		require.Nil(v)
		_, state, err = data.GetXattr(".", "user.flightctl-absent", 1024)
		require.NoError(err)
		require.Equal(XattrAbsent, state)
	})
	for _, tc := range []struct {
		name  string
		errno error
		want  XattrState
		isErr bool
	}{
		{name: "When the filesystem does not support attributes it should report unsupported", errno: unix.ENOTSUP, want: XattrUnsupported},
		{name: "When reading the attribute is denied it should report unknown with an error", errno: unix.EACCES, want: XattrUnknown, isErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			rw, root := anchorFixture(t)
			require.NoError(os.WriteFile(filepath.Join(root, "data", "file"), []byte("x"), 0o600))
			sys := *defaultSysOps
			sys.fgetxattr = func(int, string, []byte) (int, error) { return 0, tc.errno }
			data := openWithSys(t, rw, "/data", &sys)
			_, state, err := data.GetXattr("file", "security.selinux", 1024)
			require.Equal(tc.want, state)
			require.Equal(tc.isErr, err != nil)
		})
	}
	t.Run("When the entry is a symlink it should not follow it", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		sentinel := sentinelDir(t, root)
		require.NoError(os.Symlink(filepath.Join(sentinel, "keep"), filepath.Join(root, "data", "link")))
		data := openTrusted(t, rw, "/data")
		_, state, err := data.GetXattr("link", "user.flightctl", 1024)
		require.Error(err)
		require.Equal(XattrUnknown, state)
	})
}

func TestAnchorSpace(t *testing.T) {
	require := require.New(t)
	rw, _ := anchorFixture(t)
	data := openTrusted(t, rw, "/data")
	avail, err := data.AvailableBytes()
	require.NoError(err)
	require.Greater(avail, uint64(0))
	ro, err := data.ReadOnlyMount()
	require.NoError(err)
	require.False(ro)
	require.NoError(data.Sync())
}

func TestRemoveTreeMaximalValidTree(t *testing.T) {
	require := require.New(t)
	rw, root := anchorFixture(t)
	buildDisjointTree(t, filepath.Join(root, "data", "tree"), 4096, 16)
	require.Equal(61443, countNodes(t, filepath.Join(root, "data", "tree")))
	data := openTrusted(t, rw, "/data")
	res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
	require.NoError(err)
	require.True(res.Complete)
	require.Equal(RemoveComplete, res.Stop)
	require.Equal(61443, res.Removed)
	require.LessOrEqual(res.Work, 593972)
	require.Empty(res.Detached)
	require.Equal(0, res.Retries)
	_, statErr := os.Lstat(filepath.Join(root, "data", "tree"))
	require.True(os.IsNotExist(statErr))
}

func TestRemoveTreeOneOverNodeBudget(t *testing.T) {
	require := require.New(t)
	rw, root := anchorFixture(t)
	tree := filepath.Join(root, "data", "tree")
	buildDisjointTree(t, tree, 4096, 16)
	require.NoError(os.WriteFile(filepath.Join(tree, "extra"), []byte("x"), 0o600))
	data := openTrusted(t, rw, "/data")
	res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
	require.ErrorIs(err, errors.ErrWalkLimitExceeded)
	require.Equal(RemoveNodeBudget, res.Stop)
	require.False(res.Complete)
	require.Equal(61443, res.Removed)
	require.LessOrEqual(res.Work, 593976)
	entries, err := os.ReadDir(tree)
	require.NoError(err)
	require.Empty(entries)

	res, err = data.RemoveTree(context.Background(), "tree", validRemoveBounds())
	require.NoError(err)
	require.True(res.Complete)
	require.Equal(1, res.Removed)
}

func TestRemoveTreeManyDeepPaths(t *testing.T) {
	require := require.New(t)
	rw, root := anchorFixture(t)
	buildDisjointTree(t, filepath.Join(root, "data", "tree"), 256, 16)
	data := openTrusted(t, rw, "/data")
	res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
	require.NoError(err)
	require.Equal(3+15*256, res.Removed)
	require.True(res.Complete)
}

func TestRemoveTreeWorkBudget(t *testing.T) {
	require := require.New(t)
	rw, root := anchorFixture(t)
	dir := filepath.Join(root, "data", "flat")
	require.NoError(os.Mkdir(dir, 0o700))
	for i := 0; i < 5000; i++ {
		require.NoError(os.WriteFile(filepath.Join(dir, strconv.Itoa(i)), nil, 0o600))
	}
	data := openTrusted(t, rw, "/data")
	bounds := validRemoveBounds()
	bounds.MaxWork = 1000
	res, err := data.RemoveTree(context.Background(), "flat", bounds)
	require.ErrorIs(err, errors.ErrWalkLimitExceeded)
	require.Equal(RemoveWorkBudget, res.Stop)
	require.GreaterOrEqual(res.Work, 999)
	require.LessOrEqual(res.Work, 1000)
	require.GreaterOrEqual(res.Removed, 1)
	require.Less(res.Removed, 5000)
	require.NotEmpty(res.LastErr)

	for i := 0; i < 100; i++ {
		res, err = data.RemoveTree(context.Background(), "flat", bounds)
		require.LessOrEqual(res.Work, 1000)
		if res.Complete {
			break
		}
		require.ErrorIs(err, errors.ErrWalkLimitExceeded)
	}
	require.True(res.Complete)
	_, statErr := os.Lstat(dir)
	require.True(os.IsNotExist(statErr))
}

func buildOverDepthFixture(t *testing.T, root string, n int) {
	t.Helper()
	parts := []string{root}
	for i := 1; i <= 15; i++ {
		parts = append(parts, "d")
	}
	deep := filepath.Join(parts...)
	for i := 0; i < n; i++ {
		sub := filepath.Join(deep, "s"+strconv.Itoa(i))
		require.NoError(t, os.MkdirAll(sub, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o600))
	}
}

func TestRemoveTreeDetach(t *testing.T) {
	t.Run("When there are more over-depth directories than the detach budget it should stop and later calls should finish", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		buildOverDepthFixture(t, filepath.Join(root, "data", "tree"), 200)
		data := openTrusted(t, rw, "/data")
		res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
		require.ErrorIs(err, errors.ErrWalkLimitExceeded)
		require.Equal(RemoveDetachBudget, res.Stop)
		require.Len(res.Detached, 64)
		unique := map[string]bool{}
		for _, name := range res.Detached {
			require.True(strings.HasPrefix(name, ".quarantine-"))
			require.Len(name, len(".quarantine-")+16)
			unique[name] = true
			_, statErr := os.Lstat(filepath.Join(root, "data", name, "f"))
			require.NoError(statErr)
		}
		require.Len(unique, 64)
		detached := append([]string{}, res.Detached...)
		for i := 0; i < 10 && !res.Complete; i++ {
			res, err = data.RemoveTree(context.Background(), "tree", validRemoveBounds())
			require.LessOrEqual(len(res.Detached), 64)
			detached = append(detached, res.Detached...)
		}
		require.True(res.Complete, err)
		require.Len(detached, 200)
		for _, name := range detached {
			r, err := data.RemoveTree(context.Background(), name, validRemoveBounds())
			require.NoError(err)
			require.Equal(2, r.Removed)
		}
		entries, err := os.ReadDir(filepath.Join(root, "data"))
		require.NoError(err)
		require.Empty(entries)
	})
	t.Run("When there are exactly as many over-depth directories as the budget it should complete", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		buildOverDepthFixture(t, filepath.Join(root, "data", "tree"), 64)
		data := openTrusted(t, rw, "/data")
		res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
		require.NoError(err)
		require.True(res.Complete)
		require.Len(res.Detached, 64)
		for _, name := range res.Detached {
			r, err := data.RemoveTree(context.Background(), name, validRemoveBounds())
			require.NoError(err)
			require.True(r.Complete)
		}
	})
	t.Run("When there is one over-depth directory more than the budget it should stop after the budget", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		buildOverDepthFixture(t, filepath.Join(root, "data", "tree"), 65)
		data := openTrusted(t, rw, "/data")
		res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
		require.ErrorIs(err, errors.ErrWalkLimitExceeded)
		require.Equal(RemoveDetachBudget, res.Stop)
		require.Len(res.Detached, 64)
	})
}

func TestRemoveTreeChurn(t *testing.T) {
	t.Run("When an entry is added once during removal it should rewind once and complete", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		dir := filepath.Join(root, "data", "tree", "sub")
		require.NoError(os.MkdirAll(dir, 0o700))
		require.NoError(os.WriteFile(filepath.Join(dir, "a"), nil, 0o600))
		sys := *defaultSysOps
		added := false
		sys.unlinkat = func(dirfd int, path string, flags int) error {
			if path == "sub" && flags&unix.AT_REMOVEDIR != 0 && !added {
				added = true
				require.NoError(os.WriteFile(filepath.Join(dir, "late"), nil, 0o600))
			}
			return unix.Unlinkat(dirfd, path, flags)
		}
		data := openWithSys(t, rw, "/data", &sys)
		res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
		require.NoError(err)
		require.True(res.Complete)
		require.Equal(1, res.Retries)
		require.Equal(4, res.Removed)
	})
	t.Run("When a directory stays non-empty it should stop with churn after the retry budget", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		require.NoError(os.MkdirAll(filepath.Join(root, "data", "tree", "sub"), 0o700))
		sys := *defaultSysOps
		sys.unlinkat = func(dirfd int, path string, flags int) error {
			if path == "sub" && flags&unix.AT_REMOVEDIR != 0 {
				return unix.ENOTEMPTY
			}
			return unix.Unlinkat(dirfd, path, flags)
		}
		data := openWithSys(t, rw, "/data", &sys)
		bounds := validRemoveBounds()
		res, err := data.RemoveTree(context.Background(), "tree", bounds)
		require.ErrorIs(err, errors.ErrRemoveChurn)
		require.Equal(RemoveChurn, res.Stop)
		require.Equal(4, res.Retries)
		require.LessOrEqual(res.Work, bounds.MaxWork)
	})
	t.Run("When every unlink reports a missing entry it should stop on the work budget without removing anything", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		dir := filepath.Join(root, "data", "tree")
		require.NoError(os.MkdirAll(dir, 0o700))
		for i := 0; i < 10; i++ {
			require.NoError(os.WriteFile(filepath.Join(dir, strconv.Itoa(i)), nil, 0o600))
		}
		sys := *defaultSysOps
		sys.unlinkat = func(int, string, int) error { return unix.ENOENT }
		data := openWithSys(t, rw, "/data", &sys)
		bounds := validRemoveBounds()
		bounds.MaxWork = 10000
		res, err := data.RemoveTree(context.Background(), "tree", bounds)
		require.ErrorIs(err, errors.ErrWalkLimitExceeded)
		require.Equal(RemoveWorkBudget, res.Stop)
		require.Equal(0, res.Removed)
		require.GreaterOrEqual(res.Work, bounds.MaxWork-1)
		require.LessOrEqual(res.Work, bounds.MaxWork)
		require.LessOrEqual(len(res.LastErr), 256)
		require.Empty(res.Detached)
	})
	t.Run("When a subdirectory is swapped for a symlink during removal it should unlink the link and keep its target", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		sentinel := sentinelDir(t, root)
		tree := filepath.Join(root, "data", "tree")
		require.NoError(os.MkdirAll(filepath.Join(tree, "sub"), 0o700))
		require.NoError(os.WriteFile(filepath.Join(tree, "sub", "f"), nil, 0o600))
		before := treeSnapshot(t, sentinel)
		sys := *defaultSysOps
		swapped := false
		sys.fstatat = func(dirfd int, path string, st *unix.Stat_t, flags int) error {
			if path == "sub" && !swapped {
				swapped = true
				require.NoError(os.Rename(filepath.Join(tree, "sub"), filepath.Join(root, "moved")))
				require.NoError(os.Symlink(sentinel, filepath.Join(tree, "sub")))
			}
			return unix.Fstatat(dirfd, path, st, flags)
		}
		data := openWithSys(t, rw, "/data", &sys)
		res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
		require.NoError(err)
		require.True(res.Complete)
		require.Equal(before, treeSnapshot(t, sentinel))
	})
	t.Run("When a subdirectory is on a different device it should stop without descending", func(t *testing.T) {
		require := require.New(t)
		rw, root := anchorFixture(t)
		tree := filepath.Join(root, "data", "tree")
		require.NoError(os.MkdirAll(filepath.Join(tree, "mnt"), 0o700))
		require.NoError(os.WriteFile(filepath.Join(tree, "mnt", "keep"), nil, 0o600))
		sys := *defaultSysOps
		sys.fstatat = func(dirfd int, path string, st *unix.Stat_t, flags int) error {
			err := unix.Fstatat(dirfd, path, st, flags)
			if path == "mnt" {
				st.Dev++
			}
			return err
		}
		data := openWithSys(t, rw, "/data", &sys)
		res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
		require.ErrorIs(err, errors.ErrCrossDevice)
		require.Equal(RemoveCrossDevice, res.Stop)
		_, statErr := os.Lstat(filepath.Join(tree, "mnt", "keep"))
		require.NoError(statErr)
	})
}

func TestRemoveTreeInterruptedRetry(t *testing.T) {
	require := require.New(t)
	rw, root := anchorFixture(t)
	tree := filepath.Join(root, "data", "tree")
	buildDisjointTree(t, tree, 50, 6)
	total := countNodes(t, tree)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var removed atomic.Int32
	sys := *defaultSysOps
	sys.unlinkat = func(dirfd int, path string, flags int) error {
		err := unix.Unlinkat(dirfd, path, flags)
		if err == nil && removed.Add(1) == 20 {
			cancel()
		}
		return err
	}
	data := openWithSys(t, rw, "/data", &sys)
	res, err := data.RemoveTree(ctx, "tree", validRemoveBounds())
	require.ErrorIs(err, context.Canceled)
	require.Equal(RemoveCanceled, res.Stop)
	require.Equal(20, res.Removed)
	left, err := os.ReadDir(filepath.Join(tree, "payload"))
	require.NoError(err)
	require.NotEmpty(left)
	require.NoError(os.RemoveAll(filepath.Join(tree, "payload", left[0].Name())))
	remaining := countNodes(t, tree)

	res, err = data.RemoveTree(context.Background(), "tree", validRemoveBounds())
	require.NoError(err)
	require.True(res.Complete)
	require.Equal(remaining, res.Removed)
	require.Less(remaining, total-20)
}

func TestRemoveTreeSymlinksAndSpecialFiles(t *testing.T) {
	require := require.New(t)
	rw, root := anchorFixture(t)
	sentinel := sentinelDir(t, root)
	tree := filepath.Join(root, "data", "tree")
	require.NoError(os.MkdirAll(tree, 0o700))
	require.NoError(os.Symlink(sentinel, filepath.Join(tree, "dirlink")))
	require.NoError(os.Symlink(filepath.Join(sentinel, "keep"), filepath.Join(tree, "filelink")))
	require.NoError(unix.Mkfifo(filepath.Join(tree, "fifo"), 0o600))
	before := treeSnapshot(t, sentinel)
	data := openTrusted(t, rw, "/data")
	res, err := data.RemoveTree(context.Background(), "tree", validRemoveBounds())
	require.NoError(err)
	require.Equal(4, res.Removed)
	require.Equal(before, treeSnapshot(t, sentinel))

	res, err = data.RemoveTree(context.Background(), "absent", validRemoveBounds())
	require.NoError(err)
	require.True(res.Complete)
	require.Equal(0, res.Removed)
}

func TestRemoveTreeOpenHandleLimit(t *testing.T) {
	if os.Getenv("FILEIO_FD_LIMIT_CHILD") == "1" {
		fdLimitChild(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRemoveTreeOpenHandleLimit$", "-test.v") //nolint:gosec // G204: re-executes this test binary with fixed arguments
	cmd.Env = append(os.Environ(), "FILEIO_FD_LIMIT_CHILD=1", "FILEIO_FD_LIMIT_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), "fd-limit-child-ok")
}

func fdLimitChild(t *testing.T) {
	require := require.New(t)
	root := os.Getenv("FILEIO_FD_LIMIT_DIR")
	require.NoError(os.MkdirAll(filepath.Join(root, "data"), 0o700))
	rw := NewReadWriter(NewReader(WithReaderRootDir(root)), NewWriter(WithWriterRootDir(root)))
	buildDisjointTree(t, filepath.Join(root, "data", "warm"), 1, 3)
	buildDisjointTree(t, filepath.Join(root, "data", "single"), 1, 16)
	buildDisjointTree(t, filepath.Join(root, "data", "max"), 4096, 16)
	buildOverDepthFixture(t, filepath.Join(root, "data", "corrupt"), 1)
	data, err := rw.OpenAnchor("/data", DirOptions{Trusted: true})
	require.NoError(err)
	_, err = data.RemoveTree(context.Background(), "warm", validRemoveBounds())
	require.NoError(err)

	listing, err := os.Open("/proc/self/fd")
	require.NoError(err)
	self := int(listing.Fd())
	names, err := listing.Readdirnames(-1)
	require.NoError(err)
	require.NoError(listing.Close())
	highest := 0
	for _, name := range names {
		if n, err := strconv.Atoi(name); err == nil && n != self && n > highest {
			highest = n
		}
	}
	// Descriptors 0..highest stay in use; allow exactly 16 more.
	limit := uint64(highest + 1 + 16)
	require.NoError(unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: limit, Max: limit}))

	for _, name := range []string{"single", "max", "corrupt"} {
		res, err := data.RemoveTree(context.Background(), name, validRemoveBounds())
		require.NoError(err, name)
		require.True(res.Complete, name)
		if name == "corrupt" {
			require.Len(res.Detached, 1)
		}
	}
	fmt.Println("fd-limit-child-ok")
}

func TestAnchorPreservesOpenedEntryStatFailures(t *testing.T) {
	tests := []struct {
		name string
		run  func(Anchor) error
	}{
		{"When walking a directory it should preserve the stat error", func(a Anchor) error {
			return a.Walk(context.Background(), ".", WalkBounds{MaxEntries: 4, MaxDepth: 2, MaxNameBytes: 128}, func(string, EntryInfo) error { return nil })
		}},
		{"When reading an attribute it should preserve the stat error", func(a Anchor) error {
			_, _, err := a.GetXattr("child", "user.test", 64)
			return err
		}},
		{"When removing a directory it should preserve the stat error", func(a Anchor) error {
			_, err := a.RemoveTree(context.Background(), "child", validRemoveBounds())
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rw, root := anchorFixture(t)
			child := filepath.Join(root, "data", "child")
			require.NoError(t, os.Mkdir(child, 0o700))
			sys := *defaultSysOps
			sys.fstat = func(int, *unix.Stat_t) error { return unix.EIO }
			err := tc.run(openWithSys(t, rw, "/data", &sys))
			require.ErrorIs(t, err, unix.EIO)
			require.NotErrorIs(t, err, errors.ErrUnsafeFileType)
			require.DirExists(t, child)
		})
	}
}
