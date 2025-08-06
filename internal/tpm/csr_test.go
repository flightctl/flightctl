package tpm

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// MockSigner implements crypto.Signer for testing
type MockSigner struct {
	key *ecdsa.PrivateKey
}

func NewMockSigner() *MockSigner {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &MockSigner{key: key}
}

func (m *MockSigner) Public() crypto.PublicKey {
	return &m.key.PublicKey
}

func (m *MockSigner) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	// Return a mock signature
	hash := sha256.Sum256(digest)
	return hash[:], nil
}

// TestBuildTCGCSRIDevID tests the TCG-CSR-IDEVID generation
func TestBuildTCGCSRIDevID(t *testing.T) {
	// Mock data for testing
	standardCSR := []byte("mock-standard-csr")
	productModel := "FlightCtl-Test-Device"
	productSerial := "test-serial-123"
	ekCert := []byte("mock-ek-certificate")
	attestationPub := []byte("mock-attestation-public-key")
	signingPub := []byte("mock-signing-public-key")
	signingCertifyInfo := []byte("mock-signing-certify-info")
	signingCertifySignature := []byte("mock-signing-certify-signature")

	// Create a mock signer
	mockSigner := NewMockSigner()

	// Test TCG-CSR-IDEVID generation
	tcgCSR, err := BuildTCGCSRIDevID(
		standardCSR,
		productModel,
		productSerial,
		ekCert,
		attestationPub,
		signingPub,
		signingCertifyInfo,
		signingCertifySignature,
		mockSigner,
	)

	require.NoError(t, err)
	require.NotEmpty(t, tcgCSR)
	require.True(t, len(tcgCSR) > 100) // Should be a substantial structure

	t.Logf("Generated TCG-CSR-IDEVID: %d bytes", len(tcgCSR))

	// Verify the generated CSR is recognized as TCG format
	require.True(t, IsTCGCSRFormat(tcgCSR))

	// Test parsing the generated CSR
	parsed, err := ParseTCGCSR(tcgCSR)
	require.NoError(t, err)
	require.True(t, parsed.IsValid)

	// Test data extraction
	tpmData, err := ExtractTPMDataFromTCGCSR(parsed)
	require.NoError(t, err)

	// Verify extracted data matches input
	require.Equal(t, ekCert, tpmData.EKCertificate)
	require.Equal(t, attestationPub, tpmData.LAKPublicKey)
	require.Equal(t, signingPub, tpmData.LDevIDPublicKey)
	require.Equal(t, signingCertifyInfo, tpmData.LDevIDCertifyInfo)
	require.Equal(t, signingCertifySignature, tpmData.LDevIDCertifySignature)
	require.Equal(t, productModel, tpmData.ProductModel)
	require.Equal(t, productSerial, tpmData.ProductSerial)

	t.Logf("Successfully parsed and extracted data from TCG-CSR-IDEVID")
}

// TestBuildTCGCSRIDevID_EmptyInputs tests building with minimal inputs
func TestBuildTCGCSRIDevID_EmptyInputs(t *testing.T) {
	mockSigner := NewMockSigner()

	tcgCSR, err := BuildTCGCSRIDevID(
		nil,      // standardCSR
		"",       // productModel
		"",       // productSerial
		[]byte{}, // ekCert
		[]byte{}, // attestationPub
		[]byte{}, // signingPub
		[]byte{}, // signingCertifyInfo
		[]byte{}, // signingCertifySignature
		mockSigner,
	)

	require.NoError(t, err)
	require.NotEmpty(t, tcgCSR)
	require.True(t, IsTCGCSRFormat(tcgCSR))

	// Verify it can be parsed
	parsed, err := ParseTCGCSR(tcgCSR)
	require.NoError(t, err)
	require.True(t, parsed.IsValid)
}

// TestBuildTCGCSRIDevID_NilSigner tests error handling with nil signer
func TestBuildTCGCSRIDevID_NilSigner(t *testing.T) {
	_, err := BuildTCGCSRIDevID(
		nil,
		"test-model",
		"test-serial",
		[]byte("ek-cert"),
		[]byte("attest-pub"),
		[]byte("signing-pub"),
		[]byte("signing-info"),
		[]byte("signing-sig"),
		nil, // nil signer should cause error
	)

	require.Error(t, err)
}

