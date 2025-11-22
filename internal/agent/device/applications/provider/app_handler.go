package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

type appTypeHandler interface {
	// Dependencies returns the required applications required for the app to work
	Dependencies() []string
	// Verify the contents of the specified directory
	Verify(ctx context.Context, dir string) error
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

var _ appTypeHandler = (*quadletHandler)(nil)
var _ appTypeHandler = (*composeHandler)(nil)

type quadletHandler struct {
	name           string
	rw             fileio.ReadWriter
	volumeProvider volumeProvider
}

func (b *quadletHandler) Dependencies() []string {
	return []string{"podman"}
}

func (b *quadletHandler) Verify(ctx context.Context, path string) error {
	return ensureQuadlet(b.rw, path)
}

func (b *quadletHandler) Install(ctx context.Context) error {
	if err := installQuadlet(b.rw, b.AppPath(), b.ID()); err != nil {
		return fmt.Errorf("installing quadlet: %w", err)
	}
	return nil
}

func (b *quadletHandler) Remove(ctx context.Context) error {
	return nil
}

func (b *quadletHandler) AppPath() string {
	return filepath.Join(lifecycle.QuadletAppPath, b.name)
}

func (b *quadletHandler) ID() string {
	return client.NewComposeID(b.name)
}

func (b *quadletHandler) Volumes() ([]*Volume, error) {
	return b.volumeProvider()
}

type composeHandler struct {
	name string
	rw   fileio.ReadWriter
	log  *log.PrefixLogger
	vm   VolumeManager
}

func (b *composeHandler) Dependencies() []string {
	return []string{"docker-compose", "podman-compose"}
}

func (b *composeHandler) Verify(ctx context.Context, path string) error {
	if err := ensureCompose(b.rw, path); err != nil {
		return fmt.Errorf("ensuring compose: %w", err)
	}
	return nil
}

func (b *composeHandler) Install(ctx context.Context) error {
	if err := writeComposeOverride(b.log, b.AppPath(), b.vm, b.rw, client.ComposeOverrideFilename); err != nil {
		return fmt.Errorf("writing override file %w", err)
	}
	return nil
}

func (b *composeHandler) Remove(ctx context.Context) error {
	return nil
}

func (b *composeHandler) AppPath() string {
	return filepath.Join(lifecycle.ComposeAppPath, b.name)
}

func (b *composeHandler) ID() string {
	return client.NewComposeID(b.name)
}

func (b *composeHandler) Volumes() ([]*Volume, error) {
	return nil, nil
}
