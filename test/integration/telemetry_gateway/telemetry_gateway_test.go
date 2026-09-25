package telemetry_gateway_test

import (
	"compress/gzip"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	icrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	telemetrygateway "github.com/flightctl/flightctl/internal/telemetry_gateway"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"
)

const (
	timeout                     = 10 * time.Second
	polling                     = 250 * time.Millisecond
	gatewayStartupAttempts      = 5
	gatewayReadinessDialTimeout = 200 * time.Millisecond
)

var (
	suiteCtx context.Context
)

func TestTelemetryGateway(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Telemetry Gateway Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Telemetry Gateway Suite")
})

var _ = Describe("Telemetry Gateway", func() {
	var (
		ctx      context.Context
		otlpAddr string
		promAddr string
		gwCancel context.CancelFunc
		gwDone   chan error
		caClient *icrypto.CAClient

		// config plumbing
		baseCfg               *config.Config
		cfgMutators           []func(*config.Config)
		refreshPortAllocators []func()
		testDirPath           string
		runOpts               []telemetrygateway.Option
		gatewayReadyTLS       *tls.Config
	)

	BeforeEach(func() {
		var err error
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)

		testDirPath = GinkgoT().TempDir()

		// CA
		caCfg := ca.NewDefault(testDirPath)
		caClient, _, err = icrypto.EnsureCA(caCfg)
		Expect(err).ToNot(HaveOccurred())

		// CA bundle on disk (for gateway)
		caPath := filepath.Join(testDirPath, "etc", "flightctl", "certs", "ca.crt")
		Expect(os.MkdirAll(filepath.Dir(caPath), 0o755)).To(Succeed())
		caBundle, err := caClient.GetCABundle()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.WriteFile(caPath, caBundle, 0o600)).To(Succeed())

		// server keypair for gateway
		serverPriv, serverCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.ServerSvcSignerName, "svc-telemetry-gateway", 365)
		Expect(err).ToNot(HaveOccurred())
		serverCrt := filepath.Join(testDirPath, "etc", "flightctl", "certs", "telemetry-gateway.crt")
		serverKey := filepath.Join(testDirPath, "etc", "flightctl", "certs", "telemetry-gateway.key")
		Expect(os.MkdirAll(filepath.Dir(serverCrt), 0o755)).To(Succeed())
		certPEM, _ := fccrypto.EncodeCertificatePEM(serverCert)
		keyPEM, _ := fccrypto.PEMEncodeKey(serverPriv)
		Expect(os.WriteFile(serverCrt, certPEM, 0o600)).To(Succeed())
		Expect(os.WriteFile(serverKey, keyPEM, 0o600)).To(Succeed())

		// The Prometheus exporter address is set only by contexts that exercise
		// that exporter. The test discovers the OTLP listener after OTel binds it.
		promAddr = ""

		// base config + reset mutators
		baseCfg = createConfig(serverCrt, serverKey, caPath, "127.0.0.1:0")
		cfgMutators = nil
		refreshPortAllocators = nil

		runOpts = []telemetrygateway.Option{
			telemetrygateway.WithSkipSettingGRPCLogger(true), // kill grpclog race in tests
			telemetrygateway.WithOTelYAMLOverlay(`
service:
  telemetry:
    metrics:
      level: none
`),
		}

		gatewayReadyTLS, err = newGatewayClientTLSConfig(ctx, caClient, "client-telemetry-gateway-readiness")
		Expect(err).ToNot(HaveOccurred())
	})

	// Start the gateway after all mutators from nested BeforeEach have run.
	JustBeforeEach(func() {
		var err error
		otlpAddr, gwCancel, gwDone, err = startTelemetryGateway(
			ctx,
			baseCfg,
			cfgMutators,
			refreshPortAllocators,
			runOpts,
			gatewayReadyTLS,
			testDirPath,
		)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if gwCancel != nil {
			gwCancel()
		}
		if gwDone != nil {
			var err error
			Eventually(gwDone, 2*time.Second).Should(Receive(&err))
			if err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(GinkgoWriter, "[gateway] exited with error: %v\n", err)
			} else {
				fmt.Fprintln(GinkgoWriter, "[gateway] exited cleanly")
			}
			Expect(err == nil || errors.Is(err, context.Canceled)).To(BeTrue())
		}
	})

	Context("with a custom Prometheus listen address", func() {
		BeforeEach(func() {
			// Refresh the Prometheus endpoint before every gateway startup attempt.
			refreshPortAllocators = append(refreshPortAllocators, func() {
				promAddr = localAddr()
			})
			cfgMutators = append(cfgMutators, func(c *config.Config) {
				// assuming your config struct has TelemetryGateway.Export.Prometheus (string)
				snippet := fmt.Appendf(nil, "telemetrygateway:\n  export:\n    prometheus: %q\n", promAddr)
				_ = yaml.Unmarshal(snippet, c)
			})
		})

		It("accepts OTLP metrics over mTLS using device certificate", func() {
			clientPrivateKey, clientCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-testdevice", 365)
			Expect(err).ToNot(HaveOccurred())

			caPool := x509.NewCertPool()
			cabundle := caClient.GetCABundleX509()
			for _, cert := range cabundle {
				caPool.AddCert(cert)
			}

			tlsCfg := &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    caPool,
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{clientCert.Raw},
					PrivateKey:  clientPrivateKey,
				}},
				ServerName: "localhost",
			}

			creds := credentials.NewTLS(tlsCfg)
			cc, err := grpc.NewClient(
				otlpAddr,
				grpc.WithTransportCredentials(creds),
			)
			Expect(err).ToNot(HaveOccurred())
			defer cc.Close()

			// First RPC drives connection; bound with a deadline
			rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			Expect(exportTestMetrics(rpcCtx, cc)).To(Succeed())

			Eventually(func() error {
				return receiveTestMetrics(ctx, promAddr)
			}, timeout, polling).Should(BeNil())
		})
	})

	Context("forwarding", func() {
		var (
			forwardStop func()
			forwardMC   *mockOTLPCollector
		)

		BeforeEach(func() {
			// --- Downstream OTLP server (the "another collector") ---
			// Use the same CA as the rest of the suite.
			clientCAPool := x509.NewCertPool()
			for _, c := range caClient.GetCABundleX509() {
				clientCAPool.AddCert(c)
			}

			// Server cert for downstream collector (SAN=localhost)
			dsPriv, dsCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.ServerSvcSignerName, "svc-downstream-collector", 365)
			Expect(err).ToNot(HaveOccurred())

			endpoint, stop, mc, err := startMockCollector(dsCert, dsPriv, clientCAPool)
			Expect(err).ToNot(HaveOccurred())
			forwardStop, forwardMC = stop, mc

			// --- Client cert for the gateway when dialing downstream ---
			fwdCliPriv, fwdCliCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-gateway-forwarder", 365)
			Expect(err).ToNot(HaveOccurred())

			// Write client cert/key for the gateway (forward.tls.* files)
			caPath := filepath.Join(testDirPath, "etc", "flightctl", "certs", "ca.crt")
			forwardCrt := filepath.Join(testDirPath, "etc", "flightctl", "certs", fmt.Sprintf("fwd-%d.crt", time.Now().UnixNano()))
			forwardKey := filepath.Join(testDirPath, "etc", "flightctl", "certs", fmt.Sprintf("fwd-%d.key", time.Now().UnixNano()))
			certPEM, _ := fccrypto.EncodeCertificatePEM(fwdCliCert)
			keyPEM, _ := fccrypto.PEMEncodeKey(fwdCliPriv)
			Expect(os.WriteFile(forwardCrt, certPEM, 0o600)).To(Succeed())
			Expect(os.WriteFile(forwardKey, keyPEM, 0o600)).To(Succeed())

			// Mutate gateway config: set forward (no export)
			cfgMutators = append(cfgMutators, func(c *config.Config) {
				snippet := fmt.Appendf(nil,
					"telemetrygateway:\n  export: null\n  forward:\n    endpoint: %q\n    tls:\n      certFile: %q\n      keyFile: %q\n      caFile: %q\n",
					endpoint, forwardCrt, forwardKey, caPath,
				)
				_ = yaml.Unmarshal(snippet, c)
			})
		})

		AfterEach(func() {
			if forwardStop != nil {
				forwardStop()
			}
		})

		It("forwards metrics to another collector.", func() {
			clientPrivateKey, clientCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-testdevice", 365)
			Expect(err).ToNot(HaveOccurred())

			caPool := x509.NewCertPool()
			for _, cert := range caClient.GetCABundleX509() {
				caPool.AddCert(cert)
			}

			tlsCfg := &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    caPool,
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{clientCert.Raw},
					PrivateKey:  clientPrivateKey,
				}},
				ServerName: "localhost",
			}

			creds := credentials.NewTLS(tlsCfg)
			cc, err := grpc.NewClient(
				otlpAddr,
				grpc.WithTransportCredentials(creds),
			)
			Expect(err).ToNot(HaveOccurred())
			defer cc.Close()

			// First RPC drives connection; bound with a deadline
			rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			Expect(exportTestMetrics(rpcCtx, cc)).To(Succeed())

			// Assert the downstream collector received it with device attributes
			var forwardedReq *collectormetrics.ExportMetricsServiceRequest
			Eventually(func() bool {
				select {
				case req := <-forwardMC.ch:
					for _, rm := range req.ResourceMetrics {
						for _, sm := range rm.ScopeMetrics {
							for _, m := range sm.Metrics {
								if m.GetName() == "test_tg_metric" {
									forwardedReq = req
									return true
								}
							}
						}
					}
					return false
				default:
					return false
				}
			}, timeout, polling).Should(BeTrue(), "downstream collector did not receive forwarded metric")

			verifyDeviceAttrs(forwardedReq, "test_tg_metric", "testdevice")
		})
	})

	Context("HTTP forwarding", func() {
		var (
			httpStop func()
			httpMC   *mockHTTPOTLPCollector
		)

		BeforeEach(func() {
			dsPriv, dsCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.ServerSvcSignerName, "svc-http-collector", 365)
			Expect(err).ToNot(HaveOccurred())

			endpoint, stop, mc, startErr := startMockHTTPCollector(dsCert, dsPriv)
			Expect(startErr).ToNot(HaveOccurred())
			httpStop, httpMC = stop, mc

			cfgMutators = append(cfgMutators, func(c *config.Config) {
				snippet := fmt.Appendf(nil,
					"telemetrygateway:\n  export: null\n  forward:\n    endpoint: %q\n    headers:\n      Authorization: \"Api-Token test-token-123\"\n      X-Custom-Header: \"custom-value\"\n    tls:\n      insecureSkipTlsVerify: true\n",
					endpoint,
				)
				_ = yaml.Unmarshal(snippet, c)
			})
		})

		AfterEach(func() {
			if httpStop != nil {
				httpStop()
			}
		})

		It("forwards metrics via OTLP/HTTP with custom headers", func() {
			clientPrivateKey, clientCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-testdevice", 365)
			Expect(err).ToNot(HaveOccurred())

			caPool := x509.NewCertPool()
			for _, cert := range caClient.GetCABundleX509() {
				caPool.AddCert(cert)
			}

			tlsCfg := &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    caPool,
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{clientCert.Raw},
					PrivateKey:  clientPrivateKey,
				}},
				ServerName: "localhost",
			}

			creds := credentials.NewTLS(tlsCfg)
			cc, err := grpc.NewClient(otlpAddr, grpc.WithTransportCredentials(creds))
			Expect(err).ToNot(HaveOccurred())
			defer cc.Close()

			rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			Expect(exportTestMetrics(rpcCtx, cc)).To(Succeed())

			Eventually(func() *httpOTLPRequest {
				return httpMC.findRequestByMetric("test_tg_metric")
			}, timeout, polling).ShouldNot(BeNil(), "HTTP collector did not receive forwarded metric")

			req := httpMC.findRequestByMetric("test_tg_metric")
			Expect(req.headers.Get("Authorization")).To(Equal("Api-Token test-token-123"))
			Expect(req.headers.Get("X-Custom-Header")).To(Equal("custom-value"))
			verifyDeviceAttrs(req.metrics, "test_tg_metric", "testdevice")
		})
	})

	Context("HTTP forwarding with env var expansion in headers", func() {
		var (
			httpStop func()
			httpMC   *mockHTTPOTLPCollector
		)

		BeforeEach(func() {
			dsPriv, dsCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.ServerSvcSignerName, "svc-http-collector", 365)
			Expect(err).ToNot(HaveOccurred())

			endpoint, stop, mc, startErr := startMockHTTPCollector(dsCert, dsPriv)
			Expect(startErr).ToNot(HaveOccurred())
			httpStop, httpMC = stop, mc

			GinkgoT().Setenv("TEST_FORWARD_TOKEN", "secret-token-from-env")

			cfgMutators = append(cfgMutators, func(c *config.Config) {
				snippet := fmt.Appendf(nil,
					"telemetrygateway:\n  export: null\n  forward:\n    endpoint: %q\n    headers:\n      Authorization: \"Api-Token ${TEST_FORWARD_TOKEN}\"\n    tls:\n      insecureSkipTlsVerify: true\n",
					endpoint,
				)
				_ = yaml.Unmarshal(snippet, c)
			})
		})

		AfterEach(func() {
			if httpStop != nil {
				httpStop()
			}
		})

		It("When a header uses ${VAR} syntax it should resolve the env var and forward the value", func() {
			clientPrivateKey, clientCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-testdevice", 365)
			Expect(err).ToNot(HaveOccurred())

			caPool := x509.NewCertPool()
			for _, cert := range caClient.GetCABundleX509() {
				caPool.AddCert(cert)
			}

			tlsCfg := &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    caPool,
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{clientCert.Raw},
					PrivateKey:  clientPrivateKey,
				}},
				ServerName: "localhost",
			}

			creds := credentials.NewTLS(tlsCfg)
			cc, err := grpc.NewClient(otlpAddr, grpc.WithTransportCredentials(creds))
			Expect(err).ToNot(HaveOccurred())
			defer cc.Close()

			rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			Expect(exportTestMetrics(rpcCtx, cc)).To(Succeed())

			Eventually(func() *httpOTLPRequest {
				return httpMC.findRequestByMetric("test_tg_metric")
			}, timeout, polling).ShouldNot(BeNil(), "HTTP collector did not receive forwarded metric")

			req := httpMC.findRequestByMetric("test_tg_metric")
			Expect(req.headers.Get("Authorization")).To(Equal("Api-Token secret-token-from-env"))
			verifyDeviceAttrs(req.metrics, "test_tg_metric", "testdevice")
		})
	})

	Context("HTTP forwarding without custom headers", func() {
		var (
			httpStop func()
			httpMC   *mockHTTPOTLPCollector
		)

		BeforeEach(func() {
			dsPriv, dsCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.ServerSvcSignerName, "svc-http-collector", 365)
			Expect(err).ToNot(HaveOccurred())

			endpoint, stop, mc, startErr := startMockHTTPCollector(dsCert, dsPriv)
			Expect(startErr).ToNot(HaveOccurred())
			httpStop, httpMC = stop, mc

			cfgMutators = append(cfgMutators, func(c *config.Config) {
				snippet := fmt.Appendf(nil,
					"telemetrygateway:\n  export: null\n  forward:\n    endpoint: %q\n    tls:\n      insecureSkipTlsVerify: true\n",
					endpoint,
				)
				_ = yaml.Unmarshal(snippet, c)
			})
		})

		AfterEach(func() {
			if httpStop != nil {
				httpStop()
			}
		})

		It("forwards metrics via OTLP/HTTP without headers", func() {
			clientPrivateKey, clientCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-testdevice", 365)
			Expect(err).ToNot(HaveOccurred())

			caPool := x509.NewCertPool()
			for _, cert := range caClient.GetCABundleX509() {
				caPool.AddCert(cert)
			}

			tlsCfg := &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    caPool,
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{clientCert.Raw},
					PrivateKey:  clientPrivateKey,
				}},
				ServerName: "localhost",
			}

			creds := credentials.NewTLS(tlsCfg)
			cc, err := grpc.NewClient(otlpAddr, grpc.WithTransportCredentials(creds))
			Expect(err).ToNot(HaveOccurred())
			defer cc.Close()

			rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			Expect(exportTestMetrics(rpcCtx, cc)).To(Succeed())

			Eventually(func() *httpOTLPRequest {
				return httpMC.findRequestByMetric("test_tg_metric")
			}, timeout, polling).ShouldNot(BeNil(), "HTTP collector did not receive forwarded metric")

			verifyDeviceAttrs(httpMC.findRequestByMetric("test_tg_metric").metrics, "test_tg_metric", "testdevice")
		})
	})

	Context("HTTP forwarding with mTLS", func() {
		var (
			httpStop func()
			httpMC   *mockHTTPOTLPCollector
		)

		BeforeEach(func() {
			// Server cert for downstream HTTP collector
			dsPriv, dsCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.ServerSvcSignerName, "svc-http-mtls-collector", 365)
			Expect(err).ToNot(HaveOccurred())

			// Start mock HTTP collector that requires client certs
			clientCAPool := x509.NewCertPool()
			for _, c := range caClient.GetCABundleX509() {
				clientCAPool.AddCert(c)
			}
			endpoint, stop, mc, startErr := startMockHTTPCollectorWithClientAuth(dsCert, dsPriv, clientCAPool)
			Expect(startErr).ToNot(HaveOccurred())
			httpStop, httpMC = stop, mc

			// Client cert for the gateway when dialing downstream
			fwdCliPriv, fwdCliCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-gateway-http-forwarder", 365)
			Expect(err).ToNot(HaveOccurred())

			caPath := filepath.Join(testDirPath, "etc", "flightctl", "certs", "ca.crt")
			forwardCrt := filepath.Join(testDirPath, "etc", "flightctl", "certs", fmt.Sprintf("fwd-http-%d.crt", time.Now().UnixNano()))
			forwardKey := filepath.Join(testDirPath, "etc", "flightctl", "certs", fmt.Sprintf("fwd-http-%d.key", time.Now().UnixNano()))
			certPEM, _ := fccrypto.EncodeCertificatePEM(fwdCliCert)
			keyPEM, _ := fccrypto.PEMEncodeKey(fwdCliPriv)
			Expect(os.WriteFile(forwardCrt, certPEM, 0o600)).To(Succeed())
			Expect(os.WriteFile(forwardKey, keyPEM, 0o600)).To(Succeed())

			cfgMutators = append(cfgMutators, func(c *config.Config) {
				snippet := fmt.Appendf(nil,
					"telemetrygateway:\n  export: null\n  forward:\n    endpoint: %q\n    tls:\n      certFile: %q\n      keyFile: %q\n      caFile: %q\n",
					endpoint, forwardCrt, forwardKey, caPath,
				)
				_ = yaml.Unmarshal(snippet, c)
			})
		})

		AfterEach(func() {
			if httpStop != nil {
				httpStop()
			}
		})

		It("forwards metrics via OTLP/HTTP using client certificates", func() {
			clientPrivateKey, clientCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-testdevice", 365)
			Expect(err).ToNot(HaveOccurred())

			caPool := x509.NewCertPool()
			for _, cert := range caClient.GetCABundleX509() {
				caPool.AddCert(cert)
			}

			tlsCfg := &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    caPool,
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{clientCert.Raw},
					PrivateKey:  clientPrivateKey,
				}},
				ServerName: "localhost",
			}

			creds := credentials.NewTLS(tlsCfg)
			cc, err := grpc.NewClient(otlpAddr, grpc.WithTransportCredentials(creds))
			Expect(err).ToNot(HaveOccurred())
			defer cc.Close()

			rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			Expect(exportTestMetrics(rpcCtx, cc)).To(Succeed())

			Eventually(func() *httpOTLPRequest {
				return httpMC.findRequestByMetric("test_tg_metric")
			}, timeout, polling).ShouldNot(BeNil(), "HTTP mTLS collector did not receive forwarded metric")

			verifyDeviceAttrs(httpMC.findRequestByMetric("test_tg_metric").metrics, "test_tg_metric", "testdevice")
		})
	})

	Context("OTel config mutation via overlay", func() {
		var (
			forwardStop func()
			forwardMC   *mockOTLPCollector
		)

		BeforeEach(func() {
			// Downstream OTLP server (mutual TLS)
			clientCAPool := x509.NewCertPool()
			for _, c := range caClient.GetCABundleX509() {
				clientCAPool.AddCert(c)
			}
			dsPriv, dsCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.ServerSvcSignerName, "svc-downstream-collector", 365)
			Expect(err).ToNot(HaveOccurred())

			endpoint, stop, mc, err := startMockCollector(dsCert, dsPriv, clientCAPool)
			Expect(err).ToNot(HaveOccurred())
			forwardStop, forwardMC = stop, mc

			// Gateway’s client creds for forwarding
			fwdCliPriv, fwdCliCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-gateway-forwarder", 365)
			Expect(err).ToNot(HaveOccurred())

			caPath := filepath.Join(testDirPath, "etc", "flightctl", "certs", "ca.crt")
			forwardCrt := filepath.Join(testDirPath, "etc", "flightctl", "certs", fmt.Sprintf("fwd-%d.crt", time.Now().UnixNano()))
			forwardKey := filepath.Join(testDirPath, "etc", "flightctl", "certs", fmt.Sprintf("fwd-%d.key", time.Now().UnixNano()))
			certPEM, _ := fccrypto.EncodeCertificatePEM(fwdCliCert)
			keyPEM, _ := fccrypto.PEMEncodeKey(fwdCliPriv)
			Expect(os.WriteFile(forwardCrt, certPEM, 0o600)).To(Succeed())
			Expect(os.WriteFile(forwardKey, keyPEM, 0o600)).To(Succeed())

			// Refresh the Prometheus endpoint before every gateway startup attempt.
			refreshPortAllocators = append(refreshPortAllocators, func() {
				promAddr = localAddr()
			})
			// Ensure exporter exists in base config (so build map doesn’t error);
			// overlay will *also* set exporters and add otlp.
			cfgMutators = append(cfgMutators, func(c *config.Config) {
				snippet := fmt.Appendf(nil, "telemetrygateway:\n  export:\n    prometheus: %q\n", promAddr)
				_ = yaml.Unmarshal(snippet, c)
			})

			// build the overlay
			overlay := fmt.Sprintf(`
			exporters:
				otlp:
					endpoint: %q
					tls:
						cert_file: %q
						key_file:  %q
						ca_file:   %q
			service:
				pipelines:
					metrics:
						exporters: ["prometheus","otlp"]
			`, endpoint, forwardCrt, forwardKey, caPath)

			// YAML forbids tabs; normalize: each \t -> two spaces
			overlay = strings.ReplaceAll(overlay, "\t", "  ")

			// register the overlay mutator
			runOpts = append(runOpts, telemetrygateway.WithOTelYAMLOverlay(overlay))
		})

		AfterEach(func() {
			if forwardStop != nil {
				forwardStop()
			}
		})

		It("applies the overlay and both exports & forwards metrics", func() {
			// Device client -> gateway (mTLS)
			clientPrivateKey, clientCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, "client-testdevice", 365)
			Expect(err).ToNot(HaveOccurred())

			caPool := x509.NewCertPool()
			for _, cert := range caClient.GetCABundleX509() {
				caPool.AddCert(cert)
			}

			tlsCfg := &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    caPool,
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{clientCert.Raw},
					PrivateKey:  clientPrivateKey,
				}},
				ServerName: "localhost",
			}

			creds := credentials.NewTLS(tlsCfg)
			cc, err := grpc.NewClient(otlpAddr, grpc.WithTransportCredentials(creds))
			Expect(err).ToNot(HaveOccurred())
			defer cc.Close()

			// First RPC drives connection
			rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			Expect(exportTestMetrics(rpcCtx, cc)).To(Succeed())

			// Assert Prom exporter has the metric
			Eventually(func() error {
				return receiveTestMetrics(ctx, promAddr)
			}, timeout, polling).Should(BeNil())

			// Assert downstream collector received it via forward with device attributes
			var overlayForwardedReq *collectormetrics.ExportMetricsServiceRequest
			Eventually(func() bool {
				select {
				case req := <-forwardMC.ch:
					for _, rm := range req.ResourceMetrics {
						for _, sm := range rm.ScopeMetrics {
							for _, m := range sm.Metrics {
								if m.GetName() == "test_tg_metric" {
									overlayForwardedReq = req
									return true
								}
							}
						}
					}
					return false
				default:
					return false
				}
			}, timeout, polling).Should(BeTrue(), "downstream collector did not receive forwarded metric")

			verifyDeviceAttrs(overlayForwardedReq, "test_tg_metric", "testdevice")
		})
	})
})

