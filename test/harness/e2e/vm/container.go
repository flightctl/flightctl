package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// containerReadyTimeout bounds how long ContainerDevice waits for flightctl-agent.service to
// report active. Unlike the VM path there's no OS boot/SSH-daemon startup to wait through -
// systemd is already PID 1 the instant the container is running - so this only needs to cover
// systemd unit activation, not a cold boot.
const containerReadyTimeout = 90 * time.Second

// containerStopTimeout bounds how long Shutdown waits for the container to stop gracefully
// before the caller falls back to ForceDelete.
const containerStopTimeout = 10 * time.Second

// ErrUnsupported is returned by ContainerDevice operations that have no meaningful container
// equivalent (e.g. Pause/Resume, which VMs support via hypervisor suspend but plain containers don't).
var ErrUnsupported = errors.New("operation not supported for container-backed devices")

// ContainerFile describes a file to place inside the device container before it starts,
// mirroring what inject_agent_files_into_qcow.sh writes into the qcow2 for VM-backed devices
// (agent config/certs, registry CA, registries.conf.d remap). Set exactly one of HostPath
// (copy an existing file) or Content (write generated content directly, e.g. the registry remap,
// which has no standalone host-side file to point at - see container_pool.go).
type ContainerFile struct {
	HostPath      string
	Content       []byte
	ContainerPath string
	// Mode defaults to 0644 when zero.
	Mode int64
}

// ContainerDeviceConfig configures a ContainerDevice.
type ContainerDeviceConfig struct {
	// Name is the container name; must be unique per worker.
	Name string
	// Image is the flightctl-agent bootc image to run - the same image used to build the e2e
	// qcow2 for this OS variant (see test/scripts/agent-images/containerfiles).
	Image string
	// Files are copied into the container before it starts.
	Files []ContainerFile
}

// ContainerDevice implements TestVMInterface by running the flightctl-agent bootc image as a
// plain container (via testcontainers-go) instead of a libvirt VM. It's a drop-in device backend
// for suites that never switch the device's OS image or reboot it - see test/harness/e2e/container_pool.go
// for which suites qualify and how instances are configured.
//
// Container lifecycle (start/wait/stop) goes through testcontainers-go, matching every other aux
// container in this test suite (test/e2e/infra/auxiliary). Command execution instead shells out to
// `<runtime> exec` directly rather than using testcontainers' own Container.Exec, which has no
// stdin support - RunSSH callers that pipe stdin (e.g. writing a file's contents) keep working
// unchanged this way, same as VMInLibvirt shelling out to the real ssh(1)/sshpass(1) binaries.
type ContainerDevice struct {
	cfg ContainerDeviceConfig

	mu        sync.Mutex
	container testcontainers.Container
	started   bool
}

var _ TestVMInterface = (*ContainerDevice)(nil)

// NewContainerDevice creates a ContainerDevice. Call Run (or RunAndWaitForSSH) to start it.
func NewContainerDevice(cfg ContainerDeviceConfig) *ContainerDevice {
	return &ContainerDevice{cfg: cfg}
}

// nestedPodmanStorageConf overrides the device's own /etc/containers/storage.conf so podman
// running *inside* this container (the flightctl-agent's own image pulls/prefetch, and nested
// podman for quadlet-app suites) doesn't try to layer the "overlay" graph driver on top of a
// filesystem podman/Docker have themselves already presented as overlayfs (which containers/storage
// rejects outright: "'overlay' is not supported over overlayfs, a mount_program is required").
// On a real VM the qcow2 root filesystem is a plain block device (ext4/xfs/btrfs), so this problem
// doesn't exist there and only container-backed devices need the override. fuse-overlayfs ships in
// the same centos-bootc base image this device image is built from (see
// test/scripts/agent-images/containerfiles), so no extra dependency is introduced.
const nestedPodmanStorageConf = `[storage]
driver = "overlay"

[storage.options.overlay]
mount_program = "/usr/bin/fuse-overlayfs"
`

