package vm

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
)

const sshWaitTimeout time.Duration = 60 * time.Second

type TestVM struct {
	TestDir        string
	VMName         string
	LibvirtUri     string //linux only
	DiskImagePath  string
	VMUser         string //user to use when connecting to the VM
	CloudInitDir   string
	NoCredentials  bool
	CloudInitData  bool
	SSHPassword    string
	SSHPort        int
	Cmd            []string
	RemoveVm       bool
	pidFile        string
	hasCloudInit   bool
	cloudInitArgs  string
	MemoryFilePath string // Path for external snapshot memory file
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
	Unit  string
	Since string // time string like "20 minutes ago" or empty for all logs
}

func (v *TestVM) WaitForSSHToBeReady() error {
	elapsed := 0 * time.Second

	config := &ssh.ClientConfig{
		User: v.VMUser,
		Auth: []ssh.AuthMethod{
			ssh.Password(v.SSHPassword),
		},
		//nolint:gosec
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         1 * time.Second,
	}

	logrus.Infof("Waiting for VM SSH to be ready on localhost:%d", v.SSHPort)

	for elapsed < sshWaitTimeout {
		client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", "localhost", v.SSHPort), config)
		if err != nil {
			logrus.Debugf("failed to connect to SSH server: %s", err)
			time.Sleep(1 * time.Second)
			elapsed += 1 * time.Second
		} else {
			client.Close()
			return nil
		}
	}

	return fmt.Errorf("SSH did not become ready in %s seconds", sshWaitTimeout)
}

func (v *TestVM) SSHCommandWithUser(inputArgs []string, user string) *exec.Cmd {

	sshDestination := user + "@localhost"
	port := strconv.Itoa(v.SSHPort)

	args := []string{"-p", v.SSHPassword, "ssh", "-p", port, sshDestination,
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "PubkeyAuthentication=no", // avoid any local SSH keys to be used
		"-o", "LogLevel=ERROR", "-o", "SetEnv=LC_ALL="}
	if len(inputArgs) > 0 {
		args = append(args, inputArgs...)
	} else {
		logrus.Infof("Connecting to vm %s. To close connection, use `~.` or `exit`", v.VMName)
	}

	cmd := exec.Command("sshpass", args...)

	logrus.Debugf("Running ssh command: %s", cmd.String())
	return cmd
}

// RunSSH runs a command over ssh or starts an interactive ssh connection if no command is provided
func (v *TestVM) SSHCommand(inputArgs []string) *exec.Cmd {

	return v.SSHCommandWithUser(inputArgs, v.VMUser)
}

func (v *TestVM) RunSSHWithUser(inputArgs []string, stdin *bytes.Buffer, user string) (*bytes.Buffer, error) {
	cmd := v.SSHCommandWithUser(inputArgs, user)
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}

	cmd.Stdout = &stdout
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run ssh command: %w, stderr: %s, stdout: %s", err, stderr.String(), stdout.String())
	}

	return &stdout, nil
}

func (v *TestVM) RunSSH(inputArgs []string, stdin *bytes.Buffer) (*bytes.Buffer, error) {

	stdout, err := v.RunSSHWithUser(inputArgs, stdin, v.VMUser)
	return stdout, err
}

func (v *TestVM) JournalLogs(opts JournalOpts) (string, error) {
	args := []string{"sudo", "journalctl", "--no-pager", "--no-hostname", "--boot=all"}

	if opts.Unit != "" {
		args = append(args, "-u", opts.Unit)
	}
	if opts.Since != "" {
		args = append(args, "--since", fmt.Sprintf("%q", opts.Since))
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
