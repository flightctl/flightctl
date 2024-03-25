package vm

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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
	SSHIdentity   string
	SSHPort       int
	Cmd           []string
	RemoveVm      bool
	Background    bool
	DiskImagePath string
}

type BootcVM interface {
	Run(RunVMParameters) error
	ForceDelete() error
	Shutdown() error
	Delete() error
	IsRunning() (bool, error)
	WaitForSSHToBeReady() error
	RunSSH([]string) error
	Exists() (bool, error)
}

type BootcVMCommon struct {
	vmName        string
	diskImagePath string
	vmUsername    string
	sshIdentity   string
	sshPort       int
	removeVm      bool
	background    bool
	cmd           []string
	pidFile       string
	imageID       string
	noCredentials bool
	hasCloudInit  bool
	cloudInitDir  string
	cloudInitArgs string
	testDir       string
}

type BootcVMConfig struct {
	SshPort     int    `json:"SshPort"`
	SshIdentity string `json:"SshPriKey"`
	Repository  string `json:"Repository"`
	Tag         string `json:"Tag"`
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

	key, err := os.ReadFile(v.sshIdentity)
	if err != nil {
		return fmt.Errorf("failed to read private key file: %s\n", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to parse private key: %s\n", err)
	}

	config := &ssh.ClientConfig{
		User: v.vmUsername,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         1 * time.Second,
	}

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
func (v *BootcVMCommon) RunSSH(inputArgs []string) error {

	sshDestination := v.vmUsername + "@localhost"
	port := strconv.Itoa(v.sshPort)

	args := []string{"-i", v.sshIdentity, "-p", port, sshDestination,
		"-o", "IdentitiesOnly=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR", "-o", "SetEnv=LC_ALL="}
	if len(inputArgs) > 0 {
		args = append(args, inputArgs...)
	} else {
		fmt.Printf("Connecting to vm %s. To close connection, use `~.` or `exit`\n", v.imageID)
	}

	cmd := exec.Command("ssh", args...)

	logrus.Debugf("Running ssh command: %s", cmd.String())

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

func (b *BootcVMCommon) oemString() (string, error) {
	tmpFilesCmd, err := b.tmpFileInjectSshKeyEnc()
	if err != nil {
		return "", err
	}
	oemString := fmt.Sprintf("type=11,value=io.systemd.credential.binary:tmpfiles.extra=%s", tmpFilesCmd)
	return oemString, nil
}

func (b *BootcVMCommon) tmpFileInjectSshKeyEnc() (string, error) {
	pubKey, err := os.ReadFile(b.sshIdentity + ".pub")
	if err != nil {
		return "", err
	}
	pubKeyEnc := base64.StdEncoding.EncodeToString(pubKey)

	userHomeDir := "/root"
	if b.vmUsername != "root" {
		userHomeDir = filepath.Join("/home", b.vmUsername)
	}

	tmpFileCmd := fmt.Sprintf("d %[1]s/.ssh 0750 %[2]s %[2]s -\nf+~ %[1]s/.ssh/authorized_keys 700 %[2]s %[2]s - %[3]s", userHomeDir, b.vmUsername, pubKeyEnc)

	tmpFileCmdEnc := base64.StdEncoding.EncodeToString([]byte(tmpFileCmd))
	return tmpFileCmdEnc, nil
}
