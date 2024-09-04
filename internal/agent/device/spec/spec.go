package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
	"golang.org/x/net/context"
	"k8s.io/apimachinery/pkg/util/wait"
)

var (
	ErrMissingRenderedSpec = fmt.Errorf("missing rendered spec")
	ErrReadingRenderedSpec = fmt.Errorf("reading rendered spec")
	ErrWritingRenderedSpec = fmt.Errorf("writing rendered spec")
	ErrNoContent           = fmt.Errorf("no content")
	ErrCheckingFileExists  = fmt.Errorf("checking if file exists")
	ErrUnmarshalSpec       = fmt.Errorf("unmarshalling spec")
)

type Type string

const (
	Current  Type = "current"
	Desired  Type = "desired"
	Rollback Type = "rollback"
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
	GetDesired(ctx context.Context, renderedVersion string) (*v1alpha1.RenderedDeviceSpec, error)
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
	return &SpecManager{
		deviceName:       deviceName,
		currentPath:      filepath.Join(dataDir, string(Current)+".json"),
		desiredPath:      filepath.Join(dataDir, string(Desired)+".json"),
		rollbackPath:     filepath.Join(dataDir, string(Rollback)+".json"),
		deviceReadWriter: deviceReadWriter,
		bootcClient:      bootcClient,
		backoff:          backoff,
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
	desired, err := s.Read(Desired)
	if err != nil {
		return fmt.Errorf("read current rendered spec: %w", err)
	}

	if err := s.write(Current, desired); err != nil {
		return fmt.Errorf("write current rendered spec: %w", err)
	}

	s.log.Infof("Spec upgrade complete: clearing rollback spec")
	// clear the rollback spec
	return s.write(Rollback, &v1alpha1.RenderedDeviceSpec{})
}

func (s *SpecManager) PrepareRollback(ctx context.Context) error {
	current, err := s.Read(Current)
	if err != nil {
		return fmt.Errorf("read rollback rendered spec: %w", err)
	}

	// it is possible that the current rendered spec does not have an OS image.
	// In this case, we need to get the booted image from bootc.
	var currentOSImage string
	if current.Os == nil || current.Os.Image == "" {
		bootcStatus, err := s.bootcClient.Status(ctx)
		if err != nil {
			return fmt.Errorf("getting current bootc status: %w", err)
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
		return fmt.Errorf("write rollback to desired rendered spec: %w", err)
	}
	return nil
}

func (s *SpecManager) Rollback() error {
	// copy the current rendered spec to the desired rendered spec
	// this will reconcile the device with the desired "rollback" state
	return s.deviceReadWriter.CopyFile(s.currentPath, s.desiredPath)
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
	return spec, nil
}

func (s *SpecManager) GetDesired(ctx context.Context, currentRenderedVersion string) (*v1alpha1.RenderedDeviceSpec, error) {
	desired, err := s.Read(Desired)
	if err != nil {
		return nil, fmt.Errorf("read desired rendered spec: %w", err)
	}

	rollback, err := s.Read(Rollback)
	if err != nil {
		return nil, fmt.Errorf("read rollback rendered spec: %w", err)
	}

	renderedVersion, err := s.getRenderedVersion(currentRenderedVersion, desired.RenderedVersion, rollback.RenderedVersion)
	if err != nil {
		return nil, fmt.Errorf("get next rendered version: %w", err)
	}

	newDesired := &v1alpha1.RenderedDeviceSpec{}
	err = wait.ExponentialBackoff(s.backoff, func() (bool, error) {
		return s.getRenderedFromManagementAPIWithRetry(ctx, renderedVersion, newDesired)
	})
	if err != nil {
		// no content means there is no new rendered version
		if errors.Is(err, ErrNoContent) {
			s.log.Debug("No content from management API, falling back to the desired spec on disk")
			// TODO: can we avoid resync or is this necessary?
			return desired, nil
		}
		s.log.Warnf("Failed to get rendered device spec after retry: %v", err)
		return nil, err
	}

	s.log.Infof("Received desired rendered spec from management service with rendered version: %s", newDesired.RenderedVersion)
	if newDesired.RenderedVersion == desired.RenderedVersion {
		s.log.Infof("No new rendered version from management service, retry reconciling version: %s", newDesired.RenderedVersion)
		return desired, nil
	}

	// write to disk
	s.log.Infof("Writing desired rendered spec to disk with rendered version: %s", newDesired.RenderedVersion)
	if err := s.write(Desired, newDesired); err != nil {
		return nil, fmt.Errorf("write rendered spec to disk: %w", err)
	}
	return newDesired, nil
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
		return "", false, fmt.Errorf("getting current bootc status: %w", err)
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
	return nil
}

func (s *SpecManager) exists(specType Type) (bool, error) {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return false, err
	}
	exists, err := s.deviceReadWriter.FileExists(filePath)
	if err != nil {
		return false, fmt.Errorf("%w: %s: %w:", ErrCheckingFileExists, specType, err)
	}
	return exists, nil
}

// getRenderedVersion returns the last rendered version observed by the device. If the current rendered version
// matches the rollback version, the next version is returned to ensure the device progresses to the next version.
func (s *SpecManager) getRenderedVersion(currentRenderedVersion, desiredRenderedVersion, rollbackRenderedVersion string) (string, error) {
	// bootstrap case
	if currentRenderedVersion == "" {
		// empty is a valid state
		return "", nil
	}
	if currentRenderedVersion == rollbackRenderedVersion && desiredRenderedVersion == rollbackRenderedVersion {
		s.log.Info("Rollback detected, awaiting next rendered version")
		nextRenderedVersion, err := getNextRenderedVersion(currentRenderedVersion)
		if err != nil {
			return "", fmt.Errorf("get next rendered version: %w", err)
		}
		return nextRenderedVersion, nil
	}
	return currentRenderedVersion, nil
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
		return "", fmt.Errorf("unknown spec type: %s", specType)
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
		return false, err
	}
	if statusCode == http.StatusNoContent || statusCode == http.StatusConflict {
		// TODO: this is a bit of a hack
		return true, ErrNoContent
	}

	if resp != nil {
		*rendered = *resp
		return true, nil
	}
	return false, fmt.Errorf("received nil response for rendered device spec")
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
			return nil, fmt.Errorf("%w: reading %q: %w", ErrMissingRenderedSpec, filePath, err)
		}
		return nil, fmt.Errorf("%w: reading %q: %w", ErrReadingRenderedSpec, filePath, err)
	}

	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &current); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnmarshalSpec, err)
	}

	return &current, nil
}

func writeRenderedToFile(writer fileio.Writer, rendered *v1alpha1.RenderedDeviceSpec, filePath string) error {
	renderedBytes, err := json.Marshal(rendered)
	if err != nil {
		return err
	}
	if err := writer.WriteFile(filePath, renderedBytes, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("%w: writing to %q: %w", ErrWritingRenderedSpec, filePath, err)
	}
	return nil
}

func IsUpdating(current *v1alpha1.RenderedDeviceSpec, desired *v1alpha1.RenderedDeviceSpec) bool {
	return current.RenderedVersion != desired.RenderedVersion
}

func getNextRenderedVersion(renderedVersion string) (string, error) {
	// bootstrap case
	if renderedVersion == "" {
		return "", nil
	}
	version, err := strconv.Atoi(renderedVersion)
	if err != nil {
		return "", fmt.Errorf("failed to convert version to integer: %v", err)
	}

	nextVersion := version + 1
	return strconv.Itoa(nextVersion), nil
}
