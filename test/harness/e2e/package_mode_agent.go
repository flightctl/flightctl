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
// registryHost/registryPort configure insecure TLS access to the e2e registry (same role as
// inject_agent_files_into_qcow.sh for VM images).
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
		Privileged:   true,
		Cmd:          []string{"/sbin/init"},
		Files:        files,
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
	}

	if network != "" && network != "host" {
		req.Networks = []string{network}
	}
	if network == "host" {
		req.NetworkMode = "host"
		// Host network has no published port mapping; ForListeningPort uses MappedPort.
		req.WaitingFor = wait.ForExec([]string{"sh", "-c", "ss -ltn | grep -q ':22 '"}).
			WithStartupTimeout(120 * time.Second)
	} else {
		req.WaitingFor = wait.ForListeningPort("22/tcp").WithStartupTimeout(120 * time.Second)
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

	// Host network mode discards published ports; SSH is on the host's :22.
	sshPort := 22
	if network != "host" {
		mappedPort, err := c.MappedPort(ctx, "22")
		if err != nil {
			_ = c.Terminate(ctx)
			return nil, fmt.Errorf("get SSH port: %w", err)
		}
		sshPort = mappedPort.Int()
	}

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

// setupContainerEnvironment prepares the agent container for nested rootless podman and
// e2e registry pulls (CA trust + insecure registry), matching VM qcow injection.
func (a *PackageModeAgent) setupContainerEnvironment(ctx context.Context, registryHost, registryPort string) error {
	if err := a.setupFlightctlUser(ctx); err != nil {
		return fmt.Errorf("setup flightctl user: %w", err)
	}
	if err := a.setupRegistryAccess(ctx, registryHost, registryPort); err != nil {
		return fmt.Errorf("setup registry access: %w", err)
	}
	// firewalld is enabled in cs9-regular and interferes with nested podman networking.
	if err := a.execOK(ctx, "systemctl disable --now firewalld.service >/dev/null 2>&1 || true"); err != nil {
		return fmt.Errorf("disable firewalld: %w", err)
	}
	return nil
}

// setupFlightctlUser creates the flightctl user with linger and subuids for rootless podman.
func (a *PackageModeAgent) setupFlightctlUser(ctx context.Context) error {
	commands := []string{
		"id -u flightctl >/dev/null 2>&1 || useradd --create-home --user-group flightctl",
		"loginctl enable-linger flightctl >/dev/null 2>&1 || (mkdir -p /var/lib/systemd/linger && touch /var/lib/systemd/linger/flightctl)",
		"grep -q '^flightctl:' /etc/subuid || echo 'flightctl:100000:65536' >> /etc/subuid",
		"grep -q '^flightctl:' /etc/subgid || echo 'flightctl:100000:65536' >> /etc/subgid",
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

// setupRegistryAccess installs the e2e registry CA and marks the registry insecure for podman.
func (a *PackageModeAgent) setupRegistryAccess(ctx context.Context, registryHost, registryPort string) error {
	if registryHost == "" || registryPort == "" {
		logrus.Warn("package-mode agent: registry host/port empty; skipping insecure registry configuration")
		return a.execOK(ctx, fmt.Sprintf("test ! -f %s || update-ca-trust", packageModeRegistryCAPath))
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
	out, _ := io.ReadAll(reader)
	if exitCode != 0 {
		return fmt.Errorf("exec %q: exit code %d: %s", cmd, exitCode, string(out))
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

// GetEnrollmentID waits for and returns the agent's enrollment ID by parsing the agent logs.
// The enrollment ID appears in the agent logs as part of the enrollment URL (e.g. /enroll/<id>).
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

// Stop terminates the container.
func (a *PackageModeAgent) Stop(ctx context.Context) error {
	if a.Container != nil {
		return a.Container.Terminate(ctx)
	}
	return nil
}

// GetAgentLogs returns the flightctl-agent journal logs with enrollment QR noise filtered out.
func (a *PackageModeAgent) GetAgentLogs(ctx context.Context) (string, error) {
	// Prefer structured/useful lines; keep /enroll/ for GetEnrollmentID. Drop QR block noise.
	cmd := `journalctl -u flightctl-agent --no-pager -o cat -n 2000 | grep -E 'level=|/enroll/|Waiting for enrollment|Bootstrap|Starting Flight|Spec reconciliation|application|pull|error|Error' | grep -v '█' | grep -v '▀' | tail -n 400`
	exitCode, reader, err := a.Container.Exec(ctx, []string{"sh", "-c", cmd})
	if err != nil {
		return "", fmt.Errorf("get agent logs: %w", err)
	}
	if exitCode != 0 {
		// grep returns 1 when there are no matches; still try a raw tail.
		exitCode, reader, err = a.Container.Exec(ctx, []string{"journalctl", "-u", "flightctl-agent", "--no-pager", "-o", "cat", "-n", "200"})
		if err != nil {
			return "", fmt.Errorf("get agent logs fallback: %w", err)
		}
		if exitCode != 0 {
			return "", fmt.Errorf("get agent logs: exit code %d", exitCode)
		}
	}
	var buf strings.Builder
	if _, err := io.Copy(&buf, reader); err != nil {
		return "", fmt.Errorf("read agent logs: %w", err)
	}
	return filterPackageModeAgentLogNoise(buf.String()), nil
}

func filterPackageModeAgentLogNoise(logs string) string {
	var out strings.Builder
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "█") || strings.Contains(line, "▀") {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}
