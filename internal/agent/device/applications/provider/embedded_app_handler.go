package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
)

type embeddedAppTypeHandler interface {
	Verify(ctx context.Context) error
	// Install the application content to the device.
	Install(ctx context.Context) error
	// Remove the application content from the device.
	Remove(ctx context.Context) error
	// AppPath returns the path the app is to be installed at
	AppPath() string
	// ID returns the Identifier
	ID() string
	// Volumes returns any Volume that is defined as part of the spec itself
	Volumes() ([]*Volume, error)
}

var _ embeddedAppTypeHandler = (*embeddedQuadletBehavior)(nil)
var _ embeddedAppTypeHandler = (*embeddedComposeBehavior)(nil)

type embeddedQuadletBehavior struct {
	name      string
	rw        fileio.ReadWriter
	log       *log.PrefixLogger
	bootTime  string
	installed bool
}

func (e *embeddedQuadletBehavior) Verify(ctx context.Context) error {
	return ensureQuadlet(e.rw, e.embeddedPath())
}

func (e *embeddedQuadletBehavior) Install(ctx context.Context) error {
	if err := e.rw.CopyDir(e.embeddedPath(), e.AppPath(), fileio.WithFollowSymlinkWithinRoot()); err != nil {
		return fmt.Errorf("copying embedded directory to real path: %w", err)
	}

	if err := installQuadlet(e.rw, e.log, e.AppPath(), e.ID()); err != nil {
		return fmt.Errorf("installing quadlet: %w", err)
	}

	markerPath := filepath.Join(e.AppPath(), embeddedQuadletMarkerFile)
	if err := e.rw.WriteFile(markerPath, []byte(e.bootTime), fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing embedded marker: %w", err)
	}

	return nil
}

func (e *embeddedQuadletBehavior) Remove(ctx context.Context) error {
	path := filepath.Join(lifecycle.QuadletTargetPath, quadlet.NamespaceResource(e.ID(), lifecycle.QuadletTargetName))
	if err := e.rw.RemoveFile(path); err != nil {
		return fmt.Errorf("removing quadlet target file: %w", err)
	}
	if err := e.rw.RemoveAll(e.AppPath()); err != nil {
		return fmt.Errorf("removing application: %w", err)
	}
	return nil
}

func (e *embeddedQuadletBehavior) AppPath() string {
	return filepath.Join(lifecycle.QuadletAppPath, e.name)
}

func (e *embeddedQuadletBehavior) embeddedPath() string {
	return filepath.Join(lifecycle.EmbeddedQuadletAppPath, e.name)
}

func (e *embeddedQuadletBehavior) ID() string {
	return client.NewComposeID(e.name)
}

func (e *embeddedQuadletBehavior) Volumes() ([]*Volume, error) {
	if e.installed {
		return extractQuadletVolumesFromDir(e.ID(), e.rw, e.AppPath())
	}
	return extractQuadletVolumesFromDir(e.ID(), e.rw, e.embeddedPath())
}

type embeddedComposeBehavior struct {
	name string
	rw   fileio.ReadWriter
}

func (e *embeddedComposeBehavior) Verify(ctx context.Context) error {
	if err := ensureCompose(e.rw, e.AppPath()); err != nil {
		return fmt.Errorf("ensuring compose: %w", err)
	}
	return nil
}

func (e *embeddedComposeBehavior) Install(ctx context.Context) error {
	// no-op
	return nil
}

func (e *embeddedComposeBehavior) Remove(ctx context.Context) error {
	return nil
}

func (e *embeddedComposeBehavior) AppPath() string {
	return filepath.Join(lifecycle.EmbeddedComposeAppPath, e.name)
}

func (e *embeddedComposeBehavior) ID() string {
	return client.NewComposeID(e.name)
}
func (e *embeddedComposeBehavior) Volumes() ([]*Volume, error) {
	return nil, nil
}
