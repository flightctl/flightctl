package client

import (
	"context"
	"fmt"

	"github.com/containers/image/v5/docker/reference"
	ostree "github.com/coreos/rpmostree-client-go/pkg/client"
	"github.com/coreos/rpmostree-client-go/pkg/imgref"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
)

type RPMOSTree struct {
	client *ostree.Client
	exec   executer.Executer
}

// NewRPMOSTree creates a new rpm-ostree client.
func NewRPMOSTree(exec executer.Executer) *RPMOSTree {
	client := ostree.NewClient("flightctl-agent")
	return &RPMOSTree{
		client: &client,
		exec:   exec,
	}
}

func (c *RPMOSTree) Switch(_ context.Context, target string) error {
	imageRef, err := reference.Parse(target)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}

	return c.client.RebaseToContainerImageAllowUnsigned(imageRef)
}

func (c *RPMOSTree) Apply(ctx context.Context) error {
	_, stderr, exitCode := c.exec.ExecuteWithContext(ctx, "systemctl", "reboot")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if exitCode != 0 {
		return fmt.Errorf("systemctl reboot: %s", stderr)
	}
	return nil
}

func (c *RPMOSTree) Status(_ context.Context) (*container.BootcHost, error) {
	status, err := c.client.QueryStatus()
	if err != nil {
		return nil, err
	}

	bootedDeployment, err := status.GetBootedDeployment()
	if err != nil {
		return nil, err
	}

	booted, err := deploymentToImageStatus(bootedDeployment)
	if err != nil {
		return nil, fmt.Errorf("booted: %w", err)
	}
	rollback, err := deploymentToImageStatus(status.GetRollbackDeployment())
	if err != nil {
		return nil, fmt.Errorf("rollback: %w", err)
	}
	staged, err := deploymentToImageStatus(status.GetStagedDeployment())
	if err != nil {
		return nil, fmt.Errorf("staged: %w", err)
	}

	return &container.BootcHost{
		Status: container.Status{
			Staged:   staged,
			Booted:   booted,
			Rollback: rollback,
		},
	}, nil
}

func (c *RPMOSTree) Rollback(ctx context.Context) error {
	_, stderr, exitCode := c.exec.ExecuteWithContext(ctx, "rpm-ostree", "rollback")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if exitCode != 0 {
		return fmt.Errorf("rpm-ostree rollback: %s", stderr)
	}
	return nil
}

func (c *RPMOSTree) RemoveRollback(_ context.Context) error {
	return c.client.RemovePendingDeployment()
}

func (c *RPMOSTree) RemoveStaged(_ context.Context) error {
	return c.client.RemovePendingDeployment()
}

// // deploymentToImageStatus converts an ostree Deployment to ImageStatus.
func deploymentToImageStatus(deployment *ostree.Deployment) (container.ImageStatus, error) {
	if deployment == nil {
		return container.ImageStatus{}, nil
	}
	image, err := imgref.Parse(deployment.ContainerImageReference)
	if err != nil {
		return container.ImageStatus{}, fmt.Errorf("parsing image reference: %w", err)
	}

	return container.ImageStatus{
		Image: container.ImageDetails{
			Image: container.ImageSpec{
				Image: image.Imgref.Image,
			},
		},
	}, nil
}
