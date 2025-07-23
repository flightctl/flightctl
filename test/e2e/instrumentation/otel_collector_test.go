package agent_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("VM Agent behavior", func() {
	var (
		ctx     context.Context
		harness *e2e.Harness
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		harness = e2e.NewTestHarness(ctx)
		err := harness.VM.RunAndWaitForSSH()
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		// err := harness.CleanUpAllResources()
		// Expect(err).ToNot(HaveOccurred())
		// harness.Cleanup(true)
	})

	Context("status", func() {
		It("should report metrics with correct fleet label", func() {

			By("Enroll and wait for image v10 to become online")
			deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
			nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())
			_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceId, ":v10")
			Expect(err).ToNot(HaveOccurred())
			err = harness.WaitForDeviceNewRenderedVersion(deviceId, nextRenderedVersion)
			Expect(err).ToNot(HaveOccurred())

			const (
				fleet1Name  = "fleet1"
				fleet1Label = "region"
				fleet1Value = "world"
			)

			By("creating the first fleet")
			var configProviderSpec v1alpha1.ConfigProviderSpec
			err = configProviderSpec.FromInlineConfigProviderSpec(validInlineConfig)
			Expect(err).ToNot(HaveOccurred())
			err = harness.CreateTestFleetWithConfig(fleet1Name, v1alpha1.LabelSelector{
				MatchLabels: &map[string]string{
					fleet1Label: fleet1Value,
				},
			}, configProviderSpec)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = harness.DeleteFleet(fleet1Name) }()

			By("setting a label for a device, making it owned by the first fleet")
			err = harness.SetLabelsForDevice(deviceId, map[string]string{
				fleet1Label: fleet1Value,
			})
			Expect(err).ToNot(HaveOccurred())
			harness.WaitForDeviceContents(deviceId, "device should be owned by the first fleet", func(device *v1alpha1.Device) bool {
				return device.Metadata.Owner != nil && strings.Contains(*device.Metadata.Owner, fleet1Name)
			}, TIMEOUT)

			By("verifying otel-collector pod is running")
			Expect(isOtelCollectorPodReady()).To(BeTrue(), "Otel-collector pod should be ready")

			By("check that otel-collector is receiving metrics")
			//check http
			Eventually(func() (string, error) {
				response, err := harness.VM.RunSSH([]string{"curl", "http://localhost:8889/metrics"}, nil)
				if err != nil {
					return "", fmt.Errorf("failed to curl metrics endpoint: %w", err)
				}
				return response.String(), nil
			}, TIMEOUT, POLLING).Should(ContainSubstring("flightctl_system_cpu_load_average_15m"))
			By("check remote otel-collector is receiving metrics , with fleet label")
			Eventually(func() (string, error) {
				response, err := harness.SH("curl", "http://localhost:9464/metrics")
				if err != nil {
					return "", fmt.Errorf("failed to curl remote metrics endpoint: %w", err)
				}
				return response, nil
			}, TIMEOUT, POLLING).ShouldNot(BeEmpty())

		})
	})
})

// isOtelCollectorPodReady checks if the otel-collector pod is in Ready state
func isOtelCollectorPodReady() bool {
	cmd := exec.Command("kubectl", "get", "pods", "-l", "flightctl.service=flightctl-otel-collector", "-n", "flightctl-external", "-o", "jsonpath={.items[0].status.containerStatuses[0].ready}")
	output, err := cmd.Output()
	return err == nil && string(output) == "true"
}

// mode defines the file permission bits, commonly used in Unix systems for files and directories.
var mode = 0644
var modePointer = &mode

// inlineConfig defines a file specification with content, mode, and path for provisioning system files.
var inlineConfig = v1alpha1.FileSpec{
	Content: `
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
  hostmetrics:
    collection_interval: 3s
    scrapers:
      cpu:
      disk:
      load:
      memory:
      network:
      paging:
      process:

processors:
  batch:

exporters:
  # For debugging: exports data to standard output, which systemd captures.
  # You can view logs with: journalctl -u otel-collector -f
  debug:
    verbosity: detailed
  
  # OTLP exporter to send data to another collector with mTLS
  otlp:
    endpoint: "10.100.102.70:4317"
    tls:
      ca_file: /etc/flightctl/certs/ca.crt
      cert_file: /etc/otel-collector/certs/otel-collector.crt
      key_file: /etc/otel-collector/certs/otel-collector.key
      insecure: false
  
  # Prometheus exporter for testing - exposes metrics on HTTP endpoint
  prometheus:
    endpoint: "0.0.0.0:8889"
    namespace: "flightctl"

service:
  pipelines:
    metrics:
      receivers: [otlp,hostmetrics]
      processors: [batch]
      exporters: [debug, prometheus,otlp]`,
	Mode: modePointer,
	Path: "/etc/otelcol/config.yaml",
}

// validInlineConfig defines a valid inline configuration provider spec with pre-defined file specs and a name.
var validInlineConfig = v1alpha1.InlineConfigProviderSpec{
	Inline: []v1alpha1.FileSpec{inlineConfig},
	Name:   "valid-inline-config",
}
