//go:build linux

package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	PackageModeFlightctlUser      = "flightctl"
	PackageModeRejectMessage      = "package-mode device cannot satisfy spec with os.image"
	packageModeRegistriesConfPath = "/etc/containers/registries.conf.d/flightctl-e2e.conf"
	packageModeRegistryCAPath     = "/etc/pki/ca-trust/source/anchors/flightctl-e2e-registry.crt"

	nestedPodmanStorageConf = `[storage]
driver = "overlay"

[storage.options.overlay]
mount_program = "/usr/bin/fuse-overlayfs"
`
)

var (
	packageModeRegistryHostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
)

// GetPackageModeAgentImage returns the package-mode OCI image reference used by the e2e helper container.
func GetPackageModeAgentImage() string {
	return testutil.NewDeviceImageReference(testutil.DeviceTags.Package).String()
}

// PackageModeAgent represents the package-mode helper container used by the e2e suite.
type PackageModeAgent struct {
	Container testcontainers.Container
}

// StartPackageModeAgent boots the helper container and prepares it to enroll as a package-mode device.
func StartPackageModeAgent(ctx context.Context, agentConfigDir, registryHost, registryPort string) (*PackageModeAgent, error) {
	containers.ConfigureDockerHost()
	network := containers.GetDockerNetwork()

	agentConfigPath := GetAgentConfigPath(agentConfigDir)
	agentCertsDir := GetAgentCertsDir(agentConfigDir)

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
	caInfo, err := os.Stat(caPath)
	if err != nil {
		return nil, fmt.Errorf("e2e registry CA not found at %s: %w", caPath, err)
	}
	if !caInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("e2e registry CA at %s is not a regular file", caPath)
	}
	files = append(files, testcontainers.ContainerFile{
		HostFilePath:      caPath,
		ContainerFilePath: packageModeRegistryCAPath,
		FileMode:          0644,
	})

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
			hc.UsernsMode = "host"
			hc.CgroupnsMode = "host"
			hc.SecurityOpt = append(hc.SecurityOpt,
				"seccomp=unconfined",
				"label=disable",
			)
			hc.Binds = append(hc.Binds,
				"/dev/fuse:/dev/fuse",
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
		terminatePackageModeAgentContainer(ctx, c, "GenericContainer startup failure")
		return nil, fmt.Errorf("start package-mode agent container: %w", err)
	}

	agent := &PackageModeAgent{
		Container: c,
	}

	if err := agent.setupContainerEnvironment(ctx, registryHost, registryPort); err != nil {
		terminatePackageModeAgentContainer(ctx, c, "package-mode environment setup failure")
		return nil, fmt.Errorf("setup package-mode container environment: %w", err)
	}

	if err := agent.waitForAgentService(ctx, 2*time.Minute); err != nil {
		terminatePackageModeAgentContainer(ctx, c, "agent service readiness failure")
		return nil, fmt.Errorf("wait for agent service: %w", err)
	}

	logrus.Info("Package-mode agent container started")
	return agent, nil
}

// setupContainerEnvironment prepares the package-mode helper container to run the agent and nested podman workloads.
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

// disableFirewalld stops firewalld when the image includes it so nested test networking stays predictable.
func (a *PackageModeAgent) disableFirewalld(ctx context.Context) error {
	exitCode, _, err := a.execArgs(ctx, []string{"systemctl", "cat", "firewalld.service"})
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return nil
	}
	return a.execOK(ctx, "systemctl disable --now firewalld.service")
}