var _ = Describe("Telemetry Gateway startup failure", func() {
	It("When TLS cert files do not exist it should fail to start", func() {
		ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
		testDirPath := GinkgoT().TempDir()

		// CA
		caCfg := ca.NewDefault(testDirPath)
		caClient, _, err := icrypto.EnsureCA(caCfg)
		Expect(err).ToNot(HaveOccurred())

		// CA bundle on disk
		caPath := filepath.Join(testDirPath, "etc", "flightctl", "certs", "ca.crt")
		Expect(os.MkdirAll(filepath.Dir(caPath), 0o755)).To(Succeed())
		caBundle, err := caClient.GetCABundle()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.WriteFile(caPath, caBundle, 0o600)).To(Succeed())

		// Server keypair
		serverPriv, serverCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.ServerSvcSignerName, "svc-telemetry-gateway", 365)
		Expect(err).ToNot(HaveOccurred())
		serverCrt := filepath.Join(testDirPath, "etc", "flightctl", "certs", "telemetry-gateway.crt")
		serverKey := filepath.Join(testDirPath, "etc", "flightctl", "certs", "telemetry-gateway.key")
		certPEM, _ := fccrypto.EncodeCertificatePEM(serverCert)
		keyPEM, _ := fccrypto.PEMEncodeKey(serverPriv)
		Expect(os.WriteFile(serverCrt, certPEM, 0o600)).To(Succeed())
		Expect(os.WriteFile(serverKey, keyPEM, 0o600)).To(Succeed())

		cfg := createConfig(serverCrt, serverKey, caPath, "127.0.0.1:0")

		// Configure forward with TLS cert paths that do not exist
		snippet := fmt.Appendf(nil,
			"telemetrygateway:\n  export: null\n  forward:\n    endpoint: \"collector.example.com:4317\"\n    tls:\n      certFile: \"/nonexistent/path/client.crt\"\n      keyFile: \"/nonexistent/path/client.key\"\n      caFile: \"/nonexistent/path/ca.crt\"\n",
		)
		Expect(yaml.Unmarshal(snippet, cfg)).To(Succeed())

		gwDone := make(chan error, 1)
		gwCtx, gwCancel := context.WithCancel(ctx)
		defer gwCancel()
		go func() {
			gwDone <- telemetrygateway.Run(gwCtx, cfg,
				telemetrygateway.WithSkipSettingGRPCLogger(true),
				telemetrygateway.WithOTelYAMLOverlay("service:\n  telemetry:\n    metrics:\n      level: none\n"),
			)
		}()

		var gwErr error
		Eventually(gwDone, timeout).Should(Receive(&gwErr))
		Expect(gwErr).To(HaveOccurred())
		Expect(errors.Is(gwErr, os.ErrNotExist)).To(BeTrue(), "expected file-not-found error, got: %v", gwErr)
	})

	It("When a header references an undefined env var it should fail to start", func() {
		ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
		testDirPath := GinkgoT().TempDir()

		// CA
		caCfg := ca.NewDefault(testDirPath)
		caClient, _, err := icrypto.EnsureCA(caCfg)
		Expect(err).ToNot(HaveOccurred())

		// CA bundle on disk
		caPath := filepath.Join(testDirPath, "etc", "flightctl", "certs", "ca.crt")
		Expect(os.MkdirAll(filepath.Dir(caPath), 0o755)).To(Succeed())
		caBundle, err := caClient.GetCABundle()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.WriteFile(caPath, caBundle, 0o600)).To(Succeed())

		// Server keypair
		serverPriv, serverCert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.ServerSvcSignerName, "svc-telemetry-gateway", 365)
		Expect(err).ToNot(HaveOccurred())
		serverCrt := filepath.Join(testDirPath, "etc", "flightctl", "certs", "telemetry-gateway.crt")
		serverKey := filepath.Join(testDirPath, "etc", "flightctl", "certs", "telemetry-gateway.key")
		certPEM, _ := fccrypto.EncodeCertificatePEM(serverCert)
		keyPEM, _ := fccrypto.PEMEncodeKey(serverPriv)
		Expect(os.WriteFile(serverCrt, certPEM, 0o600)).To(Succeed())
		Expect(os.WriteFile(serverKey, keyPEM, 0o600)).To(Succeed())

		cfg := createConfig(serverCrt, serverKey, caPath, "127.0.0.1:0")

		// Configure HTTP forward with an undefined env var in header
		snippet := fmt.Appendf(nil,
			"telemetrygateway:\n  export: null\n  forward:\n    endpoint: \"https://collector.example.com/api/v2/otlp\"\n    headers:\n      Authorization: \"Bearer ${UNDEFINED_SECRET_VAR}\"\n    tls:\n      insecureSkipTlsVerify: true\n",
		)
		Expect(yaml.Unmarshal(snippet, cfg)).To(Succeed())

		gwDone := make(chan error, 1)
		gwCtx, gwCancel := context.WithCancel(ctx)
		defer gwCancel()
		go func() {
			gwDone <- telemetrygateway.Run(gwCtx, cfg,
				telemetrygateway.WithSkipSettingGRPCLogger(true),
				telemetrygateway.WithOTelYAMLOverlay("service:\n  telemetry:\n    metrics:\n      level: none\n"),
			)
		}()

		var gwErr error
		Eventually(gwDone, timeout).Should(Receive(&gwErr))
		Expect(gwErr).To(HaveOccurred())
		Expect(gwErr.Error()).To(ContainSubstring("UNDEFINED_SECRET_VAR"))
	})
})

