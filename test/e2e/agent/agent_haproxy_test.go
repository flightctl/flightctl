package agent_test

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	rhemHAProxyLocalPort        = 17443
	rhemHAProxyIdleTimeout      = 5 * time.Second
	rhemHAProxyWaitAfterIdle    = 15 * time.Second
	rhemHAProxyAvailableMessage = "haproxy_available"
	rhemHAProxyReadyMessage     = "haproxy_ready"
	rhemHAProxyServiceName      = "flightctl-agent-haproxy.service"
	rhemHAProxyConfigPath       = "/etc/flightctl/haproxy-agent-api.cfg"
	rhemHAProxySystemdUnitPath  = "/etc/systemd/system/" + rhemHAProxyServiceName
	rhemHAProxyMarkerConfigName = "rhem-haproxy-long-poll"
	rhemHAProxyMarkerPath       = "/etc/flightctl-rhem-haproxy-marker"
	rhemHAProxyMarkerContent    = "rendered through haproxy after idle timeout"
	rhemHAProxyOptInEnv         = "FLIGHTCTL_E2E_HAPROXY"
)

// This regression covers Edge Manager deployments where agent long-poll requests
// traverse a load balancer with an idle timeout shorter than the server poll.
var _ = Describe("VM Agent behavior behind HAProxy", func() {
	It("keeps syncing rendered specs in RHEM behind HAProxy idle timeouts", Label("89640", "agent"), func() {
		harness := e2e.GetWorkerHarness()

		By("Verifying this is an OCP environment")
		err := verifyRHEMOCPEnvironment()
		Expect(err).ToNot(HaveOccurred())

		if os.Getenv(rhemHAProxyOptInEnv) != "1" {
			Skip(fmt.Sprintf("RHEM HAProxy regression requires %s=1", rhemHAProxyOptInEnv))
		}

		By("Ensuring HAProxy is available on the agent VM")
		availabilityOutput, err := ensureRHEMHAProxyAvailableOnVM(harness)
		Expect(err).ToNot(HaveOccurred())
		Expect(availabilityOutput).ToNot(BeEmpty(), "HAProxy availability output should not be empty")
		Expect(availabilityOutput).To(ContainSubstring(rhemHAProxyAvailableMessage), "haproxy must be installed when %s=1", rhemHAProxyOptInEnv)

		By("Enrolling the VM agent before placing HAProxy in the management path")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		Expect(deviceID).ToNot(BeEmpty(), "enrolled device id should not be empty")
		Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(deviceID).
			Should(Equal(v1beta1.DeviceSummaryStatusOnline))

		By("Starting HAProxy on the VM with an idle timeout shorter than the rendered-spec long-poll")
		originalConfig, err := harness.GetAgentConfig()
		Expect(err).ToNot(HaveOccurred())
		Expect(originalConfig).ToNot(BeNil())
		registerRHEMHAProxyCleanup(harness, originalConfig)

		proxyURL, err := configureAgentManagementThroughHAProxy(harness, originalConfig)
		Expect(err).ToNot(HaveOccurred())
		Expect(proxyURL).To(ContainSubstring(strconv.Itoa(rhemHAProxyLocalPort)))

		By("Waiting for the agent to reconnect through HAProxy")
		Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(deviceID).
			Should(Equal(v1beta1.DeviceSummaryStatusOnline))

		By("Waiting beyond the HAProxy idle timeout while the agent continues polling")
		Consistently(harness.GetDeviceWithStatusSummary, rhemHAProxyWaitAfterIdle, POLLING).WithArguments(deviceID).
			Should(Equal(v1beta1.DeviceSummaryStatusOnline))

		By("Applying a device spec update that must be delivered through HAProxy")
		nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceID)
		Expect(err).ToNot(HaveOccurred())
		originalDeviceConfig, err := getRHEMHAProxyDeviceConfig(harness, deviceID)
		Expect(err).ToNot(HaveOccurred())
		registerRHEMHAProxyDeviceConfigCleanup(harness, deviceID, originalDeviceConfig)
		configSpec, err := buildRHEMHAProxyMarkerConfig()
		Expect(err).ToNot(HaveOccurred())
		err = harness.UpdateDeviceConfigWithRetries(deviceID, []v1beta1.ConfigProviderSpec{configSpec}, nextRenderedVersion)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the VM applied the rendered config from the proxied sync")
		out, err := harness.VM.RunSSH([]string{"sudo", "cat", rhemHAProxyMarkerPath}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out).ToNot(BeNil(), "marker file command output should not be nil")
		Expect(out.String()).ToNot(BeEmpty(), "marker file content should not be empty")
		Expect(out.String()).To(ContainSubstring(rhemHAProxyMarkerContent))
		Eventually(harness.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(deviceID).
			Should(Equal(v1beta1.DeviceSummaryStatusOnline))
	})
})