// setupFlightctlUser creates the rootless workload user and runtime directories required by package-mode app tests.
func (a *PackageModeAgent) setupFlightctlUser(ctx context.Context) error {
	packageModeUser := PackageModeFlightctlUser
	commands := []string{
		fmt.Sprintf("id -u %s >/dev/null 2>&1 || useradd --create-home --user-group %s", packageModeUser, packageModeUser),
		fmt.Sprintf("loginctl enable-linger %s >/dev/null 2>&1 || (mkdir -p /var/lib/systemd/linger && touch /var/lib/systemd/linger/%s)", packageModeUser, packageModeUser),
		fmt.Sprintf("grep -q '^%s:' /etc/subuid || echo '%s:100000:65536' >> /etc/subuid", packageModeUser, packageModeUser),
		fmt.Sprintf("grep -q '^%s:' /etc/subgid || echo '%s:100000:65536' >> /etc/subgid", packageModeUser, packageModeUser),
		fmt.Sprintf("uid=$(id -u %s) && mkdir -p /run/user/$uid && chown %s:%s /run/user/$uid && chmod 700 /run/user/$uid", packageModeUser, packageModeUser, packageModeUser),
		fmt.Sprintf("mkdir -p /home/%s/.config/containers/systemd", packageModeUser),
		fmt.Sprintf("mkdir -p /home/%s/.config/systemd/user", packageModeUser),
		fmt.Sprintf("mkdir -p /home/%s/.local", packageModeUser),
		fmt.Sprintf("chown -R %s:%s /home/%s", packageModeUser, packageModeUser, packageModeUser),
	}
	for _, cmd := range commands {
		if err := a.execOK(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

// setupRegistryAccess configures the nested container runtime to trust and pull from the e2e registry.
func (a *PackageModeAgent) setupRegistryAccess(ctx context.Context, registryHost, registryPort string) error {
	if registryHost == "" || registryPort == "" {
		return fmt.Errorf("registry host and port are required for package-mode agent setup")
	}
	if !packageModeRegistryHostPattern.MatchString(registryHost) {
		return fmt.Errorf("invalid registry host %q", registryHost)
	}
	portValue, err := strconv.Atoi(registryPort)
	if err != nil || portValue < 1 || portValue > 65535 {
		return fmt.Errorf("invalid registry port %q", registryPort)
	}

	registryURL := registryHost + ":" + registryPort
	conf := fmt.Sprintf(`[[registry]]
location = "%s"
insecure = false
`, registryURL)
	registryCertDir := fmt.Sprintf("/etc/containers/certs.d/%s", registryURL)
	registryCertPath := registryCertDir + "/ca.crt"
	if err := a.execOK(ctx, "mkdir -p /etc/containers/registries.conf.d"); err != nil {
		return err
	}
	writeConf := fmt.Sprintf(
		"printf '%%s' %s > %s",
		shellQuote(conf),
		shellQuote(packageModeRegistriesConfPath),
	)
	if err := a.execOK(ctx, writeConf); err != nil {
		return err
	}
	if err := a.execOK(ctx, fmt.Sprintf("test ! -f %s || update-ca-trust", packageModeRegistryCAPath)); err != nil {
		return err
	}
	installCert := fmt.Sprintf(
		"test ! -f %s || (mkdir -p %s && cp %s %s)",
		shellQuote(packageModeRegistryCAPath),
		shellQuote(registryCertDir),
		shellQuote(packageModeRegistryCAPath),
		shellQuote(registryCertPath),
	)
	if err := a.execOK(ctx, installCert); err != nil {
		return err
	}
	logrus.Infof("Configured package-mode agent registry access for %s", registryURL)
	return nil
}

// execOK runs a shell command in the helper container and returns a detailed error if it fails.
func (a *PackageModeAgent) execOK(ctx context.Context, cmd string) error {
	exitCode, out, err := a.execArgs(ctx, []string{"sh", "-c", cmd})
	if err != nil {
		return fmt.Errorf("exec %q: %w", cmd, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("exec %q: exit code %d: %s", cmd, exitCode, out)
	}
	return nil
}

// waitForAgentService polls systemd in the helper container until flightctl-agent is active.
func (a *PackageModeAgent) waitForAgentService(ctx context.Context, timeout time.Duration) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		exitCode, _, err := a.execArgs(timeoutCtx, []string{"systemctl", "is-active", "flightctl-agent"})
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

// ReadFile returns the content of a file from inside the helper container.
func (a *PackageModeAgent) ReadFile(ctx context.Context, path string) (string, error) {
	exitCode, out, err := a.execArgs(ctx, []string{"cat", path})
	if err != nil {
		return "", fmt.Errorf("read file %q: %w", path, err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("read file %q: exit code %d: %s", path, exitCode, out)
	}
	return out, nil
}

// GetEnrollmentID polls the helper container agent logs until it emits an enrollment ID or times out.
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

// Stop terminates the helper container if it is still running.
func (a *PackageModeAgent) Stop(ctx context.Context) error {
	if a.Container != nil {
		return a.Container.Terminate(ctx)
	}
	return nil
}

// GetAgentLogs returns filtered flightctl-agent journal output from the helper container.
func (a *PackageModeAgent) GetAgentLogs(ctx context.Context) (string, error) {
	exitCode, out, err := a.execArgs(ctx, []string{
		"journalctl", "-u", "flightctl-agent", "--no-pager", "-o", "cat", "-n", "2000",
	})
	if err != nil {
		return "", fmt.Errorf("get agent logs: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("get agent logs: exit code %d: %s", exitCode, out)
	}
	return filterPackageModeAgentLogs(out), nil
}

// filterPackageModeAgentLogs keeps the latest relevant agent log lines and strips noisy journal art.
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

// packageModeAgentLogLineUseful matches the log lines that help diagnose enrollment, reconciliation, and app pulls.
func packageModeAgentLogLineUseful(line string) bool {
	switch {
	case strings.Contains(line, "level="),
		strings.Contains(line, "/enroll/"),
		strings.Contains(line, "Waiting for enrollment"),
		strings.Contains(line, "Bootstrap"),
		strings.Contains(line, "Starting Flight"),
		strings.Contains(line, "Spec reconciliation"),
		strings.Contains(line, PackageModeRejectMessage),
		strings.Contains(line, "application"),
		strings.Contains(line, "pull"):
		return true
	default:
		return false
	}
}

// terminatePackageModeAgentContainer best-effort cleans up a partially initialized helper container.
func terminatePackageModeAgentContainer(ctx context.Context, container testcontainers.Container, reason string) {
	if container == nil {
		return
	}
	teardownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := container.Terminate(teardownCtx); err != nil {
		logrus.WithError(err).Warnf("Failed to terminate package-mode agent container after %s", reason)
	}
}

// execArgs runs a command in the helper container, drains its output, and closes the exec stream.
func (a *PackageModeAgent) execArgs(ctx context.Context, args []string) (int, string, error) {
	exitCode, reader, err := a.Container.Exec(ctx, args)
	if err != nil {
		return 0, "", err
	}
	out, readErr := readExecOutput(reader)
	if readErr != nil {
		return exitCode, "", readErr
	}
	return exitCode, out, nil
}

// readExecOutput reads and closes the stream returned by testcontainers Container.Exec.
func readExecOutput(reader io.Reader) (string, error) {
	if reader == nil {
		return "", nil
	}
	if closer, ok := reader.(io.Closer); ok {
		defer func() {
			if err := closer.Close(); err != nil {
				logrus.WithError(err).Debug("Failed to close package-mode exec stream")
			}
		}()
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
