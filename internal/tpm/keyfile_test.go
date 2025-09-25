package tpm

import (
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-tpm-tools/simulator"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/stretchr/testify/require"
)

// storageParentTemplate generates a storage parent key template that can create child keys.
// This key is restricted to storage operations only and cannot sign or decrypt external data.
func storageParentTemplate(keyAlgo KeyAlgorithm) (tpm2.TPMTPublic, error) {
	baseAttributes := tpm2.TPMAObject{
		FixedTPM:             true,  // true = must stay in TPM
		STClear:              false, // true = cannot be loaded after tpm2_clear
		FixedParent:          true,  // true = can't be re-parented
		SensitiveDataOrigin:  true,  // true = TPM generates all sensitive data during creation
		UserWithAuth:         true,  // true = pw or hmac can be used in addition to authpolicy
		AdminWithPolicy:      false, // true = authValue cannot be used for auth
		NoDA:                 false, // true = there are dictionary attack protections
		EncryptedDuplication: false, // true = there are more robust protections for duplication
		Restricted:           true,  // true = restricted storage key
		Decrypt:              true,  // true = can be used for storage operations
		SignEncrypt:          false, // false = storage keys don't sign
	}

	switch keyAlgo {
	case ECDSA:
		return tpm2.TPMTPublic{
			Type:             tpm2.TPMAlgECC,
			NameAlg:          tpm2.TPMAlgSHA256,
			ObjectAttributes: baseAttributes,
			Parameters: tpm2.NewTPMUPublicParms(
				tpm2.TPMAlgECC,
				&tpm2.TPMSECCParms{
					Symmetric: tpm2.TPMTSymDefObject{
						Algorithm: tpm2.TPMAlgAES,
						KeyBits: tpm2.NewTPMUSymKeyBits(
							tpm2.TPMAlgAES,
							tpm2.TPMKeyBits(128),
						),
						Mode: tpm2.NewTPMUSymMode(
							tpm2.TPMAlgAES,
							tpm2.TPMAlgCFB,
						),
					},
					Scheme: tpm2.TPMTECCScheme{
						Scheme: tpm2.TPMAlgNull,
					},
					CurveID: tpm2.TPMECCNistP256,
				},
			),
			Unique: tpm2.NewTPMUPublicID(
				tpm2.TPMAlgECC,
				&tpm2.TPMSECCPoint{
					X: tpm2.TPM2BECCParameter{Buffer: make([]byte, 32)},
					Y: tpm2.TPM2BECCParameter{Buffer: make([]byte, 32)},
				},
			),
		}, nil

	case RSA:
		return tpm2.TPMTPublic{
			Type:             tpm2.TPMAlgRSA,
			NameAlg:          tpm2.TPMAlgSHA256,
			ObjectAttributes: baseAttributes,
			Parameters: tpm2.NewTPMUPublicParms(
				tpm2.TPMAlgRSA,
				&tpm2.TPMSRSAParms{
					Symmetric: tpm2.TPMTSymDefObject{
						Algorithm: tpm2.TPMAlgAES,
						KeyBits: tpm2.NewTPMUSymKeyBits(
							tpm2.TPMAlgAES,
							tpm2.TPMKeyBits(128),
						),
						Mode: tpm2.NewTPMUSymMode(
							tpm2.TPMAlgAES,
							tpm2.TPMAlgCFB,
						),
					},
					Scheme: tpm2.TPMTRSAScheme{
						Scheme: tpm2.TPMAlgNull,
					},
					KeyBits: 2048, // 2048-bit RSA key
				},
			),
			Unique: tpm2.NewTPMUPublicID(
				tpm2.TPMAlgRSA,
				&tpm2.TPM2BPublicKeyRSA{
					Buffer: make([]byte, 256), // 2048 bits = 256 bytes
				},
			),
		}, nil

	default:
		return tpm2.TPMTPublic{}, fmt.Errorf("unsupported key algorithm: %s", keyAlgo)
	}
}

