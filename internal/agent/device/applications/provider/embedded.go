package provider

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1beta1"
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
	handler    embeddedAppTypeHandler
}

func newEmbeddedHandler(appType v1beta1.AppType, name string, rw fileio.ReadWriter, l *log.PrefixLogger, bootTime string, installed bool) (embeddedAppTypeHandler, error) {
	switch appType {
	case v1beta1.AppTypeQuadlet:
		return &embeddedQuadletBehavior{
			name:      name,
			rw:        rw,
			log:       l,
			bootTime:  bootTime,
			installed: installed,
		}, nil
	case v1beta1.AppTypeCompose:
		return &embeddedComposeBehavior{
			name: name,
			rw:   rw,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func newEmbedded(log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, name string, appType v1beta1.AppType, bootTime string, installed bool) (Provider, error) {
	handler, err := newEmbeddedHandler(appType, name, readWriter, log, bootTime, installed)
	if err != nil {
		return nil, fmt.Errorf("constructing embedded app handler: %w", err)
	}

	volumeManager, err := NewVolumeManager(log, name, appType, nil)
	if err != nil {
		return nil, err
	}

	volumes, err := handler.Volumes()
	if err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}
	volumeManager.AddVolumes(volumes)

	return &embeddedProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		handler:    handler,
		spec: &ApplicationSpec{
			Name:     name,
			ID:       handler.ID(),
			AppType:  appType,
			Embedded: true,
			EnvVars:  make(map[string]string),
			Volume:   volumeManager,
			Path:     handler.AppPath(),
			bootTime: bootTime,
		},
	}, nil
}

func (p *embeddedProvider) Verify(ctx context.Context) error {
	return p.handler.Verify(ctx)
}

func (p *embeddedProvider) Name() string {
	return p.spec.Name
}

func (p *embeddedProvider) Spec() *ApplicationSpec {
	return p.spec
}

func (p *embeddedProvider) Install(ctx context.Context) error {
	return p.handler.Install(ctx)
}

func (p *embeddedProvider) Remove(ctx context.Context) error {
	return p.handler.Remove(ctx)
}