// verifyRHEMOCPEnvironment confirms the e2e run targets OCP.
func verifyRHEMOCPEnvironment() error {
	providers := setup.GetDefaultProviders()
	if providers == nil {
		return fmt.Errorf("default providers are not initialized")
	}
	if providers.Infra == nil {
		return fmt.Errorf("infra provider is nil")
	}
	envType := providers.Infra.GetEnvironmentType()
	GinkgoWriter.Printf("RHEM HAProxy environment type: %s\n", envType)
	if envType != infra.EnvironmentOCP {
		Skip(fmt.Sprintf("RHEM HAProxy regression is OCP-only, got %q", envType))
	}
	return nil
}

// configureAgentManagementThroughHAProxy starts a TCP HAProxy on the VM and points management traffic at it.
func configureAgentManagementThroughHAProxy(harness *e2e.Harness, originalConfig *agentcfg.Config) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("harness is nil")
	}
	if harness.VM == nil {
		return "", fmt.Errorf("harness VM is nil")
	}
	if originalConfig == nil {
		return "", fmt.Errorf("original agent config is nil")
	}
	managementURL := getRHEMHAProxyManagementURL(originalConfig)
	if managementURL == "" {
		return "", fmt.Errorf("management and enrollment service server URLs are empty")
	}
	parsed, err := url.Parse(managementURL)
	if err != nil {
		return "", fmt.Errorf("parse management service URL %q: %w", managementURL, err)
	}
	backendHost := parsed.Hostname()
	if backendHost == "" {
		return "", fmt.Errorf("management service URL %q has empty hostname", managementURL)
	}
	backendPort := parsed.Port()
	if backendPort == "" {
		backendPort = "443"
	}

	output, err := startRHEMHAProxyOnVM(harness, backendHost, backendPort)
	if err != nil {
		return "", err
	}
	if !strings.Contains(output, rhemHAProxyReadyMessage) {
		return "", fmt.Errorf("HAProxy readiness output missing %q: %s", rhemHAProxyReadyMessage, output)
	}

	proxyURL := fmt.Sprintf("%s://127.0.0.1:%d", parsed.Scheme, rhemHAProxyLocalPort)
	originalTLSServerName := getRHEMHAProxyTLSServerName(originalConfig)
	if err := harness.UpdateAgentConfigWith(func(cfg *agentcfg.Config) {
		cfg.ManagementService.Service.Server = proxyURL
		if strings.TrimSpace(cfg.ManagementService.Service.TLSServerName) == "" {
			cfg.ManagementService.Service.TLSServerName = originalTLSServerName
			if cfg.ManagementService.Service.TLSServerName == "" {
				cfg.ManagementService.Service.TLSServerName = backendHost
			}
		}
	}); err != nil {
		return "", fmt.Errorf("point agent management service at HAProxy: %w", err)
	}
	return proxyURL, nil
}

