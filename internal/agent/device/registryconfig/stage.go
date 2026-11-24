package registryconfig

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

type stagedOutput struct {
	output
	path string
	dir  string
	base string
}

func (m *manager) BeforeUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	plan, err := m.plan(ctx, desired)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	next := make(map[string]*stagedOutput, len(plan.files))
	for _, path := range slices.Sorted(maps.Keys(plan.files)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		file := plan.files[path]
		staged := m.staged[path]
		if staged == nil || staged.account != file.account || !bytes.Equal(staged.data, file.data) {
			staged, err = m.stage(file)
			if err != nil {
				return err
			}
		}
		next[path] = staged
	}
	m.preparedFor = desired
	m.staged = next
	return nil
}

func (m *manager) stage(file output) (*stagedOutput, error) {
	rw, err := m.factory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return nil, err
	}
	base := filepath.Clean(os.TempDir())
	parent, err := openStageParent(rw, base)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := "flightctl-registry-auth-" + rand.Text()
	dir, err := parent.OpenDir(name, fileio.DirOptions{Create: true, Exclusive: true, Perm: 0o711})
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	staged := &stagedOutput{output: file, dir: name, base: base, path: rw.PathFor(filepath.Join(dir.Path(), authName))}
	m.all = append(m.all, staged)
	if err := dir.Revalidate(); err != nil {
		return nil, err
	}
	owner := fileio.Ownership{UID: file.account.UID, GID: file.account.GID}
	if err := dir.ReplaceFileDurable(authName, file.data, authMode, fileio.WithOwner(owner)); err != nil {
		return nil, err
	}
	return staged, nil
}

func openStageParent(rw fileio.ReadWriter, base string) (fileio.Anchor, error) {
	if !filepath.IsAbs(base) || base == "/" {
		return nil, fmt.Errorf("temporary directory must be an absolute non-root path")
	}
	a, err := openDirectory(rw, base, false)
	if err != nil {
		return nil, err
	}
	info, err := a.Lstat(".")
	if err != nil {
		_ = a.Close() // Preserve the staging directory stat error.
		return nil, err
	}
	if (info.UID != 0 && int(info.UID) != os.Geteuid()) || (info.Perm&0o022 != 0 && info.Perm&fs.ModeSticky == 0) {
		_ = a.Close() // Release the rejected staging directory.
		return nil, fmt.Errorf("untrusted registry authentication staging directory %q", base)
	}
	if err := a.Revalidate(); err != nil {
		_ = a.Close() // Preserve the staging path validation error.
		return nil, err
	}
	return a, nil
}

func (m *manager) StagedPath(livePath string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if staged := m.staged[livePath]; staged != nil {
		return staged.path, true
	}
	return "", false
}

func (m *manager) AfterUpdate(_ context.Context, desired *v1beta1.DeviceSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.preparedFor != desired {
		return nil
	}
	return m.cleanupLocked()
}

func (m *manager) Cleanup() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleanupLocked()
}

func (m *manager) cleanupLocked() error {
	m.preparedFor = nil
	m.staged = nil
	if len(m.all) == 0 {
		return nil
	}
	rw, err := m.factory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return err
	}
	var errs []error
	remaining := m.all[:0]
	for _, staged := range m.all {
		if err := removeStaging(rw, staged); err != nil {
			errs = append(errs, err)
			remaining = append(remaining, staged)
		}
	}
	clear(m.all[len(remaining):])
	m.all = remaining
	return errors.Join(errs...)
}

func removeStaging(rw fileio.ReadWriter, staged *stagedOutput) error {
	parent, err := openStageParent(rw, staged.base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer parent.Close()
	_, err = parent.RemoveTree(context.Background(), staged.dir, fileio.RemoveBounds{MaxDepth: 1, MaxNodes: 2, MaxWork: 24})
	return err
}
