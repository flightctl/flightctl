package agent_test

import (
	"encoding/base64"
	"fmt"
	"net"
	"strings"

	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	metricsEndpoint = "http://127.0.0.1:15690/metrics"
	pprofEndpoint   = "http://127.0.0.1:15689/debug/pprof/"
)

var _ = Describe("Agent observability and diagnostics", func() {
	var (
		harness  *e2e.Harness
		deviceID string
		cfgBak   *agentcfg.Config
	)

	BeforeEach(func() {
		var err error

		harness = e2e.GetWorkerHarness()
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
		Expect(strings.TrimSpace(deviceID)).ToNot(BeEmpty())

		cfgBak, err = harness.GetAgentConfig()
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		Expect(harness.SetAgentConfig(cfgBak)).To(Succeed())
		Expect(restartFlightctlAgentAndWait(harness)).To(Succeed())
	})

	Context("when local observability endpoints are enabled", func() {
		It("86397 should expose agent metrics on the loopback endpoint", Label("86397", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "agent-metrics")
			Expect(err).ToNot(HaveOccurred())

			By("enabling metrics in the agent config")
			cfg, err := harness.GetAgentConfig()
			Expect(err).ToNot(HaveOccurred())

			cfg.MetricsEnabled = true

			err = harness.SetAgentConfig(cfg)
			Expect(err).ToNot(HaveOccurred())

			By("restarting the flightctl-agent service")
			err = restartFlightctlAgentAndWait(harness)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the metrics endpoint to become ready")
			err = waitForEndpoint(harness, metricsEndpoint)
			Expect(err).ToNot(HaveOccurred())

			By("querying the local metrics endpoint from inside the VM")
			out, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_metrics_endpoint.txt",
				buildCurlCommand(metricsEndpoint),
			)
			Expect(err).ToNot(HaveOccurred())

			By("verifying the metrics endpoint returns Prometheus content")
			Expect(out).To(ContainSubstring("# HELP"))
			Expect(out).To(ContainSubstring("# TYPE"))

			By("verifying expected agent metrics are exposed")
			Expect(out).To(ContainSubstring("create_enrollmentrequest_duration_seconds"))
			Expect(out).To(ContainSubstring("get_enrollmentrequest_duration_seconds"))
			Expect(out).To(ContainSubstring("get_rendered_device_spec_duration_seconds"))
			Expect(out).To(ContainSubstring("update_device_status_duration_seconds"))
			Expect(out).To(ContainSubstring("patch_device_status_duration_seconds"))
			Expect(out).To(ContainSubstring("create_certificate_signing_request_duration_seconds"))
			Expect(out).To(ContainSubstring("get_certificate_signing_request_duration_seconds"))

			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("86399 should expose pprof endpoints on the loopback endpoint", Label("86399", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "agent-pprof")
			Expect(err).ToNot(HaveOccurred())

			By("enabling profiling in the agent config")
			cfg, err := harness.GetAgentConfig()
			Expect(err).ToNot(HaveOccurred())

			cfg.ProfilingEnabled = true

			err = harness.SetAgentConfig(cfg)
			Expect(err).ToNot(HaveOccurred())

			By("restarting the flightctl-agent service")
			err = restartFlightctlAgentAndWait(harness)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the pprof endpoint to become ready")
			err = waitForEndpoint(harness, pprofEndpoint)
			Expect(err).ToNot(HaveOccurred())

			By("querying the local pprof index endpoint from inside the VM")
			indexOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_index.txt",
				buildCurlCommand(pprofEndpoint),
			)
			Expect(err).ToNot(HaveOccurred())

			By("verifying the agent exposes a working pprof index")
			Expect(indexOut).To(ContainSubstring("/debug/pprof/"))
			Expect(indexOut).To(ContainSubstring("goroutine"))
			Expect(indexOut).To(ContainSubstring("heap"))

			By("verifying the goroutine dump endpoint works")
			goroutineOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_goroutine_debug2.txt",
				buildCurlCommand(pprofEndpoint+"goroutine?debug=2"),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(goroutineOut).To(ContainSubstring("goroutine"))

			By("verifying the heap profile endpoint works")
			heapOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_heap.txt",
				buildCurlCommand(pprofEndpoint+"heap"),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(heapOut)).ToNot(BeEmpty())

			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("should expose metrics and pprof only on loopback", Label("88811", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "agent-observability-loopback-only")
			Expect(err).ToNot(HaveOccurred())

			By("enabling metrics and profiling in the agent config")
			cfg, err := harness.GetAgentConfig()
			Expect(err).ToNot(HaveOccurred())

			cfg.MetricsEnabled = true
			cfg.ProfilingEnabled = true

			err = harness.SetAgentConfig(cfg)
			Expect(err).ToNot(HaveOccurred())

			By("restarting the flightctl-agent service")
			err = restartFlightctlAgentAndWait(harness)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the loopback endpoints to become ready")
			Expect(waitForEndpoint(harness, metricsEndpoint)).To(Succeed())
			Expect(waitForEndpoint(harness, pprofEndpoint)).To(Succeed())

			By("discovering a device non-loopback global IP address")
			vmIPOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_primary_non_loopback_ip.txt",
				buildPrimaryNonLoopbackIPCommand(),
			)
			Expect(err).ToNot(HaveOccurred())

			vmIP := strings.TrimSpace(vmIPOut)
			Expect(vmIP).ToNot(BeEmpty())
			Expect(vmIP).ToNot(Equal("127.0.0.1"))
			Expect(vmIP).ToNot(Equal("::1"))

			By("verifying the metrics endpoint is unreachable via the non-loopback interface")
			metricsNonLoopbackOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_metrics_non_loopback.txt",
				buildCurlReachabilityCommand(buildNonLoopbackURL(vmIP, 15690, "/metrics")),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(metricsNonLoopbackOut)).To(Equal("unreachable"))

			By("verifying the pprof endpoint is unreachable via the non-loopback interface")
			pprofNonLoopbackOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_non_loopback.txt",
				buildCurlReachabilityCommand(buildNonLoopbackURL(vmIP, 15689, "/debug/pprof/")),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(pprofNonLoopbackOut)).To(Equal("unreachable"))

			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("86340 should write bootstrap and sync audit log entries in JSONL format", Label("86340", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "agent-audit-log")
			Expect(err).ToNot(HaveOccurred())

			By("verifying the audit log file exists with restricted permissions")
			logStatOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_audit_log_stat.txt",
				"sudo stat -c '%U:%G %a %n' /var/log/flightctl/audit.log",
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(logStatOut)).To(ContainSubstring("root:root"))
			Expect(strings.TrimSpace(logStatOut)).To(ContainSubstring("600"))
			Expect(strings.TrimSpace(logStatOut)).To(ContainSubstring("/var/log/flightctl/audit.log"))

			By("verifying bootstrap audit entries exist for current, desired, and rollback")

			waitForAuditEntryWithEvidence(harness, artifactDir, "vm_audit_bootstrap_current.txt",
				buildAuditJQMatchCommand(deviceID, `select(.reason=="bootstrap" and .type=="current" and .old_version=="" and .new_version=="0" and .result=="success")`))

			waitForAuditEntryWithEvidence(harness, artifactDir, "vm_audit_bootstrap_desired.txt",
				buildAuditJQMatchCommand(deviceID, `select(.reason=="bootstrap" and .type=="desired" and .old_version=="" and .new_version=="0" and .result=="success")`))

			waitForAuditEntryWithEvidence(harness, artifactDir, "vm_audit_bootstrap_rollback.txt",
				buildAuditJQMatchCommand(deviceID, `select(.reason=="bootstrap" and .type=="rollback" and .old_version=="" and .new_version=="0" and .result=="success")`))

			By("generating an embedded agent config with the CLI")
			cliOut, err := harness.CLI("certificate", "request", "--expiration=365d", "-o", "embedded")
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(cliOut)).ToNot(BeEmpty())

			configYAML, err := extractEmbeddedAgentConfig(cliOut)
			Expect(err).ToNot(HaveOccurred())

			encodedConfig := base64.StdEncoding.EncodeToString([]byte(configYAML))

			By("writing the generated config to the device")
			writeCmd := "echo '" + encodedConfig + "' | base64 -d | sudo tee /etc/flightctl/config.yaml >/dev/null"

			_, err = harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_write_embedded_agent_config.txt",
				writeCmd,
			)
			Expect(err).ToNot(HaveOccurred())

			writtenConfig, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_cat_embedded_agent_config.txt",
				"sudo cat /etc/flightctl/config.yaml",
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(writtenConfig).To(ContainSubstring("enrollment-service:"))

			By("deleting the existing agent state on the device")
			_, err = harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_cleanup_flightctl_state.txt",
				"sudo rm -rf /var/lib/flightctl/* /var/lib/flightctl/certs/*",
			)
			Expect(err).ToNot(HaveOccurred())

			By("restarting the flightctl-agent service")
			err = restartFlightctlAgentAndWait(harness)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the device to re-enroll and become online")
			deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
			Expect(strings.TrimSpace(deviceID)).ToNot(BeEmpty())

			By("verifying sync, upgrade, and initialization audit entries exist after enrollment")

			waitForAuditEntryWithEvidence(harness, artifactDir, "vm_audit_sync_desired.txt",
				buildAuditJQMatchCommand(deviceID, `select(.reason=="sync" and .type=="desired" and .old_version=="0" and ((.new_version | tonumber? // -1) > 0) and .result=="success")`))

			waitForAuditEntryWithEvidence(harness, artifactDir, "vm_audit_upgrade_current.txt",
				buildAuditJQMatchCommand(deviceID, `select(.reason=="upgrade" and .type=="current" and .old_version=="0" and ((.new_version | tonumber? // -1) > 0) and .result=="success")`))

			waitForAuditEntryWithEvidence(harness, artifactDir, "vm_audit_initialization_rollback.txt",
				buildAuditJQMatchCommand(deviceID, `select(.reason=="initialization" and .type=="rollback" and .old_version=="0" and .new_version=="" and .result=="success")`))

			By("validating the audit log JSON structure")

			jsonCheckCmd := `sudo jq -e 'if .ts == null or .device == null or .old_version == null or .new_version == null or .result == null or .reason == null or .type == null or .fleet_template_version == null or .agent_version == null then error("Missing required field") else . end' /var/log/flightctl/audit.log`
			jsonCheckOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_audit_json_structure_validation.txt",
				jsonCheckCmd,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(jsonCheckOut)).ToNot(BeEmpty())

			By("verifying the audit log is JSONL format")

			lineCountOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_audit_log_line_count.txt",
				"sudo cat /var/log/flightctl/audit.log | wc -l",
			)
			Expect(err).ToNot(HaveOccurred())

			jsonCountOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_audit_log_json_count.txt",
				"sudo cat /var/log/flightctl/audit.log | jq -s 'length'",
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(lineCountOut)).To(Equal(strings.TrimSpace(jsonCountOut)))

			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})
	})

	Context("when local observability endpoints are disabled", func() {
		It("should not expose agent metrics when metrics are disabled", Label("88808", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "agent-metrics-disabled")
			Expect(err).ToNot(HaveOccurred())

			By("disabling metrics in the agent config")
			cfg, err := harness.GetAgentConfig()
			Expect(err).ToNot(HaveOccurred())

			cfg.MetricsEnabled = false

			err = harness.SetAgentConfig(cfg)
			Expect(err).ToNot(HaveOccurred())

			By("restarting the flightctl-agent service")
			err = restartFlightctlAgentAndWait(harness)
			Expect(err).ToNot(HaveOccurred())

			By("verifying the metrics endpoint remains unreachable")
			Consistently(
				harness.VMCommandOutputFunc(buildCurlReachabilityCommand(metricsEndpoint), true),
				"30s",
				e2e.POLLING,
			).Should(Equal("unreachable"))

			_, err = harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_metrics_endpoint_disabled.txt",
				buildCurlReachabilityCommand(metricsEndpoint),
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("should not expose agent pprof when profiling is disabled", Label("88809", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "agent-pprof-disabled")
			Expect(err).ToNot(HaveOccurred())

			By("disabling profiling in the agent config")
			cfg, err := harness.GetAgentConfig()
			Expect(err).ToNot(HaveOccurred())

			cfg.ProfilingEnabled = false

			err = harness.SetAgentConfig(cfg)
			Expect(err).ToNot(HaveOccurred())

			By("restarting the flightctl-agent service")
			err = restartFlightctlAgentAndWait(harness)
			Expect(err).ToNot(HaveOccurred())

			By("verifying the pprof endpoint remains unreachable")
			Consistently(
				harness.VMCommandOutputFunc(buildCurlReachabilityCommand(pprofEndpoint), true),
				"30s",
				e2e.POLLING,
			).Should(Equal("unreachable"))

			_, err = harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_endpoint_disabled.txt",
				buildCurlReachabilityCommand(pprofEndpoint),
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})

		It("should not write audit log entries when audit logging is disabled", Label("88810", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "agent-audit-log-disabled")
			Expect(err).ToNot(HaveOccurred())

			By("disabling audit logging in the agent config")
			cfg, err := harness.GetAgentConfig()
			Expect(err).ToNot(HaveOccurred())

			disabled := false
			cfg.AuditLog.Enabled = &disabled

			err = harness.SetAgentConfig(cfg)
			Expect(err).ToNot(HaveOccurred())

			By("removing any existing audit log and agent state")
			_, err = harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_remove_audit_log_and_state.txt",
				"sudo rm -f /var/log/flightctl/audit.log /var/log/flightctl/audit.log.* && sudo rm -rf /var/lib/flightctl/* /var/lib/flightctl/certs/*",
			)
			Expect(err).ToNot(HaveOccurred())

			By("restarting the flightctl-agent service")
			err = restartFlightctlAgentAndWait(harness)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for the device to re-enroll and become online")
			deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
			Expect(strings.TrimSpace(deviceID)).ToNot(BeEmpty())

			By("verifying the audit log file was not recreated")
			auditLogStatusOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_audit_log_disabled_status.txt",
				"sudo test -e /var/log/flightctl/audit.log && echo present || echo missing",
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(auditLogStatusOut)).To(Equal("missing"))

			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})
	})
})

