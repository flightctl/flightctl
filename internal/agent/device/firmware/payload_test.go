package firmware_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const (
	imagePath = "/image"
	cacheData = "/data"
	testArch  = "amd64"
)

type payloadFile struct {
	path string
	data []byte
	exec bool
}

type payloadFixture struct {
	t             *testing.T
	root          string
	rw            fileio.ReadWriter
	files         []payloadFile
	manifest      *firmware.Manifest
	manifestBytes []byte
}

func staticELF(t *testing.T) []byte { return buildELF(t, amd64Static()) }

func defaultPayload(t *testing.T) []payloadFile {
	return []payloadFile{
		{path: "payload/bin/updater", data: staticELF(t), exec: true},
		{path: "payload/share/doc.txt", data: []byte("documentation")},
		{path: "payload/share/deep/data.bin", data: bytes.Repeat([]byte{7}, 5000)},
	}
}

func testLimits() policy.ExtractionLimits {
	return policy.ExtractionLimits{
		MaxExtractedManifestBytes: 65536,
		MaxExtractedFiles:         64,
		MaxExtractedFileBytes:     64 << 20,
		MaxExtractedTotalBytes:    128 << 20,
		ExtractionScratchBytes:    128<<20 + 65536,
		MaxExtractionSeconds:      300,
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func manifestDocument(files []payloadFile, entrypoint string) map[string]any {
	schema := "sha256:" + strings.Repeat("b", 64)
	list := make([]any, 0, len(files))
	for _, f := range files {
		list = append(list, map[string]any{"path": f.path, "sha256": sha256Hex(f.data), "size": len(f.data), "executable": f.exec})
	}
	return map[string]any{
		"apiVersion":   firmware.ManifestAPIVersion,
		"name":         "nvidia-orin",
		"version":      "0.1.0",
		"protocol":     1,
		"entrypoint":   entrypoint,
		"dependencies": []any{},
		"schemas":      map[string]any{"config": schema, "observation": schema, "request": schema, "result": schema, "checkpoint": schema},
		"files":        list,
	}
}

// newPayloadFixture writes an image tree with updater/manifest.json and the
// listed payload files beneath a fresh test root.
func newPayloadFixture(t *testing.T, files []payloadFile) *payloadFixture {
	t.Helper()
	root := t.TempDir()
	f := &payloadFixture{
		t:     t,
		root:  root,
		rw:    fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root))),
		files: files,
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data"), 0o700))
	data, err := json.Marshal(manifestDocument(files, files[0].path))
	require.NoError(t, err)
	f.setManifest(data)
	for _, pf := range files {
		f.writeImageFile(pf.path, pf.data, pf.exec)
	}
	return f
}

func (f *payloadFixture) imageDir(rel string) string {
	return filepath.Join(f.root, "image", "updater", rel)
}

func (f *payloadFixture) writeImageFile(rel string, data []byte, exec bool) {
	f.t.Helper()
	p := f.imageDir(rel)
	require.NoError(f.t, os.MkdirAll(filepath.Dir(p), 0o755))
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	require.NoError(f.t, os.WriteFile(p, data, mode))
	require.NoError(f.t, os.Chmod(p, mode))
}

func (f *payloadFixture) setManifest(data []byte) {
	f.t.Helper()
	m, err := firmware.ParseManifest(data)
	require.NoError(f.t, err)
	f.manifest, f.manifestBytes = m, data
	require.NoError(f.t, os.MkdirAll(f.imageDir(""), 0o755))
	_ = os.Remove(f.imageDir("manifest.json"))
	require.NoError(f.t, os.WriteFile(f.imageDir("manifest.json"), data, 0o600))
}

func (f *payloadFixture) src() fileio.Anchor {
	f.t.Helper()
	a, err := f.rw.OpenAnchor(imagePath, fileio.DirOptions{ReadOnly: true})
	require.NoError(f.t, err)
	f.t.Cleanup(func() { _ = a.Close() })
	return a
}

func (f *payloadFixture) cache() fileio.Anchor {
	f.t.Helper()
	data, err := f.rw.OpenAnchor(cacheData, fileio.DirOptions{Trusted: true})
	require.NoError(f.t, err)
	defer data.Close()
	fw, err := data.OpenDir("firmware", fileio.DirOptions{Create: true, Perm: 0o700, Trusted: true})
	require.NoError(f.t, err)
	defer fw.Close()
	updaters, err := fw.OpenDir("updaters", fileio.DirOptions{Create: true, Perm: 0o700, Trusted: true})
	require.NoError(f.t, err)
	f.t.Cleanup(func() { _ = updaters.Close() })
	return updaters
}

