package app

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

var (
	ErrNotFound = errors.New("not found")
)

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
}

// IsState returns true if the status state is the given state.
func IsState(status *v1alpha1.ApplicationStatus, state v1alpha1.ApplicationState) bool {
	if status == nil || status.State == nil {
		return false
	}
	return *status.State == state
}

// Manager is responsible for managing and collecting the status of applications on a device.
type Manager struct {
	// TODO don't export
	Apps map[string]v1alpha1.ApplicationStatus
}

// NewManager creates a new AppManager.
func NewManager() *Manager {
	return &Manager{
		Apps: make(map[string]v1alpha1.ApplicationStatus),
	}
}

func (a *Manager) ExportStatus(name string, status v1alpha1.ApplicationStatus) {
	a.Apps[name] = status
}
