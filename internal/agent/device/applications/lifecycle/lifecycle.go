package lifecycle

import (
	"context"
	"fmt"
	"path/filepath"
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
	// Name of the application
	Name string
	// Environment variables to be passed to the manifest handler at runtime
	EnvVars map[string]string
	// Manifest handler to be used
	Handler ActionHandlerType
	// Manifest action to be executed
	Type ActionType
	// Embedded is true if the application is embedded in the device
	Embedded bool
}

func (a *Action) ApplicationPath() (string, error) {
	var typePath string
	switch a.Handler {
	case ActionHandlerCompose:
		if a.Embedded {
			typePath = EmbeddedComposeAppPath
			break
		}
		typePath = ComposeAppPath
	default:
		return "", fmt.Errorf("unsupported handler type: %s", a.Handler)
	}

	return filepath.Join(typePath, a.Name), nil
}
