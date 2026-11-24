package registryconfig

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/userutil"
)

const (
	recordDir        = "registry-config"
	recordName       = "outputs.json"
	recordVersion    = 2
	maxRecordSize    = 4 << 20
	publicationAttr  = "user.flightctl.registry-auth"
	publicationLimit = 64
)

type record struct {
	Version int            `json:"version"`
	Outputs []recordOutput `json:"outputs"`
}

type recordOutput struct {
	Path      string `json:"path"`
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Installed string `json:"installed,omitempty"`
	Pending   string `json:"pending,omitempty"`
}

func (f recordOutput) account() (userutil.Account, bool) {
	home, ok := strings.CutSuffix(f.Path, "/"+consts.ContainersAuthRelativePath)
	return userutil.Account{HomeDir: home, UID: f.UID, GID: f.GID}, ok
}

func (m *manager) Sync(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	if rollbackBeforeSync(ctx) {
		return nil
	}
	plan, err := m.plan(ctx, desired)
	if err != nil {
		return err
	}
	rw, err := m.factory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return err
	}
	dir, err := m.openRecordDir(rw, len(plan.files) > 0)
	if err != nil {
		return err
	}
	if dir == nil {
		return nil
	}
	defer dir.Close()
	previous, err := m.readRecord(dir)
	if err != nil {
		return err
	}
	if len(previous) > 0 && plan.authored == nil {
		plan.authored, err = authoredPaths(desired.Config)
		if err != nil {
			return err
		}
	}
	return m.apply(ctx, rw, dir, previous, plan)
}

func (m *manager) openRecordDir(rw fileio.ReadWriter, create bool) (fileio.Anchor, error) {
	if !filepath.IsAbs(m.dataDir) || m.dataDir == "/" {
		return nil, fmt.Errorf("invalid data directory for registry configuration")
	}
	a, err := rw.OpenAnchor(m.dataDir, fileio.DirOptions{Trusted: true})
	if !create && errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer a.Close()
	if err := a.Revalidate(); err != nil {
		return nil, err
	}
	dir, err := a.OpenDir(recordDir, fileio.DirOptions{Trusted: true, Create: create})
	if !create && errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return dir, err
}

func (m *manager) apply(ctx context.Context, rw fileio.ReadWriter, dir fileio.Anchor, previous map[string]recordOutput, plan desiredOutputs) error {
	current := make(map[string]recordOutput, len(plan.files))
	journal := maps.Clone(previous)
	paths := slices.Sorted(maps.Keys(plan.files))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		file := plan.files[path]
		installed, matches, err := inspectAuthFile(rw, file, previous[path])
		if err != nil {
			return err
		}
		entry := recordOutput{Path: path, UID: file.account.UID, GID: file.account.GID, Installed: installed}
		if !matches {
			entry.Pending = rand.Text()
		}
		journal[path] = entry
		if entry.Pending != "" {
			entry.Installed, entry.Pending = entry.Pending, ""
		}
		current[path] = entry
	}
	// Keep both publication markers until the replacement and its directory entry are durable.
	if !maps.Equal(previous, journal) {
		if err := writeRecord(dir, journal); err != nil {
			return err
		}
	}
	for _, path := range paths {
		if pending := journal[path].Pending; pending != "" {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := writeAuthFile(rw, plan.files[path], pending); err != nil {
				return err
			}
		}
	}
	for _, path := range slices.Sorted(maps.Keys(previous)) {
		if _, keep := current[path]; keep || plan.authored[path] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := removeAuthFile(rw, previous[path]); err != nil {
			return err
		}
	}
	if !maps.Equal(journal, current) {
		return writeRecord(dir, current)
	}
	return nil
}