func (f *payloadFixture) cacheDir(rel ...string) string {
	return filepath.Join(append([]string{f.root, "data", "firmware", "updaters"}, rel...)...)
}

func (f *payloadFixture) cacheEntries() []string {
	f.t.Helper()
	entries, err := os.ReadDir(f.cacheDir())
	require.NoError(f.t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (f *payloadFixture) stage(ctx context.Context, src, cache fileio.Anchor, limits policy.ExtractionLimits) (*firmware.StagedTree, error) {
	return firmware.StageTree(ctx, src, cache, f.manifestBytes, f.manifest, limits, testArch)
}

func requirePublishError(t *testing.T, err error, state firmware.PublishState, op string) {
	t.Helper()
	var pe *firmware.PublishError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, state, pe.State, "publish state")
	require.Equal(t, op, pe.Op, "publish operation")
}

func requireBudget(t *testing.T, err error, key string) {
	t.Helper()
	var fe *firmware.FetchError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, firmware.ReasonResourceLimitExceeded, fe.Reason)
	require.Equal(t, key, fe.ExceededBudget)
	require.False(t, fe.Retryable)
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	info, err := os.Lstat(p)
	require.NoError(t, err)
	return info.Sys().(*syscall.Stat_t).Ino
}

// hooks inject failures around a real cache anchor.
type hooks struct {
	openDir    func(parent, name string) error
	write      func(parent, name string) error
	copy       func(parent, name string) error
	sync       func(dir string) error
	rename     func(oldName, newName string) error
	revalidate func(dir string) error
	removeTree func(name string) (fileio.RemoveResult, error, bool)
	available  func() (uint64, bool)
	xattr      func(rel, attr string) ([]byte, fileio.XattrState, error, bool)
	replace    func(dir, name string) error
	close      func(dir string)
}

type hooked struct {
	fileio.Anchor
	h *hooks
}

func (a hooked) Unwrap() fileio.Anchor { return a.Anchor }

func (a hooked) OpenDir(name string, opts fileio.DirOptions) (fileio.Anchor, error) {
	if a.h.openDir != nil {
		if err := a.h.openDir(a.Path(), name); err != nil {
			return nil, err
		}
	}
	child, err := a.Anchor.OpenDir(name, opts)
	if err != nil {
		return nil, err
	}
	return hooked{Anchor: child, h: a.h}, nil
}

func (a hooked) WriteFileExclusive(name string, data []byte, perm fs.FileMode) error {
	if a.h.write != nil {
		if err := a.h.write(a.Path(), name); err != nil {
			return err
		}
	}
	return a.Anchor.WriteFileExclusive(name, data, perm)
}

func (a hooked) CopyFrom(ctx context.Context, src fileio.Anchor, srcRel, name string, perm fs.FileMode, b fileio.CopyBounds) (fileio.FileDigest, error) {
	if a.h.copy != nil {
		if err := a.h.copy(a.Path(), name); err != nil {
			return fileio.FileDigest{}, err
		}
	}
	return a.Anchor.CopyFrom(ctx, src, srcRel, name, perm, b)
}

func (a hooked) Sync() error {
	if a.h.sync != nil {
		if err := a.h.sync(a.Path()); err != nil {
			return err
		}
	}
	return a.Anchor.Sync()
}

func (a hooked) RenameNoReplace(oldName string, newDir fileio.Anchor, newName string) error {
	if a.h.rename != nil {
		if err := a.h.rename(oldName, newName); err != nil {
			return err
		}
	}
	return a.Anchor.RenameNoReplace(oldName, newDir, newName)
}

func (a hooked) Revalidate() error {
	if a.h.revalidate != nil {
		if err := a.h.revalidate(a.Path()); err != nil {
			return err
		}
	}
	return a.Anchor.Revalidate()
}

func (a hooked) RemoveTree(ctx context.Context, name string, b fileio.RemoveBounds) (fileio.RemoveResult, error) {
	if a.h.removeTree != nil {
		if res, err, ok := a.h.removeTree(name); ok {
			return res, err
		}
	}
	return a.Anchor.RemoveTree(ctx, name, b)
}

