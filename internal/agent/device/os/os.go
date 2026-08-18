package os

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	authPath            = "/etc/ostree/auth.json"
	fallbackReasonPull  = "delta pull failed"
	fallbackReasonApply = "delta apply failed"
	osDeltaTempPrefix   = "os-delta"
	osDeltaArchiveName  = "image.tar"
)

type Capabilities struct {
	OsMode        v1beta1.OsModeType
	DeltaEligible bool
}

type Client interface {
	// Status retrieves the current OS status
	Status(ctx context.Context) (*Status, error)
	// Switch prepares the system to switch to the specified OS image
	Switch(ctx context.Context, image string) error
	// Rollback stages the previous deployment and reboots into it
	Rollback(ctx context.Context) error
	// Apply applies the OS changes, potentially triggering a reboot
	Apply(ctx context.Context) error
}

type Manager interface {
	BeforeUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error
	AfterUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error
	// Rollback validates that the rollback deployment matches the expected image
	// from the spec, then stages the previous deployment and reboots into it.
	Rollback(ctx context.Context, desired *v1beta1.DeviceSpec) error
	Reboot(ctx context.Context, desired *v1beta1.DeviceSpec) error

	dependency.OCICollector
	status.Exporter
}

// NewManager creates a new OS manager.
func NewManager(
	log *log.PrefixLogger,
	client Client,
	caps Capabilities,
	readWriter fileio.ReadWriter,
	podmanClient *client.Podman,
	pullConfigResolver dependency.PullConfigResolver,
	ociDelta *client.OCIDelta,
	skopeo *client.Skopeo,
) Manager {
	return &manager{
		client:             client,
		caps:               caps,
		podmanClient:       podmanClient,
		readWriter:         readWriter,
		pullConfigResolver: pullConfigResolver,
		ociDelta:           ociDelta,
		skopeo:             skopeo,
		log:                log,
	}
}

type manager struct {
	client             Client
	caps               Capabilities
	podmanClient       *client.Podman
	readWriter         fileio.ReadWriter
	pullConfigResolver dependency.PullConfigResolver
	ociDelta           *client.OCIDelta
	skopeo             *client.Skopeo
	log                *log.PrefixLogger

	fallbackReason     *string
	lastAttemptedImage string
}

func (m *manager) Status(ctx context.Context, status *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	bootcInfo, err := m.client.Status(ctx)
	if err != nil {
		return err
	}

	status.Os.Image = bootcInfo.GetBootedImage()
	status.Os.ImageDigest = bootcInfo.GetBootedImageDigest()
	status.Os.LastUpdateFallbackReason = m.fallbackReason
	osMode := m.caps.OsMode
	deltaEligible := m.caps.DeltaEligible
	status.Capabilities = &v1beta1.DeviceCapabilities{
		OsMode:        &osMode,
		DeltaEligible: &deltaEligible,
	}
	return nil
}

func (m *manager) BeforeUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	if desired.Os == nil {
		return nil
	}
	m.log.Debugf("OS image %s will be scheduled for prefetching", desired.Os.Image)
	return nil
}

func (m *manager) CollectOCITargets(ctx context.Context, current, desired *v1beta1.DeviceSpec, _ ...dependency.OCICollectOpt) (*dependency.OCICollection, error) {
	if desired.Os == nil {
		m.log.Debug("No OS spec to collect OCI targets from")
		return &dependency.OCICollection{}, nil
	}

	osImage := desired.Os.Image

	bootcStatus, err := m.client.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting OS status: %w", err)
	}
	isDesiredImageRunning, err := container.IsOsImageReconciled(&bootcStatus.BootcHost, desired)
	if err != nil {
		return nil, fmt.Errorf("checking if OS image is reconciled: %w", err)
	}
	if isDesiredImageRunning {
		m.log.Debugf("Desired OS image is currently booted: %s", osImage)
		return &dependency.OCICollection{}, nil
	}

	if m.podmanClient.ImageExists(ctx, osImage) {
		m.log.Debugf("OS image already exists in container storage: %s", osImage)
		return &dependency.OCICollection{}, nil
	}

	m.startImageAttempt(osImage)
	optsFn := m.osPullOptsFn()
	if !m.caps.DeltaEligible {
		return m.fullImageCollection(osImage, optsFn), nil
	}

	candidate := m.discoverOSDelta(ctx, desired, bootcStatus.GetBootedImageDigest(), optsFn)
	if candidate == "" {
		return m.fullImageCollection(osImage, optsFn), nil
	}

	if err := m.pullAndApplyOSDelta(ctx, candidate, osImage, optsFn); err != nil {
		m.log.Errorf("OS delta failed, falling back to full pull: %v", err)
		return m.fullImageCollection(osImage, optsFn), nil
	}

	return &dependency.OCICollection{}, nil
}