// TestUint32ToBytes tests the helper function
func TestUint32ToBytes(t *testing.T) {
	tests := []struct {
		input    uint32
		expected [4]byte
	}{
		{0x01000100, [4]byte{0x01, 0x00, 0x01, 0x00}},
		{0x00000000, [4]byte{0x00, 0x00, 0x00, 0x00}},
		{0xFFFFFFFF, [4]byte{0xFF, 0xFF, 0xFF, 0xFF}},
		{0x12345678, [4]byte{0x12, 0x34, 0x56, 0x78}},
	}

	for _, tt := range tests {
		result := uint32ToBytes(tt.input)
		require.Equal(t, tt.expected, result)
	}
}

// TestSerializeTCGContent tests content serialization
func TestSerializeTCGContent(t *testing.T) {
	content := IDevIDContent{
		StructVer:  [4]byte{0x00, 0x00, 0x01, 0x00},
		HashAlgoId: [4]byte{0x00, 0x00, 0x00, 0x0B}, // SHA256
		HashSz:     [4]byte{0x00, 0x00, 0x00, 0x20}, // 32 bytes
	}

	bytes, err := serializeTCGContent(content)
	require.NoError(t, err)
	require.NotEmpty(t, bytes)

	// Should contain at least the size fields we set
	require.True(t, len(bytes) >= 12)

	// Check that the first few bytes match what we set
	require.Equal(t, byte(0x00), bytes[0]) // StructVer first byte
	require.Equal(t, byte(0x01), bytes[2]) // StructVer third byte
	require.Equal(t, byte(0x0B), bytes[7]) // HashAlgoId last byte
}

// TestSerializeTCGPayload tests payload serialization
func TestSerializeTCGPayload(t *testing.T) {
	payload := CSRPayload{
		ProdModel:  []byte("test-model"),
		ProdSerial: []byte("test-serial"),
		EkCert:     []byte("test-ek-cert"),
	}

	bytes, err := serializeTCGPayload(payload)
	require.NoError(t, err)
	require.NotEmpty(t, bytes)

	// Should contain our test data
	require.Contains(t, string(bytes), "test-model")
	require.Contains(t, string(bytes), "test-serial")
	require.Contains(t, string(bytes), "test-ek-cert")
}

// TestTCGCSRJSON tests JSON marshalling/unmarshalling
func TestTCGCSRJSON(t *testing.T) {
	// Create a test TCG-CSR structure
	tcgCSR := TCGCSRIDevID{
		StructVer: [4]byte{0x01, 0x00, 0x01, 0x00},
		Contents:  [4]byte{0x00, 0x00, 0x00, 0x50},
		SigSz:     [4]byte{0x00, 0x00, 0x00, 0x20},
		Signature: []byte("test-signature"),
	}

	// Test marshalling
	jsonData, err := tcgCSR.MarshalJSON()
	require.NoError(t, err)
	require.NotEmpty(t, jsonData)
	require.Contains(t, string(jsonData), "signature")

	// Test unmarshalling
	var unmarshalled TCGCSRIDevID
	err = unmarshalled.UnmarshalJSON(jsonData)
	require.NoError(t, err)

	require.Equal(t, tcgCSR.StructVer, unmarshalled.StructVer)
	require.Equal(t, tcgCSR.Contents, unmarshalled.Contents)
	require.Equal(t, tcgCSR.SigSz, unmarshalled.SigSz)
	require.Equal(t, tcgCSR.Signature, unmarshalled.Signature)
}

// TestEmbedTCGCSRInX509 tests X.509 embedding (currently a no-op)
func TestEmbedTCGCSRInX509(t *testing.T) {
	standardCSR := []byte("test-csr")
	tcgCSRData := []byte("test-tcg-csr")

	result, err := EmbedTCGCSRInX509(standardCSR, tcgCSRData)
	require.NoError(t, err)

	// Currently just returns the standard CSR
	require.Equal(t, standardCSR, result)
}
