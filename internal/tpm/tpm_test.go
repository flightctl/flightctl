//go:build amd64 || arm64

package tpm

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm-tools/simulator"
	legacy "github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/stretchr/testify/require"
)

type TestFixture struct {
	client *Client
}

type TestData struct {
	client           *Client
	srk              *tpm2.NamedHandle
	ldevid           *tpm2.NamedHandle
	lak              *client.Key
	nonce            []byte
	pcrSel           *legacy.PCRSelection
	persistentHandle *tpm2.TPMHandle
}

func openTPMSimulator(t *testing.T) (*Client, error) {
	t.Helper()
	require := require.New(t)

	simulator, err := simulator.Get()
	require.NoError(err)

	tpm := &Client{
		conn: simulator,
	}

	// Auto-generate keys like OpenTPM does
	_, err = tpm.generateSRKPrimary()
	require.NoError(err)

	_, err = tpm.createLDevID()
	require.NoError(err)

	_, err = tpm.getLDevIDPubKey()
	require.NoError(err)

	return tpm, nil
}

func setupTestFixture(t *testing.T) (*TestFixture, error) {
	t.Helper()

	tpm, err := openTPMSimulator(t)
	if err != nil {
		return nil, fmt.Errorf("unable to open tpm simulator: %w", err)
	}

	return &TestFixture{client: tpm}, nil
}

func setupTestData(t *testing.T) (TestData, func()) {
	t.Helper()
	require := require.New(t)

	f, err := setupTestFixture(t)
	require.NoError(err)

	lak, err := f.client.CreateLAK()
	require.NoError(err)

	nonce := make([]byte, 8)
	_, err = io.ReadFull(rand.Reader, nonce)
	require.NoError(err)

	selection := client.FullPcrSel(legacy.AlgSHA256)

	// Use a test handle
	handle := persistentHandleMin + 1

	data := TestData{
		client:           f.client,
		srk:              f.client.srk,
		ldevid:           f.client.ldevid,
		lak:              lak,
		nonce:            nonce,
		pcrSel:           &selection,
		persistentHandle: &handle,
	}

	cleanup := func() {
		data.client.Close(context.Background())
		data.lak.Close()
	}

	return data, cleanup
}

func createTestReadWriter(t *testing.T) fileio.ReadWriter {
	t.Helper()
	tempDir := t.TempDir()
	return fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
}

func TestLAK(t *testing.T) {
	data, cleanup := setupTestData(t)
	defer cleanup()

	// This template is based on that used for AK ECC key creation in go-tpm-tools, see:
	// https://github.com/google/go-tpm-tools/blob/3e063ade7f302972d7b893ca080a75efa3db5506/client/template.go#L108
	//
	// For more template options, see https://pkg.go.dev/github.com/google/go-tpm/legacy/tpm2#Public
	params := legacy.ECCParams{
		Symmetric: nil,
		CurveID:   legacy.CurveNISTP256,
		Point: legacy.ECPoint{
			XRaw: make([]byte, 32),
			YRaw: make([]byte, 32),
		},
	}
	params.Sign = &legacy.SigScheme{
		Alg:  legacy.AlgECDSA,
		Hash: legacy.AlgSHA256,
	}
	template := legacy.Public{
		Type:          legacy.AlgECC,
		NameAlg:       legacy.AlgSHA256,
		Attributes:    legacy.FlagSignerDefault,
		ECCParameters: &params,
	}

	pub := data.lak.PublicArea()
	if !pub.MatchesTemplate(template) {
		t.Errorf("local attestation key does not match template")
	}
}

func TestGetQuote(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	_, err := data.client.GetQuote(data.nonce, data.lak, data.pcrSel)
	require.NoError(err)
}

func TestGetAttestation(t *testing.T) {
	// Skip this test when running in a CI environment where the event log file is not available
	_, err := os.ReadFile("/sys/kernel/security/tpm0/binary_bios_measurements")
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		t.Skip("Skipping test: TCG Event Log not available")
	}

	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	_, err = data.client.GetAttestation(data.nonce, data.lak)
	require.NoError(err)
}

