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
	observedResults      map[string]v1beta1.DeviceDeltaApplyStatus
}

func (m *manager) Status(ctx context.Context, status *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	bootcInfo, err := m.client.Status(ctx)
	if err != nil {
		return err
	}

	status.Os.Image = bootcInfo.GetBootedImage()
	status.Os.ImageDigest = bootcInfo.GetBootedImageDigest()
	status.Os.LastDelta = nil
	if m.deltaStatusStore != nil {
		status.Os.LastDelta = m.deltaStatusStore.ObservedOSResult(status.Os.Image, status.Os.ImageDigest)
	} else if status.Os.Image != "" && status.Os.ImageDigest != "" {
		m.mu.Lock()
		if result, exists := m.observedResults[deltastatus.Fingerprint(status.Os.Image, status.Os.ImageDigest)]; exists {
			copied := result
			if result.FallbackReason != nil {
				reason := *result.FallbackReason
				copied.FallbackReason = &reason
			}
			status.Os.LastDelta = &copied
		}
		m.mu.Unlock()
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
	targetKey := m.activeDeltaTargetKey
	deltaStaged := m.stagedDeltaImage == osImage
	m.mu.Unlock()
	if deltaStaged {
		m.recordStagedOSResult(ctx, targetKey, osImage)
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
		// A reference-only attempt cannot prove how the booted content was
		// prepared. Recording NotRequired retains any exact verified history.
		m.recordDeltaResult(targetKey, v1beta1.DeviceDeltaApplyOutcomeNotRequired, "", false)
		m.recordVerifiedOSResult(targetKey, bootcStatus.GetBootedImage(), bootcStatus.GetBootedImageDigest())
		return &dependency.OCICollection{}, nil
	}
	if m.podmanClient.ImageExists(ctx, osImage) {
		m.log.Debugf("OS image already exists in container storage: %s", osImage)
		m.recordDeltaResult(targetKey, v1beta1.DeviceDeltaApplyOutcomeNotRequired, "", true)
		return &dependency.OCICollection{}, nil
	}

	optsFn := m.osPullOptsFn()

	if !m.canApplyOSDelta() {
		if desired.Os.DeltaImage != nil && *desired.Os.DeltaImage != "" {
			m.setFallbackReason(targetKey, "OS delta application is unavailable")
		} else {
			m.setDeltaNotUsedIfUnset(targetKey)
		}
		return m.fullImageCollection(osImage, optsFn), nil
	}

	candidate := m.discoverOSDelta(ctx, targetKey, desired, bootcStatus, optsFn)
	if candidate.Reference == "" {
		m.setDeltaNotUsedIfUnset(targetKey)
		return m.fullImageCollection(osImage, optsFn), nil
	}

	if err := m.pullAndApplyOSDelta(ctx, targetKey, candidate.Reference, osImage, optsFn); err != nil {
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
		// The attempt follows the OS image reference; refreshing the
		// control-plane hint alone does not change that image target.
		targetKey = deltastatus.Fingerprint(osSpec.Image)
	}

	if m.deltaStatusStore != nil {
		if err := m.deltaStatusStore.SetOSTarget(targetKey); err != nil {
			m.log.Warnf("Failed to reconcile persisted OS delta target: %v", err)
		}
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
	m.mu.Unlock()
}

func (m *manager) pullAndApplyOSDelta(ctx context.Context, targetKey, candidate, osImage string, optsFn dependency.ClientOptsFn) error {
	tmpDir, err := m.readWriter.MkdirTemp(osDeltaTempPrefix)
	if err != nil {
		m.setFallbackReason(targetKey, fallbackReasonApply)
		return fmt.Errorf("create delta temporary directory: %w", err)
	}
	defer func() { _ = m.readWriter.RemoveAll(tmpDir) }()

	deltaFile := filepath.Join(tmpDir, "delta.oci")
	opts := append([]client.ClientOption{client.Timeout(m.pullTimeout)}, optsFn()...)
	if err := m.skopeo.Copy(ctx, "docker://"+candidate, "oci-archive:"+deltaFile, opts...); err != nil {
		m.setFallbackReason(targetKey, fallbackReasonPull)
		return err
	}

	layoutDir := filepath.Join(tmpDir, osDeltaLayoutName)
	if err := m.ociDelta.Apply(ctx, deltaFile, "oci:"+layoutDir); err != nil {
		return m.failApply(targetKey, err)
	}

	if err := m.client.SwitchOCI(ctx, layoutDir); err != nil {
		return m.failApply(targetKey, err)
	}
	if err := m.client.SwitchRegistry(ctx, osImage); err != nil {
		return m.failApply(targetKey, err)
	}
	m.mu.Lock()
	if m.activeDeltaTargetKey == targetKey {
		m.stagedDeltaImage = osImage
	}
	m.mu.Unlock()
	m.recordDeltaResult(targetKey, v1beta1.DeviceDeltaApplyOutcomeApplied, "", false)
	m.recordStagedOSResult(ctx, targetKey, osImage)
	return nil
}

func (m *manager) failApply(targetKey string, err error) error {
	m.setFallbackReason(targetKey, fallbackReasonApply)
	return err
}

func (m *manager) setFallbackReason(targetKey, reason string) {
	m.recordDeltaResult(targetKey, v1beta1.DeviceDeltaApplyOutcomeFallback, reason, false)
}

func (m *manager) setDeltaNotUsedIfUnset(targetKey string) {
	m.recordDeltaResult(targetKey, v1beta1.DeviceDeltaApplyOutcomeNotUsed, "", true)
}

func (m *manager) recordDeltaResult(targetKey string, outcome v1beta1.DeviceDeltaApplyOutcomeType, fallbackReason string, onlyIfUnset bool) {
	m.mu.Lock()
	if targetKey != m.activeDeltaTargetKey || (onlyIfUnset && m.deltaOutcome != nil) {
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

// recordStagedOSResult uses bootc's completed staging observation, never a
// desired reference or registry lookup, to associate the completed outcome.
func (m *manager) recordStagedOSResult(ctx context.Context, targetKey, image string) {
	m.mu.Lock()
	hasResult := targetKey != "" && m.activeDeltaTargetKey == targetKey && m.deltaOutcome != nil
	m.mu.Unlock()
	if !hasResult {
		return
	}
	observed, err := m.client.Status(ctx)
	if err != nil {
		m.log.Warnf("Cannot verify staged OS delta result: %v", err)
		return
	}
	expected, err := container.ImageToBootcTarget(image)
	if err != nil {
		return
	}
	if observed.GetStagedImage() == expected {
		m.recordVerifiedOSResult(targetKey, expected, observed.Status.Staged.Image.ImageDigest)
	}
}

func (m *manager) recordVerifiedOSResult(targetKey, image, digest string) {
	if image == "" || digest == "" {
		return
	}
	m.mu.Lock()
	if targetKey == "" || m.activeDeltaTargetKey != targetKey || m.deltaOutcome == nil {
		m.mu.Unlock()
		return
	}
	result := v1beta1.DeviceDeltaApplyStatus{Outcome: *m.deltaOutcome}
	if m.fallbackReason != nil {
		reason := *m.fallbackReason
		result.FallbackReason = &reason
	}
	if m.deltaStatusStore == nil {
		if m.observedResults == nil {
			m.observedResults = make(map[string]v1beta1.DeviceDeltaApplyStatus)
		}
		key := deltastatus.Fingerprint(image, digest)
		if _, exists := m.observedResults[key]; !exists || result.Outcome != v1beta1.DeviceDeltaApplyOutcomeNotRequired {
			m.observedResults[key] = result
		}
	}
	m.mu.Unlock()
	if m.deltaStatusStore != nil {
		if err := m.deltaStatusStore.RecordOSImageResult(targetKey, image, digest, result); err != nil {
			m.log.Warnf("Failed to persist OS image delta association: %v", err)
		}
	}
}

func (m *manager) AfterUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	if desired.Os == nil {
		return nil
	}
	m.mu.Lock()
	deltaStaged := m.stagedDeltaImage == desired.Os.Image
	targetKey := m.activeDeltaTargetKey
	m.mu.Unlock()
	if deltaStaged {
		m.recordStagedOSResult(ctx, targetKey, desired.Os.Image)
		return nil
	}
	if err := m.client.Switch(ctx, desired.Os.Image); err != nil {
		return err
	}
	m.recordStagedOSResult(ctx, targetKey, desired.Os.Image)
	return nil
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

func (m *manager) osDeltaSourceAvailable(ctx context.Context, bootcStatus *Status, source string) (bool, error) {
	for _, image := range bootcImageStatuses(bootcStatus) {
		if image.Image.ImageDigest == source {
			return true, nil
		}
	}
	return m.ociDelta.SourceImageExists(ctx, source)
}

func (m *manager) osDeltaSourceDigests(ctx context.Context, bootcStatus *Status, wanted map[string]struct{}) (map[string]struct{}, error) {
	sources := make(map[string]struct{})
	for _, image := range bootcImageStatuses(bootcStatus) {
		if image.Image.ImageDigest != "" {
			sources[image.Image.ImageDigest] = struct{}{}
		}
	}
	needsCachedSources := false
	for source := range wanted {
		if _, found := sources[source]; !found {
			needsCachedSources = true
			break
		}
	}
	if !needsCachedSources {
		return sources, nil
	}
	cached, err := m.ociDelta.SourceImageDigests(ctx)
	if err != nil {
		return sources, err
	}
	for source := range cached {
		sources[source] = struct{}{}
	}
	return sources, nil
}

func bootcImageStatuses(status *Status) []container.ImageStatus {
	return []container.ImageStatus{status.BootcHost.Status.Booted, status.BootcHost.Status.Staged, status.BootcHost.Status.Rollback}
}

func (m *manager) osTargetPlatformDigestMatches(ctx context.Context, image, targetDigest string, optsFn dependency.ClientOptsFn) (bool, error) {
	platformDigest, err := m.skopeo.InspectPlatformDigest(ctx, image, optsFn()...)
	if err != nil {
		return false, fmt.Errorf("inspect requested OS platform digest: %w", err)
	}
	return platformDigest == targetDigest, nil
}
