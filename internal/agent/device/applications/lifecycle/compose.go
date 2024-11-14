package lifecycle

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	ComposeAppPath         = "/etc/compose/manifests"
	EmbeddedComposeAppPath = "/usr/local/etc/compose/manifests"
)

var _ ActionHandler = (*Compose)(nil)

type Compose struct {
	podman *client.Podman
	log    *log.PrefixLogger
}

func NewCompose(log *log.PrefixLogger, podman *client.Podman) *Compose {
	return &Compose{
		podman: podman,
		log:    log,
	}
}

func (c *Compose) add(ctx context.Context, action *Action) error {
	c.log.Debugf("Starting application %s", action.Name)
	appPath, err := action.ApplicationPath()
	if err != nil {
		return err
	}

	noRecreate := true
	if err := c.podman.Compose().UpFromWorkDir(ctx, appPath, noRecreate); err != nil {
		return err
	}

	c.log.Infof("Started application %s", action.Name)
	return nil
}

func (c *Compose) remove(ctx context.Context, action *Action) error {
	c.log.Debugf("Removing application %s", action.Name)

	// by using podman directly we can avoid the need to parse the compose file.
	// this makes the reconciliation process faster and more reliable as the
	// compose file is not required.
	labels := []string{fmt.Sprintf("com.docker.compose.project=%s", action.Name)}

	var errs []error
	// get networks from the running containers for the app
	networks, err := c.podman.ListNetworks(ctx, labels)
	if err != nil {
		errs = append(errs, err)
	}

	// stop containers
	if err := c.podman.StopContainers(ctx, labels); err != nil {
		errs = append(errs, err)
	}

	// remove containers
	if err := c.podman.RemoveContainer(ctx, labels); err != nil {
		errs = append(errs, err)
	}

	// remove networks
	if err := c.podman.RemoveNetworks(ctx, networks...); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	c.log.Infof("Removed application %s", action.Name)
	return nil
}

func (c *Compose) update(ctx context.Context, action *Action) error {
	c.log.Debugf("Updating application %s", action.Name)

	appPath, err := action.ApplicationPath()
	if err != nil {
		return err
	}
	labels := []string{fmt.Sprintf("com.docker.compose.project=%s", action.Name)}

	var errs []error
	// get networks from the running containers for the app
	networks, err := c.podman.ListNetworks(ctx, labels)
	if err != nil {
		errs = append(errs, err)
	}

	// stop containers
	if err := c.podman.StopContainers(ctx, labels); err != nil {
		errs = append(errs, err)
	}

	// do not remove volumes as they are not removed by `docker-compose down`
	// this is to ensure that data is not lost when the application is updated

	// remove containers
	if err := c.podman.RemoveContainer(ctx, labels); err != nil {
		errs = append(errs, err)
	}

	// remove networks
	if err := c.podman.RemoveNetworks(ctx, networks...); err != nil {
		errs = append(errs, err)
	}

	// change to work dir and run `docker compose up -d`
	noRecreate := true
	if err := c.podman.Compose().UpFromWorkDir(ctx, appPath, noRecreate); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	c.log.Infof("Updated application %s", action.Name)

	return nil
}

func (c *Compose) Execute(ctx context.Context, action *Action) error {
	switch action.Type {
	case ActionAdd:
		return c.add(ctx, action)
	case ActionRemove:
		return c.remove(ctx, action)
	case ActionUpdate:
		return c.update(ctx, action)
	default:
		return fmt.Errorf("unsupported action type: %s", action.Type)
	}
}
