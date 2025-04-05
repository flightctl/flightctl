package os

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	authPath = "/etc/ostree/auth.json"
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
func NewManager(log *log.PrefixLogger, client Client, reader fileio.Reader, podmanClient *client.Podman) Manager {
	return &manager{
		client:       client,
		podmanClient: podmanClient,
		reader:       reader,
		log:          log,
	}
}

type manager struct {
	client       Client
	podmanClient *client.Podman
	reader       fileio.Reader
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
	opts := []client.ClientOption{
		client.WithRetry(),
	}

	// check if the image is already booted or exists in container storage
	status, err := m.client.Status(ctx)
	if err != nil {
		return err
	}
	isDesiredImageRunning, err := container.IsOsImageReconciled(&status.BootcHost, desired)
	if err != nil {
		return err
	}
	if isDesiredImageRunning {
		// desired OS image is already booted, no need to pull it
		m.log.Debugf("Desired OS image is currently booted: %s", osImage)
		return nil
	}

	if m.podmanClient.ImageExists(ctx, osImage) {
		m.log.Debugf("OS image already exists in container storage: %s", osImage)
		return nil
	}

	now := time.Now()
	m.log.Infof("Fetching OS image: %s", osImage)

	// auth
	exists, err := m.reader.PathExists(authPath)
	if err != nil {
		return err
	}
	if exists {
		m.log.Infof("Using pull secret: %s", authPath)
		opts = append(opts, client.WithPullSecret(authPath))
	}

	_, err = m.podmanClient.Pull(ctx, osImage, opts...)
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
