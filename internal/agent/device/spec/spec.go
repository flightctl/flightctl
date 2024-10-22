package spec

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
	"golang.org/x/net/context"
	"k8s.io/apimachinery/pkg/util/wait"
)

type Type string

const (
	Current  Type = "current"
	Desired  Type = "desired"
	Rollback Type = "rollback"

	// defaultMaxRetries is the default number of retries for a spec item set to 0 for infinite retries.
	defaultSpecRequeueMaxRetries = 0
	defaultSpecQueueSize         = 1
	defaultSpecRequeueThreshold  = 1
	defaultSpecRequeueDelay      = 1 * time.Minute
)

var _ Manager = (*SpecManager)(nil)

type Manager interface {
	// Initialize initializes the current, desired and rollback spec files on
	// disk. If the files already exist, they are overwritten.
	Initialize() error
	// Ensure ensures that spec files exist on disk and re initializes them if they do not.
	Ensure() error
	// Read returns the rendered device spec of the specified type from disk.
	Read(specType Type) (*v1alpha1.RenderedDeviceSpec, error)
	// Upgrade updates the current rendered spec to the desired rendered spec
	// and resets the rollback spec.
	Upgrade() error
	// SetUpgradeFailed marks the desired rendered spec as failed.
	SetUpgradeFailed()
	// IsUpdating returns true if the device is in the process of reconciling the desired spec.
	IsUpgrading() bool
	// IsOSUpdate returns true if an OS update is in progress by checking the current rendered spec.
	IsOSUpdate() (bool, error)
	// CheckOsReconciliation checks if the booted OS image matches the desired OS image.
	CheckOsReconciliation(ctx context.Context) (string, bool, error)
	// IsRollingBack returns true if the device is in a rollback state.
	IsRollingBack(ctx context.Context) (bool, error)
	// PrepareRollback creates a rollback version of the current rendered spec.
	PrepareRollback(ctx context.Context) error
	// Rollback reverts the device to the state of the rollback rendered spec.
	Rollback() error
	// SetClient sets the management API client.
	SetClient(client.Management)
	// GetDesired returns the desired rendered device spec from the management API.
	GetDesired(ctx context.Context) (*v1alpha1.RenderedDeviceSpec, bool, error)
}

// Manager is responsible for managing the rendered device spec.
type SpecManager struct {
	deviceName   string
	currentPath  string
	desiredPath  string
	rollbackPath string

	deviceReadWriter fileio.ReadWriter
	managementClient client.Management
	bootcClient      container.BootcClient

	// cached rendered versions
	currentRenderedVersion  string
	desiredRenderedVersion  string
	rollbackRenderedVersion string
	queue                   *Queue

	log     *log.PrefixLogger
	backoff wait.Backoff
}

// NewManager creates a new device spec manager.
func NewManager(
	deviceName string,
	dataDir string,
	deviceReadWriter fileio.ReadWriter,
	bootcClient container.BootcClient,
	backoff wait.Backoff,
	log *log.PrefixLogger,
) *SpecManager {
	queue := newQueue(
		log,
		defaultSpecRequeueMaxRetries,
		defaultSpecQueueSize,
		defaultSpecRequeueThreshold,
		defaultSpecRequeueDelay,
	)
	return &SpecManager{
		deviceName:       deviceName,
		currentPath:      filepath.Join(dataDir, string(Current)+".json"),
		desiredPath:      filepath.Join(dataDir, string(Desired)+".json"),
		rollbackPath:     filepath.Join(dataDir, string(Rollback)+".json"),
		deviceReadWriter: deviceReadWriter,
		bootcClient:      bootcClient,
		backoff:          backoff,
		queue:            queue,
		log:              log,
	}
}

func (s *SpecManager) Initialize() error {
	// current
	if err := s.write(Current, &v1alpha1.RenderedDeviceSpec{}); err != nil {
		return err
	}
	// desired
	if err := s.write(Desired, &v1alpha1.RenderedDeviceSpec{}); err != nil {
		return err
	}
	// rollback
	if err := s.write(Rollback, &v1alpha1.RenderedDeviceSpec{}); err != nil {
		return err
	}
	return nil
}

