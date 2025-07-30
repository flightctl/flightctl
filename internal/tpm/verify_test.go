package tpm

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVerifyTPM2CertifySignature(t *testing.T) {
	require := require.New(t)

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(err)

	testCertifyInfo := []byte("TPMS_ATTEST_structure_from_TPM2_Certify")

	hash := sha256.Sum256(testCertifyInfo)
	validSignature, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, hash[:])
	require.NoError(err)

	tests := []struct {
		name          string
		certifyInfo   []byte
		signature     []byte
		signingPubKey crypto.PublicKey
		wantErr       bool
	}{
		{
			name:          "empty certify info",
			certifyInfo:   []byte{},
			signature:     validSignature,
			signingPubKey: &rsaKey.PublicKey,
			wantErr:       true,
		},
		{
			name:          "empty signature",
			certifyInfo:   testCertifyInfo,
			signature:     []byte{},
			signingPubKey: &rsaKey.PublicKey,
			wantErr:       true,
		},
		{
			name:          "nil signing key",
			certifyInfo:   testCertifyInfo,
			signature:     validSignature,
			signingPubKey: nil,
			wantErr:       true,
		},
		{
			name:          "valid rsa signature",
			certifyInfo:   testCertifyInfo,
			signature:     validSignature,
			signingPubKey: &rsaKey.PublicKey,
			wantErr:       false,
		},
		{
			name:          "wrong signature for different data",
			certifyInfo:   []byte("different-TPMS_ATTEST-data"),
			signature:     validSignature, // signature was for testCertifyInfo, not this data
			signingPubKey: &rsaKey.PublicKey,
			wantErr:       true,
		},
		{
			name:          "invalid signature length",
			certifyInfo:   testCertifyInfo,
			signature:     []byte("too-short"),
			signingPubKey: &rsaKey.PublicKey,
			wantErr:       true,
		},
		{
			name:          "unsupported key type",
			certifyInfo:   testCertifyInfo,
			signature:     validSignature,
			signingPubKey: "unsupported-key-type",
			wantErr:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyTPM2CertifySignature(tc.certifyInfo, tc.signature, tc.signingPubKey)

			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestVerifyAttestationBundle(t *testing.T) {
	require := require.New(t)

	trustedRoots := x509.NewCertPool()

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization:  []string{"Test"},
			Country:       []string{"US"},
			Province:      []string{""},
			Locality:      []string{"Test"},
			StreetAddress: []string{""},
			PostalCode:    []string{""},
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}

	testKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(err)

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &testKey.PublicKey, testKey)
	require.NoError(err)

	// add to trusted roots
	cert, err := x509.ParseCertificate(certDER)
	require.NoError(err)
	trustedRoots.AddCert(cert)

	lakInfo := []byte("TPMS_ATTEST_LAK_from_TPM2_Certify")
	ldevidInfo := []byte("TPMS_ATTEST_LDevID_from_TPM2_Certify")

	// generate signatures
	lakHash := sha256.Sum256(lakInfo)
	realLAKSignature, err := rsa.SignPKCS1v15(rand.Reader, testKey, crypto.SHA256, lakHash[:])
	require.NoError(err)

	ldevidHash := sha256.Sum256(ldevidInfo)
	realLDevIDSignature, err := rsa.SignPKCS1v15(rand.Reader, testKey, crypto.SHA256, ldevidHash[:])
	require.NoError(err)

	tests := []struct {
		name    string
		bundle  *AttestationBundle
		roots   *x509.CertPool
		wantErr bool
	}{
		{
			name:    "nil bundle",
			bundle:  nil,
			roots:   trustedRoots,
			wantErr: true,
		},
		{
			name: "invalid EK certificate",
			bundle: &AttestationBundle{
				EKCert: []byte("invalid-cert"),
			},
			roots:   trustedRoots,
			wantErr: true,
		},
		{
			name: "EK cert not in trusted roots",
			bundle: &AttestationBundle{
				EKCert:                 mustCreateUntrustedCert(),
				LAKCertifyInfo:         lakInfo,
				LAKCertifySignature:    realLAKSignature,
				LDevIDCertifyInfo:      ldevidInfo,
				LDevIDCertifySignature: realLDevIDSignature,
			},
			roots:   trustedRoots,
			wantErr: true,
		},
		{
			name: "valid bundle with trusted cert and signatures",
			bundle: &AttestationBundle{
				EKCert:                 certDER,
				LAKCertifyInfo:         lakInfo,
				LAKCertifySignature:    realLAKSignature,
				LDevIDCertifyInfo:      ldevidInfo,
				LDevIDCertifySignature: realLDevIDSignature,
			},
			roots:   trustedRoots,
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyAttestationBundle(tc.bundle, tc.roots)

			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

// create an untrusted certificate
func mustCreateUntrustedCert() []byte {
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			Organization: []string{"Untrusted"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}

	return certDER
}
