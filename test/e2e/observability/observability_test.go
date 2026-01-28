package observability_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	telemetryGatewayNamespace   = "flightctl-external"
	telemetryGatewayConfigMap   = "flightctl-telemetry-gateway-config"
	telemetryGatewayServiceName = "svc/flightctl-telemetry-gateway"
	telemetryGatewayMetricsPort = 9464
	prometheusServiceName       = "svc/flightctl-prometheus"
	prometheusService           = "flightctl-prometheus"
	prometheusPort              = 9090
	metricsEndpointPath         = "/metrics"
	telemetryGatewayConfigPath  = "jsonpath={.data.config\\.yaml}"
)

var _ = Describe("Device observability", func() {
	BeforeEach(func() {
		ctxStr, err := e2e.GetContext()
		if err != nil || ctxStr != util.KIND {
			Skip("KIND context required for telemetry gateway metrics")
		}
	})

	Context("telemetry gateway metrics", func() {
		It("should export device host metrics via the telemetry gateway", Label("85040"), func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()

			By("verifying telemetry gateway configuration exports Prometheus metrics")
			cfg, err := harness.GetConfigMapValue(telemetryGatewayNamespace, telemetryGatewayConfigMap, telemetryGatewayConfigPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg).To(ContainSubstring("prometheus"))
			Expect(cfg).To(ContainSubstring("listen"))
			Expect(cfg).To(ContainSubstring("logLevel"))
			Expect(cfg).To(ContainSubstring("tls:"))
			Expect(cfg).To(ContainSubstring("certFile"))
			Expect(cfg).To(ContainSubstring("keyFile"))
			Expect(cfg).To(ContainSubstring("caCert"))

			if !strings.Contains(cfg, "forward:") {
				Skip("telemetry gateway forward configuration is required for this test case")
			}
			Expect(cfg).To(ContainSubstring("endpoint"))
			Expect(cfg).To(ContainSubstring("insecureSkipTlsVerify"))
			Expect(cfg).To(ContainSubstring("caFile"))
			Expect(cfg).To(ContainSubstring("certFile"))
			Expect(cfg).To(ContainSubstring("keyFile"))

			By("enrolling a device and updating to the v10 image with OTEL collector")
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V10)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for otelcol to be running on the device")
			Eventually(harness.OTelcolActiveStatus(), TIMEOUT, POLLING).Should(Equal("active"))

			By("port-forwarding telemetry gateway metrics")
			localPort, err := harness.GetFreeLocalPort()
			Expect(err).ToNot(HaveOccurred())
			pfCleanup, err := harness.StartPortForwardWithCleanup(telemetryGatewayNamespace, telemetryGatewayServiceName, localPort, telemetryGatewayMetricsPort)
			Expect(err).ToNot(HaveOccurred())
			defer pfCleanup()

			By("verifying telemetry gateway metrics include device host metrics")
			metricsURL := fmt.Sprintf("http://127.0.0.1:%d%s", localPort, metricsEndpointPath)
			Eventually(harness.MetricsLineCount(metricsURL), TIMEOUT, POLLING).Should(BeNumerically(">", 0))

			states := []string{"idle", "interrupt", "nice"}
			requiredLabels := []string{"org_id", "otel_scope_schema_url", "otel_scope_version"}
			requiredNonEmpty := []string{"otel_scope_version"}
			for _, state := range states {
				exact := map[string]string{
					"cpu":             "cpu0",
					"device_id":       deviceId,
					"otel_scope_name": "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/hostmetricsreceiver/internal/scraper/cpuscraper",
					"state":           state,
				}
				Eventually(harness.MetricsMatchLabels(metricsURL, exact, requiredLabels, requiredNonEmpty), TIMEOUT, POLLING).Should(BeTrue())
			}

			By("verifying Prometheus queries return device metrics")
			err = harness.VerifyServiceExists(util.E2E_NAMESPACE, prometheusService)
			Expect(err).ToNot(HaveOccurred())

			promLocalPort, err := harness.GetFreeLocalPort()
			Expect(err).ToNot(HaveOccurred())
			promCleanup, err := harness.StartPortForwardWithCleanup(util.E2E_NAMESPACE, prometheusServiceName, promLocalPort, prometheusPort)
			Expect(err).ToNot(HaveOccurred())
			defer promCleanup()

			promURL := fmt.Sprintf("http://127.0.0.1:%d", promLocalPort)
			queryAll := fmt.Sprintf(`{device_id="%s"}`, deviceId)
			Eventually(harness.PromQueryResultCount(promURL, queryAll), TIMEOUT, POLLING).Should(BeNumerically(">", 0))

			By("verifying Prometheus returns CPU metrics with device and org labels")
			orgID, err := harness.GetOrganizationID()
			Expect(err).ToNot(HaveOccurred())
			Expect(orgID).ToNot(BeEmpty())
			queryCPU := fmt.Sprintf(`system_cpu_time_seconds_total{device_id="%s"}`, deviceId)
			Eventually(
				harness.PromQueryHasLabels(
					promURL,
					queryCPU,
					map[string]string{"device_id": deviceId, "org_id": orgID},
					nil,
				),
				TIMEOUT,
				POLLING,
			).Should(BeTrue())

			queryCount := fmt.Sprintf(`count({device_id="%s"})`, deviceId)
			Eventually(harness.PromQueryCountValue(promURL, queryCount), TIMEOUT, POLLING).Should(BeNumerically(">", 0))
		})
	})
})
