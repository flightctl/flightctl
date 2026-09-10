package observability_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

const (
	telemetryGatewayConfigKey             = "config.yaml"
	telemetryHTTPMetricsPath              = "/v1/metrics"
	telemetryHTTPExampleEndpoint          = "https://collector.example.com/api/v2/otlp"
	telemetryForwardEnvName               = "E2E_FORWARD_TOKEN"
	telemetryForwardMissingEnvName        = "E2E_MISSING_FORWARD_TOKEN"
	telemetryForwardInvalidQueryParameter = "unsupported=true"
	telemetryGatewayLogTailLines          = 200
	telemetryMissingEnvError              = "undefined environment variable: " + telemetryForwardMissingEnvName
	telemetryQueryStringError             = "query strings are not supported"
	telemetryGRPCHeadersError             = "forward headers are only supported for http(s) endpoints"
)

type telemetryGatewayConfigSchema struct {
	LogLevel string                         `yaml:"logLevel,omitempty"`
	TLS      *telemetryGatewayTLSSchema     `yaml:"tls,omitempty"`
	Listen   *telemetryGatewayListenSchema  `yaml:"listen,omitempty"`
	Export   *telemetryGatewayExportSchema  `yaml:"export,omitempty"`
	Forward  *telemetryGatewayForwardConfig `yaml:"forward,omitempty"`
}

type telemetryGatewayTLSSchema struct {
	CertFile string `yaml:"certFile,omitempty"`
	KeyFile  string `yaml:"keyFile,omitempty"`
	CACert   string `yaml:"caCert,omitempty"`
}

type telemetryGatewayListenSchema struct {
	Device string `yaml:"device,omitempty"`
}

type telemetryGatewayExportSchema struct {
	Prometheus string `yaml:"prometheus,omitempty"`
}

type telemetryGatewayForwardConfig struct {
	Endpoint string                            `yaml:"endpoint,omitempty"`
	Headers  map[string]string                 `yaml:"headers,omitempty"`
	TLS      *telemetryGatewayForwardTLSConfig `yaml:"tls,omitempty"`
}

type telemetryGatewayForwardTLSConfig struct {
	InsecureSkipTLSVerify bool   `yaml:"insecureSkipTlsVerify,omitempty"`
	CAFile                string `yaml:"caFile,omitempty"`
	CertFile              string `yaml:"certFile,omitempty"`
	KeyFile               string `yaml:"keyFile,omitempty"`
}

