package appinspection

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const packageDiskBudget int64 = 512 << 20

var errBudgetExceeded = errors.New("application package inspection budget exceeded")

// packageWorkspace accounts for live file payloads across sequential package writes.
type packageWorkspace struct {
	root  *os.Root
	dir   string
	limit int64
	used  int64
}

func newPackageWorkspace(limit int64) (*packageWorkspace, error) {
	dir, err := os.MkdirTemp("", "flightctl-package-inspection-")
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	if err := root.Mkdir("files", 0o700); err != nil {
		return nil, errors.Join(err, root.Close(), os.RemoveAll(dir))
	}
	return &packageWorkspace{root: root, dir: dir, limit: limit}, nil
}

func (w *packageWorkspace) close() error {
	return errors.Join(w.root.Close(), os.RemoveAll(w.dir))
}

func (w *packageWorkspace) files() fs.FS {
	// The directory is created with the workspace and retained for its lifetime.
	return rootedFiles{root: w.root}
}

type rootedFiles struct {
	root *os.Root
}

func (f rootedFiles) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, fs.ErrInvalid
	}
	return f.root.Open(filepath.Join("files", name))
}

func (w *packageWorkspace) put(ctx context.Context, name string, src io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	f, err := w.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(budgetWriter{ctx: ctx, workspace: w, dst: f}, packageReader{ctx: ctx, src: src})
	if err := errors.Join(copyErr, f.Close(), ctx.Err()); err != nil {
		return errors.Join(err, w.remove(name))
	}
	return nil
}

func (w *packageWorkspace) remove(name string) error {
	var bytes int64
	err := fs.WalkDir(w.root.FS(), name, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		bytes += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := w.root.RemoveAll(name); err != nil {
		return err
	}
	w.used -= bytes
	return nil
}

// publish moves verified files into the package view without making a second copy.
func (w *packageWorkspace) publish(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	old, err := w.root.Stat(to)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if old == nil {
		if err := w.root.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			return err
		}
		return w.root.Rename(from, to)
	}
	incoming, err := w.root.Stat(from)
	if err != nil {
		return err
	}
	if !old.IsDir() || !incoming.IsDir() {
		if err := w.remove(to); err != nil {
			return err
		}
		return w.root.Rename(from, to)
	}
	children, err := fs.ReadDir(w.root.FS(), from)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := w.publish(ctx, filepath.Join(from, child.Name()), filepath.Join(to, child.Name())); err != nil {
			return err
		}
	}
	return nil
}

type budgetWriter struct {
	ctx       context.Context
	workspace *packageWorkspace
	dst       io.Writer
}

func (b budgetWriter) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	reserved := int64(len(p))
	if reserved > b.workspace.limit-b.workspace.used {
		return 0, errBudgetExceeded
	}
	b.workspace.used += reserved
	n, err := b.dst.Write(p)
	b.workspace.used -= reserved - int64(n)
	return n, err
}

type packageReader struct {
	ctx context.Context
	src io.Reader
}

func (r packageReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}