func TestReadPCRValues(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	measurements := make(map[string]string)

	err := data.client.ReadPCRValues(measurements)
	require.NoError(err)
}

func TestLoadLDevIDErrors(t *testing.T) {
	data, cleanup := setupTestData(t)
	defer cleanup()
	invalidPublic := tpm2.New2B(tpm2.TPMTPublic{
		Type:    tpm2.TPMAlgRSA,
		NameAlg: tpm2.TPMAlgSHA256,
	})
	invalidPrivate := tpm2.TPM2BPrivate{
		Buffer: []byte{0x00, 0x01, 0x02},
	}
	_, err := data.client.loadLDevIDFromBlob(invalidPublic, invalidPrivate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "loading ldevid key")
}

func TestLoadLDevIDFromBlob(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	createCmd := tpm2.Create{
		ParentHandle: *data.srk,
		InPublic:     tpm2.New2B(LDevIDTemplate),
	}
	transportTPM := transport.FromReadWriter(data.client.conn)
	createRsp, err := createCmd.Execute(transportTPM)
	require.NoError(err)

	loadedLDevID, err := data.client.loadLDevIDFromBlob(createRsp.OutPublic, createRsp.OutPrivate)
	require.NoError(err)
	require.NotNil(loadedLDevID)
	require.NotEqual(tpm2.TPMHandle(0), loadedLDevID.Handle)
	require.NotEmpty(loadedLDevID.Name)
}

func TestEnsureLDevID(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	readWriter := createTestReadWriter(t)
	data.client.rw = readWriter
	blobPath := "ldevid.yaml"

	ldevid1, err := data.client.ensureLDevID(blobPath)
	require.NoError(err)
	require.NotNil(ldevid1)
	err = data.client.flushContextForHandle(ldevid1.Handle)
	require.NoError(err)
	ldevid2, err := data.client.ensureLDevID(blobPath)
	require.NoError(err)
	require.NotNil(ldevid2)

	require.Equal(ldevid1.Name, ldevid2.Name)
}

func TestFlushContextForHandle(t *testing.T) {
	data, cleanup := setupTestData(t)
	defer cleanup()

	// Create a transient LDevID for testing
	ldevid, err := data.client.createLDevID()
	require.NoError(t, err)
	require.NotNil(t, ldevid)

	tests := []struct {
		name        string
		handle      tpm2.TPMHandle
		shouldError bool
		description string
	}{
		{
			name:        "flush transient handle",
			handle:      ldevid.Handle,
			shouldError: false,
			description: "transient handle should flush successfully",
		},
		{
			name:        "flush persistent handle (no-op)",
			handle:      persistentHandleMin,
			shouldError: false,
			description: "persistent handle should be a no-op and not error",
		},
		{
			name:        "flush another persistent handle",
			handle:      persistentHandleMin + 1,
			shouldError: false,
			description: "another persistent handle should also be a no-op",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := data.client.flushContextForHandle(tt.handle)
			if tt.shouldError {
				require.Error(t, err, tt.description)
			} else {
				require.NoError(t, err, tt.description)
			}
		})
	}
}

func TestEnsureLDevIDEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T) (*Client, *tpm2.NamedHandle, string)
		expectError   bool
		errorContains string
	}{
		{
			name: "empty blob path",
			setupFunc: func(t *testing.T) (*Client, *tpm2.NamedHandle, string) {
				data, cleanup := setupTestData(t)
				t.Cleanup(cleanup)

				readWriter := createTestReadWriter(t)
				data.client.rw = readWriter

				return data.client, data.srk, ""
			},
			expectError:   true,
			errorContains: "blob path cannot be empty",
		},
		{
			name: "corrupted file recovery",
			setupFunc: func(t *testing.T) (*Client, *tpm2.NamedHandle, string) {
				data, cleanup := setupTestData(t)
				t.Cleanup(cleanup)

				readWriter := createTestReadWriter(t)
				data.client.rw = readWriter

				// Write corrupted file first
				path := "corrupted_recovery.yaml"
				corruptedContent := "invalid yaml content: [unclosed bracket"
				err := readWriter.WriteFile(path, []byte(corruptedContent), 0600)
				require.NoError(t, err)

				return data.client, data.srk, path
			},
			expectError:   true,
			errorContains: "loading blob from file",
		},
		{
			name: "successful recovery from missing file",
			setupFunc: func(t *testing.T) (*Client, *tpm2.NamedHandle, string) {
				data, cleanup := setupTestData(t)
				t.Cleanup(cleanup)

				readWriter := createTestReadWriter(t)
				data.client.rw = readWriter

				// Use a path that doesn't exist - should create new key
				return data.client, data.srk, "new_key.yaml"
			},
			expectError:   false,
			errorContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpmClient, _, path := tt.setupFunc(t)

			_, err := tpmClient.ensureLDevID(path)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					require.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCloseFlushesHandles(t *testing.T) {
	require := require.New(t)
	f, err := setupTestFixture(t)
	require.NoError(err)

	// Verify handles are set
	require.NotNil(f.client.srk)
	require.NotNil(f.client.ldevid)

	// Close should flush handles and set them to nil
	err = f.client.Close(context.Background())
	require.NoError(err)

	// Verify handles are cleared
	require.Nil(f.client.srk)
	require.Nil(f.client.ldevid)
}

func TestSaveLDevIDBlob(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T) (*Client, fileio.ReadWriter, tpm2.TPM2BPublic, tpm2.TPM2BPrivate)
		path          string
		expectError   bool
		errorContains string
	}{
		{
			name: "success case",
			setupFunc: func(t *testing.T) (*Client, fileio.ReadWriter, tpm2.TPM2BPublic, tpm2.TPM2BPrivate) {
				data, cleanup := setupTestData(t)
				t.Cleanup(cleanup)

				readWriter := createTestReadWriter(t)
				data.client.rw = readWriter

				// Create valid blob data
				createCmd := tpm2.Create{
					ParentHandle: *data.srk,
					InPublic:     tpm2.New2B(LDevIDTemplate),
				}
				transportTPM := transport.FromReadWriter(data.client.conn)
				createRsp, err := createCmd.Execute(transportTPM)
				require.NoError(t, err)

				return data.client, readWriter, createRsp.OutPublic, createRsp.OutPrivate
			},
			path:        "test_blob.yaml",
			expectError: false,
		},
		{
			name: "YAML marshaling success with nested path",
			setupFunc: func(t *testing.T) (*Client, fileio.ReadWriter, tpm2.TPM2BPublic, tpm2.TPM2BPrivate) {
				data, cleanup := setupTestData(t)
				t.Cleanup(cleanup)

				readWriter := createTestReadWriter(t)
				data.client.rw = readWriter

				// Create valid blob data
				createCmd := tpm2.Create{
					ParentHandle: *data.srk,
					InPublic:     tpm2.New2B(LDevIDTemplate),
				}
				transportTPM := transport.FromReadWriter(data.client.conn)
				createRsp, err := createCmd.Execute(transportTPM)
				require.NoError(t, err)

				return data.client, readWriter, createRsp.OutPublic, createRsp.OutPrivate
			},
			path:        "nested/test_blob.yaml",
			expectError: false,
		},
		{
			name: "empty blob data",
			setupFunc: func(t *testing.T) (*Client, fileio.ReadWriter, tpm2.TPM2BPublic, tpm2.TPM2BPrivate) {
				data, cleanup := setupTestData(t)
				t.Cleanup(cleanup)

				readWriter := createTestReadWriter(t)
				data.client.rw = readWriter

				// Create empty blob data
				emptyPublic := tpm2.TPM2BPublic{}
				emptyPrivate := tpm2.TPM2BPrivate{}

				return data.client, readWriter, emptyPublic, emptyPrivate
			},
			path:        "empty_blob.yaml",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpClient, _, public, private := tt.setupFunc(t)

			err := tmpClient.saveLDevIDBlob(public, private, tt.path)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					require.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestLoadLDevIDBlobErrors(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T) (*Client, string)
		expectError   bool
		errorContains string
	}{
		{
			name: "file not found",
			setupFunc: func(t *testing.T) (*Client, string) {
				data, cleanup := setupTestData(t)
				t.Cleanup(cleanup)

				readWriter := createTestReadWriter(t)
				data.client.rw = readWriter

				return data.client, "nonexistent_file.yaml"
			},
			expectError:   true,
			errorContains: "",
		},
		{
			name: "corrupted YAML",
			setupFunc: func(t *testing.T) (*Client, string) {
				data, cleanup := setupTestData(t)
				t.Cleanup(cleanup)

				readWriter := createTestReadWriter(t)
				data.client.rw = readWriter

				// Write corrupted YAML
				corruptedContent := "invalid yaml content: [unclosed bracket"
				path := "corrupted.yaml"
				err := readWriter.WriteFile(path, []byte(corruptedContent), 0600)
				require.NoError(t, err)

				return data.client, path
			},
			expectError:   true,
			errorContains: "unmarshaling YAML",
		},
		{
			name: "invalid blob structure",
			setupFunc: func(t *testing.T) (*Client, string) {
				data, cleanup := setupTestData(t)
				t.Cleanup(cleanup)

				readWriter := createTestReadWriter(t)
				data.client.rw = readWriter

				// Write YAML with wrong structure
				invalidYAML := `
invalid_field: "value"
another_field: 123
`
				path := "invalid_structure.yaml"
				err := readWriter.WriteFile(path, []byte(invalidYAML), 0600)
				require.NoError(t, err)

				return data.client, path
			},
			expectError:   false, // This might not error due to YAML unmarshaling into empty struct
			errorContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpmClient, path := tt.setupFunc(t)

			_, _, err := tpmClient.loadLDevIDBlob(path)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					require.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// Helper function to verify ECDSA signatures
func verifyECDSASignature(pubKey *ecdsa.PublicKey, data []byte, signature []byte) error {
	// Parse ASN.1 encoded signature
	var sig ecdsaSignature
	_, err := asn1.Unmarshal(signature, &sig)
	if err != nil {
		return fmt.Errorf("failed to parse signature: %w", err)
	}

	// Hash the data
	hash := sha256.Sum256(data)

	// Verify signature
	if !ecdsa.Verify(pubKey, hash[:], sig.R, sig.S) {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

func TestGetLDevIDPubKey(t *testing.T) {
	t.Run("successful retrieval", func(t *testing.T) {
		require := require.New(t)
		data, cleanup := setupTestData(t)
		defer cleanup()
		pubKey, err := data.client.getLDevIDPubKey()
		require.NoError(err)
		require.NotNil(pubKey)

		// Verify it's an ECDSA public key
		ecdsaPubKey, ok := pubKey.(*ecdsa.PublicKey)
		require.True(ok, "public key should be *ecdsa.PublicKey")

		// Verify it's P-256 curve
		require.Equal("P-256", ecdsaPubKey.Curve.Params().Name)

		// Verify coordinates are valid
		require.True(ecdsaPubKey.X.Sign() > 0, "X coordinate should be positive")
		require.True(ecdsaPubKey.Y.Sign() > 0, "Y coordinate should be positive")
	})

	t.Run("error when ldevid not initialized", func(t *testing.T) {
		require := require.New(t)
		// Create TPM without auto-generation to test error case
		simulator, err := simulator.Get()
		require.NoError(err)
		tpm := &Client{
			conn: simulator,
		}
		defer tpm.Close(context.Background())

		_, err = tpm.getLDevIDPubKey()
		require.Error(err)
		require.Contains(err.Error(), "ldevid not initialized")
	})
}

func TestSign(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	// TPM Sign expects a 32-byte hash (SHA-256)
	testHash := sha256.Sum256([]byte("test data to sign"))

	t.Run("successful signing", func(t *testing.T) {
		signature, err := data.client.Sign(nil, testHash[:], nil)
		require.NoError(err)
		require.NotEmpty(signature)

		// Verify signature is ASN.1 encoded
		var sig ecdsaSignature
		_, err = asn1.Unmarshal(signature, &sig)
		require.NoError(err)
		require.NotNil(sig.R)
		require.NotNil(sig.S)
		require.True(sig.R.Sign() > 0, "R should be positive")
		require.True(sig.S.Sign() > 0, "S should be positive")
	})

	t.Run("signing different hash inputs", func(t *testing.T) {
		testCases := []struct {
			name     string
			origData []byte
		}{
			{"empty data hash", []byte{}},
			{"small data hash", []byte("hello")},
			{"medium data hash", make([]byte, 256)},
			{"large data hash", make([]byte, 1024)},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				// Hash the data first since TPM expects a digest
				hash := sha256.Sum256(tc.origData)
				signature, err := data.client.Sign(rand.Reader, hash[:], crypto.SHA256)
				require.NoError(err)
				require.NotEmpty(signature)
			})
		}
	})

	t.Run("rand parameter is ignored", func(t *testing.T) {
		// Sign with nil rand
		sig1, err := data.client.Sign(nil, testHash[:], nil)
		require.NoError(err)

		// Sign with real rand - should still work (rand is ignored)
		sig2, err := data.client.Sign(rand.Reader, testHash[:], nil)
		require.NoError(err)

		// Both signatures should be valid (though different due to randomness)
		require.NotEmpty(sig1)
		require.NotEmpty(sig2)
	})
}

func TestSignAndVerify(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	t.Run("sign and verify integration", func(t *testing.T) {
		testPayloads := [][]byte{
			[]byte("test message 1"),
			[]byte("another test message"),
			[]byte(""),
			make([]byte, 100), // filled with zeros
		}

		// Get public key
		pubKey, err := data.client.getLDevIDPubKey()
		require.NoError(err)
		ecdsaPubKey := pubKey.(*ecdsa.PublicKey)

		for i, payload := range testPayloads {
			t.Run(fmt.Sprintf("payload_%d", i), func(t *testing.T) {
				// Hash the payload since TPM expects a digest
				hash := sha256.Sum256(payload)

				// Sign the hash
				signature, err := data.client.Sign(rand.Reader, hash[:], crypto.SHA256)
				require.NoError(err)

				// Verify the signature against the original payload
				err = verifyECDSASignature(ecdsaPubKey, payload, signature)
				require.NoError(err, "signature verification should succeed")
			})
		}
	})

	t.Run("verification fails with wrong data", func(t *testing.T) {
		originalData := []byte("original data")
		wrongData := []byte("wrong data")

		// Get public key
		pubKey, err := data.client.getLDevIDPubKey()
		require.NoError(err)
		ecdsaPubKey := pubKey.(*ecdsa.PublicKey)

		// Hash and sign original data
		originalHash := sha256.Sum256(originalData)
		signature, err := data.client.Sign(rand.Reader, originalHash[:], crypto.SHA256)
		require.NoError(err)

		// Try to verify with wrong data - should fail
		err = verifyECDSASignature(ecdsaPubKey, wrongData, signature)
		require.Error(err, "verification should fail with wrong data")
	})
}

func TestReadEndorsementKeyCert(t *testing.T) {
	tests := []struct {
		name               string
		setupTPM           func(t *testing.T) *Client
		expectError        bool
		expectedErrContent string
		expectedCertType   string
	}{
		{
			name: "no connection available",
			setupTPM: func(t *testing.T) *Client {
				return &Client{conn: nil}
			},
			expectError:        true,
			expectedErrContent: "no connection available",
		},
		{
			name: "RSA EK certificate found",
			setupTPM: func(t *testing.T) *Client {
				t.Skip("Skipping endorsement key certificate test: TPM simulator does not provide EK certificates")
				return nil
			},
			expectError:      false,
			expectedCertType: "RSA EK Certificate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			tpm := tc.setupTPM(t)
			defer func() {
				_ = tpm.Close(context.Background())
			}()

			certData, err := tpm.EndorsementKeyCert()

			if tc.expectError {
				require.Error(err)
				require.Contains(err.Error(), tc.expectedErrContent)
				return
			}

			require.NoError(err)
			require.NotEmpty(certData)

			// Verify it's a valid X.509 certificate with expected type
			cert, err := x509.ParseCertificate(certData)
			require.NoError(err)
			require.Contains(cert.Subject.CommonName, tc.expectedCertType)
		})
	}
}

func TestCryptoSignerInterface(t *testing.T) {
	data, cleanup := setupTestData(t)
	defer cleanup()

	t.Run("TPM implements crypto.Signer", func(t *testing.T) {
		require := require.New(t)
		// Verify TPM implements crypto.Signer interface
		var signer crypto.Signer = data.client
		require.NotNil(signer)

		// Test Public() method
		pubKey := signer.Public()
		require.NotNil(pubKey)

		ecdsaPubKey, ok := pubKey.(*ecdsa.PublicKey)
		require.True(ok, "public key should be *ecdsa.PublicKey")
		require.Equal("P-256", ecdsaPubKey.Curve.Params().Name)
	})

	t.Run("signer-only interface test with full sign and verify", func(t *testing.T) {
		require := require.New(t)
		// This test uses TPM only as crypto.Signer interface
		testSignerInterface := func(signer crypto.Signer) error {
			testData := []byte("interface test data")
			// Hash the data since TPM expects a digest
			testHash := sha256.Sum256(testData)

			// Sign using only crypto.Signer interface
			signature, err := signer.Sign(rand.Reader, testHash[:], crypto.SHA256)
			if err != nil {
				return fmt.Errorf("signing failed: %w", err)
			}

			// Get public key using only crypto.Signer interface
			pubKey := signer.Public()
			ecdsaPubKey, ok := pubKey.(*ecdsa.PublicKey)
			if !ok {
				return fmt.Errorf("expected *ecdsa.PublicKey, got %T", pubKey)
			}

			// Verify signature against original data (verifyECDSASignature will hash it)
			return verifyECDSASignature(ecdsaPubKey, testData, signature)
		}

		// Test with TPM as crypto.Signer
		var signer crypto.Signer = data.client
		err := testSignerInterface(signer)
		require.NoError(err, "signer interface test should pass")
	})

	t.Run("GetSigner returns self", func(t *testing.T) {
		require := require.New(t)
		signer := data.client.GetSigner()
		require.Equal(data.client, signer, "GetSigner should return the TPM instance itself")
	})
}

func TestEndorsementKeyPublic(t *testing.T) {
	tests := []struct {
		name               string
		setupTPM           func(t *testing.T) *Client
		expectError        bool
		expectedErrContent string
		validateResult     func(t *testing.T, data []byte)
	}{
		{
			name: "no connection available",
			setupTPM: func(t *testing.T) *Client {
				return &Client{conn: nil}
			},
			expectError:        true,
			expectedErrContent: "no connection available",
		},
		{
			name: "successful public key retrieval with simulator",
			setupTPM: func(t *testing.T) *Client {
				require := require.New(t)
				tpm, err := openTPMSimulator(t)
				require.NoError(err)
				return tpm
			},
			expectError: false,
			validateResult: func(t *testing.T, data []byte) {
				require := require.New(t)
				require.NotEmpty(data, "encoded public key data should not be empty")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			tpm := tc.setupTPM(t)
			defer func() {
				_ = tpm.Close(context.Background())
			}()

			publicKeyData, err := tpm.EndorsementKeyPublic()

			if tc.expectError {
				require.Error(err)
				require.Contains(err.Error(), tc.expectedErrContent)
				require.Empty(publicKeyData)
				return
			}

			require.NoError(err)
			if tc.validateResult != nil {
				tc.validateResult(t, publicKeyData)
			}
		})
	}
}

func TestTPMDeviceExists(t *testing.T) {
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	// Create mock resource manager file using ReadWriter
	err := rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
	require.NoError(t, err)

	tests := []struct {
		name     string
		device   TPM
		expected bool
	}{
		{
			name: "device exists at resource manager path",
			device: TPM{
				path:            "/dev/tpm0",
				resourceMgrPath: "/dev/tpmrm0",
				rw:              rw,
			},
			expected: true,
		},
		{
			name: "device does not exist at resource manager path",
			device: TPM{
				path:            "/dev/tpm0",
				resourceMgrPath: "/dev/nonexistent",
				rw:              rw,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.device.Exists()
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestDiscoverTPMDevices(t *testing.T) {
	// Create a temporary directory structure to simulate /sys/class/tpm
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	// Create mock TPM directories using ReadWriter
	tpmDirs := []string{"tpm0", "tpm1", "tpm2"}
	for _, dir := range tpmDirs {
		err := rw.MkdirAll("/sys/class/tpm/"+dir, 0755)
		require.NoError(t, err)
	}

	// Create a non-TPM directory that should be ignored
	err := rw.MkdirAll("/sys/class/tpm/other", 0755)
	require.NoError(t, err)

	// Create a file that should be ignored
	err = rw.WriteFile("/sys/class/tpm/file.txt", []byte{}, 0600)
	require.NoError(t, err)

	devices, err := discover(rw)
	require.NoError(t, err)
	require.Len(t, devices, 3)

	expectedDevices := []TPM{
		{
			index:           "0",
			path:            "/dev/tpm0",
			resourceMgrPath: "/dev/tpmrm0",
			versionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
			sysfsPath:       "/sys/class/tpm/tpm0",
		},
		{
			index:           "1",
			path:            "/dev/tpm1",
			resourceMgrPath: "/dev/tpmrm1",
			versionPath:     "/sys/class/tpm/tpm1/tpm_version_major",
			sysfsPath:       "/sys/class/tpm/tpm1",
		},
		{
			index:           "2",
			path:            "/dev/tpm2",
			resourceMgrPath: "/dev/tpmrm2",
			versionPath:     "/sys/class/tpm/tpm2/tpm_version_major",
			sysfsPath:       "/sys/class/tpm/tpm2",
		},
	}

	for i, expected := range expectedDevices {
		require.Equal(t, expected.index, devices[i].index)
		require.Equal(t, expected.path, devices[i].path)
		require.Equal(t, expected.resourceMgrPath, devices[i].resourceMgrPath)
		require.Equal(t, expected.versionPath, devices[i].versionPath)
		require.Equal(t, expected.sysfsPath, devices[i].sysfsPath)
		require.Equal(t, rw, devices[i].rw)
	}
}

func TestDiscoverError(t *testing.T) {
	// Test with a ReadWriter that points to a non-existent directory
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	// Try to discover TPM devices from a directory that doesn't exist
	devices, err := discover(rw)

	// The ReadWriter with test root dir will return empty results instead of error
	// when the directory doesn't exist, so we should get an empty slice
	require.NoError(t, err)
	require.Empty(t, devices)
}

func TestTPMDeviceValidateVersion2(t *testing.T) {
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

	// Create mock resource manager and version files using ReadWriter
	err := rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
	require.NoError(t, err)
	err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0600)
	require.NoError(t, err)

	tests := []struct {
		name        string
		device      TPM
		expectError bool
	}{
		{
			name: "valid TPM 2.0 device",
			device: TPM{
				path:            "/dev/tpm0",
				resourceMgrPath: "/dev/tpmrm0",
				versionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
				rw:              rw,
			},
			expectError: false,
		},
		{
			name: "resource manager does not exist",
			device: TPM{
				path:            "/dev/tpm0",
				resourceMgrPath: "/dev/nonexistent",
				versionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
				rw:              rw,
			},
			expectError: true,
		},
		{
			name: "version file does not exist",
			device: TPM{
				path:            "/dev/tpm0",
				resourceMgrPath: "/dev/tpmrm0",
				versionPath:     "/sys/class/tpm/tpm0/nonexistent",
				rw:              rw,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.device.ValidateVersion2()
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestResolveDefault(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(rw fileio.ReadWriter)
		expectError    bool
		expectedDevice *TPM
	}{
		{
			name: "returns first valid TPM device when available",
			setup: func(rw fileio.ReadWriter) {
				// Create multiple TPM devices
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)
				err = rw.MkdirAll("/sys/class/tpm/tpm1", 0755)
				require.NoError(t, err)

				// Create resource manager files for both
				err = rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/dev/tpmrm1", []byte{}, 0600)
				require.NoError(t, err)

				// Create version files for both (TPM 2.0)
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/sys/class/tpm/tpm1/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
			},
			expectError: false,
			expectedDevice: &TPM{
				index:           "0",
				path:            "/dev/tpm0",
				resourceMgrPath: "/dev/tpmrm0",
				versionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
				sysfsPath:       "/sys/class/tpm/tpm0",
			},
		},
		{
			name: "returns error when no TPM devices exist",
			setup: func(rw fileio.ReadWriter) {
				// Don't create any TPM devices
			},
			expectError: true,
		},
		{
			name: "returns error when TPM devices exist but are not version 2.0",
			setup: func(rw fileio.ReadWriter) {
				// Create TPM device directory
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)

				// Create resource manager file
				err = rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
				require.NoError(t, err)

				// Create version file with non-2.0 version
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("1"), 0600)
				require.NoError(t, err)
			},
			expectError: true,
		},
		{
			name: "returns error when resource manager files don't exist",
			setup: func(rw fileio.ReadWriter) {
				// Create TPM device directory
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)

				// Create version file but not resource manager files
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

			tt.setup(rw)

			device, err := resolveDefault(rw, log.NewPrefixLogger("test"))

			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, device)
			} else {
				require.NoError(t, err)
				require.NotNil(t, device)
				require.Equal(t, tt.expectedDevice.index, device.index)
				require.Equal(t, tt.expectedDevice.path, device.path)
				require.Equal(t, tt.expectedDevice.resourceMgrPath, device.resourceMgrPath)
				require.Equal(t, tt.expectedDevice.versionPath, device.versionPath)
				require.Equal(t, tt.expectedDevice.sysfsPath, device.sysfsPath)
				require.Equal(t, rw, device.rw)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(rw fileio.ReadWriter)
		path        string
		expectError bool
		expectedTPM *TPM
	}{
		{
			name: "resolves tpm by resource manager path",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)
				err = rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
			},
			path:        "/dev/tpmrm0",
			expectError: false,
			expectedTPM: &TPM{
				index:           "0",
				path:            "/dev/tpm0",
				resourceMgrPath: "/dev/tpmrm0",
				versionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
				sysfsPath:       "/sys/class/tpm/tpm0",
			},
		},
		{
			name: "resolves tpm by direct path",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll("/sys/class/tpm/tpm1", 0755)
				require.NoError(t, err)
				err = rw.WriteFile("/dev/tpmrm1", []byte{}, 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/sys/class/tpm/tpm1/tpm_version_major", []byte("2"), 0600)
				require.NoError(t, err)
			},
			path:        "/dev/tpm1",
			expectError: false,
			expectedTPM: &TPM{
				index:           "1",
				path:            "/dev/tpm1",
				resourceMgrPath: "/dev/tpmrm1",
				versionPath:     "/sys/class/tpm/tpm1/tpm_version_major",
				sysfsPath:       "/sys/class/tpm/tpm1",
			},
		},
		{
			name: "returns error for non-existent device",
			setup: func(rw fileio.ReadWriter) {
			},
			path:        "/dev/tpmrm99",
			expectError: true,
		},
		{
			name: "returns error for non-TPM2 device",
			setup: func(rw fileio.ReadWriter) {
				err := rw.MkdirAll("/sys/class/tpm/tpm0", 0755)
				require.NoError(t, err)
				err = rw.WriteFile("/dev/tpmrm0", []byte{}, 0600)
				require.NoError(t, err)
				err = rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("1"), 0600)
				require.NoError(t, err)
			},
			path:        "/dev/tpmrm0",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			rw := fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))

			tt.setup(rw)

			device, err := resolve(rw, tt.path)

			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, device)
			} else {
				require.NoError(t, err)
				require.NotNil(t, device)
				require.Equal(t, tt.expectedTPM.index, device.index)
				require.Equal(t, tt.expectedTPM.path, device.path)
				require.Equal(t, tt.expectedTPM.resourceMgrPath, device.resourceMgrPath)
				require.Equal(t, tt.expectedTPM.versionPath, device.versionPath)
				require.Equal(t, tt.expectedTPM.sysfsPath, device.sysfsPath)
				require.Equal(t, rw, device.rw)
			}
		})
	}
}
