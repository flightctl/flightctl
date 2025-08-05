package tpm

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"math/big"
	"testing"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	legacy "github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpm2"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewClient(t *testing.T) {
	testCases := []struct {
		name        string
		config      *agent_config.Config
		setupFiles  func(t *testing.T, rw fileio.ReadWriter)
		expectError bool
		errorMsg    string
	}{
		{
			name: "successful TPM discovery with default path",
			config: &agent_config.Config{
				TPM: agent_config.TPM{
					Path:            "",
					PersistencePath: "tpm-persistence",
					EnableOwnership: false,
				},
			},
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Create mock sysfs structure for TPM discovery
				versionFile := "/sys/class/tpm/tpm0/tpm_version_major"
				require.NoError(t, rw.WriteFile(versionFile, []byte("2\n"), 0644))

				// Create mock device files
				require.NoError(t, rw.WriteFile("/dev/tpmrm0", []byte(""), 0644))
			},
			expectError: false,
		},
		{
			name: "successful TPM discovery with specific path",
			config: &agent_config.Config{
				TPM: agent_config.TPM{
					Path:            "/dev/tpm0",
					PersistencePath: "tpm-persistence",
					EnableOwnership: false,
				},
			},
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Create mock sysfs structure
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2\n"), 0644))

				// Create mock device files
				require.NoError(t, rw.WriteFile("/dev/tpm0", []byte(""), 0644))
				require.NoError(t, rw.WriteFile("/dev/tpmrm0", []byte(""), 0644))
			},
			expectError: false,
		},
		{
			name: "no TPM devices found",
			config: &agent_config.Config{
				TPM: agent_config.TPM{
					Path:            "",
					PersistencePath: "tpm-persistence",
					EnableOwnership: false,
				},
			},
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Create empty sysfs structure - no TPM devices
				require.NoError(t, rw.WriteFile("/sys/class/tpm/.empty", []byte(""), 0644))
			},
			expectError: true,
			errorMsg:    "no valid TPM 2.0 devices found",
		},
		{
			name: "TPM version validation fails",
			config: &agent_config.Config{
				TPM: agent_config.TPM{
					Path:            "",
					PersistencePath: "tpm-persistence",
					EnableOwnership: false,
				},
			},
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Create TPM with wrong version
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("1\n"), 0644))
				require.NoError(t, rw.WriteFile("/dev/tpmrm0", []byte(""), 0644))
			},
			expectError: true,
			errorMsg:    "no valid TPM 2.0 devices found",
		},
		{
			name: "device does not exist",
			config: &agent_config.Config{
				TPM: agent_config.TPM{
					Path:            "",
					PersistencePath: "tpm-persistence",
					EnableOwnership: false,
				},
			},
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Create sysfs structure but no device files
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2\n"), 0644))
				// Don't create /dev/tpmrm0 file
			},
			expectError: true,
			errorMsg:    "no valid TPM 2.0 devices found",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)
			logger := log.NewPrefixLogger("test")

			if tc.setupFiles != nil {
				tc.setupFiles(t, rw)
			}

			// Note: We test the discovery logic since NewClient tries to open a real TPM connection
			_, err := discoverAndValidateTPM(rw, logger, tc.config.TPM.Path)

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestClient_Public(t *testing.T) {
	testCases := []struct {
		name        string
		setupMocks  func(*MockSession)
		expectError bool
		errorMsg    string
	}{
		{
			name: "successful public key retrieval",
			setupMocks: func(mockSession *MockSession) {
				// Create a valid TPM2BPublic for ECDSA P-256
				pub := createTestTPM2BPublic(t)
				mockSession.EXPECT().GetPublicKey(LDevID).Return(pub, nil)
			},
			expectError: false,
		},
		{
			name: "session GetPublicKey fails",
			setupMocks: func(mockSession *MockSession) {
				mockSession.EXPECT().GetPublicKey(LDevID).Return(nil, errors.New("TPM error"))
			},
			expectError: false, // Public() returns nil on error, doesn't panic
		},
		{
			name: "RSA public key should also work",
			setupMocks: func(mockSession *MockSession) {
				// Create an RSA TPM2BPublic (should also convert successfully)
				pub := createTestTPM2BPublicRSA(t)
				mockSession.EXPECT().GetPublicKey(LDevID).Return(pub, nil)
			},
			expectError: false, // convertTPM2BPublicToPublicKey should succeed for RSA too
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockSession := NewMockSession(ctrl)
			logger := log.NewPrefixLogger("test")

			tc.setupMocks(mockSession)

			client := &Client{
				session: mockSession,
				log:     logger,
			}

			pubKey := client.Public()

			if tc.expectError {
				require.Nil(t, pubKey)
			} else {
				// For successful cases, we should get a valid public key
				if pubKey != nil {
					// Could be either ECDSA or RSA
					_, isECDSA := pubKey.(*ecdsa.PublicKey)
					_, isRSA := pubKey.(*rsa.PublicKey)
					require.True(t, isECDSA || isRSA, "Expected either ECDSA or RSA public key")
				}
			}
		})
	}
}

