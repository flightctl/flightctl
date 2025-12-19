package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/spec/audit"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
)

var _ Manager = (*manager)(nil)

// manager is responsible for managing the rendered device spec.
type manager struct {
	currentPath  string
	desiredPath  string
	rollbackPath string

	deviceName       string
	deviceReadWriter fileio.ReadWriter
	osClient         os.Client
	publisher        Publisher
	watcher          Watcher
	cache            *cache
	queue            PriorityQueue
	policyManager    policy.Manager
	auditLogger      audit.Logger

	lastConsumedDevice *v1beta1.Device
	log                *log.PrefixLogger
}

// NewManager creates a new device spec manager.
//
// Note: This manager is designed for sequential operations only and is not
// thread-safe.
func NewManager(
	deviceName string,
	dataDir string,
	policyManager policy.Manager,
	deviceReadWriter fileio.ReadWriter,
	osClient os.Client,
	pollConfig poll.Config,
	deviceNotFoundHandler func() error,
	auditLogger audit.Logger,
	log *log.PrefixLogger,
) *manager {
	cache := newCache(log)
	queue := newQueueManager(
		defaultSpecQueueMaxSize,
		defaultSpecRequeueMaxRetries,
		defaultSpecPollConfig,
		policyManager,
		cache,
		log,
	)

	m := &manager{
		currentPath:      filepath.Join(dataDir, string(Current)+".json"),
		desiredPath:      filepath.Join(dataDir, string(Desired)+".json"),
		rollbackPath:     filepath.Join(dataDir, string(Rollback)+".json"),
		deviceName:       deviceName,
		deviceReadWriter: deviceReadWriter,
		osClient:         osClient,
		cache:            cache,
		policyManager:    policyManager,
		auditLogger:      auditLogger,
		queue:            queue,
		log:              log,
	}

	lastKnownVersion := "0"
	if desired, err := m.Read(Desired); err == nil && desired != nil {
		lastKnownVersion = desired.Version()
	}

	pub := newPublisher(deviceName, pollConfig, lastKnownVersion, deviceNotFoundHandler, log)
	m.publisher = pub
	m.watcher = pub.Watch()

	return m
}

func (s *manager) Initialize(ctx context.Context) error {
	// Create all files during bootstrap - all use bootstrap reason
	if err := s.write(ctx, Current, newVersionedDevice("0"), audit.ReasonBootstrap); err != nil {
		return err
	}
	if err := s.write(ctx, Desired, newVersionedDevice("0"), audit.ReasonBootstrap); err != nil {
		return err
	}
	if err := s.write(ctx, Rollback, newVersionedDevice("0"), audit.ReasonBootstrap); err != nil {
		return err
	}
	// reconcile the initial spec even though its empty
	s.queue.Add(ctx, newVersionedDevice("0"))

	return nil
}

func (s *manager) Ensure() error {
	// Check and recreate missing spec files.
	// Distinguishes between first startup (bootstrap) and recovery from corruption.

	// Determine if this is first startup by checking if ALL files are missing
	allMissing := true
	for _, specType := range []Type{Current, Desired, Rollback} {
		exists, err := s.exists(specType)
		if err != nil {
			return err
		}
		if exists {
			allMissing = false
			break
		}
	}

	// Create missing files with appropriate reason
	for _, specType := range []Type{Current, Desired, Rollback} {
		exists, err := s.exists(specType)
		if err != nil {
			return err
		}

		if !exists {
			var reason audit.Reason
			if allMissing {
				// First startup - all files created during bootstrap
				reason = audit.ReasonBootstrap
				s.log.Infof("First startup: creating %s spec file", specType)
			} else {
				// File corruption recovery - use appropriate reason
				if specType == Rollback {
					reason = audit.ReasonInitialization
				} else {
					reason = audit.ReasonRecovery
				}
				s.log.Warnf("Spec file does not exist %s. Resetting state to empty...", specType)
			}

			if err := s.write(context.TODO(), specType, newVersionedDevice("0"), reason); err != nil {
				return err
			}
		}
	}

	current, err := s.Read(Current)
	if err != nil {
		return fmt.Errorf("ensuring cache: %w", err)
	}
	desired, err := s.Read(Desired)
	if err != nil {
		return fmt.Errorf("ensuring cache: %w", err)
	}
	rollback, err := s.Read(Rollback)
	if err != nil {
		return fmt.Errorf("ensuring cache: %w", err)
	}

	// update cached rendered versions
	s.cache.update(Current, current)
	s.cache.update(Desired, desired)
	s.cache.update(Rollback, rollback)

	// add the desired spec to the queue this ensures that the device will
	// reconcile the desired spec on startup.
	s.queue.Add(context.TODO(), desired)

	return nil
}

