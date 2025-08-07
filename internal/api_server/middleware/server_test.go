package middleware_test

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

var (
	suiteCtx context.Context
)

func TestServer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Server Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Server Suite")
})

var _ = Describe("Low level server behavior", func() {
	var (
		ctx            context.Context
		ca             *crypto.CAClient
		enrollmentCert *crypto.TLSCertificateConfig
		noSubjectCert  *crypto.TLSCertificateConfig
		listener       net.Listener
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)

		var err error
		tempDir := GinkgoT().TempDir()
		serverLog := log.InitLogs()
		config := config.NewDefault()
		config.Service.CertStore = tempDir
		config.CA.InternalConfig.CertStore = tempDir

		var serverCerts *crypto.TLSCertificateConfig

		ca, serverCerts, enrollmentCert, err = testutil.NewTestCerts(config)
		Expect(err).ToNot(HaveOccurred())

		noSubjectCert, err = makeNoSubjectClientCertificate(ctx, ca, 365)
		Expect(err).NotTo(HaveOccurred())

		_, tlsConfig, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
		Expect(err).ToNot(HaveOccurred())

		// create a listener using the next available port
		listener, err = middleware.NewTLSListener("", tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		srv := middleware.NewHTTPServerWithTLSContext(
			otelhttp.NewHandler(testTLSCNServer{}, "test-tlscn-server"),
			serverLog,
			listener.Addr().String(),
			config,
		)

		// capture the spec‑scoped context to avoid races when the outer
		// `ctx` variable is mutated by the next `BeforeEach`
		localCtx := ctx
		srv.BaseContext = func(_ net.Listener) context.Context {
			return localCtx
		}

		go func() {
			defer GinkgoRecover()
			if err := srv.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
				Expect(err).ToNot(HaveOccurred())
			}
		}()
	})

	AfterEach(func() {
		// close the listener, this will cause the server to stop
		if listener != nil {
			listener.Close()
		}
	})

	Context("TLS client peer CommonName", func() {
		It("should be included as context in the request for client bootstrap", func() {
			dataStr := requestFromTLSCNServer(ca.GetCABundleX509(), enrollmentCert, listener)
			Expect(dataStr).To(Equal(ca.Cfg.ClientBootstrapCommonName))
		})
	})

	Context("TLS client peer with no subject/common name", func() {
		It("should not include a context in the request", func() {
			requestFromTLSCNServerExpectNotFound(ca.GetCABundleX509(), noSubjectCert, listener)
		})
	})

})

// testTLSCNServer is a simple http server that returns the TLS CommonName from the request context
// if it exists, otherwise it returns a NotFound status code.
type testTLSCNServer struct {
}

func (s testTLSCNServer) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	peerCertificate := request.Context().Value(consts.TLSPeerCertificateCtxKey)
	if peerCertificate == nil {
		// this should not really happen, this will make the tests fail
		response.WriteHeader(http.StatusInternalServerError)
	} else {
		peerCertificate := peerCertificate.(*x509.Certificate)
		tlsCN := peerCertificate.Subject.CommonName
		if tlsCN != "" {
			response.WriteHeader(http.StatusOK)
			_, err := response.Write([]byte(tlsCN))
			Expect(err).NotTo(HaveOccurred())
		} else {
			response.WriteHeader(http.StatusNotFound)
		}
	}
}

func requestFromTLSCNServer(caBundle []*x509.Certificate, clientCert *crypto.TLSCertificateConfig, listener net.Listener) string {
	client, err := testutil.NewBareHTTPsClient(caBundle, clientCert)
	Expect(err).NotTo(HaveOccurred())

	resp, err := client.Get("https://" + listener.Addr().String())
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	Expect(resp.StatusCode).To(Equal(http.StatusOK))

	data := make([]byte, 1024)
	n, err := resp.Body.Read(data)
	Expect(err).To(Or(Equal(io.EOF), BeNil()))

	dataStr := string(data[:n])
	return dataStr
}

func requestFromTLSCNServerExpectNotFound(caBundle []*x509.Certificate, clientCert *crypto.TLSCertificateConfig, listener net.Listener) {
	client, err := testutil.NewBareHTTPsClient(caBundle, clientCert)
	Expect(err).NotTo(HaveOccurred())

	resp, err := client.Get("https://" + listener.Addr().String())
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
}

func makeNoSubjectClientCertificate(ctx context.Context, ca *crypto.CAClient, expiryDays int) (*crypto.TLSCertificateConfig, error) {
	_, clientPrivateKey, err := fccrypto.NewKeyPair()
	if err != nil {
		return nil, err
	}

	clientTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: ""},
	}
	raw, err := x509.CreateCertificateRequest(rand.Reader, clientTemplate, clientPrivateKey)
	if err != nil {
		return nil, err
	}
	csr, err := x509.ParseCertificateRequest(raw)
	if err != nil {
		return nil, err
	}

	clientCrt, err := ca.IssueRequestedClientCertificateAsX509(ctx, csr, expiryDays*24*3600)
	if err != nil {
		return nil, err
	}
	client := &crypto.TLSCertificateConfig{
		Certs: append([]*x509.Certificate{clientCrt}, ca.GetCABundleX509()...),
		Key:   clientPrivateKey,
	}
	return client, nil
}
