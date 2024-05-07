package action

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"

	"github.com/flightctl/flightctl/pkg/log"
)

// TODO: Add blacklist and whitelist for file paths

const (
	systemdCommand = "/usr/bin/systemctl"
)

type AppName string

const (
	AppSystemd        AppName = "systemd"
	AppNetworkMAnager AppName = "NetworkManager"
	AppIPTables       AppName = "iptables"
	AppPodman         AppName = "podman"
	AppCrio           AppName = "crio"
	AppSysctl         AppName = "sysctl"
)

type Action string

const (
	ActionReload Action = "reload"
	ActionStart  Action = "start"
	ActionStop   Action = "stop"
)

type Config struct {
	// Target is an optional target that the action can be applied to.
	Target string
	// WatchPath is the path to watch for changes.
	WatchPath string
	// Application this config is for
	App AppName
	// Events describe the actions to taken when an event is observed.
	Events []Event
}

type Event struct {
	// Op is the observed event type which will fire the defined actions.
	Op Op
	// Actions is a list of actions to take against the app when the event is
	// observed. These actions are run in series.
	Actions []Action
}

type Op uint32

const (
	Create Op = 1 << iota
	Write
	Remove
	Rename
	Chmod
)

func (op Op) String() string {
	var b strings.Builder
	if op.Has(Create) {
		b.WriteString("|CREATE")
	}
	if op.Has(Remove) {
		b.WriteString("|REMOVE")
	}
	if op.Has(Write) {
		b.WriteString("|WRITE")
	}
	if op.Has(Rename) {
		b.WriteString("|RENAME")
	}
	if op.Has(Chmod) {
		b.WriteString("|CHMOD")
	}
	if b.Len() == 0 {
		return "[no events]"
	}
	return b.String()[1:]
}

func (o Op) Has(h Op) bool { return o&h == h }

type AppActions interface {
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Reload(context.Context, string) error
	Restart(context.Context, string) error
}

func NewHandler(appName AppName, eventFn func(fsnotify.Event) error) Handler {
	return Handler{
		EventFn: eventFn,
		AppName: appName,
	}
}

type Handler struct {
	EventFn func(fsnotify.Event) error
	AppName AppName
}

// NewManager creates a new manager with a fsnotify watcher.
func NewManager() (*Manager, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &Manager{
		watcher:  watcher,
		log:      log.InitLogs(),
		handlers: make(map[string][]Handler),
		app:      make(map[AppName]AppActions),
	}, nil
}

type Manager struct {
	watcher  *fsnotify.Watcher
	log      *logrus.Logger
	handlers map[string][]Handler
	app      map[AppName]AppActions
}

// RegisterHandler registers a handler for a given watch path.
func (a *Manager) RegisterHandler(watchPath string, handler Handler) error {
	if handler.EventFn == nil {
		return fmt.Errorf("handler event function is not defined")
	}
	a.handlers[watchPath] = append(a.handlers[watchPath], handler)
	return nil
}

// RegisterApp registers an app with the manager.
func (a *Manager) RegisterApp(name AppName, actions AppActions) {
	a.app[name] = actions
}

// Handle processes the event and runs the handler function.
func (a *Manager) Handle(event fsnotify.Event) error {
	watchPath := filepath.Dir(event.Name)
	handlers, exists := a.handlers[watchPath]
	if !exists {
		return nil
	}

	for _, handle := range handlers {
		if err := handle.EventFn(event); err != nil {
			return err
		}
	}

	return nil
}

// Run starts the manager and listens for events.
func (a *Manager) Run(ctx context.Context) {
	defer a.watcher.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-a.watcher.Events:
			if !ok {
				return
			}
			// TODO add support for batching.
			err := a.Handle(event)
			if err != nil {
				a.log.Errorf("error: %v", err)
			}
		case err, ok := <-a.watcher.Errors:
			if !ok {
				return
			}
			a.log.Errorf("error: %v", err)
		}
	}
}

// Reboot reboots the system.
func (a *Manager) Reboot(ctx context.Context, rationale string) error {
	rebootCmd := rebootCommand(rationale)
	if err := rebootCmd.Run(); err != nil {
		return fmt.Errorf("reboot command failed: %w", err)
	}
	return nil
}

// CreateHandlerFromConfig creates a handler from a custom config.
func (a *Manager) CreateHandlerFromConfig(config Config) Handler {
	ctx := context.Background()
	handlerFn := func(watchEvent fsnotify.Event) error {
		app, found := a.app[config.App]
		if !found {
			return fmt.Errorf("app not registered: %s", config.App)
		}

		for _, event := range config.Events {
			if watchEvent.Op.String() == event.Op.String() {
				for _, action := range event.Actions {
					switch action {
					case ActionStart:
						err := app.Start(ctx, config.Target)
						if err != nil {
							return fmt.Errorf("failed to start %s: %w", config.App, err)
						}
					case ActionStop:
						err := app.Stop(ctx, config.Target)
						if err != nil {
							return fmt.Errorf("failed to stop %s: %w", config.App, err)
						}
					case ActionReload:
						err := app.Reload(ctx, config.Target)
						if err != nil {
							return fmt.Errorf("failed to reload %s: %w", config.App, err)
						}
					default:
						return fmt.Errorf("unsupported action for %s", config.App)
					}
				}
				return nil
			}
		}
		return nil
	}

	return NewHandler(config.App, handlerFn)
}

// rebootCommand creates a new transient systemd unit to reboot the system.
// With the upstream implementation of kubelet graceful shutdown feature,
// we don't explicitly stop the kubelet so that kubelet can gracefully shutdown
// pods when `GracefulNodeShutdown` feature gate is enabled.
// kubelet uses systemd inhibitor locks to delay node shutdown to terminate pods.
// https://kubernetes.io/docs/concepts/architecture/nodes/#graceful-node-shutdown
func rebootCommand(rationale string) *exec.Cmd {
	return exec.Command("systemd-run", "--unit", "machine-config-daemon-reboot",
		"--description", fmt.Sprintf("machine-config-daemon: %s", rationale), "/bin/sh", "-c", "systemctl reboot")
}
