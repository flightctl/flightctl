package provider

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

type embeddedProvider struct {
	podman         *client.Podman
	readWriter     fileio.ReadWriter
	log            *log.PrefixLogger
	commandChecker commandChecker
	spec           *ApplicationSpec
	handler        embeddedAppTypeHandler
}

func newEmbeddedHandler(appType v1beta1.AppType, name string, rw fileio.ReadWriter, l *log.PrefixLogger, podman *client.Podman, bootTime string, installed bool, checker commandChecker) (embeddedAppTypeHandler, error) {
	switch appType {
	case v1beta1.AppTypeQuadlet:
		return &embeddedQuadletBehavior{
			name:           name,
			rw:             rw,
			log:            l,
			bootTime:       bootTime,
			installed:      installed,
			podman:         podman,
			commandChecker: checker,
		}, nil
	case v1beta1.AppTypeCompose:
		return &embeddedComposeBehavior{
			name:           name,
			rw:             rw,
			commandChecker: checker,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func newEmbedded(log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, name string, appType v1beta1.AppType, bootTime string, installed bool) (appProvider, error) {
	checker := client.IsCommandAvailable
	handler, err := newEmbeddedHandler(appType, name, readWriter, log, podman, bootTime, installed, checker)
	if err != nil {
		return nil, fmt.Errorf("constructing embedded app handler: %w", err)
	}

	volumeManager, err := NewVolumeManager(log, name, appType, v1beta1.CurrentProcessUsername, nil)
	if err != nil {
		return nil, err
	}

	volumes, err := handler.Volumes()
	if err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}
	volumeManager.AddVolumes(volumes)

	return &embeddedProvider{
		log:            log,
		podman:         podman,
		readWriter:     readWriter,
		commandChecker: checker,
		handler:        handler,
		spec: &ApplicationSpec{
			Name:     name,
			ID:       handler.ID(),
			AppType:  appType,
			User:     v1beta1.CurrentProcessUsername,
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

func (p *embeddedProvider) ID() string {
	return p.spec.ID
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

func (p *embeddedProvider) EnsureDependencies(ctx context.Context) error {
	return p.handler.EnsureDependencies(ctx)
}

func (p *embeddedProvider) collectOCITargets(ctx context.Context, configProvider dependency.PullConfigResolver) (dependency.OCIPullTargetsByUser, error) {
	targets, err := p.handler.CollectOCITargets(ctx, configProvider)
	if err != nil {
		return nil, err
	}
	var result dependency.OCIPullTargetsByUser
	return result.Add(v1beta1.CurrentProcessUsername, targets...), nil
}

func (p *embeddedProvider) extractNestedTargets(_ context.Context, _ dependency.PullConfigResolver) (*AppData, error) {
	// Embedded apps don't have nested targets to extract
	return &AppData{}, nil
}

func (p *embeddedProvider) parentIsAvailable(_ context.Context) (string, string, bool, error) {
	// Embedded apps have no parent artifact, always available
	return "", "", true, nil
}
