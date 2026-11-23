package appinspection

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

type archiveEntry struct {
	name string
	data string
	kind byte
	link string
}

func makeTar(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: entry.name, Mode: 0o777, Size: int64(len(entry.data)), Typeflag: kind, Linkname: entry.link,
		}))
		_, err := io.WriteString(tw, entry.data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, err := gz.Write(data)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func testWorkspace(t *testing.T, limit int64) *packageWorkspace {
	t.Helper()
	w, err := newPackageWorkspace(limit, packageEntryLimit)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, w.close())
		_, err := os.Stat(w.dir)
		require.ErrorIs(t, err, os.ErrNotExist)
	})
	return w
}

func TestPackageArchiveFormats(t *testing.T) {
	for _, name := range []string{"package.tar", "package.tar.gz", "package.tgz"} {
		t.Run("When the package is "+name+" it should extract regular files", func(t *testing.T) {
			data := makeTar(t, archiveEntry{name: "./", kind: tar.TypeDir}, archiveEntry{name: "./units/app.container", data: "abc"})
			format := artifactFormat(name)
			if name != "package.tar" {
				require.Equal(t, packageTarGzip, format)
				data = gzipBytes(t, data)
			}
			w := testWorkspace(t, int64(len(data))+3)
			require.NoError(t, w.put(t.Context(), "download", bytes.NewReader(data)))
			require.NoError(t, w.extract(t.Context(), "download", format, false))
			file, err := fs.ReadFile(w.files(), "units/app.container")
			require.NoError(t, err)
			require.Equal(t, "abc", string(file))
			info, err := fs.Stat(w.files(), "units/app.container")
			require.NoError(t, err)
			require.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
			require.Equal(t, int64(len(data))+3, w.used)
			require.NoError(t, w.remove("download"))
			require.EqualValues(t, 3, w.used)
		})
	}
}

func TestCompressedPackageExpansionBudget(t *testing.T) {
	for _, delta := range []int64{0, -1} {
		t.Run(fmt.Sprintf("When the available budget differs from the expanded size by %d it should enforce the combined limit", delta), func(t *testing.T) {
			old := "known"
			first, second := strings.Repeat("a", 64<<10), strings.Repeat("b", 96<<10)
			data := gzipBytes(t, makeTar(t, archiveEntry{name: "one", data: first}, archiveEntry{name: "two", data: second}))
			exact := int64(len(old) + len(data) + len(first) + len(second))
			w := testWorkspace(t, exact+delta)
			require.NoError(t, w.put(t.Context(), "files/known", strings.NewReader(old)))
			require.NoError(t, w.put(t.Context(), "download", bytes.NewReader(data)))
			err := w.extract(t.Context(), "download", packageTarGzip, false)
			if delta == 0 {
				require.NoError(t, err)
				require.Equal(t, exact, w.used)
				return
			}
			require.ErrorIs(t, err, errBudgetExceeded)
			_, err = fs.Stat(w.files(), "one")
			require.ErrorIs(t, err, fs.ErrNotExist)
			_, err = fs.Stat(w.files(), "two")
			require.ErrorIs(t, err, fs.ErrNotExist)
			require.EqualValues(t, len(old)+len(data), w.used)
			known, err := fs.ReadFile(w.files(), "known")
			require.NoError(t, err)
			require.Equal(t, old, string(known))
		})
	}
}

