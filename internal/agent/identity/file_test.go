package identity

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestFileProvider_NewExportable(t *testing.T) {
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
			provider := &fileProvider{
				log: log.NewPrefixLogger("test"),
			}

			result, err := provider.NewExportable(tt.appName)

			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify the name is set correctly
			require.Equal(t, tt.appName, result.Name)

			// Verify CSR is not empty and is valid PEM
			require.NotEmpty(t, result.CSR)
			csrBlock, _ := pem.Decode(result.CSR)
			require.NotNil(t, csrBlock, "CSR should be valid PEM")
			require.Equal(t, "CERTIFICATE REQUEST", csrBlock.Type)

			// Parse and validate the CSR
			csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
			require.NoError(t, err)
			require.Equal(t, tt.appName, csr.Subject.CommonName)

			// Verify KeyPEM is not empty and is valid PEM
			require.NotEmpty(t, result.KeyPEM)
			keyBlock, _ := pem.Decode(result.KeyPEM)
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

func TestFileProvider_NewExportable_GeneratesUniqueKeys(t *testing.T) {
	provider := &fileProvider{
		log: log.NewPrefixLogger("test"),
	}

	// Generate two exportables with the same name
	result1, err := provider.NewExportable("test-app")
	require.NoError(t, err)
	require.NotNil(t, result1)

	result2, err := provider.NewExportable("test-app")
	require.NoError(t, err)
	require.NotNil(t, result2)

	// Verify that different keys are generated each time
	require.NotEqual(t, result1.CSR, result2.CSR, "CSRs should be different")
	require.NotEqual(t, result1.KeyPEM, result2.KeyPEM, "Key PEMs should be different")

	// But names should be the same
	require.Equal(t, result1.Name, result2.Name)
}
