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
	if err := validatePin(pin); err != nil || pin.Role != PinRoleActive {
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
	// A crash during setup leaves the occupied slot and blocks later dispatch.
	const name = "active"
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

func (e *verifiedExecution) copyFile(ctx context.Context, file ManifestFile) error {
	parent, name := path.Split(file.Path)
	dir := e.root
	for component := range strings.SplitSeq(strings.TrimSuffix(parent, "/"), "/") {
		child, err := dir.OpenDir(component, fileio.DirOptions{Create: true, Trusted: true, Perm: 0o755})
		if err != nil {
			return err
		}
		defer child.Close()
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, removeErr := e.roots.RemoveTree(ctx, e.name, cleanupBounds())
		err = errors.Join(err, removeErr)
	}
	for _, anchor := range []fileio.Anchor{e.roots, e.state, e.source} {
		if anchor != nil {
			err = errors.Join(err, anchor.Close())
		}
	}
	if !uncertain {
		e.retention.ReleaseLease(&PublishedPayload{lease: e.lease})
	}
	return err
}
