package middleware_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestServer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Server Suite")
}

var _ = Describe("Low level server behavior", func() {
	var (
		ca             *crypto.CA
		enrollmentCert *crypto.TLSCertificateConfig
		clientCert     *crypto.TLSCertificateConfig
		noSubjectCert  *crypto.TLSCertificateConfig
		listener       net.Listener
	)

	BeforeEach(func() {
		var err error
		tempDir := GinkgoT().TempDir()
		serverLog := log.InitLogs()
		config := config.NewDefault()
		config.Service.CertStore = tempDir

		var serverCerts *crypto.TLSCertificateConfig

		ca, serverCerts, enrollmentCert, clientCert, err = testutil.NewTestCerts(config)
		Expect(err).ToNot(HaveOccurred())

		noSubjectCert, _, err = ca.EnsureClientCertificate(filepath.Join(tempDir, "no-subject.crt"), filepath.Join(tempDir, "no-subject.key"), "", 365)
		Expect(err).NotTo(HaveOccurred())

		tlsConfig, err := crypto.TLSConfigForServer(ca.Config, serverCerts)
		Expect(err).ToNot(HaveOccurred())

		// create a listener using the next available port
		listener, err = middleware.NewTLSListener("", tlsConfig)
		Expect(err).ToNot(HaveOccurred())

		srv := middleware.NewHTTPServerWithTLSContext(testTLSCNServer{}, serverLog, listener.Addr().String())

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
			dataStr := requestFromTLSCNServer(ca.Config, enrollmentCert, listener)
			Expect(dataStr).To(Equal(crypto.ClientBootstrapCommonName))
		})

		It("should be included as context in the request for admin cert", func() {
			dataStr := requestFromTLSCNServer(ca.Config, clientCert, listener)
			Expect(dataStr).To(Equal(crypto.AdminCommonName))
		})

	})

	Context("TLS client peer with no subject/common name", func() {
		It("should not include a context in the request", func() {
			requestFromTLSCNServerExpectNotFound(ca.Config, noSubjectCert, listener)
		})
	})

})

// testTLSCNServer is a simple http server that returns the TLS CommonName from the request context
// if it exists, otherwise it returns a NotFound status code.
type testTLSCNServer struct {
}

func (s testTLSCNServer) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	tlsCN := request.Context().Value(middleware.TLSCommonNameContextKey)
	if tlsCN == nil {
		// this should not really happen, this will make the tests fail
		response.WriteHeader(http.StatusInternalServerError)
	} else {
		tlsCN := tlsCN.(string)
		if tlsCN != "" {
			response.WriteHeader(http.StatusOK)
			_, err := response.Write([]byte(tlsCN))
			Expect(err).NotTo(HaveOccurred())
		} else {
			response.WriteHeader(http.StatusNotFound)
		}
	}
}

func requestFromTLSCNServer(caCert, clientCert *crypto.TLSCertificateConfig, listener net.Listener) string {
	client, err := testutil.NewBareHTTPsClient(caCert, clientCert)
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

func requestFromTLSCNServerExpectNotFound(caCert, clientCert *crypto.TLSCertificateConfig, listener net.Listener) {
	client, err := testutil.NewBareHTTPsClient(caCert, clientCert)
	Expect(err).NotTo(HaveOccurred())

	resp, err := client.Get("https://" + listener.Addr().String())
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
}
