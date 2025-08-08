package tpm

import (
	"crypto"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"io"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm/tpmutil"
	"github.com/stretchr/testify/require"
)

// mockTestSigner is a mock signer for testing
type mockTestSigner struct{}

func (m *mockTestSigner) Public() crypto.PublicKey { return nil }
func (m *mockTestSigner) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return []byte("mock-signature"), nil
}

// TestIsTCGCSRFormat tests the TCG-CSR format detection
func TestIsTCGCSRFormat(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected bool
	}{
		{
			name:     "Valid TCG-CSR format",
			data:     []byte{0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x50, 0x00, 0x00, 0x00, 0x20},
			expected: true,
		},
		{
			name:     "Invalid version",
			data:     []byte{0x02, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x50, 0x00, 0x00, 0x00, 0x20},
			expected: false,
		},
		{
			name:     "Too short data",
			data:     []byte{0x01, 0x00, 0x01},
			expected: false,
		},
		{
			name:     "Empty data",
			data:     []byte{},
			expected: false,
		},
		{
			name:     "Random data",
			data:     []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x00, 0x00, 0x50, 0x00, 0x00, 0x00, 0x20},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsTCGCSRFormat(tt.data)
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestTCGCSRParser_readUint32 tests the internal readUint32 method
func TestTCGCSRParser_readUint32(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		startPos    int
		expected    uint32
		expectError bool
	}{
		{
			name:     "Valid read at start",
			data:     []byte{0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x50},
			startPos: 0,
			expected: 0x01000100,
		},
		{
			name:     "Valid read in middle",
			data:     []byte{0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x50},
			startPos: 4,
			expected: 0x00000050,
		},
		{
			name:        "Insufficient data",
			data:        []byte{0x01, 0x00, 0x01},
			startPos:    0,
			expectError: true,
		},
		{
			name:        "Read beyond end",
			data:        []byte{0x01, 0x00, 0x01, 0x00},
			startPos:    2,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := &TCGCSRParser{data: tt.data, pos: tt.startPos}
			result, err := parser.readUint32()

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestTCGCSRParser_readBytes tests the internal readBytes method
func TestTCGCSRParser_readBytes(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		startPos    int
		length      int
		expected    []byte
		expectError bool
	}{
		{
			name:     "Valid read",
			data:     []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06},
			startPos: 1,
			length:   3,
			expected: []byte{0x02, 0x03, 0x04},
		},
		{
			name:     "Zero length read",
			data:     []byte{0x01, 0x02, 0x03, 0x04},
			startPos: 0,
			length:   0,
			expected: []byte{},
		},
		{
			name:        "Read beyond end",
			data:        []byte{0x01, 0x02, 0x03, 0x04},
			startPos:    2,
			length:      5,
			expectError: true,
		},
		{
			name:        "Negative length",
			data:        []byte{0x01, 0x02, 0x03, 0x04},
			startPos:    0,
			length:      -1,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := &TCGCSRParser{data: tt.data, pos: tt.startPos}
			result, err := parser.readBytes(tt.length)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestParseTCGCSR tests the main parsing function with invalid data
func TestParseTCGCSR_InvalidData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "Not TCG-CSR format",
			data: []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x00, 0x00, 0x50},
		},
		{
			name: "Empty data",
			data: []byte{},
		},
		{
			name: "Truncated header",
			data: []byte{0x01, 0x00, 0x01, 0x00, 0x00, 0x00},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTCGCSR(tt.data)
			require.Error(t, err)
		})
	}
}