func (s *SpecManager) Ensure() error {
	for _, specType := range []Type{Current, Desired, Rollback} {
		exists, err := s.exists(specType)
		if err != nil {
			return err
		}

		if !exists {
			s.log.Warnf("Spec file does not exist %s. Resetting state to empty...", specType)
			if err := s.write(specType, &v1alpha1.RenderedDeviceSpec{}); err != nil {
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
	s.currentRenderedVersion = current.RenderedVersion
	s.desiredRenderedVersion = desired.RenderedVersion
	s.rollbackRenderedVersion = rollback.RenderedVersion

	// add the desired spec to the queue this ensures that the device will
	// reconcile the desired spec on startup.
	s.queue.Add(newItem(desired))

	return nil
}

func (s *SpecManager) IsRollingBack(ctx context.Context) (bool, error) {
	desired, err := s.Read(Desired)
	if err != nil {
		return false, err
	}

	if desired.Os == nil || desired.Os.Image == "" {
		return false, nil
	}

	rollback, err := s.Read(Rollback)
	if err != nil {
		return false, err
	}

	if rollback.Os == nil || rollback.Os.Image == "" {
		return false, nil
	}

	bootcStatus, err := s.bootcClient.Status(ctx)
	if err != nil {
		return false, err
	}

	// The system is in a rollback state if:
	// 1. There is no staged OS image, indicating that no update is in progress.
	// 2. The currently booted OS image matches the rollback image.
	// 3. The booted image does not match the desired OS image.
	if bootcStatus.GetStagedImage() != "" {
		return false, nil
	}

	bootedOSImage := bootcStatus.GetBootedImage()
	return bootedOSImage == rollback.Os.Image && bootedOSImage != desired.Os.Image, nil
}

func (s *SpecManager) Upgrade() error {
	// only upgrade if the device is in the process of reconciling the desired spec
	if s.IsUpgrading() {
		desired, err := s.Read(Desired)
		if err != nil {
			return err
		}

		if err := s.write(Current, desired); err != nil {
			return err
		}

		if err := s.write(Rollback, &v1alpha1.RenderedDeviceSpec{}); err != nil {
			return err
		}
		s.log.Infof("Spec reconciliation complete: current version %s", desired.RenderedVersion)
	}

	// clear the desired spec from the queue
	s.queue.forget(s.desiredRenderedVersion)
	return nil
}

func (s *SpecManager) SetUpgradeFailed() {
	s.queue.SetVersionFailed(s.desiredRenderedVersion)
}

func (s *SpecManager) IsUpgrading() bool {
	return s.currentRenderedVersion != s.desiredRenderedVersion
}

func (s *SpecManager) PrepareRollback(ctx context.Context) error {
	current, err := s.Read(Current)
	if err != nil {
		return err
	}

	// it is possible that the current rendered spec does not have an OS image.
	// In this case, we need to get the booted image from bootc.
	var currentOSImage string
	if current.Os == nil || current.Os.Image == "" {
		bootcStatus, err := s.bootcClient.Status(ctx)
		if err != nil {
			return fmt.Errorf("%w: %w", errors.ErrGettingBootcStatus, err)
		}
		currentOSImage = bootcStatus.GetBootedImage()
	} else {
		currentOSImage = current.Os.Image
	}

	// rollback is a basic copy of the current rendered spec
	// which contains the rendered version and the OS image.
	rollback := &v1alpha1.RenderedDeviceSpec{
		RenderedVersion: current.RenderedVersion,
		Os:              &v1alpha1.DeviceOSSpec{Image: currentOSImage},
	}

	if err := s.write(Rollback, rollback); err != nil {
		return err
	}
	return nil
}

func (s *SpecManager) Rollback() error {
	// set the desired spec as failed
	s.queue.SetVersionFailed(s.desiredRenderedVersion)

	// copy the current rendered spec to the desired rendered spec
	// this will reconcile the device with the desired "rollback" state
	err := s.deviceReadWriter.CopyFile(s.currentPath, s.desiredPath)
	if err != nil {
		return fmt.Errorf("%w: copy %q to %q", errors.ErrCopySpec, s.currentPath, s.desiredPath)
	}

	// update cached rendered versions
	desired, err := s.Read(Desired)
	if err != nil {
		return err
	}

	// update cached rendered versions
	s.desiredRenderedVersion = desired.RenderedVersion
	return nil
}

func (s *SpecManager) Read(specType Type) (*v1alpha1.RenderedDeviceSpec, error) {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return nil, err
	}
	spec, err := readRenderedSpecFromFile(s.deviceReadWriter, filePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", specType, err)
	}

	// since we already have the version in memory update cached version.
	switch specType {
	case Current:
		s.currentRenderedVersion = spec.RenderedVersion
	case Desired:
		s.desiredRenderedVersion = spec.RenderedVersion
	case Rollback:
		s.rollbackRenderedVersion = spec.RenderedVersion
	}

	return spec, nil
}

func (s *SpecManager) GetDesired(ctx context.Context) (*v1alpha1.RenderedDeviceSpec, bool, error) {
	renderedVersion, err := s.getRenderedVersion()
	if err != nil {
		return nil, false, fmt.Errorf("get next rendered version: %w", err)
	}

	newDesired := &v1alpha1.RenderedDeviceSpec{}
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
		}
		s.log.Debugf("Requeuing current desired spec from disk version: %s", desired.RenderedVersion)
		s.queue.Add(newItem(desired))
	} else {
		// write to disk
		s.log.Infof("Writing new desired rendered spec to disk version: %s", newDesired.RenderedVersion)
		if err := s.write(Desired, newDesired); err != nil {
			return nil, false, err
		}
		s.queue.Add(newItem(newDesired))
	}

	return s.getSpecFromQueue()
}