func (a hooked) AvailableBytes() (uint64, error) {
	if a.h.available != nil {
		if n, ok := a.h.available(); ok {
			return n, nil
		}
	}
	return a.Anchor.AvailableBytes()
}

type slowSource struct {
	fileio.Anchor
	delay  time.Duration
	onRead func()
}

func (s *slowSource) OpenFile(rel string) (fileio.File, error) {
	f, err := s.Anchor.OpenFile(rel)
	if err != nil {
		return nil, err
	}
	return &slowFile{File: f, s: s}, nil
}

type slowFile struct {
	fileio.File
	s *slowSource
}

func (f *slowFile) ReadAt(p []byte, off int64) (int, error) {
	time.Sleep(f.s.delay)
	if f.s.onRead != nil {
		f.s.onRead()
	}
	return f.File.ReadAt(p, off)
}

func TestStageAndPublishValidPayload(t *testing.T) {
	require := require.New(t)
	f := newPayloadFixture(t, defaultPayload(t))
	cache := f.cache()
	st, err := f.stage(context.Background(), f.src(), cache, testLimits())
	require.NoError(err)
	require.True(strings.HasPrefix(st.Name(), ".staging-"))
	final := strings.Repeat("c", 64)
	payload, err := st.Publish(context.Background(), final)
	require.NoError(err)
	require.NoError(st.Discard(context.Background()), "discarding a published tree is a no-op")

	require.Equal(final, payload.Dir)
	require.Equal("payload/bin/updater", payload.Entrypoint)
	require.Equal(policy.Hash(f.manifestBytes), payload.ManifestDigest)
	require.Equal(testLimits(), payload.Limits)
	require.False(payload.SourceMountReadOnly)
	require.Len(payload.Files, len(f.files))
	for i, pf := range f.files {
		require.Equal(firmware.PublishedFile{Path: pf.path, SHA256: sha256Hex(pf.data), Size: int64(len(pf.data)), Executable: pf.exec}, payload.Files[i])
		info, err := os.Lstat(f.cacheDir(final, pf.path))
		require.NoError(err)
		want := os.FileMode(0o400)
		if pf.exec {
			want = 0o500
		}
		require.Equal(want, info.Mode().Perm(), pf.path)
		got, err := os.ReadFile(f.cacheDir(final, pf.path))
		require.NoError(err)
		require.Equal(pf.data, got)
	}
	manifest, err := os.ReadFile(f.cacheDir(final, "manifest.json"))
	require.NoError(err)
	require.Equal(f.manifestBytes, manifest)
	for _, dir := range []string{"", "payload", "payload/bin", "payload/share/deep"} {
		info, err := os.Lstat(f.cacheDir(final, dir))
		require.NoError(err)
		require.True(info.IsDir())
		require.Equal(os.FileMode(0o700), info.Mode().Perm(), dir)
	}
	require.Equal([]string{final}, f.cacheEntries())
}