func verifyDeviceAttrs(req *collectormetrics.ExportMetricsServiceRequest, metricName, expectedDeviceID string) {
	GinkgoHelper()
	Expect(req).ToNot(BeNil())
	found := false
	for _, rm := range req.ResourceMetrics {
		var deviceID, orgID string
		for _, attr := range rm.Resource.Attributes {
			switch attr.Key {
			case "device_id":
				deviceID = attr.Value.GetStringValue()
			case "org_id":
				orgID = attr.Value.GetStringValue()
			}
		}
		Expect(deviceID).To(Equal(expectedDeviceID), "resource attribute device_id mismatch")
		Expect(orgID).ToNot(BeEmpty(), "resource attribute org_id should not be empty")

		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.GetName() != metricName {
					continue
				}
				found = true
				dp := m.GetSum().GetDataPoints()
				Expect(dp).ToNot(BeEmpty())
				attrs := dp[0].GetAttributes()
				var dpDevice, dpOrg string
				for _, a := range attrs {
					switch a.Key {
					case "device_id":
						dpDevice = a.Value.GetStringValue()
					case "org_id":
						dpOrg = a.Value.GetStringValue()
					}
				}
				Expect(dpDevice).To(Equal(expectedDeviceID), "data point device_id mismatch")
				Expect(dpOrg).ToNot(BeEmpty(), "data point org_id should not be empty")
			}
		}
	}
	Expect(found).To(BeTrue(), "metric %q not found in request", metricName)
}

