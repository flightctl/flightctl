package applications

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

var (
	ErrNameRequired         = errors.New("application name is required")
	ErrNotFound             = errors.New("application not found")
	ErrorUnsupportedAppType = errors.New("unsupported application type")
	ErrFailedToParseAppType = errors.New("failed to parse application type")
)

const (
	AppTypeLabel            = "appType"
	DefaultImageManifestDir = "/"
)

type ContainerStatusType string

const (
	ContainerStatusInit    ContainerStatusType = "init"
	ContainerStatusRunning ContainerStatusType = "start"
	ContainerStatusDie     ContainerStatusType = "die"
)

func (c ContainerStatusType) String() string {
	return string(c)
}

type AppType string

const (
	AppCompose AppType = "compose"
)

type Monitor interface {
	Run(ctx context.Context)
	Status() []v1alpha1.DeviceApplicationStatus
}

type Manager interface {
	Add(app Application) error
	Remove(app Application) error
	Update(app Application) error
	ExecuteActions(ctx context.Context) error
	Status() ([]v1alpha1.DeviceApplicationStatus, v1alpha1.ApplicationsSummaryStatusType, error)
}

type Application interface {
	Name() string
	Type() AppType
	EnvVars() map[string]string
	SetEnvVars(envVars map[string]string) bool
	Path() (string, error)
	Container(name string) (*Container, bool)
	AddContainer(container Container)
	Status() (*v1alpha1.DeviceApplicationStatus, v1alpha1.ApplicationsSummaryStatusType, error)
}

type Container struct {
	ID       string
	Image    string
	Name     string
	Status   ContainerStatusType
	Restarts int
}

type application[T any] struct {
	envVars    map[string]string
	containers []Container
	appType    AppType
	provider   T
	status     *v1alpha1.DeviceApplicationStatus
	embedded   bool
}

func NewApplication[T any](name string, provider T, appType AppType) *application[T] {
	return &application[T]{
		status: &v1alpha1.DeviceApplicationStatus{
			Name:   name,
			Status: v1alpha1.ApplicationStatusPreparing,
		},
		provider: provider,
		appType:  appType,
		envVars:  make(map[string]string),
	}
}

func (a *application[T]) Name() string {
	return a.status.Name
}

func (a *application[T]) Type() AppType {
	return a.appType
}

func (a *application[T]) EnvVars() map[string]string {
	return a.envVars
}

func (a *application[T]) SetEnvVars(envVars map[string]string) bool {
	if len(envVars) == 0 {
		return false
	}
	// TODO evaluate if we should merge or replace
	a.envVars = envVars
	return true
}

func (a *application[T]) Container(name string) (*Container, bool) {
	for i := range a.containers {
		if a.containers[i].Name == name {
			return &a.containers[i], true
		}
	}
	return nil, false
}

func (a *application[T]) AddContainer(container Container) {
	a.containers = append(a.containers, container)
}

func (a *application[T]) Path() (string, error) {
	var typePath string
	switch a.Type() {
	case AppCompose:
		if a.embedded {
			typePath = lifecycle.EmbeddedComposeAppPath
			break
		}
		typePath = lifecycle.ComposeAppPath
	default:
		return "", fmt.Errorf("%w: %s", ErrorUnsupportedAppType, a.Type())
	}

	return filepath.Join(typePath, a.Name()), nil
}

