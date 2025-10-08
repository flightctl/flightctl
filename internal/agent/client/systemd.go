package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	systemctlCommand        = "/usr/bin/systemctl"
	systemdCredsCommand     = "systemd-creds"
	defaultSystemctlTimeout = time.Minute
)

func NewSystemd(exec executer.Executer) *Systemd {
	return &Systemd{
		exec: exec,
	}
}

type Systemd struct {
	exec executer.Executer
}

func (s *Systemd) Reload(ctx context.Context, name string) error {
	args := []string{"reload", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("reload systemd unit:%s :%w", name, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Start(ctx context.Context, name string) error {
	args := []string{"start", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("start systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
	}

	return nil
}

func (s *Systemd) Stop(ctx context.Context, name string) error {
	args := []string{"stop", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("stop systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Reboot(ctx context.Context) error {
	args := []string{"reboot"}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("reboot systemd: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Restart(ctx context.Context, name string) error {
	args := []string{"restart", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("restart systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Disable(ctx context.Context, name string) error {
	args := []string{"disable", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("disable systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) Enable(ctx context.Context, name string) error {
	args := []string{"enable", name}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("enable systemd unit: %s: %w", name, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) DaemonReload(ctx context.Context) error {
	args := []string{"daemon-reload"}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemctlCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("daemon-reload systemd: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (s *Systemd) ListUnitsByMatchPattern(ctx context.Context, matchPatterns []string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, defaultSystemctlTimeout)
	defer cancel()
	args := append([]string{"list-units", "--all", "--output", "json"}, matchPatterns...)
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(execCtx, systemctlCommand, args...)
	if exitCode != 0 {
		return "", fmt.Errorf("list systemd units: %w", errors.FromStderr(stderr, exitCode))
	}
	out := strings.TrimSpace(stdout)
	return out, nil
}

// CredsEncrypt encrypts data using systemd-creds
// Returns the encrypted data or an error
func (s *Systemd) CredsEncrypt(ctx context.Context, name, withKey, inputPath, outputPath string) error {
	args := []string{
		"encrypt",
		"--with-key=" + withKey,
		"--name=" + name,
		inputPath,
		outputPath,
	}
	_, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemdCredsCommand, args...)
	if exitCode != 0 {
		return fmt.Errorf("systemd-creds encrypt failed: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

// CredsDecrypt decrypts a credential file using systemd-creds
// Returns the decrypted data or an error
func (s *Systemd) CredsDecrypt(ctx context.Context, credPath string) ([]byte, error) {
	stdout, stderr, exitCode := s.exec.ExecuteWithContext(ctx, systemdCredsCommand, "decrypt", credPath, "-")
	if exitCode != 0 {
		return nil, fmt.Errorf("systemd-creds decrypt failed: %w", errors.FromStderr(stderr, exitCode))
	}
	return []byte(stdout), nil
}

// CredsHasTPM2 checks if systemd-creds has TPM2 support
func (s *Systemd) CredsHasTPM2(ctx context.Context) bool {
	_, _, exitCode := s.exec.ExecuteWithContext(ctx, systemdCredsCommand, "has-tpm2")
	return exitCode == 0
}
