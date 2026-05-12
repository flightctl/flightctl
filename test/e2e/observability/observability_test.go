package observability_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/e2e/tpm"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	telemetryGatewayMetricsPort = 9464
	metricsEndpointPath         = "/metrics"
	telemetryGatewayConfigPath  = "jsonpath={.data.config\\.yaml}"
	fleetImage                  = "quay.io/redhat/rhde:9.2"
)

// getPrometheusURL returns the Prometheus URL from auxiliary.Services.
func getPrometheusURL() (string, error) {
	if auxSvcs == nil {
		return "", fmt.Errorf("aux services not initialized")
	}
	if auxSvcs.Prometheus == nil || auxSvcs.Prometheus.URL == "" {
		return "", fmt.Errorf("Prometheus not started")
	}
	return auxSvcs.Prometheus.URL, nil
}

var _ = Describe("Device observability", func() {
	BeforeEach(func() {
		p := setup.GetDefaultProviders()
		if p.Infra.GetEnvironmentType() != infra.EnvironmentKind {
			Skip("KIND context required for telemetry gateway metrics")
		}
	})

	Context("telemetry gateway metrics", func() {
		It("should export device host metrics via the telemetry gateway", Label("85040"), func() {
			harness := e2e.GetWorkerHarness()
			p := setup.GetDefaultProviders()
			workerID := GinkgoParallelProcess()

			By("setting up VM and starting agent")
			err := harness.SetupVMFromPoolAndStartAgent(workerID)
			Expect(err).ToNot(HaveOccurred())

			By("verifying telemetry gateway configuration exports Prometheus metrics")
			cfg, err := p.Infra.GetServiceConfig(infra.ServiceTelemetryGateway)
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
			err = harness.WaitForDeviceNewRenderedVersionWithReboot(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for otelcol to be running on the device")
			Eventually(harness.OTelcolActiveStatus(), TIMEOUT, POLLING).Should(Equal("active"))

			By("getting telemetry gateway metrics endpoint")
			baseURL, pfCleanup, err := p.Infra.ExposeService(infra.ServiceTelemetryGateway, "http")
			Expect(err).ToNot(HaveOccurred())
			metricsURL := baseURL + metricsEndpointPath
			defer pfCleanup()

			By("verifying telemetry gateway metrics include device host metrics")
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
			promURL, err := getPrometheusURL()
			Expect(err).ToNot(HaveOccurred())
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

	Context("TPM-based telemetry", func() {
		var (
			harness   *e2e.Harness
			providers *infra.Providers
			deviceId  string
		)
		BeforeEach(func() {
			harness = e2e.GetWorkerHarness()
			providers = setup.GetDefaultProviders()
			workerID := GinkgoParallelProcess()

			By("injecting swtpm CA certificates")
			err := tpm.InjectTPMCerts(harness.GetTestContext(), false)
			Expect(err).ToNot(HaveOccurred())

			By("setting up VM with software TPM")
			err = harness.SetupVMFromPoolWithTPM(workerID, e2e.TPMTypeSwtpm)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for enrollment request with TPM attestation")
			var enrollmentID string
			Eventually(func() error {
				enrollmentID = harness.GetEnrollmentIDFromServiceLogs("flightctl-agent")
				if enrollmentID == "" {
					return fmt.Errorf("enrollment ID not found in agent logs")
				}
				return nil
			}, TIMEOUT, POLLING).Should(Succeed())

			By("verifying TPM verified condition on enrollment request")
			Eventually(func() bool {
				resp, err := harness.Client.GetEnrollmentRequestWithResponse(harness.Context, enrollmentID)
				if err != nil || resp.JSON200 == nil || resp.JSON200.Status == nil {
					return false
				}
				cond := v1beta1.FindStatusCondition(resp.JSON200.Status.Conditions, v1beta1.ConditionTypeEnrollmentRequestTPMVerified)
				if cond == nil {
					return false
				}
				return cond.Status == v1beta1.ConditionStatusTrue
			}, LONGTIMEOUT, POLLING).Should(BeTrue(), "EnrollmentRequest should have TPMVerified condition set to True")

			By("approving enrollment and waiting for device online")
			deviceId, _ = harness.EnrollAndWaitForOnlineStatus()
		})
		AfterEach(func() {
			harness.PrintAgentLogsIfFailed()
		})
		It("should export device metrics using TPM-backed OTEL authentication", Label("85185", "tpm", "tpm-sw", "agent"), func() {
			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, util.DeviceTags.V10)
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersionWithReboot(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("configuring OTEL to use TPM for mTLS authentication")
			nextRenderedVersion, err = harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			otelTPMConfig := buildOTELTPMConfig()
			otelRestartHook := buildOTELRestartHook()

			var otelConfigSpec v1beta1.ConfigProviderSpec
			err = otelConfigSpec.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{
				Name:   "otel-tpm-config",
				Inline: []v1beta1.FileSpec{otelTPMConfig, otelRestartHook},
			})
			Expect(err).ToNot(HaveOccurred())

			err = harness.UpdateDeviceConfigWithRetries(deviceId, []v1beta1.ConfigProviderSpec{otelConfigSpec}, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			By("waiting for otelcol to restart with TPM configuration")
			Eventually(harness.OTelcolActiveStatus(), TIMEOUT, POLLING).Should(Equal("active"))

			By("getting telemetry gateway metrics endpoint")
			baseURL, pfCleanup, err := providers.Infra.ExposeService(infra.ServiceTelemetryGateway, "http")
			Expect(err).ToNot(HaveOccurred())
			metricsURL := baseURL + metricsEndpointPath
			defer pfCleanup()

			By("verifying telemetry gateway receives metrics from TPM-authenticated device")
			Eventually(harness.MetricsLineCount(metricsURL), TIMEOUT, POLLING).Should(BeNumerically(">", 0))

			exact := map[string]string{
				"cpu":       "cpu0",
				"device_id": deviceId,
				"state":     "idle",
			}
			requiredLabels := []string{"org_id", "otel_scope_version"}
			Eventually(harness.MetricsMatchLabels(metricsURL, exact, requiredLabels, nil), TIMEOUT, POLLING).Should(BeTrue())

			By("verifying Prometheus queries return metrics from TPM device")
			promURL, err := getPrometheusURL()
			Expect(err).ToNot(HaveOccurred())
			queryCPU := fmt.Sprintf(`system_cpu_time_seconds_total{device_id="%s"}`, deviceId)
			Eventually(harness.PromQueryResultCount(promURL, queryCPU), TIMEOUT, POLLING).Should(BeNumerically(">", 0))
		})
	})
})

var _ = Describe("Service observability", func() {
	BeforeEach(func() {
		p := setup.GetDefaultProviders()
		if p.Infra.GetEnvironmentType() != infra.EnvironmentKind {
			Skip("KIND context required for service observability metrics")
		}
	})

	Context("service level prometheus metrics", func() {
		It("should expose service level metrics via the prometheus server", Label("88170"), func() {
			harness := e2e.GetWorkerHarness()

			By("getting Prometheus URL from aux (testcontainer)")
			promURL, err := getPrometheusURL()
			Expect(err).ToNot(HaveOccurred())

			By("verifying service metrics exist")
			metrics := []string{
				"flightctl_cpu_utilization",
				"flightctl_memory_utilization",
				"flightctl_disk_utilization",
				"http_server_request_duration_seconds_bucket",
				"flightctl_repositories_total",
			}

			for _, query := range metrics {
				By(fmt.Sprintf("verifying metric %s", query))
				Eventually(harness.PromQueryResultCount(promURL, query), TIMEOUT, POLLING).Should(BeNumerically(">", 0))
			}

			By("creating test domain objects for detailed metrics verification")
			// Note: Prometheus works on a pull scrape model, so the initial request to get metrics
			// from created domain objects are dependent on async scrape windows and might take
			// upwards of 30 seconds to be available.
			fleetName := fmt.Sprintf("test-fleet-%s", harness.GetTestIDFromContext())
			_, err = resources.CreateFleet(harness, fleetName, fleetImage, &map[string]string{"fleet": fleetName})
			Expect(err).ToNot(HaveOccurred())

			deviceName := fmt.Sprintf("test-device-%s", harness.GetTestIDFromContext())
			_, err = resources.CreateDevice(harness, deviceName, &map[string]string{"fleet": fleetName})
			Expect(err).ToNot(HaveOccurred())

			By("verifying fleet metrics include our created fleet with correct labels")
			orgID, err := harness.GetOrganizationID()
			Expect(err).ToNot(HaveOccurred())

			fleetsLabelQuery := fmt.Sprintf(`flightctl_fleets{organization_id="%s"}`, orgID)
			fleetExactLabels := map[string]string{
				"organization_id": orgID,
			}
			fleetRequiredLabels := []string{"status", "organization_id"}
			Eventually(harness.PromQueryHasLabels(promURL, fleetsLabelQuery, fleetExactLabels, fleetRequiredLabels),
				TIMEOUT, POLLING).Should(BeTrue())

			By("verifying device summary metrics can be filtered by our created fleet")
			expectedFleetLabel := fmt.Sprintf("Fleet/%s", fleetName)
			deviceSummaryQuery := fmt.Sprintf(`flightctl_devices_summary{organization_id="%s",fleet="%s"}`, orgID, expectedFleetLabel)
			deviceLabels := map[string]string{
				"organization_id": orgID,
				"fleet":           expectedFleetLabel,
			}
			deviceMetricLabels := []string{"status", "organization_id", "fleet"}
			Eventually(
				harness.PromQueryHasLabels(
					promURL,
					deviceSummaryQuery,
					deviceLabels,
					deviceMetricLabels,
				),
				TIMEOUT,
				POLLING,
			).Should(BeTrue())

			By("verifying device application and update metrics support fleet filtering")
			deviceAppQuery := fmt.Sprintf(`flightctl_devices_application{organization_id="%s",fleet="%s"}`, orgID, expectedFleetLabel)
			deviceAppLabels := map[string]string{
				"organization_id": orgID,
				"fleet":           expectedFleetLabel,
			}
			Eventually(
				harness.PromQueryHasLabels(
					promURL,
					deviceAppQuery,
					deviceAppLabels,
					deviceMetricLabels,
				),
				TIMEOUT,
				POLLING,
			).Should(BeTrue())

			deviceUpdateQuery := fmt.Sprintf(`flightctl_devices_update{organization_id="%s",fleet="%s"}`, orgID, expectedFleetLabel)
			deviceUpdateLabels := map[string]string{
				"organization_id": orgID,
				"fleet":           expectedFleetLabel,
			}
			Eventually(
				harness.PromQueryHasLabels(
					promURL,
					deviceUpdateQuery,
					deviceUpdateLabels,
					deviceMetricLabels,
				),
				TIMEOUT,
				POLLING,
			).Should(BeTrue())
		})
	})
})

// buildOTELTPMConfig creates a FileSpec for OTEL config with TPM TLS settings.
func buildOTELTPMConfig() v1beta1.FileSpec {
	// A copy of the config built into the image, but with tpm enabled
	content := `receivers:
  hostmetrics:
    collection_interval: 10s
    scrapers: { cpu: {}, memory: {} }
  hostmetrics/disk:
    collection_interval: 1m
    scrapers: { disk: {}, filesystem: {} }

processors:
  batch: {}

exporters:
  otlp:
    endpoint: ${OTEL_GATEWAY}
    tls:
      ca_file:   /etc/otelcol/certs/gateway-ca.crt
      cert_file: /etc/otelcol/certs/otel.crt
      key_file:  /etc/otelcol/certs/otel.key
      insecure:  false
      tpm:
        enabled: true
        path: /dev/tpmrm0

service:
  pipelines:
    metrics:
      receivers: [hostmetrics, hostmetrics/disk]
      processors: [batch]
      exporters: [otlp]
`
	return v1beta1.FileSpec{
		Path:    "/etc/otelcol/config.yaml",
		Content: content,
		Mode:    lo.ToPtr(0640),
		User:    "root",
		Group:   "otelcol",
	}
}

// buildOTELRestartHook creates a FileSpec for an afterupdating hook that restarts otelcol.
func buildOTELRestartHook() v1beta1.FileSpec {
	content := `- if:
  - path: /etc/otelcol/config.yaml
    op: [created, updated]
  run: systemctl restart otelcol
`
	return v1beta1.FileSpec{
		Path:    "/etc/flightctl/hooks.d/afterupdating/otelcol-restart.yaml",
		Content: content,
		Mode:    lo.ToPtr(0644),
	}
}
