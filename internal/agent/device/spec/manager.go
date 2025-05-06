package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
	"k8s.io/apimachinery/pkg/util/wait"
)

var _ Manager = (*manager)(nil)

// Manager is responsible for managing the rendered device spec.
type manager struct {
	deviceName   string
	currentPath  string
	desiredPath  string
	rollbackPath string

	deviceReadWriter fileio.ReadWriter
	managementClient client.Management
	osClient         os.Client

	cache *cache
	queue PriorityQueue

	log     *log.PrefixLogger
	backoff wait.Backoff
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
	backoff wait.Backoff,
	log *log.PrefixLogger,
) Manager {
	queue := newPriorityQueue(
		defaultSpecQueueMaxSize,
		defaultSpecRequeueMaxRetries,
		defaultSpecRequeueThreshold,
		defaultSpecRequeueDelay,
		policyManager,
		log,
	)
	return &manager{
		deviceName:       deviceName,
		currentPath:      filepath.Join(dataDir, string(Current)+".json"),
		desiredPath:      filepath.Join(dataDir, string(Desired)+".json"),
		rollbackPath:     filepath.Join(dataDir, string(Rollback)+".json"),
		deviceReadWriter: deviceReadWriter,
		osClient:         osClient,
		backoff:          backoff,
		cache:            newCache(log),
		queue:            queue,
		log:              log,
	}
}

func (s *manager) Initialize(ctx context.Context) error {
	// current
	if err := s.write(Current, newVersionedDevice("0")); err != nil {
		return err
	}
	// desired
	if err := s.write(Desired, newVersionedDevice("0")); err != nil {
		return err
	}
	// rollback
	if err := s.write(Rollback, newVersionedDevice("0")); err != nil {
		return err
	}
	// reconcile the initial spec even though its empty
	s.queue.Add(ctx, newVersionedDevice("0"))
	return nil
}

