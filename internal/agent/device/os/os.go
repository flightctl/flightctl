package os

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
)

type Client interface {
	// Status retrieves the current OS status.
	Status(ctx context.Context) (*Status, error)
	// Switch prepares the system to switch to the specified OS image.
	Switch(ctx context.Context, image string) error
	// Apply applies the OS changes, potentially triggering a reboot.
	Apply(ctx context.Context) error
}

type Manager interface {
	BeforeUpdate(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error
	AfterUpdate(ctx context.Context, desired *v1alpha1.DeviceSpec) error
	Reboot(ctx context.Context, desired *v1alpha1.DeviceSpec) error
	status.Exporter
}

// NewManager creates a new os manager.
func NewManager(log *log.PrefixLogger, client Client, podmanClient *client.Podman) Manager {
	return &manager{
		client:       client,
		podmanClient: podmanClient,
		log:          log,
	}
}

type manager struct {
	client       Client
	podmanClient *client.Podman
	log          *log.PrefixLogger
}

func (m *manager) Status(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	bootcInfo, err := m.client.Status(ctx)
	if err != nil {
		return err
	}

	status.Os.Image = bootcInfo.GetBootedImage()
	status.Os.ImageDigest = bootcInfo.GetBootedImageDigest()
	return nil
}

func (m *manager) BeforeUpdate(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error {
	if desired.Os == nil {
		return nil
	}

	osImage := desired.Os.Image
	now := time.Now()
	m.log.Infof("Fetching OS image: %s", osImage)

	_, err := m.podmanClient.Pull(ctx, osImage, client.WithRetry())
	if err != nil {
		return err
	}
	m.log.Infof("Fetched OS image: %s in %s", osImage, time.Since(now))

	return nil
}

func (m *manager) AfterUpdate(ctx context.Context, desired *v1alpha1.DeviceSpec) error {
	if desired.Os == nil {
		return nil
	}
	osImage := desired.Os.Image
	return m.client.Switch(ctx, osImage)
}

func (m *manager) Reboot(ctx context.Context, desired *v1alpha1.DeviceSpec) error {
	return m.client.Apply(ctx)
}

type Status struct {
	container.BootcHost
}
