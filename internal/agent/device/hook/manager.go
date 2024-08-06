package hook

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

var _ Manager = (*manager)(nil)

type Manager interface {
	Run(ctx context.Context)
	Sync(current, desired *v1alpha1.RenderedDeviceSpec) error
	OnBeforeCreate(ctx context.Context, path string)
	OnAfterCreate(ctx context.Context, path string)
	OnBeforeUpdate(ctx context.Context, path string)
	OnAfterUpdate(ctx context.Context, path string)
	OnBeforeRemove(ctx context.Context, path string)
	OnAfterRemove(ctx context.Context, path string)
	Errors() []error
	Close() error
}

type ActionHook interface {
	OnChange(ctx context.Context, path string) error
}

type ActionMap map[string][]ActionHook

type manager struct {
	onBeforeCreate ActionMap
	onAfterCreate  ActionMap
	onBeforeUpdate ActionMap
	onAfterUpdate  ActionMap
	onBeforeRemove ActionMap
	onAfterRemove  ActionMap
	log            *log.PrefixLogger
	mu             sync.Mutex
	errors         map[string]error
	backgroundJobs chan func(ctx context.Context)
	exec           executer.Executer
	initialized    atomic.Bool
}

func NewManager(exec executer.Executer, log *log.PrefixLogger) Manager {
	return &manager{
		onBeforeCreate: make(ActionMap),
		onAfterCreate:  make(ActionMap),
		onBeforeUpdate: make(ActionMap),
		onAfterUpdate:  make(ActionMap),
		onBeforeRemove: make(ActionMap),
		onAfterRemove:  make(ActionMap),
		log:            log,
		errors:         make(map[string]error),
		backgroundJobs: make(chan func(ctx context.Context), 100),
		exec:           exec,
	}
}

func (m *manager) createExecutableActionHook(action v1alpha1.HookAction) (ActionHook, []v1alpha1.FileOperation, error) {
	executableSpec, err := action.AsHookActionExecutableSpec()
	if err != nil {
		return nil, nil, err
	}
	actionTimeout, err := parseTimeout(executableSpec.Timeout)
	if err != nil {
		return nil, nil, err
	}
	envVars := lo.FromPtr(executableSpec.Executable.EnvVars)
	if err = validateEnvVars(envVars); err != nil {
		return nil, nil, err
	}
	return newExecutableActionHook(executableSpec.Executable.Run,
		lo.FromPtr(executableSpec.Executable.EnvVars),
		m.exec,
		actionTimeout,
		executableSpec.Executable.WorkDir,
		m.log), lo.FromPtr(executableSpec.On), nil
}

func (m *manager) createSystemdActionHook(action v1alpha1.HookAction) (ActionHook, []v1alpha1.FileOperation, error) {
	systemdSpec, err := action.AsHookActionSystemdSpec()
	if err != nil {
		return nil, nil, err
	}
	actionTimeout, err := parseTimeout(systemdSpec.Timeout)
	if err != nil {
		return nil, nil, err
	}
	return newSystemdActionHook(systemdSpec.Unit.Name,
		systemdSpec.Unit.Operations,
		m.exec,
		actionTimeout,
		m.log), lo.FromPtr(systemdSpec.On), nil
}

func (m *manager) generateOperationMaps(hookSpecs []v1alpha1.DeviceHookSpec) (ActionMap, ActionMap, ActionMap, error) {
	createMap := make(ActionMap)
	updateMap := make(ActionMap)
	removeMap := make(ActionMap)
	for _, hookSpec := range hookSpecs {
		for _, action := range hookSpec.Actions {
			hookActionType, err := getHookActionType(action)
			if err != nil {
				return nil, nil, nil, err
			}
			var actionHook ActionHook
			var ops []v1alpha1.FileOperation
			switch hookActionType {
			case ExecutableActionType:
				actionHook, ops, err = m.createExecutableActionHook(action)
			case SystemdActionType:
				actionHook, ops, err = m.createSystemdActionHook(action)
			default:
				return nil, nil, nil, ErrActionTypeNotFound
			}
			if err != nil {
				return nil, nil, nil, err
			}
			path := lo.FromPtr(hookSpec.Path)
			for _, op := range ops {
				switch op {
				case v1alpha1.FileOperationCreate:
					createMap[path] = append(createMap[path], actionHook)
				case v1alpha1.FileOperationUpdate:
					updateMap[path] = append(updateMap[path], actionHook)
				case v1alpha1.FileOperationRemove:
					removeMap[path] = append(removeMap[path], actionHook)
				default:
					return nil, nil, nil, ErrUnsupportedFilesystemOperation
				}
			}
		}
	}
	return createMap, updateMap, removeMap, nil
}