func TestStageTreeRejectsHostilePayloads(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(t *testing.T, f *payloadFixture)
		reason firmware.Reason
	}{
		{
			name: "When a listed file is a symlink it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, os.Remove(f.imageDir("payload/share/doc.txt")))
				require.NoError(t, os.Symlink("/etc/passwd", f.imageDir("payload/share/doc.txt")))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When a listed file has a second hard link it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, os.Link(f.imageDir("payload/share/doc.txt"), filepath.Join(f.root, "image", "extra-link")))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When a listed file is a FIFO it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, os.Remove(f.imageDir("payload/share/doc.txt")))
				require.NoError(t, unix.Mkfifo(f.imageDir("payload/share/doc.txt"), 0o644))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When the payload holds a socket it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, unix.Mknod(f.imageDir("payload/share/sock"), unix.S_IFSOCK|0o644, 0))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When the payload holds an unlisted file it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				f.writeImageFile("payload/share/extra", []byte("x"), false)
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When the updater directory holds an unexpected entry it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, os.WriteFile(f.imageDir("README"), []byte("x"), 0o600))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When a listed file is missing it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, os.Remove(f.imageDir("payload/share/doc.txt")))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When a listed file is larger than its manifest size it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				f.writeImageFile("payload/share/doc.txt", []byte("documentation and more"), false)
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When a listed file is smaller than its manifest size it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				f.writeImageFile("payload/share/doc.txt", []byte("doc"), false)
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When a listed file has different content it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				f.writeImageFile("payload/share/doc.txt", []byte("DOCUMENTATION"), false)
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When a listed file is setuid it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, os.Chmod(f.imageDir("payload/bin/updater"), 0o755|os.ModeSetuid))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When a listed file is a directory it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, os.Remove(f.imageDir("payload/share/doc.txt")))
				require.NoError(t, os.Mkdir(f.imageDir("payload/share/doc.txt"), 0o755))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When a listed parent directory is a symlink it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, os.Rename(f.imageDir("payload/share"), filepath.Join(f.root, "image", "moved-share")))
				require.NoError(t, os.Symlink(filepath.Join(f.root, "image", "moved-share"), f.imageDir("payload/share")))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When the manifest in the image is a symlink it should reject the payload",
			mutate: func(t *testing.T, f *payloadFixture) {
				require.NoError(t, os.Rename(f.imageDir("manifest.json"), filepath.Join(f.root, "image", "manifest.json")))
				require.NoError(t, os.Symlink(filepath.Join(f.root, "image", "manifest.json"), f.imageDir("manifest.json")))
			},
			reason: firmware.ReasonUpdaterUnavailable,
		},
		{
			name: "When the executable needs a program interpreter it should report it unsupported",
			mutate: func(t *testing.T, f *payloadFixture) {
				dynamic := buildELF(t, func() elfSpec { s := amd64Static(); s.interp = true; return s }())
				files := defaultPayload(t)
				files[0].data = dynamic
				data, err := json.Marshal(manifestDocument(files, files[0].path))
				require.NoError(t, err)
				f.setManifest(data)
				f.writeImageFile(files[0].path, dynamic, true)
			},
			reason: firmware.ReasonFirmwareNotSupported,
		},
		{
			name: "When the executable is a script it should report it unsupported",
			mutate: func(t *testing.T, f *payloadFixture) {
				script := []byte("#!/bin/sh\necho update\n")
				files := defaultPayload(t)
				files[0].data = script
				data, err := json.Marshal(manifestDocument(files, files[0].path))
				require.NoError(t, err)
				f.setManifest(data)
				f.writeImageFile(files[0].path, script, true)
			},
			reason: firmware.ReasonFirmwareNotSupported,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			tc.mutate(t, f)
			cache := f.cache()
			st, err := f.stage(context.Background(), f.src(), cache, testLimits())
			requireFetchError(t, err, tc.reason, false)
			requirePublishError(t, err, firmware.PublishNotVisible, "stage")
			if st != nil {
				require.NoError(st.Discard(context.Background()))
			}
			require.Empty(f.cacheEntries(), "no staging or final may remain")
		})
	}
}

func TestStageTreeChecksTheManifestBeforeCopying(t *testing.T) {
	big := func(n int) []payloadFile {
		files := defaultPayload(t)
		for i := 0; i < n; i++ {
			files = append(files, payloadFile{path: "payload/extra/f" + strconv.Itoa(i), data: []byte("x")})
		}
		return files
	}
	testCases := []struct {
		name   string
		files  []payloadFile
		limits func(l *policy.ExtractionLimits)
		check  func(t *testing.T, err error)
	}{
		{
			name:   "When the manifest lists more files than the budget it should report the file budget",
			files:  big(2),
			limits: func(l *policy.ExtractionLimits) { l.MaxExtractedFiles = 4 },
			check:  func(t *testing.T, err error) { requireBudget(t, err, "maxExtractedFiles") },
		},
		{
			name:   "When a listed file is larger than the per-file budget it should report the file size budget",
			files:  defaultPayload(t),
			limits: func(l *policy.ExtractionLimits) { l.MaxExtractedFileBytes = 1000 },
			check:  func(t *testing.T, err error) { requireBudget(t, err, "maxExtractedFileBytes") },
		},
		{
			name:  "When the listed files exceed the total budget it should report the total budget",
			files: defaultPayload(t),
			limits: func(l *policy.ExtractionLimits) {
				l.MaxExtractedFileBytes = 5000
				l.MaxExtractedTotalBytes = 5100
			},
			check: func(t *testing.T, err error) { requireBudget(t, err, "maxExtractedTotalBytes") },
		},
		{
			name:   "When the manifest is larger than its budget it should report the manifest budget",
			files:  defaultPayload(t),
			limits: func(l *policy.ExtractionLimits) { l.MaxExtractedManifestBytes = 100 },
			check:  func(t *testing.T, err error) { requireBudget(t, err, "maxExtractedManifestBytes") },
		},
		{
			name: "When a listed path is deeper than supported it should report it unsupported",
			files: append(defaultPayload(t), payloadFile{
				path: "payload/" + strings.Repeat("d/", 15) + "f", data: []byte("x"),
			}),
			limits: func(*policy.ExtractionLimits) {},
			check: func(t *testing.T, err error) {
				requireFetchError(t, err, firmware.ReasonFirmwareNotSupported, false)
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, tc.files)
			limits := testLimits()
			tc.limits(&limits)
			cache := f.cache()
			st, err := f.stage(context.Background(), f.src(), cache, limits)
			require.Nil(st)
			tc.check(t, err)
			require.Empty(f.cacheEntries(), "nothing may be staged before the checks pass")
		})
	}
}

