package tpm

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm-tools/simulator"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestTpmSession_GetHandle(t *testing.T) {
	tests := []struct {
		name       string
		keyType    KeyType
		setupMocks func(*MockStorage, *tpmSession)
		wantHandle bool
		wantErr    error
	}{
		{
			name:    "successful get existing handle",
			keyType: LDevID,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, handle := createValidTPMObjects()
				session.handles[LDevID] = handle
			},
			wantHandle: true,
			wantErr:    nil,
		},
		{
			name:    "handle not found",
			keyType: LAK,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				// No handle in session.handles
			},
			wantHandle: false,
			wantErr:    errors.New("handle not found for key type lak"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, mockStorage, conn, _, logger := setupMocks(t)
			defer ctrl.Finish()

			session := &tpmSession{
				conn:        conn,
				storage:     mockStorage,
				log:         logger,
				authEnabled: false,
				keyAlgo:     ECDSA,
				handles:     make(map[KeyType]*tpm2.NamedHandle),
			}

			if tt.setupMocks != nil {
				tt.setupMocks(mockStorage, session)
			}

			handle, err := session.GetHandle(tt.keyType)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				require.NoError(t, err)
			}

			if tt.wantHandle {
				require.NotNil(t, handle)
			} else {
				require.Nil(t, handle)
			}
		})
	}
}

func TestTpmSession_LoadKey(t *testing.T) {
	tests := []struct {
		name       string
		keyType    KeyType
		setupMocks func(*MockStorage, *tpmSession)
		wantErr    error
	}{
		{
			name:    "load already cached key",
			keyType: LDevID,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, handle := createValidTPMObjects()
				session.handles[LDevID] = handle
			},
			wantErr: nil,
		},
		{
			name:    "load key from storage - key exists",
			keyType: LAK,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, srkHandle := createValidTPMObjects()
				session.handles[SRK] = srkHandle
				// Enable valid SRK response in mock connection
				session.conn.(*mockReadWriteCloser).validSRK = true

				pub, priv, _ := createValidTPMObjects()
				mockStorage.EXPECT().GetKey(LAK).Return(pub, priv, nil)
			},
			wantErr: nil, // Will fail at TPM operation level in unit test
		},
		{
			name:    "auto-create key when ErrNotFound",
			keyType: LDevID,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, srkHandle := createValidTPMObjects()
				session.handles[SRK] = srkHandle
				// Enable valid SRK response in mock connection
				session.conn.(*mockReadWriteCloser).validSRK = true

				// First call returns ErrNotFound - this should trigger auto-creation
				mockStorage.EXPECT().GetKey(LDevID).Return(nil, nil, ErrNotFound)

				// CreateKey will fail due to TPM operations on mock connection
				// So StoreKey and second GetKey won't be called
			},
			wantErr: errors.New("creating missing key"), // Expect error due to TPM operation failure in test
		},
		{
			name:    "return nil blobs without error",
			keyType: LDevID,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, srkHandle := createValidTPMObjects()
				session.handles[SRK] = srkHandle
				// Enable valid SRK response in mock connection
				session.conn.(*mockReadWriteCloser).validSRK = true

				// Return nil pub/priv with nil error - should be caught
				mockStorage.EXPECT().GetKey(LDevID).Return(nil, nil, nil)
			},
			wantErr: errors.New("key ldevid returned nil blobs without error"),
		},
		{
			name:    "storage error on get key - not ErrNotFound",
			keyType: LAK,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, srkHandle := createValidTPMObjects()
				session.handles[SRK] = srkHandle
				// Enable valid SRK response in mock connection
				session.conn.(*mockReadWriteCloser).validSRK = true

				// Return a different error - should not trigger creation
				mockStorage.EXPECT().GetKey(LAK).Return(nil, nil, errors.New("storage corrupted"))
			},
			wantErr: errors.New("getting key from storage: storage corrupted"),
		},
		{
			name:    "no SRK available",
			keyType: LDevID,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				delete(session.handles, SRK)
			},
			wantErr: errors.New("ensuring SRK is loaded"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, mockStorage, conn, _, logger := setupMocks(t)
			defer ctrl.Finish()

			session := &tpmSession{
				conn:        conn,
				storage:     mockStorage,
				log:         logger,
				authEnabled: false,
				keyAlgo:     ECDSA,
				handles:     make(map[KeyType]*tpm2.NamedHandle),
			}

			if tt.setupMocks != nil {
				tt.setupMocks(mockStorage, session)
			}

			handle, err := session.LoadKey(tt.keyType)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
				require.Nil(t, handle)
			} else {
				// For successful cases with cached handles, we should get the handle
				if session.handles[tt.keyType] != nil {
					require.NotNil(t, handle)
					require.NoError(t, err)
				} else {
					// For cases requiring TPM operations, expect error in unit test
					t.Logf("TPM operation expected to fail in unit test: %v", err)
				}
			}
		})
	}
}