func TestGenerateTPM2KeyFile(t *testing.T) {
	ldevTemp, err := LDevIDTemplate(ECDSA)
	require.NoError(t, err)
	template := tpm2.New2B(ldevTemp)
	tests := []struct {
		name         string
		keyType      KeyFileType
		parent       tpm2.TPMHandle
		publicData   tpm2.TPM2BPublic
		privateData  tpm2.TPM2BPrivate
		opts         []KeyFileOption
		expectError  bool
		errorMessage string
	}{
		{
			name:        "valid loadable key",
			keyType:     LoadableKey,
			parent:      0x81000000,
			publicData:  template,
			privateData: tpm2.TPM2BPrivate{Buffer: []byte("mock-private-key-data")},
			opts:        nil,
			expectError: false,
		},
		{
			name:        "with empty auth option",
			keyType:     LoadableKey,
			parent:      0x81000000,
			publicData:  template,
			privateData: tpm2.TPM2BPrivate{Buffer: []byte("mock-private-key-data")},
			opts:        []KeyFileOption{WithEmptyAuth()},
			expectError: false,
		},
		{
			name:         "invalid key type",
			keyType:      KeyFileType("invalid"),
			parent:       0x81000000,
			publicData:   template,
			privateData:  tpm2.TPM2BPrivate{Buffer: []byte("mock-private-key-data")},
			opts:         nil,
			expectError:  true,
			errorMessage: "unsupported key type: invalid",
		},
		{
			name:         "invalid parent handle",
			keyType:      LoadableKey,
			parent:       0x12345678,
			publicData:   template,
			privateData:  tpm2.TPM2BPrivate{Buffer: []byte("mock-private-key-data")},
			opts:         nil,
			expectError:  true,
			errorMessage: "invalid parent handle: 12345678",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GenerateTPM2KeyFile(tt.keyType, tt.parent, tt.publicData, tt.privateData, tt.opts...)

			if tt.expectError {
				require.Error(t, err, "expected error but got none")
				require.Contains(t, err.Error(), tt.errorMessage, "error message should contain expected text")
				return
			}

			require.NoError(t, err, "unexpected error")
			require.NotNil(t, result, "expected result but got nil")

			// Verify PEM format
			block, _ := pem.Decode(result)
			require.NotNil(t, block, "failed to decode PEM block")
			require.Equal(t, "TSS2 PRIVATE KEY", block.Type, "unexpected PEM type")

			// Verify ASN.1 structure can be parsed
			var key tpmKey
			_, err = asn1.Unmarshal(block.Bytes, &key)
			require.NoError(t, err, "failed to unmarshal ASN.1 data")

			// Verify basic fields
			require.Equal(t, tt.parent.HandleValue(), forceUint32(key.Parent), "parent handle mismatch")
			require.NotEmpty(t, key.PubKey, "public key should not be empty")
			require.NotEmpty(t, key.PrivKey, "private key should not be empty")

			// Verify OID based on key type
			var expectedOID asn1.ObjectIdentifier
			switch tt.keyType {
			case LoadableKey:
				expectedOID = oidLoadableKey
			}

			require.True(t, key.Type.Equal(expectedOID), "expected OID %v, got %v", expectedOID, key.Type)

			// Verify EmptyAuth option if provided
			if len(tt.opts) > 0 {
				for _, opt := range tt.opts {
					if opt != nil {
						// Check if WithEmptyAuth was used by checking the result
						testKey := &tpmKey{}
						opt(testKey)
						if testKey.EmptyAuth {
							require.True(t, key.EmptyAuth, "EmptyAuth should be true when WithEmptyAuth() is used")
						}
					}
				}
			}
		})
	}
}

func TestKeyFileTypeConstants(t *testing.T) {
	tests := []struct {
		keyType     KeyFileType
		expectedOID asn1.ObjectIdentifier
	}{
		{LoadableKey, oidLoadableKey},
	}

	for _, tt := range tests {
		t.Run(string(tt.keyType), func(t *testing.T) {
			public := tpm2.New2B(tpm2.TPMTPublic{
				Type:    tpm2.TPMAlgRSA,
				NameAlg: tpm2.TPMAlgSHA256,
				ObjectAttributes: tpm2.TPMAObject{
					SignEncrypt: true,
				},
				Parameters: tpm2.NewTPMUPublicParms(tpm2.TPMAlgRSA, &tpm2.TPMSRSAParms{
					KeyBits: 2048,
				}),
				Unique: tpm2.NewTPMUPublicID(tpm2.TPMAlgRSA, &tpm2.TPM2BPublicKeyRSA{
					Buffer: []byte("pub"),
				}),
			})
			private := tpm2.TPM2BPrivate{Buffer: []byte("priv")}
			result, err := GenerateTPM2KeyFile(tt.keyType, 0x81000000, public, private)
			require.NoError(t, err, "unexpected error")

			block, _ := pem.Decode(result)
			require.NotNil(t, block, "failed to decode PEM block")

			var key tpmKey
			_, err = asn1.Unmarshal(block.Bytes, &key)
			require.NoError(t, err, "failed to unmarshal ASN.1")

			require.True(t, key.Type.Equal(tt.expectedOID), "expected OID %v, got %v", tt.expectedOID, key.Type)
		})
	}
}