func TestStageTreeRejectsInvalidManifestStructures(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(m *firmware.Manifest)
	}{
		{name: "When a path escapes payload it should reject the manifest", mutate: func(m *firmware.Manifest) { m.Files[1].Path = "payload/../evil" }},
		{name: "When a path is absolute it should reject the manifest", mutate: func(m *firmware.Manifest) { m.Files[1].Path = "/payload/evil" }},
		{name: "When a path is listed twice it should reject the manifest", mutate: func(m *firmware.Manifest) { m.Files[1].Path = m.Files[2].Path }},
		{name: "When a file is also a listed directory it should reject the manifest", mutate: func(m *firmware.Manifest) { m.Files[1].Path = "payload/share/deep" }},
		{name: "When the entrypoint is not listed it should reject the manifest", mutate: func(m *firmware.Manifest) { m.Entrypoint = "payload/bin/other" }},
		{name: "When a size is negative it should reject the manifest", mutate: func(m *firmware.Manifest) { m.Files[1].Size = -1 }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			m := *f.manifest
			m.Files = append([]firmware.ManifestFile(nil), f.manifest.Files...)
			tc.mutate(&m)
			cache := f.cache()
			st, err := firmware.StageTree(context.Background(), f.src(), cache, f.manifestBytes, &m, testLimits(), testArch)
			require.Nil(st)
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
			require.Empty(f.cacheEntries())
		})
	}
}

func TestStageTreeRejectsAHugeUnexpectedDirectoryAtTheFirstExtraEntry(t *testing.T) {
	require := require.New(t)
	f := newPayloadFixture(t, defaultPayload(t))
	dir := f.imageDir("payload/flood")
	require.NoError(os.MkdirAll(dir, 0o755))
	for i := 0; i < 20000; i++ {
		file, err := os.Create(filepath.Join(dir, strconv.Itoa(i)))
		require.NoError(err)
		require.NoError(file.Close())
	}
	cache := f.cache()
	start := time.Now()
	st, err := f.stage(context.Background(), f.src(), cache, testLimits())
	require.Nil(st)
	requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
	require.Less(time.Since(start), 5*time.Second)
	require.Empty(f.cacheEntries())
}

func TestStageTreeSpace(t *testing.T) {
	t.Run("When the cache has less free space than the scratch budget it should fail before staging", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		cache := hooked{Anchor: f.cache(), h: &hooks{available: func() (uint64, bool) { return uint64(testLimits().ExtractionScratchBytes) - 1, true }}}
		st, err := f.stage(context.Background(), f.src(), cache, testLimits())
		require.Nil(st)
		requireFetchError(t, err, firmware.ReasonInsufficientSpace, false)
		var fe *firmware.FetchError
		require.ErrorAs(err, &fe)
		require.Empty(fe.ExceededBudget)
		require.Empty(f.cacheEntries())
	})
	t.Run("When the cache runs out of space while copying it should report insufficient space", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		cache := hooked{Anchor: f.cache(), h: &hooks{copy: func(_, name string) error {
			return fmt.Errorf("write %s: %w", name, unix.ENOSPC)
		}}}
		st, err := f.stage(context.Background(), f.src(), cache, testLimits())
		requireFetchError(t, err, firmware.ReasonInsufficientSpace, false)
		requirePublishError(t, err, firmware.PublishNotVisible, "copy")
		require.NotNil(st)
		require.NoError(st.Discard(context.Background()))
		require.Empty(f.cacheEntries())
	})
}

