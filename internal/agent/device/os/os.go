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

type Manager interface {
	BeforeUpdate(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error
	AfterUpdate(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error
	Reboot(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error

	status.Exporter
}

func NewManager(log *log.PrefixLogger, bootcClient container.BootcClient, podmanClient *client.Podman) Manager {
	return &manager{
		bootcClient:  bootcClient,
		podmanClient: podmanClient,
		log:          log,
	}
}

type manager struct {
	bootcClient  container.BootcClient
	podmanClient *client.Podman
	log          *log.PrefixLogger
}

func (m *manager) Initialize() error {
	return nil
}

func (m *manager) Status(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	bootcInfo, err := m.bootcClient.Status(ctx)
	if err != nil {
		return err
	}

	status.Os.Image = bootcInfo.GetBootedImage()
	status.Os.ImageDigest = bootcInfo.GetBootedImageDigest()
	return nil
}

func (m *manager) BeforeUpdate(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
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

func (m *manager) AfterUpdate(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	if desired.Os == nil {
		return nil
	}
	osImage := desired.Os.Image
	return m.bootcClient.Switch(ctx, osImage)
}

func (m *manager) Reboot(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	return m.bootcClient.Apply(ctx)
}
