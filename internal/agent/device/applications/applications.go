package applications

import (
	"context"
	"fmt"
	"strconv"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	AppTypeLabel            = "appType"
	DefaultImageManifestDir = "/"
)

type StatusType string

const (
	StatusCreated StatusType = "created"
	StatusInit    StatusType = "init"
	StatusRunning StatusType = "start"
	StatusStop    StatusType = "stop"
	StatusDie     StatusType = "die" // docker only
	StatusDied    StatusType = "died"
	StatusRemove  StatusType = "remove"
	StatusExited  StatusType = "exited"
)

func (c StatusType) String() string {
	return string(c)
}

type Monitor interface {
	Run(ctx context.Context)
	Status() []v1alpha1.DeviceApplicationStatus
}

// Provider defines the interface for supplying and managing an application's spec
// and lifecycle operations for installation to disk.
type Provider interface {
	// Verify the application content is valid and dependencies are met.
	Verify(ctx context.Context) error
	// Install the application content to the device.
	Install(ctx context.Context) error
	// Remove the application content from the device.
	Remove(ctx context.Context) error
	// Name returns the name of the application.
	Name() string
	// Spec returns the application spec.
	Spec() *ApplicationSpec
}

// Manager coordinates the lifecycle of an application by interacting with its Provider
// and ensuring it is properly handed off to the appropriate runtime Monitor.
type Manager interface {
	// Ensure installs and starts the application on the device using the given provider.
	Ensure(ctx context.Context, provider Provider) error
	// Remove uninstalls the application from the device using the given provider.
	Remove(ctx context.Context, provider Provider) error
	// Update replaces the current application with a new version provided by the given provider.
	Update(ctx context.Context, provider Provider) error
	// BeforeUpdate is called prior to installing an application to ensure the
	// application is valid and dependencies are met.
	BeforeUpdate(ctx context.Context, desired *v1alpha1.DeviceSpec) error
	// AfterUpdate is called after the application has been validated and is ready to be executed.
	AfterUpdate(ctx context.Context) error
	// Stop halts the application running on the device.
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
	AppType() v1alpha1.AppType
	// Path returns the path to the application on the device.
	Path() string
	// Workload returns a workload by name.
	Workload(name string) (*Workload, bool)
	// AddWorkload adds a workload to the application.
	AddWorkload(Workload *Workload)
	// RemoveWorkload removes a workload from the application.
	RemoveWorkload(name string) bool
	// IsEmbedded returns true if the application is embedded.
	IsEmbedded() bool
	// Status reports the status of an application using the name as defined by
	// the user. In the case there is no name provided it will be populated
	// according to the rules of the application type.
	Status() (*v1alpha1.DeviceApplicationStatus, v1alpha1.DeviceApplicationsSummaryStatus, error)
}

// Workload represents an application workload tracked by a Monitor.
type Workload struct {
	ID       string
	Image    string
	Name     string
	Status   StatusType
	Restarts int
}

type application struct {
	id        string
	appType   v1alpha1.AppType
	path      string
	workloads []Workload
	status    *v1alpha1.DeviceApplicationStatus
	embedded  bool
}

// NewApplication creates a new application from an application provider.
func NewApplication(provider Provider) *application {
	spec := provider.Spec()
	return &application{
		id:       spec.ID,
		appType:  spec.AppType,
		path:     spec.Path,
		embedded: spec.Embedded,
		status: &v1alpha1.DeviceApplicationStatus{
			Name:   spec.Name,
			Status: v1alpha1.ApplicationStatusUnknown,
		},
	}
}

func (a *application) ID() string {
	return a.id
}

func (a *application) Name() string {
	return a.status.Name
}

func (a *application) AppType() v1alpha1.AppType {
	return a.appType
}

func (a *application) Workload(name string) (*Workload, bool) {
	for i := range a.workloads {
		if a.workloads[i].Name == name {
			return &a.workloads[i], true
		}
	}
	return nil, false
}

func (a *application) AddWorkload(workload *Workload) {
	a.workloads = append(a.workloads, *workload)
}

func (a *application) RemoveWorkload(name string) bool {
	for i, workload := range a.workloads {
		if workload.Name == name {
			a.workloads = append(a.workloads[:i], a.workloads[i+1:]...)
			return true
		}
	}
	return false
}

func (a *application) Path() string {
	return a.path
}

func (a *application) IsEmbedded() bool {
	return a.embedded
}

func (a *application) Status() (*v1alpha1.DeviceApplicationStatus, v1alpha1.DeviceApplicationsSummaryStatus, error) {
	// TODO: revisit performance of this function
	healthy := 0
	initializing := 0
	restarts := 0
	exited := 0
	for _, workload := range a.workloads {
		restarts += workload.Restarts
		switch workload.Status {
		case StatusInit, StatusCreated:
			initializing++
		case StatusRunning:
			healthy++
		case StatusExited:
			exited++
		}
	}

	total := len(a.workloads)
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

func ParseAppType(s string) (v1alpha1.AppType, error) {
	appType := v1alpha1.AppType(s)
	if appType == "" {
		return "", fmt.Errorf("invalid app type: %s", s)
	}
	return appType, nil
}

// typeFromImage returns the app type from the image label take from the image in local container storage.
func typeFromImage(ctx context.Context, podman *client.Podman, image string) (v1alpha1.AppType, error) {
	labels, err := podman.InspectLabels(ctx, image)
	if err != nil {
		return "", err
	}
	appTypeLabel, ok := labels[AppTypeLabel]
	if !ok {
		return "", fmt.Errorf("%w: %s, %s", errors.ErrAppLabel, AppTypeLabel, image)
	}
	return ParseAppType(appTypeLabel)
}

// ensureDependenciesFromType ensures that the dependencies required for the given app type are available.
func ensureDependenciesFromType(appType v1alpha1.AppType) error {
	var deps []string
	switch appType {
	case v1alpha1.AppTypeCompose:
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

// ensureImageExists ensures that the image exists in the container storage.
func ensureImageExists(ctx context.Context, log *log.PrefixLogger, podman *client.Podman, image string) error {
	// pull the image if it does not exist. it is possible that the image
	// tag such as latest in which case it will be pulled later. but we
	// don't want to require calling out the network on every sync.
	if podman.ImageExists(ctx, image) {
		log.Tracef("Image already exists in container storage: %s", image)
		return nil
	}

	_, err := podman.Pull(ctx, image, client.WithRetry())
	if err != nil {
		log.Warnf("Failed to pull image %q: %v", image, err)
		return err
	}

	return nil
}