// TestExtractTPMDataFromTCGCSR tests the data extraction function
func TestExtractTPMDataFromTCGCSR(t *testing.T) {
	// Test with nil input
	t.Run("Nil input", func(t *testing.T) {
		_, err := extractTPMDataFromTCGCSR(nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid parsed TCG-CSR data")
	})

	// Test with missing CSRContents
	t.Run("Missing CSRContents", func(t *testing.T) {
		parsed := &ParsedTCGCSR{
			IsValid: true,
		}
		_, err := extractTPMDataFromTCGCSR(parsed)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid parsed TCG-CSR data")
	})

	// Test with missing Payload
	t.Run("Missing Payload", func(t *testing.T) {
		parsed := &ParsedTCGCSR{
			IsValid:     true,
			CSRContents: &ParsedTCGContent{},
		}
		_, err := extractTPMDataFromTCGCSR(parsed)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid parsed TCG-CSR data")
	})

	// Test with valid data
	t.Run("Valid data", func(t *testing.T) {
		testEKCert := []byte("test-ek-cert")
		testLAKPubKey := []byte("test-lak-pubkey")
		testProductModel := "test-model"
		testProductSerial := "test-serial"

		parsed := &ParsedTCGCSR{
			IsValid: true,
			CSRContents: &ParsedTCGContent{
				Payload: &ParsedTCGPayload{
					EkCert:                  testEKCert,
					AttestPub:               testLAKPubKey,
					AtCertifyInfo:           []byte("attest-certify-info"),
					AtCertifyInfoSignature:  []byte("attest-certify-sig"),
					SigningPub:              []byte("signing-pub"),
					SgnCertifyInfo:          []byte("signing-certify-info"),
					SgnCertifyInfoSignature: []byte("signing-certify-sig"),
					ProdModel:               []byte(testProductModel),
					ProdSerial:              []byte(testProductSerial),
				},
			},
		}

		data, err := extractTPMDataFromTCGCSR(parsed)
		require.NoError(t, err)
		require.NotNil(t, data)
		require.Equal(t, testEKCert, data.EKCertificate)
		require.Equal(t, testLAKPubKey, data.LAKPublicKey)
		require.Equal(t, testProductModel, data.ProductModel)
		require.Equal(t, testProductSerial, data.ProductSerial)
	})
}

// Helper function to create a minimal valid-looking TCG-CSR structure for testing
func createMinimalTCGCSR(t *testing.T) []byte {
	// This creates a minimal structure that passes format detection
	// but may not be fully parseable - that's fine for format detection tests
	data := make([]byte, 100)
	// Set version to valid TCG-CSR version (0x01000100)
	data[0] = 0x01
	data[1] = 0x00
	data[2] = 0x01
	data[3] = 0x00
	// Set some content size
	data[4] = 0x00
	data[5] = 0x00
	data[6] = 0x00
	data[7] = 0x50
	// Set signature size
	data[8] = 0x00
	data[9] = 0x00
	data[10] = 0x00
	data[11] = 0x10
	return data
}

// createTCGCSRWithEmbeddedCSR creates a TCG CSR with an optional embedded standard CSR for testing
func createTCGCSRWithEmbeddedCSR(t *testing.T, embeddedCSR []byte) []byte {
	// Use a mock signer for testing
	signer := &mockTestSigner{}

	tcgCSR, err := BuildTCGCSRIDevID(
		embeddedCSR,
		"test-model",
		"test-serial",
		nil, // ekCert
		nil, // attestationPub
		nil, // signingPub
		nil, // signingCertifyInfo
		nil, // signingCertifySignature
		signer,
	)
	require.NoError(t, err)
	return tcgCSR
}

// TestCreateMinimalTCGCSR tests our helper function
func TestCreateMinimalTCGCSR(t *testing.T) {
	data := createMinimalTCGCSR(t)
	require.True(t, IsTCGCSRFormat(data))
	require.True(t, len(data) >= 12)
}

// TestVerifyTCGCSRChainOfTrust tests TCG-CSR chain of trust verification
func TestVerifyTCGCSRChainOfTrust(t *testing.T) {
	t.Run("Invalid TCG-CSR data", func(t *testing.T) {
		err := VerifyTCGCSRChainOfTrust([]byte("not-tcg-csr"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "data is not in TCG-CSR-IDEVID format")
	})

	t.Run("Minimal TCG-CSR fails parsing", func(t *testing.T) {
		tcgData := []byte{0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x50, 0x00, 0x00, 0x00, 0x20}
		err := VerifyTCGCSRChainOfTrust(tcgData)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to parse TCG-CSR")
	})
}

// TestVerifyTCGCSRChainOfTrustWithRoots tests verification with trusted roots
func TestVerifyTCGCSRChainOfTrustWithRoots(t *testing.T) {
	t.Run("Invalid data with roots", func(t *testing.T) {
		roots := x509.NewCertPool()
		err := VerifyTCGCSRChainOfTrustWithRoots([]byte("not-tcg-csr"), roots)
		require.Error(t, err)
		require.Contains(t, err.Error(), "data is not in TCG-CSR-IDEVID format")
	})

	t.Run("Nil roots parameter", func(t *testing.T) {
		tcgData := []byte{0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x50, 0x00, 0x00, 0x00, 0x20}
		err := VerifyTCGCSRChainOfTrustWithRoots(tcgData, nil)
		require.Error(t, err)
		// Should still fail on parsing, not on nil roots
		require.Contains(t, err.Error(), "failed to parse TCG-CSR")
	})
}

// TestStripSANExtensionOIDs tests SAN extension removal
func TestStripSANExtensionOIDs(t *testing.T) {
	// Create a test certificate with SAN extension in unhandled critical extensions
	cert := &x509.Certificate{
		UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
			{1, 2, 3, 4},    // Some other extension
			{2, 5, 29, 17},  // SAN extension (should be removed)
			{1, 2, 3, 4, 5}, // Another extension
		},
	}

	// Strip SAN extensions
	stripSANExtensionOIDs(cert)

	// Verify SAN extension was removed
	require.Len(t, cert.UnhandledCriticalExtensions, 2)

	// Verify the remaining extensions don't include SAN
	for _, ext := range cert.UnhandledCriticalExtensions {
		require.False(t, ext.Equal([]int{2, 5, 29, 17}))
	}
}

// TestStripSANExtensionOIDs_NoSANExtension tests when there's no SAN extension
func TestStripSANExtensionOIDs_NoSANExtension(t *testing.T) {
	cert := &x509.Certificate{
		UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
			{1, 2, 3, 4},    // Some extension
			{1, 2, 3, 4, 5}, // Another extension
		},
	}

	originalLen := len(cert.UnhandledCriticalExtensions)
	stripSANExtensionOIDs(cert)

	// Should remain unchanged
	require.Len(t, cert.UnhandledCriticalExtensions, originalLen)
}

