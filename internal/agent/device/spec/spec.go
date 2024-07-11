package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"golang.org/x/net/context"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// name of the file which stores the current device spec
	CurrentFile = "current-spec.json"
	// name of the file which stores the desired device spec
	DesiredFile = "desired-spec.json"
)

var (
	ErrMissingRenderedSpec = fmt.Errorf("missing rendered spec")
	ErrNoContent           = fmt.Errorf("no content")
)

// Manager is responsible for managing the rendered device spec.
type Manager struct {
	deviceName              string
	currentRenderedFilePath string
	desiredRenderedFilePath string
	deviceWriter            *fileio.Writer
	deviceReader            *fileio.Reader
	managementClient        client.Management

	log     *log.PrefixLogger
	backoff wait.Backoff
}

// NewManager creates a new device spec manager.
func NewManager(
	deviceName string,
	currentRenderedFilePath string,
	desiredRenderedFilePath string,
	deviceWriter *fileio.Writer,
	deviceReader *fileio.Reader,
	managementClient client.Management,
	backoff wait.Backoff,
	log *log.PrefixLogger,
) *Manager {
	return &Manager{
		deviceName:              deviceName,
		deviceWriter:            deviceWriter,
		deviceReader:            deviceReader,
		currentRenderedFilePath: currentRenderedFilePath,
		desiredRenderedFilePath: desiredRenderedFilePath,
		managementClient:        managementClient,
		log:                     log,
		backoff:                 backoff,
	}
}

// WriteCurrentRendered writes the rendered device spec to disk
func (s *Manager) WriteCurrentRendered(rendered *v1alpha1.RenderedDeviceSpec) error {
	return WriteRenderedSpecToFile(s.deviceWriter, rendered, s.currentRenderedFilePath)
}

// GetRendered returns the current and desired rendered device specs.
func (s *Manager) GetRendered(ctx context.Context) (v1alpha1.RenderedDeviceSpec, v1alpha1.RenderedDeviceSpec, error) {
	current, err := ReadRenderedSpecFromFile(s.deviceReader, s.currentRenderedFilePath)
	if err != nil {
		return v1alpha1.RenderedDeviceSpec{}, v1alpha1.RenderedDeviceSpec{}, err
	}

	desired, err := s.getDesiredRenderedSpec(ctx, current.RenderedVersion)
	if err != nil {
		return v1alpha1.RenderedDeviceSpec{}, v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("get rendered spec: %w", err)
	}
	return current, desired, nil
}

// getDesiredRenderedSpec returns the desired rendered device spec from the management API or from disk if the API is unavailable.
func (m *Manager) getDesiredRenderedSpec(ctx context.Context, renderedVersion string) (v1alpha1.RenderedDeviceSpec, error) {
	var desired v1alpha1.RenderedDeviceSpec
	err := wait.ExponentialBackoff(m.backoff, func() (bool, error) {
		return m.getRenderedSpecFromManagementAPIWithRetry(ctx, renderedVersion, &desired)
	})
	if err != nil {
		if !errors.Is(err, ErrNoContent) {
			m.log.Warnf("Failed to get rendered device spec after retry: %v", err)
		}
	} else {
		// write to disk
		m.log.Infof("Writing desired rendered spec to disk with rendered version: %s", desired.RenderedVersion)
		if err := WriteRenderedSpecToFile(m.deviceWriter, &desired, m.desiredRenderedFilePath); err != nil {
			return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("write rendered spec to disk: %w", err)
		}
		return desired, nil
	}

	// fall back to latest from disk
	m.log.Debug("Falling back to latest rendered spec from disk")

	renderedBytes, err := os.ReadFile(m.desiredRenderedFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			// TODO: handle this case better
			// if the file does not exist, this means it has been removed/corrupted
			return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("%w: rendered device has been deleted: %w", ErrMissingRenderedSpec, err)
		}
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("read from '%s': %w", m.desiredRenderedFilePath, err)
	}

	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &desired); err != nil {
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("unmarshal %T: %w", desired, err)
	}

	return desired, nil
}

