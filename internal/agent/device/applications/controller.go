package applications

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

type Controller struct {
	podman     *client.Podman
	readWriter fileio.ReadWriter
	manager    Manager
	log        *log.PrefixLogger
	bootTime   string
}

func NewController(
	podman *client.Podman,
	manager Manager,
	readWriter fileio.ReadWriter,
	log *log.PrefixLogger,
	bootTime string,
) *Controller {
	return &Controller{
		log:        log,
		manager:    manager,
		podman:     podman,
		readWriter: readWriter,
		bootTime:   bootTime,
	}
}

func (c *Controller) Sync(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	c.log.Debug("Syncing device applications")
	defer c.log.Debug("Finished syncing device applications")

	currentAppProviders, err := provider.FromDeviceSpec(
		ctx,
		c.log,
		c.podman,
		c.readWriter,
		current,
		provider.WithInstalledEmbedded(),
	)
	if err != nil {
		return fmt.Errorf("current app providers: %w", err)
	}

	desiredAppProviders, err := provider.FromDeviceSpec(
		ctx,
		c.log,
		c.podman,
		c.readWriter,
		desired,
		provider.WithEmbedded(c.bootTime),
	)
	if err != nil {
		return fmt.Errorf("desired app providers: %w", err)
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

	for _, provider := range diff.Removed {
		log.Debugf("Removing application: %s", provider.Name())
		if err := manager.Remove(ctx, provider); err != nil {
			return err
		}
	}

	for _, provider := range diff.Ensure {
		log.Debugf("Ensuring application: %s", provider.Name())
		if err := manager.Ensure(ctx, provider); err != nil {
			return err
		}
	}

	for _, provider := range diff.Changed {
		log.Debugf("Updating application: %s", provider.Name())
		if err := manager.Update(ctx, provider); err != nil {
			return err
		}
	}

	return nil
}
