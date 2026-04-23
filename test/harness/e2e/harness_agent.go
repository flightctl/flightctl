package e2e

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

const agentConfigPath = "/etc/flightctl/config.yaml"

func vmShellCommandArgs(command string) []string {
	return []string{"bash -lc " + strconv.Quote(command)}
}

// RunScriptOnVM executes a bash script on the VM without relying on direct shell quoting
// of the full script body.
func (h *Harness) RunScriptOnVM(script string) (*bytes.Buffer, error) {
	if h == nil {
		return nil, fmt.Errorf("harness is nil")
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	remote := "printf %s " + strconv.Quote(encoded) + " | base64 -d | sudo bash"
	return h.VM.RunSSH(vmShellCommandArgs(remote), nil)
}

// StartFlightCtlAgent starts the flightctl-agent service
func (h *Harness) StartFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "start", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to start flightctl-agent: %w", err)
	}
	return nil
}

// StopFlightCtlAgent stops the flightctl-agent service
func (h *Harness) StopFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "stop", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to stop flightctl-agent: %w", err)
	}
	return nil
}

// RestartFlightCtlAgent restarts the flightctl-agent service
func (h *Harness) RestartFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "restart", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to restart flightctl-agent: %w", err)
	}
	return nil
}

// CopyAgentFile copies a file on the VM using sudo cp.
func (h *Harness) CopyAgentFile(sourcePath, destinationPath string) error {
	if strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(destinationPath) == "" {
		return fmt.Errorf("source and destination paths must be non-empty")
	}
	_, err := h.VM.RunSSH([]string{"sudo", "cp", sourcePath, destinationPath}, nil)
	if err != nil {
		return fmt.Errorf("failed to copy %s to %s: %w", sourcePath, destinationPath, err)
	}
	return nil
}

// RemoveAgentFile removes a file on the VM using sudo rm -f.
func (h *Harness) RemoveAgentFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path must be non-empty")
	}
	_, err := h.VM.RunSSH([]string{"sudo", "rm", "-f", path}, nil)
	if err != nil {
		return fmt.Errorf("failed to remove file %s: %w", path, err)
	}
	return nil
}

// WriteAgentFile writes content to a file on the VM while suppressing payload echo in stdout.
func (h *Harness) WriteAgentFile(path, content string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path must be non-empty")
	}
	command := fmt.Sprintf("sudo tee %s >/dev/null", strconv.Quote(path))
	_, err := h.VM.RunSSH(vmShellCommandArgs(command), bytes.NewBufferString(content))
	if err != nil {
		return fmt.Errorf("failed to write file %s: %w", path, err)
	}
	return nil
}

// UpdateAgentConfigWith reads the current agent config from the VM, calls mutate to modify it,
// writes it back via SetAgentConfig, and restarts the agent.
func (h *Harness) UpdateAgentConfigWith(mutate func(*agentcfg.Config)) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	stdout, err := h.VM.RunSSH([]string{"sudo", "cat", agentConfigPath}, nil)
	if err != nil {
		return fmt.Errorf("reading agent config: %w", err)
	}

	cfg := &agentcfg.Config{}
	if err := yaml.Unmarshal(stdout.Bytes(), cfg); err != nil {
		return fmt.Errorf("parsing agent config: %w", err)
	}

	if mutate != nil {
		mutate(cfg)
	}

	if err := h.SetAgentConfig(cfg); err != nil {
		return err
	}

	return h.RestartFlightCtlAgent()
}

// ReloadFlightCtlAgent reloads the flightctl-agent service
func (h *Harness) ReloadFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "reload", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to reload flightctl-agent: %w", err)
	}
	return nil
}

// CreateAgentDropIn creates a systemd drop-in file for the flightctl-agent service.
func (h *Harness) CreateAgentDropIn(filename, content string) error {
	if _, err := h.VM.RunSSH([]string{
		"sudo", "mkdir", "-p", "/etc/systemd/system/flightctl-agent.service.d",
	}, nil); err != nil {
		return fmt.Errorf("failed to create drop-in directory: %w", err)
	}

	path := "/etc/systemd/system/flightctl-agent.service.d/" + filename
	if _, err := h.VM.RunSSH([]string{
		"sudo", "tee", path,
	}, bytes.NewBufferString(content)); err != nil {
		return fmt.Errorf("failed to write drop-in %s: %w", filename, err)
	}

	return nil
}

func (h *Harness) VMDaemonReload() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "daemon-reload"}, nil)
	if err != nil {
		return fmt.Errorf("failed to run systemctl daemon-reload: %w", err)
	}
	return nil
}