func (m *manager) Sync(currentPtr, desiredPtr *v1alpha1.RenderedDeviceSpec) error {
	m.log.Info("Syncing hook manager")
	defer m.log.Info("Finished syncing hook manager")

	current := lo.FromPtr(currentPtr)
	desired := lo.FromPtr(desiredPtr)
	if m.initialized.Load() && reflect.DeepEqual(current.Hooks, desired.Hooks) {
		m.log.Info("Hooks are equal.  Nothing to update")
		return nil
	}
	desiredHooks := lo.FromPtr(desired.Hooks)
	beforeCreateMap, beforeUpdateMap, beforeRemoveMap, err := m.generateOperationMaps(append(lo.FromPtr(desiredHooks.BeforeUpdating), defaultBeforeUpdateHooks()...))
	if err != nil {
		return err
	}
	afterCreateMap, afterUpdateMap, afterRemoveMap, err := m.generateOperationMaps(append(lo.FromPtr(desiredHooks.AfterUpdating), defaultAfterUpdateHooks()...))
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onBeforeCreate = beforeCreateMap
	m.onBeforeUpdate = beforeUpdateMap
	m.onBeforeRemove = beforeRemoveMap
	m.onAfterCreate = afterCreateMap
	m.onAfterUpdate = afterUpdateMap
	m.onAfterRemove = afterRemoveMap
	m.initialized.Store(true)
	return nil
}

func (m *manager) setError(path string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors[path] = err
}

func (m *manager) getActionsForPath(path string, actions ActionMap) []ActionHook {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append(actions[path], actions[filepath.Dir(path)]...)
}

func (m *manager) submitBackgroundJob(path string, actions ActionMap) {
	actionList := m.getActionsForPath(path, actions)
	if len(actionList) == 0 {
		return
	}
	m.backgroundJobs <- func(ctx context.Context) {
		m.runActionList(ctx, path, actionList)
	}
}

func (m *manager) Run(ctx context.Context) {
	for {
		select {
		case job, ok := <-m.backgroundJobs:
			if !ok {
				m.log.Warn("background jobs channel closed")
				return
			}
			job(ctx)
		case <-ctx.Done():
			m.log.Info("background jobs context closed")
			return
		}
	}
}

func (m *manager) runActionList(ctx context.Context, path string, actionHooks []ActionHook) {
	for _, actionHook := range actionHooks {
		if err := actionHook.OnChange(ctx, path); err != nil {
			m.log.Errorf("error while running hook for path %s: %+v", path, err)
			m.setError(path, err)
		}
	}
}

func (m *manager) runActions(ctx context.Context, path string, actions ActionMap) {
	m.runActionList(ctx, path, m.getActionsForPath(path, actions))
}

func (m *manager) OnBeforeCreate(ctx context.Context, path string) {
	m.runActions(ctx, path, m.onBeforeCreate)
}

func (m *manager) OnAfterCreate(ctx context.Context, path string) {
	m.submitBackgroundJob(path, m.onAfterCreate)
}

func (m *manager) OnBeforeUpdate(ctx context.Context, path string) {
	m.runActions(ctx, path, m.onBeforeUpdate)
}

func (m *manager) OnAfterUpdate(ctx context.Context, path string) {
	m.submitBackgroundJob(path, m.onAfterUpdate)
}

func (m *manager) OnBeforeRemove(ctx context.Context, path string) {
	m.runActions(ctx, path, m.onBeforeRemove)
}

func (m *manager) OnAfterRemove(ctx context.Context, path string) {
	m.submitBackgroundJob(path, m.onAfterRemove)
}

func (m *manager) Errors() []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return lo.Values(m.errors)
}

func (m *manager) Close() error {
	close(m.backgroundJobs)
	return nil
}
