package telemetry_gateway_test

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config/ca"
	tgconfig "github.com/flightctl/flightctl/internal/config/telemetrygateway"
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
	"sigs.k8s.io/yaml"
)

const (
	timeout = 10 * time.Second
	polling = 250 * time.Millisecond
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
		baseCfg     *tgconfig.Config
		cfgMutators []func(*tgconfig.Config)
		testDirPath string
		runOpts     []telemetrygateway.Option
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

		// ports
		otlpAddr = localAddr()
		promAddr = ""

		// base config + reset mutators
		baseCfg = createConfig(serverCrt, serverKey, caPath, otlpAddr)
		cfgMutators = nil
		runOpts = []telemetrygateway.Option{
			telemetrygateway.WithSkipSettingGRPCLogger(true), // kill grpclog race in tests
		}
	})

	// Start the gateway after all mutators from nested BeforeEach have run
	JustBeforeEach(func() {
		// apply per-test tweaks
		cfg := *baseCfg // shallow copy of struct
		for _, m := range cfgMutators {
			m(&cfg)
		}

		// (optional) persist for debugging
		cfgBytes, err := yaml.Marshal(&cfg)
		Expect(err).ToNot(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(testDirPath, "config.yaml"), cfgBytes, 0o600)).To(Succeed())

		gwCtx, cancel := context.WithCancel(ctx)
		gwCancel = cancel
		gwDone = make(chan error, 1)
		go func() {
			gwDone <- telemetrygateway.Run(gwCtx, &cfg, runOpts...)
		}()

		// wait OTLP up
		Eventually(func() bool {
			conn, err := net.DialTimeout("tcp", otlpAddr, 200*time.Millisecond)
			if err != nil {
				return false
			}
			_ = conn.Close()
			return true
		}, timeout, polling).Should(BeTrue())
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
			// change prom listen addr in this context only
			newProm := localAddr()
			promAddr = newProm
			cfgMutators = append(cfgMutators, func(c *tgconfig.Config) {
				// assuming your config struct has TelemetryGateway.Export.Prometheus (string)
				snippet := fmt.Appendf(nil, "telemetrygateway:\n  export:\n    prometheus: %q\n", newProm)
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
			cfgMutators = append(cfgMutators, func(c *tgconfig.Config) {
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

			// Assert the downstream collector received it
			Eventually(func() bool {
				select {
				case req := <-forwardMC.ch:
					// sanity: does it contain "test_tg_metric"?
					for _, rm := range req.ResourceMetrics {
						for _, sm := range rm.ScopeMetrics {
							for _, m := range sm.Metrics {
								if m.GetName() == "test_tg_metric" {
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

			// Choose Prometheus endpoint for this test
			promAddr = localAddr()
			// Ensure exporter exists in base config (so build map doesn’t error);
			// overlay will *also* set exporters and add otlp.
			cfgMutators = append(cfgMutators, func(c *tgconfig.Config) {
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

			// Assert downstream collector received it via forward
			Eventually(func() bool {
				select {
				case req := <-forwardMC.ch:
					for _, rm := range req.ResourceMetrics {
						for _, sm := range rm.ScopeMetrics {
							for _, m := range sm.Metrics {
								if m.GetName() == "test_tg_metric" {
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
		})
	})
})

func createConfig(serverCrt string, serverKey string, caPath string, otlpAddr string) *tgconfig.Config {
	cfg := tgconfig.NewDefault()
	cfg.TelemetryGateway.LogLevel = "debug"
	cfg.TelemetryGateway.TLS.CertFile = serverCrt
	cfg.TelemetryGateway.TLS.KeyFile = serverKey
	cfg.TelemetryGateway.TLS.CACert = caPath
	cfg.TelemetryGateway.Listen.Device = otlpAddr
	return cfg
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