func (m *manager) readRecord(dir fileio.Anchor) (map[string]recordOutput, error) {
	outputs := make(map[string]recordOutput)
	data, err := dir.ReadFile(recordName, maxRecordSize)
	if errors.Is(err, fs.ErrNotExist) {
		return outputs, nil
	}
	if err != nil {
		return nil, err
	}
	var saved record
	if err := json.Unmarshal(data, &saved); err != nil || saved.Version != recordVersion {
		return nil, fmt.Errorf("invalid registry configuration output record")
	}
	for _, file := range saved.Outputs {
		account, ok := file.account()
		if !ok || !m.allowedHome(account.HomeDir) || file.Path != filepath.Join(account.HomeDir, consts.ContainersAuthRelativePath) {
			return nil, fmt.Errorf("invalid destination in registry configuration output record")
		}
		if (file.Installed == "" && file.Pending == "") || len(file.Installed) > publicationLimit || len(file.Pending) > publicationLimit {
			return nil, fmt.Errorf("invalid publication markers in registry configuration output record")
		}
		outputs[file.Path] = file
	}
	return outputs, nil
}

func writeRecord(dir fileio.Anchor, groups map[string]recordOutput) error {
	saved := record{Version: recordVersion, Outputs: make([]recordOutput, 0, len(groups))}
	for _, path := range slices.Sorted(maps.Keys(groups)) {
		saved.Outputs = append(saved.Outputs, groups[path])
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if len(data) > maxRecordSize {
		return fmt.Errorf("registry configuration output record exceeds size limit")
	}
	if err := dir.Revalidate(); err != nil {
		return err
	}
	return dir.ReplaceFileDurable(recordName, data, authMode)
}

func inspectAuthFile(rw fileio.ReadWriter, file output, previous recordOutput) (string, bool, error) {
	if previous.Installed == "" && previous.Pending == "" {
		return "", false, nil
	}
	dir, err := openDestination(rw, file.account, false, true)
	if err != nil || dir == nil {
		return "", false, err
	}
	defer dir.Close()
	id, err := publishedID(dir, previous)
	if err != nil || id == "" {
		return "", false, err
	}
	if id == previous.Pending {
		// A replacement can be visible even if its directory sync failed.
		if err := dir.Sync(); err != nil {
			return "", false, err
		}
	}
	matches, err := matchesAuthFile(dir, file)
	return id, matches, err
}

func publishedID(dir fileio.Anchor, entry recordOutput) (string, error) {
	value, state, err := dir.GetXattr(authName, publicationAttr, publicationLimit)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil || state != fileio.XattrPresent {
		return "", err
	}
	id := string(value)
	if id != "" && (id == entry.Installed || id == entry.Pending) {
		return id, nil
	}
	return "", nil
}

func writeAuthFile(rw fileio.ReadWriter, file output, id string) error {
	dir, err := openDestination(rw, file.account, true, false)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Revalidate(); err != nil {
		return err
	}
	owner := fileio.Ownership{UID: file.account.UID, GID: file.account.GID}
	return dir.ReplaceFileDurable(authName, file.data, authMode, fileio.WithOwner(owner), fileio.WithXattr(publicationAttr, []byte(id)))
}

func matchesAuthFile(dir fileio.Anchor, desired output) (bool, error) {
	f, err := dir.OpenFile(authName)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info := f.Info()
	if info.Nlink != 1 {
		return false, fmt.Errorf("%w: %w", deviceerrors.ErrHardLinkedFile, deviceerrors.WithElement(filepath.Join(dir.Path(), authName)))
	}
	if info.Perm != authMode || info.UID != desired.account.UID || info.GID != desired.account.GID || info.Size != int64(len(desired.data)) {
		return false, nil
	}
	data, err := io.ReadAll(io.NewSectionReader(f, 0, int64(len(desired.data))+1))
	return bytes.Equal(data, desired.data), err
}

func removeAuthFile(rw fileio.ReadWriter, entry recordOutput) error {
	account, _ := entry.account() // readRecord has validated the path.
	dir, err := openDestination(rw, account, false, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || dir == nil {
		return err
	}
	defer dir.Close()
	if err := dir.Revalidate(); err != nil {
		return err
	}
	id, err := publishedID(dir, entry)
	if err != nil || id == "" {
		return err
	}
	return dir.RemoveFile(authName)
}