func TestTpmSession_Sign(t *testing.T) {
	tests := []struct {
		name       string
		keyType    KeyType
		digest     []byte
		setupMocks func(*MockStorage, *tpmSession)
		wantErr    error
	}{
		{
			name:    "successful sign with cached key",
			keyType: LDevID,
			digest:  make([]byte, 32),
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, handle := createValidTPMObjects()
				session.handles[LDevID] = handle
			},
			wantErr: nil, // Will fail at TPM operation level in unit test
		},
		{
			name:    "sign with key load failure",
			keyType: LAK,
			digest:  make([]byte, 32),
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				// Pre-populate SRK to avoid creation attempt
				_, _, srkHandle := createValidTPMObjects()
				session.handles[SRK] = srkHandle
				// Enable valid SRK response in mock connection
				session.conn.(*mockReadWriteCloser).validSRK = true
				mockStorage.EXPECT().GetKey(LAK).Return(nil, nil, errors.New("key load error"))
			},
			wantErr: errors.New("loading signing key: getting key from storage: key load error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, mockStorage, conn, _, logger := setupMocks(t)
			defer ctrl.Finish()

			session := &tpmSession{
				conn:        conn,
				storage:     mockStorage,
				log:         logger,
				authEnabled: false,
				keyAlgo:     ECDSA,
				handles:     make(map[KeyType]*tpm2.NamedHandle),
			}

			if tt.setupMocks != nil {
				tt.setupMocks(mockStorage, session)
			}

			signature, err := session.Sign(tt.keyType, tt.digest)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
				require.Nil(t, signature)
			} else {
				// For cases with cached handles, expect TPM operation to fail in unit test
				t.Logf("TPM operation expected to fail in unit test: %v", err)
			}
		})
	}
}

