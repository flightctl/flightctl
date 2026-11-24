package firmware

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

type verifiedExecution struct {
	retention *Retention
	lease     *lease
	manifest  *Manifest
	source    fileio.Anchor
	state     fileio.Anchor
	roots     fileio.Anchor
	root      fileio.Anchor
	name      string
}

func (r *Retention) acquireExecution(ctx context.Context, pin Pin) (*verifiedExecution, error) {
	if err := validatePin(pin); err != nil {
		return nil, fmt.Errorf("validate phase pin: %w: %w", ErrPinConflict, err)
	}
	if pin.Role != PinRoleActive {
		return nil, ErrPinConflict
	}
	if err := r.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-r.cache.gate }()
	cache, err := r.openCache(false)
	if err != nil {
		return nil, err
	}
	defer cache.Close()
	if cache.updaters == nil || cache.firmware == nil {
		return nil, ErrRetainedPayloadMissing
	}
	pins, err := readRegistry(cache.firmware)
	if err != nil {
		return nil, err
	}
	found := false
	for _, retained := range pins {
		if retained == pin.identity() {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrPinConflict
	}
	limits := capLimits(pin.Limits, r.ceilings)
	validationCtx, cancel := context.WithTimeout(ctx, time.Duration(limits.MaxExtractionSeconds)*time.Second)
	defer cancel()
	manifest, err := verifyPublishedTree(validationCtx, cache.updaters, pin.PayloadDir, pin.ManifestDigest, limits)
	if err != nil {
		return nil, err
	}
	source, err := cache.updaters.OpenDir(pin.PayloadDir, fileio.DirOptions{Trusted: true, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	state := cache.firmware
	cache.firmware = nil
	r.cache.leases[pin.PackageDigest]++
	r.cache.phaseLeases[pin.AttemptID]++
	return &verifiedExecution{
		retention: r, lease: &lease{owner: r.cache, digest: pin.PackageDigest, attemptID: pin.AttemptID},
		manifest: manifest, source: source, state: state,
	}, nil
}

func (e *verifiedExecution) project(ctx context.Context, attemptID string, request []byte) error {
	var err error
	e.roots, err = e.state.OpenDir("phase-roots", fileio.DirOptions{Create: true, Trusted: true, Perm: 0o700})
	if err != nil {
		return err
	}
	// Only the active slot can be passed to an executor. A leftover staging
	// directory is therefore safe to reclaim, including after restart.
	if _, err := e.roots.Lstat("active"); err == nil {
		return errPhasePending
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	const name = "staging"
	if err := e.removeProjection(ctx, name); err != nil {
		return err
	}
	e.root, err = e.roots.OpenDir(name, fileio.DirOptions{Create: true, Exclusive: true, Trusted: true, Perm: 0o755})
	if errors.Is(err, fs.ErrExist) {
		return errPhasePending
	}
	if err != nil {
		return err
	}
	e.name = name
	if err := e.root.ReplaceFileDurable("attempt-id", []byte(attemptID), 0o444); err != nil {
		return err
	}
	for _, file := range e.manifest.Files {
		if err := e.copyFile(ctx, file); err != nil {
			return err
		}
	}
	if err := e.root.WriteFileExclusive("request.json", request, 0o444); err != nil {
		return err
	}
	return e.root.Revalidate()
}

func (e *verifiedExecution) activateProjection() error {
	if err := e.root.Close(); err != nil {
		e.root = nil
		return err
	}
	e.root = nil
	if err := e.roots.RenameNoReplace(e.name, e.roots, "active"); err != nil {
		return err
	}
	e.name = "active"
	if err := e.roots.Sync(); err != nil {
		return err
	}
	root, err := e.roots.OpenDir(e.name, fileio.DirOptions{Trusted: true})
	e.root = root
	return err
}

func (e *verifiedExecution) removeProjection(ctx context.Context, name string) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := e.roots.RemoveTree(cleanupCtx, name, cleanupBounds()); err != nil {
		return fmt.Errorf("remove phase projection: %w", err)
	}
	return e.roots.Sync()
}

func (e *verifiedExecution) copyFile(ctx context.Context, file ManifestFile) (retErr error) {
	parent, name := path.Split(file.Path)
	dir := e.root
	for component := range strings.SplitSeq(strings.TrimSuffix(parent, "/"), "/") {
		child, err := dir.OpenDir(component, fileio.DirOptions{Create: true, Trusted: true, Perm: 0o755})
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, child.Close()) }()
		dir = child
	}
	mode := os.FileMode(0o444)
	if file.Executable {
		mode = 0o555
	}
	written, err := dir.CopyFrom(ctx, e.source, file.Path, name, mode, fileio.CopyBounds{MaxBytes: file.Size})
	if err != nil {
		return err
	}
	if written.SHA256 != file.SHA256 || written.Size != file.Size {
		return fmt.Errorf("%w: %s changed during phase projection", ErrRetainedPayloadChanged, file.Path)
	}
	return nil
}

func (e *verifiedExecution) close(uncertain bool) error {
	var err error
	if e.root != nil {
		err = e.root.Close()
	}
	if !uncertain && e.name != "" {
		err = errors.Join(err, e.removeProjection(context.Background(), e.name))
	}
	for _, anchor := range []fileio.Anchor{e.roots, e.state, e.source} {
		if anchor != nil {
			err = errors.Join(err, anchor.Close())
		}
	}
	if !uncertain && e.lease != nil {
		e.retention.ReleaseLease(&PublishedPayload{lease: e.lease})
	}
	return err
}
