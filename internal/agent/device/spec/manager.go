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

var _ Manager = (*manager)(nil)

// Manager is responsible for managing the rendered device spec.
type manager struct {
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
	queue                   PriorityQueue

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
) Manager {
	queue := newQueue(
		log,
		defaultSpecRequeueMaxRetries,
		defaultSpecQueueSize,
		defaultSpecRequeueThreshold,
		defaultSpecRequeueDelay,
	)
	return &manager{
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

func (s *manager) Initialize() error {
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

func (s *manager) Ensure() error {
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
	return s.queue.Add(newItem(desired))
}

func (s *manager) IsRollingBack(ctx context.Context) (bool, error) {
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

func (s *manager) Upgrade() error {
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

	// remove desired spec from the queue
	s.queue.Remove(s.desiredRenderedVersion)
	return nil
}

func (s *manager) SetUpgradeFailed() {
	s.queue.SetVersionFailed(s.desiredRenderedVersion)
}

func (s *manager) IsUpgrading() bool {
	return s.currentRenderedVersion != s.desiredRenderedVersion
}

func (s *manager) PrepareRollback(ctx context.Context) error {
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

func (s *manager) Rollback() error {
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

func (s *manager) Read(specType Type) (*v1alpha1.RenderedDeviceSpec, error) {
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

func (s *manager) GetDesired(ctx context.Context) (*v1alpha1.RenderedDeviceSpec, bool, error) {
	renderedVersion := s.getRenderedVersion()
	newDesired := &v1alpha1.RenderedDeviceSpec{}

	startTime := time.Now()
	err := wait.ExponentialBackoff(s.backoff, func() (bool, error) {
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
		s.log.Debugf("Requeuing current desired spec from disk version: %s", desired.RenderedVersion)
		if err := s.queue.Add(newItem(desired)); err != nil {
			return nil, false, err
		}
	} else {
		s.log.Debugf("New template version received from management service: %s", newDesired.RenderedVersion)
		if err := s.queue.Add(newItem(newDesired)); err != nil {
			return nil, false, err
		}
	}

	return s.getSpecFromQueue()
}

// getSpecFromQueue retrieves the next desired spec to reconcile.  Returns true
// to signal requeue if no spec is available. If the spec is newer than the
// current desired version it will be written to disk.
func (s *manager) getSpecFromQueue() (*v1alpha1.RenderedDeviceSpec, bool,
	error) {
	item, exists := s.queue.Next()
	if !exists {
		// no spec available, signal to requeue
		return nil, true, nil
	}

	// if this is a new version ensure we persist it to disk
	desired := item.Spec
	if desired.RenderedVersion != s.desiredRenderedVersion {
		s.log.Infof("Writing new desired rendered spec to disk version: %s", desired.RenderedVersion)
		if err := s.write(Desired, desired); err != nil {
			return nil, false, err
		}
	}

	return desired, false, nil
}

func (s *manager) SetClient(client client.Management) {
	s.managementClient = client
}

func (s *manager) IsOSUpdate() (bool, error) {
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

func (s *manager) CheckOsReconciliation(ctx context.Context) (string, bool, error) {
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

func (s *manager) write(specType Type, spec *v1alpha1.RenderedDeviceSpec) error {
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
func (s *manager) getRenderedVersion() string {
	if s.queue.IsVersionFailed(s.desiredRenderedVersion) {
		return s.desiredRenderedVersion
	}
	return s.currentRenderedVersion
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