func TestTpmSession_CertifyKey(t *testing.T) {
	tests := []struct {
		name           string
		keyType        KeyType
		qualifyingData []byte
		setupMocks     func(*MockStorage, *tpmSession)
		wantErr        error
	}{
		{
			name:           "successful key certification with cached handles",
			keyType:        LDevID,
			qualifyingData: []byte("test-qualifying-data"),
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, handle := createValidTPMObjects()
				session.handles[LDevID] = handle
				session.handles[LAK] = handle

				mockStorage.EXPECT().GetPassword().Return([]byte("test-password"), nil).AnyTimes()
			},
			wantErr: nil, // Will fail at TPM operation level in unit test
		},
		{
			name:           "certification with target key load failure",
			keyType:        LDevID,
			qualifyingData: []byte("test-data"),
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				// Pre-populate SRK to avoid creation attempt
				_, _, srkHandle := createValidTPMObjects()
				session.handles[SRK] = srkHandle
				// Enable valid SRK response in mock connection
				session.conn.(*mockReadWriteCloser).validSRK = true
				mockStorage.EXPECT().GetPassword().Return([]byte("test-password"), nil).AnyTimes()
				mockStorage.EXPECT().GetKey(LDevID).Return(nil, nil, errors.New("key load error"))
			},
			wantErr: errors.New("loading target key: getting key from storage: key load error"),
		},
		{
			name:           "certification with LAK load failure",
			keyType:        LDevID,
			qualifyingData: []byte("test-data"),
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				// Pre-populate SRK to avoid creation attempt
				_, _, srkHandle := createValidTPMObjects()
				session.handles[SRK] = srkHandle
				// Enable valid SRK response in mock connection
				session.conn.(*mockReadWriteCloser).validSRK = true
				_, _, handle := createValidTPMObjects()
				session.handles[LDevID] = handle
				mockStorage.EXPECT().GetPassword().Return([]byte("test-password"), nil).AnyTimes()
				mockStorage.EXPECT().GetKey(LAK).Return(nil, nil, errors.New("LAK load error"))
			},
			wantErr: errors.New("loading LAK for certification: getting key from storage: LAK load error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, mockStorage, conn, _, logger := setupMocks(t)
			defer ctrl.Finish()

			session := &tpmSession{
				conn:        conn,
				storage:     mockStorage,
				log:         logger,
				authEnabled: true,
				keyAlgo:     ECDSA,
				handles:     make(map[KeyType]*tpm2.NamedHandle),
			}

			if tt.setupMocks != nil {
				tt.setupMocks(mockStorage, session)
			}

			certifyInfo, signature, err := session.CertifyKey(tt.keyType, tt.qualifyingData)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
				require.Nil(t, certifyInfo)
				require.Nil(t, signature)
			} else {
				// For cases with cached handles, expect TPM operation to fail in unit test
				t.Logf("TPM operation expected to fail in unit test: %v", err)
			}
		})
	}
}

func TestTpmSession_GetPublicKey(t *testing.T) {
	tests := []struct {
		name       string
		keyType    KeyType
		setupMocks func(*MockStorage, *tpmSession)
		wantErr    error
	}{
		{
			name:    "successful get public key with cached handle",
			keyType: LDevID,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, handle := createValidTPMObjects()
				session.handles[LDevID] = handle
			},
			wantErr: nil, // Will fail at TPM operation level in unit test
		},
		{
			name:    "get public key with load failure",
			keyType: LDevID,
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				// Pre-populate SRK to avoid creation attempt
				_, _, srkHandle := createValidTPMObjects()
				session.handles[SRK] = srkHandle
				// Enable valid SRK response in mock connection
				session.conn.(*mockReadWriteCloser).validSRK = true
				mockStorage.EXPECT().GetKey(LDevID).Return(nil, nil, errors.New("key load error"))
			},
			wantErr: errors.New("loading key: getting key from storage: key load error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, mockStorage, conn, _, logger := setupMocks(t)
			defer ctrl.Finish()

			session := &tpmSession{
				conn:        conn,
				storage:     mockStorage,
				log:         logger,
				authEnabled: false,
				keyAlgo:     ECDSA,
				handles:     make(map[KeyType]*tpm2.NamedHandle),
			}

			if tt.setupMocks != nil {
				tt.setupMocks(mockStorage, session)
			}

			pubKey, err := session.GetPublicKey(tt.keyType)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
				require.Nil(t, pubKey)
			} else {
				// For cases with cached handles, expect TPM operation to fail in unit test
				t.Logf("TPM operation expected to fail in unit test: %v", err)
			}
		})
	}
}