func TestCompressedPackageRejection(t *testing.T) {
	valid := makeTar(t, archiveEntry{name: "valid", data: "known"})
	compressed := gzipBytes(t, valid)
	badCRC := bytes.Clone(compressed)
	badCRC[len(badCRC)-8] ^= 1
	outside := filepath.Join(t.TempDir(), "escaped")
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "traversal", data: gzipBytes(t, makeTar(t, archiveEntry{name: "safe/../../escape", data: "bad"}))},
		{name: "absolute path", data: gzipBytes(t, makeTar(t, archiveEntry{name: outside, data: "bad"}))},
		{name: "symlink", data: gzipBytes(t, makeTar(t, archiveEntry{name: "link", kind: tar.TypeSymlink, link: "../escape"}))},
		{name: "hardlink", data: gzipBytes(t, makeTar(t, archiveEntry{name: "link", kind: tar.TypeLink, link: "../escape"}))},
		{name: "FIFO", data: gzipBytes(t, makeTar(t, archiveEntry{name: "fifo", kind: tar.TypeFifo}))},
		{name: "bad gzip header", data: []byte("bad")},
		{name: "truncated tar entry", data: gzipBytes(t, valid[:514])},
		{name: "truncated gzip trailer", data: compressed[:len(compressed)-1]},
		{name: "bad gzip checksum", data: badCRC},
		{name: "trailing tar data", data: gzipBytes(t, append(bytes.Clone(valid), 1))},
	} {
		t.Run("When the package has "+tc.name+" it should leave no extracted files", func(t *testing.T) {
			w := testWorkspace(t, 1<<20)
			require.NoError(t, w.put(t.Context(), "download", bytes.NewReader(tc.data)))
			require.Error(t, w.extract(t.Context(), "download", packageTarGzip, false))
			files, err := fs.ReadDir(w.files(), ".")
			require.NoError(t, err)
			require.Empty(t, files)
			require.EqualValues(t, len(tc.data), w.used)
		})
	}
	_, err := os.Stat(outside)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCompressedPackageManyFiles(t *testing.T) {
	entries := make([]archiveEntry, 0, 10002)
	for i := range 10001 {
		entries = append(entries, archiveEntry{name: fmt.Sprintf("files/%05d", i), data: "x"})
	}
	entries = append(entries, archiveEntry{name: "docker-compose.yaml", data: "services:\n  app:\n    image: quay.io/example/app:latest\n"})
	registry := packageRegistry{}
	layer := registry.blob("many-files.tgz", "application/octet-stream", gzipBytes(t, makeTar(t, entries...)))
	parent := registry.manifest(t, ocispec.DescriptorEmptyJSON, layer)
	repo := registry.serve(t, parent, false)
	result := InspectPackage(t.Context(), v1beta1.AppTypeCompose, repo, "target", nil)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, []Reference{{Image: "quay.io/example/app:latest", Role: RoleWorkload, ConsumerKind: ConsumerContainer}}, result.References)
}

func TestPackageImageLayerWhiteouts(t *testing.T) {
	w := testWorkspace(t, 1<<20)
	lower := makeTar(t,
		archiveEntry{name: "pods/old.yaml", data: "old"},
		archiveEntry{name: "replace", data: "old"},
	)
	require.NoError(t, w.extractArchive(t.Context(), bytes.NewReader(lower), packageTar, true))
	upper := makeTar(t,
		archiveEntry{name: "pods/new.yaml", data: "new"},
		archiveEntry{name: "pods/.wh..wh..opq"},
		archiveEntry{name: "replace/.wh.old"},
		archiveEntry{name: "replace/current", data: "new"},
	)
	require.NoError(t, w.extractArchive(t.Context(), bytes.NewReader(upper), packageTar, true))
	for _, name := range []string{"pods/old.yaml", "pods/.wh..wh..opq", "replace/.wh.old"} {
		_, err := fs.Stat(w.files(), name)
		require.ErrorIs(t, err, fs.ErrNotExist)
	}
	for _, name := range []string{"pods/new.yaml", "replace/current"} {
		file, err := fs.ReadFile(w.files(), name)
		require.NoError(t, err)
		require.Equal(t, "new", string(file))
	}
	require.EqualValues(t, 6, w.used)
}