func (s *manager) IsRollingBack(ctx context.Context) (bool, error) {
	desired, err := s.Read(Desired)
	if err != nil {
		return false, err
	}

	if desired.Spec == nil {
		return false, fmt.Errorf("%w: desired spec is nil", errors.ErrInvalidSpec)
	}

	if desired.Spec.Os == nil || desired.Spec.Os.Image == "" {
		return false, nil
	}

	rollback, err := s.Read(Rollback)
	if err != nil {
		return false, err
	}

	if rollback.Spec.Os == nil || rollback.Spec.Os.Image == "" {
		return false, nil
	}

	osStatus, err := s.osClient.Status(ctx)
	if err != nil {
		return false, err
	}

	// The system is in a rollback state if:
	// 1. There is no staged OS image, indicating that no update is in progress.
	// 2. The currently booted OS image matches the rollback image.
	// 3. The booted image does not match the desired OS image.
	if osStatus.GetStagedImage() != "" {
		return false, nil
	}

	bootedOSImage := osStatus.GetBootedImage()
	return bootedOSImage == rollback.Spec.Os.Image && bootedOSImage != desired.Spec.Os.Image, nil
}

func (s *manager) Upgrade(ctx context.Context) error {
	// return early if the context is done
	if err := ctx.Err(); err != nil {
		return err
	}

	// only upgrade if the device is in the process of reconciling the desired spec
	if s.IsUpgrading() {
		desired, err := s.Read(Desired)
		if err != nil {
			return err
		}

		if err := s.write(ctx, Current, desired, audit.ReasonUpgrade); err != nil {
			return err
		}

		if err := s.ClearRollback(); err != nil {
			return err
		}

		s.log.Infof("Spec reconciliation complete: current version %s", desired.Version())
	}

	// remove reconciled desired spec from the queue
	version, err := stringToInt64(s.cache.getRenderedVersion(Desired))
	if err != nil {
		return err
	}
	s.queue.Remove(version)

	return nil
}

func (s *manager) SetUpgradeFailed(version string) error {
	versionInt, err := stringToInt64(version)
	if err != nil {
		return err
	}
	s.queue.SetFailed(versionInt)

	return nil
}

func (s *manager) IsUpgrading() bool {
	return s.cache.getRenderedVersion(Current) != s.cache.getRenderedVersion(Desired)
}

func (s *manager) CreateRollback(ctx context.Context) error {
	current, err := s.Read(Current)
	if err != nil {
		return err
	}

	// it is possible that the current rendered spec does not have an OS image.
	// In this case, we need to get the booted image from bootc.
	var currentOSImage string
	if current.Spec.Os == nil || current.Spec.Os.Image == "" {
		osStatus, err := s.osClient.Status(ctx)
		if err != nil {
			return err
		}
		currentOSImage = osStatus.GetBootedImage()
	} else {
		currentOSImage = current.Spec.Os.Image
	}

	// rollback is a basic copy of the current rendered spec
	// which contains the rendered version and the OS image.
	rollback := newVersionedDevice(current.Version())
	rollback.Spec = &v1beta1.DeviceSpec{
		Os: &v1beta1.DeviceOsSpec{Image: currentOSImage},
	}

	if err := s.write(ctx, Rollback, rollback, audit.ReasonInitialization); err != nil {
		return err
	}
	return nil
}

func (s *manager) ClearRollback() error {
	return s.write(context.TODO(), Rollback, newVersionedDevice(""), audit.ReasonInitialization)
}

