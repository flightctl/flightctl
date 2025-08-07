package tpm

import (
	"crypto/x509"
	"encoding/asn1"
	"testing"

	"github.com/stretchr/testify/require"
)

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
		_, err := ExtractTPMDataFromTCGCSR(nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid parsed TCG-CSR data")
	})

	// Test with missing CSRContents
	t.Run("Missing CSRContents", func(t *testing.T) {
		parsed := &ParsedTCGCSR{
			IsValid: true,
		}
		_, err := ExtractTPMDataFromTCGCSR(parsed)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid parsed TCG-CSR data")
	})

	// Test with missing Payload
	t.Run("Missing Payload", func(t *testing.T) {
		parsed := &ParsedTCGCSR{
			IsValid:     true,
			CSRContents: &ParsedTCGContent{},
		}
		_, err := ExtractTPMDataFromTCGCSR(parsed)
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

		data, err := ExtractTPMDataFromTCGCSR(parsed)
		require.NoError(t, err)
		require.NotNil(t, data)
		require.Equal(t, testEKCert, data.EKCertificate)
		require.Equal(t, testLAKPubKey, data.LAKPublicKey)
		require.Equal(t, testProductModel, data.ProductModel)
		require.Equal(t, testProductSerial, data.ProductSerial)
	})
}

// TestVerifyStandardCSR tests standard X.509 CSR verification
func TestVerifyStandardCSR(t *testing.T) {
	// Test with invalid CSR data
	t.Run("Invalid CSR data", func(t *testing.T) {
		invalidCSR := []byte("not-a-csr")
		err := VerifyStandardCSR(invalidCSR)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to parse X.509 CSR")
	})

	// Test with empty data
	t.Run("Empty data", func(t *testing.T) {
		err := VerifyStandardCSR([]byte{})
		require.Error(t, err)
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

// TestCreateMinimalTCGCSR tests our helper function
func TestCreateMinimalTCGCSR(t *testing.T) {
	data := createMinimalTCGCSR(t)
	require.True(t, IsTCGCSRFormat(data))
	require.True(t, len(data) >= 12)
}

// TestVerifyEnrollmentCSR tests the main CSR verification function
func TestVerifyEnrollmentCSR(t *testing.T) {
	t.Run("Non-TCG CSR falls back to standard verification", func(t *testing.T) {
		// This should fail standard CSR verification
		err := VerifyEnrollmentCSR([]byte("not-a-csr"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to parse X.509 CSR")
	})

	t.Run("TCG CSR format triggers TCG verification", func(t *testing.T) {
		// Create minimal TCG-CSR that passes format detection but fails parsing
		tcgData := []byte{0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x50, 0x00, 0x00, 0x00, 0x20}
		err := VerifyEnrollmentCSR(tcgData)
		// Should attempt TCG verification and fail during parsing
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to parse TCG-CSR")
	})
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
	StripSANExtensionOIDs(cert)

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
	StripSANExtensionOIDs(cert)

	// Should remain unchanged
	require.Len(t, cert.UnhandledCriticalExtensions, originalLen)
}

// TestStripSANExtensionOIDs_EmptyExtensions tests with empty extensions
func TestStripSANExtensionOIDs_EmptyExtensions(t *testing.T) {
	cert := &x509.Certificate{
		UnhandledCriticalExtensions: []asn1.ObjectIdentifier{},
	}

	StripSANExtensionOIDs(cert)

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

	StripSANExtensionOIDs(cert)

	// Should have only the non-SAN extension
	require.Len(t, cert.UnhandledCriticalExtensions, 1)
	require.True(t, cert.UnhandledCriticalExtensions[0].Equal([]int{1, 2, 3, 4}))
}
