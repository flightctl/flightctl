package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
	"golang.org/x/net/context"
	"k8s.io/apimachinery/pkg/util/wait"
)

var (
	ErrMissingRenderedSpec  = fmt.Errorf("missing rendered spec")
	ErrReadingRenderedSpec  = fmt.Errorf("reading rendered spec")
	ErrWritingRenderedSpec  = fmt.Errorf("writing rendered spec")
	ErrCheckingFileExists   = fmt.Errorf("checking if file exists")
	ErrUnmarshalSpec        = fmt.Errorf("unmarshalling spec")
	ErrCopySpec             = fmt.Errorf("copying spec")
	ErrGettingBootcStatus   = fmt.Errorf("getting current bootc status")
	ErrInvalidSpecType      = fmt.Errorf("invalid spec type")
	ErrParseRenderedVersion = fmt.Errorf("failed to convert version to integer")

	// Errors related to fetching the rendered device spec
	ErrNoContent         = fmt.Errorf("no content")
	ErrNilResponse       = fmt.Errorf("received nil response for rendered device spec")
	ErrGettingDeviceSpec = fmt.Errorf("getting device spec")
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
	CheckOsReconciliation(ctx context.Context) (*Image, bool, error)
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

	bootedImage := bootcStatusToImage(bootcStatus)
	rollbackImage := SpecToImage(rollback.Os)
	desiredImage := SpecToImage(desired.Os)

	return AreImagesEquivalent(bootedImage, rollbackImage) && !AreImagesEquivalent(bootedImage, desiredImage), nil
}

func (s *SpecManager) Upgrade() error {
	desired, err := s.Read(Desired)
	if err != nil {
		return err
	}

	if err := s.write(Current, desired); err != nil {
		return err
	}

	s.log.Infof("Spec upgrade complete: clearing rollback spec")
	// clear the rollback spec
	return s.write(Rollback, &v1alpha1.RenderedDeviceSpec{})
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
			return fmt.Errorf("%w: %w", ErrGettingBootcStatus, err)
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
	// copy the current rendered spec to the desired rendered spec
	// this will reconcile the device with the desired "rollback" state
	err := s.deviceReadWriter.CopyFile(s.currentPath, s.desiredPath)
	if err != nil {
		return fmt.Errorf("%w: copy %q to %q", ErrCopySpec, s.currentPath, s.desiredPath)
	}
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
	return spec, nil
}

func (s *SpecManager) GetDesired(ctx context.Context, currentRenderedVersion string) (*v1alpha1.RenderedDeviceSpec, error) {
	desired, err := s.Read(Desired)
	if err != nil {
		return nil, err
	}

	rollback, err := s.Read(Rollback)
	if err != nil {
		return nil, err
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
		return nil, err
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

	currentImage := SpecToImage(current.Os)
	desiredImage := SpecToImage(desired.Os)

	return !AreImagesEquivalent(currentImage, desiredImage), nil
}

// TODO delete
func isOsSame(first *v1alpha1.DeviceOSSpec, second *v1alpha1.DeviceOSSpec) bool {
	firstImage := ""
	firstDigest := ""
	if first != nil {
		firstImage = first.Image
		if first.ImageDigest != nil {
			firstDigest = *first.ImageDigest
		}
	}
	secondImage := ""
	secondDigest := ""
	if second != nil {
		secondImage = second.Image
		if second.ImageDigest != nil {
			secondDigest = *second.ImageDigest
		}
	}

	if firstDigest != "" && secondDigest != "" {
		return firstDigest == secondDigest
	}
	return firstImage == secondImage
}

// TODO does this need more handling for cases where the image might be defined but fields are ""?
func AreImagesEquivalent(first, second *Image) bool {
	if first == nil && second == nil {
		return true
	} else if first == nil && second != nil || first != nil && second == nil {
		return false
	}

	// Digests are unique identifiers and have precedence if defined
	if first.Digest != "" && second.Digest != "" {
		return first.Digest == second.Digest
	}

	if first.Base != second.Base {
		return false
	}

	if first.Tag != second.Tag {
		return false
	}

	return true
}

// TODO define on Image?
// TODO also define a string representation that spits back out the fully built image string?
func ImageToBootcTarget(image *Image) string {
	if image.Digest != "" {
		return fmt.Sprintf("%s@%s", image.Base, image.Digest)
	}
	if image.Tag != "" {
		return fmt.Sprintf("%s:%s", image.Base, image.Tag)
	}
	return image.Base
}

func (s *SpecManager) CheckOsReconciliation(ctx context.Context) (*Image, bool, error) {
	bootc, err := s.bootcClient.Status(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrGettingBootcStatus, err)
	}

	desired, err := s.Read(Desired)
	if err != nil {
		return nil, false, err
	}

	bootedImage := bootcStatusToImage(bootc)

	if desired.Os == nil {
		return bootedImage, false, nil
	}

	desiredImage := SpecToImage(desired.Os)
	return bootedImage, AreImagesEquivalent(bootedImage, desiredImage), nil
}

// TODO where should this live
type Image struct {
	Base   string
	Tag    string
	Digest string
}

func parseImage(image string) *Image {
	imageObj := &Image{}

	imageAndDigest := strings.SplitN(image, "@", 2)
	if len(imageAndDigest) == 2 {
		imageObj.Digest = imageAndDigest[1]
	}

	imageAndTag := strings.SplitN(imageAndDigest[0], ":", 2)
	imageObj.Base = imageAndTag[0]
	if len(imageAndTag) == 2 {
		imageObj.Tag = imageAndTag[1]
	}

	return imageObj
}

func SpecToImage(spec *v1alpha1.DeviceOSSpec) *Image {
	if spec == nil || spec.Image == "" {
		return nil
	}
	image := parseImage(spec.Image)
	// It is possible for the spec image string to NOT contain a digest but the
	// saved spec has one
	if image.Digest == "" && spec.ImageDigest != nil && *spec.ImageDigest != "" {
		image.Digest = *spec.ImageDigest
	}
	return image
}

func bootcStatusToImage(bootc *container.BootcHost) *Image {
	if bootc == nil {
		return nil
	}

	bootedOsImage := bootc.GetBootedImage()
	image := parseImage(bootedOsImage)

	// If the parsed image string doesn't have a digest, explicitly set it from the bootc status
	if image.Digest == "" {
		image.Digest = bootc.GetBootedImageDigeest()
	}

	return image
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
		return "", fmt.Errorf("%w: %s", ErrInvalidSpecType, specType)
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
		return false, fmt.Errorf("%w: %w", ErrGettingDeviceSpec, err)
	}
	if statusCode == http.StatusNoContent || statusCode == http.StatusConflict {
		// TODO: this is a bit of a hack
		return true, ErrNoContent
	}

	if resp != nil {
		*rendered = *resp
		return true, nil
	}
	return false, ErrNilResponse
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
		return "", fmt.Errorf("%w: %v", ErrParseRenderedVersion, err)
	}

	nextVersion := version + 1
	return strconv.Itoa(nextVersion), nil
}