var _ = Describe("Telemetry gateway forwarding", Serial, Label("observability"), func() {
	var (
		harness           *e2e.Harness
		providers         *infra.Providers
		serviceLogs       infra.ServiceLogProvider
		collector         *auxiliary.TelemetryHTTPCollector
		grpcCollector     *auxiliary.TelemetryGRPCCollector
		originalTGConfig  string
		deviceID          string
		staticAuthValue   string
		staticHeaderValue string
		envTokenValue     string
		envAuthValue      string
	)

	BeforeEach(func(ctx SpecContext) {
		var err error
		harness = e2e.GetWorkerHarness()
		providers = setup.GetDefaultProviders()
		Expect(providers).ToNot(BeNil())
		Expect(providers.Infra).ToNot(BeNil())
		Expect(providers.Lifecycle).ToNot(BeNil())

		var ok bool
		serviceLogs, ok = providers.Infra.(infra.ServiceLogProvider)
		Expect(ok).To(BeTrue(), "infra provider should expose service logs")

		originalTGConfig, err = providers.Infra.GetServiceConfig(infra.ServiceTelemetryGateway)
		Expect(err).ToNot(HaveOccurred())
		Expect(originalTGConfig).To(ContainSubstring("telemetryGateway"))
		DeferCleanup(restoreTelemetryGatewayConfig, providers, originalTGConfig)

		staticAuthValue, err = runtimeHeaderValue("Api-Token")
		Expect(err).ToNot(HaveOccurred())
		staticHeaderValue, err = runtimeHeaderValue("custom")
		Expect(err).ToNot(HaveOccurred())
		envTokenValue, err = runtimeHeaderValue("env")
		Expect(err).ToNot(HaveOccurred())
		envAuthValue = "Api-Token " + envTokenValue
		deviceID = ""
	})

	AfterEach(func() {
		if grpcCollector != nil {
			Expect(grpcCollector.Stop(context.Background())).To(Succeed())
			grpcCollector = nil
		}
		if collector != nil {
			Expect(auxiliary.StopServices([]auxiliary.Service{auxiliary.ServiceTelemetryHTTPCollector})).To(Succeed())
			collector = nil
		}
	})

	It("should forward Flight Control device telemetry over OTLP/HTTP with custom headers", Label("90533", "sanity", "agent"), func(ctx SpecContext) {
		var err error
		By("enrolling a device and enabling the OTEL collector image")
		deviceID, err = ensureOTelDevice(ctx, harness, deviceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(deviceID).ToNot(BeEmpty())

		By("starting the OTLP/HTTP mock collector")
		collector, err = auxiliary.StartTelemetryHTTPCollector(ctx, TENMINTIMEOUT)
		Expect(err).ToNot(HaveOccurred())
		Expect(collector.Endpoint).To(ContainSubstring(telemetryHTTPMetricsPath))

		By("configuring telemetry gateway HTTP forwarding")
		updated, err := telemetryGatewayConfigWithForward(originalTGConfig, telemetryGatewayForwardConfig{
			Endpoint: collector.Endpoint,
			Headers: map[string]string{
				"Authorization":             staticAuthValue,
				"X-Custom-Header":           staticHeaderValue,
				"X-Expected-Body-Substring": deviceID,
			},
		})
		Expect(err).ToNot(HaveOccurred())
		endpoint, headerNames, err := telemetryGatewayForwardMetadata(updated)
		Expect(err).ToNot(HaveOccurred())
		Expect(endpoint).To(Equal(collector.Endpoint))
		Expect(headerNames).To(ContainElements("Authorization", "X-Custom-Header", "X-Expected-Body-Substring"))
		Expect(setTelemetryGatewayConfigAndRestart(providers, updated)).To(Succeed())
		Expect(providers.Lifecycle.WaitForReady(infra.ServiceTelemetryGateway, TENSECTIMEOUT)).To(Succeed())

		By("verifying the HTTP collector received the device telemetry and headers")
		Eventually(collectorLogsContain(
			ctx,
			collector,
			"OTLP_HTTP_PATH="+telemetryHTTPMetricsPath,
			"AUTHORIZATION_PRESENT=true",
			"AUTHORIZATION_SHA256="+auxiliary.HeaderSHA256(staticAuthValue),
			"CUSTOM_HEADER_SHA256="+auxiliary.HeaderSHA256(staticHeaderValue),
			"EXPECTED_BODY_MATCH=true",
		), TIMEOUT, POLLING).Should(BeTrue())
		logs, err := collectorLogs(ctx, collector)
		Expect(err).ToNot(HaveOccurred())
		Expect(logs).To(ContainSubstring("BODY_BYTES="))
	})

	It("should forward device telemetry over OTLP/gRPC with a bare host and port", Label("90560", "sanity", "agent"), func(ctx SpecContext) {
		var err error
		By("enrolling a device and enabling the OTEL collector image")
		deviceID, err = ensureOTelDevice(ctx, harness, deviceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(deviceID).ToNot(BeEmpty())

		By("starting a real OTLP/gRPC metrics collector")
		grpcCollector, err = auxiliary.StartTelemetryGRPCCollector(ctx, TENMINTIMEOUT)
		Expect(err).ToNot(HaveOccurred())
		_, _, err = net.SplitHostPort(grpcCollector.Endpoint)
		Expect(err).ToNot(HaveOccurred())

		By("configuring telemetry gateway gRPC forwarding with a bare endpoint")
		updated, err := telemetryGatewayConfigWithForward(originalTGConfig, telemetryGatewayForwardConfig{
			Endpoint: grpcCollector.Endpoint,
			TLS:      &telemetryGatewayForwardTLSConfig{InsecureSkipTLSVerify: true},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(setTelemetryGatewayConfigAndRestart(providers, updated)).To(Succeed())
		Expect(providers.Lifecycle.WaitForReady(infra.ServiceTelemetryGateway, TENSECTIMEOUT)).To(Succeed())

		By("verifying the gRPC collector received the enrolled device metric")
		Eventually(grpcCollectorContains(ctx, grpcCollector, deviceID), TIMEOUT, POLLING).Should(BeTrue())
	})

	It("should expand environment variables in OTLP/HTTP forward headers", Label("90534", "sanity", "agent"), func(ctx SpecContext) {
		var err error
		deviceID, err = ensureOTelDevice(ctx, harness, deviceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(deviceID).ToNot(BeEmpty())

		By("starting the OTLP/HTTP mock collector")
		collector, err = auxiliary.StartTelemetryHTTPCollector(ctx, TENMINTIMEOUT)
		Expect(err).ToNot(HaveOccurred())
		Expect(collector.Endpoint).To(ContainSubstring(telemetryHTTPMetricsPath))

		By("configuring a telemetry gateway environment variable")
		Expect(providers.Lifecycle.SetDeploymentEnv(infra.ServiceTelemetryGateway, telemetryForwardEnvName, envTokenValue)).To(Succeed())
		DeferCleanup(cleanupTelemetryGatewayEnv, providers, originalTGConfig, telemetryForwardEnvName)

		By("configuring the forward header to reference the environment variable")
		updated, err := telemetryGatewayConfigWithForward(originalTGConfig, telemetryGatewayForwardConfig{
			Endpoint: collector.Endpoint,
			Headers: map[string]string{
				"Authorization":             "Api-Token ${" + telemetryForwardEnvName + "}",
				"X-Expected-Body-Substring": deviceID,
			},
		})
		Expect(err).ToNot(HaveOccurred())
		endpoint, headerNames, err := telemetryGatewayForwardMetadata(updated)
		Expect(err).ToNot(HaveOccurred())
		Expect(endpoint).To(Equal(collector.Endpoint))
		Expect(headerNames).To(ContainElements("Authorization", "X-Expected-Body-Substring"))
		Expect(setTelemetryGatewayConfigAndRestart(providers, updated)).To(Succeed())
		Expect(providers.Lifecycle.WaitForReady(infra.ServiceTelemetryGateway, TENSECTIMEOUT)).To(Succeed())

		By("verifying the collector received the expanded token")
		Eventually(collectorLogsContain(
			ctx,
			collector,
			"AUTHORIZATION_PRESENT=true",
			"AUTHORIZATION_SHA256="+auxiliary.HeaderSHA256(envAuthValue),
			"EXPECTED_BODY_MATCH=true",
		), TIMEOUT, POLLING).Should(BeTrue())
		logs, err := collectorLogs(ctx, collector)
		Expect(err).ToNot(HaveOccurred())
		Expect(logs).ToNot(ContainSubstring("${" + telemetryForwardEnvName + "}"))
	})

	It("should fail startup when a forward header references an undefined environment variable", Label("90535", "sanity"), func(ctx SpecContext) {
		By("configuring telemetry gateway with a missing header environment variable")
		updated, err := telemetryGatewayConfigWithForward(originalTGConfig, telemetryGatewayForwardConfig{
			Endpoint: telemetryHTTPExampleEndpoint,
			Headers: map[string]string{
				"Authorization": "Api-Token ${" + telemetryForwardMissingEnvName + "}",
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(providers.Infra.SetServiceConfig(infra.ServiceTelemetryGateway, telemetryGatewayConfigKey, updated)).To(Succeed())
		Expect(providers.Lifecycle.Restart(infra.ServiceTelemetryGateway)).To(Succeed())

		By("verifying telemetry gateway logs the undefined variable and does not become ready")
		logs, err := waitForTelemetryGatewayFailureLogs(ctx, providers, serviceLogs, TENSECTIMEOUT, telemetryGatewayLogTailLines, telemetryMissingEnvError)
		Expect(err).ToNot(HaveOccurred())
		Expect(logs).To(ContainSubstring(telemetryMissingEnvError))
	})

	It("should accept a bare host:port forward endpoint for gRPC backward compatibility", Label("90536", "sanity"), func(ctx SpecContext) {
		By("configuring telemetry gateway with a bare endpoint")
		updated, err := telemetryGatewayConfigWithForward(originalTGConfig, telemetryGatewayForwardConfig{
			Endpoint: "collector.example.com:4317",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(setTelemetryGatewayConfigAndRestart(providers, updated)).To(Succeed())
		Expect(providers.Lifecycle.WaitForReady(infra.ServiceTelemetryGateway, TENSECTIMEOUT)).To(Succeed())

		By("verifying the gateway did not reject the bare endpoint as HTTP")
		ioCtx, cancel := context.WithTimeout(ctx, FIVESECTIMEOUT)
		defer cancel()
		logs, err := serviceLogs.GetServiceLogs(ioCtx, infra.ServiceTelemetryGateway, telemetryGatewayLogTailLines)
		Expect(err).ToNot(HaveOccurred())
		Expect(logs).ToNot(ContainSubstring("missing host"))
		Expect(logs).ToNot(ContainSubstring(telemetryQueryStringError))
		Expect(logs).ToNot(ContainSubstring(telemetryGRPCHeadersError))
	})

	It("should reject HTTP forward endpoints with query strings", Label("90537", "sanity"), func(ctx SpecContext) {
		By("configuring telemetry gateway with an unsupported query string endpoint")
		updated, err := telemetryGatewayConfigWithForward(originalTGConfig, telemetryGatewayForwardConfig{
			Endpoint: telemetryHTTPExampleEndpoint + "?" + telemetryForwardInvalidQueryParameter,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(providers.Infra.SetServiceConfig(infra.ServiceTelemetryGateway, telemetryGatewayConfigKey, updated)).To(Succeed())
		Expect(providers.Lifecycle.Restart(infra.ServiceTelemetryGateway)).To(Succeed())

		By("verifying telemetry gateway rejects the unsupported query string")
		logs, err := waitForTelemetryGatewayFailureLogs(ctx, providers, serviceLogs, TENSECTIMEOUT, telemetryGatewayLogTailLines, telemetryQueryStringError)
		Expect(err).ToNot(HaveOccurred())
		Expect(logs).To(ContainSubstring(telemetryQueryStringError))
	})

	It("should reject custom forward headers on bare gRPC endpoints", Label("90538", "sanity"), func(ctx SpecContext) {
		By("configuring telemetry gateway with a bare endpoint and HTTP headers")
		updated, err := telemetryGatewayConfigWithForward(originalTGConfig, telemetryGatewayForwardConfig{
			Endpoint: "collector.example.com:4317",
			Headers: map[string]string{
				"X-Custom-Header": staticHeaderValue,
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(providers.Infra.SetServiceConfig(infra.ServiceTelemetryGateway, telemetryGatewayConfigKey, updated)).To(Succeed())
		Expect(providers.Lifecycle.Restart(infra.ServiceTelemetryGateway)).To(Succeed())

		By("verifying telemetry gateway rejects headers for the gRPC forward path")
		logs, err := waitForTelemetryGatewayFailureLogs(ctx, providers, serviceLogs, TENSECTIMEOUT, telemetryGatewayLogTailLines, telemetryGRPCHeadersError)
		Expect(err).ToNot(HaveOccurred())
		Expect(logs).To(ContainSubstring(telemetryGRPCHeadersError))
	})
})

// runtimeHeaderValue generates a unique test value without storing reusable credentials in source.
func runtimeHeaderValue(prefix string) (string, error) {
	if strings.TrimSpace(prefix) == "" {
		return "", fmt.Errorf("header value prefix is empty")
	}
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate runtime header value: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(randomBytes), nil
}

// collectorLogsContain polls the mock OTLP/HTTP collector logs for all requested substrings.
func collectorLogsContain(ctx context.Context, collector *auxiliary.TelemetryHTTPCollector, substrings ...string) func() bool {
	return func() bool {
		ioCtx, cancel := context.WithTimeout(ctx, FIVESECTIMEOUT)
		defer cancel()
		found, logs, err := collector.LogsContain(ioCtx, substrings...)
		if err != nil {
			GinkgoWriter.Printf("collector logs unavailable: %v\n", err)
			return false
		}
		if !found {
			GinkgoWriter.Printf("collector logs do not yet contain %q; current logs:\n%s\n", strings.Join(substrings, ", "), logs)
		}
		return found
	}
}

// collectorLogs returns collector output using a short context bound for the log stream.
func collectorLogs(ctx context.Context, collector *auxiliary.TelemetryHTTPCollector) (string, error) {
	ioCtx, cancel := context.WithTimeout(ctx, FIVESECTIMEOUT)
	defer cancel()
	return collector.Logs(ioCtx)
}

// grpcCollectorContains polls the bounded OTLP/gRPC receiver history for a value.
func grpcCollectorContains(ctx context.Context, collector *auxiliary.TelemetryGRPCCollector, value string) func() bool {
	return func() bool {
		ioCtx, cancel := context.WithTimeout(ctx, FIVESECTIMEOUT)
		defer cancel()
		found, err := collector.MetricsContain(ioCtx, value)
		if err != nil {
			GinkgoWriter.Printf("gRPC collector metrics unavailable: %v\n", err)
			return false
		}
		return found
	}
}

// removeTelemetryGatewayEnv removes a temporary telemetry gateway environment variable.
func removeTelemetryGatewayEnv(providers *infra.Providers, envName string) error {
	if providers == nil || providers.Lifecycle == nil {
		return fmt.Errorf("providers lifecycle is nil")
	}
	return providers.Lifecycle.RemoveDeploymentEnv(infra.ServiceTelemetryGateway, envName)
}

// cleanupTelemetryGatewayEnv restores the gateway config before removing an
// environment variable that the test config may reference.
func cleanupTelemetryGatewayEnv(providers *infra.Providers, originalConfig, envName string) error {
	if err := restoreTelemetryGatewayConfig(providers, originalConfig); err != nil {
		return err
	}
	return removeTelemetryGatewayEnv(providers, envName)
}

// restoreTelemetryGatewayConfig restores the telemetry gateway configuration and waits for readiness.
func restoreTelemetryGatewayConfig(providers *infra.Providers, originalConfig string) error {
	if providers == nil || providers.Infra == nil || providers.Lifecycle == nil {
		return fmt.Errorf("providers are not initialized")
	}
	current, err := providers.Infra.GetServiceConfig(infra.ServiceTelemetryGateway)
	if err != nil {
		return fmt.Errorf("read current telemetry gateway config: %w", err)
	}
	if current == originalConfig {
		return nil
	}
	if err := providers.Infra.SetServiceConfig(infra.ServiceTelemetryGateway, telemetryGatewayConfigKey, originalConfig); err != nil {
		return fmt.Errorf("restore telemetry gateway config: %w", err)
	}
	if err := providers.Lifecycle.Restart(infra.ServiceTelemetryGateway); err != nil {
		return fmt.Errorf("restart telemetry gateway after config restore: %w", err)
	}
	return providers.Lifecycle.WaitForReady(infra.ServiceTelemetryGateway, TENMINTIMEOUT)
}

// setTelemetryGatewayConfigAndRestart writes telemetry gateway config and restarts the service.
func setTelemetryGatewayConfigAndRestart(providers *infra.Providers, config string) error {
	if providers == nil || providers.Infra == nil || providers.Lifecycle == nil {
		return fmt.Errorf("providers are not initialized")
	}
	if strings.TrimSpace(config) == "" {
		return fmt.Errorf("telemetry gateway config is empty")
	}
	if err := providers.Infra.SetServiceConfig(infra.ServiceTelemetryGateway, telemetryGatewayConfigKey, config); err != nil {
		return fmt.Errorf("set telemetry gateway config: %w", err)
	}
	return providers.Lifecycle.Restart(infra.ServiceTelemetryGateway)
}

// telemetryGatewayConfigWithForward returns a copy of the service config with
// only telemetryGateway.forward replaced.
func telemetryGatewayConfigWithForward(original string, forward telemetryGatewayForwardConfig) (string, error) {
	if strings.TrimSpace(original) == "" {
		return "", fmt.Errorf("original telemetry gateway config is empty")
	}
	if err := validateTelemetryGatewayServiceConfig(original); err != nil {
		return "", err
	}
	if err := validateTelemetryGatewayForwardConfig(forward); err != nil {
		return "", err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(original), &doc); err != nil {
		return "", fmt.Errorf("parse telemetry gateway service config: %w", err)
	}
	root, err := telemetryGatewayRootNode(&doc)
	if err != nil {
		return "", err
	}
	tg, err := telemetryGatewaySectionNode(root)
	if err != nil {
		return "", err
	}
	forwardNode, err := yamlNodeForForwardConfig(forward)
	if err != nil {
		return "", err
	}
	replaceMappingValue(tg, "forward", forwardNode)

	var b bytes.Buffer
	encoder := yaml.NewEncoder(&b)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return "", fmt.Errorf("marshal telemetry gateway service config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("close telemetry gateway service config encoder: %w", err)
	}
	return b.String(), nil
}

// validateTelemetryGatewayServiceConfig validates the gateway fields this test
// rewrites without rejecting unrelated fields added by newer deployments.
func validateTelemetryGatewayServiceConfig(original string) error {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(original), &doc); err != nil {
		return fmt.Errorf("parse telemetry gateway service config: %w", err)
	}
	root, err := telemetryGatewayRootNode(&doc)
	if err != nil {
		return err
	}
	if telemetryGatewaySectionCount(root) > 1 {
		return fmt.Errorf("multiple telemetry gateway config sections found")
	}
	tg, err := telemetryGatewaySectionNode(root)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	encoder := yaml.NewEncoder(&b)
	if err := encoder.Encode(tg); err != nil {
		return fmt.Errorf("marshal telemetry gateway section for validation: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close telemetry gateway section validator encoder: %w", err)
	}
	decoder := yaml.NewDecoder(&b)
	decoder.KnownFields(false)
	var cfg telemetryGatewayConfigSchema
	if err := decoder.Decode(&cfg); err != nil {
		return fmt.Errorf("validate telemetry gateway service config schema: %w", err)
	}
	return validateTelemetryGatewayForwardNode(tg)
}

// validateTelemetryGatewayForwardNode strictly validates the forward section
// that this test replaces while allowing unrelated gateway fields to evolve.
func validateTelemetryGatewayForwardNode(tg *yaml.Node) error {
	forwardNode := mappingValue(tg, "forward")
	if forwardNode == nil {
		return nil
	}
	var b bytes.Buffer
	encoder := yaml.NewEncoder(&b)
	if err := encoder.Encode(forwardNode); err != nil {
		return fmt.Errorf("marshal telemetry gateway forward section for validation: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close telemetry gateway forward validator encoder: %w", err)
	}
	decoder := yaml.NewDecoder(&b)
	decoder.KnownFields(true)
	var forward telemetryGatewayForwardConfig
	if err := decoder.Decode(&forward); err != nil {
		return fmt.Errorf("validate telemetry gateway forward schema: %w", err)
	}
	return nil
}

func validateTelemetryGatewayForwardConfig(forward telemetryGatewayForwardConfig) error {
	if strings.TrimSpace(forward.Endpoint) == "" {
		return fmt.Errorf("forward endpoint is empty")
	}
	for key := range forward.Headers {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("forward header name is empty")
		}
	}
	return nil
}

func telemetryGatewayRootNode(doc *yaml.Node) (*yaml.Node, error) {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, fmt.Errorf("telemetry gateway service config has unexpected document structure")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("telemetry gateway service config root has unexpected kind %d", root.Kind)
	}
	return root, nil
}

func telemetryGatewaySectionNode(root *yaml.Node) (*yaml.Node, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "telemetryGateway" && root.Content[i].Value != "telemetrygateway" {
			continue
		}
		tg := root.Content[i+1]
		if tg.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s config section has unexpected kind %d", root.Content[i].Value, tg.Kind)
		}
		return tg, nil
	}
	return nil, fmt.Errorf("telemetryGateway config section not found")
}

func telemetryGatewaySectionCount(root *yaml.Node) int {
	if root == nil {
		return 0
	}
	count := 0
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "telemetryGateway" || root.Content[i].Value == "telemetrygateway" {
			count++
		}
	}
	return count
}

func yamlNodeForForwardConfig(forward telemetryGatewayForwardConfig) (*yaml.Node, error) {
	forwardYAML, err := yaml.Marshal(forward)
	if err != nil {
		return nil, fmt.Errorf("marshal telemetry gateway forward config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(forwardYAML, &doc); err != nil {
		return nil, fmt.Errorf("parse telemetry gateway forward config node: %w", err)
	}
	node, err := telemetryGatewayRootNode(&doc)
	if err != nil {
		return nil, err
	}
	return node, nil
}

// telemetryGatewayForwardMetadata returns only non-sensitive forward metadata
// for assertions, avoiding generated header values in failure output.
func telemetryGatewayForwardMetadata(config string) (string, []string, error) {
	if strings.TrimSpace(config) == "" {
		return "", nil, fmt.Errorf("telemetry gateway service config is empty")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(config), &doc); err != nil {
		return "", nil, fmt.Errorf("parse telemetry gateway service config: %w", err)
	}
	root, err := telemetryGatewayRootNode(&doc)
	if err != nil {
		return "", nil, err
	}
	tg, err := telemetryGatewaySectionNode(root)
	if err != nil {
		return "", nil, err
	}
	forwardNode := mappingValue(tg, "forward")
	if forwardNode == nil {
		return "", nil, fmt.Errorf("telemetry gateway forward section not found")
	}
	var b bytes.Buffer
	encoder := yaml.NewEncoder(&b)
	if err := encoder.Encode(forwardNode); err != nil {
		return "", nil, fmt.Errorf("marshal telemetry gateway forward section: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", nil, fmt.Errorf("close telemetry gateway forward encoder: %w", err)
	}
	decoder := yaml.NewDecoder(&b)
	decoder.KnownFields(true)
	var forward telemetryGatewayForwardConfig
	if err := decoder.Decode(&forward); err != nil {
		return "", nil, fmt.Errorf("parse telemetry gateway forward section: %w", err)
	}
	headerNames := make([]string, 0, len(forward.Headers))
	for name := range forward.Headers {
		headerNames = append(headerNames, name)
	}
	return forward.Endpoint, headerNames, nil
}

// mappingValue returns the value node for a key in a YAML mapping.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func replaceMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: key,
	}, value)
}

// waitForTelemetryGatewayFailureLogs waits for a service readiness failure and returns recent logs.
func waitForTelemetryGatewayFailureLogs(ctx context.Context, providers *infra.Providers, serviceLogs infra.ServiceLogProvider, timeout time.Duration, tailLines int, expectedLog string) (string, error) {
	if providers == nil || providers.Lifecycle == nil {
		return "", fmt.Errorf("providers lifecycle is nil")
	}
	if serviceLogs == nil {
		return "", fmt.Errorf("service log provider is nil")
	}
	return infra.ServiceFailureObserver(ctx, providers.Lifecycle, serviceLogs, infra.ServiceTelemetryGateway, timeout, tailLines, expectedLog)
}
