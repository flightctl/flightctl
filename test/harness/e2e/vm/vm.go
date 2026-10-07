package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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

const (
	// EnvLibvirtURI selects the libvirt connection for pool guests. Direct NewVM
	// callers stay on qemu:///session unless they set LibvirtUri themselves.
	// Example: qemu+ssh://kni@192.168.122.1/system?keyfile=/home/kni/.ssh/id_rsa
	EnvLibvirtURI = "E2E_LIBVIRT_URI"
	// EnvVMNetwork attaches pool guests to this libvirt network (bridged/LAN)
	// instead of QEMU user-net. When set, tests SSH to the guest DHCP address on port 22.
	EnvVMNetwork = "E2E_VM_NETWORK"
	// EnvVMDiskDir is the directory for overlay disks and cloud-init ISOs. When guests
	// run on a remote hypervisor this must be a path qemu on that host can open
	// (for example an NFS mount of /var/lib/libvirt/images/flightctl-e2e).
	EnvVMDiskDir = "E2E_VM_DISK_DIR"
	// EnvVMSSHKnownHosts, when set, is the known_hosts file for bridged guest SSH.
	// The harness creates the file if it is missing. Entries are stored under the
	// VM name, not the DHCP address. The first connection to a new VM records the
	// key; a later connection must present the same key.
	EnvVMSSHKnownHosts = "E2E_VM_SSH_KNOWN_HOSTS"
	// EnvVMSSHInsecure set to 1 disables guest host-key checks for a bridged address.
	// The guest password is then sent to whatever answers. Isolated lab networks only.
	EnvVMSSHInsecure = "E2E_VM_SSH_INSECURE"
	defaultSSHHost   = "127.0.0.1"
	bridgedSSHPort   = 22
)

var guestSSHInsecureWarned bool

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
	// SSHHost is the address the test process uses to reach guest sshd.
	// Nested e2e uses 127.0.0.1 (QEMU user-net hostfwd). Bridged guests use the
	// DHCP address discovered from libvirt after boot.
	SSHHost string
	SSHPort int
	// NetworkName is a libvirt network to attach (virtio). Empty keeps QEMU user-net.
	NetworkName string
	// NvramPath is the OVMF vars file for UEFI guests. Empty lets libvirt
	// create NVRAM from the firmware descriptor.
	NvramPath      string
	Cmd            []string
	RemoveVm       bool
	pidFile        string
	hasCloudInit   bool
	cloudInitArgs  string
	MemoryFilePath string // Path for external snapshot memory file
	MemoryMiB      int    // VM memory in MiB; 0 means use default (2048)
	DiskSizeGB     int
	TPMDevice      string // Host TPM device path for passthrough (e.g., /dev/tpmrm0); empty uses swtpm emulator
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
	// GuestSSHEndpoint is where the test process reaches guest sshd, plus the
	// host-key options for that address.
	GuestSSHEndpoint() (host string, port int, hostKeyArgs []string, err error)
	RunAndWaitForSSH() error
	SSHCommand(inputArgs []string) *exec.Cmd
	SSHCommandWithUser(nputArgs []string, user string) *exec.Cmd
	RunSSH(inputArgs []string, stdin *bytes.Buffer) (*bytes.Buffer, error)
	RunSSHContext(ctx context.Context, inputArgs []string, stdin *bytes.Buffer) (*bytes.Buffer, error)
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
	// Lines, if > 0, passes journalctl -n Lines (most recent N entries matching other filters).
	Lines int
}