func TestStageTreeTimeAndCancellation(t *testing.T) {
	files := []payloadFile{
		{path: "payload/bin/updater", data: staticELF(t), exec: true},
		{path: "payload/blob", data: bytes.Repeat([]byte{1}, 2<<20)},
	}
	t.Run("When the extraction deadline expires mid-file it should report the time budget", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, files)
		cache := f.cache()
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		st, err := f.stage(ctx, &slowSource{Anchor: f.src(), delay: 20 * time.Millisecond}, cache, testLimits())
		requireBudget(t, err, "maxExtractionSeconds")
		require.NotNil(st)
		require.NoError(st.Discard(context.Background()))
		require.Empty(f.cacheEntries())
	})
	t.Run("When the caller cancels mid-stream it should stop with the cancellation", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, files)
		cache := f.cache()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reads := 0
		src := &slowSource{Anchor: f.src(), onRead: func() {
			reads++
			if reads == 3 {
				cancel()
			}
		}}
		st, err := f.stage(ctx, src, cache, testLimits())
		require.ErrorIs(err, context.Canceled)
		var fe *firmware.FetchError
		require.False(errors.As(err, &fe), "a cancellation carries no firmware reason")
		require.NotNil(st)
		require.NoError(st.Discard(context.Background()))
		require.Empty(f.cacheEntries())
	})
}

func TestStageTreeFaultsBeforePublication(t *testing.T) {
	errIO := fmt.Errorf("injected: %w", unix.EIO)
	testCases := []struct {
		name string
		h    *hooks
		op   string
	}{
		{name: "When creating the staging directory fails it should leave nothing behind", op: "stage", h: &hooks{openDir: func(_, name string) error {
			if strings.HasPrefix(name, ".staging-") {
				return errIO
			}
			return nil
		}}},
		{name: "When creating the manifest fails it should keep the staging until discarded", op: "stage", h: &hooks{write: func(_, name string) error {
			if name == "manifest.json" {
				return errIO
			}
			return nil
		}}},
		{name: "When writing a file chunk fails it should keep the staging until discarded", op: "copy", h: &hooks{copy: func(string, string) error { return errIO }}},
		{name: "When syncing a staged directory fails it should keep the staging until discarded", op: "sync", h: &hooks{sync: func(dir string) error {
			if strings.Contains(dir, ".staging-") {
				return errIO
			}
			return nil
		}}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			cache := hooked{Anchor: f.cache(), h: tc.h}
			st, err := f.stage(context.Background(), f.src(), cache, testLimits())
			require.Error(err)
			requirePublishError(t, err, firmware.PublishNotVisible, tc.op)
			if st != nil {
				entries := f.cacheEntries()
				require.Len(entries, 1)
				require.Equal(st.Name(), entries[0])
				require.NoError(st.Discard(context.Background()))
			}
			require.Empty(f.cacheEntries())
		})
	}
}