func (c *ContainerDevice) buildRequest() testcontainers.ContainerRequest {
	files := make([]testcontainers.ContainerFile, 0, len(c.cfg.Files)+1)
	files = append(files, testcontainers.ContainerFile{
		ContainerFilePath: "/etc/containers/storage.conf",
		FileMode:          0644,
		Reader:            strings.NewReader(nestedPodmanStorageConf),
	})
	for _, f := range c.cfg.Files {
		mode := f.Mode
		if mode == 0 {
			mode = 0644
		}
		tcFile := testcontainers.ContainerFile{
			HostFilePath:      f.HostPath,
			ContainerFilePath: f.ContainerPath,
			FileMode:          mode,
		}
		if f.Content != nil {
			tcFile.Reader = bytes.NewReader(f.Content)
		}
		files = append(files, tcFile)
	}
	return testcontainers.ContainerRequest{
		Image: c.cfg.Image,
		Name:  c.cfg.Name,
		Files: files,
		// Privileged: the device runs systemd as PID 1 and, for suites that deploy quadlet
		// apps, a *nested* podman inside that - both need cgroup delegation and capabilities
		// (SYS_ADMIN, etc.) an unprivileged container doesn't get. This is test infrastructure
		// only, not production - the same trade-off "podman-in-podman"/systemd-in-container
		// testing setups generally make. ContainerRequest.Privileged is deprecated in favor of
		// HostConfigModifier, but does the exact same thing.
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Privileged = true
		},
		// Network mode is set separately in Run via containers.WithNetwork - see that comment.
		WaitingFor: wait.ForExec([]string{"systemctl", "is-active", "flightctl-agent.service"}).
			WithStartupTimeout(containerReadyTimeout),
	}
}

// CreateDomain satisfies TestVMInterface. Containers have no meaningful "define without start"
// step (Run creates and starts in one call), so this is a no-op.
func (c *ContainerDevice) CreateDomain() error {
	return nil
}

// Run starts the device container if it isn't already running.
func (c *ContainerDevice) Run() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.container != nil {
		return nil
	}

	ctx := context.Background()
	// A container from a previous, uncleanly-terminated process may still hold this name.
	if containers.ContainerExistsByName(c.cfg.Name) {
		if err := containers.RemoveContainerByName(c.cfg.Name); err != nil {
			logrus.Warnf("failed to remove stale device container %s: %v", c.cfg.Name, err)
		}
	}

	logrus.Infof("Starting device container %s (image %s)", c.cfg.Name, c.cfg.Image)
	// Join the same network aux services use (see containers.GetDockerNetwork), not a hardcoded
	// "host" network: many devices run concurrently (one per Ginkgo worker, across LPT shards on
	// the same runner), and each one's *nested* podman (for suites that deploy quadlet apps) may
	// publish a fixed, test-hardcoded host port (e.g. 8080) - on host networking that port would
	// collide across devices sharing the runner's network namespace, unlike VMs which each get a
	// fully isolated network namespace for free. A dedicated bridge/kind network gives each
	// device that same per-device isolation; aux services (registry, etc.) stay reachable via
	// their host-published ports either way (see containers.GetHostIP - same mechanism VMs use).
	ct, err := containers.GenericStart(ctx, c.buildRequest(), false,
		containers.WithNetwork(containers.GetDockerNetwork()), containers.WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start device container %s: %w", c.cfg.Name, err)
	}
	c.container = ct
	c.started = true

	if err := c.fixShadowPermissionsLocked(); err != nil {
		logrus.Warnf("failed to fix /etc/shadow permissions in %s (sudo calls may fail): %v", c.cfg.Name, err)
	}

	if err := c.updateCATrustLocked(); err != nil {
		logrus.Warnf("update-ca-trust inside %s failed (registry pulls may fail if a custom CA was injected): %v", c.cfg.Name, err)
	}
	return nil
}

// fixShadowPermissionsLocked works around a sudo/PAM failure specific to running this bootc image
// as a plain container instead of booting it as a real VM: /etc/shadow ships mode 0000 (typical
// for RHEL/CentOS Stream images, relying on callers having the CAP_DAC_READ_SEARCH/CAP_DAC_OVERRIDE
// effective capability to read it), but sudo's account phase reads it through the setuid-root
// unix_chkpwd helper, whose effective capability set is computed fresh from its own file
// capabilities on exec - not inherited from the caller - so it ends up with none, regardless of
// this container being --privileged (bounding-set capabilities from --privileged don't propagate
// into a setuid exec's effective set either). The result is every "sudo ..." call (the same ones
// the VM/SSH path already runs unmodified as the non-root "user" account) failing with "PAM
// account management error: Authentication service cannot retrieve authentication info" before
// even reaching the command. See https://github.com/linux-pam/linux-pam/issues/876. Loosening
// /etc/shadow to normal owner-read permissions (as most non-RHEL distros ship it) sidesteps the
// capability requirement entirely. Must be called with c.mu held and c.container set.
func (c *ContainerDevice) fixShadowPermissionsLocked() error {
	_, err := c.runExecWithUserContext(context.Background(), []string{"chmod", "0600", "/etc/shadow"}, nil, "")
	return err
}

// updateCATrustLocked runs update-ca-trust inside the container when a CA anchor file was
// injected, mirroring inject_agent_files_into_qcow.sh's flightctl-update-ca-trust.service (which
// only exists for the qcow2/VM path). Must be called with c.mu held and c.container set.
func (c *ContainerDevice) updateCATrustLocked() error {
	hasCA := false
	for _, f := range c.cfg.Files {
		if strings.Contains(f.ContainerPath, "ca-trust/source/anchors") {
			hasCA = true
			break
		}
	}
	if !hasCA {
		return nil
	}
	_, err := c.runExecWithUserContext(context.Background(), []string{"update-ca-trust"}, nil, "")
	return err
}

