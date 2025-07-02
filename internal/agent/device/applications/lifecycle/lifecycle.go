package lifecycle

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

type ActionType string

const (
	ActionAdd    ActionType = "add"
	ActionRemove ActionType = "remove"
	ActionUpdate ActionType = "update"
)

type ActionHandlerType string

const (
	ActionHandlerCompose ActionHandlerType = "compose"
)

type ActionHandler interface {
	Execute(ctx context.Context, action *Action) error
}

type Action struct {
	// ID of the application
	ID string
	// Name of the application
	Name string
	// Environment variables to be passed to the manifest handler at runtime
	EnvVars map[string]string
	// Type of the action
	Type ActionType
	// AppType of the application
	AppType v1alpha1.AppType
	// Path to the application
	Path string
	// Embedded is true if the application is embedded in the device
	Embedded bool
	// Volumes is a list of volume names related to this application
	Volumes []Volume
}

type Volume struct {
	ID        string
	Reference string
}
