package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

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
	TypeCurrent  SpecType = "current"
	TypeDesired  SpecType = "desired"
	TypeRollback SpecType = "rollback"
)

type ManagerNoClient interface {
	// Initialize initializes the current, desired and rollback spec files on disk if they do not exist.
	Initialize() error
	// ReadSpec returns the rendered device spec of the specified type from disk.
	Read(specType SpecType) (*v1alpha1.RenderedDeviceSpec, error)
	// WriteSpec writes the rendered device spec of the specified type to disk.
	Write(specType SpecType, rendered *v1alpha1.RenderedDeviceSpec) error
	// SpecExists returns true if the rendered device spec of the specified type exists on disk.
	Exists(specType SpecType) (bool, error)
	// IsOSUpdateInProgress returns true if an OS update is in progress by checking the current rendered spec.
	IsOSUpdateInProgress() (bool, error)
	// IsOsImageReconciled returns true if the desired OS image matches the booted OS image.
	IsOsImageReconciled(bootcHost *container.BootcHost) (bool, error)
}

type Manager interface {
	ManagerNoClient
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
		deviceName:       deviceName,
		currentPath:      filepath.Join(dataDir, string(TypeCurrent)+".json"),
		desiredPath:      filepath.Join(dataDir, string(TypeDesired)+".json"),
		rollbackPath:     filepath.Join(dataDir, string(TypeRollback)+".json"),
		deviceReadWriter: deviceReadWriter,
		backoff:          backoff,
		log:              log,
	}
}

func (s *SpecManager) Initialize() error {
	if err := s.Write(TypeCurrent, &v1alpha1.RenderedDeviceSpec{}); err != nil {
		return fmt.Errorf("writing current rendered spec: %w", err)
	}
	// desired
	if err := s.Write(TypeDesired, &v1alpha1.RenderedDeviceSpec{}); err != nil {
		return fmt.Errorf("writing desired rendered spec: %w", err)
	}
	// rollback
	if err := s.Write(TypeRollback, &v1alpha1.RenderedDeviceSpec{}); err != nil {
		return fmt.Errorf("writing rollback rendered spec: %w", err)
	}
	return nil
}

func (s *SpecManager) Write(specType SpecType, spec *v1alpha1.RenderedDeviceSpec) error {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return err
	}
	return writeRenderedSpecToFile(s.deviceReadWriter, spec, filePath)
}

func (s *SpecManager) Read(specType SpecType) (*v1alpha1.RenderedDeviceSpec, error) {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return nil, err
	}
	return readRenderedSpecFromFile(s.deviceReadWriter, filePath)
}

func (s *SpecManager) Exists(specType SpecType) (bool, error) {
	filePath, err := s.pathFromType(specType)
	if err != nil {
		return false, err
	}
	return s.deviceReadWriter.FileExists(filePath)
}

func (s *SpecManager) GetDesired(ctx context.Context, renderedVersion string) (*v1alpha1.RenderedDeviceSpec, error) {
	var desired v1alpha1.RenderedDeviceSpec
	err := wait.ExponentialBackoff(s.backoff, func() (bool, error) {
		return s.getRenderedSpecFromManagementAPIWithRetry(ctx, renderedVersion, &desired)
	})
	if err != nil {
		if !errors.Is(err, ErrNoContent) {
			s.log.Warnf("Failed to get rendered device spec after retry: %v", err)
		}
		return nil, err
	}

	// write to disk
	s.log.Infof("Writing desired rendered spec to disk with rendered version: %s", desired.RenderedVersion)
	if err := writeRenderedSpecToFile(s.deviceReadWriter, &desired, s.desiredPath); err != nil {
		return nil, fmt.Errorf("write rendered spec to disk: %w", err)
	}
	return &desired, nil
}

func (s *SpecManager) SetClient(client client.Management) {
	s.managementClient = client
}

func (s *SpecManager) IsOSUpdateInProgress() (bool, error) {
	current, err := s.Read(TypeCurrent)
	if err != nil {
		return false, err
	}
	desired, err := s.Read(TypeDesired)
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
	desired, err := s.Read(TypeDesired)
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
	case TypeCurrent:
		filePath = s.currentPath
	case TypeDesired:
		filePath = s.desiredPath
	case TypeRollback:
		filePath = s.rollbackPath
	default:
		return "", fmt.Errorf("unknown spec type: %s", specType)
	}
	return filePath, nil
}

func (m *SpecManager) getRenderedSpecFromManagementAPIWithRetry(
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
			return nil, fmt.Errorf("%w: current: %w", ErrMissingRenderedSpec, err)
		}
		return nil, fmt.Errorf("read device specification from '%s': %w", filePath, err)
	}

	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &current); err != nil {
		return nil, fmt.Errorf("unmarshal device specification: %w", err)
	}

	return &current, nil
}

func writeRenderedSpecToFile(writer fileio.Writer, rendered *v1alpha1.RenderedDeviceSpec, filePath string) error {
	renderedBytes, err := json.Marshal(rendered)
	if err != nil {
		return err
	}
	if err := writer.WriteFile(filePath, renderedBytes, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("write default device spec file %q: %w", filePath, err)
	}
	return nil
}