// RunAndWaitForSSH starts the container (if needed) and waits until command execution is ready.
func (c *ContainerDevice) RunAndWaitForSSH() error {
	c.mu.Lock()
	running := c.container != nil
	c.mu.Unlock()
	if !running {
		if err := c.Run(); err != nil {
			return err
		}
	}
	return c.WaitForSSHToBeReady()
}

// WaitForSSHToBeReady polls until exec into the container succeeds. Run's WaitingFor strategy
// already blocks until flightctl-agent.service is active, so this is normally near-instant; it
// exists mainly for callers (e.g. after RevertToSnapshot) that call it independently.
func (c *ContainerDevice) WaitForSSHToBeReady() error {
	deadline := time.Now().Add(containerReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := c.runExecWithUserContext(probeCtx, []string{"true"}, nil, "")
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	if lastErr != nil {
		return fmt.Errorf("device container %s did not become ready: %w", c.cfg.Name, lastErr)
	}
	return fmt.Errorf("device container %s did not become ready", c.cfg.Name)
}

func (c *ContainerDevice) getContainer() (testcontainers.Container, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.container, c.container != nil
}

// Exists reports whether the container currently exists (running or stopped).
func (c *ContainerDevice) Exists() (bool, error) {
	return containers.ContainerExistsByName(c.cfg.Name), nil
}

// IsRunning reports whether the container exists and is running.
func (c *ContainerDevice) IsRunning() (bool, error) {
	return containers.ContainerRunningByName(c.cfg.Name), nil
}

// Shutdown stops the container without removing it.
func (c *ContainerDevice) Shutdown() error {
	ct, ok := c.getContainer()
	if !ok {
		return nil
	}
	timeout := containerStopTimeout
	if err := ct.Stop(context.Background(), &timeout); err != nil {
		return fmt.Errorf("failed to stop device container %s: %w", c.cfg.Name, err)
	}
	return nil
}

// Delete removes the container. Alias of ForceDelete - containers have no separate
// "graceful shutdown, then undefine" distinction worth modeling.
func (c *ContainerDevice) Delete() error {
	return c.ForceDelete()
}

// ForceDelete stops and removes the container.
func (c *ContainerDevice) ForceDelete() error {
	c.mu.Lock()
	ct := c.container
	c.container = nil
	c.started = false
	c.mu.Unlock()

	if ct == nil {
		if containers.ContainerExistsByName(c.cfg.Name) {
			return containers.RemoveContainerByName(c.cfg.Name)
		}
		return nil
	}
	if err := ct.Terminate(context.Background()); err != nil {
		return fmt.Errorf("failed to terminate device container %s: %w", c.cfg.Name, err)
	}
	return nil
}

// RevertToSnapshot resets the device to a pristine state. Containers have no snapshot mechanism
// worth modeling (see CreateSnapshot) - a freshly created container from the same image already
// IS the pristine state, so this just recreates the container from scratch. The name argument is
// accepted only to satisfy TestVMInterface; ContainerDevice supports exactly one "snapshot".
func (c *ContainerDevice) RevertToSnapshot(_ string) error {
	c.mu.Lock()
	ct := c.container
	c.container = nil
	c.started = false
	c.mu.Unlock()

	if ct != nil {
		if err := ct.Terminate(context.Background()); err != nil {
			logrus.Warnf("failed to terminate device container %s before revert: %v", c.cfg.Name, err)
		}
	} else if containers.ContainerExistsByName(c.cfg.Name) {
		if err := containers.RemoveContainerByName(c.cfg.Name); err != nil {
			logrus.Warnf("failed to remove stale device container %s before revert: %v", c.cfg.Name, err)
		}
	}
	return c.Run()
}

// CreateSnapshot is a no-op: RevertToSnapshot always recreates the container from the same image
// rather than reverting to a captured state, so there's nothing to capture here.
func (c *ContainerDevice) CreateSnapshot(_ string) error {
	return nil
}

// DeleteSnapshot is a no-op; see CreateSnapshot.
func (c *ContainerDevice) DeleteSnapshot(_ string) error {
	return nil
}

// HasSnapshot reports whether a pristine revert is possible, which for containers is simply
// whether the device has been started successfully at least once.
func (c *ContainerDevice) HasSnapshot(_ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started, nil
}

// Pause is unsupported for container-backed devices (see ErrUnsupported).
func (c *ContainerDevice) Pause() error {
	return ErrUnsupported
}

// Resume is unsupported for container-backed devices (see ErrUnsupported).
func (c *ContainerDevice) Resume() error {
	return ErrUnsupported
}

// GetConsoleOutput returns the container's full logs. Unlike VMInLibvirt's incremental console
// stream, podman/docker logs are already persisted and can be refetched in full on each call, so
// no incremental buffering is needed here.
func (c *ContainerDevice) GetConsoleOutput() string {
	ct, ok := c.getContainer()
	if !ok {
		return ""
	}
	rc, err := ct.Logs(context.Background())
	if err != nil {
		logrus.Warnf("failed to fetch logs for device container %s: %v", c.cfg.Name, err)
		return ""
	}
	defer rc.Close()
	buf, err := io.ReadAll(rc)
	if err != nil {
		logrus.Warnf("failed to read logs for device container %s: %v", c.cfg.Name, err)
	}
	return string(buf)
}

// EnsureConsoleStream satisfies TestVMInterface. No persistent stream is needed - see GetConsoleOutput.
func (c *ContainerDevice) EnsureConsoleStream() error {
	return nil
}

func (c *ContainerDevice) JournalLogs(opts JournalOpts) (string, error) {
	return runJournalLogs(c.RunSSH, opts)
}

func (c *ContainerDevice) GetServiceLogs(serviceName string) (string, error) {
	return runServiceLogs(c.RunSSH, serviceName)
}

// runExecWithUserContext execs inputArgs inside the container via the `<runtime> exec` CLI
// (podman/docker), not testcontainers' Container.Exec, so stdin piping works - see ContainerDevice's
// doc comment. user is passed through to `exec -u`; empty runs as the container's default user
// (root for this image, matching the "sudo ..." commands already embedded in every call site
// written for the SSH/VM path - sudo as root is a no-op privilege-wise).
func (c *ContainerDevice) runExecWithUserContext(ctx context.Context, inputArgs []string, stdin *bytes.Buffer, user string) (*bytes.Buffer, error) {
	cmd := c.execCommandWithUserContext(ctx, inputArgs, user)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.Join(context.DeadlineExceeded, err)
		}
		return nil, fmt.Errorf("failed to run exec command in device container %s: %w, stderr: %s, stdout: %s", c.cfg.Name, err, stderr.String(), stdout.String())
	}
	return &stdout, nil
}