// TestStripSANExtensionOIDs_EmptyExtensions tests with empty extensions
func TestStripSANExtensionOIDs_EmptyExtensions(t *testing.T) {
	cert := &x509.Certificate{
		UnhandledCriticalExtensions: []asn1.ObjectIdentifier{},
	}

	stripSANExtensionOIDs(cert)

	// Should remain empty
	require.Len(t, cert.UnhandledCriticalExtensions, 0)
}

// TestStripSANExtensionOIDs_MultipleSANExtensions tests multiple SAN extensions
func TestStripSANExtensionOIDs_MultipleSANExtensions(t *testing.T) {
	cert := &x509.Certificate{
		UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
			{2, 5, 29, 17}, // SAN extension
			{1, 2, 3, 4},   // Other extension
			{2, 5, 29, 17}, // Another SAN extension
		},
	}

	stripSANExtensionOIDs(cert)

	// Should have only the non-SAN extension
	require.Len(t, cert.UnhandledCriticalExtensions, 1)
	require.True(t, cert.UnhandledCriticalExtensions[0].Equal([]int{1, 2, 3, 4}))
}

// TestNormalizeEnrollmentCSR tests the NormalizeEnrollmentCSR function
func TestNormalizeEnrollmentCSR(t *testing.T) {
	t.Run("Standard CSR returns unchanged", func(t *testing.T) {
		standardCSR := "-----BEGIN CERTIFICATE REQUEST-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA...\n-----END CERTIFICATE REQUEST-----"

		result, isTPM, err := NormalizeEnrollmentCSR(standardCSR)
		require.NoError(t, err)
		require.False(t, isTPM)
		require.Equal(t, []byte(standardCSR), result)
	})

	t.Run("TCG CSR with embedded standard CSR", func(t *testing.T) {
		// Create a minimal TCG CSR with embedded standard CSR
		embeddedCSR := []byte("embedded-standard-csr-data")
		tcgCSR := createTCGCSRWithEmbeddedCSR(t, embeddedCSR)
		tcgCSRBase64 := base64.StdEncoding.EncodeToString(tcgCSR)

		result, isTPM, err := NormalizeEnrollmentCSR(tcgCSRBase64)
		require.NoError(t, err)
		require.True(t, isTPM)
		require.Equal(t, embeddedCSR, result)
	})

	t.Run("TCG CSR without embedded standard CSR", func(t *testing.T) {
		// Create a minimal TCG CSR without embedded standard CSR
		tcgCSR := createTCGCSRWithEmbeddedCSR(t, nil)
		tcgCSRBase64 := base64.StdEncoding.EncodeToString(tcgCSR)

		_, isTPM, err := NormalizeEnrollmentCSR(tcgCSRBase64)
		require.Error(t, err)
		require.True(t, isTPM)
		require.Contains(t, err.Error(), "invalid X.509 data parsed from TCG CSR")
	})

	t.Run("Invalid TCG CSR data", func(t *testing.T) {
		// Create invalid TCG CSR data - has valid header but truncated content
		invalidData := []byte{
			0x01, 0x00, 0x01, 0x00, // Valid TCG version header
			0x00, 0x00, 0x00, 0x10, // Content size (16 bytes)
			0x00, 0x00, 0x00, 0x00, // Signature size (0 bytes)
			// Missing actual content - this will cause parsing to fail
		}
		invalidDataBase64 := base64.StdEncoding.EncodeToString(invalidData)

		_, isTPM, err := NormalizeEnrollmentCSR(invalidDataBase64)
		require.Error(t, err)
		require.True(t, isTPM)
		require.Contains(t, err.Error(), "failed to parse TCG CSR")
	})
}