func (h *Harness) SyncVMClock() error {
	hostTime := time.Now().UTC().Format("2006-01-02T15:04:05")
	_, err := h.VM.RunSSH([]string{"sudo", "date", "-u", "-s", hostTime}, nil)
	if err != nil {
		return fmt.Errorf("failed to sync VM clock: %w", err)
	}
	return nil
}

// VMCommandOutputFunc returns a polling function for Eventually/Consistently.
func (h *Harness) VMCommandOutputFunc(command string, trim bool) func() string {
	return func() string {
		outputBuf, err := h.VM.RunSSH(vmShellCommandArgs(command), nil)
		output := ""
		if outputBuf != nil {
			output = outputBuf.String()
		} else if err != nil {
			output = ""
		}
		if trim {
			return strings.TrimSpace(output)
		}
		return output
	}
}

// WaitForPodmanImagePresence waits until a podman image is present/absent on the VM.
func (h *Harness) WaitForPodmanImagePresence(imageRef string, shouldExist bool) {
	GinkgoWriter.Printf("Waiting for podman image presence (image=%s shouldExist=%t)\n", imageRef, shouldExist)
	Expect(h).ToNot(BeNil())
	Expect(strings.TrimSpace(imageRef)).ToNot(BeEmpty())

	checkCmd := fmt.Sprintf("sudo podman image inspect %s >/dev/null 2>&1 && echo present || echo missing", imageRef)
	Eventually(h.VMCommandOutputFunc(checkCmd, false), TIMEOUT, POLLING).
		Should(Satisfy(func(raw string) bool {
			present := strings.TrimSpace(raw) == "present"
			if shouldExist {
				return present
			}
			return !present
		}))
}

const pruningArtifactsDir = "artifacts/pruning"

func WriteEvidence(dir, filename, command, output string, err error) error {
	var b strings.Builder
	b.WriteString("$ ")
	b.WriteString(command)
	b.WriteString("\n")
	if err != nil {
		b.WriteString("error: ")
		b.WriteString(err.Error())
		b.WriteString("\n")
	}
	b.WriteString(output)
	return os.WriteFile(filepath.Join(dir, filename), []byte(b.String()), 0o600)
}