func TestPublishOutcomes(t *testing.T) {
	final := strings.Repeat("d", 64)
	stageWith := func(t *testing.T, h *hooks) (*payloadFixture, *firmware.StagedTree) {
		t.Helper()
		f := newPayloadFixture(t, defaultPayload(t))
		cache := hooked{Anchor: f.cache(), h: h}
		st, err := f.stage(context.Background(), f.src(), cache, testLimits())
		require.NoError(t, err)
		return f, st
	}
	requireCompleteFinal := func(t *testing.T, f *payloadFixture) {
		t.Helper()
		for _, pf := range f.files {
			got, err := os.ReadFile(f.cacheDir(final, pf.path))
			require.NoError(t, err)
			require.Equal(t, pf.data, got)
		}
	}

	t.Run("When the rename fails it should not publish and keep the staging until discarded", func(t *testing.T) {
		require := require.New(t)
		h := &hooks{}
		f, st := stageWith(t, h)
		h.rename = func(string, string) error { return fmt.Errorf("injected: %w", unix.EIO) }
		_, err := st.Publish(context.Background(), final)
		requirePublishError(t, err, firmware.PublishNotVisible, "rename")
		require.Equal([]string{st.Name()}, f.cacheEntries())
		require.NoError(st.Discard(context.Background()))
		require.Empty(f.cacheEntries())
	})
	t.Run("When syncing the cache after the rename fails it should report visible but not usable and keep the final", func(t *testing.T) {
		require := require.New(t)
		h := &hooks{}
		f, st := stageWith(t, h)
		h.sync = func(dir string) error {
			if !strings.Contains(dir, ".staging-") && strings.HasSuffix(dir, "updaters") {
				return fmt.Errorf("injected: %w", unix.EIO)
			}
			return nil
		}
		_, err := st.Publish(context.Background(), final)
		requirePublishError(t, err, firmware.PublishVisibleNotUsable, "sync-parent")
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
		require.NoError(st.Discard(context.Background()))
		require.Equal([]string{final}, f.cacheEntries())
		requireCompleteFinal(t, f)
	})
	t.Run("When revalidating the cache after the rename fails it should report visible but not usable and keep the final", func(t *testing.T) {
		require := require.New(t)
		h := &hooks{}
		f, st := stageWith(t, h)
		h.revalidate = func(string) error { return deviceerrors.ErrAnchorMoved }
		_, err := st.Publish(context.Background(), final)
		requirePublishError(t, err, firmware.PublishVisibleNotUsable, "revalidate")
		require.NoError(st.Discard(context.Background()))
		require.Equal([]string{final}, f.cacheEntries())
		requireCompleteFinal(t, f)
	})
	t.Run("When the final name exists it should keep the staging and leave the existing final unchanged", func(t *testing.T) {
		require := require.New(t)
		f, st := stageWith(t, &hooks{})
		require.NoError(os.Mkdir(f.cacheDir(final), 0o700))
		require.NoError(os.WriteFile(f.cacheDir(final, "marker"), []byte("winner"), 0o400))
		stagingIno := inode(t, f.cacheDir(st.Name()))
		finalIno := inode(t, f.cacheDir(final))
		_, err := st.Publish(context.Background(), final)
		require.ErrorIs(err, deviceerrors.ErrDestinationExists)
		requirePublishError(t, err, firmware.PublishNotVisible, "rename")
		require.Equal(stagingIno, inode(t, f.cacheDir(st.Name())))
		require.Equal(finalIno, inode(t, f.cacheDir(final)))
		marker, err := os.ReadFile(f.cacheDir(final, "marker"))
		require.NoError(err)
		require.Equal([]byte("winner"), marker)
		got, err := os.ReadFile(f.cacheDir(st.Name(), "payload/share/doc.txt"))
		require.NoError(err)
		require.Equal([]byte("documentation"), got)

		require.NoError(st.Discard(context.Background()))
		require.Equal([]string{final}, f.cacheEntries())
		require.NoError(st.Discard(context.Background()), "discard is idempotent")
	})
}

func TestDiscard(t *testing.T) {
	t.Run("When the staging was partly removed it should finish removing it", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		st, err := f.stage(context.Background(), f.src(), f.cache(), testLimits())
		require.NoError(err)
		require.NoError(os.RemoveAll(f.cacheDir(st.Name(), "payload", "share")))
		require.NoError(st.Discard(context.Background()))
		require.Empty(f.cacheEntries())
	})
	t.Run("When removal stops early it should report the stop reason and a later discard should finish", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		h := &hooks{}
		cache := hooked{Anchor: f.cache(), h: h}
		st, err := f.stage(context.Background(), f.src(), cache, testLimits())
		require.NoError(err)
		h.removeTree = func(string) (fileio.RemoveResult, error, bool) {
			return fileio.RemoveResult{Stop: fileio.RemoveWorkBudget, Work: 10}, deviceerrors.ErrWalkLimitExceeded, true
		}
		err = st.Discard(context.Background())
		require.ErrorIs(err, deviceerrors.ErrWalkLimitExceeded)
		require.ErrorContains(err, string(fileio.RemoveWorkBudget))
		require.Equal([]string{st.Name()}, f.cacheEntries())
		h.removeTree = nil
		require.NoError(st.Discard(context.Background()))
		require.Empty(f.cacheEntries())
	})
}

func TestStagedTreeReportsTheSourceMountMode(t *testing.T) {
	require := require.New(t)
	f := newPayloadFixture(t, defaultPayload(t))
	st, err := f.stage(context.Background(), readOnlySource{Anchor: f.src()}, f.cache(), testLimits())
	require.NoError(err)
	payload, err := st.Publish(context.Background(), strings.Repeat("e", 64))
	require.NoError(err)
	require.True(payload.SourceMountReadOnly)
}

type readOnlySource struct{ fileio.Anchor }

func (readOnlySource) ReadOnlyMount() (bool, error) { return true, nil }

var _ = elf.ET_EXEC
