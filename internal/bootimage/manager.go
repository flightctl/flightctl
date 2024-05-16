package bootimage

import (
	"context"
	"errors"
	"os/exec"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

// ImageManager is the type of the OS image manager
type ImageManager string

const (
	ImageManagerBootc     ImageManager = "bootc"
	ImageManagerRpmOsTree ImageManager = "rpm-ostree"
)

// String method to convert Manager to string
func (m ImageManager) String() string {
	return string(m)
}

type Manager interface {
	// Status returns the current boot image status.
	Status(context.Context) (*HostStatus, error)
	// Switch pulls the specified image and stages it for the next boot while retaining a copy of the most recently booted image.
	Switch(context.Context, string) error
	// Apply reboots the system to apply the staged image.
	Apply(context.Context) error
	// RemoveRollback removes the rollback image.
	RemoveRollback(context.Context) error
	// RemoveStaged removes the staged image.
	RemoveStaged(context.Context) error
	// IsDisabled returns true if the manager is disabled.
	IsDisabled() bool
}

type Client struct {
	Manager
	log *log.PrefixLogger
}

// NewClient creates a new boot image client.
func NewClient(executer executer.Executer, log *log.PrefixLogger) (*Client, error) {
	c := &Client{
		log: log,
	}

	bootcClient, err := NewBootcClient(log, executer)
	if err != nil {
		log.Debugf("failed to get bootc client: %v", err)
	} else {
		bootcStatus, err := bootcClient.Status(context.Background())
		if err != nil {
			log.Debugf("failed to get bootc status: %v", err)
		} else if bootcStatus.IsImageManagerSupported(ImageManagerBootc) {
			log.Info("Detected bootc client support")
			c.Manager = bootcClient
			return c, nil
		}
	}

	// Fallback to rpm-ostree client
	osTreeClient, err := NewOsTreeClient("flightctl-agent", executer)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			log.Infof("Bootc and rpm-ostree not found: os image reconciliation is disabled")
			c.Manager = &Disabled{}
			return c, nil
		}
	}

	log.Info("Detected rpm-ostree client support")
	c.Manager = osTreeClient

	return c, nil
}

var _ Manager = (*Disabled)(nil)

// Disabled is a disabled image manager implementation for cases where the host does not support bootc or rpm-ostree.
type Disabled struct{}

func (d *Disabled) Status(context.Context) (*HostStatus, error) {
	return nil, nil
}

func (d *Disabled) Switch(context.Context, string) error {
	return nil
}

func (d *Disabled) Apply(context.Context) error {
	return nil
}

func (d *Disabled) RemoveRollback(context.Context) error {
	return nil
}

func (d *Disabled) RemoveStaged(context.Context) error {
	return nil
}

func (d *Disabled) IsDisabled() bool {
	return true
}