func (c *ContainerDevice) execCommandWithUserContext(ctx context.Context, inputArgs []string, user string) *exec.Cmd {
	cli := containers.RuntimeCLIName()
	args := []string{"exec"}
	if len(inputArgs) == 0 {
		args = append(args, "-it")
	} else {
		args = append(args, "-i")
	}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, c.cfg.Name)
	if len(inputArgs) == 0 {
		args = append(args, "bash")
	} else {
		// Run through a shell instead of exec'ing inputArgs directly: vm.go's runJournalLogs/
		// runServiceLogs (and plenty of call sites like WaitForFileInDevice) build inputArgs that
		// rely on shell features - command substitution ($(...)), quoting, multi-line scripts -
		// which only work if something interprets them as shell syntax. Over the real-SSH path
		// (TestVM), that's implicit: ssh concatenates multiple command-line arguments with spaces
		// and hands the whole string to the remote login shell (see ssh(1)). `<runtime> exec`
		// has no such implicit shell, so replicate the same concatenate-then-shell-interpret
		// behavior explicitly here rather than exec'ing inputArgs[0] with literal argv strings
		// (which left "$(...)" uninterpreted and quote characters passed through literally -
		// e.g. journalctl receiving a literal `"2026-01-02 15:04:05"` and failing to parse it).
		args = append(args, "sh", "-c", strings.Join(inputArgs, " "))
	}
	return exec.CommandContext(ctx, cli, args...) // #nosec G204 - test code with controlled inputs, mirrors vm.go's ssh command building
}

// SSHCommand returns a command that execs inputArgs inside the container (or opens an interactive
// shell if inputArgs is empty). Named to satisfy TestVMInterface; there is no real SSH involved.
func (c *ContainerDevice) SSHCommand(inputArgs []string) *exec.Cmd {
	return c.execCommandWithUserContext(context.Background(), inputArgs, "")
}

func (c *ContainerDevice) SSHCommandWithUser(inputArgs []string, user string) *exec.Cmd {
	return c.execCommandWithUserContext(context.Background(), inputArgs, user)
}

// RunSSH execs inputArgs inside the container. Named to satisfy TestVMInterface; there is no real
// SSH involved (see ContainerDevice's doc comment).
func (c *ContainerDevice) RunSSH(inputArgs []string, stdin *bytes.Buffer) (*bytes.Buffer, error) {
	return c.RunSSHWithUser(inputArgs, stdin, "")
}

func (c *ContainerDevice) RunSSHWithUser(inputArgs []string, stdin *bytes.Buffer, user string) (*bytes.Buffer, error) {
	return c.runExecWithUserContext(context.Background(), inputArgs, stdin, user)
}
