package hook

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	ignv3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

type PreConfigMonitor struct {
	mu   sync.Mutex
	once sync.Once

	events   chan Event[v1alpha1.FileOperation]
	files    map[string]*FileMetadata
	watcher  map[string]struct{}
	handlers map[string]*Handler

	exec executer.Executer
	log  *log.PrefixLogger
}

// newPreConfigMonitor creates a new PreConfigMonitor
func newPreConfigMonitor(log *log.PrefixLogger, exec executer.Executer) *PreConfigMonitor {
	return &PreConfigMonitor{
		events:  make(chan Event[v1alpha1.FileOperation], 10),
		files:   make(map[string]*FileMetadata),
		watcher: make(map[string]struct{}),
		exec:    exec,
		log:     log,
	}
}

func (m *PreConfigMonitor) Run(ctx context.Context) {
	m.initialize()
	defer func() {
		if err := m.Close(); err != nil {
			m.log.Errorf("Error closing watcher: %v", err)
		}
		m.log.Infof("PreConfigMonitor stopped")
	}()

	for {
		select {
		case <-ctx.Done():
			m.log.Infof("PreConfigMonitor context done")
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

func (m *PreConfigMonitor) initialize() {
	m.mu.Lock()
	defer m.mu.Unlock()
	// initialize the handlers map here for testing observability.
	m.handlers = make(map[string]*Handler)
}

func (m *PreConfigMonitor) handle(ctx context.Context, event Event[v1alpha1.FileOperation]) {
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

func (m *PreConfigMonitor) getHandler(eventName string) *Handler {
	m.mu.Lock()
	defer m.mu.Unlock()
	return getHandler(eventName, m.handlers)
}

// Update the manager with the new hook if appropriate.
func (m *PreConfigMonitor) Update(hook *v1alpha1.DeviceHookSpec) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.handlers == nil {
		return false, ErrHookManagerNotInitialized
	}

	handler, ok := m.handlers[hook.Path]
	if !ok || !reflect.DeepEqual(hook, handler.DeviceHookSpec) {
		return true, m.addOrReplaceConfigHookHandler(hook, m.handlers)
	}

	return false, nil
}

func (m *PreConfigMonitor) ListWatches() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	list := make([]string, 0, len(m.files))
	for k := range m.files {
		list = append(list, k)
	}
	return list
}

func (m *PreConfigMonitor) isWatched(name string) bool {
	// check for exact match on file
	_, ok := m.watcher[name]
	if ok {
		return ok
	}
	// fallback to dir
	parentDir := filepath.Dir(name)
	_, ok = m.watcher[parentDir]
	return ok
}

func (m *PreConfigMonitor) CreateOrUpdate(path string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	hash, err := computeFNVHash(data)
	if err != nil {
		return err
	}

	newMetadata := FileMetadata{
		Size: len(data),
		Hash: hash,
	}

	watched := m.isWatched(path)
	existingMetadata, ok := m.files[path]
	if ok {
		// existing file has been updated
		if watched && newMetadata.Size != existingMetadata.Size || newMetadata.Hash != existingMetadata.Hash {
			m.events <- Event[v1alpha1.FileOperation]{
				Name: path,
				Op:   v1alpha1.FileOperationUpdate,
			}
			// update metadata
			existingMetadata.Size = newMetadata.Size
			existingMetadata.Hash = newMetadata.Hash
		}
	} else {
		m.files[path] = &newMetadata
		// new file we are tracking but it may already exist on disk
		if !watched || fileExists(path) {
			return nil
		}
		// new file
		m.events <- Event[v1alpha1.FileOperation]{
			Name: path,
			Op:   v1alpha1.FileOperationCreate,
		}
	}

	return nil
}

func (m *PreConfigMonitor) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.files[name]
	if ok {
		// if the file does not exist on disk, we can remove it from the list
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return os.ErrNotExist
		}

		// remove is a special case where the action may need to do work with the file before it is removed.
		ctx, cancel := context.WithTimeout(context.Background(), defaultFileEventTimeout)
		m.events <- Event[v1alpha1.FileOperation]{
			Name:     name,
			Op:       v1alpha1.FileOperationRemove,
			cancelFn: cancel,
		}

		// wait for the action to complete or timeout before removing the file from the list
		<-ctx.Done()

		delete(m.files, name)
	}

	return nil
}

func (m *PreConfigMonitor) ComputeRemoval(files []ignv3types.File) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	newIgnFiles := make(map[string]struct{})
	for _, file := range files {
		newIgnFiles[file.Path] = struct{}{}
	}

	var deleteFiles []string
	for path := range m.files {
		if _, exists := newIgnFiles[path]; !exists {
			deleteFiles = append(deleteFiles, path)
		}
	}

	return deleteFiles
}

func (m *PreConfigMonitor) Errors() []error {
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

func (m *PreConfigMonitor) RemoveWatch(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.handlers == nil {
		return ErrHookManagerNotInitialized
	}

	if _, ok := m.files[name]; !ok {
		return nil
	}
	delete(m.files, name)
	return nil
}

func (m *PreConfigMonitor) AddWatch(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.handlers == nil {
		return ErrHookManagerNotInitialized
	}

	m.watcher[name] = struct{}{}
	return nil
}

func (m *PreConfigMonitor) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.once.Do(func() {
		defer func() {
			close(m.events)
		}()
	})
	return nil
}

func (m *PreConfigMonitor) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.handlers = make(map[string]*Handler)
	return nil
}

// addOrReplaceConfigHookHandler adds or replaces a config hook handler. this function assumes a lock is held.
func (m *PreConfigMonitor) addOrReplaceConfigHookHandler(newHook *v1alpha1.DeviceHookSpec, existingHandlers map[string]*Handler) error {
	if err := updateHandlersFromConfigHook(newHook, existingHandlers); err != nil {
		return fmt.Errorf("failed updating handlers: %w", err)
	}

	if err := m.AddWatch(newHook.Path); err != nil {
		return fmt.Errorf("failed adding watch: %w", err)
	}

	return nil
}

// ref. https://pkg.go.dev/hash/fnv, https://en.wikipedia.org/wiki/Fowler%E2%80%93Noll%E2%80%93Vo_hash_function
func computeFNVHash(data []byte) (uint64, error) {
	h := fnv.New64a()
	_, err := h.Write(data)
	if err != nil {
		return 0, fmt.Errorf("failed to compute FNV hash: %w", err)
	}
	return h.Sum64(), nil
}
