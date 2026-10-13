package os

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
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
	osDeltaLayoutName   = "image"
)

type Capabilities struct {
	OsMode          v1beta1.OsModeType
	DeltaEligible   bool
	BootcVersion    string
	OCIDeltaVersion string
}

type Client interface {
	// Status retrieves the current OS status
	Status(ctx context.Context) (*Status, error)
	// Switch prepares the system to switch to the specified OS image
	Switch(ctx context.Context, image string) error
	// SwitchOCI stages a reconstructed OCI layout directory for the next boot.
	SwitchOCI(ctx context.Context, layoutDir string) error
	// SwitchRegistry stages the registry image so bootc records the spec name.
	SwitchRegistry(ctx context.Context, image string) error
	// Rollback stages the previous deployment and reboots into it
	Rollback(ctx context.Context) error
	// Apply applies the OS changes, potentially triggering a reboot
	Apply(ctx context.Context) error
	// Capabilities reports OS mode, tool versions, and whether this device can consume OCI deltas.
	Capabilities(ctx context.Context) Capabilities
	// RebootStaged reboots into an already staged deployment without fetching.
	RebootStaged(ctx context.Context) error
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
	pullTimeout time.Duration,
	opts ...Option,
) Manager {
	m := &manager{
		client:             client,
		caps:               caps,
		podmanClient:       podmanClient,
		readWriter:         readWriter,
		pullConfigResolver: pullConfigResolver,
		ociDelta:           ociDelta,
		skopeo:             skopeo,
		pullTimeout:        pullTimeout,
		log:                log,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Option configures optional OS manager integrations.
type Option func(*manager)

// WithDeltaStatusStore persists OS delta outcomes across agent restarts.
func WithDeltaStatusStore(store *deltastatus.Store) Option {
	return func(m *manager) { m.deltaStatusStore = store }
}

type manager struct {
	client             Client
	caps               Capabilities
	podmanClient       *client.Podman
	readWriter         fileio.ReadWriter
	pullConfigResolver dependency.PullConfigResolver
	ociDelta           *client.OCIDelta
	skopeo             *client.Skopeo
	pullTimeout        time.Duration
	log                *log.PrefixLogger
	deltaStatusStore   *deltastatus.Store

	mu                   sync.Mutex
	fallbackReason       *string
	deltaOutcome         *v1beta1.DeviceDeltaApplyOutcomeType
	activeDeltaTargetKey string
	lastAttemptedImage   string
	stagedDeltaImage     string
}

func (m *manager) Status(ctx context.Context, status *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	bootcInfo, err := m.client.Status(ctx)
	if err != nil {
		return err
	}

	status.Os.Image = bootcInfo.GetBootedImage()
	status.Os.ImageDigest = bootcInfo.GetBootedImageDigest()
	status.Os.LastDelta = nil
	m.mu.Lock()
	var reason *string
	if m.fallbackReason != nil {
		copiedReason := *m.fallbackReason
		reason = &copiedReason
	}
	var outcome *v1beta1.DeviceDeltaApplyOutcomeType
	if m.deltaOutcome != nil {
		copiedOutcome := *m.deltaOutcome
		outcome = &copiedOutcome
	}
	m.mu.Unlock()
	if outcome != nil {
		status.Os.LastDelta = &v1beta1.DeviceDeltaApplyStatus{FallbackReason: reason, Outcome: *outcome}
	}
	osMode := m.caps.OsMode
	status.Capabilities = &v1beta1.DeviceCapabilities{OsMode: &osMode}
	ApplyDeltaSystemInfo(&status.SystemInfo, m.caps)
	return nil
}

// ApplyDeltaSystemInfo populates the capability-derived fields of the device
// system info: OCI delta eligibility, the bootc and oci-delta tool versions,
// and the OS management mode. OsMode is only reported when it is a recognized
// mode; when the mode is unknown the field is cleared so any stale value is
// omitted from status.
func ApplyDeltaSystemInfo(info *v1beta1.DeviceSystemInfo, caps Capabilities) {
	eligible := caps.DeltaEligible
	info.DeltaEligible = &eligible
	if caps.BootcVersion != "" {
		v := caps.BootcVersion
		info.BootcVersion = &v
	}
	if caps.OCIDeltaVersion != "" {
		v := caps.OCIDeltaVersion
		info.OciDeltaVersion = &v
	}
	if mode, ok := systemInfoOsMode(caps.OsMode); ok {
		info.OsMode = &mode
	} else {
		// Clear any stale value so an unrecognized mode is omitted from status
		// rather than leaving a previously reported mode in place.
		info.OsMode = nil
	}
}

// systemInfoOsMode returns the OS management mode to report via systemInfo and
// whether it is a recognized mode. Unrecognized (including empty) modes are not
// reported so the field is omitted from status.
func systemInfoOsMode(mode v1beta1.OsModeType) (v1beta1.OsModeType, bool) {
	switch mode {
	case v1beta1.OsModeImage, v1beta1.OsModePackage:
		return mode, true
	default:
		return "", false
	}
}

func (m *manager) canApplyOSDelta() bool {
	if !m.caps.DeltaEligible {
		return false
	}
	return m.caps.BootcVersion != ""
}

func (m *manager) BeforeUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	if desired == nil || desired.Os == nil {
		m.startImageAttempt(nil)
		return nil
	}
	m.startImageAttempt(desired.Os)
	m.log.Debugf("OS image %s will be scheduled for prefetching", desired.Os.Image)
	return nil
}

func (m *manager) CollectOCITargets(ctx context.Context, current, desired *v1beta1.DeviceSpec, _ ...dependency.OCICollectOpt) (*dependency.OCICollection, error) {
	if desired == nil || desired.Os == nil {
		m.startImageAttempt(nil)
		m.log.Debug("No OS spec to collect OCI targets from")
		return &dependency.OCICollection{}, nil
	}

	osImage := desired.Os.Image
	m.startImageAttempt(desired.Os)

	m.mu.Lock()
	deltaStaged := m.stagedDeltaImage == osImage
	m.mu.Unlock()
	if deltaStaged {
		m.log.Debugf("OS image already staged from delta: %s", osImage)
		return &dependency.OCICollection{}, nil
	}

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
		m.setDeltaNotUsedIfUnset()
		return &dependency.OCICollection{}, nil
	}

	if m.podmanClient.ImageExists(ctx, osImage) {
		m.log.Debugf("OS image already exists in container storage: %s", osImage)
		m.setDeltaNotUsedIfUnset()
		return &dependency.OCICollection{}, nil
	}

	optsFn := m.osPullOptsFn()
	if !m.canApplyOSDelta() {
		m.setDeltaNotUsedIfUnset()
		return m.fullImageCollection(osImage, optsFn), nil
	}

	candidate := m.discoverOSDelta(ctx, desired, bootcStatus.GetBootedImageDigest(), optsFn)
	if candidate == "" {
		m.setDeltaNotUsedIfUnset()
		return m.fullImageCollection(osImage, optsFn), nil
	}

	if err := m.pullAndApplyOSDelta(ctx, candidate, osImage, optsFn); err != nil {
		m.log.Warnf("OS delta failed, falling back to full pull: %v", err)
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

func (m *manager) startImageAttempt(osSpec *v1beta1.DeviceOsSpec) {
	var osImage string
	var targetKey string
	if osSpec != nil {
		osImage = osSpec.Image
		// LastDelta describes the desired OS image result; refreshing the
		// control-plane hint alone does not change that image target.
		targetKey = deltastatus.Fingerprint(osSpec.Image)
	}

	var persisted *v1beta1.DeviceDeltaApplyStatus
	if m.deltaStatusStore != nil {
		if err := m.deltaStatusStore.SetOSTarget(targetKey); err != nil {
			m.log.Warnf("Failed to reconcile persisted OS delta target: %v", err)
		}
		persisted = m.deltaStatusStore.OSResult(targetKey)
	}

	m.mu.Lock()
	targetChanged := m.activeDeltaTargetKey != targetKey || m.lastAttemptedImage != osImage
	if targetChanged {
		m.fallbackReason = nil
		m.deltaOutcome = nil
	}
	m.activeDeltaTargetKey = targetKey
	m.lastAttemptedImage = osImage
	if m.stagedDeltaImage != "" && m.stagedDeltaImage != osImage {
		m.stagedDeltaImage = ""
	}
	if persisted != nil {
		m.deltaOutcome = &persisted.Outcome
		if persisted.FallbackReason != nil {
			reason := *persisted.FallbackReason
			m.fallbackReason = &reason
		}
	}
	m.mu.Unlock()
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
	tmpDir, err := m.readWriter.MkdirTemp(osDeltaTempPrefix)
	if err != nil {
		m.setFallbackReason(fallbackReasonApply)
		return fmt.Errorf("create delta temporary directory: %w", err)
	}
	defer func() { _ = m.readWriter.RemoveAll(tmpDir) }()

	deltaFile := filepath.Join(tmpDir, "delta.oci")
	opts := append([]client.ClientOption{client.Timeout(m.pullTimeout)}, optsFn()...)
	if err := m.skopeo.Copy(ctx, "docker://"+candidate, "oci-archive:"+deltaFile, opts...); err != nil {
		m.setFallbackReason(fallbackReasonPull)
		return err
	}

	layoutDir := filepath.Join(tmpDir, osDeltaLayoutName)
	if err := m.ociDelta.Apply(ctx, deltaFile, "oci:"+layoutDir); err != nil {
		return m.failApply(err)
	}

	if err := m.client.SwitchOCI(ctx, layoutDir); err != nil {
		return m.failApply(err)
	}
	if err := m.client.SwitchRegistry(ctx, osImage); err != nil {
		return m.failApply(err)
	}
	m.mu.Lock()
	m.stagedDeltaImage = osImage
	m.mu.Unlock()
	m.recordDeltaResult(v1beta1.DeviceDeltaApplyOutcomeApplied, "", false)
	return nil
}

func (m *manager) failApply(err error) error {
	m.setFallbackReason(fallbackReasonApply)
	return err
}

func (m *manager) setFallbackReason(reason string) {
	m.recordDeltaResult(v1beta1.DeviceDeltaApplyOutcomeFallback, reason, false)
}

func (m *manager) setDeltaNotUsedIfUnset() {
	m.recordDeltaResult(v1beta1.DeviceDeltaApplyOutcomeNotUsed, "", true)
}

func (m *manager) recordDeltaResult(outcome v1beta1.DeviceDeltaApplyOutcomeType, fallbackReason string, onlyIfUnset bool) {
	m.mu.Lock()
	if onlyIfUnset && m.deltaOutcome != nil {
		m.mu.Unlock()
		return
	}
	var reason *string
	if fallbackReason != "" {
		copied := fallbackReason
		reason = &copied
	}
	m.fallbackReason = reason
	m.deltaOutcome = &outcome
	targetKey := m.activeDeltaTargetKey
	m.mu.Unlock()

	if m.deltaStatusStore == nil || targetKey == "" {
		return
	}
	if err := m.deltaStatusStore.RecordOSResult(targetKey, v1beta1.DeviceDeltaApplyStatus{
		Outcome:        outcome,
		FallbackReason: reason,
	}); err != nil {
		m.log.Warnf("Failed to persist OS delta result: %v", err)
	}
}

func (m *manager) AfterUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	if desired.Os == nil {
		return nil
	}
	m.mu.Lock()
	deltaStaged := m.stagedDeltaImage == desired.Os.Image
	m.mu.Unlock()
	if deltaStaged {
		return nil
	}
	return m.client.Switch(ctx, desired.Os.Image)
}

func (m *manager) Rollback(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	if desired == nil || desired.Os == nil || desired.Os.Image == "" {
		return fmt.Errorf("rollback spec has no OS image")
	}
	m.startImageAttempt(desired.Os)

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
	if desired == nil || desired.Os == nil {
		return m.client.Apply(ctx)
	}
	m.mu.Lock()
	deltaStaged := m.stagedDeltaImage == desired.Os.Image
	m.mu.Unlock()
	if deltaStaged {
		return m.client.RebootStaged(ctx)
	}
	return m.client.Apply(ctx)
}

type Status struct {
	container.BootcHost
}