func (v *TestVM) WaitForSSHToBeReady() error {
	timeout := v.SSHWaitTimeout
	if timeout <= 0 {
		timeout = sshWaitTimeout
	}
	deadline := time.Now().Add(timeout)
	sshAddr := fmt.Sprintf("%s:%d", v.sshHost(), v.SSHPort)
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
	// Prefer 127.0.0.1 over localhost (localhost can close the connection during handshake).
	host := v.sshHost()
	sshDestination := user + "@" + host
	port := strconv.Itoa(v.SSHPort)

	hostKeyArgs, err := v.sshHostKeyArgs(host)
	if err != nil {
		logrus.Error(err)
		// Fail before opening a connection or placing the guest password in the environment.
		script := fmt.Sprintf("printf '%%s\\n' %q >&2; exit 1", err.Error())
		return exec.CommandContext(ctx, "sh", "-c", script) // #nosec G204 - fixed shell and a quoted error string
	}

	// Common SSH args
	sshArgs := []string{"-p", port, sshDestination}
	sshArgs = append(sshArgs, hostKeyArgs...)
	sshArgs = append(sshArgs, "-o", "LogLevel=ERROR", "-o", "SetEnv=LC_ALL=")
	if len(inputArgs) > 0 {
		// Non-interactive remote commands: no TTY so motd/profile noise stays off stdout.
		sshArgs = append(sshArgs, "-T")
	}

	var cmd *exec.Cmd
	if v.SSHPrivateKeyPath != "" {
		// Key-based authentication
		sshArgs = append([]string{"-i", string(v.SSHPrivateKeyPath), "-o", "PasswordAuthentication=no"}, sshArgs...)
		cmd = exec.CommandContext(ctx, "ssh", append(sshArgs, inputArgs...)...) // #nosec G204 - test code with controlled inputs
	} else {
		// Password-based authentication with sshpass. Pass the password via the
		// SSHPASS environment variable (sshpass -e) rather than on the command line
		// (sshpass -p): the latter puts the credential in argv, where it lands in the
		// process table and in every debug log of cmd.String() below.
		sshArgs = append([]string{"-o", "PubkeyAuthentication=no"}, sshArgs...)
		cmd = exec.CommandContext(ctx, "sshpass", append([]string{"-e", "ssh"}, append(sshArgs, inputArgs...)...)...) // #nosec G204 - test code with controlled inputs
		cmd.Env = append(os.Environ(), "SSHPASS="+v.SSHPassword)
	}

	if len(inputArgs) == 0 {
		logrus.Infof("Connecting to vm %s. To close connection, use `~.` or `exit`", v.VMName)
	}

	logrus.Debugf("Running ssh command: %s", cmd.String())
	return cmd
}

func (v *TestVM) sshHost() string {
	if v.SSHHost != "" {
		return v.SSHHost
	}
	return defaultSSHHost
}

// GuestSSHEndpoint reports the address and host-key options for guest sshd.
// A loopback target is the nested QEMU port forward. A bridged target is the
// guest DHCP address.
func (v *TestVM) GuestSSHEndpoint() (host string, port int, hostKeyArgs []string, err error) {
	host = v.sshHost()
	port = v.SSHPort
	hostKeyArgs, err = v.sshHostKeyArgs(host)
	return host, port, hostKeyArgs, err
}

// sshHostKeyArgs selects host-key checking for guest SSH.
// A loopback target is the local QEMU user-net forward, so the guest host key is not checked.
// A bridged address records the key on first use. A new VM has no known_hosts entry until that connection.
// E2E_VM_SSH_INSECURE=1 disables the check.
func (v *TestVM) sshHostKeyArgs(host string) ([]string, error) {
	if sshHostIsLoopback(host) {
		return []string{
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "StrictHostKeyChecking=no",
		}, nil
	}
	if os.Getenv(EnvVMSSHInsecure) == "1" {
		if !guestSSHInsecureWarned {
			guestSSHInsecureWarned = true
			logrus.Warnf("%s=1: guest SSH host-key checking is disabled for %s; the guest password is sent to whatever answers. Use only on an isolated lab network.", EnvVMSSHInsecure, host)
		}
		return []string{
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "GlobalKnownHostsFile=/dev/null",
			"-o", "StrictHostKeyChecking=no",
		}, nil
	}
	knownHosts, err := v.guestKnownHostsPath()
	if err != nil {
		return nil, err
	}
	args := []string{
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if v.VMName != "" {
		args = append(args, "-o", "HostKeyAlias="+v.VMName)
	}
	return args, nil
}

// guestKnownHostsPath returns the file that stores bridged guest host keys.
// A new VM has no entry yet. The file is created empty so the first SSH can record the key.
func (v *TestVM) guestKnownHostsPath() (string, error) {
	path := v.guestKnownHostsFile()
	createParent := strings.TrimSpace(os.Getenv(EnvVMSSHKnownHosts)) == ""
	if err := ensureKnownHostsFile(path, createParent); err != nil {
		if createParent {
			return "", fmt.Errorf("create guest known_hosts file: %w", err)
		}
		return "", fmt.Errorf("%s: %w", EnvVMSSHKnownHosts, err)
	}
	return path, nil
}

// guestKnownHostsFile is the known_hosts path for this VM. It does not create the file.
func (v *TestVM) guestKnownHostsFile() string {
	if path := strings.TrimSpace(os.Getenv(EnvVMSSHKnownHosts)); path != "" {
		return path
	}
	dir := v.TestDir
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, v.guestHostKeyAlias()+".known_hosts")
}