func (m *Manager) getRenderedSpecFromManagementAPIWithRetry(
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

// EnsureCurrentRenderedSpec ensures the current rendered spec exists on disk or is initialized as empty.
func EnsureCurrentRenderedSpec(
	ctx context.Context,
	log *log.PrefixLogger,
	writer *fileio.Writer,
	reader *fileio.Reader,
	filePath string,
) (v1alpha1.RenderedDeviceSpec, error) {
	current, err := ReadRenderedSpecFromFile(reader, filePath)
	if err != nil {
		if errors.Is(err, ErrMissingRenderedSpec) {
			log.Info("Current rendered spec file does not exist, initializing as empty")
			if err := WriteRenderedSpecToFile(writer, &v1alpha1.RenderedDeviceSpec{}, filePath); err != nil {
				return v1alpha1.RenderedDeviceSpec{}, err
			}
			log.Info("Wrote initial current rendered spec to disk")

			return v1alpha1.RenderedDeviceSpec{}, nil
		}
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("read device specification from '%s': %w", filePath, err)
	}

	return current, nil
}

// EnsureDesiredRenderedSpec ensures the desired rendered spec exists on disk or is initialized from the management API.
func EnsureDesiredRenderedSpec(
	ctx context.Context,
	log *log.PrefixLogger,
	writer *fileio.Writer,
	reader *fileio.Reader,
	managementClient client.Management,
	deviceName string,
	filePath string,
	backoff wait.Backoff,
) (v1alpha1.RenderedDeviceSpec, error) {
	var desired v1alpha1.RenderedDeviceSpec
	renderedBytes, err := reader.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Info("Desired rendered spec file does not exist, fetching from management API")
			var rendered *v1alpha1.RenderedDeviceSpec
			// retry on failure
			err := wait.ExponentialBackoffWithContext(ctx, backoff, func() (bool, error) {
				var statusCode int
				log.Info("Attempting to fetch desired spec from management API")
				rendered, statusCode, err = managementClient.GetRenderedDeviceSpec(ctx, deviceName, &v1alpha1.GetRenderedDeviceSpecParams{})
				if err != nil && statusCode != http.StatusConflict {
					return false, err
				}
				if statusCode != http.StatusOK {
					log.Warnf("Received %d from management API", statusCode)
				}

				return true, nil
			})
			if err != nil {
				log.Errorf("Failed to get desired spec after retry: %v", err)
				// this could mean the management API is not available, or the internet connection is down.
				// we really can't make progress here without the desired rendered spec. this should fail the service
				// and require a restart.
				return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("get rendered device spec: %w", err)
			}
			// on StatusConflict the response object is nil
			if rendered == nil {
				rendered = &v1alpha1.RenderedDeviceSpec{}
			}

			if err := WriteRenderedSpecToFile(writer, rendered, filePath); err != nil {
				return v1alpha1.RenderedDeviceSpec{}, err
			}
			log.Info("Wrote initial rendered spec to disk")

			return *rendered, nil
		}
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("read device specification from '%s': %w", filePath, err)
	}

	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &desired); err != nil {
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("unmarshal device specification: %w", err)
	}

	return desired, nil
}

func ReadRenderedSpecFromFile(
	reader *fileio.Reader,
	filePath string,
) (v1alpha1.RenderedDeviceSpec, error) {
	var current v1alpha1.RenderedDeviceSpec
	renderedBytes, err := reader.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// if the file does not exist, this means it has been removed/corrupted
			return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("%w: current: %w", ErrMissingRenderedSpec, err)
		}
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("read device specification from '%s': %w", filePath, err)
	}

	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &current); err != nil {
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("unmarshal device specification: %w", err)
	}

	return current, nil
}

func WriteRenderedSpecToFile(writer *fileio.Writer, rendered *v1alpha1.RenderedDeviceSpec, filePath string) error {
	renderedBytes, err := json.Marshal(rendered)
	if err != nil {
		return err
	}
	if err := writer.WriteFile(filePath, renderedBytes, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("write default device spec file %q: %w", filePath, err)
	}
	return nil
}
