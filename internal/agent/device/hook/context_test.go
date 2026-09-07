package hook

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
)

// generateTestCert creates a self-signed certificate PEM and returns
// the PEM bytes, the parsed x509.Certificate, and raw DER bytes.
func generateTestCert(t *testing.T, serial *big.Int, notAfter time.Time) ([]byte, *x509.Certificate, []byte) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "test-device",
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	parsedCert, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)

	return certPEM, parsedCert, certDER
}

// formatSerialHex converts a *big.Int to colon-separated uppercase hex,
// matching the expected output of ParseCertMetadata.
func formatSerialHex(serial *big.Int) string {
	b := serial.Bytes()
	if len(b) == 0 {
		return ""
	}
	out := make([]byte, 0, len(b)*3-1)
	for i, v := range b {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, fmt.Sprintf("%02X", v)...)
	}
	return string(out)
}

// TestParseCertMetadata verifies certificate metadata extraction from PEM input.
func TestParseCertMetadata(t *testing.T) {
	serial := big.NewInt(0x00ABCD)
	notAfter := time.Date(2027, 9, 6, 0, 0, 0, 0, time.UTC)
	certPEM, parsedCert, certDER := generateTestCert(t, serial, notAfter)

	expectedSHA := fmt.Sprintf("%x", sha256.Sum256(certDER))
	expectedSerial := formatSerialHex(parsedCert.SerialNumber)
	expectedNotAfter := parsedCert.NotAfter.Format(time.RFC3339)

	tests := []struct {
		name       string
		pemData    []byte
		wantNil    bool
		wantErr    bool
		wantSerial string
		wantAfter  string
		wantSHA    string
	}{
		{
			name:       "When given valid PEM it should return correct metadata",
			pemData:    certPEM,
			wantSerial: expectedSerial,
			wantAfter:  expectedNotAfter,
			wantSHA:    expectedSHA,
		},
		{
			name:    "When given nil input it should return nil without error",
			pemData: nil,
			wantNil: true,
		},
		{
			name:    "When given empty input it should return nil without error",
			pemData: []byte{},
			wantNil: true,
		},
		{
			name:    "When given malformed PEM it should return error",
			pemData: []byte("not-a-pem"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			meta, err := ParseCertMetadata(tt.pemData)

			if tt.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)

			if tt.wantNil {
				require.Nil(meta)
				return
			}

			require.NotNil(meta)
			require.Equal(tt.wantSerial, meta.SerialNumber, "serialNumber should be colon-separated uppercase hex")
			require.Equal(tt.wantAfter, meta.NotAfter, "notAfter should be RFC3339")
			require.Equal(tt.wantSHA, meta.SHA256, "sha256 should be lowercase hex")
		})
	}
}

// TestParseCertMetadataMultiCert verifies only the first certificate is used when PEM contains multiple certs.
func TestParseCertMetadataMultiCert(t *testing.T) {
	require := require.New(t)

	serial1 := big.NewInt(111)
	serial2 := big.NewInt(222)
	notAfter := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)

	pem1, _, _ := generateTestCert(t, serial1, notAfter)
	pem2, _, _ := generateTestCert(t, serial2, notAfter)

	// Concatenate two PEM certs
	multiPEM := append(pem1, pem2...)

	meta, err := ParseCertMetadata(multiPEM)
	require.NoError(err)
	require.NotNil(meta)
	// Should use the first cert's serial
	require.Equal(formatSerialHex(serial1), meta.SerialNumber)
}