func (s *manager) Rollback(ctx context.Context, opts ...RollbackOption) error {
	cfg := &rollbackConfig{}

	// apply opts
	for _, opt := range opts {
		opt(cfg)
	}

	failedDesiredVersion := s.cache.getRenderedVersion(Desired)

	if cfg.setFailed {
		version, err := stringToInt64(failedDesiredVersion)
		if err != nil {
			return err
		}
		// set the desired spec as failed
		s.queue.SetFailed(version)
	}

	// rollback on disk state current == desired
	if err := s.deviceReadWriter.CopyFile(s.currentPath, s.desiredPath); err != nil {
		return fmt.Errorf("%w: copy %q to %q: %w", errors.ErrCopySpec, s.currentPath, s.desiredPath, err)
	}

	current, err := s.Read(Current)
	if err != nil {
		return err
	}

	// add the current spec back to the priority queue to ensure future resync
	s.queue.Add(ctx, current)

	// rollback in-memory cache current == desired
	s.cache.update(Desired, current)

	s.logAudit(ctx, current, failedDesiredVersion, current.Version(), audit.ResultSuccess, audit.ReasonRollback, audit.TypeDesired)

	return nil
}

func (s *manager) Read(specType Type) (*v1beta1.Device, error) {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return nil, err
	}
	spec, err := readDeviceFromFile(s.deviceReadWriter, filePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", specType, err)
	}

	s.cache.update(specType, spec)

	return spec, nil
}

func (s *manager) RenderedVersion(specType Type) string {
	return s.cache.getRenderedVersion(specType)
}

func (s *manager) OSVersion(specType Type) string {
	return s.cache.getOSVersion(specType)
}

// consumeLatest consumes the latest device spec from the device publisher channel.
// it will consume all available messages until the channel is empty.
func (s *manager) consumeLatest(ctx context.Context) (bool, error) {
	var consumed bool

	// consume all available messages from the device publisher and add them to the local queue
	for {
		newDesired, popped, err := s.watcher.TryPop()
		if err != nil {
			return false, err
		}
		if !popped {
			break
		}
		s.log.Debugf("New template version received from management service: %s", newDesired.Version())
		// as the queue maintains some policy state we need to sync the policy state prior to adding versions to the queue
		// otherwise policy state is delayed by a version and could lead to instances in which updating is stuck for a long time.
		// we log an error if policy syncing fails, but we don't return an error as the mechanism for indicating to users
		// that a spec is poorly defined doesn't exist here.
		if err = s.policyManager.Sync(ctx, newDesired.Spec); err != nil {
			s.log.Errorf("Failed to sync new device version %s policy manager: %v", newDesired.Version(), err)
		}
		s.queue.Add(ctx, newDesired)
		s.lastConsumedDevice = newDesired
		consumed = true
	}
	if !consumed && s.lastConsumedDevice != nil {
		version, err := s.getRenderedVersion()
		if err != nil {
			return false, fmt.Errorf("getting rendered version: %w", err)
		}
		if s.lastConsumedDevice.Version() != version {
			// In case of rollback we would like to consume again the last device
			s.log.Debugf("Requeuing last consumed device. version: %s", s.lastConsumedDevice.Version())
			s.queue.Add(ctx, s.lastConsumedDevice)
			consumed = true
		}
	}
	return consumed, nil
}

func (s *manager) GetDesired(ctx context.Context) (*v1beta1.Device, bool, error) {
	consumed, err := s.consumeLatest(ctx)
	if err != nil {
		return nil, false, err
	}
	if !consumed {

		// no new spec available, read the current desired spec from disk and add it to the queue
		desired, readErr := s.Read(Desired)
		if readErr != nil {
			return nil, false, readErr
		}
		s.log.Tracef("Requeuing current desired spec from disk version: %s", desired.Version())
		s.queue.Add(ctx, desired)
	}

	return s.getDeviceFromQueue(ctx)
}

