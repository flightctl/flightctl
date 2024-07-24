package hook

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	fsnotify "github.com/fsnotify/fsnotify"
)

var _ Monitor = (*PostConfigMonitor)(nil)
var _ Monitor = (*PreConfigMonitor)(nil)

const (
	defaultFileEventTimeout = 30 * time.Second
)

// PostConfigMonitor is a file monitor that watches for file system events
// using fsnotify. This provides a way to monitor file level changes to configs
// after they happen on disk.
type PostConfigMonitor struct {
	mu       sync.Mutex
	watcher  *fsnotify.Watcher
	events   chan Event[v1alpha1.FileOperation]
	handlers map[string]*Handler

	exec executer.Executer
	log  *log.PrefixLogger
}

// newPostConfigMonitor creates a new PostConfigMonitor
func newPostConfigMonitor(log *log.PrefixLogger, exec executer.Executer) (*PostConfigMonitor, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	m := &PostConfigMonitor{
		watcher: watcher,
		events:  make(chan Event[v1alpha1.FileOperation], 10),
		exec:    exec,
		log:     log,
	}
	m.forwardEvents()
	return m, nil
}

// Run starts the file monitor and processes events
func (m *PostConfigMonitor) Run(ctx context.Context) {
	m.initialize()
	defer func() {
		if err := m.Close(); err != nil {
			m.log.Errorf("Error closing watcher: %v", err)
		}
		m.log.Infof("PostConfigMonitor stopped")
	}()
	for {
		select {
		case <-ctx.Done():
			m.log.Infof("PostConfigMonitor context done")
			return
		case event, ok := <-m.events:
			if !ok {
				m.log.Debug("Watcher events channel closed")
				return
			}
			m.handle(ctx, event)
		}
	}
}

func (m *PostConfigMonitor) handle(ctx context.Context, event Event[v1alpha1.FileOperation]) {
	filePath := event.Name
	handle := m.getHandler(filePath)
	if handle == nil {
		// no handler for this event
		return
	}
	actions, ok := handle.Actions(event.Op)
	if !ok {
		// handle does not have any actions for this file operation
		return
	}

	if err := handleActions[v1alpha1.FileOperation](ctx, m.log, m.exec, event, actions); err != nil {
		handle.SetError(err)
	}
}

func (m *PostConfigMonitor) getHandler(eventName string) *Handler {
	m.mu.Lock()
	defer m.mu.Unlock()
	return getHandler(eventName, m.handlers)
}

func (m *PostConfigMonitor) Update(hook *v1alpha1.DeviceHookSpec) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.handlers == nil {
		return false, ErrHookManagerNotInitialized
	}

	handler, ok := m.handlers[hook.Path]
	if !ok || !reflect.DeepEqual(hook, handler.DeviceHookSpec) {
		return true, m.addOrReplaceConfigHandler(hook, m.handlers)
	}

	return false, nil
}

// forwardEvents watches the fsnotify events and passes them to the events channel
func (m *PostConfigMonitor) forwardEvents() {
	go func() {
		for {
			select {
			case event, ok := <-m.watcher.Events:
				if !ok {
					return
				}
				fileEvent, err := fsnotifyOpToFileOperation(event.Op)
				if err != nil {
					if !errors.Is(err, ErrUnsupportedFilesystemOperation) {
						m.log.Errorf("fsnotify error: %v", err)
					}

					continue
				}
				m.events <- Event[v1alpha1.FileOperation]{
					Name: event.Name,
					Op:   fileEvent,
				}
			case err, ok := <-m.watcher.Errors:
				if !ok {
					return
				}
				m.log.Errorf("fsnotify error: %v", err)
			}
		}
	}()
}

func (m *PostConfigMonitor) AddWatch(name string) error {
	// fsnotify watcher will error if the path is already being watched
	for _, existingWatchPath := range m.ListWatches() {
		if existingWatchPath == name {
			return nil
		}
	}

	return m.watcher.Add(name)
}

func (w *PostConfigMonitor) RemoveWatch(name string) error {
	return w.watcher.Remove(name)
}

func (w *PostConfigMonitor) ListWatches() []string {
	return w.watcher.WatchList()
}

func (m *PostConfigMonitor) Errors() []error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for _, handler := range m.handlers {
		if err := handler.Error(); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}
func (m *PostConfigMonitor) Close() error {
	return m.watcher.Close()
}

func (m *PostConfigMonitor) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, watchPath := range m.watcher.WatchList() {
		m.log.Infof("Removing watch: %s", watchPath)
		if err := m.watcher.Remove(watchPath); err != nil {
			return err
		}
	}

	m.handlers = make(map[string]*Handler)
	return nil
}

func (m *PostConfigMonitor) initialize() {
	m.mu.Lock()
	defer m.mu.Unlock()
	// initialize the handlers map here for testing observability.
	m.handlers = make(map[string]*Handler)
}

// addOrReplaceConfigHookHandler adds or replaces a config hook handler. this function assumes a lock is held.
func (m *PostConfigMonitor) addOrReplaceConfigHandler(newHook *v1alpha1.DeviceHookSpec, existingHandlers map[string]*Handler) error {
	if err := updateHandlersFromConfigHook(newHook, existingHandlers); err != nil {
		return fmt.Errorf("failed updating handlers: %w", err)
	}

	if err := m.AddWatch(newHook.Path); err != nil {
		return fmt.Errorf("failed adding watch: %w", err)
	}

	return nil
}
