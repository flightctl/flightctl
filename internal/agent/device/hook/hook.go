package hook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	ignv3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/fsnotify/fsnotify"
)

const (
	SystemdActionType    = "Systemd"
	ExecutableActionType = "Executable"

	// FilePathKey is a placeholder which will be replaced with the file path
	FilePathKey = "FilePath"
	noValueKey  = "<no value>"
)

var (
	ErrHookManagerNotInitialized      = errors.New("hook manager not initialized")
	ErrInvalidTokenFormat             = errors.New("invalid token: formatting")
	ErrTokenNotSupported              = errors.New("invalid token: not supported")
	ErrFailedToParseJSONToken         = errors.New("failed to parse JSON token")
	ErrActionTypeNotFound             = errors.New("failed to find action type")
	ErrUnsupportedFilesystemOperation = errors.New("unsupported filesystem operation")
	ErrNotFound                       = errors.New("not found")
)

type Manager interface {
	Run(ctx context.Context)
	Config() *ConfigHook
	// OS() Hook TODO: implement the OS hooks
}

type Hook interface {
	Pre() Monitor
	Post() Monitor
}

type Monitor interface {
	Run(ctx context.Context)
	Update(hook *v1alpha1.DeviceHookSpec) (bool, error)
	AddWatch(name string) error
	RemoveWatch(name string) error
	ListWatches() []string
	Reset() error
	Errors() []error
	Close() error
}

type ObserveMonitor interface {
	Monitor
	CreateOrUpdate(path string, contents []byte) error
	Remove(path string) error
	ComputeRemoval(files []ignv3types.File) []string
}

func newConfigHook(log *log.PrefixLogger, exec executer.Executer) (*ConfigHook, error) {
	postConfigMonitor, err := newPostConfigMonitor(log, exec)
	if err != nil {
		return nil, err
	}

	return &ConfigHook{
		preMonitor:  newPreConfigMonitor(log, exec),
		postMonitor: postConfigMonitor,
	}, nil
}

type ConfigHook struct {
	preMonitor  ObserveMonitor
	postMonitor Monitor
}

func (c *ConfigHook) Pre() ObserveMonitor {
	return c.preMonitor
}

func (c *ConfigHook) Post() Monitor {
	return c.postMonitor
}

type Handler struct {
	mu sync.Mutex
	*v1alpha1.DeviceHookSpec
	opActions map[v1alpha1.FileOperation][]v1alpha1.HookAction
	err       error
}

func (h *Handler) Actions(op v1alpha1.FileOperation) ([]v1alpha1.HookAction, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	actions, ok := h.opActions[op]
	return actions, ok
}

func (h *Handler) Error() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

func (h *Handler) SetError(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = err
}

func fsnotifyOpToFileOperation(event fsnotify.Event) (v1alpha1.FileOperation, error) {
	switch {
	case event.Has(fsnotify.Create):
		return v1alpha1.FileOperationCreate, nil
	case event.Has(fsnotify.Write):
		return v1alpha1.FileOperationUpdate, nil
	case event.Has(fsnotify.Remove):
		return v1alpha1.FileOperationRemove, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedFilesystemOperation, op)
	}
}

type Event[T any] struct {
	Name     string
	Op       T
	cancelFn context.CancelFunc
}

func (f *Event[T]) Done() {
	if f.cancelFn != nil {
		f.cancelFn()
	}
}

type FileMetadata struct {
	Size int
	Hash uint64
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return err == nil
}

func getHandler(eventName string, handlers map[string]*Handler) *Handler {

	// check for exact match on file
	if handler, exists := handlers[eventName]; exists {
		return handler
	}
	// fallback to dir
	parentDir := filepath.Dir(eventName)
	if handler, exists := handlers[parentDir]; exists {
		return handler
	}

	return nil
}

func updateHandlersFromConfigHook(hook *v1alpha1.DeviceHookSpec, handlers map[string]*Handler) error {
	opActions := make(map[v1alpha1.FileOperation][]v1alpha1.HookAction)
	for _, action := range hook.Actions {
		// TODO: can we avoid this?
		hookActionType, err := getHookActionType(action)
		if err != nil {
			return err
		}
		switch hookActionType {
		case SystemdActionType:
			configHook, err := action.AsHookActionSystemdSpec()
			if err != nil {
				return err
			}
			for _, op := range configHook.On {
				fileOP, err := op.AsFileOperation()
				if err != nil {
					return err
				}

				opActions[fileOP] = append(opActions[fileOP], action)
			}
		case ExecutableActionType:
			configHook, err := action.AsHookActionExecutableSpec()
			if err != nil {
				return err
			}
			if configHook.Executable.EnvVars != nil {
				if err := validateEnvVars(*configHook.Executable.EnvVars); err != nil {
					return err
				}
			}
			for _, op := range configHook.On {
				fileOP, err := op.AsFileOperation()
				if err != nil {
					return err
				}
				opActions[fileOP] = append(opActions[fileOP], action)
			}
		default:
			return fmt.Errorf("unknown hook action type: %s", hookActionType)
		}
	}

	watchPath := hook.Path
	// TODO: this is a fair amount of work to do on every update, we should consider optimizing this.
	handlers[watchPath] = &Handler{
		DeviceHookSpec: hook,
		opActions:      opActions,
	}

	return nil
}
