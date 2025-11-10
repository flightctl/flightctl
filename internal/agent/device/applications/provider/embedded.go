package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

type embeddedProvider struct {
	podman     *client.Podman
	readWriter fileio.ReadWriter
	log        *log.PrefixLogger
	spec       *ApplicationSpec
}

func newEmbedded(log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, name string, appType v1alpha1.AppType) (Provider, error) {
	volumeManager, err := NewVolumeManager(log, name, nil)
	if err != nil {
		return nil, err
	}

	appPath, err := pathFromAppType(appType, name, true)
	if err != nil {
		return nil, fmt.Errorf("getting app path: %w", err)
	}

	return &embeddedProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		spec: &ApplicationSpec{
			Name:     name,
			ID:       client.NewComposeID(name),
			AppType:  appType,
			Embedded: true,
			EnvVars:  make(map[string]string),
			Volume:   volumeManager,
			Path:     appPath,
		},
	}, nil
}

func (p *embeddedProvider) OCITargets(pullSecret *client.PullSecret) ([]dependency.OCIPullTarget, error) {
	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		spec, err := client.ParseComposeSpecFromDir(p.readWriter, p.spec.Path)
		if err != nil {
			return nil, fmt.Errorf("parsing compose spec: %w", err)
		}
		// extract images from service
		var targets []dependency.OCIPullTarget
		for _, svc := range spec.Services {
			if svc.Image != "" {
				targets = append(targets, dependency.OCIPullTarget{
					Type:       dependency.OCITypeImage,
					Reference:  svc.Image,
					PullPolicy: v1alpha1.PullIfNotPresent,
					PullSecret: pullSecret,
				})
			}
		}

		return targets, nil
	case v1alpha1.AppTypeQuadlet:
		quadlets, err := client.ParseQuadletReferencesFromDir(p.readWriter, filepath.Join(lifecycle.EmbeddedQuadletAppPath, p.spec.Name))
		if err != nil {
			return nil, fmt.Errorf("parsing quadlet spec: %w", err)
		}
		var targets []dependency.OCIPullTarget
		for _, quad := range quadlets {
			targets = append(targets, extractQuadletTargets(quad, pullSecret)...)
		}
		return targets, nil
	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}
}

func (p *embeddedProvider) Verify(ctx context.Context) error {
	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		if err := ensureCompose(p.readWriter, p.spec.Path); err != nil {
			return fmt.Errorf("ensuring compose: %w", err)
		}
	case v1alpha1.AppTypeQuadlet:
		if err := ensureQuadlet(p.readWriter, filepath.Join(lifecycle.EmbeddedQuadletAppPath, p.spec.Name)); err != nil {
			return fmt.Errorf("ensuring quadlet: %w", err)
		}
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}
	return nil
}

func (p *embeddedProvider) Name() string {
	return p.spec.Name
}

func (p *embeddedProvider) Spec() *ApplicationSpec {
	return p.spec
}

func (p *embeddedProvider) Install(ctx context.Context) error {
	switch p.spec.AppType {
	case v1alpha1.AppTypeQuadlet:
		// quadlet apps must be moved from their embedded location into the default
		// systemd location. A symlink can't be used as the installed contents must be mutated
		// to abide by flightctl's namespacing rules
		if err := p.readWriter.CopyDir(filepath.Join(lifecycle.EmbeddedQuadletAppPath, p.spec.Name), p.spec.Path, fileio.WithFollowSymlinkWithinRoot()); err != nil {
			return fmt.Errorf("copying embedded directory to real path: %w", err)
		}

		if err := installQuadlet(p.readWriter, p.spec.Path, p.spec.ID); err != nil {
			return fmt.Errorf("installing quadlet: %w", err)
		}
	case v1alpha1.AppTypeCompose:
		return nil
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}
	return nil
}

func (p *embeddedProvider) Remove(ctx context.Context) error {
	switch p.spec.AppType {
	case v1alpha1.AppTypeQuadlet:
		if err := p.readWriter.RemoveAll(p.spec.Path); err != nil {
			return fmt.Errorf("removing application: %w", err)
		}
	case v1alpha1.AppTypeCompose:
		return nil
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}
	return nil
}
