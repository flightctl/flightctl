package applications

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

type Controller struct {
	podmanFactory client.PodmanFactory
	clients       client.CLIClients
	rwFactory     fileio.ReadWriterFactory
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
) *Controller {
	return &Controller{
		log:           log,
		manager:       manager,
		podmanFactory: podmanFactory,
		clients:       clients,
		rwFactory:     rwFactory,
		bootTime:      bootTime,
	}
}

func (c *Controller) Sync(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	c.log.Debug("Syncing device applications")
	defer c.log.Debug("Finished syncing device applications")

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

	return syncProviders(ctx, c.log, c.manager, currentAppProviders, desiredAppProviders)
}

func syncProviders(
	ctx context.Context,
	log *log.PrefixLogger,
	manager Manager,
	currentProviders, desiredProviders []provider.Provider,
) error {
	diff, err := provider.GetDiff(currentProviders, desiredProviders)
	if err != nil {
		return err
	}

	var errs []error

	for _, p := range diff.Removed {
		log.Debugf("Removing application: %s", p.Name())
		if err := manager.Remove(ctx, p); err != nil {
			log.Warnf("Failed to remove application %s: %v", p.Name(), err)
			// Missing dependencies is acceptable for removed applications. If the spec defines removing a helm application
			// at the same time that it changes OSs to one that does not have helm app support, then we accept it without failure
			if !errors.Is(err, errors.ErrAppDependency) {
				errs = append(errs, fmt.Errorf("removing: %w: %w", errors.WithElement(p.Name()), err))
			}
		}
	}

	for _, p := range diff.Ensure {
		log.Debugf("Ensuring application: %s", p.Name())
		if err := manager.Ensure(ctx, p); err != nil {
			log.Warnf("Failed to ensure application %s: %v", p.Name(), err)
			errs = append(errs, fmt.Errorf("ensuring: %w: %w", errors.WithElement(p.Name()), err))
		}
	}

	for _, p := range diff.Changed {
		log.Debugf("Updating application: %s", p.Name())
		if err := manager.Update(ctx, p); err != nil {
			log.Warnf("Failed to update application %s: %v", p.Name(), err)
			errs = append(errs, fmt.Errorf("updating: %w: %w", errors.WithElement(p.Name()), err))
		}
	}

	return errors.Join(errs...)
}