// getDeviceFromQueue retrieves the next desired device to reconcile.  Returns true
// to signal requeue if no spec is available. If the spec is newer than the
// current desired version it will be written to disk.
func (s *manager) getDeviceFromQueue(ctx context.Context) (*v1beta1.Device, bool,
	error) {
	desired, exists := s.queue.Next(ctx)
	if !exists {
		// no spec available, signal to requeue
		return nil, true, nil
	}

	// if this is a new version ensure we persist it to disk
	if desired.Version() != s.cache.getRenderedVersion(Desired) {
		// Guard check: ensure we don't allow older versions
		desiredVersion, err := stringToInt64(desired.Version())
		if err != nil {
			return nil, false, fmt.Errorf("invalid desired version: %w", err)
		}
		currentDesiredVersion, err := stringToInt64(s.cache.getRenderedVersion(Desired))
		if err != nil {
			return nil, false, fmt.Errorf("invalid current desired version: %w", err)
		}
		if desiredVersion < currentDesiredVersion {
			s.log.Errorf("Rejecting older version: %d < %d", desiredVersion, currentDesiredVersion)
			return nil, false, fmt.Errorf("version %d is older than current desired version %d", desiredVersion, currentDesiredVersion)
		}

		if s.isNewDesiredVersion(desired) {
			s.log.Infof("Writing new desired rendered spec to disk version: %s", desired.Version())
		}

		if err := s.write(ctx, Desired, desired, audit.ReasonSync); err != nil {
			return nil, false, err
		}
	}

	return desired, false, nil
}

// isNewDesiredVersion returns true if this is the first time observing this desired version.
func (s *manager) isNewDesiredVersion(desired *v1beta1.Device) bool {
	return s.lastConsumedDevice == nil || s.lastConsumedDevice.Version() != desired.Version()
}

func (s *manager) IsOSUpdate() bool {
	return s.cache.getOSVersion(Current) != s.cache.getOSVersion(Desired)
}

func (s *manager) CheckOsReconciliation(ctx context.Context) (string, bool, error) {
	osStatus, err := s.osClient.Status(ctx)
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", errors.ErrGettingBootcStatus, err)
	}
	bootedOSImage := osStatus.GetBootedImage()

	desired, err := s.Read(Desired)
	if err != nil {
		return "", false, err
	}

	if desired.Spec.Os == nil {
		return bootedOSImage, false, nil
	}

	return bootedOSImage, desired.Spec.Os.Image == osStatus.GetBootedImage(), nil
}

func (s *manager) Status(ctx context.Context, status *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	status.Config.RenderedVersion = s.cache.getRenderedVersion(Current)
	return nil
}

func (s *manager) CheckPolicy(ctx context.Context, policyType policy.Type, version string) error {
	return s.queue.CheckPolicy(ctx, policyType, version)
}

func (s *manager) SetClient(client client.Management) {
	s.publisher.SetClient(client)
}

// Watch returns a watcher to observe new device specs from the management API.
func (s *manager) Watch() Watcher {
	return s.publisher.Watch()
}

// Publisher returns the spec publisher that polls the management server
// for spec updates. This is exposed separately to make the spec fetching goroutine
// explicit and isolated from other spec manager operations.
func (s *manager) Publisher() Publisher {
	return s.publisher
}

func (s *manager) write(ctx context.Context, specType Type, device *v1beta1.Device, reason audit.Reason) error {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return err
	}

	oldVersion := s.cache.getRenderedVersion(specType)

	err = writeDeviceToFile(s.deviceReadWriter, device, filePath)
	if err != nil {
		return fmt.Errorf("writing %s: %w", specType, err)
	}

	s.cache.update(specType, device)

	// Validate reason is provided
	if reason == "" {
		return fmt.Errorf("reason is required for writing %s spec", specType)
	}

	// Audit all disk writes
	var auditType audit.Type
	switch specType {
	case Current:
		auditType = audit.TypeCurrent
	case Desired:
		auditType = audit.TypeDesired
	case Rollback:
		auditType = audit.TypeRollback
	}
	s.logAudit(ctx, device, oldVersion, device.Version(), audit.ResultSuccess, reason, auditType)

	return nil
}

func (s *manager) exists(specType Type) (bool, error) {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return false, err
	}
	exists, err := s.deviceReadWriter.PathExists(filePath)
	if err != nil {
		return false, fmt.Errorf("%w: %s: %w", errors.ErrCheckingFileExists, specType, err)
	}
	return exists, nil
}

