package app

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

var (
	ErrNotFound = errors.New("not found")
)

type RuntimeType string

const (
	RuntimeTypePodman  RuntimeType = "podman"
	RuntimeTypeCrio    RuntimeType = "crio"
)

func (r RuntimeType) String() string {
	return string(r)
}

// Engine is an interface for managing applications.
type Engine interface {
	// List returns a list of engine managed applications based on the match patterns.
	List(ctx context.Context, matchPatterns ...string) ([]v1alpha1.ApplicationStatus, error)
	// GetStatus returns the status of the engine managed application based on the Id/Name.
	GetStatus(ctx context.Context, id string) (*v1alpha1.ApplicationStatus, error)
}

type Runtime interface {
	Engine
	// PullImage pulls an image from the registry.
	PullImage(ctx context.Context, name string) error
	// ImageExists checks if an image exists in the local container storage.
	ImageExists(ctx context.Context, name string) (bool, error)
	// The underlying runtime type.
	Type() RuntimeType
}

// IsState returns true if the status state is the given state.
func IsState(status *v1alpha1.ApplicationStatus, state v1alpha1.ApplicationState) bool {
	if status == nil || status.State == nil {
		return false
	}
	return *status.State == state
}

// App represents an application monitored by thw app manager.
type App struct {
	id                string
	runtime           Runtime
	currentStatus     *v1alpha1.ApplicationStatus
	created           time.Time
	lastUpdated       time.Time
	deletionTimestamp time.Time
	cancelFn          context.CancelFunc

	notifyCh        chan<- struct{}
	hasStatusUpdate bool
}

// NewApp creates a new application.
func NewApp(id string, runtime Runtime, cancelFn context.CancelFunc, notifyCh chan struct{}) *App {
	return &App{
		id:       id,
		runtime:  runtime,
		created:  time.Now(),
		notifyCh: notifyCh,
		cancelFn: cancelFn,
	}
}

// UpdateStatus updates the application status.
func (a *App) UpdateStatus(ctx context.Context) error {
	newStatus, err := a.runtime.GetStatus(ctx, a.id)
	if err != nil {
		return err
	}

	change := false
	if a.currentStatus.Name == nil && a.currentStatus.Name != newStatus.Name {
		a.currentStatus.Name = newStatus.Name
		change = true
	}

	if a.currentStatus.State != newStatus.State {
		a.currentStatus.State = newStatus.State
		change = true
	}

	if a.currentStatus.Restarts != newStatus.Restarts {
		a.currentStatus.Restarts = newStatus.Restarts
		change = true
	}

	if change {
		a.lastUpdated = time.Now()
		a.hasStatusUpdate = true
		a.notify(ctx)
	}

	return nil
}

func (a *App) Runtime() RuntimeType {
	return a.runtime.Type()
}

func (a *App) SyncStatus() (*v1alpha1.ApplicationStatus, bool) {
	if !a.hasStatusUpdate {
		return nil, false
	}

	a.hasStatusUpdate = false
	return a.currentStatus, true
}

func (a *App) Delete(ctx context.Context) error {
	a.deletionTimestamp = time.Now()
	a.cancelFn()
	return nil
}

func (a *App) notify(ctx context.Context) {
	// notify the app manager of update
	select {
	case <-ctx.Done():
		return
	case a.notifyCh <- struct{}{}:
	default:
		// don't block if channel is full
	}
}
