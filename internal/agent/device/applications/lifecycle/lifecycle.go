package lifecycle

import (
	"context"
	"iter"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
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
	Execute(ctx context.Context, actions Actions) error
}

// ActionSpec is a marker interface for type-specific action configuration.
// Only spec types defined in this package can implement this interface.
type ActionSpec interface {
	actionSpec()
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
	// User that owns the app. Blank means the same user as the current process.
	User v1beta1.Username
	// Embedded is true if the application is embedded in the device
	Embedded bool
	// Volumes is a list of volume names related to this application
	Volumes []Volume
	// Spec holds type-specific configuration, discriminated by AppType.
	Spec ActionSpec
}

// HelmSpec contains Helm-specific action configuration.
type HelmSpec struct {
	// Namespace is the Kubernetes namespace for the Helm release.
	Namespace string
	// ValuesFiles is a list of relative paths to values files within the chart.
	ValuesFiles []string
	// ProviderValuesPath is the absolute path to the provider-generated values file.
	// This is set when the application spec contains inline Values.
	ProviderValuesPath string
}

func (HelmSpec) actionSpec() {}

type Actions []Action

type ActionsByType struct {
	Adds    []Action
	Removes []Action
	Updates []Action
	Unknown []Action
}

func (as Actions) ByUser() iter.Seq2[v1beta1.Username, ActionsByType] {
	return func(yield func(v1beta1.Username, ActionsByType) bool) {
		byUser := make(map[v1beta1.Username]ActionsByType)
		for _, a := range as {
			byType := byUser[a.User]
			switch a.Type {
			case ActionAdd:
				byType.Adds = append(byType.Adds, a)
			case ActionRemove:
				byType.Removes = append(byType.Removes, a)
			case ActionUpdate:
				byType.Updates = append(byType.Updates, a)
			default:
				byType.Unknown = append(byType.Unknown, a)
			}
			byUser[a.User] = byType
		}
		for u, abt := range byUser {
			if !yield(u, abt) {
				return
			}
		}
	}
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
