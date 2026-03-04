package os

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
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
	// Status retrieves the current OS status
	Status(ctx context.Context) (*Status, error)
	// Switch prepares the system to switch to the specified OS image
	Switch(ctx context.Context, image string) error
	// Apply applies the OS changes, potentially triggering a reboot
	Apply(ctx context.Context) error
	// Rollback swaps the boot order to the previous deployment
	Rollback(ctx context.Context) error
}

type Manager interface {
	BeforeUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error
	AfterUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error
	Reboot(ctx context.Context, desired *v1beta1.DeviceSpec) error

	dependency.OCICollector
	status.Exporter
}

// NewManager creates a new OS manager
func NewManager(
	log *log.PrefixLogger,
	client Client,
	readWriter fileio.ReadWriter,
	podmanClient *client.Podman,
	pullConfigResolver dependency.PullConfigResolver,
) Manager {
	return &manager{
		client:             client,
		podmanClient:       podmanClient,
		readWriter:         readWriter,
		pullConfigResolver: pullConfigResolver,
		log:                log,
	}
}

type manager struct {
	client             Client
	podmanClient       *client.Podman
	readWriter         fileio.ReadWriter
	pullConfigResolver dependency.PullConfigResolver
	log                *log.PrefixLogger
}

func (m *manager) Status(ctx context.Context, status *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	bootcInfo, err := m.client.Status(ctx)
	if err != nil {
		return err
	}

	status.Os.Image = bootcInfo.GetBootedImage()
	status.Os.ImageDigest = bootcInfo.GetBootedImageDigest()
	return nil
}

func (m *manager) BeforeUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	if desired.Os == nil {
		return nil
	}
	// The prefetch manager now handles scheduling
	m.log.Debugf("OS image %s will be scheduled for prefetching", desired.Os.Image)
	return nil
}

func (m *manager) CollectOCITargets(ctx context.Context, current, desired *v1beta1.DeviceSpec, _ ...dependency.OCICollectOpt) (*dependency.OCICollection, error) {
	if desired.Os == nil {
		m.log.Debug("No OS spec to collect OCI targets from")
		return &dependency.OCICollection{}, nil
	}

	osImage := desired.Os.Image

	// check if the image is already booted or exists in container storage
	status, err := m.client.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting OS status: %w", err)
	}
	isDesiredImageRunning, err := container.IsOsImageReconciled(&status.BootcHost, desired)
	if err != nil {
		return nil, fmt.Errorf("checking if OS image is reconciled: %w", err)
	}
	if isDesiredImageRunning {
		// desired OS image is already booted, no need to pull it
		m.log.Debugf("Desired OS image is currently booted: %s", osImage)
		return &dependency.OCICollection{}, nil
	}

	if m.podmanClient.ImageExists(ctx, osImage) {
		m.log.Debugf("OS image already exists in container storage: %s", osImage)
		return &dependency.OCICollection{}, nil
	}

	target := dependency.OCIPullTarget{
		Type:       dependency.OCITypePodmanImage,
		Reference:  osImage,
		PullPolicy: v1beta1.PullIfNotPresent,
		ClientOptsFn: m.pullConfigResolver.Options(dependency.PullConfigSpec{
			Paths:    []string{authPath},
			OptionFn: client.WithPullSecret,
		}),
	}

	m.log.Debugf("Collected 1 OCI target from OS spec: %s", osImage)
	return &dependency.OCICollection{
		Targets: dependency.OCIPullTargetsByUser{
			v1beta1.CurrentProcessUsername: []dependency.OCIPullTarget{target},
		},
	}, nil
}

func (m *manager) AfterUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	if desired.Os == nil {
		return nil
	}
	osImage := desired.Os.Image
	return m.client.Switch(ctx, osImage)
}

func (m *manager) Reboot(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	return m.client.Apply(ctx)
}

type Status struct {
	container.BootcHost
}
