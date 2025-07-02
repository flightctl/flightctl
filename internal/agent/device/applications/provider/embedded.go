package provider

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
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
	return &embeddedProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		spec: &ApplicationSpec{
			Name:     name,
			AppType:  appType,
			Embedded: true,
			EnvVars:  make(map[string]string),
			Volume:   volumeManager,
		},
	}, nil
}

func (p *embeddedProvider) Verify(ctx context.Context) error {
	appPath, err := pathFromAppType(p.spec.AppType, p.spec.Name, p.spec.Embedded)
	if err != nil {
		return fmt.Errorf("getting app path: %w", err)
	}
	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		p.spec.ID = client.NewComposeID(p.spec.Name)
		p.spec.Path = appPath
		if err := ensureCompose(ctx, p.log, p.podman, p.readWriter, appPath); err != nil {
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
