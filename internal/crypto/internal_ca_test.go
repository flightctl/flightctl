package crypto

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	oscrypto "github.com/openshift/library-go/pkg/crypto"
	"github.com/stretchr/testify/require"
)

func TestInternalCA_WhenSigningAnRSACSR(t *testing.T) {
	deviceKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: "test-device"},
		SignatureAlgorithm: x509.SHA256WithRSAPSS,
	}, deviceKey)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(request)
	require.NoError(t, err)

	ecdsaCA, err := makeSelfSignedCAConfig(pkix.Name{CommonName: "ecdsa-ca"}, time.Hour, 1)
	require.NoError(t, err)
	rsaCA, err := oscrypto.MakeSelfSignedCAConfig("rsa-ca", 1)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		ca   *oscrypto.TLSCertificateConfig
		want x509.SignatureAlgorithm
	}{
		{"ECDSA issuer uses its own algorithm", ecdsaCA, x509.ECDSAWithSHA256},
		{"RSA issuer preserves RSA-PSS", rsaCA, x509.SHA256WithRSAPSS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ca := internalCA{Config: (*TLSCertificateConfig)(tc.ca), SerialGenerator: &oscrypto.RandomSerialGenerator{}}
			cert, err := ca.IssueRequestedCertificateAsX509(t.Context(), csr, 3600, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
			require.NoError(t, err)
			require.Equal(t, tc.want, cert.SignatureAlgorithm)
			require.Equal(t, deviceKey.Public(), cert.PublicKey)
			require.NoError(t, cert.CheckSignatureFrom(tc.ca.Certs[0]))
		})
	}
}