// TestFetchIntermediateCertificates tests the intermediate certificate fetching functionality
func TestFetchIntermediateCertificates(t *testing.T) {
	t.Run("Certificate with no AIA URLs", func(t *testing.T) {
		cert := &x509.Certificate{
			IssuingCertificateURL: nil,
		}

		intermediates, err := fetchIntermediateCertificates(cert)
		require.NoError(t, err)
		require.NotNil(t, intermediates)
	})

	t.Run("Certificate with empty AIA URLs", func(t *testing.T) {
		cert := &x509.Certificate{
			IssuingCertificateURL: []string{},
		}

		intermediates, err := fetchIntermediateCertificates(cert)
		require.NoError(t, err)
		require.NotNil(t, intermediates)
	})

	t.Run("Nil certificate", func(t *testing.T) {
		_, err := fetchIntermediateCertificates(nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no EK certificate provided")
	})
}

func TestFetchIntermediateCertificates_Integration(t *testing.T) {
	t.Skip("Integration test requires root access to TPM device and makes an http call to a remote server")
	rw := fileio.NewReadWriter()
	l := log.NewPrefixLogger("test")
	tpmPath, err := discoverAndValidateTPM(rw, l, "")
	require.NoError(t, err)

	// open the TPM connection
	conn, err := tpmutil.OpenTPM(tpmPath)
	defer conn.Close()
	require.NoError(t, err)

	session := tpmSession{conn: conn, log: l}

	// Get EK certificate from TPM using direct session access
	ekCertBytes, err := session.GetEndorsementKeyCert()
	require.NoError(t, err)
	require.NotEmpty(t, ekCertBytes, "TPM should have an EK certificate")

	// Parse the EK certificate
	ekCert, err := x509.ParseCertificate(ekCertBytes)
	require.NoError(t, err)
	require.NotNil(t, ekCert)

	t.Logf("Found EK certificate: Subject=%s, Issuer=%s", ekCert.Subject.String(), ekCert.Issuer.String())
	t.Logf("AIA URLs: %v", ekCert.IssuingCertificateURL)

	// Call fetchIntermediateCertificates with the real EK cert
	intermediateCerts, err := fetchIntermediateCertificates(ekCert)

	// Assert we get a cert pool back (even if empty)
	require.NotNil(t, intermediateCerts)

	// If there are AIA URLs, we should either succeed or get a descriptive error
	if len(ekCert.IssuingCertificateURL) > 0 {
		if err != nil {
			t.Logf("Intermediate certificate fetch had errors (this is OK): %v", err)
			// Ensure error mentions the URL(s) that failed
			for _, url := range ekCert.IssuingCertificateURL {
				require.Contains(t, err.Error(), url)
			}
		} else {
			t.Logf("Successfully fetched intermediate certificates")
		}
	} else {
		t.Logf("No AIA URLs found in EK certificate")
		require.NoError(t, err)
	}
}
