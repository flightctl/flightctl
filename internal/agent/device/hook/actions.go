package hook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

type CommandLineVarKey string

const (
	DefaultHookActionTimeout = 10 * time.Second

	// PathKey defines the name of the variable that contains the path operated on
	PathKey CommandLineVarKey = "Path"
	// FilesKey defines the name of the variable that contains a space-
	// separated list of files created, updated, or removed during the update
	FilesKey CommandLineVarKey = "Files"
	// CreatedKey defines the name of the variable that contains a space-
	// separated list of files created during the update
	CreatedKey CommandLineVarKey = "CreatedFiles"
	// UpdatedKey defines the name of the variable that contains a space-
	// separated list of files updated during the update
	UpdatedKey CommandLineVarKey = "UpdatedFiles"
	// RemovedKey defines the name of the variable that contains a space-
	// separated list of files removed during the update
	RemovedKey CommandLineVarKey = "RemovedFiles"
	// BackupKey defines the name of the variable that contains a space-
	// separated list of files backed up before removal from the system
	// into a temporary location deleted after the action completes.
	BackupKey CommandLineVarKey = "BackupFiles"
)

type actionContext struct {
	hook            api.DeviceLifecycleHookType
	systemRebooted  bool
	createdFiles    map[string]api.FileSpec
	updatedFiles    map[string]api.FileSpec
	removedFiles    map[string]api.FileSpec
	commandLineVars map[CommandLineVarKey]string
}

func newActionContext(hook api.DeviceLifecycleHookType, current *api.DeviceSpec, desired *api.DeviceSpec, systemRebooted bool) *actionContext {
	actionContext := &actionContext{
		hook:            hook,
		systemRebooted:  systemRebooted,
		createdFiles:    make(map[string]api.FileSpec),
		updatedFiles:    make(map[string]api.FileSpec),
		removedFiles:    make(map[string]api.FileSpec),
		commandLineVars: make(map[CommandLineVarKey]string),
	}
	resetCommandLineVars(actionContext)
	if current != nil || desired != nil {
		defaultIfNil := func(spec *api.DeviceSpec) *api.DeviceSpec {
			if spec == nil {
				return &api.DeviceSpec{}
			}
			return spec
		}
		computeFileDiff(actionContext, defaultIfNil(current), defaultIfNil(desired))
	}
	return actionContext
}

func resetCommandLineVars(actionCtx *actionContext) {
	clear(actionCtx.commandLineVars)
	for _, key := range []CommandLineVarKey{PathKey, FilesKey, CreatedKey, UpdatedKey, RemovedKey, BackupKey} {
		actionCtx.commandLineVars[key] = ""
	}
}

func computeFileDiff(actionCtx *actionContext, current *api.DeviceSpec, desired *api.DeviceSpec) {
	currentFileList, _ := config.ProviderSpecToFiles(current.Config)
	desiredFileList, _ := config.ProviderSpecToFiles(desired.Config)

	currentFileMap := make(map[string]api.FileSpec)
	for _, f := range currentFileList {
		currentFileMap[f.Path] = f
	}
	for _, f := range desiredFileList {
		if content, ok := currentFileMap[f.Path]; !ok {
			actionCtx.createdFiles[f.Path] = api.FileSpec{}
		} else if !reflect.DeepEqual(f, content) {
			actionCtx.updatedFiles[f.Path] = api.FileSpec{}
		}
	}

	desiredFileMap := make(map[string]api.FileSpec)
	for _, f := range desiredFileList {
		desiredFileMap[f.Path] = f
	}
	for _, f := range currentFileList {
		if content, ok := desiredFileMap[f.Path]; !ok {
			actionCtx.removedFiles[f.Path] = content
		}
	}
}

func executeAction(ctx context.Context, exec executer.Executer, log *log.PrefixLogger, action api.HookAction, actionCtx *actionContext, actionTimeout time.Duration) error {
	actionType, err := action.Type()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()

	switch actionType {
	case api.HookActionTypeRun:
		runAction, err := action.AsHookActionRun()
		if err != nil {
			return err
		}
		return executeRunAction(ctx, exec, log, runAction, actionCtx)
	default:
		return fmt.Errorf("%w: %q", errors.ErrUnknownHookActionType, actionType)
	}
}

func executeRunAction(ctx context.Context, exec executer.Executer, log *log.PrefixLogger,
	action api.HookActionRun, actionCtx *actionContext) error {

	var workDir string
	if action.WorkDir != nil {
		workDir = *action.WorkDir
		dirExists, err := dirExists(workDir)
		if err != nil {
			return err
		}

		// we expect the directory to exist should be created by config if its new.
		if !dirExists {
			return fmt.Errorf("workdir %s: %w", workDir, os.ErrNotExist)
		}
	}

	// render variables in args if they exist
	commandLine := replaceTokens(action.Run, actionCtx.commandLineVars)
	cmd, args := splitCommandAndArgs(commandLine)

	if err := validateEnvVars(action.EnvVars); err != nil {
		return err
	}
	envVars := util.LabelMapToArray(action.EnvVars)

	// Inject agent PID as environment variable for hooks to use
	envVars = append(envVars, fmt.Sprintf("FLIGHTCTL_AGENT_PID=%d", os.Getpid()))

	_, stderr, exitCode := exec.ExecuteWithContextFromDir(ctx, workDir, cmd, args, envVars...)
	if exitCode != 0 {
		log.Errorf("Running %q returned with exit code %d: %s", commandLine, exitCode, stderr)
		return fmt.Errorf("%w: %s (%d)", errors.ErrExitCode, stderr, exitCode)
	}
	log.Infof("Hook %s executed %q without error", actionCtx.hook, commandLine)

	return nil
}