func createConfig(serverCrt string, serverKey string, caPath string, otlpAddr string) *config.Config {
	config := config.NewDefault()
	config.TelemetryGateway.LogLevel = "debug"
	config.TelemetryGateway.TLS.CertFile = serverCrt
	config.TelemetryGateway.TLS.KeyFile = serverKey
	config.TelemetryGateway.TLS.CACert = caPath
	config.TelemetryGateway.Listen.Device = otlpAddr
	return config
}

func makeKeyPairAndCSR(ctx context.Context, ca *icrypto.CAClient, signerName string, subjectName string, expiryDays int) (crypto.PrivateKey, *x509.Certificate, error) {
	_, clientPrivateKey, err := fccrypto.NewKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate client key pair: %w", err)
	}

	raw, err := fccrypto.MakeCSR(clientPrivateKey.(crypto.Signer), subjectName, fccrypto.WithDNSNames("localhost"))
	if err != nil {
		return nil, nil, err
	}

	seconds := expiryDays * 24 * 3600
	if seconds > math.MaxInt32 {
		return nil, nil, fmt.Errorf("expiryDays too large: would overflow int32 seconds")
	}
	expiry := int32(seconds) // #nosec G115 -- safe: bounds already checked above

	x509CSR, err := fccrypto.ParseCSR(raw)
	if err != nil {
		return nil, nil, err
	}

	req, err := signer.NewSignRequest(
		signerName,
		*x509CSR,
		signer.WithExpirationSeconds(expiry),
		signer.WithResourceName(subjectName),
	)
	if err != nil {
		return nil, nil, err
	}

	signedCert, err := signer.Sign(ctx, ca, req)
	if err != nil {
		return nil, nil, fmt.Errorf("makeKeyPairAndCSR: Signing certificate: %w", err)
	}
	return clientPrivateKey, signedCert, nil
}

