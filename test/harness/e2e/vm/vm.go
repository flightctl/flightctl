package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
)

// SSH timeout increased from 60s to 180s for nested VM environments (OCP)
// where boot time + SSH startup can exceed 60s under resource contention
const sshWaitTimeout time.Duration = 180 * time.Second

// sshProbeAttemptTimeout caps a single RunSSH readiness probe so one hung ssh(1)
// cannot exceed the overall WaitForSSHToBeReady deadline by blocking forever.
const sshProbeAttemptTimeout = 10 * time.Second

type TestVM struct {
	TestDir           string
	VMName            string
	LibvirtUri        string //linux only
	DiskImagePath     string
	VMUser            string //user to use when connecting to the VM
	CloudInitDir      string
	NoCredentials     bool
	CloudInitData     bool
	SSHPassword       string
	SSHPrivateKeyPath util.SSHPrivateKeyPath // Path to SSH private key for key-based auth (alternative to SSHPassword)
	SSHPort           int
	Cmd               []string
	RemoveVm          bool
	pidFile           string
	hasCloudInit      bool
	cloudInitArgs     string
	MemoryFilePath    string // Path for external snapshot memory file
	MemoryMiB         int    // VM memory in MiB; 0 means use default (2048)
	DiskSizeGB        int
	TPMDevice         string // Host TPM device path for passthrough (e.g., /dev/tpmrm0); empty uses swtpm emulator
	// SSHWaitTimeout is how long to wait for SSH to become ready. Zero uses the default (180s).
	// Use a longer value for first-boot VMs (e.g. imagebuild workflow) where cloud-init or sshd may start late.
	SSHWaitTimeout time.Duration
}

type TestVMInterface interface {
	Run() error
	ForceDelete() error
	Shutdown() error
	Delete() error
	IsRunning() (bool, error)
	WaitForSSHToBeReady() error
	RunAndWaitForSSH() error
	SSHCommand(inputArgs []string) *exec.Cmd
	SSHCommandWithUser(nputArgs []string, user string) *exec.Cmd
	RunSSH(inputArgs []string, stdin *bytes.Buffer) (*bytes.Buffer, error)
	RunSSHWithUser(inputArgs []string, stdin *bytes.Buffer, user string) (*bytes.Buffer, error)
	Exists() (bool, error)
	GetConsoleOutput() string
	EnsureConsoleStream() error
	JournalLogs(opts JournalOpts) (string, error)
	GetServiceLogs(serviceName string) (string, error)
	// Snapshot methods for performance optimization
	CreateSnapshot(name string) error
	RevertToSnapshot(name string) error
	DeleteSnapshot(name string) error
	Pause() error
	Resume() error
	HasSnapshot(name string) (bool, error)
	// Domain creation without starting
	CreateDomain() error
}

// JournalOpts collects optional filters.
// Zero values mean "all units" / "start of journal".
type JournalOpts struct {
	Unit     string
	Since    string // time string like "20 minutes ago" or empty for all logs
	LastBoot bool   // false by default, when true restricts logs to current boot
}

