package applications

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	AppTypeLabel            = "appType"
	DefaultImageManifestDir = "/"
)

type ContainerStatusType string

const (
	ContainerStatusCreated ContainerStatusType = "created"
	ContainerStatusInit    ContainerStatusType = "init"
	ContainerStatusRunning ContainerStatusType = "start"
	ContainerStatusStop    ContainerStatusType = "stop"
	ContainerStatusDie     ContainerStatusType = "die" // docker only
	ContainerStatusDied    ContainerStatusType = "died"
	ContainerStatusRemove  ContainerStatusType = "remove"
	ContainerStatusExited  ContainerStatusType = "exited"
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
	Ensure(app Application) error
	Remove(app Application) error
	Update(app Application) error
	BeforeUpdate(ctx context.Context, desired *v1alpha1.DeviceSpec) error
	AfterUpdate(ctx context.Context) error
	Stop(ctx context.Context) error
	status.Exporter
}

type Application interface {
	// ID is an internal identifier for tracking the application this may or may
	// not be the name provided by the user. How this ID is generated is
	// determined on the application type level.
	ID() string
	// Name is the name of the application as defined by the user. If the name
	// is not populated by the user a name will be generated based on the
	// application type.
	Name() string
	// Type returns the application type.
	Type() AppType
	// EnvVars returns the environment variables for the application.
	EnvVars() map[string]string
	// SetEnvVars sets the environment variables for the application.
	SetEnvVars(envVars map[string]string) bool
	// Path returns the path to the application on the device.
	Path() (string, error)
	// Container returns a container by name.
	Container(name string) (*Container, bool)
	// AddContainer adds a container to the application.
	AddContainer(container Container)
	// RemoveContainer removes a container from the application.
	RemoveContainer(name string) bool
	// IsEmbedded returns true if the application is embedded.
	IsEmbedded() bool
	// Status reports the status of an application using the name as defined by
	// the user. In the case there is no name provided it will be populated
	// according to the rules of the application type.
	Status() (*v1alpha1.DeviceApplicationStatus, v1alpha1.DeviceApplicationsSummaryStatus, error)
}

// EmbeddedProvider is a provider for embedded applications.
type EmbeddedProvider struct{}

type Container struct {
	ID       string
	Image    string
	Name     string
	Status   ContainerStatusType
	Restarts int
}

type application[T any] struct {
	id         string
	envVars    map[string]string
	containers []Container
	appType    AppType
	provider   T
	status     *v1alpha1.DeviceApplicationStatus
	embedded   bool
}

func NewApplication[T any](id, name string, provider T, appType AppType) *application[T] {
	a := &application[T]{
		id: id,
		status: &v1alpha1.DeviceApplicationStatus{
			Name:   name,
			Status: v1alpha1.ApplicationStatusPreparing,
		},
		provider: provider,
		appType:  appType,
		envVars:  make(map[string]string),
	}
	if _, ok := any(provider).(EmbeddedProvider); ok {
		a.embedded = true
	}
	return a
}

func (a *application[T]) ID() string {
	return a.id
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
		return "", fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, a.Type())
	}

	return filepath.Join(typePath, a.Name()), nil
}

func (a *application[T]) RemoveContainer(name string) bool {
	for i := range a.containers {
		if a.containers[i].Name == name {
			a.containers = append(a.containers[:i], a.containers[i+1:]...)
			return true
		}
	}
	return false
}

func (a *application[T]) IsEmbedded() bool {
	return a.embedded
}

func (a *application[T]) Status() (*v1alpha1.DeviceApplicationStatus, v1alpha1.DeviceApplicationsSummaryStatus, error) {
	// TODO: revisit performance of this function
	healthy := 0
	initializing := 0
	restarts := 0
	exited := 0
	for _, container := range a.containers {
		restarts += container.Restarts
		switch container.Status {
		case ContainerStatusInit, ContainerStatusCreated:
			initializing++
		case ContainerStatusRunning:
			healthy++
		case ContainerStatusExited:
			exited++
		}
	}

	total := len(a.containers)
	var summary v1alpha1.DeviceApplicationsSummaryStatus
	readyStatus := strconv.Itoa(healthy) + "/" + strconv.Itoa(total)

	var newStatus v1alpha1.ApplicationStatusType

	// order is important
	switch {
	case isUnknown(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusUnknown
		summary.Status = v1alpha1.ApplicationsSummaryStatusUnknown
	case isStarting(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusStarting
		summary.Status = v1alpha1.ApplicationsSummaryStatusDegraded
	case isPreparing(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusPreparing
		summary.Status = v1alpha1.ApplicationsSummaryStatusUnknown
	case isCompleted(total, exited):
		newStatus = v1alpha1.ApplicationStatusCompleted
		summary.Status = v1alpha1.ApplicationsSummaryStatusHealthy
	case isRunningHealthy(total, healthy, initializing, exited):
		newStatus = v1alpha1.ApplicationStatusRunning
		summary.Status = v1alpha1.ApplicationsSummaryStatusHealthy
	case isRunningDegraded(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusRunning
		summary.Status = v1alpha1.ApplicationsSummaryStatusDegraded
	case isErrored(total, healthy, initializing):
		newStatus = v1alpha1.ApplicationStatusError
		summary.Status = v1alpha1.ApplicationsSummaryStatusError
	default:
		summary.Status = v1alpha1.ApplicationsSummaryStatusUnknown
		return nil, summary, fmt.Errorf("unknown application status: %d/%d/%d", total, healthy, initializing)
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

func isCompleted(total, completed int) bool {
	return total > 0 && completed == total
}

func isPreparing(total, healthy, initializing int) bool {
	return total > 0 && healthy == 0 && initializing > 0
}

func isRunningDegraded(total, healthy, initializing int) bool {
	return total != healthy && healthy > 0 && initializing == 0
}

func isRunningHealthy(total, healthy, initializing, exited int) bool {
	return total > 0 && (healthy == total || healthy+exited == total) && initializing == 0
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
		return "", fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, a)
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
func ImageProvidersFromSpec(spec *v1alpha1.DeviceSpec) ([]v1alpha1.ImageApplicationProvider, error) {
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

// typeFromImage returns the app type from the image label take from the image in local container storage.
func typeFromImage(ctx context.Context, podman *client.Podman, image string) (AppType, error) {
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

// ensureDependenciesFromType ensures that the dependencies required for the given app type are available.
func ensureDependenciesFromType(appType AppType) error {
	var deps []string
	switch appType {
	case AppCompose:
		deps = []string{"docker-compose", "podman-compose"}
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}

	for _, dep := range deps {
		if client.IsCommandAvailable(dep) {
			return nil
		}
	}

	return fmt.Errorf("%w: %v", errors.ErrAppDependency, deps)
}

func copyImageManifests(ctx context.Context, log *log.PrefixLogger, writer fileio.Writer, podman *client.Podman, image, destPath string) (err error) {
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
	err = filepath.Walk(writer.PathFor(mountPoint), func(filePath string, info os.FileInfo, err error) error {
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
