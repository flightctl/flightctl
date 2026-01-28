package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"sigs.k8s.io/yaml"
)

// TODO: Deduplicate with internal/agent/config.go after agreeing how to best break import cycles
const (
	// ReadOnlyConfigDir is where read-only configuration files are stored
	ReadOnlyConfigDir = "/usr/lib/flightctl"
	// ReadOnlyConfigDir is where read-only configuration files are stored
	UserWritableConfigDir = "/etc/flightctl"
	// HooksDropInDirName is the subdirectory in which hooks are stored
	HooksDropInDirName = "hooks.d"
)

var _ Manager = (*manager)(nil)

type Manager interface {
	Sync(current, desired *api.DeviceSpec) error

	OnBeforeUpdating(ctx context.Context, current *api.DeviceSpec, desired *api.DeviceSpec) error
	OnAfterUpdating(ctx context.Context, current *api.DeviceSpec, desired *api.DeviceSpec, systemRebooted bool) error
	OnBeforeRebooting(ctx context.Context) error
	OnAfterRebooting(ctx context.Context) error
}

type manager struct {
	log    *log.PrefixLogger
	reader fileio.Reader
	exec   executer.Executer
}

func NewManager(reader fileio.Reader, exec executer.Executer, log *log.PrefixLogger) Manager {
	return &manager{
		log:    log,
		reader: reader,
		exec:   exec,
	}
}

func (m *manager) Sync(currentPtr, desiredPtr *api.DeviceSpec) error {
	return nil
}

func (m *manager) OnBeforeUpdating(ctx context.Context, current *api.DeviceSpec, desired *api.DeviceSpec) error {
	actionCtx := newActionContext(api.DeviceLifecycleHookBeforeUpdating, current, desired, false)
	return m.loadAndExecuteActions(ctx, actionCtx)
}

func (m *manager) OnAfterUpdating(ctx context.Context, current *api.DeviceSpec, desired *api.DeviceSpec, systemRebooted bool) error {

	actionCtx := newActionContext(api.DeviceLifecycleHookAfterUpdating, current, desired, systemRebooted)
	return m.loadAndExecuteActions(ctx, actionCtx)
}

func (m *manager) OnBeforeRebooting(ctx context.Context) error {
	actionCtx := newActionContext(api.DeviceLifecycleHookBeforeRebooting, nil, nil, false)
	return m.loadAndExecuteActions(ctx, actionCtx)
}

func (m *manager) OnAfterRebooting(ctx context.Context) error {
	actionCtx := newActionContext(api.DeviceLifecycleHookAfterRebooting, nil, nil, true)
	return m.loadAndExecuteActions(ctx, actionCtx)
}

func (m *manager) loadAndExecuteActions(ctx context.Context, actionCtx *actionContext) error {
	m.log.Debugf("Starting hook manager On%s()", actionCtx.hook)
	defer m.log.Debugf("Finished hook manager On%s()", actionCtx.hook)

	actions, err := m.loadAndMergeActions(actionCtx.hook)
	if err != nil {
		return err
	}
	return m.executeActions(ctx, actions, actionCtx)
}

func (m *manager) loadAndMergeActions(hookType api.DeviceLifecycleHookType) ([]api.HookAction, error) {
	actionsMap := map[string][]api.HookAction{}
	// Read actions from the read-only hooks directory (/usr/lib/flightctl/hooks.d/${hookType}/*.yaml)
	err := m.loadActions(actionsMap, filepath.Join(ReadOnlyConfigDir, HooksDropInDirName, strings.ToLower(string(hookType)), "*.yaml"))
	if err != nil {
		return nil, err
	}
	// Overlay actions from the user-writable hooks directory (/etc/flightctl/hooks.d/${hookType}/*.yaml)
	err = m.loadActions(actionsMap, filepath.Join(UserWritableConfigDir, HooksDropInDirName, strings.ToLower(string(hookType)), "*.yaml"))
	if err != nil {
		return nil, err
	}
	// Sort files containing actions in lexical order, then flatten actionMap into a list of actions in that order
	keyList := make([]string, 0, len(actionsMap))
	for k := range actionsMap {
		keyList = append(keyList, k)
	}
	sort.Strings(keyList)
	actions := []api.HookAction{}
	for _, k := range keyList {
		actions = append(actions, actionsMap[k]...)
	}
	return actions, nil
}

func (m *manager) loadActions(actionsMap map[string][]api.HookAction, actionFilesGlob string) error {
	actionFiles, err := filepath.Glob(m.reader.PathFor(actionFilesGlob))
	if err != nil {
		return fmt.Errorf("%w: actions matching %q: %w", errors.ErrLookingForHook, actionFilesGlob, err)
	}
	for _, f := range actionFiles {
		contents, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("%w %w: %w", errors.ErrReadingHookActionsFrom, errors.WithElement(f), err)
		}
		actions := []api.HookAction{}
		if err := yaml.UnmarshalStrict(contents, &actions); err != nil {
			return fmt.Errorf("%w: %q: %w", errors.ErrParsingHookActionsFrom, f, err)
		}
		allErrs := []error{}
		for i, action := range actions {
			allErrs = append(allErrs, action.Validate(fmt.Sprintf("validating %q hook action[%d]", f, i))...)
		}
		if len(allErrs) > 0 {
			return errors.Join(allErrs...)
		}
		actionsMap[filepath.Base(f)] = actions
	}
	return nil
}

func (m *manager) executeActions(ctx context.Context, actions []api.HookAction, actionCtx *actionContext) error {
	for i, action := range actions {
		if err := checkActionDependency(action); err != nil {
			m.log.Debugf("Skipping %s hook action #%d: dependencies not met: %v", actionCtx.hook, i+1, err)
			continue
		}
		if action.If != nil {
			conditionsMet := true
			for j, condition := range *action.If {
				conditionMet, err := checkCondition(&condition, actionCtx)
				if err != nil {
					return fmt.Errorf("failed to check %s hook action #%d condition #%d: %w", actionCtx.hook, i+1, j+1, err)
				}
				if !conditionMet {
					m.log.Debugf("Skipping %s hook action #%d condition #%d: condition not met", actionCtx.hook, i+1, j+1)
					conditionsMet = false
					break
				}
			}
			if !conditionsMet {
				continue
			}
		}

		actionTimeout, err := parseTimeout(action.Timeout)
		if err != nil {
			return err
		}
		if err := executeAction(ctx, m.exec, m.log, action, actionCtx, actionTimeout); err != nil {
			return fmt.Errorf("%w: %s hook action #%d: %w", errors.ErrFailedToExecute, actionCtx.hook, i+1, err)
		}
	}
	return nil
}
