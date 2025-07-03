package os

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
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
func NewManager(
	log *log.PrefixLogger,
	prefetchManager dependency.PrefetchManager,
	client Client,
	readWriter fileio.ReadWriter,
	podmanClient *client.Podman,
) Manager {
	return &manager{
		client:          client,
		prefetchManager: prefetchManager,
		podmanClient:    podmanClient,
		readWriter:      readWriter,
		log:             log,
	}
}

type manager struct {
	client          Client
	prefetchManager dependency.PrefetchManager
	podmanClient    *client.Podman
	readWriter      fileio.ReadWriter
	log             *log.PrefixLogger
}

func (m *manager) Status(ctx context.Context, status *v1alpha1.DeviceStatus, _ ...status.CollectorOpt) error {
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

	target := dependency.OCIPullTarget{
		Type:       dependency.OCITypeImage,
		Reference:  osImage,
		PullPolicy: v1alpha1.PullIfNotPresent,
	}

	// auth
	secret, found, err := client.ResolvePullSecret(m.log, m.readWriter, desired, authPath)
	if err != nil {
		return err
	}
	if found {
		defer secret.Cleanup()
		target.PullSecret = secret
	}

	// if the os image is not ready we will return a retryable error
	if err := m.prefetchManager.EnsureScheduled(ctx, []dependency.OCIPullTarget{target}); err != nil {
		return fmt.Errorf("os prefetch scheduled: %w", err)
	}

	m.log.Infof("OS image pulled successfully")
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
