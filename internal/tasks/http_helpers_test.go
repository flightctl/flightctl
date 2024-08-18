package tasks

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/http"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func generateDummyX509KeyPair() ([]byte, []byte, error) {
	// Generate a private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	// Create a template for the certificate
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Dummy Org"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Create a self-signed certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}

	// Encode the certificate to PEM format
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	// Encode the private key to PEM format
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	return certPEM, keyPEM, nil
}

func TestHttpHelpers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "HttpHelpers Suite")
}

var _ = Describe("buildHttpRepoRequestAuth", func() {
	When("all authentication details are provided", func() {
		It("sets basic auth and bearer token headers", func() {
			username := "user"
			password := "pass"
			token := "token"
			req, _ := http.NewRequest("GET", "http://example.com", nil)
			repoHttpSpec := api.HttpRepoSpec{
				HttpConfig: api.HttpConfig{
					Username: &username,
					Password: &password,
					Token:    &token,
				},
			}
			req, tlsConfig, err := buildHttpRepoRequestAuth(repoHttpSpec, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(req.Header.Get("Authorization")).To(Equal("Bearer token"))
			Expect(tlsConfig.MinVersion).To(Equal(uint16(tls.VersionTLS12)))
		})
	})

	When("TLS certificates are provided", func() {
		It("sets the TLS certificates", func() {
			tlsCrt, tlsKey, err := generateDummyX509KeyPair()
			Expect(err).ToNot(HaveOccurred())
			tlsCrtEncoded := base64.StdEncoding.EncodeToString(tlsCrt)
			tlsKeyEncoded := base64.StdEncoding.EncodeToString(tlsKey)
			req, _ := http.NewRequest("GET", "http://example.com", nil)
			repoHttpSpec := api.HttpRepoSpec{
				HttpConfig: api.HttpConfig{
					TlsCrt: &tlsCrtEncoded,
					TlsKey: &tlsKeyEncoded,
				},
			}
			req, tlsConfig, err := buildHttpRepoRequestAuth(repoHttpSpec, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(tlsConfig.Certificates).To(HaveLen(1))
		})
	})

	When("CA certificate is provided", func() {
		It("sets the CA certificate", func() {
			caCrt := base64.StdEncoding.EncodeToString([]byte("ca"))
			req, _ := http.NewRequest("GET", "http://example.com", nil)
			repoHttpSpec := api.HttpRepoSpec{
				HttpConfig: api.HttpConfig{
					CaCrt: &caCrt,
				},
			}
			req, tlsConfig, err := buildHttpRepoRequestAuth(repoHttpSpec, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(tlsConfig.RootCAs.Subjects()).ToNot(BeEmpty())
		})
	})

	When("SkipServerVerification is true", func() {
		It("sets InsecureSkipVerify to true", func() {
			skip := true
			req, _ := http.NewRequest("GET", "http://example.com", nil)
			repoHttpSpec := api.HttpRepoSpec{
				HttpConfig: api.HttpConfig{
					SkipServerVerification: &skip,
				},
			}
			req, tlsConfig, err := buildHttpRepoRequestAuth(repoHttpSpec, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(tlsConfig.InsecureSkipVerify).To(BeTrue())
		})
	})

	When("no authentication details are provided", func() {
		It("does not set any auth headers", func() {
			req, _ := http.NewRequest("GET", "http://example.com", nil)
			repoHttpSpec := api.HttpRepoSpec{}
			req, tlsConfig, err := buildHttpRepoRequestAuth(repoHttpSpec, req)
			Expect(err).ToNot(HaveOccurred())
			Expect(req.Header.Get("Authorization")).To(BeEmpty())
			Expect(tlsConfig.MinVersion).To(Equal(uint16(tls.VersionTLS12)))
		})
	})
})
