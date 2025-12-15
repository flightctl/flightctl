package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

type appTypeHandler interface {
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
var _ appTypeHandler = (*containerHandler)(nil)

type quadletHandler struct {
	name           string
	rw             fileio.ReadWriter
	log            *log.PrefixLogger
	volumeProvider volumeProvider
	specVolumes    []v1beta1.ApplicationVolume
}

func (b *quadletHandler) Verify(ctx context.Context, path string) error {
	if err := ensureDependenciesFromAppType([]string{"podman"}); err != nil {
		return fmt.Errorf("ensuring dependencies: %w", err)
	}
	if err := ensureQuadlet(b.rw, path); err != nil {
		return fmt.Errorf("ensuring quadlet: %w", err)
	}

	// only support image volumes
	if err := ensureImageVolumes(b.specVolumes); err != nil {
		return fmt.Errorf("ensuring volumes: %w", err)
	}
	return nil
}

func (b *quadletHandler) Install(ctx context.Context) error {
	if err := installQuadlet(b.rw, b.log, b.AppPath(), b.ID()); err != nil {
		return fmt.Errorf("installing quadlet: %w", err)
	}
	return nil
}

func (b *quadletHandler) Remove(ctx context.Context) error {
	path := filepath.Join(lifecycle.QuadletTargetPath, quadlet.NamespaceResource(b.ID(), lifecycle.QuadletTargetName))
	if err := b.rw.RemoveFile(path); err != nil {
		return fmt.Errorf("removing quadlet target file: %w", err)
	}
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
	name        string
	rw          fileio.ReadWriter
	log         *log.PrefixLogger
	vm          VolumeManager
	specVolumes []v1beta1.ApplicationVolume
}

func (b *composeHandler) Verify(ctx context.Context, path string) error {
	if err := ensureDependenciesFromAppType([]string{"docker-compose", "podman-compose"}); err != nil {
		return fmt.Errorf("ensuring dependencies: %w", err)
	}
	if err := ensureCompose(b.rw, path); err != nil {
		return fmt.Errorf("ensuring compose: %w", err)
	}

	// only support image volumes
	if err := ensureImageVolumes(b.specVolumes); err != nil {
		return fmt.Errorf("ensuring volumes: %w", err)
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

// containers reuse a lot of quadlet functionality
type containerHandler struct {
	name   string
	rw     fileio.ReadWriter
	log    *log.PrefixLogger
	podman *client.Podman
	spec   *v1beta1.ImageApplicationProviderSpec
}

func (b *containerHandler) Verify(ctx context.Context, path string) error {
	errs := v1beta1.ValidateContainerImageApplicationSpec(b.name, b.spec)
	if err := ensureDependenciesFromAppType([]string{"podman"}); err != nil {
		errs = append(errs, fmt.Errorf("ensuring dependencies: %w", err))
	}
	for _, vol := range lo.FromPtr(b.spec.Volumes) {
		volType, err := vol.Type()
		if err != nil {
			errs = append(errs, fmt.Errorf("volume type: %w", err))
			continue
		}
		switch volType {
		// mount and image_mount are supported to allow creating volumes within containers. The regular image provider
		// is not allowed as it does not specify where it should be mounted.
		case v1beta1.MountApplicationVolumeProviderType, v1beta1.ImageMountApplicationVolumeProviderType:
			break
		default:
			errs = append(errs, fmt.Errorf("%w: container %s", errors.ErrUnsupportedVolumeType, volType))
		}
	}
	return errors.Join(errs...)
}

func (b *containerHandler) Install(ctx context.Context) error {
	if err := generateQuadlet(ctx, b.podman, b.rw, b.AppPath(), b.spec); err != nil {
		return fmt.Errorf("generating quadlet: %w", err)
	}

	if err := installQuadlet(b.rw, b.log, b.AppPath(), b.ID()); err != nil {
		return fmt.Errorf("installing container: %w", err)
	}
	return nil
}

func (b *containerHandler) Remove(ctx context.Context) error {
	path := filepath.Join(lifecycle.QuadletTargetPath, quadlet.NamespaceResource(b.ID(), lifecycle.QuadletTargetName))
	if err := b.rw.RemoveFile(path); err != nil {
		return fmt.Errorf("removing container target file: %w", err)
	}
	return nil
}

func (b *containerHandler) AppPath() string {
	return filepath.Join(lifecycle.QuadletAppPath, b.name)
}

func (b *containerHandler) ID() string {
	return client.NewComposeID(b.name)
}

func (b *containerHandler) Volumes() ([]*Volume, error) {
	return nil, nil
}

func ensureImageVolumes(vols []v1beta1.ApplicationVolume) error {
	for _, vol := range vols {
		volType, err := vol.Type()
		if err != nil {
			return fmt.Errorf("volume type: %w", err)
		}
		if volType != v1beta1.ImageApplicationVolumeProviderType {
			return fmt.Errorf("%w: %s", errors.ErrUnsupportedVolumeType, volType)
		}
	}
	return nil
}