func (a *application[T]) Status() (*v1alpha1.DeviceApplicationStatus, v1alpha1.ApplicationsSummaryStatusType, error) {
	// TODO: revisit performance of this function
	healthy := 0
	initializing := 0
	restarts := 0
	for _, container := range a.containers {
		restarts += container.Restarts
		if container.Status == ContainerStatusRunning {
			healthy++
		}
		if container.Status == ContainerStatusInit {
			initializing++
		}
	}

	total := len(a.containers)
	var summary v1alpha1.ApplicationsSummaryStatusType
	readyStatus := strconv.Itoa(healthy) + "/" + strconv.Itoa(total)

	var newStatus v1alpha1.ApplicationStatusType

	switch {
	case isUnknown(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusUnknown
		summary = v1alpha1.ApplicationsSummaryStatusUnknown
	case isStarting(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusStarting
		summary = v1alpha1.ApplicationsSummaryStatusUnknown
	case isPreparing(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusPreparing
		summary = v1alpha1.ApplicationsSummaryStatusUnknown
	case isRunningDegraded(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusRunning
		summary = v1alpha1.ApplicationsSummaryStatusDegraded
	case isErrored(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusError
		summary = v1alpha1.ApplicationsSummaryStatusError
	case isRunningHealthy(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusRunning
		summary = v1alpha1.ApplicationsSummaryStatusHealthy
	default:
		return nil, v1alpha1.ApplicationsSummaryStatusUnknown, fmt.Errorf("unknown application status: %d/%d/%d", total, healthy, initializing)
	}

	if a.status.Status != newStatus {
		a.status.Status = newStatus
	}
	if a.status.Ready != readyStatus {
		a.status.Ready = readyStatus
	}
	if a.status.Restarts != restarts {
		a.status.Restarts = restarts
	}

	return a.status, summary, nil
}

func isStarting(total, healthy, initializing int) bool {
	return total > 0 && initializing > 0 && healthy > 0
}

func isUnknown(total, healthy, initializing int) bool {
	return total == 0 && healthy == 0 && initializing == 0
}

func isPreparing(total, healthy, initializing int) bool {
	return total > 0 && healthy == 0 && initializing > 0
}

func isRunningDegraded(total, healthy, initializing int) bool {
	return total != healthy && healthy > 0 && initializing == 0
}

func isRunningHealthy(total, healthy, initializing int) bool {
	return total > 0 && healthy == total && initializing == 0
}

func isErrored(total, healthy, initializing int) bool {
	return total > 0 && healthy == 0 && initializing == 0
}

func ParseAppType(s string) (AppType, error) {
	appType := AppType(s)
	if !appType.isValid() {
		return "", fmt.Errorf("invalid app type: %s", s)
	}
	return appType, nil
}

func (a AppType) isValid() bool {
	switch a {
	case AppCompose:
		return true
	default:
		return false
	}
}

func (a AppType) ActionHandler() (lifecycle.ActionHandlerType, error) {
	switch a {
	case AppCompose:
		return lifecycle.ActionHandlerCompose, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrorUnsupportedAppType, a)
	}
}

type applications struct {
	images []*application[*v1alpha1.ImageApplicationProvider]
	// add other types of application providers here
}

func (a *applications) ImageBased() []*application[*v1alpha1.ImageApplicationProvider] {
	return a.images
}

// ImageProvidersFromSpec returns a list of image application providers from a rendered device spec.
func ImageProvidersFromSpec(spec *v1alpha1.RenderedDeviceSpec) ([]v1alpha1.ImageApplicationProvider, error) {
	var providers []v1alpha1.ImageApplicationProvider
	for _, appSpec := range *spec.Applications {
		appProvider, err := appSpec.Type()
		if err != nil {
			return nil, err
		}
		if appProvider == v1alpha1.ImageApplicationProviderType {
			provider, err := appSpec.AsImageApplicationProvider()
			if err != nil {
				return nil, fmt.Errorf("failed to convert application to image provider: %w", err)
			}
			providers = append(providers, provider)
		}
	}
	return providers, nil
}

func TypeFromImage(ctx context.Context, podman *client.Podman, image string) (AppType, error) {
	labels, err := podman.InspectLabels(ctx, image)
	if err != nil {
		return "", err
	}
	appTypeLabel, ok := labels[AppTypeLabel]
	if !ok {
		return "", fmt.Errorf("required label not found: %s, %s", AppTypeLabel, image)
	}
	return ParseAppType(appTypeLabel)
}

func EnsureDependenciesFromType(appType AppType) error {
	var deps []string
	switch appType {
	case AppCompose:
		deps = []string{"docker-compose", "podman-compose"}
	default:
		return fmt.Errorf("%w: %s", ErrorUnsupportedAppType, appType)
	}

	for _, dep := range deps {
		if client.IsCommandAvailable(dep) {
			return nil
		}
	}

	return fmt.Errorf("application dependencies not found: %v", deps)
}

func CopyImageManifests(ctx context.Context, log *log.PrefixLogger, writer fileio.Writer, podman *client.Podman, image, destPath string) (err error) {
	var mountPoint string

	rootless := client.IsPodmanRootless()
	if rootless {
		log.Warnf("Running in rootless mode this is for testing only")
		mountPoint, err = podman.Unshare(ctx, "podman", "image", "mount", image)
		if err != nil {
			return fmt.Errorf("failed to execute podman share: %w", err)
		}
	} else {
		mountPoint, err = podman.Mount(ctx, image)
		if err != nil {
			return fmt.Errorf("failed to mount image: %w", err)
		}
	}

	if err := writer.MkdirAll(destPath, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("failed to dest create directory: %w", err)
	}

	defer func() {
		if err := podman.Unmount(ctx, image); err != nil {
			log.Errorf("failed to unmount image: %s %v", image, err)
		}
	}()

	// recursively copy image files to agent destination
	err = filepath.Walk(mountPoint, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			if info.Name() == "merged" {
				log.Debugf("Skipping merged directory: %s", filePath)
				return nil
			}
			log.Debugf("Creating directory: %s", info.Name())

			// ensure any directories in the image are also created
			return writer.MkdirAll(path.Join(destPath, info.Name()), fileio.DefaultDirectoryPermissions)
		}

		return copyImageFile(filePath, writer.PathFor(destPath))
	})
	if err != nil {
		return fmt.Errorf("error during copy: %w", err)
	}

	return nil
}

func copyImageFile(from, to string) error {
	// local writer ensures that the container from directory is correct.
	writer := fileio.NewWriter()
	if err := writer.CopyFile(from, to); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	return nil
}