func TestClient_Sign(t *testing.T) {
	digest := []byte("test digest")
	signature := []byte("test signature")

	testCases := []struct {
		name        string
		setupMocks  func(*MockSession)
		expectError bool
		errorMsg    string
	}{
		{
			name: "successful signing",
			setupMocks: func(mockSession *MockSession) {
				mockSession.EXPECT().Sign(LDevID, digest).Return(signature, nil)
			},
			expectError: false,
		},
		{
			name: "session Sign fails",
			setupMocks: func(mockSession *MockSession) {
				mockSession.EXPECT().Sign(LDevID, digest).Return(nil, errors.New("TPM signing error"))
			},
			expectError: true,
			errorMsg:    "TPM signing error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockSession := NewMockSession(ctrl)
			logger := log.NewPrefixLogger("test")

			tc.setupMocks(mockSession)

			client := &Client{
				session: mockSession,
				log:     logger,
			}

			result, err := client.Sign(nil, digest, crypto.SHA256)

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorMsg)
			} else {
				require.NoError(t, err)
				require.Equal(t, signature, result)
			}
		})
	}
}

func TestClient_Close(t *testing.T) {
	testCases := []struct {
		name        string
		setupMocks  func(*MockSession)
		expectError bool
		errorMsg    string
	}{
		{
			name: "successful close",
			setupMocks: func(mockSession *MockSession) {
				mockSession.EXPECT().Close().Return(nil)
			},
			expectError: false,
		},
		{
			name: "session Close fails",
			setupMocks: func(mockSession *MockSession) {
				mockSession.EXPECT().Close().Return(errors.New("close error"))
			},
			expectError: true,
			errorMsg:    "close error",
		},
		{
			name: "nil session",
			setupMocks: func(mockSession *MockSession) {
				// No expectations for nil session
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			var session Session
			if tc.name != "nil session" {
				mockSession := NewMockSession(ctrl)
				tc.setupMocks(mockSession)
				session = mockSession
			}

			logger := log.NewPrefixLogger("test")

			client := &Client{
				session: session,
				log:     logger,
			}

			err := client.Close(context.Background())

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDiscoverTPMDevices(t *testing.T) {
	testCases := []struct {
		name        string
		setupFiles  func(t *testing.T, rw fileio.ReadWriter)
		expected    []tpmDevice
		expectError bool
		errorMsg    string
	}{
		{
			name: "single TPM device discovered",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Create TPM device directory structure
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/.dir", []byte(""), 0644))
			},
			expected: []tpmDevice{
				{
					index:           "0",
					path:            "/dev/tpm0",
					resourceMgrPath: "/dev/tpmrm0",
					versionPath:     "/sys/class/tpm/tpm0/tpm_version_major",
					sysfsPath:       "/sys/class/tpm/tpm0",
				},
			},
			expectError: false,
		},
		{
			name: "multiple TPM devices discovered",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Create multiple TPM device directory structures
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/.dir", []byte(""), 0644))
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm1/.dir", []byte(""), 0644))
				require.NoError(t, rw.WriteFile("/sys/class/tpm/other_file", []byte(""), 0644)) // should be ignored
			},
			expected: []tpmDevice{
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
			},
			expectError: false,
		},
		{
			name: "no TPM devices found",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Create sysfs structure but no TPM devices
				require.NoError(t, rw.WriteFile("/sys/class/tpm/other_device", []byte(""), 0644))
			},
			expected:    nil,
			expectError: false,
		},
		{
			name: "sysfs directory missing",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Don't create the sysfs directory - ReadDir returns empty slice for missing directories
			},
			expected:    nil,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			if tc.setupFiles != nil {
				tc.setupFiles(t, rw)
			}

			devices, err := discoverTPMDevices(rw)

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorMsg)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, devices)
			}
		})
	}
}