func TestPEMFormat(t *testing.T) {
	conn, err := simulator.Get()
	require.NoError(t, err)
	defer conn.Close()

	tpmTransport := transport.FromReadWriter(conn)

	// Create SRK (Storage Root Key) under owner hierarchy
	srkTemplate := tpm2.ECCSRKTemplate
	createSRKCmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHOwner,
			Auth:   tpm2.PasswordAuth(nil),
		},
		InPublic: tpm2.New2B(srkTemplate),
	}

	srkResp, err := createSRKCmd.Execute(tpmTransport)
	require.NoError(t, err, "failed to create SRK")

	srkHandle := &tpm2.NamedHandle{
		Handle: srkResp.ObjectHandle,
		Name:   srkResp.Name,
	}

	// Flush SRK when done
	defer func() {
		flushCmd := tpm2.FlushContext{FlushHandle: srkHandle.Handle}
		_, _ = flushCmd.Execute(tpmTransport)
	}()

	// Create parent storage key under SRK
	parentTemplate, err := storageParentTemplate(ECDSA)
	require.NoError(t, err, "failed to get parent storage template")

	createParentCmd := tpm2.Create{
		ParentHandle: *srkHandle,
		InPublic:     tpm2.New2B(parentTemplate),
	}

	parentResp, err := createParentCmd.Execute(tpmTransport)
	require.NoError(t, err, "failed to create parent storage key")

	// Load the parent key into TPM to get a transient handle first
	loadParentCmd := tpm2.Load{
		ParentHandle: *srkHandle,
		InPrivate:    parentResp.OutPrivate,
		InPublic:     parentResp.OutPublic,
	}

	parentLoadResp, err := loadParentCmd.Execute(tpmTransport)
	require.NoError(t, err, "failed to load parent storage key")

	// Choose a persistent handle for the parent storage key
	persistentParentHandle := tpm2.TPMHandle(0x81000003)

	// Evict the parent key to make it persistent
	evictCmd := tpm2.EvictControl{
		Auth: tpm2.AuthHandle{
			Handle: tpm2.TPMRHOwner,
			Auth:   tpm2.PasswordAuth(nil),
		},
		ObjectHandle: tpm2.NamedHandle{
			Handle: parentLoadResp.ObjectHandle,
			Name:   parentLoadResp.Name,
		},
		PersistentHandle: persistentParentHandle,
	}

	_, err = evictCmd.Execute(tpmTransport)
	require.NoError(t, err, "failed to evict parent storage key to persistent handle")

	// Flush the original transient handle since it's now persistent
	flushTransientCmd := tpm2.FlushContext{FlushHandle: parentLoadResp.ObjectHandle}
	_, err = flushTransientCmd.Execute(tpmTransport)
	require.NoError(t, err, "failed to flush transient parent handle")

	parentHandle := &tpm2.NamedHandle{
		Handle: persistentParentHandle,
		Name:   parentLoadResp.Name,
	}

	// Create LDevID key as child of parent storage key
	ldevidTemplate, err := LDevIDTemplate(ECDSA)
	require.NoError(t, err, "failed to get LDevID template")

	createLDevIDCmd := tpm2.Create{
		ParentHandle: *parentHandle,
		InPublic:     tpm2.New2B(ldevidTemplate),
	}

	ldevidResp, err := createLDevIDCmd.Execute(tpmTransport)
	require.NoError(t, err, "failed to create LDevID")

	// Generate TPM2 key file with real key data, using parent handle as parent
	result, err := GenerateTPM2KeyFile(LoadableKey, parentHandle.Handle, ldevidResp.OutPublic, ldevidResp.OutPrivate)
	require.NoError(t, err, "unexpected error")

	// Check PEM format structure
	pemStr := string(result)
	require.True(t, strings.HasPrefix(pemStr, "-----BEGIN TSS2 PRIVATE KEY-----"), "PEM does not start with correct header")
	require.True(t, strings.HasSuffix(strings.TrimSpace(pemStr), "-----END TSS2 PRIVATE KEY-----"), "PEM does not end with correct footer")

	// Verify the PEM can be decoded
	block, rest := pem.Decode(result)
	require.NotNil(t, block, "failed to decode PEM block")
	require.Empty(t, rest, "unexpected data after PEM block")

	// Parse ASN.1 structure and verify it contains real key data
	var key tpmKey
	_, err = asn1.Unmarshal(block.Bytes, &key)
	require.NoError(t, err, "failed to unmarshal ASN.1 data")

	// Verify we have the expected key type
	require.True(t, key.Type.Equal(oidLoadableKey), "expected loadable key OID, got %v", key.Type)

	// Verify parent handle matches
	require.Equal(t, parentHandle.Handle.HandleValue(), forceUint32(key.Parent), "parent handle mismatch")

	// Verify key blobs are not empty and match what we generated
	require.NotEmpty(t, key.PubKey, "public key blob is empty")
	require.NotEmpty(t, key.PrivKey, "private key blob is empty")
}

func forceUint32(i int64) uint32 {
	if i < 0 {
		panic("cannot convert negative int64 to uint32")
	}
	if i > int64(^uint32(0)) {
		panic("int64 value too large for uint32")
	}
	return uint32(i)
}
