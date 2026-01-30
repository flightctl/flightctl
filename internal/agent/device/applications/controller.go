package applications

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/pkg/log"
)

type Controller struct {
	podmanFactory client.PodmanFactory
	clients       client.CLIClients
	rwFactory     fileio.ReadWriterFactory
	specManager   spec.Manager
	manager       Manager
	log           *log.PrefixLogger
	bootTime      string
}

func NewController(
	podmanFactory client.PodmanFactory,
	clients client.CLIClients,
	manager Manager,
	rwFactory fileio.ReadWriterFactory,
	log *log.PrefixLogger,
	bootTime string,
	specManager spec.Manager,
) *Controller {
	return &Controller{
		log:           log,
		manager:       manager,
		podmanFactory: podmanFactory,
		clients:       clients,
		rwFactory:     rwFactory,
		specManager:   specManager,
		bootTime:      bootTime,
	}
}

func (c *Controller) Sync(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	c.log.Debug("Syncing device applications")
	defer c.log.Debug("Finished syncing device applications")

	osUpdatePending, err := c.specManager.IsOSUpdatePending(ctx)
	if err != nil {
		return fmt.Errorf("checking OS update status: %w", err)
	}

	currentAppProviders, err := provider.FromDeviceSpec(
		ctx,
		c.log,
		c.podmanFactory,
		c.clients,
		c.rwFactory,
		current,
		provider.WithInstalledEmbedded(),
	)
	if err != nil {
		return fmt.Errorf("current %w: %w", errors.ErrAppProviders, err)
	}

	desiredAppProviders, err := provider.FromDeviceSpec(
		ctx,
		c.log,
		c.podmanFactory,
		c.clients,
		c.rwFactory,
		desired,
		provider.WithEmbedded(c.bootTime),
	)
	if err != nil {
		return fmt.Errorf("desired %w: %w", errors.ErrAppProviders, err)
	}

	return syncProviders(ctx, c.log, c.manager, currentAppProviders, desiredAppProviders, osUpdatePending)
}

func syncProviders(
	ctx context.Context,
	log *log.PrefixLogger,
	manager Manager,
	currentProviders, desiredProviders []provider.Provider,
	osUpdatePending bool,
) error {
	diff, err := provider.GetDiff(currentProviders, desiredProviders)
	if err != nil {
		return err
	}

	for _, p := range diff.Removed {
		log.Debugf("Removing application: %s", p.Name())
		if err := manager.Remove(ctx, p); err != nil {
			return err
		}
	}

	for _, p := range diff.Ensure {
		if err := p.EnsureDependencies(ctx); err != nil {
			if isDeferredError(osUpdatePending, err) {
				log.Infof("Deferring ensuring app %s until after OS update: %v", p.Name(), err)
				continue
			}
			return fmt.Errorf("ensure dependencies for %s: %w", p.Name(), err)
		}
		log.Debugf("Ensuring application: %s", p.Name())
		if err := manager.Ensure(ctx, p); err != nil {
			return err
		}
	}

	for _, p := range diff.Changed {
		if err := p.EnsureDependencies(ctx); err != nil {
			if isDeferredError(osUpdatePending, err) {
				log.Infof("Deferring app update %s until after OS update: %v", p.Name(), err)
				continue
			}
			return fmt.Errorf("ensure dependencies for %s: %w", p.Name(), err)
		}
		log.Debugf("Updating application: %s", p.Name())
		if err := manager.Update(ctx, p); err != nil {
			return err
		}
	}

	return nil
}