// EnsureArtifactDir builds and creates the per-test artifact directory.
func (h *Harness) EnsureArtifactDir(testCase string) (string, error) {
	testID := h.GetTestIDFromContext()
	dir := filepath.Join(pruningArtifactsDir, fmt.Sprintf("%s-%s", testCase, testID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	GinkgoWriter.Printf("Created artifact dir (testCase=%s dir=%s)\n", testCase, dir)
	return dir, nil
}

// SetupScenario creates the artifacts dir for the scenario.
func (h *Harness) SetupScenario(deviceID, testCase string) (string, error) {
	artifactDir, err := h.EnsureArtifactDir(testCase)
	if err != nil {
		return "", err
	}
	GinkgoWriter.Printf("Preparing scenario (device=%s testCase=%s)\n", deviceID, testCase)
	return artifactDir, nil
}

// CaptureStandardEvidence records common evidence files for pruning scenarios.
func (h *Harness) CaptureStandardEvidence(artifactDir, deviceID string) error {
	GinkgoWriter.Printf("Capturing standard evidence (device=%s dir=%s)\n", deviceID, artifactDir)
	if err := h.CaptureVMCommand(artifactDir, "vm_systemctl_status.txt", "sudo systemctl status flightctl-agent --no-pager", false); err != nil {
		return err
	}
	if err := h.CaptureVMCommand(artifactDir, "vm_desired_json.txt", "sudo cat /var/lib/flightctl/desired.json", false); err != nil {
		return err
	}
	if err := h.CaptureVMCommand(artifactDir, "vm_current_json.txt", "sudo cat /var/lib/flightctl/current.json", false); err != nil {
		return err
	}
	_ = h.CaptureVMCommand(artifactDir, "vm_refs_cat.txt", "sudo cat /var/lib/flightctl/image-artifact-references.json", true)
	if err := h.CaptureVMCommand(artifactDir, "vm_podman_images_all.txt", "podman images --no-trunc", false); err != nil {
		return err
	}
	if err := h.CaptureHostCLI(artifactDir, "host_flightctl_get_device_yaml.txt", "get", "device", deviceID, "-o", "yaml"); err != nil {
		return err
	}
	if err := h.CaptureHostCLI(artifactDir, "host_flightctl_get_device_json.txt", "get", "device", deviceID, "-o", "json"); err != nil {
		return err
	}
	_ = h.CaptureVMCommand(artifactDir, "vm_journalctl_flightctl_agent.txt", "sudo journalctl -u flightctl-agent --no-pager -n 200", true)
	return nil
}

// RunVMCommandWithEvidence runs a VM command and captures its output.
func (h *Harness) RunVMCommandWithEvidence(artifactDir, filename, command string) (string, error) {
	GinkgoWriter.Printf("VM command (file=%s cmd=%s)\n", filename, command)
	outputBuf, err := h.VM.RunSSH(vmShellCommandArgs(command), nil)
	output := ""
	if outputBuf != nil {
		output = outputBuf.String()
	}
	if writeErr := WriteEvidence(artifactDir, filename, command, output, err); writeErr != nil {
		if err == nil {
			return output, writeErr
		}
		return output, errors.Join(err, writeErr)
	}
	GinkgoWriter.Printf("Wrote VM evidence file: %s\n", filepath.Join(artifactDir, filename))
	return output, err
}

// CaptureVMCommand runs a VM command and writes evidence.
func (h *Harness) CaptureVMCommand(artifactDir, filename, command string, optional bool) error {
	GinkgoWriter.Printf("Capture VM evidence (file=%s optional=%t)\n", filename, optional)
	_, err := h.RunVMCommandWithEvidence(artifactDir, filename, command)
	if optional {
		return nil
	}
	return err
}

// CaptureHostCLI runs flightctl on the host and writes evidence.
func (h *Harness) CaptureHostCLI(artifactDir, filename string, args ...string) error {
	GinkgoWriter.Printf("Host CLI evidence (file=%s args=%s)\n", filename, strings.Join(args, " "))
	output, err := h.CLI(args...)
	cmd := "flightctl " + strings.Join(args, " ")
	if writeErr := WriteEvidence(artifactDir, filename, cmd, output, err); writeErr != nil {
		if err == nil {
			return writeErr
		}
		return errors.Join(err, writeErr)
	}
	GinkgoWriter.Printf("Wrote host evidence file: %s\n", filepath.Join(artifactDir, filename))
	return err
}

// DropinCleanupFunc returns a cleanup func that removes a drop-in and records evidence.
func (h *Harness) DropinCleanupFunc(artifactDir, filename, command string) func() {
	GinkgoWriter.Printf("Registering drop-in cleanup (file=%s)\n", filename)
	return func() {
		_, _ = h.RunVMCommandWithEvidence(artifactDir, filename, command)
	}
}

// CreateInlineApplicationSpec builds an inline application provider spec from compose yaml content.
func CreateInlineApplicationSpec(inlineAppComposeYaml, filename string) v1beta1.InlineApplicationProviderSpec {
	return v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Content: &inlineAppComposeYaml,
				Path:    filename,
			},
		},
	}
}

// EnableAgentMetrics appends metrics-enabled: true to the agent config so
// the Prometheus /metrics endpoint is available for test validation.
func (h *Harness) EnableAgentMetrics() error {
	metricsConfig := "\nmetrics-enabled: true\n"
	_, err := h.VM.RunSSH([]string{
		"sudo", "tee", "-a", "/etc/flightctl/config.yaml",
	}, bytes.NewBufferString(metricsConfig))
	return err
}

// GetAgentMetrics fetches the Prometheus metrics from the agent's metrics
// endpoint on the VM via SSH.
func (h *Harness) GetAgentMetrics() (string, error) {
	output, err := h.VM.RunSSH([]string{
		"curl", "-s", "http://127.0.0.1:15690/metrics",
	}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to fetch agent metrics: %w", err)
	}
	return output.String(), nil
}