// getRenderedVersion returns the next rendered version to reconcile. If the
// desired rendered version is marked as failed it will return the next version
// to make progress.
func (s *manager) getRenderedVersion() (string, error) {
	desiredRenderedVersion := s.cache.getRenderedVersion(Desired)
	version, err := stringToInt64(desiredRenderedVersion)
	if err != nil {
		return "", err
	}
	if s.queue.IsFailed(version) {
		return desiredRenderedVersion, nil
	}
	return s.cache.getRenderedVersion(Current), nil
}

func (s *manager) pathFromType(specType Type) (string, error) {
	var filePath string
	switch specType {
	case Current:
		filePath = s.currentPath
	case Desired:
		filePath = s.desiredPath
	case Rollback:
		filePath = s.rollbackPath
	default:
		return "", fmt.Errorf("%w: %s", errors.ErrInvalidSpecType, specType)
	}
	return filePath, nil
}

func readDeviceFromFile(
	reader fileio.Reader,
	filePath string,
) (*v1beta1.Device, error) {
	var current v1beta1.Device
	renderedBytes, err := reader.ReadFile(filePath)
	if err != nil {
		if fileio.IsNotExist(err) {
			// if the file does not exist, this means it has been removed/corrupted
			return nil, fmt.Errorf("%w: reading %q: %w", errors.ErrMissingRenderedSpec, filePath, err)
		}
		return nil, fmt.Errorf("%w: reading %q: %w", errors.ErrReadingRenderedSpec, filePath, err)
	}

	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &current); err != nil {
		return nil, fmt.Errorf("%w: %w", errors.ErrUnmarshalSpec, err)
	}

	return &current, nil
}

func writeDeviceToFile(writer fileio.Writer, device *v1beta1.Device, filePath string) error {
	deviceBytes, err := json.Marshal(device)
	if err != nil {
		return err
	}
	if err := writer.WriteFile(filePath, deviceBytes, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("%w: writing to %q: %w", errors.ErrWritingRenderedSpec, filePath, err)
	}
	return nil
}

func IsUpgrading(current *v1beta1.Device, desired *v1beta1.Device) bool {
	return current.Version() != desired.Version()
}

// IsRollback returns true if the version of the current spec is greater than the desired.
func IsRollback(current *v1beta1.Device, desired *v1beta1.Device) bool {
	currentVersion, err := stringToInt64(current.Version())
	if err != nil {
		return false
	}
	desiredVersion, err := stringToInt64(desired.Version())
	if err != nil {
		return false
	}
	return currentVersion > desiredVersion
}

// logAudit logs an audit event for spec transitions. It extracts the fleet template
// version from device annotations and handles nil device for bootstrap events.
func (s *manager) logAudit(ctx context.Context, device *v1beta1.Device, oldVersion, newVersion string, result audit.Result, reason audit.Reason, auditType audit.Type) {
	if s.auditLogger == nil {
		return
	}

	// Extract fleet template version from device annotations
	fleetTemplateVersion := ""
	if device != nil && device.Metadata.Annotations != nil {
		if version, exists := (*device.Metadata.Annotations)[v1beta1.DeviceAnnotationRenderedTemplateVersion]; exists {
			fleetTemplateVersion = version
		}
	}

	auditInfo := &audit.EventInfo{
		Device:               s.deviceName,
		OldVersion:           oldVersion,
		NewVersion:           newVersion,
		Result:               result,
		Reason:               reason,
		Type:                 auditType,
		FleetTemplateVersion: fleetTemplateVersion,
		StartTime:            time.Now(),
	}

	if err := s.auditLogger.LogEvent(ctx, auditInfo); err != nil {
		s.log.Warnf("Failed to write %s audit log (%s->%s, reason=%s): %v", auditType, oldVersion, newVersion, reason, err)
	}
}

type rollbackConfig struct {
	setFailed bool
}

type RollbackOption func(*rollbackConfig)

// WithSetFailed enables setting the desired spec as failed.
func WithSetFailed() RollbackOption {
	return func(cfg *rollbackConfig) {
		cfg.setFailed = true
	}
}