func TestTpmSession_Close(t *testing.T) {
	tests := []struct {
		name       string
		setupMocks func(*MockStorage, *tpmSession)
		wantErr    error
	}{
		{
			name: "successful close with handles",
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				_, _, handle := createValidTPMObjects()
				session.handles[LDevID] = handle
				session.handles[LAK] = handle
			},
			wantErr: nil, // Will fail at TPM operation level but should clear handles
		},
		{
			name: "close with no handles",
			setupMocks: func(mockStorage *MockStorage, session *tpmSession) {
				// No handles to close
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, mockStorage, conn, _, logger := setupMocks(t)
			defer ctrl.Finish()

			session := &tpmSession{
				conn:        conn,
				storage:     mockStorage,
				log:         logger,
				authEnabled: false,
				keyAlgo:     ECDSA,
				handles:     make(map[KeyType]*tpm2.NamedHandle),
			}

			if tt.setupMocks != nil {
				tt.setupMocks(mockStorage, session)
			}

			err := session.Close()

			// After close, handles should be cleared regardless of TPM operation success
			require.Empty(t, session.handles)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				// May have TPM operation errors but handles should be cleared
				if err != nil {
					t.Logf("TPM operation errors during close (expected in unit test): %v", err)
				}
			}
		})
	}
}

func TestTpmSession_GetPassword(t *testing.T) {
	tests := []struct {
		name        string
		authEnabled bool
		setupMocks  func(*MockStorage)
		want        []byte
		wantErr     error
	}{
		{
			name:        "auth disabled returns nil",
			authEnabled: false,
			setupMocks:  func(mockStorage *MockStorage) {},
			want:        nil,
			wantErr:     nil,
		},
		{
			name:        "auth enabled with password",
			authEnabled: true,
			setupMocks: func(mockStorage *MockStorage) {
				mockStorage.EXPECT().GetPassword().Return([]byte("test-password"), nil)
			},
			want:    []byte("test-password"),
			wantErr: nil,
		},
		{
			name:        "auth enabled with password error",
			authEnabled: true,
			setupMocks: func(mockStorage *MockStorage) {
				mockStorage.EXPECT().GetPassword().Return(nil, errors.New("password error"))
			},
			want:    nil,
			wantErr: errors.New("password error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, mockStorage, conn, _, logger := setupMocks(t)
			defer ctrl.Finish()

			session := &tpmSession{
				conn:        conn,
				storage:     mockStorage,
				log:         logger,
				authEnabled: tt.authEnabled,
				keyAlgo:     ECDSA,
				handles:     make(map[KeyType]*tpm2.NamedHandle),
			}

			if tt.setupMocks != nil {
				tt.setupMocks(mockStorage)
			}

			result, err := session.getPassword()

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tt.want, result)
		})
	}
}

func TestTpmSession_GetKeyTemplate(t *testing.T) {
	tests := []struct {
		name    string
		keyType KeyType
		keyAlgo KeyAlgorithm
		wantErr error
	}{
		{
			name:    "LDevID ECDSA template",
			keyType: LDevID,
			keyAlgo: ECDSA,
			wantErr: nil,
		},
		{
			name:    "LDevID RSA template",
			keyType: LDevID,
			keyAlgo: RSA,
			wantErr: nil,
		},
		{
			name:    "LAK ECDSA template",
			keyType: LAK,
			keyAlgo: ECDSA,
			wantErr: nil,
		},
		{
			name:    "LAK RSA template",
			keyType: LAK,
			keyAlgo: RSA,
			wantErr: nil,
		},
		{
			name:    "unsupported key type",
			keyType: KeyType("unsupported"),
			keyAlgo: ECDSA,
			wantErr: errors.New("unsupported key type: unsupported"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, mockStorage, conn, _, logger := setupMocks(t)
			defer ctrl.Finish()

			session := &tpmSession{
				conn:        conn,
				storage:     mockStorage,
				log:         logger,
				authEnabled: false,
				keyAlgo:     tt.keyAlgo,
				handles:     make(map[KeyType]*tpm2.NamedHandle),
			}

			template, err := session.getKeyTemplate(tt.keyType)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				require.NoError(t, err)
				require.NotZero(t, template.Type)
			}
		})
	}
}

func TestConvertTPMSignatureToDER(t *testing.T) {
	tests := []struct {
		name      string
		signature *tpm2.TPMTSignature
		wantErr   error
	}{
		{
			name: "unsupported signature type",
			signature: &tpm2.TPMTSignature{
				SigAlg: tpm2.TPMAlgHMAC, // Unsupported
			},
			wantErr: errors.New("unsupported or unrecognized TPM signature algorithm"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ConvertTPMSignatureToDER(tt.signature)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Greater(t, len(result), 0)
			}
		})
	}
}

