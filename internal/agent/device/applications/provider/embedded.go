package provider

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
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
					PullPolicy: v1alpha1.PullAlways,
					PullSecret: pullSecret,
				})
			}
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
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}
	return nil
}

func (e *embeddedProvider) Name() string {
	return e.spec.Name
}

func (e *embeddedProvider) Spec() *ApplicationSpec {
	return e.spec
}

func (e *embeddedProvider) Install(ctx context.Context) error {
	return nil
}

func (e *embeddedProvider) Update(ctx context.Context) error {
	return nil
}

func (e *embeddedProvider) Remove(ctx context.Context) error {
	return nil
}