// TestWriteHookContext verifies hook-context.json content, permissions, and JSON shape.
func TestWriteHookContext(t *testing.T) {
	tests := []struct {
		name      string
		hookType  string
		enrollCtx *EnrollmentContext
		wantCert  bool
	}{
		{
			name:     "When pre-enrollment it should write context with null managementCertificate",
			hookType: "BeforeEnrolling",
			enrollCtx: &EnrollmentContext{
				DeviceName:            "device-01",
				SystemInfo:            map[string]interface{}{"os": "linux"},
				Labels:                map[string]string{"env": "prod"},
				ManagementCertificate: nil,
			},
			wantCert: false,
		},
		{
			name:     "When post-enrollment it should write context with certificate fields",
			hookType: "AfterEnrolling",
			enrollCtx: &EnrollmentContext{
				DeviceName: "device-01",
				SystemInfo: map[string]interface{}{"os": "linux"},
				Labels:     map[string]string{"env": "prod"},
				ManagementCertificate: &CertificateMetadata{
					SerialNumber: "00:AB:CD",
					NotAfter:     "2027-09-06T00:00:00Z",
					SHA256:       "abcdef1234567890",
				},
			},
			wantCert: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			tempDir := t.TempDir()
			readWriter := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
			)

			jsonBytes, err := writeHookContext(readWriter, tt.hookType, tt.enrollCtx)
			require.NoError(err)
			require.NotEmpty(jsonBytes)

			// Verify file was written
			fullPath := filepath.Join(tempDir, HookContextPath)
			data, err := os.ReadFile(fullPath)
			require.NoError(err)
			require.Equal(string(jsonBytes), string(data))

			// Verify file permissions (mode 0600)
			info, err := os.Stat(fullPath)
			require.NoError(err)
			require.Equal(os.FileMode(HookContextMode), info.Mode().Perm())

			// Verify JSON structure
			var ctx hookContext
			require.NoError(json.Unmarshal(data, &ctx))
			require.Equal(tt.hookType, ctx.Hook)
			require.Equal("device-01", ctx.DeviceName)
			require.Equal("linux", ctx.SystemInfo["os"])
			require.Equal("prod", ctx.Labels["env"])

			if tt.wantCert {
				require.NotNil(ctx.ManagementCertificate)
				require.Equal("00:AB:CD", ctx.ManagementCertificate.SerialNumber)
				require.Equal("2027-09-06T00:00:00Z", ctx.ManagementCertificate.NotAfter)
				require.Equal("abcdef1234567890", ctx.ManagementCertificate.SHA256)
			} else {
				require.Nil(ctx.ManagementCertificate)
			}
		})
	}
}

// TestWriteHookContextDirectoryCreation verifies HookContextDir is created on first write.
func TestWriteHookContextDirectoryCreation(t *testing.T) {
	require := require.New(t)

	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)

	enrollCtx := &EnrollmentContext{
		DeviceName: "device-01",
		SystemInfo: map[string]interface{}{},
		Labels:     map[string]string{},
	}

	// Directory should not exist yet
	dirPath := filepath.Join(tempDir, HookContextDir)
	_, err := os.Stat(dirPath)
	require.True(os.IsNotExist(err))

	_, err = writeHookContext(readWriter, "BeforeEnrolling", enrollCtx)
	require.NoError(err)

	// Directory should now exist
	info, err := os.Stat(dirPath)
	require.NoError(err)
	require.True(info.IsDir())
}

// TestHookContextJSONStructure verifies JSON field names and empty-map serialization.
func TestHookContextJSONStructure(t *testing.T) {
	require := require.New(t)

	tempDir := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)

	// Empty maps should serialize as {} not null
	enrollCtx := &EnrollmentContext{
		DeviceName: "device-01",
		SystemInfo: map[string]interface{}{},
		Labels:     map[string]string{},
	}

	jsonBytes, err := writeHookContext(readWriter, "BeforeEnrolling", enrollCtx)
	require.NoError(err)

	// Verify JSON field names match design spec
	var raw map[string]interface{}
	require.NoError(json.Unmarshal(jsonBytes, &raw))

	require.Contains(raw, "hook")
	require.Contains(raw, "deviceName")
	require.Contains(raw, "systemInfo")
	require.Contains(raw, "labels")
	require.Contains(raw, "managementCertificate")

	// systemInfo and labels should be empty objects, not null
	require.NotNil(raw["systemInfo"])
	require.NotNil(raw["labels"])
	require.Nil(raw["managementCertificate"])
}