func (v *TestVM) WaitForSSHToBeReady() error {
	timeout := v.SSHWaitTimeout
	if timeout <= 0 {
		timeout = sshWaitTimeout
	}
	deadline := time.Now().Add(timeout)
	sshAddr := fmt.Sprintf("127.0.0.1:%d", v.SSHPort)
	logrus.Infof("Waiting for VM SSH to be ready via RunSSH on %s (timeout %s)", sshAddr, timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		probeTimeout := sshProbeAttemptTimeout
		if remaining < probeTimeout {
			probeTimeout = remaining
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		_, err := v.runSSHWithUserContext(probeCtx, []string{"true"}, nil, v.VMUser)
		cancel()
		if err != nil {
			lastErr = err
			logrus.Debugf("RunSSH probe failed: %v", err)
			time.Sleep(time.Second)
			continue
		}
		return nil
	}
	if lastErr != nil {
		return fmt.Errorf("SSH did not become ready in %s: %w", timeout, lastErr)
	}
	return fmt.Errorf("SSH did not become ready in %s", timeout)
}

func (v *TestVM) SSHCommandWithUser(inputArgs []string, user string) *exec.Cmd {
	return v.sshCommandWithUserContext(context.Background(), inputArgs, user)
}

func (v *TestVM) sshCommandWithUserContext(ctx context.Context, inputArgs []string, user string) *exec.Cmd {
	// Use 127.0.0.1 to match WaitForSSHToBeReady (localhost can cause connection closed during handshake).
	sshDestination := user + "@127.0.0.1"
	port := strconv.Itoa(v.SSHPort)

	// Common SSH args
	sshArgs := []string{"-p", port, sshDestination,
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR",
		"-o", "SetEnv=LC_ALL="}

	var cmd *exec.Cmd
	if v.SSHPrivateKeyPath != "" {
		// Key-based authentication
		sshArgs = append([]string{"-i", string(v.SSHPrivateKeyPath), "-o", "PasswordAuthentication=no"}, sshArgs...)
		cmd = exec.CommandContext(ctx, "ssh", append(sshArgs, inputArgs...)...) // #nosec G204 - test code with controlled inputs
	} else {
		// Password-based authentication with sshpass
		sshArgs = append([]string{"-o", "PubkeyAuthentication=no"}, sshArgs...)
		cmd = exec.CommandContext(ctx, "sshpass", append([]string{"-p", v.SSHPassword, "ssh"}, append(sshArgs, inputArgs...)...)...) // #nosec G204 - test code with controlled inputs
	}

	if len(inputArgs) == 0 {
		logrus.Infof("Connecting to vm %s. To close connection, use `~.` or `exit`", v.VMName)
	}

	logrus.Debugf("Running ssh command: %s", cmd.String())
	return cmd
}

// RunSSH runs a command over ssh or starts an interactive ssh connection if no command is provided
func (v *TestVM) SSHCommand(inputArgs []string) *exec.Cmd {

	return v.SSHCommandWithUser(inputArgs, v.VMUser)
}

func (v *TestVM) RunSSHWithUser(inputArgs []string, stdin *bytes.Buffer, user string) (*bytes.Buffer, error) {
	return v.runSSHWithUserContext(context.Background(), inputArgs, stdin, user)
}

func (v *TestVM) runSSHWithUserContext(ctx context.Context, inputArgs []string, stdin *bytes.Buffer, user string) (*bytes.Buffer, error) {
	cmd := v.sshCommandWithUserContext(ctx, inputArgs, user)
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}

	cmd.Stdout = &stdout
	err := cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.Join(context.DeadlineExceeded, err)
		}
		return nil, fmt.Errorf("failed to run ssh command: %w, stderr: %s, stdout: %s", err, stderr.String(), stdout.String())
	}

	return &stdout, nil
}

func (v *TestVM) RunSSH(inputArgs []string, stdin *bytes.Buffer) (*bytes.Buffer, error) {

	stdout, err := v.RunSSHWithUser(inputArgs, stdin, v.VMUser)
	return stdout, err
}

func (v *TestVM) JournalLogs(opts JournalOpts) (string, error) {
	args := []string{"sudo", "TZ=UTC", "journalctl", "--no-pager", "--no-hostname"}

	if opts.Unit != "" {
		args = append(args, "-u", opts.Unit)
	}
	if opts.LastBoot {
		// Add systemd invocation ID to get logs from the latest service invocation
		args = append(args, fmt.Sprintf("_SYSTEMD_INVOCATION_ID=$(systemctl show -p InvocationID --value %s)", opts.Unit))
	} else {
		args = append(args, "--boot=all")
	}

	if opts.Since != "" {
		since := opts.Since
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			since = t.UTC().Format("2006-01-02 15:04:05")
		}
		args = append(args, "--since", fmt.Sprintf("%q", since))
	}

	logrus.Debugf("Reading journal logs with command: %s", strings.Join(args, " "))
	stdout, err := v.RunSSH(args, nil)
	if err != nil {
		return "", fmt.Errorf("failed to read journal logs: %w", err)
	}
	return stdout.String(), nil
}

// GetServiceLogs returns the logs from the specified service using journalctl.
// This method uses the systemd invocation ID to get logs from the latest service invocation.
func (v *TestVM) GetServiceLogs(serviceName string) (string, error) {
	args := []string{
		"sudo",
		"journalctl",
		fmt.Sprintf("_SYSTEMD_INVOCATION_ID=$(systemctl show -p InvocationID --value %s.service)", serviceName),
		"--no-pager",
	}

	logrus.Infof("Reading service logs for %s with command: %s", serviceName, strings.Join(args, " "))
	stdout, err := v.RunSSH(args, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get service logs for %s: %w", serviceName, err)
	}
	return stdout.String(), nil
}

func StartAndWaitForSSH(params TestVM) (vm TestVMInterface, err error) {
	vm, err = NewVM(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create new VM: %w", err)
	}

	return vm, vm.RunAndWaitForSSH()
}
