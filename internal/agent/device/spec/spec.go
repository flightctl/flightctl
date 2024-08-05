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
	ErrNoContent           = fmt.Errorf("no content")
)

type SpecType string

const (
	Current  SpecType = "current"
	Desired  SpecType = "desired"
	Rollback SpecType = "rollback"
)

var _ Manager = (*SpecManager)(nil)

type ManagerNoClient interface {
	// Initialize initializes the current and desired spec files on disk if they do not exist.
	Initialize() error
	// Read returns the rendered device spec of the specified type from disk.
	Read(specType SpecType) (*Rendered, error)
	// Write writes the rendered device spec of the specified type to disk.
	Write(specType SpecType, rendered *Rendered) error
	// Exists returns true if the rendered device spec of the specified type exists on disk.
	Exists(specType SpecType) (bool, error)
	// IsOSUpdateInProgress returns true if an OS update is in progress by checking the current rendered spec.
	IsOSUpdateInProgress() (bool, error)
	// IsOsImageReconciled returns true if the desired OS image matches the booted OS image.
	IsOsImageReconciled(bootcHost *container.BootcHost) (bool, error)
	// Rollback rolls back the desired spec to the current spec and marks the version as failed.
	Rollback() error
}

type Manager interface {
	ManagerNoClient
	// SetClient sets the management API client.
	SetClient(client.Management)
	// GetDesired returns the desired rendered device spec from the management API.
	GetDesired(ctx context.Context, renderedVersion string, rollback bool) (*Rendered, error)
}

// Manager is responsible for managing the rendered device spec.
type SpecManager struct {
	deviceName  string
	currentPath string
	desiredPath string

	deviceReadWriter fileio.ReadWriter
	managementClient client.Management

	// failedRenderedVersions is a map of failed rendered versions to the next version to fetch from the management API.
	failedRenderedVersions map[string]string

	log     *log.PrefixLogger
	backoff wait.Backoff
}

// NewManager creates a new device spec manager.
func NewManager(
	deviceName string,
	dataDir string,
	deviceReadWriter fileio.ReadWriter,
	backoff wait.Backoff,
	log *log.PrefixLogger,
) *SpecManager {
	return &SpecManager{
		deviceName:             deviceName,
		currentPath:            filepath.Join(dataDir, string(Current)+".json"),
		desiredPath:            filepath.Join(dataDir, string(Desired)+".json"),
		deviceReadWriter:       deviceReadWriter,
		failedRenderedVersions: make(map[string]string),
		backoff:                backoff,
		log:                    log,
	}
}

func (s *SpecManager) Initialize() error {
	// current
	if err := s.Write(Current, NewRendered()); err != nil {
		return fmt.Errorf("writing current rendered spec: %w", err)
	}
	// desired
	if err := s.Write(Desired, NewRendered()); err != nil {
		return fmt.Errorf("writing desired rendered spec: %w", err)
	}
	return nil
}

func (s *SpecManager) Rollback() error {
	desired, err := s.Read(Desired)
	if err != nil {
		return fmt.Errorf("read desired rendered spec: %w", err)
	}

	// mark the current version as failed
	nextRenderedVersion, err := getNextRenderedVersion(desired.RenderedVersion)
	if err != nil {
		return fmt.Errorf("get next version: %w", err)
	}
	s.failedRenderedVersions[desired.RenderedVersion] = nextRenderedVersion

	rollbackCurrent, err := s.Read(Current)
	if err != nil {
		return fmt.Errorf("read rollback rendered spec: %w", err)
	}

	// write the current spec to the desired spec with the rollback bool set
	rollbackCurrent.Rollback = true

	if err := s.Write(Desired, rollbackCurrent); err != nil {
		return fmt.Errorf("write rollback to desired rendered spec: %w", err)
	}
	return nil
}

func (s *SpecManager) Write(specType SpecType, spec *Rendered) error {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return err
	}
	return writeRenderedToFile(s.deviceReadWriter, spec, filePath)
}

