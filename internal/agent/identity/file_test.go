package identity

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSoftwareExportableProvider_NewExportable(t *testing.T) {
	tests := []struct {
		name    string
		appName string
	}{
		{
			name:    "success",
			appName: "test-app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newSoftwareExportableProvider()

			result, err := provider.NewExportable(tt.appName)

			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify the name is set correctly
			require.Equal(t, tt.appName, result.Name())

			// Verify CSR is not empty and is valid PEM
			csr, err := result.CSR()
			require.NoError(t, err)
			require.NotEmpty(t, csr)
			csrBlock, _ := pem.Decode(csr)
			require.NotNil(t, csrBlock, "CSR should be valid PEM")
			require.Equal(t, "CERTIFICATE REQUEST", csrBlock.Type)

			// Parse and validate the CSR
			parsedCSR, err := x509.ParseCertificateRequest(csrBlock.Bytes)
			require.NoError(t, err)
			require.Equal(t, tt.appName, parsedCSR.Subject.CommonName)

			// Verify KeyPEM is not empty and is valid PEM
			keyPEM, err := result.KeyPEM()
			require.NoError(t, err)
			require.NotEmpty(t, keyPEM)
			keyBlock, _ := pem.Decode(keyPEM)
			require.NotNil(t, keyBlock, "Key should be valid PEM")
			require.Contains(t, []string{"PRIVATE KEY", "EC PRIVATE KEY", "RSA PRIVATE KEY"}, keyBlock.Type)

			// Parse and validate the private key
			var privateKey crypto.PrivateKey
			switch keyBlock.Type {
			case "PRIVATE KEY":
				privateKey, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
			case "EC PRIVATE KEY":
				privateKey, err = x509.ParseECPrivateKey(keyBlock.Bytes)
			case "RSA PRIVATE KEY":
				privateKey, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
			}
			require.NoError(t, err)
			require.NotNil(t, privateKey)

			// Verify the private key implements crypto.Signer
			_, ok := privateKey.(crypto.Signer)
			require.True(t, ok, "Private key should implement crypto.Signer")
		})
	}
}

func TestSoftwareExportableProvider_NewExportable_GeneratesUniqueKeys(t *testing.T) {
	provider := newSoftwareExportableProvider()

	// Generate two exportables with the same name
	result1, err := provider.NewExportable("test-app")
	require.NoError(t, err)
	require.NotNil(t, result1)

	result2, err := provider.NewExportable("test-app")
	require.NoError(t, err)
	require.NotNil(t, result2)

	// Verify that different keys are generated each time
	csr1, err := result1.CSR()
	require.NoError(t, err)
	csr2, err := result2.CSR()
	require.NoError(t, err)
	require.NotEqual(t, csr1, csr2, "CSRs should be different")

	keyPEM1, err := result1.KeyPEM()
	require.NoError(t, err)
	keyPEM2, err := result2.KeyPEM()
	require.NoError(t, err)
	require.NotEqual(t, keyPEM1, keyPEM2, "Key PEMs should be different")

	// But names should be the same
	require.Equal(t, result1.Name(), result2.Name())
}
