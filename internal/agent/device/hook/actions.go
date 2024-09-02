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

const (
	DefaultHookActionTimeout = 10 * time.Second
)

type systemdActionHook struct {
	actionTimeout time.Duration
	operations    []v1alpha1.HookActionSystemdUnitOperations
	unitName      string
	exec          executer.Executer
	log           *log.PrefixLogger
}

func newSystemdActionHook(unitName string, operations []v1alpha1.HookActionSystemdUnitOperations, exec executer.Executer, actionTimeout time.Duration, log *log.PrefixLogger) ActionHook {
	return &systemdActionHook{
		actionTimeout: actionTimeout,
		operations:    operations,
		unitName:      unitName,
		exec:          exec,
		log:           log,
	}
}

func (s *systemdActionHook) OnChange(ctx context.Context, path string) error {
	var unitName string
	var err error
	if s.unitName != "" {
		unitName = s.unitName
	} else {
		// attempt to extract the systemd unit name from the file path
		unitName, err = getSystemdUnitNameFromFilePath(path)
		if err != nil {
			s.log.Errorf("%v: skipping...", err)
			return nil
		}
	}

	for _, op := range s.operations {
		if err := s.executeSystemdOperation(ctx, op, unitName); err != nil {
			return err
		}
	}

	return nil
}

func (s *systemdActionHook) executeSystemdOperation(ctx context.Context, op v1alpha1.HookActionSystemdUnitOperations, unitName string) error {
	ctx, cancel := context.WithTimeout(ctx, s.actionTimeout)
	defer cancel()

	systemdClient := client.NewSystemd(s.exec)

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

type executableActionHook struct {
	cmd           string
	envVars       []string
	exec          executer.Executer
	actionTimeout time.Duration
	workDir       *string
	log           *log.PrefixLogger
}

func newExecutableActionHook(cmd string, envVars []string, exec executer.Executer, actionTimeout time.Duration, workDir *string, log *log.PrefixLogger) ActionHook {
	return &executableActionHook{
		cmd:           cmd,
		envVars:       envVars,
		exec:          exec,
		actionTimeout: actionTimeout,
		workDir:       workDir,
		log:           log,
	}
}

func (e *executableActionHook) OnChange(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, e.actionTimeout)
	defer cancel()

	var workDir string
	if e.workDir != nil {
		workDir = *e.workDir
		dirExists, err := dirExists(workDir)
		if err != nil {
			return err
		}

		// we expect the directory to exist should be created by config if its new.
		if !dirExists {
			return fmt.Errorf("workdir %s: %w", workDir, os.ErrNotExist)
		}
	}

	// replace file token in args if it exists
	tokenMap := newTokenMap(path)
	cmd, err := replaceTokens(e.cmd, tokenMap)
	if err != nil {
		return err
	}

	// We cannot split the cmd by whitespace because we want to allow running a command with arguments the contain spaces
	// For example bash -c might be useful if we want to run several commands as a single action.  Therefore, all commands
	// will run by using 'bash -c' to let bash do the parsing
	_, stderr, exitCode := e.exec.ExecuteWithContextFromDir(ctx, workDir, "bash", []string{"-c", cmd}, e.envVars...)
	if exitCode != 0 {
		e.log.Errorf("execute hook for path %s failed with exitCode %d for command %s: %s", path, exitCode, cmd, stderr)
		return fmt.Errorf("command for path %s exited with code %d: %s", path, exitCode, stderr)
	}

	return nil
}