func exportTestMetrics(ctx context.Context, conn *grpc.ClientConn) error {
	metricsClient := collectormetrics.NewMetricsServiceClient(conn)

	_, err := metricsClient.Export(ctx, &collectormetrics.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{}},
				ScopeMetrics: []*metricspb.ScopeMetrics{
					{
						Metrics: []*metricspb.Metric{
							{
								Name: "test_tg_metric",
								Data: &metricspb.Metric_Sum{
									Sum: &metricspb.Sum{
										AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
										IsMonotonic:            true,
										DataPoints: []*metricspb.NumberDataPoint{{
											Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 1},
										}},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	return err
}

func receiveTestMetrics(ctx context.Context, promAddr string) error {
	url := fmt.Sprintf("http://%s/metrics", promAddr)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	client := &http.Client{}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		client.Timeout = 3 * time.Second
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("expected 200 OK, got %s", resp.Status)
	}

	var parser expfmt.TextParser
	fams, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return fmt.Errorf("parse prometheus text: %w", err)
	}

	mf, ok := fams["test_tg_metric_total"]
	if !ok || mf == nil {
		return fmt.Errorf("metric test_tg_metric_total not found")
	}

	var found bool
	for _, m := range mf.Metric {
		var deviceID, orgID string
		for _, lp := range m.Label {
			switch lp.GetName() {
			case "device_id":
				if lp.Value != nil {
					deviceID = *lp.Value
				}
			case "org_id":
				if lp.Value != nil {
					orgID = *lp.Value
				}
			}
		}
		if deviceID != "" && orgID != "" {
			if deviceID != "testdevice" {
				return fmt.Errorf("device_id mismatch: expected %q, got %q", "testdevice", deviceID)
			}
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("test_tg_metric_total present but missing required labels (device_id/org_id)")
	}
	return nil
}

func localAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).ToNot(HaveOccurred())
	defer l.Close()
	return l.Addr().String()
}

func newGatewayClientTLSConfig(ctx context.Context, caClient *icrypto.CAClient, subjectName string) (*tls.Config, error) {
	privateKey, cert, err := makeKeyPairAndCSR(ctx, caClient, caClient.Cfg.DeviceSvcClientSignerName, subjectName, 365)
	if err != nil {
		return nil, err
	}

	caPool := x509.NewCertPool()
	for _, caCert := range caClient.GetCABundleX509() {
		caPool.AddCert(caCert)
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    caPool,
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{cert.Raw},
			PrivateKey:  privateKey,
		}},
		ServerName: "localhost",
	}, nil
}

