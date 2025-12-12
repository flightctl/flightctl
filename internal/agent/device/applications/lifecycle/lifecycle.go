package lifecycle

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
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
	Execute(ctx context.Context, actions ...*Action) error
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
	AppType v1beta1.AppType
	// Path to the application
	Path string
	// Embedded is true if the application is embedded in the device
	Embedded bool
	// Volumes is a list of volume names related to this application
	Volumes []Volume
}

type Volume struct {
	ID            string
	Reference     string
	ReclaimPolicy v1beta1.ApplicationVolumeReclaimPolicy
}

type contextKey string

const batchStartTimeKey contextKey = "batchStartTime"

func ContextWithBatchStartTime(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, batchStartTimeKey, t)
}

func BatchStartTimeFromContext(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(batchStartTimeKey).(time.Time)
	return t, ok
}