func (m *manager) osPullOptsFn() dependency.ClientOptsFn {
	return m.pullConfigResolver.Options(dependency.PullConfigSpec{
		Paths:    []string{authPath},
		OptionFn: client.WithPullSecret,
	})
}

func (m *manager) fullImageCollection(osImage string, optsFn dependency.ClientOptsFn) *dependency.OCICollection {
	m.log.Debugf("Collected 1 OCI target from OS spec: %s", osImage)
	return &dependency.OCICollection{
		Targets: dependency.OCIPullTargetsByUser{
			v1beta1.CurrentProcessUsername: []dependency.OCIPullTarget{
				{
					Type:         dependency.OCITypePodmanImage,
					Reference:    osImage,
					PullPolicy:   v1beta1.PullIfNotPresent,
					ClientOptsFn: optsFn,
				},
			},
		},
	}
}

func (m *manager) startImageAttempt(osImage string) {
	if m.lastAttemptedImage == osImage {
		return
	}
	m.lastAttemptedImage = osImage
	m.fallbackReason = nil
}

func (m *manager) discoverOSDelta(ctx context.Context, desired *v1beta1.DeviceSpec, sourceDigest string, optsFn dependency.ClientOptsFn) string {
	if desired.Os.DeltaImage != nil && *desired.Os.DeltaImage != "" {
		return *desired.Os.DeltaImage
	}

	index, err := m.skopeo.ListReferrers(ctx, desired.Os.Image, optsFn()...)
	if err != nil {
		m.log.Debugf("OS delta referrers unavailable: %v", err)
		return ""
	}
	return selectOSDeltaCandidate(nil, desired.Os.Image, sourceDigest, index)
}

func (m *manager) pullAndApplyOSDelta(ctx context.Context, candidate, osImage string, optsFn dependency.ClientOptsFn) error {
	if _, err := m.podmanClient.PullArtifact(ctx, candidate, optsFn()...); err != nil {
		m.setFallbackReason(fallbackReasonPull)
		return err
	}

	tmpDir, err := m.readWriter.MkdirTemp(osDeltaTempPrefix)
	if err != nil {
		return m.failApply(ctx, candidate, err)
	}
	defer func() { _ = m.readWriter.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, osDeltaArchiveName)
	if err := m.ociDelta.Apply(ctx, candidate, archivePath); err != nil {
		return m.failApply(ctx, candidate, err)
	}

	loaded, err := m.podmanClient.LoadArchive(ctx, archivePath)
	if err != nil {
		return m.failApply(ctx, candidate, err)
	}
	if err := m.podmanClient.Tag(ctx, loaded, osImage); err != nil {
		return m.failApply(ctx, candidate, err)
	}

	m.removeArtifactBestEffort(ctx, candidate)
	return nil
}

func (m *manager) failApply(ctx context.Context, candidate string, err error) error {
	m.setFallbackReason(fallbackReasonApply)
	m.removeArtifactBestEffort(ctx, candidate)
	return err
}

func (m *manager) setFallbackReason(reason string) {
	r := reason
	m.fallbackReason = &r
}

func (m *manager) removeArtifactBestEffort(ctx context.Context, candidate string) {
	if err := m.podmanClient.RemoveArtifact(ctx, candidate); err != nil {
		m.log.Warnf("Failed to remove OS delta artifact %s: %v", candidate, err)
	}
}

func (m *manager) AfterUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	if desired.Os == nil {
		return nil
	}
	osImage := desired.Os.Image
	return m.client.Switch(ctx, osImage)
}

func (m *manager) Rollback(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	if desired == nil || desired.Os == nil || desired.Os.Image == "" {
		return fmt.Errorf("rollback spec has no OS image")
	}

	expectedImage := desired.Os.Image
	status, err := m.client.Status(ctx)
	if err != nil {
		return fmt.Errorf("getting OS status: %w", err)
	}

	rollbackImage := status.GetRollbackImage()
	if rollbackImage == "" {
		return fmt.Errorf("no rollback deployment available")
	}

	expectedTarget, err := container.ImageToBootcTarget(expectedImage)
	if err != nil {
		return fmt.Errorf("parsing expected image: %w", err)
	}

	if rollbackImage != expectedTarget {
		return fmt.Errorf("rollback deployment mismatch: bootc has %q, expected %q", rollbackImage, expectedTarget)
	}

	m.log.Infof("Validated rollback deployment matches expected image: %s", rollbackImage)
	return m.client.Rollback(ctx)
}

func (m *manager) Reboot(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	return m.client.Apply(ctx)
}

type Status struct {
	container.BootcHost
}