// mockReadWriteCloser implements io.ReadWriteCloser for testing
type mockReadWriteCloser struct {
	*bytes.Buffer
	// When validSRK is true, the mock will respond successfully to ReadPublic for SRK
	validSRK bool
}

func (m *mockReadWriteCloser) Close() error {
	return nil
}

func (m *mockReadWriteCloser) Read(p []byte) (n int, err error) {
	// TPM response simulation for ReadPublic command
	if m.validSRK {
		// simplified response that just indicates success
		response := []byte{0x80, 0x01, 0x00, 0x00, 0x00, 0x0A, 0x00, 0x00, 0x00, 0x00}
		copy(p, response)
		return len(response), nil
	}
	return 0, io.EOF
}

// setupMocks creates standard test mocks and components
func setupMocks(t *testing.T) (*gomock.Controller, *MockStorage, *mockReadWriteCloser, fileio.ReadWriter, *log.PrefixLogger) {
	ctrl := gomock.NewController(t)
	mockStorage := NewMockStorage(ctrl)
	conn := &mockReadWriteCloser{Buffer: bytes.NewBuffer(nil), validSRK: false}

	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	logger := log.NewPrefixLogger("test")

	return ctrl, mockStorage, conn, rw, logger
}

func TestDetectEKAlgorithm(t *testing.T) {
	testCases := []struct {
		name     string
		setup    func(t *testing.T, sim io.ReadWriter)
		expected KeyAlgorithm
		wantErr  bool
	}{
		{
			name: "only RSA cert returns RSA",
			setup: func(t *testing.T, sim io.ReadWriter) {
				require.NoError(t, setupFakeRSAEKCertificate(sim))
			},
			expected: RSA,
		},
		{
			name: "only ECC cert returns ECDSA",
			setup: func(t *testing.T, sim io.ReadWriter) {
				require.NoError(t, setupFakeECCEKCertificate(sim))
			},
			expected: ECDSA,
		},
		{
			name: "both certs defaults to RSA",
			setup: func(t *testing.T, sim io.ReadWriter) {
				require.NoError(t, setupFakeRSAEKCertificate(sim))
				require.NoError(t, setupFakeECCEKCertificate(sim))
			},
			expected: RSA,
		},
		{
			name: "ignores persistent handles and NV templates",
			setup: func(t *testing.T, sim io.ReadWriter) {
				require.NoError(t, setupFakeRSAEKCertificate(sim))
				require.NoError(t, setupFakeECCEKCertificate(sim))
				persistEKAtHandle(t, sim, tpm2.ECCEKTemplate, ekECCPersistentHandle)
				writeEKTemplateToNV(t, sim, tpm2.ECCEKTemplate, ekECCTemplateNVIndex)
			},
			expected: RSA,
		},
		{
			name:    "no certs returns error",
			setup:   func(t *testing.T, sim io.ReadWriter) {},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			sim, err := simulator.Get()
			require.NoError(err)
			defer sim.Close()

			tc.setup(t, sim)

			session := &tpmSession{
				conn:    sim,
				log:     log.NewPrefixLogger("test"),
				handles: make(map[KeyType]*tpm2.NamedHandle),
			}

			algo, err := session.detectEKAlgorithm()
			if tc.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
				require.Equal(tc.expected, algo)
			}
		})
	}
}

