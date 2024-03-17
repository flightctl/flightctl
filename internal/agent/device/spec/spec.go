package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/sirupsen/logrus"
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
	managementClient        *client.Management

	log       *logrus.Logger
	logPrefix string
	backoff   wait.Backoff
}

// NewManager creates a new device spec manager.
func NewManager(
	deviceName string,
	currentRenderedFilePath string,
	desiredRenderedFilePath string,
	deviceWriter *fileio.Writer,
	deviceReader *fileio.Reader,
	managementClient *client.Management,
	backoff wait.Backoff,
	log *logrus.Logger,
	logPrefix string,
) *Manager {
	return &Manager{
		deviceName:              deviceName,
		deviceWriter:            deviceWriter,
		deviceReader:            deviceReader,
		currentRenderedFilePath: currentRenderedFilePath,
		desiredRenderedFilePath: desiredRenderedFilePath,
		managementClient:        managementClient,
		log:                     log,
		logPrefix:               logPrefix,
		backoff:                 backoff,
	}
}

// WriteCurrentRendered writes the rendered device spec to disk
func (s *Manager) WriteCurrentRendered(rendered *v1alpha1.RenderedDeviceSpec) error {
	return writeRenderedSpecToFile(s.deviceWriter, rendered, s.currentRenderedFilePath)
}

// GetRendered returns the current and desired rendered device specs.
func (s *Manager) GetRendered(ctx context.Context) (v1alpha1.RenderedDeviceSpec, v1alpha1.RenderedDeviceSpec, bool, error) {
	current, err := s.getCurrentRenderedSpec()
	if err != nil {
		return v1alpha1.RenderedDeviceSpec{}, v1alpha1.RenderedDeviceSpec{}, false, err
	}

	desired, skipSync, err := s.getDesiredRenderedSpec(ctx, current.Owner, current.TemplateVersion)
	if err != nil {
		return v1alpha1.RenderedDeviceSpec{}, v1alpha1.RenderedDeviceSpec{}, false, fmt.Errorf("get rendered spec: %w", err)
	}
	return current, desired, skipSync, nil
}

func (m *Manager) getCurrentRenderedSpec() (v1alpha1.RenderedDeviceSpec, error) {
	var current v1alpha1.RenderedDeviceSpec
	renderedBytes, err := m.deviceReader.ReadFile(m.currentRenderedFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			// if the file does not exist, this means it has been removed/corrupted
			return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("%w: current: %w", ErrMissingRenderedSpec, err)
		}
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("read device specification from '%s': %w", m.currentRenderedFilePath, err)
	}

	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &current); err != nil {
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("unmarshal device specification: %w", err)
	}

	return current, nil
}

// getDesiredRenderedSpec returns the desired rendered device spec from the management API or from disk if the API is unavailable.
func (m *Manager) getDesiredRenderedSpec(ctx context.Context, owner, templateVersion string) (v1alpha1.RenderedDeviceSpec, bool, error) {
	var desired v1alpha1.RenderedDeviceSpec
	err := wait.ExponentialBackoff(m.backoff, func() (bool, error) {
		return m.getRenderedSpecFromManagementAPIWithRetry(ctx, owner, templateVersion, &desired)
	})
	if err != nil {
		if errors.Is(err, ErrNoContent) {
			return v1alpha1.RenderedDeviceSpec{}, true, nil
		}
		m.log.Warningf("%sFailed to get rendered device spec after retry: %v", m.logPrefix, err)
	} else {
		// write to disk
		m.log.Infof("%swriting desired rendered spec to disk", m.logPrefix)
		if err := writeRenderedSpecToFile(m.deviceWriter, &desired, m.desiredRenderedFilePath); err != nil {
			return v1alpha1.RenderedDeviceSpec{}, false, fmt.Errorf("write rendered spec to disk: %w", err)
		}
		return desired, false, nil
	}

	// fall back to latest from disk
	m.log.Infof("%sfalling back to latest rendered spec from disk", m.logPrefix)

	renderedBytes, err := os.ReadFile(m.desiredRenderedFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			// if the file does not exist, this means it has been removed/corrupted
			return v1alpha1.RenderedDeviceSpec{}, false, fmt.Errorf("%w: rendered device has been deleted: %w", ErrMissingRenderedSpec, err)
		}
		return v1alpha1.RenderedDeviceSpec{}, false, fmt.Errorf("read from '%s': %w", m.desiredRenderedFilePath, err)
	}

	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &desired); err != nil {
		return v1alpha1.RenderedDeviceSpec{}, false, fmt.Errorf("unmarshal %T: %w", desired, err)
	}

	return desired, false, nil
}

