package agent_test

import (
	"fmt"
	"strings"

	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	metricsEndpoint = "http://127.0.0.1:15690/metrics"
	pprofEndpoint   = "http://127.0.0.1:15689/debug/pprof/"

	serviceActiveTimeout = "10s"
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
		Expect(err).ToNot(HaveOccurred())
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
	})
})

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
	}, serviceActiveTimeout, e2e.POLLING).Should(Equal("active"))

	return nil
}

func buildCurlCommand(url string) string {
	return "curl -sS --fail " + url
}

func waitForEndpoint(harness *e2e.Harness, url string) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	Eventually(harness.VMCommandOutputFunc(buildCurlCommand(url), false), serviceActiveTimeout, POLLING).ShouldNot(BeEmpty())
	return nil
}