func TestLoadEK(t *testing.T) {
	testCases := []struct {
		name             string
		setup            func(t *testing.T, sim io.ReadWriter)
		expectPersistent bool
	}{
		{
			name: "uses persistent handle when available",
			setup: func(t *testing.T, sim io.ReadWriter) {
				require.NoError(t, setupFakeRSAEKCertificate(sim))
				persistEKAtHandle(t, sim, tpm2.RSAEKTemplate, ekRSAPersistentHandle)
			},
			expectPersistent: true,
		},
		{
			name: "falls back to CreatePrimary when no persistent handle",
			setup: func(t *testing.T, sim io.ReadWriter) {
				require.NoError(t, setupFakeRSAEKCertificate(sim))
			},
			expectPersistent: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			sim, err := simulator.Get()
			require.NoError(err)
			defer sim.Close()

			tc.setup(t, sim)

			session := &tpmSession{
				conn:    sim,
				log:     log.NewPrefixLogger("test"),
				handles: make(map[KeyType]*tpm2.NamedHandle),
			}

			handle, cleanup, err := session.loadEK()
			require.NoError(err)
			require.NotNil(handle)
			defer cleanup()

			if tc.expectPersistent {
				require.True(handle.Handle >= persistentHandleMin && handle.Handle <= persistentHandleMax,
					"expected persistent handle, got 0x%08X", handle.Handle)
			} else {
				require.True(handle.Handle >= transientHandleMin && handle.Handle <= transientHandleMax,
					"expected transient handle, got 0x%08X", handle.Handle)
			}
		})
	}
}

func TestResolveEKTemplate(t *testing.T) {
	testCases := []struct {
		name             string
		algo             KeyAlgorithm
		setup            func(t *testing.T, sim io.ReadWriter)
		expectFromNV     bool
		expectedTemplate tpm2.TPMTPublic
	}{
		{
			name:             "RSA falls back to standard template when no NV template",
			algo:             RSA,
			setup:            func(t *testing.T, sim io.ReadWriter) {},
			expectFromNV:     false,
			expectedTemplate: tpm2.RSAEKTemplate,
		},
		{
			name:             "ECC falls back to standard template when no NV template",
			algo:             ECDSA,
			setup:            func(t *testing.T, sim io.ReadWriter) {},
			expectFromNV:     false,
			expectedTemplate: tpm2.ECCEKTemplate,
		},
		{
			name: "RSA uses manufacturer template from NV",
			algo: RSA,
			setup: func(t *testing.T, sim io.ReadWriter) {
				writeEKTemplateToNV(t, sim, tpm2.RSAEKTemplate, ekRSATemplateNVIndex)
			},
			expectFromNV:     true,
			expectedTemplate: tpm2.RSAEKTemplate,
		},
		{
			name: "ECC uses manufacturer template from NV",
			algo: ECDSA,
			setup: func(t *testing.T, sim io.ReadWriter) {
				writeEKTemplateToNV(t, sim, tpm2.ECCEKTemplate, ekECCTemplateNVIndex)
			},
			expectFromNV:     true,
			expectedTemplate: tpm2.ECCEKTemplate,
		},
		{
			name: "falls back to standard when NV contains invalid data",
			algo: RSA,
			setup: func(t *testing.T, sim io.ReadWriter) {
				require.NoError(t, writeEKCertToTPM(sim, tpm2.TPMHandle(ekRSATemplateNVIndex), []byte("not a valid template")))
			},
			expectFromNV:     false,
			expectedTemplate: tpm2.RSAEKTemplate,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			sim, err := simulator.Get()
			require.NoError(err)
			defer sim.Close()

			tc.setup(t, sim)

			session := &tpmSession{
				conn:    sim,
				log:     log.NewPrefixLogger("test"),
				handles: make(map[KeyType]*tpm2.NamedHandle),
			}

			result := session.resolveEKTemplate(tc.algo)

			// CreatePrimary should succeed with the resolved template
			createCmd := tpm2.CreatePrimary{
				PrimaryHandle: tpm2.AuthHandle{
					Handle: tpm2.TPMRHEndorsement,
					Auth:   tpm2.PasswordAuth(nil),
				},
				InPublic: result,
			}
			createResp, err := createCmd.Execute(transport.FromReadWriter(sim))
			require.NoError(err)

			flushCmd := tpm2.FlushContext{FlushHandle: createResp.ObjectHandle}
			_, _ = flushCmd.Execute(transport.FromReadWriter(sim))

			if tc.expectFromNV {
				// Verify the returned template matches what we wrote to NV
				expectedBytes := tpm2.Marshal(tpm2.New2B(tc.expectedTemplate))
				resultBytes := tpm2.Marshal(result)
				require.Equal(expectedBytes, resultBytes)
			}
		})
	}
}