// getRHEMHAProxyManagementURL returns the effective management endpoint from the agent config.
func getRHEMHAProxyManagementURL(cfg *agentcfg.Config) string {
	if cfg == nil {
		return ""
	}
	if server := strings.TrimSpace(cfg.ManagementService.Service.Server); server != "" {
		return server
	}
	return strings.TrimSpace(cfg.EnrollmentService.Service.Server)
}

// getRHEMHAProxyTLSServerName returns the effective TLS server name from the agent config.
func getRHEMHAProxyTLSServerName(cfg *agentcfg.Config) string {
	if cfg == nil {
		return ""
	}
	if serverName := strings.TrimSpace(cfg.ManagementService.Service.TLSServerName); serverName != "" {
		return serverName
	}
	return strings.TrimSpace(cfg.EnrollmentService.Service.TLSServerName)
}

// ensureRHEMHAProxyAvailableOnVM installs HAProxy on the VM when missing and verifies it is usable.
func ensureRHEMHAProxyAvailableOnVM(harness *e2e.Harness) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("harness is nil")
	}
	if harness.VM == nil {
		return "", fmt.Errorf("harness VM is nil")
	}

	output, err := runRHEMHAProxyScriptOnVM(harness, "ensure HAProxy availability on VM", fmt.Sprintf(`set -e
if ! command -v haproxy >/dev/null 2>&1; then
  if command -v dnf >/dev/null 2>&1; then
    dnf install -y haproxy
  elif command -v microdnf >/dev/null 2>&1; then
    microdnf install -y haproxy
  else
    echo "no supported package manager found for installing haproxy" >&2
    exit 127
  fi
fi
command -v haproxy >/dev/null 2>&1
haproxy -v
echo %[1]s
`, rhemHAProxyAvailableMessage))
	if err != nil {
		return output, err
	}
	GinkgoWriter.Printf("RHEM HAProxy availability output: %s\n", strings.TrimSpace(output))
	return output, nil
}

// startRHEMHAProxyOnVM starts a TCP HAProxy service on the VM.
func startRHEMHAProxyOnVM(harness *e2e.Harness, backendHost, backendPort string) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(backendHost) == "" || strings.TrimSpace(backendPort) == "" {
		return "", fmt.Errorf("backend host and port must be non-empty")
	}

	script := fmt.Sprintf(`set -euo pipefail
install -d /etc/flightctl
cat >%[1]s <<'EOF'
global
  log stdout format raw local0

defaults
  mode tcp
  timeout connect %[2]s
  timeout client %[2]s
  timeout server %[2]s

frontend agent_api
  bind 127.0.0.1:%[3]d
  default_backend flightctl_agent_api

backend flightctl_agent_api
  server api %[4]s:%[5]s check
EOF
cat >%[6]s <<'EOF'
[Unit]
Description=Flight Control e2e HAProxy for agent API
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=/usr/sbin/haproxy -Ws -f %[1]s
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl restart %[7]s
systemctl is-active --quiet %[7]s
echo %[8]s
`,
		strconv.Quote(rhemHAProxyConfigPath),
		rhemHAProxyIdleTimeout.String(),
		rhemHAProxyLocalPort,
		backendHost,
		backendPort,
		strconv.Quote(rhemHAProxySystemdUnitPath),
		rhemHAProxyServiceName,
		rhemHAProxyReadyMessage,
	)

	return runRHEMHAProxyScriptOnVM(harness, "start HAProxy on VM", script)
}

// runRHEMHAProxyScriptOnVM executes a VM script and returns non-nil output with contextual errors.
func runRHEMHAProxyScriptOnVM(harness *e2e.Harness, description, script string) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("%s: harness is nil", description)
	}
	if harness.VM == nil {
		return "", fmt.Errorf("%s: harness VM is nil", description)
	}
	if strings.TrimSpace(description) == "" {
		return "", fmt.Errorf("script description is empty")
	}
	if strings.TrimSpace(script) == "" {
		return "", fmt.Errorf("%s: script is empty", description)
	}
	out, err := harness.RunScriptOnVM(script)
	output := ""
	if out != nil {
		output = out.String()
	}
	if err != nil {
		return output, fmt.Errorf("%s: %w: %s", description, err, output)
	}
	return output, nil
}

