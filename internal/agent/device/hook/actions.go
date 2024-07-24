package hook

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

func handleHookActionSystemd(ctx context.Context, log *log.PrefixLogger, exec executer.Executer, action *v1alpha1.HookActionSystemdSpec, filePath string) error {
	actionTimeout, err := parseTimeout(action.Timeout)
	if err != nil {
		return err
	}

	var unitName string
	if action.Unit.Name != "" {
		unitName = action.Unit.Name
	} else {
		// attempt to extract the systemd unit name from the file path
		unitName, err = getSystemdUnitNameFromFilePath(filePath)
		if err != nil {
			log.Errorf("%v: skipping...", err)
			return nil
		}
	}

	for _, op := range action.Unit.Operations {
		if err := executeSystemdOperation(ctx, exec, op, actionTimeout, unitName); err != nil {
			return err
		}
	}

	return nil
}

func executeSystemdOperation(ctx context.Context, exec executer.Executer, op v1alpha1.HookActionSystemdUnitOperations, timeout time.Duration, unitName string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	systemdClient := client.NewSystemd(exec)

	switch op {
	case v1alpha1.SystemdStart:
		if err := systemdClient.Start(ctx, unitName); err != nil {
			return err
		}
	case v1alpha1.SystemdStop:
		if err := systemdClient.Stop(ctx, unitName); err != nil {
			return err
		}
	case v1alpha1.SystemdRestart:
		if err := systemdClient.Restart(ctx, unitName); err != nil {
			return err
		}
	case v1alpha1.SystemdReload:
		if err := systemdClient.Reload(ctx, unitName); err != nil {
			return err
		}
	case v1alpha1.SystemdEnable:
		if err := systemdClient.Enable(ctx, unitName); err != nil {
			return err
		}
	case v1alpha1.SystemdDisable:
		if err := systemdClient.Disable(ctx, unitName); err != nil {
			return err
		}
	case v1alpha1.SystemdDaemonReload:
		if err := systemdClient.DaemonReload(ctx); err != nil {
			return err
		}
	}
	return nil
}

// getSystemdUnitNameFromFilePath attempts to extract the systemd unit name from
// the file path or returns an error if the file does not have a valid systemd
// file suffix.
func getSystemdUnitNameFromFilePath(filePath string) (string, error) {
	unitName := filepath.Base(filePath)

	// list of valid systemd unit file extensions from systemd documentation
	// ref. https://www.freedesktop.org/software/systemd/man/systemd.unit.html
	validExtensions := []string{
		".service",   // Service unit
		".socket",    // Socket unit
		".device",    // Device unit
		".mount",     // Mount unit
		".automount", // Automount unit
		".swap",      // Swap unit
		".target",    // Target unit
		".path",      // Path unit
		".timer",     // Timer unit
		".slice",     // Slice unit
		".scope",     // Scope unit
	}

	// Check if the unit name ends with a valid extension
	for _, ext := range validExtensions {
		if strings.HasSuffix(unitName, ext) {
			return unitName, nil
		}
	}

	return "", fmt.Errorf("invalid systemd unit file: %s", filePath)
}

func handleHookActionExecutable(ctx context.Context, exec executer.Executer, action *v1alpha1.HookActionExecutableSpec, configFilePath string) error {
	actionTimeout, err := parseTimeout(action.Timeout)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()

	var workDir string
	if action.Executable.WorkDir != nil {
		workDir = *action.Executable.WorkDir
		dirExists, err := dirExists(workDir)
		if err != nil {
			return err
		}

		// we expect the directory to exist should be created by config if its new.
		if !dirExists {
			return os.ErrNotExist
		}
	}

	// replace file token in args if it exists
	var envVars []string
	if action.Executable.EnvVars != nil {
		envVars = *action.Executable.EnvVars
	}

	// TODO: cache the map.
	tokenMap := newTokenMap(configFilePath)
	parsedCommand, err := replaceTokens(action.Executable.Run, tokenMap)
	if err != nil {
		return err
	}
	parts := strings.Fields(parsedCommand)
	if len(parts) == 0 {
		return fmt.Errorf("no command provided")
	}

	cmd := parts[0]
	args := parts[1:]

	_, stderr, exitCode := exec.ExecuteWithContextFromDir(ctx, workDir, cmd, args, envVars...)
	if exitCode != 0 {
		return fmt.Errorf("failed to execute command: %s %d: %s", action.Executable.Run, exitCode, stderr)
	}

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
	return false, fmt.Errorf("failed to check if directory exists: %w", err)
}

func parseTimeout(timeout *string) (time.Duration, error) {
	if timeout == nil {
		return DefaultHookActionTimeout, nil
	}
	return time.ParseDuration(*timeout)
}

func getHookActionType(action v1alpha1.HookAction) (string, error) {
	actionMap, err := action.AsHookAction0()
	if err != nil {
		return "", err
	}
	if _, ok := actionMap["executable"]; ok {
		return ExecutableActionType, nil
	} else if _, ok := actionMap["executable"]; ok {
		return SystemdActionType, nil
	} else {
		return "", fmt.Errorf("unknown hook action type: %v", action)
	}
}

func validateEnvVars(envVars []string) error {
	for _, envVar := range envVars {
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid envVar format: should be KEY=VALUE: %s", envVar)
		}
		key, value := parts[0], parts[1]
		if key == "" {
			return fmt.Errorf("invalid envVar format: key cannot be empty: %s", envVar)
		}
		if strings.Contains(key, " ") {
			return fmt.Errorf("invalid envVar format: key cannot contain spaces: %s", envVar)
		}
		if value == "" {
			return fmt.Errorf("invalid envVar format: value cannot be empty: %s", envVar)
		}
		if key != strings.ToUpper(key) {
			return fmt.Errorf("invalid envVar format: key must be uppercase: %s", envVar)
		}
	}
	return nil
}

func replaceTokens(args string, tokens map[string]string) (string, error) {
	tmpl, err := template.New("args").Parse(args)
	if err != nil {
		// unfortunately we can not use errors.As here, as we are working with basic error types.
		if strings.Contains(err.Error(), "function") && strings.Contains(err.Error(), "not defined") {
			return "", fmt.Errorf("%w, %w", ErrInvalidTokenFormat, err)
		}
		return "", err
	}

	// capture the output of the executed template in buffer
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, tokens)
	if err != nil {
		return "", err
	}
	output := buf.String()
	if strings.Contains(output, noValueKey) {
		return "", ErrTokenNotSupported
	}

	return output, nil
}

func newTokenMap(filePath string) map[string]string {
	return map[string]string{
		FilePathKey: filePath,
	}
}

func handleActions[T any](ctx context.Context, log *log.PrefixLogger, exec executer.Executer, event Event[T], actions []v1alpha1.HookAction) error {
	filePath := event.Name
	for i := range actions {
		action := actions[i]
		hookActionType, err := getHookActionType(action)
		if err != nil {
			return err
		}

		switch hookActionType {
		case SystemdActionType:
			hookAction, err := action.AsHookActionSystemdSpec()
			if err != nil {
				return err
			}
			if err := handleHookActionSystemd(ctx, log, exec, &hookAction, filePath); err != nil {
				return err
			}
		case ExecutableActionType:
			hookAction, err := action.AsHookActionExecutableSpec()
			if err != nil {
				return err
			}

			if err := handleHookActionExecutable(ctx, exec, &hookAction, filePath); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown hook action type: %s", hookActionType)
		}
	}

	return nil
}