func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.IsDir(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check if directory %s exists: %w", path, err)
}

func parseTimeout(timeout *string) (time.Duration, error) {
	if timeout == nil {
		return DefaultHookActionTimeout, nil
	}
	return time.ParseDuration(*timeout)
}

func splitCommandAndArgs(command string) (string, []string) {
	parts := splitWithQuotes(command)
	if len(parts) == 0 {
		return "", []string{}
	}
	return parts[0], parts[1:]
}

// splitWithQuotes splits a command string into tokens, respecting basic shell like quoting rules.
func splitWithQuotes(s string) []string {
	var (
		args    []string
		current strings.Builder
		inQuote rune
		escaped bool
	)

	for _, r := range s {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false

		case r == '\\':
			escaped = true

		case (r == '"' || r == '\''):
			if inQuote == 0 {
				inQuote = r          // start quote
				current.WriteRune(r) // preserve opening unmatched quote
			} else if inQuote == r {
				current.WriteRune(r) // preserve closing quote
				inQuote = 0          // quote closed
			} else {
				current.WriteRune(r) // mismatched quote
			}

		// treat space || tab as delimiter if not inside quotes
		case (r == ' ' || r == '\t') && inQuote == 0:
			if current.Len() > 0 {
				// flush the current token, removing matched surrounding quotes "foo" --> foo
				args = append(args, stripMatchedQuotes(current.String()))
				current.Reset()
			}

		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		// add final token with matched quotes stripped
		args = append(args, stripMatchedQuotes(current.String()))
	}

	if len(args) == 0 {
		return []string{}
	}

	return args
}

// stripMatchedQuotes removes surrounding matching quotes from a string.
// It only strips the quotes if both the first and last characters match
// and are either single or double quotes.
func stripMatchedQuotes(s string) string {
	if len(s) >= 2 {
		first := s[0]
		last := s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func validateEnvVars(envVars *map[string]string) error {
	if envVars == nil {
		return nil
	}
	for key, value := range *envVars {
		if key == "" {
			return fmt.Errorf("%w: key cannot be empty: %s", errors.ErrInvalidEnvvarFormat, strings.Join([]string{key, value}, "="))
		}
		if strings.Contains(key, " ") {
			return fmt.Errorf("%w: key cannot contain spaces: %s", errors.ErrInvalidEnvvarFormat, strings.Join([]string{key, value}, "="))
		}
		if value == "" {
			return fmt.Errorf("%w: value cannot be empty: %s", errors.ErrInvalidEnvvarFormat, strings.Join([]string{key, value}, "="))
		}
		if key != strings.ToUpper(key) {
			return fmt.Errorf("%w: key must be uppercase: %s", errors.ErrInvalidEnvvarFormat, strings.Join([]string{key, value}, "="))
		}
	}
	return nil
}

// replaceTokens replaces all registered command line variables with the
// provided values. Wrongly formatted or unknown variables are left in
// in the string.
func replaceTokens(s string, tokens map[CommandLineVarKey]string) string {
	var sb strings.Builder
	sb.Grow(len(s))

	i := 0
	for i < len(s) {
		// find the next placeholder
		if i+2 < len(s) && s[i] == '$' && s[i+1] == '{' { // `${`
			end := i + 2
			for end < len(s) && s[end] != '}' {
				end++
			}
			// ensure the placeholder is closed
			if end < len(s) && s[end] == '}' { // `}`
				token := s[i+2 : end]
				trimmedToken := strings.TrimSpace(token)
				// replace token if it exists otherwise return original
				if val, exists := tokens[CommandLineVarKey(trimmedToken)]; exists {
					sb.WriteString(val)
				} else {
					sb.WriteString("${" + token + "}")
				}
				i = end + 1
				continue
			}
		}
		sb.WriteByte(s[i])
		i++
	}

	return sb.String()
}

func checkActionDependency(action api.HookAction) error {
	actionType, err := action.Type()
	if err != nil {
		return err
	}

	switch actionType {
	case api.HookActionTypeRun:
		runAction, err := action.AsHookActionRun()
		if err != nil {
			return err
		}
		return checkRunActionDependency(runAction)
	default:
		return fmt.Errorf("%w: %q", errors.ErrUnknownHookActionType, actionType)
	}
}

// checkRunActionDependency checks if the first executable in the run action is available
func checkRunActionDependency(action api.HookActionRun) error {
	parts := strings.Fields(action.Run)
	for _, part := range parts {
		// skip if ENV var prefix
		if strings.Contains(part, "=") {
			continue
		}

		_, err := exec.LookPath(part)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return fmt.Errorf("%w: %s", err, part)
			} else if pathErr, ok := err.(*os.PathError); ok {
				return fmt.Errorf("%w: %s", pathErr.Err, part)
			} else {
				return err
			}
		}

		// TODO: run can include multiple commands, for now we only verify the
		// first
		return nil
	}

	if len(parts) == 0 {
		return fmt.Errorf("%w: no executable: %s", errors.ErrRunActionInvalid, action.Run)
	}

	return nil
}
