package vm

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
)

const sshWaitTimeout time.Duration = 60 * time.Second

type TestVM struct {
	TestDir       string
	VMName        string
	LibvirtUri    string //linux only
	DiskImagePath string
	VMUser        string //user to use when connecting to the VM
	CloudInitDir  string
	NoCredentials bool
	CloudInitData bool
	SSHPassword   string
	SSHPort       int
	Cmd           []string
	RemoveVm      bool
	pidFile       string
	hasCloudInit  bool
	cloudInitArgs string
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

func StartAndWaitForSSH(params TestVM) (vm TestVMInterface, err error) {
	vm, err = NewVM(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create new VM: %w", err)
	}

	return vm, vm.RunAndWaitForSSH()
}
