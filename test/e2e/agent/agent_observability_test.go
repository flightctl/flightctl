package agent_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gopkg.in/yaml.v3"
)

const (
	agentConfigBackupPath = "/tmp/flightctl-config.yaml.bak"

	metricsEndpoint = "http://127.0.0.1:15690/metrics"
	pprofEndpoint   = "http://127.0.0.1:15689/debug/pprof/"

	serviceActiveTimeout = "10s"
)

var _ = Describe("Agent observability and diagnostics", func() {
	var (
		harness  *e2e.Harness
		deviceID string
	)

	BeforeEach(func() {
		var err error

		harness = e2e.GetWorkerHarness()
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(deviceID)).ToNot(BeEmpty())

		err = backupAgentConfig(harness)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		Expect(restoreAgentConfig(harness)).To(Succeed())
		Expect(restartFlightctlAgentAndWait(harness)).To(Succeed())
	})

	Context("when local observability endpoints are enabled", func() {
		It("86397 should expose agent metrics on the loopback endpoint", Label("86397", "sanity", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "agent-metrics")
			Expect(err).ToNot(HaveOccurred())

			By("enabling metrics in the agent config")
			err = appendAgentObservabilityConfig(harness, observabilityConfig{
				MetricsEnabled: lo.ToPtr(true),
			})
			Expect(err).ToNot(HaveOccurred())

			By("restarting the flightctl-agent service")
			err = restartFlightctlAgentAndWait(harness)
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

		It("86399 should expose pprof endpoints on the loopback endpoint", Label("86399", "agent"), func() {
			artifactDir, err := harness.SetupScenario(deviceID, "agent-pprof")
			Expect(err).ToNot(HaveOccurred())

			By("enabling profiling in the agent config")
			err = appendAgentObservabilityConfig(harness, observabilityConfig{
				ProfilingEnabled: lo.ToPtr(true),
			})
			Expect(err).ToNot(HaveOccurred())

			By("restarting the flightctl-agent service")
			err = restartFlightctlAgentAndWait(harness)
			Expect(err).ToNot(HaveOccurred())

			By("querying the local pprof index endpoint from inside the VM")
			indexOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_index.txt",
				buildCurlCommand(pprofEndpoint),
			)
			Expect(err).ToNot(HaveOccurred())

			By("verifying the pprof index is reachable and lists the expected profiles")
			Expect(indexOut).To(ContainSubstring("/debug/pprof/"))
			Expect(indexOut).To(ContainSubstring("goroutine"))
			Expect(indexOut).To(ContainSubstring("heap"))
			Expect(indexOut).To(ContainSubstring("profile"))
			Expect(indexOut).To(ContainSubstring("trace"))

			By("verifying the heap profile endpoint")
			heapOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_heap.txt",
				buildCurlCommand(pprofEndpoint+"heap"),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(heapOut)).ToNot(BeEmpty())

			By("verifying the goroutine dump endpoint with debug=2")
			goroutineOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_goroutine_debug2.txt",
				buildCurlCommand(pprofEndpoint+"goroutine?debug=2"),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(goroutineOut).To(ContainSubstring("goroutine"))
			Expect(goroutineOut).To(ContainSubstring("flightctl-agent"))

			By("verifying the CPU profile endpoint is reachable with a short duration")
			profileOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_profile_seconds1.txt",
				buildCurlCommand(pprofEndpoint+"profile?seconds=1"),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(profileOut)).ToNot(BeEmpty())

			By("verifying the trace endpoint is reachable with a short duration")
			traceOut, err := harness.RunVMCommandWithEvidence(
				artifactDir,
				"vm_pprof_trace_seconds1.txt",
				buildCurlCommand(pprofEndpoint+"trace?seconds=1"),
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.TrimSpace(traceOut)).ToNot(BeEmpty())

			By("verifying the remaining pprof endpoints are reachable")
			for _, endpoint := range []string{
				"allocs",
				"block",
				"cmdline",
				"heap",
				"mutex",
				"symbol",
				"threadcreate",
			} {
				out, err := harness.RunVMCommandWithEvidence(
					artifactDir,
					fmt.Sprintf("vm_pprof_%s.txt", endpoint),
					buildCurlCommand(pprofEndpoint+endpoint),
				)
				Expect(err).ToNot(HaveOccurred(), "endpoint %s should be reachable", endpoint)
				Expect(strings.TrimSpace(out)).ToNot(BeEmpty(), "endpoint %s should return content", endpoint)
			}

			Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
		})
	})
})

type observabilityConfig struct {
	MetricsEnabled   *bool `yaml:"metrics-enabled,omitempty"`
	ProfilingEnabled *bool `yaml:"profiling-enabled,omitempty"`
}

func appendAgentObservabilityConfig(harness *e2e.Harness, cfg observabilityConfig) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal observability config: %w", err)
	}

	cmd := fmt.Sprintf(
		"cat <<'CONFIGEOF' | sudo tee -a %s >/dev/null\n%sCONFIGEOF",
		agentConfigPath,
		string(out),
	)

	_, err = harness.VM.RunSSH([]string{"bash", "-lc", cmd}, nil)
	if err != nil {
		return fmt.Errorf("failed appending observability config: %w", err)
	}

	return nil
}

func backupAgentConfig(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}

	_, err := harness.VM.RunSSH([]string{
		"sudo", "cp", agentConfigPath, agentConfigBackupPath,
	}, nil)
	if err != nil {
		return fmt.Errorf("failed backing up agent config: %w", err)
	}

	return nil
}

func restoreAgentConfig(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}

	_, err := harness.VM.RunSSH([]string{
		"sudo", "cp", agentConfigBackupPath, agentConfigPath,
	}, nil)
	if err != nil {
		return fmt.Errorf("failed restoring agent config: %w", err)
	}

	_, err = harness.VM.RunSSH([]string{
		"sudo", "rm", "-f", agentConfigBackupPath,
	}, nil)
	if err != nil {
		return fmt.Errorf("failed removing backup agent config: %w", err)
	}

	return nil
}

func restartFlightctlAgentAndWait(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}

	_, err := harness.VM.RunSSH([]string{
		"sudo", "systemctl", "restart", "flightctl-agent",
	}, nil)
	if err != nil {
		return fmt.Errorf("failed restarting flightctl-agent: %w", err)
	}

	Eventually(func() string {
		output, err := harness.VM.RunSSH([]string{
			"sudo", "systemctl", "is-active", "flightctl-agent",
		}, nil)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(output.String())
	}, serviceActiveTimeout, e2e.POLLING).Should(Equal("active"))

	return nil
}

func buildCurlCommand(url string) string {
	return "curl -sS --fail " + url
}

func buildCurlHeadersCommand(url string) string {
	return "curl -sS --fail -D - -o /dev/null " + url
}