func startTelemetryGateway(
	ctx context.Context,
	baseCfg *config.Config,
	cfgMutators []func(*config.Config),
	refreshPortAllocators []func(),
	baseOpts []telemetrygateway.Option,
	readinessTLS *tls.Config,
	testDirPath string,
) (string, context.CancelFunc, chan error, error) {
	startupCtx, startupCancel := context.WithTimeout(ctx, timeout)
	defer startupCancel()

	var lastStartupErr error
	for attempt := 0; attempt < gatewayStartupAttempts; attempt++ {
		for _, refresh := range refreshPortAllocators {
			refresh()
		}

		cfg := *baseCfg // shallow copy of struct
		for _, mutate := range cfgMutators {
			mutate(&cfg)
		}

		opts := append([]telemetrygateway.Option(nil), baseOpts...)

		cfgBytes, err := yaml.Marshal(&cfg)
		if err != nil {
			return "", nil, nil, err
		}
		if err := os.WriteFile(filepath.Join(testDirPath, "config.yaml"), cfgBytes, 0o600); err != nil {
			return "", nil, nil, err
		}

		listenersBefore, err := currentProcessTCPListeners()
		if err != nil {
			return "", nil, nil, err
		}

		gwCtx, cancel := context.WithCancel(ctx)
		gwDone := make(chan error, 1)
		go func() {
			gwDone <- telemetrygateway.Run(gwCtx, &cfg, opts...)
		}()

		otlpAddr, startupErr := waitForGatewayReady(startupCtx, listenersBefore, readinessTLS, gwDone)
		if startupErr == nil {
			return otlpAddr, cancel, gwDone, nil
		}
		lastStartupErr = startupErr

		cancel()
		if !isAddressInUse(startupErr) {
			select {
			case <-gwDone:
			case <-time.After(2 * time.Second):
			}
			return "", nil, nil, fmt.Errorf("telemetry gateway failed to become ready: %w", startupErr)
		}
	}

	if startupCtx.Err() != nil {
		return "", nil, nil, fmt.Errorf("telemetry gateway failed to become ready: %w", startupCtx.Err())
	}
	return "", nil, nil, fmt.Errorf("telemetry gateway could not acquire ports after %d attempts: %w", gatewayStartupAttempts, lastStartupErr)
}

func isAddressInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(strings.ToLower(err.Error()), "address already in use")
}

func waitForGatewayReady(
	ctx context.Context,
	listenersBefore map[string]string,
	clientTLS *tls.Config,
	done <-chan error,
) (string, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(polling)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			if err == nil {
				return "", errors.New("telemetry gateway exited before binding its device listener")
			}
			return "", err
		case <-ticker.C:
			listeners, err := currentProcessTCPListeners()
			if err != nil {
				return "", err
			}
			for inode, addr := range listeners {
				if _, existed := listenersBefore[inode]; existed {
					continue
				}

				addr = dialableAddress(addr)
				conn, err := tls.DialWithDialer(&net.Dialer{Timeout: gatewayReadinessDialTimeout}, "tcp", addr, clientTLS.Clone())
				if err == nil {
					defer func() {
						if closeErr := conn.Close(); closeErr != nil {
							fmt.Fprintf(GinkgoWriter, "[gateway] failed to close readiness connection: %v\n", closeErr)
						}
					}()
					return addr, nil
				}
			}
		case <-deadline.C:
			return "", errors.New("telemetry gateway did not become TLS-ready")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// currentProcessTCPListeners returns listening TCP sockets owned by this test
// process. OTel binds port 0 internally and does not expose the selected port;
// the test identifies the new listener and verifies it with the gateway's mTLS
// handshake.
func currentProcessTCPListeners() (map[string]string, error) {
	fdEntries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, fmt.Errorf("read process file descriptors: %w", err)
	}

	socketInodes := make(map[string]struct{})
	for _, entry := range fdEntries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			// The descriptor can close between ReadDir and Readlink.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read process file descriptor %s: %w", entry.Name(), err)
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			socketInodes[inode] = struct{}{}
		}
	}

	tcpTable, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return nil, fmt.Errorf("read TCP socket table: %w", err)
	}

	listeners := make(map[string]string)
	for _, line := range strings.Split(string(tcpTable), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}

		inode := fields[9]
		if _, owned := socketInodes[inode]; !owned {
			continue
		}

		addr, err := parseProcTCPAddress(fields[1])
		if err != nil {
			return nil, err
		}
		listeners[inode] = addr
	}

	return listeners, nil
}

