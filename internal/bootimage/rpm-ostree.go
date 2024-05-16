package bootimage

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	"github.com/coreos/rpmostree-client-go/pkg/client"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	CmdRpmOsTree = "rpm-ostree"
)

var _ Manager = (*OsTreeClient)(nil)

type RpmOsTreeStatus struct {
	Deployments []*client.Deployment `json:"deployments"`
}

type OsTreeClient struct {
	client   *client.Client
	executer executer.Executer
}

// NewOsTreeClient creates a new rpm-ostree client.
func NewOsTreeClient(id string, executer executer.Executer) (*OsTreeClient, error) {
	_, err := exec.LookPath(CmdRpmOsTree)
	if err != nil {
		return nil, fmt.Errorf("look path failed: %w", err)
	}
	client := client.NewClient(id)
	return &OsTreeClient{
		client:   &client,
		executer: executer,
	}, nil
}

func (c *OsTreeClient) Switch(_ context.Context, target string) error {
	imageRef, err := reference.ParseNamed(target)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}

	return c.client.RebaseToContainerImageAllowUnsigned(imageRef)
}

func (c *OsTreeClient) Apply(ctx context.Context) error {
	_, stderr, exitCode := c.executer.ExecuteWithContext(ctx, "systemctl", "reboot")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if exitCode != 0 {
		return fmt.Errorf("systemctl reboot: %s", stderr)
	}
	return nil
}

func (c *OsTreeClient) Status(_ context.Context) (*HostStatus, error) {
	status, err := c.client.QueryStatus()
	if err != nil {
		return nil, err
	}

	staged, err := getOsTreeBootEntry(status.GetStagedDeployment())
	if err != nil {
		return nil, err
	}

	bootedDeployment, err := status.GetBootedDeployment()
	if err != nil {
		return nil, err
	}

	booted, err := getOsTreeBootEntry(bootedDeployment)
	if err != nil {
		return nil, err
	}

	rollback, err := getOsTreeBootEntry(status.GetRollbackDeployment())
	if err != nil {
		return nil, err
	}

	return &HostStatus{
		Staged:   *staged,
		Booted:   *booted,
		Rollback: *rollback,
		Type:     ImageManagerRpmOsTree.String(),
	}, nil
}

func (c *OsTreeClient) RemoveRollback(_ context.Context) error {
	return c.client.RemovePendingDeployment()
}

func (c *OsTreeClient) RemoveStaged(_ context.Context) error {
	return c.client.RemovePendingDeployment()
}

func (c *OsTreeClient) IsDisabled() bool {
	return false
}

// getOsTreeImageStatus returns ImageStatus given an rpm-ostree Deployment
func getOsTreeBootEntry(deployment *client.Deployment) (*BootEntry, error) {
	image := parseImageReference(deployment.ContainerImageReference)
	return &BootEntry{
		Pinned: deployment.Pinned,
		Image: ImageStatus{
			Image: ImageReference{
				Image: image,
			},
			Version:     deployment.Version,
			ImageDigest: deployment.ContainerImageReferenceDigest,
		},
		Ostree: BootEntryOstree{
			Checksum:     deployment.Checksum,
			DeploySerial: int(deployment.Serial),
		},
	}, nil
}

// TODO: there is probably a supported way to do this in the containers/image library
func parseImageReference(imageReference string) string {
	parts := strings.SplitN(imageReference, ":", 2)
	if len(parts) > 1 {
		return parts[1]
	}
	return imageReference
}
