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
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	PackageModeAgentContainerName = "flightctl-package-mode-agent"
	PackageModeAgentSSHUser       = "user"
	PackageModeAgentSSHPassword   = "user"
	PackageModeFlightctlUser      = "flightctl"
	packageModeRegistriesConfPath = "/etc/containers/registries.conf.d/flightctl-e2e.conf"
	packageModeRegistryCAPath     = "/etc/pki/ca-trust/source/anchors/flightctl-e2e-registry.crt"

	nestedPodmanStorageConf = `[storage]
driver = "overlay"

[storage.options.overlay]
mount_program = "/usr/bin/fuse-overlayfs"
`
)

func GetPackageModeAgentImage() string {
	return testutil.NewDeviceImageReference(testutil.DeviceTags.Package).String()
}

type PackageModeAgent struct {
	Container testcontainers.Container
	Host      string
	SSHPort   int
}

func StartPackageModeAgent(ctx context.Context, agentConfigDir, registryHost, registryPort string) (*PackageModeAgent, error) {
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

	files := []testcontainers.ContainerFile{
		{
			ContainerFilePath: "/etc/containers/storage.conf",
			FileMode:          0644,
			Reader:            strings.NewReader(nestedPodmanStorageConf),
		},
		{HostFilePath: agentConfigPath, ContainerFilePath: "/etc/flightctl/config.yaml", FileMode: 0644},
	}
	caPath := filepath.Join(testutil.GetTopLevelDir(), "bin", "e2e-certs", "pki", "CA", "ca.crt")
	if _, err := os.Stat(caPath); err == nil {
		files = append(files, testcontainers.ContainerFile{
			HostFilePath:      caPath,
			ContainerFilePath: packageModeRegistryCAPath,
			FileMode:          0644,
		})
	} else {
		logrus.Warnf("e2e registry CA not found at %s; nested podman pulls may fail TLS verification", caPath)
	}

	containerName := fmt.Sprintf("%s-%d-%d", PackageModeAgentContainerName, os.Getpid(), time.Now().UnixNano())
	req := testcontainers.ContainerRequest{
		Image:        GetPackageModeAgentImage(),
		Name:         containerName,
		ExposedPorts: []string{"22/tcp"},
		Cmd:          []string{"/sbin/init"},
		Files:        files,
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Privileged = true
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
	}

	if network != "" && network != "host" {
		req.Networks = []string{network}
	}
	req.WaitingFor = wait.ForListeningPort("22/tcp").WithStartupTimeout(120 * time.Second)

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
	sshPort := mappedPort.Int()

	agent := &PackageModeAgent{
		Container: c,
		Host:      host,
		SSHPort:   sshPort,
	}

	if err := agent.setupContainerEnvironment(ctx, registryHost, registryPort); err != nil {
		_ = c.Terminate(ctx)
		return nil, fmt.Errorf("setup package-mode container environment: %w", err)
	}

	if err := agent.waitForAgentService(ctx, 2*time.Minute); err != nil {
		_ = c.Terminate(ctx)
		return nil, fmt.Errorf("wait for agent service: %w", err)
	}

	logrus.Infof("Package-mode agent container started: %s:%d", host, sshPort)
	return agent, nil
}

func (a *PackageModeAgent) setupContainerEnvironment(ctx context.Context, registryHost, registryPort string) error {
	if err := a.execOK(ctx, "mount --make-rshared /"); err != nil {
		return fmt.Errorf("make root mount shared: %w", err)
	}
	if err := a.setupFlightctlUser(ctx); err != nil {
		return fmt.Errorf("setup flightctl user: %w", err)
	}
	if err := a.setupRegistryAccess(ctx, registryHost, registryPort); err != nil {
		return fmt.Errorf("setup registry access: %w", err)
	}
	if err := a.disableFirewalld(ctx); err != nil {
		return fmt.Errorf("disable firewalld: %w", err)
	}
	return nil
}

func (a *PackageModeAgent) disableFirewalld(ctx context.Context) error {
	exitCode, _, err := a.Container.Exec(ctx, []string{"systemctl", "cat", "firewalld.service"})
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return nil
	}
	return a.execOK(ctx, "systemctl disable --now firewalld.service")
}