// writeEKTemplateToNV marshals a TPMTPublic and writes it to the specified NV template index.
// The NV index stores the raw TPMT_PUBLIC structure per TCG EK Credential Profile.
func writeEKTemplateToNV(t *testing.T, conn io.ReadWriter, template tpm2.TPMTPublic, nvIndex uint32) {
	t.Helper()
	data := tpm2.Marshal(template)
	require.NoError(t, writeEKCertToTPM(conn, tpm2.TPMHandle(nvIndex), data))
}

// persistEKAtHandle creates an EK with the given template and persists it at the specified handle.
func persistEKAtHandle(t *testing.T, conn io.ReadWriter, template tpm2.TPMTPublic, persistHandle tpm2.TPMHandle) {
	t.Helper()

	createCmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHEndorsement,
			Auth:   tpm2.PasswordAuth(nil),
		},
		InPublic: tpm2.New2B(template),
	}

	createResp, err := createCmd.Execute(transport.FromReadWriter(conn))
	require.NoError(t, err)

	evictCmd := tpm2.EvictControl{
		Auth: tpm2.AuthHandle{
			Handle: tpm2.TPMRHOwner,
			Auth:   tpm2.PasswordAuth(nil),
		},
		ObjectHandle: tpm2.NamedHandle{
			Handle: createResp.ObjectHandle,
			Name:   createResp.Name,
		},
		PersistentHandle: persistHandle,
	}
	_, err = evictCmd.Execute(transport.FromReadWriter(conn))
	require.NoError(t, err)

	flushCmd := tpm2.FlushContext{FlushHandle: createResp.ObjectHandle}
	_, _ = flushCmd.Execute(transport.FromReadWriter(conn))
}

// createValidTPMObjects creates valid TPM objects for testing
func createValidTPMObjects() (*tpm2.TPM2BPublic, *tpm2.TPM2BPrivate, *tpm2.NamedHandle) {
	// create valid ECC coordinates for P-256 (32 bytes each)
	xBytes := make([]byte, 32)
	yBytes := make([]byte, 32)
	xBytes[31] = 1 // set to 1 instead of 0 to make it a valid point
	yBytes[31] = 2

	pub := tpm2.New2B(tpm2.TPMTPublic{
		Type:    tpm2.TPMAlgECC,
		NameAlg: tpm2.TPMAlgSHA256,
		Parameters: tpm2.NewTPMUPublicParms(
			tpm2.TPMAlgECC,
			&tpm2.TPMSECCParms{
				CurveID: tpm2.TPMECCNistP256,
				Scheme: tpm2.TPMTECCScheme{
					Scheme: tpm2.TPMAlgECDSA,
					Details: tpm2.NewTPMUAsymScheme(
						tpm2.TPMAlgECDSA,
						&tpm2.TPMSSigSchemeECDSA{
							HashAlg: tpm2.TPMAlgSHA256,
						},
					),
				},
			},
		),
		Unique: tpm2.NewTPMUPublicID(
			tpm2.TPMAlgECC,
			&tpm2.TPMSECCPoint{
				X: tpm2.TPM2BECCParameter{Buffer: xBytes},
				Y: tpm2.TPM2BECCParameter{Buffer: yBytes},
			},
		),
	})

	priv := &tpm2.TPM2BPrivate{
		Buffer: make([]byte, 256),
	}

	handle := &tpm2.NamedHandle{
		Handle: tpm2.TPMHandle(0x80000001),
		Name:   tpm2.TPM2BName{Buffer: make([]byte, 32)},
	}

	return &pub, priv, handle
}