func TestPackageTarMetadataBudget(t *testing.T) {
	baseContent := strings.Repeat("a", 4<<10)
	base := makeTar(t, archiveEntry{name: "base", data: baseContent})
	var pax bytes.Buffer
	tw := tar.NewWriter(&pax)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "unused", Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "metadata"}}))
	require.NoError(t, tw.Close())
	require.Equal(t, byte(tar.TypeXHeader), pax.Bytes()[156])
	const extendedHeaderBytes = 1024 // One header block and one padded PAX data block.
	data := []byte("x")
	overlay := append(bytes.Repeat(pax.Bytes()[:extendedHeaderBytes], 2), makeTar(t, archiveEntry{name: "overlay", data: string(data)})...)
	baseMetadata := len(base) - len(baseContent)
	for _, tc := range []struct {
		name  string
		limit int
		fits  bool
	}{
		{name: "metadata exactly fits it should publish both archives", limit: baseMetadata + len(overlay) - len(data), fits: true},
		{name: "extended headers exceed the remaining budget it should retain the prior archive", limit: baseMetadata + extendedHeaderBytes + 512},
	} {
		t.Run("When "+tc.name, func(t *testing.T) {
			w := testWorkspace(t, 1<<20)
			w.remainingTarMetadata = tc.limit
			ctx := t.Context()
			require.NoError(t, w.extractArchive(ctx, bytes.NewReader(base), packageTar, false))
			err := w.extractArchive(ctx, bytes.NewReader(gzipBytes(t, overlay)), packageTarGzip, false)
			if tc.fits {
				require.NoError(t, err)
				got, err := fs.ReadFile(w.files(), "overlay")
				require.NoError(t, err)
				require.Equal(t, data, got)
			} else {
				require.ErrorIs(t, err, errBudgetExceeded)
				_, err = fs.Stat(w.files(), "overlay")
				require.ErrorIs(t, err, fs.ErrNotExist)
			}
			got, err := fs.ReadFile(w.files(), "base")
			require.NoError(t, err)
			require.Equal(t, baseContent, string(got))
			_, err = w.root.Stat("stage")
			require.ErrorIs(t, err, fs.ErrNotExist)
		})
	}
}

func TestPackageWhiteoutPathBudget(t *testing.T) {
	const pathBytes = 256 << 10
	entries := make([]archiveEntry, maxWhiteoutPathBytes/pathBytes+1)
	for i := range entries {
		entries[i].name = ".wh." + strings.Repeat("x", pathBytes)
	}
	data := gzipBytes(t, makeTar(t, entries...))
	w := testWorkspace(t, 1<<20)
	require.NoError(t, w.put(t.Context(), "download", bytes.NewReader(data)))
	require.ErrorIs(t, w.extract(t.Context(), "download", packageTarGzip, true), errBudgetExceeded)
	_, err := w.root.Stat("stage")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestCompressedPackageCancellation(t *testing.T) {
	// Enough incompressible data to force more compressed reads after staging starts.
	random := rand.NewPCG(1, 2)
	data := make([]byte, 256<<10)
	for i := range data {
		data[i] = byte(random.Uint64())
	}
	compressed := gzipBytes(t, makeTar(t, archiveEntry{name: "large", data: string(data)}))
	w := testWorkspace(t, 1<<20)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &cancelAfterStage{Reader: bytes.NewReader(compressed), workspace: w, cancel: cancel}
	require.ErrorIs(t, w.extractArchive(ctx, reader, packageTarGzip, false), context.Canceled)
	require.True(t, reader.canceled, "the test must cancel after staging bytes")
	require.Zero(t, w.used)
	files, err := fs.ReadDir(w.files(), ".")
	require.NoError(t, err)
	require.Empty(t, files)
}

type cancelAfterStage struct {
	io.Reader
	workspace *packageWorkspace
	cancel    context.CancelFunc
	canceled  bool
}

func (r *cancelAfterStage) Read(p []byte) (int, error) {
	if r.workspace.used > 0 {
		r.canceled = true
		r.cancel()
		return 0, context.Canceled
	}
	return r.Reader.Read(p)
}

func TestPackageWorkspaceCancelsWithoutLeavingPartialWrites(t *testing.T) {
	w := testWorkspace(t, 1<<20)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := w.put(ctx, "download", &cancelReader{cancel: cancel})
	require.ErrorIs(t, err, context.Canceled)
	_, err = w.root.Stat("download")
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.Zero(t, w.used)
}

type cancelReader struct {
	cancel context.CancelFunc
	read   bool
}

func (r *cancelReader) Read(p []byte) (int, error) {
	if r.read {
		r.cancel()
		return copy(p, "two"), nil
	}
	r.read = true
	return copy(p, "one"), nil
}
