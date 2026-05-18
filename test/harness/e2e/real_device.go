package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/pkg/ignition"
	"github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// RealDevice provides SSH access and agent provisioning for a physical device.
type RealDevice struct {
	Host       string
	SSHUser    string
	SSHKeyPath string
	SSHPort    int
}

// NewRealDeviceFromEnv creates a RealDevice from environment variables.
// Required: REAL_DEVICE_HOST, REAL_DEVICE_SSH_KEY.
// Optional: REAL_DEVICE_SSH_USER (default root), REAL_DEVICE_SSH_PORT (default 22).
func NewRealDeviceFromEnv() (*RealDevice, error) {
	host := os.Getenv("REAL_DEVICE_HOST")
	if host == "" {
		return nil, fmt.Errorf("REAL_DEVICE_HOST environment variable is required")
	}
	sshKey := os.Getenv("REAL_DEVICE_SSH_KEY")
	if sshKey == "" {
		return nil, fmt.Errorf("REAL_DEVICE_SSH_KEY environment variable is required")
	}

	user := os.Getenv("REAL_DEVICE_SSH_USER")
	if user == "" {
		user = "root"
	}

	port := 22
	if p := os.Getenv("REAL_DEVICE_SSH_PORT"); p != "" {
		var err error
		port, err = strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid REAL_DEVICE_SSH_PORT: %w", err)
		}
	}

	return &RealDevice{
		Host:       host,
		SSHUser:    user,
		SSHKeyPath: sshKey,
		SSHPort:    port,
	}, nil
}

// RunSSH executes a command on the device via SSH.
func (d *RealDevice) RunSSH(args []string, stdin *bytes.Buffer) (*bytes.Buffer, error) {
	sshDest := fmt.Sprintf("%s@%s", d.SSHUser, d.Host)
	sshArgs := []string{
		"-i", d.SSHKeyPath,
		"-p", strconv.Itoa(d.SSHPort),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		sshDest,
	}
	sshArgs = append(sshArgs, args...)

	cmd := exec.Command("ssh", sshArgs...) // #nosec G204 - test code with controlled inputs
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}

	logrus.Debugf("RunSSH: ssh %s", strings.Join(sshArgs, " "))
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ssh command failed: %w, stderr: %s, stdout: %s", err, stderr.String(), stdout.String())
	}
	return &stdout, nil
}

// CopyToDevice copies a local file to the device via scp.
func (d *RealDevice) CopyToDevice(localPath, remotePath string) error {
	dest := fmt.Sprintf("%s@%s:%s", d.SSHUser, d.Host, remotePath)
	cmd := exec.Command("scp", // #nosec G204 - test code
		"-i", d.SSHKeyPath,
		"-P", strconv.Itoa(d.SSHPort),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		localPath, dest,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	logrus.Debugf("CopyToDevice: %s -> %s", localPath, dest)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scp failed: %w, stderr: %s", err, stderr.String())
	}
	return nil
}

// WaitForSSH waits until SSH is reachable on the device.
func (d *RealDevice) WaitForSSH(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := d.RunSSH([]string{"true"}, nil)
		if err == nil {
			return nil
		}
		logrus.Debugf("WaitForSSH: not ready yet: %v", err)
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("SSH not reachable on %s after %s", d.Host, timeout)
}

// GetServiceLogs returns recent journal logs for a systemd service on the device.
func (d *RealDevice) GetServiceLogs(serviceName string) (string, error) {
	stdout, err := d.RunSSH([]string{"sudo", "journalctl", "-u", serviceName, "--no-pager", "-n", "500"}, nil)
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// GetEnrollmentID extracts the enrollment ID from the flightctl-agent service logs.
func (d *RealDevice) GetEnrollmentID() (string, error) {
	logs, err := d.GetServiceLogs("flightctl-agent")
	if err != nil {
		return "", fmt.Errorf("getting agent logs: %w", err)
	}
	id := util.GetEnrollmentIdFromText(logs)
	if id == "" {
		return "", fmt.Errorf("enrollment ID not found in agent logs")
	}
	return id, nil
}

// ProvisionAgent installs flightctl-agent on the device and configures it for enrollment.
// format is "ignition" or "cloud-init" and controls the generated payload format (for logging/auditing).
// In both cases, the actual provisioning is done via SSH.
func (d *RealDevice) ProvisionAgent(h *Harness, format string) error {
	logrus.Infof("Provisioning agent on %s (format: %s)", d.Host, format)

	configYAML, caCert, enrollCert, enrollKey, err := d.generateEnrollmentConfig(h)
	if err != nil {
		return fmt.Errorf("generating enrollment config: %w", err)
	}

	switch format {
	case "ignition":
		payload, err := d.GenerateIgnitionConfig(configYAML, caCert, enrollCert, enrollKey)
		if err != nil {
			return fmt.Errorf("generating ignition config: %w", err)
		}
		logrus.Infof("Generated ignition config (%d bytes)", len(payload))
	case "cloud-init":
		payload, err := d.GenerateCloudInitConfig(configYAML, caCert, enrollCert, enrollKey)
		if err != nil {
			return fmt.Errorf("generating cloud-init config: %w", err)
		}
		logrus.Infof("Generated cloud-init config (%d bytes)", len(payload))
	default:
		logrus.Infof("Using direct SSH provisioning (no config format specified)")
	}

	if err := d.applyProvisioningViaSSH(configYAML, caCert, enrollCert, enrollKey); err != nil {
		return fmt.Errorf("applying provisioning via SSH: %w", err)
	}

	logrus.Infof("Agent provisioned on %s", d.Host)
	return nil
}

func (d *RealDevice) generateEnrollmentConfig(h *Harness) (configYAML, caCert, enrollCert, enrollKey []byte, err error) {
	tmpDir, err := os.MkdirTemp("", "real-device-enrollment-*")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	certDir := tmpDir + "/certs"
	if err := os.MkdirAll(certDir, 0755); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("creating cert dir: %w", err)
	}

	output, err := h.CLI("certificate", "request", "-n", "client-enrollment", "-d", certDir)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("requesting enrollment certificate: %w", err)
	}
	configYAML = []byte(output)

	caCert, err = os.ReadFile(certDir + "/ca.crt")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("reading ca.crt: %w", err)
	}
	enrollCert, err = os.ReadFile(certDir + "/client-enrollment.crt")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("reading client-enrollment.crt: %w", err)
	}
	enrollKey, err = os.ReadFile(certDir + "/client-enrollment.key")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("reading client-enrollment.key: %w", err)
	}

	return configYAML, caCert, enrollCert, enrollKey, nil
}