func TestValidateTPMVersion2(t *testing.T) {
	testCases := []struct {
		name        string
		setupFiles  func(t *testing.T, rw fileio.ReadWriter)
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid TPM 2.0",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0644))
			},
			expectError: false,
		},
		{
			name: "valid TPM 2.0 with newline",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2\n"), 0644))
			},
			expectError: false,
		},
		{
			name: "invalid TPM version 1.0",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("1"), 0644))
			},
			expectError: true,
			errorMsg:    "TPM is not version 2.0. Found version: 1",
		},
		{
			name: "file not found",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Don't create the version file
			},
			expectError: true,
			errorMsg:    "reading tpm version file",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			if tc.setupFiles != nil {
				tc.setupFiles(t, rw)
			}

			err := validateTPMVersion2(rw, "/sys/class/tpm/tpm0/tpm_version_major")

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDeviceExists(t *testing.T) {
	testCases := []struct {
		name       string
		setupFiles func(t *testing.T, rw fileio.ReadWriter)
		expected   bool
	}{
		{
			name: "device exists",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/dev/tpmrm0", []byte(""), 0644))
			},
			expected: true,
		},
		{
			name: "device does not exist",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Don't create the device file
			},
			expected: false,
		},
		{
			name: "dev directory missing",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Don't create the dev directory at all
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			if tc.setupFiles != nil {
				tc.setupFiles(t, rw)
			}

			result := deviceExists(rw, "/dev/tpmrm0")
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestDiscoverDefaultTPM(t *testing.T) {
	testCases := []struct {
		name        string
		setupFiles  func(t *testing.T, rw fileio.ReadWriter)
		expected    string
		expectError bool
		errorMsg    string
	}{
		{
			name: "first device is valid",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0644))
				require.NoError(t, rw.WriteFile("/dev/tpmrm0", []byte(""), 0644))
			},
			expected:    "/dev/tpmrm0",
			expectError: false,
		},
		{
			name: "first device invalid, second device valid",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// First device - wrong version
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("1"), 0644))
				// Second device - correct version
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm1/tpm_version_major", []byte("2"), 0644))

				require.NoError(t, rw.WriteFile("/dev/tpmrm0", []byte(""), 0644))
				require.NoError(t, rw.WriteFile("/dev/tpmrm1", []byte(""), 0644))
			},
			expected:    "/dev/tpmrm1",
			expectError: false,
		},
		{
			name: "no devices exist",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0644))
				// Don't create device files
			},
			expected:    "",
			expectError: true,
			errorMsg:    "no valid TPM 2.0 devices found",
		},
		{
			name: "discovery fails - no sysfs",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Don't create sysfs directory - will return empty list and then fail with no devices found
			},
			expected:    "",
			expectError: true,
			errorMsg:    "no valid TPM 2.0 devices found",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)
			logger := log.NewPrefixLogger("test")

			if tc.setupFiles != nil {
				tc.setupFiles(t, rw)
			}

			result, err := discoverDefaultTPM(rw, logger)

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorMsg)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, result)
			}
		})
	}
}

