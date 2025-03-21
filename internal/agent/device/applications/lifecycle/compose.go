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
	appName := action.Name
	projectName := action.ID
	c.log.Debugf("Starting application: %s projectName: %s", appName, projectName)

	noRecreate := true
	if err := c.podman.Compose().UpFromWorkDir(ctx, action.Path, projectName, noRecreate); err != nil {
		return err
	}

	c.log.Infof("Started application: %s", appName)
	return nil
}

func (c *Compose) remove(ctx context.Context, action *Action) error {
	appName := action.Name
	c.log.Debugf("Removing application: %s projectName: %s", appName, action.ID)

	if err := c.stopAndRemoveContainers(ctx, action); err != nil {
		return err
	}

	c.log.Infof("Removed application: %s", appName)
	return nil
}

func (c *Compose) update(ctx context.Context, action *Action) error {
	appName := action.Name
	c.log.Debugf("Updating application: %s projectName: %s", appName, action.ID)

	if err := c.stopAndRemoveContainers(ctx, action); err != nil {
		return err
	}

	// change to work dir and run `docker compose up -d`
	projectName := action.ID
	noRecreate := true
	if err := c.podman.Compose().UpFromWorkDir(ctx, action.Path, projectName, noRecreate); err != nil {
		return err
	}

	c.log.Infof("Updated application: %s", action.Name)

	return nil
}

// stopAndRemoveContainers stops and removes all containers and networks created by the compose application.
func (c *Compose) stopAndRemoveContainers(ctx context.Context, action *Action) error {
	var errs []error

	// project name is derived from the application ID
	projectName := action.ID
	labels := []string{fmt.Sprintf("com.docker.compose.project=%s", projectName)}
	networks, err := c.podman.ListNetworks(ctx, labels)
	if err != nil {
		errs = append(errs, err)
	}

	if err := c.podman.StopContainers(ctx, labels); err != nil {
		errs = append(errs, err)
	}
	if err := c.podman.RemoveContainer(ctx, labels); err != nil {
		errs = append(errs, err)
	}
	if err := c.podman.RemoveNetworks(ctx, networks...); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
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