// GenerateIgnitionConfig builds an Ignition JSON config with agent files.
func (d *RealDevice) GenerateIgnitionConfig(configYAML, caCert, enrollCert, enrollKey []byte) ([]byte, error) {
	ign, err := ignition.NewWrapper()
	if err != nil {
		return nil, fmt.Errorf("creating ignition wrapper: %w", err)
	}

	ign.SetFile("/etc/flightctl/config.yaml", configYAML, 0600, false, "root", "root")
	ign.SetFile("/etc/flightctl/certs/ca.crt", caCert, 0600, false, "root", "root")
	ign.SetFile("/etc/flightctl/certs/client-enrollment.crt", enrollCert, 0600, false, "root", "root")
	ign.SetFile("/etc/flightctl/certs/client-enrollment.key", enrollKey, 0600, false, "root", "root")

	return ign.AsJson()
}

type cloudInitConfig struct {
	WriteFiles []cloudInitFile `yaml:"write_files"`
	RunCmd     [][]string      `yaml:"runcmd"`
}

type cloudInitFile struct {
	Path        string `yaml:"path"`
	Content     string `yaml:"content"`
	Permissions string `yaml:"permissions"`
}

// GenerateCloudInitConfig builds a cloud-init YAML config with agent files.
func (d *RealDevice) GenerateCloudInitConfig(configYAML, caCert, enrollCert, enrollKey []byte) ([]byte, error) {
	rpmSource := os.Getenv("FLIGHTCTL_AGENT_RPM")
	if rpmSource == "" {
		rpmSource = "flightctl-agent"
	}

	cfg := cloudInitConfig{
		WriteFiles: []cloudInitFile{
			{Path: "/etc/flightctl/config.yaml", Content: string(configYAML), Permissions: "0600"},
			{Path: "/etc/flightctl/certs/ca.crt", Content: string(caCert), Permissions: "0600"},
			{Path: "/etc/flightctl/certs/client-enrollment.crt", Content: string(enrollCert), Permissions: "0600"},
			{Path: "/etc/flightctl/certs/client-enrollment.key", Content: string(enrollKey), Permissions: "0600"},
		},
		RunCmd: [][]string{
			{"dnf", "install", "-y", rpmSource},
			{"systemctl", "enable", "--now", "flightctl-agent"},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshalling cloud-init config: %w", err)
	}

	return append([]byte("#cloud-config\n"), data...), nil
}

func (d *RealDevice) applyProvisioningViaSSH(configYAML, caCert, enrollCert, enrollKey []byte) error {
	if _, err := d.RunSSH([]string{"sudo", "mkdir", "-p", "/etc/flightctl/certs"}, nil); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	files := map[string][]byte{
		"/etc/flightctl/config.yaml":                    configYAML,
		"/etc/flightctl/certs/ca.crt":                   caCert,
		"/etc/flightctl/certs/client-enrollment.crt":    enrollCert,
		"/etc/flightctl/certs/client-enrollment.key":    enrollKey,
	}
	for path, content := range files {
		if _, err := d.RunSSH([]string{"sudo", "tee", path}, bytes.NewBuffer(content)); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		if _, err := d.RunSSH([]string{"sudo", "chmod", "0600", path}, nil); err != nil {
			return fmt.Errorf("setting permissions on %s: %w", path, err)
		}
	}

	rpmSource := os.Getenv("FLIGHTCTL_AGENT_RPM")
	if rpmSource == "" {
		rpmSource = "flightctl-agent"
	}

	logrus.Infof("Installing flightctl-agent on %s", d.Host)
	if _, err := d.RunSSH([]string{"sudo", "dnf", "install", "-y", rpmSource}, nil); err != nil {
		return fmt.Errorf("installing flightctl-agent: %w", err)
	}

	logrus.Infof("Starting flightctl-agent on %s", d.Host)
	if _, err := d.RunSSH([]string{"sudo", "systemctl", "enable", "--now", "flightctl-agent"}, nil); err != nil {
		return fmt.Errorf("starting flightctl-agent: %w", err)
	}

	return nil
}

// UninstallAgent removes the flightctl-agent from the device.
func (d *RealDevice) UninstallAgent() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = ctx

	logrus.Infof("Uninstalling agent from %s", d.Host)
	d.RunSSH([]string{"sudo", "systemctl", "disable", "--now", "flightctl-agent"}, nil) //nolint:errcheck
	d.RunSSH([]string{"sudo", "dnf", "remove", "-y", "flightctl-agent"}, nil)          //nolint:errcheck
	d.RunSSH([]string{"sudo", "rm", "-rf", "/etc/flightctl", "/var/lib/flightctl"}, nil) //nolint:errcheck
	return nil
}