func (v *TestVM) guestHostKeyAlias() string {
	if v.VMName != "" {
		return v.VMName
	}
	return "guest"
}

// ForgetGuestHostKey drops a saved bridged host key for this VM.
// A new fresh overlay generates new sshd host keys. accept-new records a missing
// key and rejects a different key stored under the same VM name.
func (v *TestVM) ForgetGuestHostKey() error {
	if os.Getenv(EnvVMSSHInsecure) == "1" {
		return nil
	}
	path := v.guestKnownHostsFile()
	if err := removeKnownHostAlias(path, v.guestHostKeyAlias()); err != nil {
		return fmt.Errorf("forget guest host key for %s: %w", v.guestHostKeyAlias(), err)
	}
	return nil
}

// ensureKnownHostsFile creates path as an empty file when it does not exist.
// createParent also creates the parent directory. An existing file is left unchanged.
func ensureKnownHostsFile(path string, createParent bool) error {
	if createParent {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

// removeKnownHostAlias deletes known_hosts lines for alias. A missing file is unchanged.
func removeKnownHostAlias(path, alias string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read guest known_hosts: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat guest known_hosts: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		if knownHostLineHasAlias(line, alias) {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return nil
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), info.Mode().Perm()); err != nil {
		return fmt.Errorf("write guest known_hosts: %w", err)
	}
	return nil
}

// knownHostLineHasAlias reports whether a known_hosts line is stored for alias.
func knownHostLineHasAlias(line, alias string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || alias == "" {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	host := fields[0]
	if strings.HasPrefix(host, "@") {
		if len(fields) < 2 {
			return false
		}
		host = fields[1]
	}
	for _, name := range strings.Split(host, ",") {
		if name == alias {
			return true
		}
	}
	return false
}

// sshHostIsLoopback reports whether host is the local QEMU user-net forward.
func sshHostIsLoopback(host string) bool {
	host = strings.TrimSpace(host)
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// firstGuestIPv4 returns the first non-loopback IPv4 from libvirt interface addresses.
func firstGuestIPv4(ifaces []guestIfaceAddrs) string {
	for _, iface := range ifaces {
		if strings.HasPrefix(iface.Name, "lo") {
			continue
		}
		for _, addr := range iface.Addrs {
			if addr == "" || strings.HasPrefix(addr, "127.") {
				continue
			}
			if strings.Contains(addr, ":") {
				continue
			}
			return addr
		}
	}
	return ""
}

type guestIfaceAddrs struct {
	Name  string
	Addrs []string
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

// RunSSHContext runs a command over SSH using the VM's default user and the provided context.
func (v *TestVM) RunSSHContext(ctx context.Context, inputArgs []string, stdin *bytes.Buffer) (*bytes.Buffer, error) {
	return v.runSSHWithUserContext(ctx, inputArgs, stdin, v.VMUser)
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
	if opts.Lines > 0 {
		args = append(args, "-n", strconv.Itoa(opts.Lines))
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

func diskTargetXML(_ string) string {
	// bootc/CS9 initramfs waits on the root UUID via virtio-blk.
	return `<target bus="virtio" dev="vda"/>`
}

func cpuXML(networkName string) string {
	if networkName != "" {
		return `<cpu mode='host-model'/>`
	}
	return `<cpu mode='custom' check='none'>
    <model>Haswell-noTSX-IBRS</model>
    <feature name='vmx' policy='optional'/>
    <feature name='svm' policy='optional'/>
  </cpu>`
}

func networkDeviceXML(networkName string) string {
	if networkName == "" {
		return ""
	}
	return fmt.Sprintf(`<interface type='network'>
      <source network='%s'/>
      <model type='virtio'/>
      <rom enabled='no'/>
    </interface>`, networkName)
}

// tpmXML is the guest TPM device. An empty tpmDevice uses the software emulator.
// A path uses that host device as a passthrough TPM. The libvirt network does not change this.
func tpmXML(tpmDevice string) string {
	if tpmDevice != "" {
		return fmt.Sprintf(`<tpm model='tpm-tis'>
      <backend type='passthrough'>
        <device path='%s'/>
      </backend>
    </tpm>`, tpmDevice)
	}
	return `<tpm model='tpm-tis'>
      <backend type='emulator' version='2.0'>
        <active_pcr_banks>
            <sha256/>
        </active_pcr_banks>
      </backend>
    </tpm>`
}

// osXML is the guest firmware. A libvirt network does not change it: bridged
// guests keep the same EFI secure-boot configuration as nested guests.
func osXML(nvramPath string) string {
	return fmt.Sprintf(`<os firmware='efi'>
    <type machine='q35'>hvm</type>
    <bootmenu enable='no'/>
    <firmware>
      <feature enabled='yes' name='secure-boot'/>
      <feature enabled='no' name='enrolled-keys'/>
    </firmware>
    %s
  </os>`, nvramXML(nvramPath))
}

func qemuCommandline(networkName string, sshPort int) string {
	if networkName != "" {
		return ""
	}
	return fmt.Sprintf(`<qemu:commandline>
    <qemu:arg value='-netdev'/>
    <qemu:arg value='user,id=n0,hostfwd=tcp::%d-:22'/>
    <qemu:arg value='-device' />
    <qemu:arg value='virtio-net-pci,netdev=n0,bus=pcie.0,addr=0x10' />
  </qemu:commandline>`, sshPort)
}

func nvramXML(path string) string {
	if path == "" {
		return ""
	}
	return fmt.Sprintf("<nvram>%s</nvram>", path)
}

func applyVMDefaults(params *TestVM) {
	if params.LibvirtUri == "" {
		params.LibvirtUri = "qemu:///session"
	}
	if params.NetworkName != "" {
		params.SSHPort = bridgedSSHPort
	}
}

// ApplyPoolRemoteDefaults copies E2E_LIBVIRT_URI and E2E_VM_NETWORK onto params
// when those fields are empty. The VM pool calls this before NewVM. Direct
// callers keep local qemu:///session and user-net unless they set the fields.
func ApplyPoolRemoteDefaults(params *TestVM) {
	if params.LibvirtUri == "" {
		params.LibvirtUri = strings.TrimSpace(os.Getenv(EnvLibvirtURI))
	}
	if params.NetworkName == "" {
		params.NetworkName = strings.TrimSpace(os.Getenv(EnvVMNetwork))
	}
}

// requireRemoteDiskReachable rejects a remote libvirt URI whose guest files
// are not under E2E_VM_DISK_DIR. QEMU on the remote host opens those paths
// directly, so a local temp disk cannot be used.
func requireRemoteDiskReachable(params *TestVM) error {
	if !libvirtURIIsRemote(params.LibvirtUri) {
		return nil
	}
	root, err := sharedDiskRoot()
	if err != nil {
		return fmt.Errorf("libvirt URI %q runs qemu on another host: %w", params.LibvirtUri, err)
	}
	checks := []struct {
		name string
		path string
	}{
		{"disk image", params.DiskImagePath},
		{"test directory", params.TestDir},
		{"memory file", params.MemoryFilePath},
		{"NVRAM file", params.NvramPath},
	}
	for _, check := range checks {
		if check.path == "" {
			continue
		}
		if !pathWithin(root, check.path) {
			return fmt.Errorf("libvirt URI %q runs qemu on another host, but %s %q is not under %s (%s); place guest files on that shared directory or leave the URI unset to use local qemu:///session", params.LibvirtUri, check.name, check.path, EnvVMDiskDir, root)
		}
	}
	return nil
}

// libvirtURIIsRemote reports whether uri runs QEMU on another host.
// qemu:///session and qemu:///system are local. A query string is ignored.
func libvirtURIIsRemote(uri string) bool {
	base := uri
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		base = uri[:i]
	}
	switch base {
	case "", "qemu:///session", "qemu:///system":
		return false
	default:
		return true
	}
}

// sharedDiskRoot returns E2E_VM_DISK_DIR when it is an absolute path with no ".." segments.
func sharedDiskRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv(EnvVMDiskDir))
	if root == "" {
		return "", fmt.Errorf("%s is unset", EnvVMDiskDir)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%s must be an absolute path: %q", EnvVMDiskDir, root)
	}
	for _, seg := range strings.Split(filepath.ToSlash(root), "/") {
		if seg == ".." {
			return "", fmt.Errorf("%s must not contain '..' path segments: %q", EnvVMDiskDir, root)
		}
	}
	cleaned := filepath.Clean(root)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("%s must be an absolute path: %q", EnvVMDiskDir, root)
	}
	return cleaned, nil
}

// pathWithin reports whether target is root or a path under root.
func pathWithin(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if !filepath.IsAbs(root) || !filepath.IsAbs(target) {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func StartAndWaitForSSH(params TestVM) (vm TestVMInterface, err error) {
	vm, err = NewVM(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create new VM: %w", err)
	}

	return vm, vm.RunAndWaitForSSH()
}