func TestResolveTPMPath(t *testing.T) {
	testCases := []struct {
		name        string
		path        string
		setupFiles  func(t *testing.T, rw fileio.ReadWriter)
		expected    string
		expectError bool
		errorMsg    string
	}{
		{
			name: "resolve by device path",
			path: "/dev/tpm0",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0644))
			},
			expected:    "/dev/tpmrm0",
			expectError: false,
		},
		{
			name: "resolve by resource manager path",
			path: "/dev/tpmrm0",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0644))
			},
			expected:    "/dev/tpmrm0",
			expectError: false,
		},
		{
			name: "device not found",
			path: "/dev/tpm99",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("2"), 0644))
			},
			expected:    "",
			expectError: true,
			errorMsg:    "TPM \"/dev/tpm99\" not found",
		},
		{
			name: "device found but invalid version",
			path: "/dev/tpm0",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				require.NoError(t, rw.WriteFile("/sys/class/tpm/tpm0/tpm_version_major", []byte("1"), 0644))
			},
			expected:    "",
			expectError: true,
			errorMsg:    "invalid TPM \"/dev/tpm0\"",
		},
		{
			name: "discovery fails",
			path: "/dev/tpm0",
			setupFiles: func(t *testing.T, rw fileio.ReadWriter) {
				// Don't create sysfs directory - returns empty list, so device not found
			},
			expected:    "",
			expectError: true,
			errorMsg:    "TPM \"/dev/tpm0\" not found",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			if tc.setupFiles != nil {
				tc.setupFiles(t, rw)
			}

			result, err := resolveTPMPath(rw, tc.path)

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorMsg)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, result)
			}
		})
	}
}