func (a *PackageModeAgent) setupFlightctlUser(ctx context.Context) error {
	commands := []string{
		"id -u flightctl >/dev/null 2>&1 || useradd --create-home --user-group flightctl",
		"loginctl enable-linger flightctl >/dev/null 2>&1 || (mkdir -p /var/lib/systemd/linger && touch /var/lib/systemd/linger/flightctl)",
		"grep -q '^flightctl:' /etc/subuid || echo 'flightctl:100000:65536' >> /etc/subuid",
		"grep -q '^flightctl:' /etc/subgid || echo 'flightctl:100000:65536' >> /etc/subgid",
		"uid=$(id -u flightctl) && mkdir -p /run/user/$uid && chown flightctl:flightctl /run/user/$uid && chmod 700 /run/user/$uid",
		"mkdir -p /home/flightctl/.config/containers/systemd",
		"mkdir -p /home/flightctl/.config/systemd/user",
		"mkdir -p /home/flightctl/.local",
		"chown -R flightctl:flightctl /home/flightctl",
	}
	for _, cmd := range commands {
		if err := a.execOK(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

func (a *PackageModeAgent) setupRegistryAccess(ctx context.Context, registryHost, registryPort string) error {
	if registryHost == "" || registryPort == "" {
		return fmt.Errorf("registry host and port are required for package-mode agent setup")
	}

	registryURL := registryHost + ":" + registryPort
	conf := fmt.Sprintf(`[[registry]]
location = "%s"
insecure = true
`, registryURL)
	writeConf := fmt.Sprintf(
		"mkdir -p /etc/containers/registries.conf.d && cat > %s <<'EOF'\n%sEOF",
		packageModeRegistriesConfPath,
		conf,
	)
	if err := a.execOK(ctx, writeConf); err != nil {
		return err
	}
	if err := a.execOK(ctx, fmt.Sprintf("test ! -f %s || update-ca-trust", packageModeRegistryCAPath)); err != nil {
		return err
	}
	logrus.Infof("Configured package-mode agent registry access for %s", registryURL)
	return nil
}

func (a *PackageModeAgent) execOK(ctx context.Context, cmd string) error {
	exitCode, reader, err := a.Container.Exec(ctx, []string{"sh", "-c", cmd})
	if err != nil {
		return fmt.Errorf("exec %q: %w", cmd, err)
	}
	out, readErr := io.ReadAll(reader)
	if readErr != nil {
		return fmt.Errorf("read exec output %q: %w", cmd, readErr)
	}
	if exitCode != 0 {
		return fmt.Errorf("exec %q: exit code %d: %s", cmd, exitCode, string(out))
	}
	return nil
}

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

func (a *PackageModeAgent) GetEnrollmentID(ctx context.Context, timeout time.Duration) (string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		logs, err := a.GetAgentLogs(timeoutCtx)
		if err == nil {
			enrollmentID := testutil.GetEnrollmentIdFromText(logs)
			if enrollmentID != "" {
				return enrollmentID, nil
			}
		}
		select {
		case <-timeoutCtx.Done():
			return "", fmt.Errorf("timeout waiting for enrollment ID in agent logs: %w", timeoutCtx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *PackageModeAgent) Stop(ctx context.Context) error {
	if a.Container != nil {
		return a.Container.Terminate(ctx)
	}
	return nil
}

func (a *PackageModeAgent) GetAgentLogs(ctx context.Context) (string, error) {
	exitCode, reader, err := a.Container.Exec(ctx, []string{
		"journalctl", "-u", "flightctl-agent", "--no-pager", "-o", "cat", "-n", "2000",
	})
	if err != nil {
		return "", fmt.Errorf("get agent logs: %w", err)
	}
	out, readErr := io.ReadAll(reader)
	if readErr != nil {
		return "", fmt.Errorf("read agent logs: %w", readErr)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("get agent logs: exit code %d: %s", exitCode, string(out))
	}
	return filterPackageModeAgentLogs(string(out)), nil
}

func filterPackageModeAgentLogs(logs string) string {
	var useful []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "█") || strings.Contains(line, "▀") {
			continue
		}
		if !packageModeAgentLogLineUseful(line) {
			continue
		}
		useful = append(useful, line)
	}
	if len(useful) > 400 {
		useful = useful[len(useful)-400:]
	}
	if len(useful) == 0 {
		return ""
	}
	return strings.Join(useful, "\n") + "\n"
}

func packageModeAgentLogLineUseful(line string) bool {
	switch {
	case strings.Contains(line, "level="),
		strings.Contains(line, "/enroll/"),
		strings.Contains(line, "Waiting for enrollment"),
		strings.Contains(line, "Bootstrap"),
		strings.Contains(line, "Starting Flight"),
		strings.Contains(line, "Spec reconciliation"),
		strings.Contains(line, "application"),
		strings.Contains(line, "pull"):
		return true
	default:
		return false
	}
}