// getRHEMHAProxyDeviceConfig returns a copy of the device config providers before the test replaces them.
func getRHEMHAProxyDeviceConfig(harness *e2e.Harness, deviceID string) (*[]v1beta1.ConfigProviderSpec, error) {
	if harness == nil {
		return nil, fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(deviceID) == "" {
		return nil, fmt.Errorf("device ID is empty")
	}
	device, err := harness.GetDevice(deviceID)
	if err != nil {
		return nil, fmt.Errorf("get device %q: %w", deviceID, err)
	}
	if device == nil {
		return nil, fmt.Errorf("device %q is nil", deviceID)
	}
	if device.Spec.Config == nil {
		return nil, nil
	}
	config := append([]v1beta1.ConfigProviderSpec(nil), (*device.Spec.Config)...)
	return &config, nil
}

// buildRHEMHAProxyMarkerConfig returns the marker config applied through the proxied long-poll path.
func buildRHEMHAProxyMarkerConfig() (v1beta1.ConfigProviderSpec, error) {
	configSpec, err := util.BuildInlineConfigSpec(rhemHAProxyMarkerConfigName, rhemHAProxyMarkerPath, rhemHAProxyMarkerContent, "")
	if err != nil {
		return v1beta1.ConfigProviderSpec{}, fmt.Errorf("build RHEM HAProxy marker config: %w", err)
	}
	return configSpec, nil
}

// registerRHEMHAProxyDeviceConfigCleanup restores the device config providers replaced by the test.
func registerRHEMHAProxyDeviceConfigCleanup(harness *e2e.Harness, deviceID string, originalConfig *[]v1beta1.ConfigProviderSpec) {
	DeferCleanup(func() error {
		if harness == nil {
			return fmt.Errorf("RHEM HAProxy device config cleanup: harness is nil")
		}
		if strings.TrimSpace(deviceID) == "" {
			return fmt.Errorf("RHEM HAProxy device config cleanup: device ID is empty")
		}
		if err := harness.UpdateDeviceWithRetries(deviceID, func(device *v1beta1.Device) {
			if originalConfig == nil {
				device.Spec.Config = nil
				return
			}
			config := append([]v1beta1.ConfigProviderSpec(nil), (*originalConfig)...)
			device.Spec.Config = &config
		}); err != nil {
			return fmt.Errorf("restore RHEM HAProxy device config: %w", err)
		}
		return nil
	})
}

// registerRHEMHAProxyCleanup restores the original agent config and stops the test HAProxy service.
func registerRHEMHAProxyCleanup(harness *e2e.Harness, originalConfig *agentcfg.Config) {
	DeferCleanup(func() error {
		if harness == nil {
			return fmt.Errorf("RHEM HAProxy cleanup: harness is nil")
		}
		var cleanupErr error
		if originalConfig != nil {
			if err := harness.SetAgentConfig(originalConfig); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore agent config: %w", err))
			} else if err := harness.RestartFlightCtlAgent(); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restart agent after restoring config: %w", err))
			}
		}
		if _, err := harness.RunScriptOnVM(fmt.Sprintf(`set -e
if [ -f %[3]s ]; then
  systemctl stop %[1]s
  systemctl disable %[1]s
fi
rm -f %[2]s %[3]s %[4]s
systemctl daemon-reload
`, rhemHAProxyServiceName, rhemHAProxyConfigPath, rhemHAProxySystemdUnitPath, rhemHAProxyMarkerPath)); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove HAProxy artifacts: %w", err))
		}
		return cleanupErr
	})
}
