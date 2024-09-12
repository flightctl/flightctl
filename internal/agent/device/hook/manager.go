package hook

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
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
	OnBeforeReboot(ctx context.Context, path string)
	OnAfterReboot(ctx context.Context, path string)
	Errors() []error
	Close() error
}

type ActionHookFactory interface {
	Create(exec executer.Executer, log *log.PrefixLogger) (ActionHook, error)
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
	onBeforeReboot ActionMap
	onAfterReboot  ActionMap
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
		onBeforeReboot: make(ActionMap),
		onAfterReboot:  make(ActionMap),
		log:            log,
		errors:         make(map[string]error),
		backgroundJobs: make(chan func(ctx context.Context), 100),
		exec:           exec,
	}
}

func (m *manager) createApiHookDefinition(hookSpec v1alpha1.DeviceUpdateHookSpec) (HookDefinition, error) {
	var actionHooks []ActionHookFactory
	for _, action := range hookSpec.Actions {
		actionHooks = append(actionHooks, newApiHookActionFactory(action))
	}
	return HookDefinition{
		name:        util.FromPtr(hookSpec.Name),
		description: util.FromPtr(hookSpec.Description),
		actionHooks: actionHooks,
		ops:         util.FromPtr(hookSpec.OnFile),
		path:        util.FromPtr(hookSpec.Path),
	}, nil
}

func (m *manager) generateOperationMaps(hookSpecs []v1alpha1.DeviceUpdateHookSpec, additionalHooks ...HookDefinition) (ActionMap, ActionMap, ActionMap, ActionMap, error) {
	createMap := make(ActionMap)
	updateMap := make(ActionMap)
	removeMap := make(ActionMap)
	rebootMap := make(ActionMap)
	var hookDefinitions []HookDefinition
	for _, hookSpec := range hookSpecs {
		h, err := m.createApiHookDefinition(hookSpec)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		hookDefinitions = append(hookDefinitions, h)
	}
	for _, h := range append(hookDefinitions, additionalHooks...) {
		path := h.Path()
		opts := h.Ops()
		for _, actionHookFactory := range h.ActionFactories() {
			actionHook, err := actionHookFactory.Create(m.exec, m.log)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			for _, op := range opts {
				switch op {
				case v1alpha1.FileOperationCreate:
					createMap[path] = append(createMap[path], actionHook)
				case v1alpha1.FileOperationUpdate:
					updateMap[path] = append(updateMap[path], actionHook)
				case v1alpha1.FileOperationRemove:
					removeMap[path] = append(removeMap[path], actionHook)
				case v1alpha1.FileOperationReboot:
					rebootMap[path] = append(rebootMap[path], actionHook)
				default:
					return nil, nil, nil, nil, ErrUnsupportedFilesystemOperation
				}
			}
		}
	}
	return createMap, updateMap, removeMap, rebootMap, nil
}

func (m *manager) Sync(currentPtr, desiredPtr *v1alpha1.RenderedDeviceSpec) error {
	m.log.Debug("Syncing hook manager")
	defer m.log.Debug("Finished syncing hook manager")

	current := util.FromPtr(currentPtr)
	desired := util.FromPtr(desiredPtr)
	if m.initialized.Load() && reflect.DeepEqual(current.Hooks, desired.Hooks) {
		m.log.Debug("Hooks are equal. Nothing to update")
		return nil
	}
	desiredHooks := util.FromPtr(desired.Hooks)
	beforeCreateMap, beforeUpdateMap, beforeRemoveMap, beforeRebootMap, err := m.generateOperationMaps(util.FromPtr(desiredHooks.BeforeUpdating), defaultBeforeUpdateHooks()...)
	if err != nil {
		return err
	}
	afterCreateMap, afterUpdateMap, afterRemoveMap, afterRebootMap, err := m.generateOperationMaps(util.FromPtr(desiredHooks.AfterUpdating), defaultAfterUpdateHooks()...)
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
	m.onBeforeReboot = beforeRebootMap
	m.onAfterReboot = afterRebootMap
	m.initialized.Store(true)
	return nil
}

func (m *manager) setError(path string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.errors[path] = err
	} else {
		delete(m.errors, path)
	}
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
				m.log.Warn("Background jobs channel closed")
				return
			}
			job(ctx)
		case <-ctx.Done():
			m.log.Info("Background jobs context closed")
			return
		}
	}
}

func (m *manager) runActionList(ctx context.Context, path string, actionHooks []ActionHook) {
	if len(actionHooks) == 0 {
		return
	}
	var errs []error
	for _, actionHook := range actionHooks {
		if err := actionHook.OnChange(ctx, path); err != nil {
			m.log.Errorf("error while running hook for path %s: %+v", path, err)
			errs = append(errs, fmt.Errorf("failed to run hook for path %s: %w", path, err))
		}
	}
	m.setError(path, errors.Join(errs...))
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

func (m *manager) OnBeforeReboot(ctx context.Context, path string) {
	m.runActions(ctx, path, m.onBeforeReboot)
}

func (m *manager) OnAfterReboot(ctx context.Context, path string) {
	m.submitBackgroundJob(path, m.onAfterReboot)
}

func (m *manager) Errors() []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	errs := make([]error, 0, len(m.errors))
	for _, err := range m.errors {
		errs = append(errs, err)
	}
	return errs
}

func (m *manager) Close() error {
	close(m.backgroundJobs)
	return nil
}
