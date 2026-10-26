package certificatesigningrequest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/domain"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// TestNewCertificateSigningRequestStore_ReturnsNonNilStore proves the
// constructor builds a working Store without dialing the database. This also
// exercises the cross-package store.NewGenericStore[...] wiring, proving it
// compiles and runs at construction time with a nil DB handle.
func TestNewCertificateSigningRequestStore_ReturnsNonNilStore(t *testing.T) {
	req := require.New(t)

	s := NewCertificateSigningRequestStore(nil, logrus.New(), cacfg.DefaultDeviceEnrollmentSignerName)

	req.NotNil(s)
}

func TestCertificateSigningRequestToModel_ScopesDigestToEnrollmentSigner(t *testing.T) {
	certificatePEM := testCertificatePEM(t)

	tests := []struct {
		name                  string
		configuredSigner      string
		resourceSigner        string
		certificate           *[]byte
		wantCertificateDigest bool
	}{
		{
			name:                  "When the CSR signer is the enrollment signer it should derive the digest",
			configuredSigner:      cacfg.DefaultDeviceEnrollmentSignerName,
			resourceSigner:        cacfg.DefaultDeviceEnrollmentSignerName,
			certificate:           &certificatePEM,
			wantCertificateDigest: true,
		},
		{
			name:             "When the CSR signer is not the enrollment signer it should not derive the digest",
			configuredSigner: cacfg.DefaultDeviceEnrollmentSignerName,
			resourceSigner:   "other.example/issuer",
			certificate:      &certificatePEM,
		},
		{
			name:           "When the enrollment signer is empty it should not derive the digest",
			resourceSigner: cacfg.DefaultDeviceEnrollmentSignerName,
			certificate:    &certificatePEM,
		},
		{
			name:             "When the certificate is absent it should not derive the digest",
			configuredSigner: cacfg.DefaultDeviceEnrollmentSignerName,
			resourceSigner:   cacfg.DefaultDeviceEnrollmentSignerName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := "enrollment-credential"
			resource := &domain.CertificateSigningRequest{
				Metadata: domain.ObjectMeta{Name: &name},
				Spec:     domain.CertificateSigningRequestSpec{SignerName: test.resourceSigner},
				Status:   &domain.CertificateSigningRequestStatus{Certificate: test.certificate},
			}

			modelResource, err := certificateSigningRequestToModel(resource, test.configuredSigner)

			require.NoError(t, err)
			if test.wantCertificateDigest {
				require.NotNil(t, modelResource.CertificateDigest)
				return
			}
			require.Nil(t, modelResource.CertificateDigest)
		})
	}
}

func testCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "enrollment-credential"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	certificatePEM, err := fcrypto.EncodeCertificatePEM(certificate)
	require.NoError(t, err)
	return certificatePEM
}

func TestPreserveExistingCertificateDigest(t *testing.T) {
	digest := "certificate-digest"
	unchangedCertificate := []byte("certificate")
	changedCertificate := []byte("replacement")

	tests := []struct {
		name     string
		existing *string
		before   *[]byte
		after    *[]byte
		want     *string
	}{
		{
			name:     "When the retained certificate is unchanged it should preserve the existing digest",
			existing: &digest,
			before:   &unchangedCertificate,
			after:    &unchangedCertificate,
			want:     &digest,
		},
		{
			name:     "When the retained certificate changes it should not preserve the existing digest",
			existing: &digest,
			before:   &unchangedCertificate,
			after:    &changedCertificate,
		},
		{
			name:   "When no digest exists it should not fabricate one",
			before: &unchangedCertificate,
			after:  &unchangedCertificate,
		},
		{
			name:     "When the certificate is absent it should not preserve a digest",
			existing: &digest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := &domain.CertificateSigningRequest{Status: &domain.CertificateSigningRequestStatus{Certificate: test.before}}
			after := &domain.CertificateSigningRequest{Status: &domain.CertificateSigningRequestStatus{Certificate: test.after}}

			require.Equal(t, test.want, preserveExistingCertificateDigest(test.existing, before, after))
		})
	}
}
