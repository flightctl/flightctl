package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	// Enrollment hook methods — added by EDM-5701.
	// Callers provide an EnrollmentContext with device metadata; the hook type
	// string is set internally by each method.
	OnBeforeEnrolling(ctx context.Context, enrollCtx *EnrollmentContext) error
	OnAfterEnrolling(ctx context.Context, enrollCtx *EnrollmentContext) error
}

type manager struct {
	log        *log.PrefixLogger
	readWriter fileio.ReadWriter
	exec       executer.Executer
}

// NewManager creates a hook manager. The readWriter provides both read access
// for loading hook YAML and write access for writing hook-context.json.
func NewManager(readWriter fileio.ReadWriter, exec executer.Executer, log *log.PrefixLogger) Manager {
	return &manager{
		log:        log,
		readWriter: readWriter,
		exec:       exec,
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

// OnBeforeEnrolling writes hook-context.json, then runs BeforeEnrolling hooks
// from both the image and /etc overlay directories. After execution, it
// populates enrollCtx result fields (Success, Output, HookLabels).
func (m *manager) OnBeforeEnrolling(ctx context.Context, enrollCtx *EnrollmentContext) error {
	hookType := api.DeviceLifecycleHookBeforeEnrolling

	// Write hook-context.json and get JSON bytes for env var injection
	jsonBytes, err := writeHookContext(m.readWriter, string(hookType), enrollCtx)
	if err != nil {
		return fmt.Errorf("writing hook context for %s: %w", hookType, err)
	}

	actionCtx := newEnrollmentActionContext(hookType, string(jsonBytes))

	if err := m.readWriter.RemoveFile(HookLabelsPath); err != nil {
		return fmt.Errorf("clearing stale hook labels: %w", err)
	}

	// BeforeEnrolling loads from both image and /etc overlay dirs
	execErr := m.loadAndExecuteActionsFromDirs(ctx, actionCtx, []string{ReadOnlyConfigDir, UserWritableConfigDir})

	// Populate result fields regardless of execution outcome
	enrollCtx.Actions = actionCtx.actionResults
	enrollCtx.Success = execErr == nil

	// Read hook labels if the hooks wrote them
	enrollCtx.HookLabels = m.readHookLabels()

	return execErr
}

// OnAfterEnrolling writes hook-context.json, then runs AfterEnrolling hooks
// from the image directory only (no /etc overlay per design §4.2).
func (m *manager) OnAfterEnrolling(ctx context.Context, enrollCtx *EnrollmentContext) error {
	hookType := api.DeviceLifecycleHookAfterEnrolling

	// Write hook-context.json and get JSON bytes for env var injection
	jsonBytes, err := writeHookContext(m.readWriter, string(hookType), enrollCtx)
	if err != nil {
		return fmt.Errorf("writing hook context for %s: %w", hookType, err)
	}

	actionCtx := newEnrollmentActionContext(hookType, string(jsonBytes))

	// 1.4: AfterEnrolling image-only per design §4.2 — no /etc overlay
	return m.loadAndExecuteActionsFromDirs(ctx, actionCtx, []string{ReadOnlyConfigDir})
}

// loadAndExecuteActionsFromDirs loads hook actions from the given config roots
// and executes them in order for the hook type in actionCtx.
func (m *manager) loadAndExecuteActionsFromDirs(ctx context.Context, actionCtx *actionContext, dirs []string) error {
	m.log.Debugf("Starting hook manager On%s()", actionCtx.hook)
	defer m.log.Debugf("Finished hook manager On%s()", actionCtx.hook)

	actions, err := m.loadAndMergeActionsFromDirs(actionCtx.hook, dirs)
	if err != nil {
		return err
	}
	return m.executeActions(ctx, actions, actionCtx)
}

// loadAndExecuteActions loads hook actions from the default config directories
// and executes them in order for the hook type in actionCtx.
func (m *manager) loadAndExecuteActions(ctx context.Context, actionCtx *actionContext) error {
	m.log.Debugf("Starting hook manager On%s()", actionCtx.hook)
	defer m.log.Debugf("Finished hook manager On%s()", actionCtx.hook)

	actions, err := m.loadAndMergeActions(actionCtx.hook)
	if err != nil {
		return err
	}
	return m.executeActions(ctx, actions, actionCtx)
}

// loadAndMergeActions loads and merges hook actions from the default config
// directories (ReadOnlyConfigDir and UserWritableConfigDir).
func (m *manager) loadAndMergeActions(hookType api.DeviceLifecycleHookType) ([]api.HookAction, error) {
	return m.loadAndMergeActionsFromDirs(hookType, []string{ReadOnlyConfigDir, UserWritableConfigDir})
}

// loadAndMergeActionsFromDirs loads hook YAML from the specified base directories,
// merges them in lexical order, and returns the flattened action list.
// Each dir is expected to contain hooks.d/<hooktype>/*.yaml.
func (m *manager) loadAndMergeActionsFromDirs(hookType api.DeviceLifecycleHookType, dirs []string) ([]api.HookAction, error) {
	actionsMap := map[string][]api.HookAction{}
	for _, dir := range dirs {
		err := m.loadActions(actionsMap, filepath.Join(dir, HooksDropInDirName, strings.ToLower(string(hookType)), "*.yaml"))
		if err != nil {
			return nil, err
		}
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

// loadActions reads hook YAML files matching actionFilesGlob, parses and
// validates them, and appends the resulting actions to actionsMap keyed by filename.
func (m *manager) loadActions(actionsMap map[string][]api.HookAction, actionFilesGlob string) error {
	actionFiles, err := filepath.Glob(m.readWriter.PathFor(actionFilesGlob))
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
		actionCtx.actionIndex = i + 1
		if err := executeAction(ctx, m.exec, m.log, action, actionCtx, actionTimeout); err != nil {
			return fmt.Errorf("%w: %s hook action #%d: %w", errors.ErrFailedToExecute, actionCtx.hook, i+1, err)
		}
		actionCtx.actionIndex = 0
	}
	return nil
}

// readHookLabels reads labels from HookLabelsPath if the file exists.
// Returns nil if the file does not exist or cannot be parsed.
func (m *manager) readHookLabels() map[string]string {
	path := m.readWriter.PathFor(HookLabelsPath)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		m.log.Warnf("Failed to read hook labels from %s: %v", HookLabelsPath, err)
		return nil
	}
	defer file.Close()

	limited := io.LimitReader(file, int64(MaxHookLabelsFileSize)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		m.log.Warnf("Failed to read hook labels from %s: %v", HookLabelsPath, err)
		return nil
	}
	if len(data) > MaxHookLabelsFileSize {
		m.log.Warnf("Hook labels file %s exceeds size limit (%d bytes)", HookLabelsPath, MaxHookLabelsFileSize)
		return nil
	}

	var labels map[string]string
	if err := json.Unmarshal(data, &labels); err != nil {
		m.log.Warnf("Failed to parse hook labels from %s: %v", HookLabelsPath, err)
		return nil
	}
	return labels
}
