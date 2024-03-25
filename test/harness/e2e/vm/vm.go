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

type NewVMParameters struct {
	TestDir       string
	VMName        string
	LibvirtUri    string //linux only
	DiskImagePath string
}

type RunVMParameters struct {
	VMUser        string //user to use when connecting to the VM
	CloudInitDir  string
	NoCredentials bool
	CloudInitData bool
	SSHPassword   string
	SSHPort       int
	Cmd           []string
	RemoveVm      bool
	DiskImagePath string
}

type BootcVM interface {
	Run(RunVMParameters) error
	ForceDelete() error
	Shutdown() error
	Delete() error
	IsRunning() (bool, error)
	WaitForSSHToBeReady() error
	SSHCommand(inputArgs []string) *exec.Cmd
	RunSSH(inputArgs []string, stdin *bytes.Buffer) (*bytes.Buffer, error)
	Exists() (bool, error)
	ReadConsole() string
	GetConsoleOutput() string
}

type BootcVMCommon struct {
	vmName        string
	diskImagePath string
	vmUsername    string
	sshPassword   string
	sshPort       int
	removeVm      bool
	background    bool
	cmd           []string
	pidFile       string
	imageID       string
	hasCloudInit  bool
	cloudInitDir  string
	cloudInitArgs string
	testDir       string
}

type BootcVMConfig struct {
	SshPort     int
	SshPassword string
	Repository  string
	Tag         string
}

func (v *BootcVMCommon) SetUser(user string) error {
	if user == "" {
		return fmt.Errorf("user is required")
	}

	v.vmUsername = user
	return nil
}

func (v *BootcVMCommon) WaitForSSHToBeReady() error {
	fmt.Println("Waiting for SSH to be ready")
	timeout := 60 * time.Second
	elapsed := 0 * time.Second

	config := &ssh.ClientConfig{
		User: v.vmUsername,
		Auth: []ssh.AuthMethod{
			ssh.Password(v.sshPassword),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         1 * time.Second,
	}

	logrus.Infof("Waiting for VM SSH to be ready on localhost:%d", v.sshPort)

	for elapsed < timeout {
		client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", "localhost", v.sshPort), config)
		if err != nil {
			logrus.Debugf("failed to connect to SSH server: %s\n", err)
			time.Sleep(1 * time.Second)
			elapsed += 1 * time.Second
		} else {
			client.Close()
			return nil
		}
	}

	return fmt.Errorf("SSH did not become ready in %s seconds", timeout)
}

// RunSSH runs a command over ssh or starts an interactive ssh connection if no command is provided
func (v *BootcVMCommon) SSHCommand(inputArgs []string) *exec.Cmd {

	sshDestination := v.vmUsername + "@localhost"
	port := strconv.Itoa(v.sshPort)

	args := []string{"-p", v.sshPassword, "ssh", "-p", port, sshDestination,
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR", "-o", "SetEnv=LC_ALL="}
	if len(inputArgs) > 0 {
		args = append(args, inputArgs...)
	} else {
		logrus.Infof("Connecting to vm %s. To close connection, use `~.` or `exit`\n", v.imageID)
	}

	cmd := exec.Command("sshpass", args...)

	logrus.Debugf("Running ssh command: %s", cmd.String())
	return cmd
}

func (v *BootcVMCommon) RunSSH(inputArgs []string, stdin *bytes.Buffer) (*bytes.Buffer, error) {
	cmd := v.SSHCommand(inputArgs)
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run ssh command: %w, stderr: %s", err, stderr.String())
	}

	return &stdout, nil
}