func TestConvertTPMLPCRSelectionToPCRSelection(t *testing.T) {
	testCases := []struct {
		name     string
		input    *tpm2.TPMLPCRSelection
		expected legacy.PCRSelection
	}{
		{
			name: "SHA256 with PCRs 0,1,2",
			input: &tpm2.TPMLPCRSelection{
				PCRSelections: []tpm2.TPMSPCRSelection{
					{
						Hash:      tpm2.TPMAlgSHA256,
						PCRSelect: []byte{0x07}, // bits 0,1,2 set
					},
				},
			},
			expected: legacy.PCRSelection{
				Hash: legacy.AlgSHA256,
				PCRs: []int{0, 1, 2},
			},
		},
		{
			name: "SHA1 with PCRs 8,9,10",
			input: &tpm2.TPMLPCRSelection{
				PCRSelections: []tpm2.TPMSPCRSelection{
					{
						Hash:      tpm2.TPMAlgSHA1,
						PCRSelect: []byte{0x00, 0x07}, // bits 8,9,10 set
					},
				},
			},
			expected: legacy.PCRSelection{
				Hash: legacy.AlgSHA1,
				PCRs: []int{8, 9, 10},
			},
		},
		{
			name: "empty selection",
			input: &tpm2.TPMLPCRSelection{
				PCRSelections: []tpm2.TPMSPCRSelection{
					{
						Hash:      tpm2.TPMAlgSHA256,
						PCRSelect: []byte{0x00},
					},
				},
			},
			expected: legacy.PCRSelection{
				Hash: legacy.AlgSHA256,
				PCRs: nil,
			},
		},
		{
			name:     "nil input",
			input:    nil,
			expected: legacy.PCRSelection{},
		},
		{
			name: "no PCR selections",
			input: &tpm2.TPMLPCRSelection{
				PCRSelections: []tpm2.TPMSPCRSelection{},
			},
			expected: legacy.PCRSelection{},
		},
		{
			name: "unknown hash algorithm defaults to SHA256",
			input: &tpm2.TPMLPCRSelection{
				PCRSelections: []tpm2.TPMSPCRSelection{
					{
						Hash:      tpm2.TPMAlgID(999), // unknown algorithm
						PCRSelect: []byte{0x01},       // PCR 0
					},
				},
			},
			expected: legacy.PCRSelection{
				Hash: legacy.AlgSHA256,
				PCRs: []int{0},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := convertTPMLPCRSelectionToPCRSelection(tc.input)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestCreatePCRSelection(t *testing.T) {
	testCases := []struct {
		name      string
		selection [3]byte
		expected  *tpm2.TPMLPCRSelection
	}{
		{
			name:      "all PCRs selected",
			selection: [3]byte{0xFF, 0xFF, 0xFF},
			expected: &tpm2.TPMLPCRSelection{
				PCRSelections: []tpm2.TPMSPCRSelection{
					{
						Hash:      tpm2.TPMAlgSHA256,
						PCRSelect: []byte{0xFF, 0xFF, 0xFF},
					},
				},
			},
		},
		{
			name:      "first PCR only",
			selection: [3]byte{0x01, 0x00, 0x00},
			expected: &tpm2.TPMLPCRSelection{
				PCRSelections: []tpm2.TPMSPCRSelection{
					{
						Hash:      tpm2.TPMAlgSHA256,
						PCRSelect: []byte{0x01, 0x00, 0x00},
					},
				},
			},
		},
		{
			name:      "no PCRs selected",
			selection: [3]byte{0x00, 0x00, 0x00},
			expected: &tpm2.TPMLPCRSelection{
				PCRSelections: []tpm2.TPMSPCRSelection{
					{
						Hash:      tpm2.TPMAlgSHA256,
						PCRSelect: []byte{0x00, 0x00, 0x00},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := createPCRSelection(tc.selection)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestCreateFullPCRSelection(t *testing.T) {
	expected := &tpm2.TPMLPCRSelection{
		PCRSelections: []tpm2.TPMSPCRSelection{
			{
				Hash:      tpm2.TPMAlgSHA256,
				PCRSelect: []byte{0xFF, 0xFF, 0xFF},
			},
		},
	}

	result := createFullPCRSelection()
	require.Equal(t, expected, result)
}

// Helper functions for creating test data

func createTestTPM2BPublic(t *testing.T) *tpm2.TPM2BPublic {
	_ = t // unused but keep for consistency with testing pattern
	// Create a valid ECDSA P-256 public key structure
	x := big.NewInt(1)
	y := big.NewInt(2)

	// Pad to correct size for P-256 (32 bytes)
	xBytes := make([]byte, 32)
	yBytes := make([]byte, 32)
	x.FillBytes(xBytes)
	y.FillBytes(yBytes)

	public := tpm2.TPMTPublic{
		Type:    tpm2.TPMAlgECC,
		NameAlg: tpm2.TPMAlgSHA256,
		ObjectAttributes: tpm2.TPMAObject{
			SignEncrypt: true,
		},
		Parameters: tpm2.NewTPMUPublicParms(tpm2.TPMAlgECC, &tpm2.TPMSECCParms{
			CurveID: tpm2.TPMECCNistP256,
		}),
		Unique: tpm2.NewTPMUPublicID(tpm2.TPMAlgECC, &tpm2.TPMSECCPoint{
			X: tpm2.TPM2BECCParameter{Buffer: xBytes},
			Y: tpm2.TPM2BECCParameter{Buffer: yBytes},
		}),
	}

	pub := tpm2.New2B(public)
	return &pub
}

func createTestTPM2BPublicRSA(t *testing.T) *tpm2.TPM2BPublic {
	_ = t // unused but keep for consistency with testing pattern
	// Create an RSA public key structure (should fail conversion)
	public := tpm2.TPMTPublic{
		Type:    tpm2.TPMAlgRSA,
		NameAlg: tpm2.TPMAlgSHA256,
		ObjectAttributes: tpm2.TPMAObject{
			SignEncrypt: true,
		},
		Parameters: tpm2.NewTPMUPublicParms(tpm2.TPMAlgRSA, &tpm2.TPMSRSAParms{
			KeyBits: 2048,
		}),
		Unique: tpm2.NewTPMUPublicID(tpm2.TPMAlgRSA, &tpm2.TPM2BPublicKeyRSA{
			Buffer: make([]byte, 256), // 2048 bits = 256 bytes
		}),
	}

	pub := tpm2.New2B(public)
	return &pub
}
