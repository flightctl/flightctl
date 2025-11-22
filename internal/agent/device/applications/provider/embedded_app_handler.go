package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
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
	name string
	rw   fileio.ReadWriter
}

func (e *embeddedQuadletBehavior) Verify(ctx context.Context) error {
	return ensureQuadlet(e.rw, e.embeddedPath())
}

func (e *embeddedQuadletBehavior) Install(ctx context.Context) error {
	// quadlet apps must be moved from their embedded location into the default
	// systemd location. A symlink can't be used as the installed contents must be mutated
	// to abide by flightctl's namespacing rules
	if err := e.rw.CopyDir(e.embeddedPath(), e.AppPath(), fileio.WithFollowSymlinkWithinRoot()); err != nil {
		return fmt.Errorf("copying embedded directory to real path: %w", err)
	}

	if err := installQuadlet(e.rw, e.AppPath(), e.ID()); err != nil {
		return fmt.Errorf("installing quadlet: %w", err)
	}
	return nil
}

func (e *embeddedQuadletBehavior) Remove(ctx context.Context) error {
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