// ParseMetricValue extracts the value of a Prometheus metric line from the
// metrics output. For a metric like `foo{label="val"} 42`, pass the full
// metric name with labels as the metricName parameter.
func ParseMetricValue(metricsOutput, metricName string) string {
	for _, line := range strings.Split(metricsOutput, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, metricName) {
			rest := strings.TrimPrefix(line, metricName)
			if len(rest) == 0 || (rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// GetAgentMetricValue fetches the agent metrics and parses a single metric
// value in one step.
func (h *Harness) GetAgentMetricValue(metricName string) (string, error) {
	metricsOutput, err := h.GetAgentMetrics()
	if err != nil {
		return "", err
	}
	return ParseMetricValue(metricsOutput, metricName), nil
}

func (h *Harness) getAPIEndpointAddressFromVM() (host string, ip string, port string, err error) {
	output, err := h.VM.RunSSH([]string{"sudo", "cat", "/etc/flightctl/config.yaml"}, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read agent config: %w", err)
	}

	cfg := &agentcfg.Config{}
	if err := yaml.Unmarshal(output.Bytes(), cfg); err != nil {
		return "", "", "", fmt.Errorf("failed to parse agent config YAML: %w", err)
	}
	serverURL := cfg.ManagementService.Service.Server
	if serverURL == "" {
		serverURL = cfg.EnrollmentService.Service.Server
	}
	if serverURL == "" {
		return "", "", "", fmt.Errorf("server URL not found in agent config")
	}

	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to parse server URL %q: %w", serverURL, err)
	}

	hostname := parsed.Hostname()
	p := parsed.Port()
	if p == "" {
		if parsed.Scheme == "https" {
			p = "443"
		} else {
			p = "80"
		}
	}

	resolvedIP := hostname
	// If hostname is not an IP, resolve it on the VM so iptables rules
	// use the actual destination IP rather than relying on iptables DNS
	// resolution which may differ from the Go HTTP client's resolution.
	if net.ParseIP(hostname) == nil {
		resolved, resolveErr := h.VM.RunSSH([]string{
			"getent", "ahosts", hostname,
		}, nil)
		if resolveErr == nil {
			lines := strings.Split(strings.TrimSpace(resolved.String()), "\n")
			if len(lines) > 0 {
				fields := strings.Fields(lines[0])
				if len(fields) > 0 && net.ParseIP(fields[0]) != nil {
					resolvedIP = fields[0]
				}
			}
		}
	}

	return hostname, resolvedIP, p, nil
}

// GetAPIEndpointFromVM extracts the resolved API server IP and port from the
// agent config file on the VM (/etc/flightctl/config.yaml).
func (h *Harness) GetAPIEndpointFromVM() (ip string, port string, err error) {
	_, ip, port, err = h.getAPIEndpointAddressFromVM()
	return ip, port, err
}

// GetAPIEndpointHostIPPortFromVM extracts both the configured API hostname and
// the resolved destination IP plus port from the agent config file on the VM.
func (h *Harness) GetAPIEndpointHostIPPortFromVM() (host string, ip string, port string, err error) {
	return h.getAPIEndpointAddressFromVM()
}

// BlockTrafficOnVM adds an iptables/ip6tables rule on the VM to reject TCP traffic to the
// specified IP and port. Automatically detects IPv4 vs IPv6 and uses the appropriate command.
func (h *Harness) BlockTrafficOnVM(ip, port string) {
	iptablesCmd := getIPTablesCommand(ip)

	_, err := h.VM.RunSSH([]string{
		"sudo", iptablesCmd, "-A", "OUTPUT", "-d", ip, "-p", "tcp", "--dport", port, "-j", "REJECT",
	}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to add %s rule on VM", iptablesCmd)
}

// UnblockTrafficOnVM removes the iptables/ip6tables rule on the VM that blocks TCP
// traffic to the specified IP and port. Silently ignores errors.
func (h *Harness) UnblockTrafficOnVM(ip, port string) {
	iptablesCmd := getIPTablesCommand(ip)

	_, _ = h.VM.RunSSH([]string{
		"sudo", iptablesCmd, "-D", "OUTPUT", "-d", ip, "-p", "tcp", "--dport", port, "-j", "REJECT",
	}, nil)
}

func (h *Harness) IsAgentServiceRunning() bool {
	output, err := h.VM.RunSSH([]string{
		"sudo", "systemctl", "is-active", "flightctl-agent",
	}, nil)
	if err != nil {
		return false
	}
	return strings.TrimSpace(output.String()) == "active"
}

// UpdateDeviceApplicationFromInline updates or appends an inline application spec on the device.
func UpdateDeviceApplicationFromInline(device *v1beta1.Device, appName string, inlineApp v1beta1.InlineApplicationProviderSpec) error {
	composeApp := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    &appName,
	}
	if err := composeApp.FromInlineApplicationProviderSpec(inlineApp); err != nil {
		return err
	}

	var appSpec v1beta1.ApplicationProviderSpec
	if err := appSpec.FromComposeApplication(composeApp); err != nil {
		return err
	}

	if device.Spec.Applications == nil {
		device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
		return nil
	}

	for i, existing := range *device.Spec.Applications {
		existingName, _ := existing.GetName()
		if existingName != nil && *existingName == appName {
			(*device.Spec.Applications)[i] = appSpec
			return nil
		}
	}

	*device.Spec.Applications = append(*device.Spec.Applications, appSpec)
	return nil
}
