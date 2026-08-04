//go:build linux

package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	PackageModeAgentContainerName = "flightctl-package-mode-agent"
	PackageModeAgentSSHUser       = "user"
	PackageModeAgentSSHPassword   = "user"
	PackageModeFlightctlUser      = "flightctl"
)

// GetPackageModeAgentImage returns the OCI image reference for the package-mode agent container.
// The image must be loaded into local podman storage before tests run (in CI, this is done
// by loading the cs9-regular agent bundle artifact).
func GetPackageModeAgentImage() string {
	return "quay.io/flightctl/flightctl-device:base-cs9-regular"
}

// PackageModeAgent represents a testcontainer running the flightctl agent in package mode.
type PackageModeAgent struct {
	Container testcontainers.Container
	Host      string
	SSHPort   int
}

// StartPackageModeAgent starts a privileged systemd container with the flightctl agent.
// The container runs cs9-regular (no bootc/rpm-ostree) so the agent reports osMode=package.
func StartPackageModeAgent(ctx context.Context, agentConfigDir string) (*PackageModeAgent, error) {
	containers.ConfigureDockerHost()
	network := containers.GetDockerNetwork()

	agentConfigPath := filepath.Join(agentConfigDir, "config.yaml")
	agentCertsDir := filepath.Join(agentConfigDir, "certs")

	if _, err := os.Stat(agentConfigPath); err != nil {
		return nil, fmt.Errorf("agent config not found at %s: %w", agentConfigPath, err)
	}
	if _, err := os.Stat(agentCertsDir); err != nil {
		return nil, fmt.Errorf("agent certs dir not found at %s: %w", agentCertsDir, err)
	}

	req := testcontainers.ContainerRequest{
		Image:        GetPackageModeAgentImage(),
		Name:         PackageModeAgentContainerName,
		ExposedPorts: []string{"22/tcp"},
		Privileged:   true,
		Cmd:          []string{"/sbin/init"},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: agentConfigPath, ContainerFilePath: "/etc/flightctl/config.yaml", FileMode: 0644},
		},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Tmpfs = map[string]string{
				"/run":      "rw",
				"/run/lock": "rw",
			}
			hc.CgroupnsMode = "host"
			hc.Binds = append(hc.Binds,
				"/sys/fs/cgroup:/sys/fs/cgroup:rw",
				fmt.Sprintf("%s:/etc/flightctl/certs:ro", agentCertsDir),
			)
			if containers.IsPodman() {
				hc.ExtraHosts = append(hc.ExtraHosts, "host.containers.internal:host-gateway")
			}
		},
		WaitingFor: wait.ForListeningPort("22/tcp").WithStartupTimeout(120 * time.Second),
	}

	if network != "" && network != "host" {
		req.Networks = []string{network}
	}
	if network == "host" {
		req.NetworkMode = "host"
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ProviderType:     containers.GetProviderType(),
		ContainerRequest: req,
		Started:          true,
		Reuse:            false,
	})
	if err != nil {
		return nil, fmt.Errorf("start package-mode agent container: %w", err)
	}

	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, fmt.Errorf("get container host: %w", err)
	}

	mappedPort, err := c.MappedPort(ctx, "22")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, fmt.Errorf("get SSH port: %w", err)
	}

	agent := &PackageModeAgent{
		Container: c,
		Host:      host,
		SSHPort:   mappedPort.Int(),
	}

	if err := agent.setupFlightctlUser(ctx); err != nil {
		_ = c.Terminate(ctx)
		return nil, fmt.Errorf("setup flightctl user: %w", err)
	}

	if err := agent.waitForAgentService(ctx, 2*time.Minute); err != nil {
		_ = c.Terminate(ctx)
		return nil, fmt.Errorf("wait for agent service: %w", err)
	}

	logrus.Infof("Package-mode agent container started: %s:%d", host, mappedPort.Int())
	return agent, nil
}

// setupFlightctlUser creates the flightctl user with linger for rootless podman.
func (a *PackageModeAgent) setupFlightctlUser(ctx context.Context) error {
	commands := []string{
		"id -u flightctl >/dev/null 2>&1 || useradd --create-home --user-group flightctl",
		"mkdir -p /var/lib/systemd/linger && touch /var/lib/systemd/linger/flightctl",
		"mkdir -p /home/flightctl/.config/containers/systemd",
		"mkdir -p /home/flightctl/.config/systemd/user",
		"mkdir -p /home/flightctl/.local",
		"chown -R flightctl:flightctl /home/flightctl",
	}
	for _, cmd := range commands {
		exitCode, _, err := a.Container.Exec(ctx, []string{"sh", "-c", cmd})
		if err != nil {
			return fmt.Errorf("exec %q: %w", cmd, err)
		}
		if exitCode != 0 {
			return fmt.Errorf("exec %q: exit code %d", cmd, exitCode)
		}
	}
	return nil
}

// waitForAgentService waits for the flightctl-agent service to be active.
func (a *PackageModeAgent) waitForAgentService(ctx context.Context, timeout time.Duration) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		exitCode, _, err := a.Container.Exec(timeoutCtx, []string{"systemctl", "is-active", "flightctl-agent"})
		if err == nil && exitCode == 0 {
			return nil
		}
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("timeout waiting for flightctl-agent service: %w", timeoutCtx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// RunSSH runs a command via SSH and returns stdout.
func (a *PackageModeAgent) RunSSH(ctx context.Context, args []string) (string, error) {
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-p", fmt.Sprintf("%d", a.SSHPort),
		fmt.Sprintf("%s@%s", PackageModeAgentSSHUser, a.Host),
	}
	sshArgs = append(sshArgs, strings.Join(args, " "))

	//nolint:gosec // G204: args come from test code, not user input
	cmd := exec.CommandContext(ctx, "sshpass", append([]string{"-p", PackageModeAgentSSHPassword, "ssh"}, sshArgs...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ssh command failed: %w: %s", err, string(out))
	}
	return string(out), nil
}

// GetEnrollmentID waits for and returns the agent's enrollment ID.
func (a *PackageModeAgent) GetEnrollmentID(ctx context.Context, timeout time.Duration) (string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		out, err := a.RunSSH(timeoutCtx, []string{"cat", "/var/lib/flightctl/enrollment-id", "2>/dev/null"})
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
		select {
		case <-timeoutCtx.Done():
			return "", fmt.Errorf("timeout waiting for enrollment ID: %w", timeoutCtx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// Stop terminates the container.
func (a *PackageModeAgent) Stop(ctx context.Context) error {
	if a.Container != nil {
		return a.Container.Terminate(ctx)
	}
	return nil
}

// GetAgentLogs returns the flightctl-agent journal logs.
func (a *PackageModeAgent) GetAgentLogs(ctx context.Context) (string, error) {
	exitCode, reader, err := a.Container.Exec(ctx, []string{"journalctl", "-u", "flightctl-agent", "--no-pager"})
	if err != nil {
		return "", fmt.Errorf("get agent logs: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("get agent logs: exit code %d", exitCode)
	}
	var buf strings.Builder
	if _, err := io.Copy(&buf, reader); err != nil {
		return "", fmt.Errorf("read agent logs: %w", err)
	}
	return buf.String(), nil
}