func (s *SpecManager) Read(specType SpecType) (*Rendered, error) {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return nil, err
	}
	s.log.Infof("#### Reading rendered spec from disk: %s", filePath)
	return readRenderedFromFile(s.deviceReadWriter, filePath)
}

func (s *SpecManager) Exists(specType SpecType) (bool, error) {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return false, err
	}
	return s.deviceReadWriter.FileExists(filePath)
}

func (s *SpecManager) GetDesired(ctx context.Context, currentRenderedVersion string, currentIsRolledBack bool) (*Rendered, error) {
	desired, err := s.Read(Desired)
	if err != nil {
		return nil, fmt.Errorf("read desired rendered spec: %w", err)
	}
	if desired.Rollback && !currentIsRolledBack {
		return desired, nil
	}

	renderedVersion, err := s.getRenderedVersion(currentRenderedVersion, currentIsRolledBack)
	if err != nil {
		return nil, fmt.Errorf("get next rendered version: %w", err)
	}

	desired = &Rendered{}
	err = wait.ExponentialBackoff(s.backoff, func() (bool, error) {
		return s.getRenderedFromManagementAPIWithRetry(ctx, renderedVersion, desired.RenderedDeviceSpec)
	})
	if err != nil {
		if !errors.Is(err, ErrNoContent) {
			s.log.Warnf("Failed to get rendered device spec after retry: %v", err)
		}
		return nil, err
	}

	// write to disk
	s.log.Infof("Writing desired rendered spec to disk with rendered version: %s", desired.RenderedVersion)
	if err := writeRenderedToFile(s.deviceReadWriter, desired, s.desiredPath); err != nil {
		return nil, fmt.Errorf("write rendered spec to disk: %w", err)
	}
	return desired, nil
}

func (s *SpecManager) getRenderedVersion(currentRenderedVersion string, isRolledBack bool) (string, error) {
	nextRenderedVersion, failed := s.failedRenderedVersions[currentRenderedVersion]
	if failed && isRolledBack {
		return nextRenderedVersion, nil
	}

	return currentRenderedVersion, nil
}

func (s *SpecManager) SetClient(client client.Management) {
	s.managementClient = client
}

func (s *SpecManager) IsOSUpdateInProgress() (bool, error) {
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

func (s *SpecManager) IsOsImageReconciled(bootcHost *container.BootcHost) (bool, error) {
	desired, err := s.Read(Desired)
	if err != nil {
		return false, err
	}

	if desired.Os == nil {
		return false, nil
	}

	return desired.Os.Image == bootcHost.GetBootedImage(), nil

}

func (s *SpecManager) pathFromType(specType SpecType) (string, error) {
	var filePath string
	switch specType {
	case Current:
		filePath = s.currentPath
	case Desired:
		filePath = s.desiredPath
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

func readRenderedFromFile(
	reader fileio.Reader,
	filePath string,
) (*Rendered, error) {
	current := NewRendered() 
	renderedBytes, err := reader.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// if the file does not exist, this means it has been removed/corrupted
			return nil, fmt.Errorf("%w: current: %w", ErrMissingRenderedSpec, err)
		}
		return nil, fmt.Errorf("read device specification from '%s': %w", filePath, err)
	}

	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &current); err != nil {
		return nil, fmt.Errorf("unmarshal device specification: %w", err)
	}

	return current, nil
}

func writeRenderedToFile(writer fileio.Writer, rendered *Rendered, filePath string) error {
	renderedBytes, err := json.Marshal(rendered)
	if err != nil {
		return err
	}
	if err := writer.WriteFile(filePath, renderedBytes, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("write default device spec file %q: %w", filePath, err)
	}
	return nil
}

func IsUpdate(current *Rendered, desired *Rendered) bool {
	return current.RenderedVersion == desired.RenderedVersion
}

func getNextRenderedVersion(renderedVersion string) (string, error) {
	version, err := strconv.Atoi(renderedVersion)
	if err != nil {
		return "", fmt.Errorf("failed to convert version to integer: %v", err)
	}

	nextVersion := version + 1
	return strconv.Itoa(nextVersion), nil
}