func parseProcTCPAddress(encoded string) (string, error) {
	parts := strings.Split(encoded, ":")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid TCP address %q", encoded)
	}

	encodedIP, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return "", fmt.Errorf("parse TCP address %q: %w", encoded, err)
	}
	port, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil {
		return "", fmt.Errorf("parse TCP port %q: %w", encoded, err)
	}

	host := net.IPv4(
		byte(encodedIP),
		byte(encodedIP>>8),
		byte(encodedIP>>16),
		byte(encodedIP>>24),
	).String()
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func dialableAddress(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err == nil && host == "0.0.0.0" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// ---- Mock downstream OTLP HTTP collector (TLS, no client certs) ----

type httpOTLPRequest struct {
	metrics *collectormetrics.ExportMetricsServiceRequest
	headers http.Header
}

type mockHTTPOTLPCollector struct {
	mu   sync.Mutex
	reqs []httpOTLPRequest
}

func (m *mockHTTPOTLPCollector) findRequestByMetric(name string) *httpOTLPRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.reqs {
		for _, rm := range m.reqs[i].metrics.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, metric := range sm.Metrics {
					if metric.GetName() == name {
						req := m.reqs[i]
						req.headers = req.headers.Clone()
						return &req
					}
				}
			}
		}
	}
	return nil
}

func (m *mockHTTPOTLPCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var reader io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "gzip reader", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		reader = gz
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req collectormetrics.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, "unmarshal proto", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	m.reqs = append(m.reqs, httpOTLPRequest{metrics: &req, headers: r.Header.Clone()})
	m.mu.Unlock()

	resp := &collectormetrics.ExportMetricsServiceResponse{}
	out, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func startMockHTTPCollector(serverCert *x509.Certificate, serverKey crypto.PrivateKey) (endpoint string, stop func(), mc *mockHTTPOTLPCollector, err error) {
	mc = &mockHTTPOTLPCollector{}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{serverCert.Raw},
		PrivateKey:  serverKey,
	}
	tcfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{tlsCert},
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/metrics", mc)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	tlsLis := tls.NewListener(lis, tcfg)
	_, portStr, _ := net.SplitHostPort(lis.Addr().String())
	endpoint = fmt.Sprintf("https://localhost:%s", portStr)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(tlsLis) }()
	stop = func() {
		_ = srv.Close()
	}
	return endpoint, stop, mc, nil
}

func startMockHTTPCollectorWithClientAuth(serverCert *x509.Certificate, serverKey crypto.PrivateKey, clientCAPool *x509.CertPool) (endpoint string, stop func(), mc *mockHTTPOTLPCollector, err error) {
	mc = &mockHTTPOTLPCollector{}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{serverCert.Raw},
		PrivateKey:  serverKey,
	}
	tcfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAPool,
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/metrics", mc)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	tlsLis := tls.NewListener(lis, tcfg)
	_, portStr, _ := net.SplitHostPort(lis.Addr().String())
	endpoint = fmt.Sprintf("https://localhost:%s", portStr)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(tlsLis) }()
	stop = func() {
		_ = srv.Close()
	}
	return endpoint, stop, mc, nil
}

// ---- Mock downstream OTLP gRPC collector (TLS + mTLS) ----

type mockOTLPCollector struct {
	collectormetrics.UnimplementedMetricsServiceServer
	ch chan *collectormetrics.ExportMetricsServiceRequest
}

func newMockOTLPCollector() *mockOTLPCollector {
	return &mockOTLPCollector{ch: make(chan *collectormetrics.ExportMetricsServiceRequest, 16)}
}

func (m *mockOTLPCollector) Export(ctx context.Context, req *collectormetrics.ExportMetricsServiceRequest) (*collectormetrics.ExportMetricsServiceResponse, error) {
	// non-blocking in case channel is full
	select {
	case m.ch <- req:
	default:
	}
	return &collectormetrics.ExportMetricsServiceResponse{}, nil
}

// Starts a TLS-enabled OTLP gRPC server that REQUIRES client certs.
// Returns endpoint "localhost:<port>", a stop func, and the mock instance.
func startMockCollector(serverCert *x509.Certificate, serverKey crypto.PrivateKey, clientCAPool *x509.CertPool) (endpoint string, stop func(), mc *mockOTLPCollector, err error) {
	mc = newMockOTLPCollector()

	// TLS server config (require client cert)
	tlsCert := tls.Certificate{
		Certificate: [][]byte{serverCert.Raw},
		PrivateKey:  serverKey,
	}
	tcfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAPool,
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	_, portStr, _ := net.SplitHostPort(lis.Addr().String())
	endpoint = "localhost:" + portStr // SNI = "localhost"

	s := grpc.NewServer(grpc.Creds(credentials.NewTLS(tcfg)))
	collectormetrics.RegisterMetricsServiceServer(s, mc)

	go func() { _ = s.Serve(lis) }()
	stop = func() {
		s.GracefulStop()
		_ = lis.Close()
	}
	return endpoint, stop, mc, nil
}