func (s *manager) Ensure() error {
	for _, specType := range []Type{Current, Desired, Rollback} {
		exists, err := s.exists(specType)
		if err != nil {
			return err
		}

		if !exists {
			s.log.Warnf("Spec file does not exist %s. Resetting state to empty...", specType)
			if err := s.write(specType, newVersionedDevice("0")); err != nil {
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

		if err := s.write(Current, desired); err != nil {
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
	rollback.Spec = &v1alpha1.DeviceSpec{
		Os: &v1alpha1.DeviceOsSpec{Image: currentOSImage},
	}

	if err := s.write(Rollback, rollback); err != nil {
		return err
	}
	return nil
}

func (s *manager) ClearRollback() error {
	return s.write(Rollback, newVersionedDevice(""))
}

func (s *manager) Rollback(ctx context.Context, opts ...RollbackOption) error {
	cfg := &rollbackConfig{}

	// apply opts
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.setFailed {
		version, err := stringToInt64(s.cache.getRenderedVersion(Desired))
		if err != nil {
			return err
		}
		// set the desired spec as failed

		s.queue.SetFailed(version)
	}

	// copy the current rendered spec to the desired rendered spec
	// this will reconcile the device with the desired "rollback" state
	if err := s.deviceReadWriter.CopyFile(s.currentPath, s.desiredPath); err != nil {
		return fmt.Errorf("%w: copy %q to %q: %w", errors.ErrCopySpec, s.currentPath, s.desiredPath, err)
	}

	// update cache
	desired, err := s.Read(Desired)
	if err != nil {
		return err
	}

	// add the desired spec back to the queue to ensure reconciliation
	s.queue.Add(ctx, desired)

	s.cache.update(Desired, desired)
	return nil
}

func (s *manager) Read(specType Type) (*v1alpha1.Device, error) {
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

func (s *manager) GetDesired(ctx context.Context) (*v1alpha1.Device, bool, error) {
	renderedVersion, err := s.getRenderedVersion()
	if err != nil {
		return nil, false, err
	}
	newDesired := &v1alpha1.Device{}

	startTime := time.Now()
	err = wait.ExponentialBackoff(s.backoff, func() (bool, error) {
		return s.getRenderedFromManagementAPIWithRetry(ctx, renderedVersion, newDesired)
	})

	// log slow calls
	duration := time.Since(startTime)
	if duration > time.Minute {
		s.log.Debugf("Dialing management API took: %v", duration)
	}

	if err != nil {
		desired, readErr := s.Read(Desired)
		if readErr != nil {
			return nil, false, readErr
		}
		// no content means there is no new rendered version
		if errors.Is(err, errors.ErrNoContent) || errors.IsTimeoutError(err) {
			s.log.Debug("No new template version from management service")
		} else {
			s.log.Errorf("Received non-retryable error from management service: %v", err)
			return nil, false, err
		}
		s.log.Debugf("Requeuing current desired spec from disk version: %s", desired.Version())
		s.queue.Add(ctx, desired)
	} else {
		s.log.Debugf("New template version received from management service: %s", newDesired.Version())
		s.queue.Add(ctx, newDesired)
	}

	return s.getDeviceFromQueue(ctx)
}

// getDeviceFromQueue retrieves the next desired device to reconcile.  Returns true
// to signal requeue if no spec is available. If the spec is newer than the
// current desired version it will be written to disk.
func (s *manager) getDeviceFromQueue(ctx context.Context) (*v1alpha1.Device, bool,
	error) {
	desired, exists := s.queue.Next(ctx)
	if !exists {
		// no spec available, signal to requeue
		return nil, true, nil
	}

	// if this is a new version ensure we persist it to disk
	if desired.Version() != s.cache.getRenderedVersion(Desired) {
		s.log.Infof("Writing new desired rendered spec to disk version: %s", desired.Version())
		if err := s.write(Desired, desired); err != nil {
			return nil, false, err
		}
	}

	return desired, false, nil
}

func (s *manager) SetClient(client client.Management) {
	s.managementClient = client
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

func (s *manager) Status(ctx context.Context, status *v1alpha1.DeviceStatus, _ ...status.CollectorOpt) error {
	status.Config.RenderedVersion = s.cache.getRenderedVersion(Current)
	return nil
}

func (s *manager) CheckPolicy(ctx context.Context, policyType policy.Type, version string) error {
	return s.queue.CheckPolicy(ctx, policyType, version)
}

func (s *manager) write(specType Type, device *v1alpha1.Device) error {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return err
	}

	err = writeDeviceToFile(s.deviceReadWriter, device, filePath)
	if err != nil {
		return fmt.Errorf("writing %s: %w", specType, err)
	}

	s.cache.update(specType, device)

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

func (m *manager) getRenderedFromManagementAPIWithRetry(
	ctx context.Context,
	renderedVersion string,
	rendered *v1alpha1.Device,
) (bool, error) {
	params := &v1alpha1.GetRenderedDeviceParams{}
	if renderedVersion != "" {
		params.KnownRenderedVersion = &renderedVersion
	}

	resp, statusCode, err := m.managementClient.GetRenderedDevice(ctx, m.deviceName, params)
	if err != nil {
		return false, fmt.Errorf("%w: %w", errors.ErrGettingDeviceSpec, err)
	}

	switch statusCode {
	case http.StatusOK:
		if resp == nil {
			// 200 OK but response is nil
			return false, errors.ErrNilResponse
		}
		*rendered = *resp
		return true, nil

	case http.StatusNoContent, http.StatusConflict:
		// instead of treating it as an error indicate that no new content is available
		return true, errors.ErrNoContent

	default:
		// unexpected status codes
		return false, fmt.Errorf("%w: unexpected status code %d", errors.ErrGettingDeviceSpec, statusCode)
	}
}

func readDeviceFromFile(
	reader fileio.Reader,
	filePath string,
) (*v1alpha1.Device, error) {
	var current v1alpha1.Device
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

func writeDeviceToFile(writer fileio.Writer, device *v1alpha1.Device, filePath string) error {
	deviceBytes, err := json.Marshal(device)
	if err != nil {
		return err
	}
	if err := writer.WriteFile(filePath, deviceBytes, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("%w: writing to %q: %w", errors.ErrWritingRenderedSpec, filePath, err)
	}
	return nil
}

func IsUpgrading(current *v1alpha1.Device, desired *v1alpha1.Device) bool {
	return current.Version() != desired.Version()
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