// getSpecFromQueue retrieves the next desired spec to reconcile.
// Returns true to signal requeue if no spec is available.
func (s *SpecManager) getSpecFromQueue() (*v1alpha1.RenderedDeviceSpec, bool,
	error) {
	desired, exists := s.queue.Get()
	if !exists {
		// no spec available, signal to requeue
		return nil, true, nil
	}
	return desired, false, nil
}

func (s *SpecManager) SetClient(client client.Management) {
	s.managementClient = client
}

func (s *SpecManager) IsOSUpdate() (bool, error) {
	current, err := s.Read(Current)
	if err != nil {
		return false, err
	}
	desired, err := s.Read(Desired)
	if err != nil {
		return false, err
	}

	currentImage := ""
	if current.Os != nil {
		currentImage = current.Os.Image
	}
	desiredImage := ""
	if desired.Os != nil {
		desiredImage = desired.Os.Image
	}
	return currentImage != desiredImage, nil
}

func (s *SpecManager) CheckOsReconciliation(ctx context.Context) (string, bool, error) {
	bootc, err := s.bootcClient.Status(ctx)
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", errors.ErrGettingBootcStatus, err)
	}
	bootedOSImage := bootc.GetBootedImage()

	desired, err := s.Read(Desired)
	if err != nil {
		return "", false, err
	}

	if desired.Os == nil {
		return bootedOSImage, false, nil
	}

	return bootedOSImage, desired.Os.Image == bootc.GetBootedImage(), nil
}

func (s *SpecManager) write(specType Type, spec *v1alpha1.RenderedDeviceSpec) error {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return err
	}

	err = writeRenderedToFile(s.deviceReadWriter, spec, filePath)
	if err != nil {
		return fmt.Errorf("writing %s: %w", specType, err)
	}

	// since we already have the version in memory update cached version.
	switch specType {
	case Current:
		s.currentRenderedVersion = spec.RenderedVersion
	case Desired:
		s.desiredRenderedVersion = spec.RenderedVersion
	case Rollback:
		s.rollbackRenderedVersion = spec.RenderedVersion
	}

	return nil
}

func (s *SpecManager) exists(specType Type) (bool, error) {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return false, err
	}
	exists, err := s.deviceReadWriter.FileExists(filePath)
	if err != nil {
		return false, fmt.Errorf("%w: %s: %w", errors.ErrCheckingFileExists, specType, err)
	}
	return exists, nil
}

// getRenderedVersion returns the next rendered version to reconcile. If the
// desired rendered version is marked as failed it will return the next version
// to make progress.
func (s *SpecManager) getRenderedVersion() (string, error) {
	if s.queue.IsVersionFailed(s.desiredRenderedVersion) {
		return getNextRenderedVersion(s.currentRenderedVersion)
	}
	return s.currentRenderedVersion, nil
}

func (s *SpecManager) pathFromType(specType Type) (string, error) {
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

func (m *SpecManager) getRenderedFromManagementAPIWithRetry(
	ctx context.Context,
	renderedVersion string,
	rendered *v1alpha1.RenderedDeviceSpec,
) (bool, error) {
	params := &v1alpha1.GetRenderedDeviceSpecParams{}
	if renderedVersion != "" {
		params.KnownRenderedVersion = &renderedVersion
	}

	resp, statusCode, err := m.managementClient.GetRenderedDeviceSpec(ctx, m.deviceName, params)
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

func readRenderedSpecFromFile(
	reader fileio.Reader,
	filePath string,
) (*v1alpha1.RenderedDeviceSpec, error) {
	var current v1alpha1.RenderedDeviceSpec
	renderedBytes, err := reader.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
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

func writeRenderedToFile(writer fileio.Writer, rendered *v1alpha1.RenderedDeviceSpec, filePath string) error {
	renderedBytes, err := json.Marshal(rendered)
	if err != nil {
		return err
	}
	if err := writer.WriteFile(filePath, renderedBytes, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("%w: writing to %q: %w", errors.ErrWritingRenderedSpec, filePath, err)
	}
	return nil
}

func IsUpgrading(current *v1alpha1.RenderedDeviceSpec, desired *v1alpha1.RenderedDeviceSpec) bool {
	return current.RenderedVersion != desired.RenderedVersion
}

func getNextRenderedVersion(renderedVersion string) (string, error) {
	// bootstrap case
	if renderedVersion == "" {
		return "", nil
	}
	version, err := strconv.Atoi(renderedVersion)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errors.ErrParseRenderedVersion, err)
	}

	nextVersion := version + 1
	return strconv.Itoa(nextVersion), nil
}

func stringToInt64(s string) int64 {
	if s == "" {
		return 0
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	if i < 0 {
		return 0
	}
	return i
}
