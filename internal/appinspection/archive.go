package appinspection

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type packageFormat string

const (
	packageRaw     packageFormat = "raw"
	packageTar     packageFormat = "tar"
	packageTarGzip packageFormat = "tar.gz"
	packageTarZstd packageFormat = "tar+zstd"
)

const (
	maxTarTrailingPadding = 1 << 20
	maxWhiteoutPathBytes  = 16 << 20
)

var errInvalidArchive = errors.New("invalid application package archive")

func artifactFormat(name string) packageFormat {
	switch {
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return packageTarGzip
	case strings.HasSuffix(name, ".tar"):
		return packageTar
	default:
		return packageRaw
	}
}

func packagePath(name string) (string, error) {
	name = filepath.FromSlash(name)
	if !filepath.IsLocal(name) || strings.ContainsRune(name, '\x00') {
		return "", errInvalidArchive
	}
	return filepath.Clean(name), nil
}

// extract stages an entire archive before publishing files. For gzip, tar EOF
// can precede validation of the gzip trailer, so extraction drains the stream.
func (w *packageWorkspace) extract(ctx context.Context, name string, format packageFormat, imageLayer bool) (err error) {
	f, err := w.root.Open(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return w.extractArchive(ctx, f, format, imageLayer)
}

func (w *packageWorkspace) extractArchive(ctx context.Context, input io.Reader, format packageFormat, imageLayer bool) (err error) {
	var src io.Reader = packageReader{ctx: ctx, src: input}
	switch format {
	case packageTarGzip:
		gz, err := gzip.NewReader(src)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, gz.Close()) }()
		src = gz
	case packageTarZstd:
		decoder, err := zstd.NewReader(src, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(uint64(packageDiskBudget)))
		if err != nil {
			return err
		}
		defer decoder.Close()
		src = decoder
	case packageTar:
	default:
		return errInvalidArchive
	}

	const stage = "stage"
	if err := w.root.Mkdir(stage, 0o700); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, w.remove(stage)) }()

	whiteouts, err := w.readTar(ctx, stage, packageReader{ctx: ctx, src: src}, imageLayer)
	if err != nil {
		return err
	}
	for _, target := range whiteouts {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Keep the package root available even when a layer makes it opaque.
		if target == "." {
			if err := w.remove("files"); err != nil {
				return err
			}
			if err := w.root.Mkdir("files", 0o700); err != nil {
				return err
			}
		} else if err := w.remove(filepath.Join("files", target)); err != nil {
			return err
		}
	}
	return w.publish(ctx, stage, "files")
}

func (w *packageWorkspace) readTar(ctx context.Context, stage string, src io.Reader, imageLayer bool) ([]string, error) {
	tr := tar.NewReader(src)
	var whiteouts []string
	remainingWhiteoutBytes := maxWhiteoutPathBytes
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := w.takeEntries(1); err != nil {
			return nil, err
		}
		name, err := packagePath(hdr.Name)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(stage, name)
		if err := w.makeEntryParents(path); err != nil {
			return nil, err
		}
		if imageLayer && strings.HasPrefix(filepath.Base(name), ".wh.") {
			if hdr.Typeflag != tar.TypeReg {
				return nil, errInvalidArchive
			}
			target, err := whiteoutTarget(name, hdr.Size)
			if err != nil {
				return nil, err
			}
			// Retain the target paths until the archive and trailer are verified.
			if len(target) > remainingWhiteoutBytes {
				return nil, errBudgetExceeded
			}
			remainingWhiteoutBytes -= len(target)
			whiteouts = append(whiteouts, strings.Clone(target))
			continue
		}
		if err := w.stageTarEntry(ctx, path, hdr, tr); err != nil {
			return nil, err
		}
	}
	// Read through the compression trailer while bounding work after the tar end marker.
	n, err := io.Copy(tarPadding{}, io.LimitReader(src, maxTarTrailingPadding+1))
	if err != nil {
		return nil, err
	}
	if n > maxTarTrailingPadding {
		return nil, errInvalidArchive
	}
	return whiteouts, nil
}

func (w *packageWorkspace) stageTarEntry(ctx context.Context, path string, hdr *tar.Header, src io.Reader) error {
	switch hdr.Typeflag {
	case tar.TypeDir:
		return w.root.MkdirAll(path, 0o700)
	case tar.TypeReg:
		if hasSparseMetadata(hdr) {
			return errInvalidArchive
		}
		if info, err := w.root.Stat(path); err == nil && info.Mode().IsRegular() {
			if err := w.remove(path); err != nil {
				return err
			}
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return w.put(ctx, path, src)
	default:
		return errInvalidArchive
	}
}

func hasSparseMetadata(hdr *tar.Header) bool {
	for key := range hdr.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse.") {
			return true
		}
	}
	return false
}

func whiteoutTarget(name string, size int64) (string, error) {
	if size != 0 {
		return "", errInvalidArchive
	}
	base := filepath.Base(name)
	if base == ".wh..wh..opq" {
		return filepath.Dir(name), nil
	}
	base = strings.TrimPrefix(base, ".wh.")
	if base == "" || base == "." || base == ".." {
		return "", errInvalidArchive
	}
	return filepath.Join(filepath.Dir(name), base), nil
}

type tarPadding struct{}

func (tarPadding) Write(p []byte) (int, error) {
	for i, c := range p {
		if c != 0 {
			return i, errInvalidArchive
		}
	}
	return len(p), nil
}