func waitForAuditEntryWithEvidence(harness *e2e.Harness, artifactDir, filename, command string) {
	Eventually(func() string {
		out, err := harness.RunVMCommandWithEvidence(artifactDir, filename, command)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}, e2e.TIMEOUT, e2e.POLLING).ShouldNot(BeEmpty())
}

func buildAuditJQMatchCommand(deviceID string, filter string) string {
	return fmt.Sprintf(
		"sudo cat /var/log/flightctl/audit.log | jq -c 'select(.device==\"%s\" and %s)'",
		deviceID,
		filter,
	)
}

func extractEmbeddedAgentConfig(cliOut string) (string, error) {
	const yamlStart = "enrollment-service:"

	idx := strings.Index(cliOut, yamlStart)
	if idx == -1 {
		return "", fmt.Errorf("failed to find embedded config YAML in CLI output")
	}

	configYAML := strings.TrimSpace(cliOut[idx:])
	if configYAML == "" {
		return "", fmt.Errorf("embedded config YAML is empty")
	}

	var cfg agentcfg.Config
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		return "", fmt.Errorf("failed to parse embedded config YAML: %w", err)
	}

	return configYAML + "\n", nil
}

func restartFlightctlAgentAndWait(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}

	if err := harness.RestartFlightCtlAgent(); err != nil {
		return err
	}

	Eventually(func() string {
		output, err := harness.VM.RunSSH([]string{
			"sudo", "systemctl", "is-active", "flightctl-agent",
		}, nil)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(output.String())
	}, e2e.TIMEOUT, e2e.POLLING).Should(Equal("active"))

	return nil
}

func buildCurlCommand(url string) string {
	return fmt.Sprintf("curl -sS --fail %q", url)
}

func buildCurlReachabilityCommand(url string) string {
	return fmt.Sprintf("if curl -sS --fail --connect-timeout 2 %q >/dev/null 2>&1; then echo reachable; else echo unreachable; fi", url)
}

func buildPrimaryNonLoopbackIPCommand() string {
	return `hostname -I 2>/dev/null | tr ' ' '\n' | grep -m1 -vE '^(127\.|::1$|fe80:|$)' || ip route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([^ ]*\).*/\1/p' | head -n1 || ip -6 route get 2606:4700:4700::1111 2>/dev/null | sed -n 's/.* src \([^ ]*\).*/\1/p' | head -n1`
}

func buildNonLoopbackURL(host string, port int, path string) string {
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		return fmt.Sprintf("http://[%s]:%d%s", host, port, path)
	}
	return fmt.Sprintf("http://%s:%d%s", host, port, path)
}

func waitForEndpoint(harness *e2e.Harness, url string) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	Eventually(harness.VMCommandOutputFunc(buildCurlCommand(url), false), e2e.TIMEOUT, e2e.POLLING).ShouldNot(BeEmpty())
	return nil
}