func (m *Manager) getRenderedSpecFromManagementAPIWithRetry(
	ctx context.Context,
	owner string,
	templateVersion string,
	rendered *v1alpha1.RenderedDeviceSpec,
) (bool, error) {
	params := &v1alpha1.GetRenderedDeviceSpecParams{
		KnownOwner:           &owner,
		KnownTemplateVersion: &templateVersion,
	}
	resp, statusCode, err := m.managementClient.GetRenderedDeviceSpec(ctx, m.deviceName, params)
	if err != nil {
		return false, err
	}
	if statusCode == http.StatusNoContent {
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
	log *logrus.Logger,
	logPrefix string,
	writer *fileio.Writer,
	reader *fileio.Reader,
	deviceName,
	filePath string,
) (v1alpha1.RenderedDeviceSpec, error) {
	var current v1alpha1.RenderedDeviceSpec
	renderedBytes, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Infof("%scurrent rendered spec file does not exist, initializing as empty", logPrefix)
			if err := writeRenderedSpecToFile(writer, &v1alpha1.RenderedDeviceSpec{}, filePath); err != nil {
				return v1alpha1.RenderedDeviceSpec{}, err
			}
			log.Infof("%swrote initial current rendered spec to disk", logPrefix)

			return v1alpha1.RenderedDeviceSpec{}, nil
		}
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("read device specification from '%s': %w", filePath, err)
	}
	// read bytes from file
	if err := json.Unmarshal(renderedBytes, &current); err != nil {
		return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("unmarshal device specification: %w", err)
	}
	return current, nil
}

// EnsureDesiredRenderedSpec ensures the desired rendered spec exists on disk or is initialized from the management API.
func EnsureDesiredRenderedSpec(
	ctx context.Context,
	log *logrus.Logger,
	logPrefix string,
	writer *fileio.Writer,
	reader *fileio.Reader,
	managementClient *client.Management,
	deviceName,
	filePath string,
	backoff wait.Backoff,
) (v1alpha1.RenderedDeviceSpec, error) {
	var desired v1alpha1.RenderedDeviceSpec
	renderedBytes, err := reader.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Infof("%sdesired rendered spec file does not exist, fetching from management API", logPrefix)
			var rendered *v1alpha1.RenderedDeviceSpec
			// retry on failure
			err := wait.ExponentialBackoffWithContext(ctx, backoff, func() (bool, error) {
				var statusCode int
				log.Infof("%s attempting to fetch desired spec from management API", logPrefix)
				rendered, statusCode, err = managementClient.GetRenderedDeviceSpec(ctx, deviceName, &v1alpha1.GetRenderedDeviceSpecParams{})
				if err != nil {
					return false, err
				}
				if statusCode == http.StatusNoContent {
					log.Warningf("%s received 204 from management API", logPrefix)
				}

				return true, nil
			})
			if err != nil {
				log.Errorf("%sFailed to get desired spec after retry: %v", logPrefix, err)
				// this could mean the management API is not available, or the internet connection is down.
				// we really can't make progress here without the desired rendered spec. this should fail the service
				// and require a restart.
				return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("get rendered device spec: %w", err)
			}
			// on StatusNoContent the response object is nil
			if rendered == nil {
				return v1alpha1.RenderedDeviceSpec{}, fmt.Errorf("received empty response for rendered device spec")
			}

			if err := writeRenderedSpecToFile(writer, rendered, filePath); err != nil {
				return v1alpha1.RenderedDeviceSpec{}, err
			}
			log.Infof("%swrote initial rendered spec to disk", logPrefix)

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

func writeRenderedSpecToFile(writer *fileio.Writer, rendered *v1alpha1.RenderedDeviceSpec, filePath string) error {
	renderedBytes, err := json.Marshal(rendered)
	if err != nil {
		return err
	}
	if err := writer.WriteIgnitionFiles(
		fileio.NewIgnFileBytes(filePath, renderedBytes, fileio.DefaultFilePermissions),
	); err != nil {
		return fmt.Errorf("write default device spec file %q: %w", filePath, err)
	}
	return nil
}
