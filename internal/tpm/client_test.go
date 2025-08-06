package tpm

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"testing"
	"time"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm-tools/simulator"
	legacy "github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
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

func TestClient_SimulatorIntegration(t *testing.T) {
	testCases := []struct {
		name            string
		enableOwnership bool
	}{
		{
			name:            "client with ownership enabled",
			enableOwnership: true,
		},
		{
			name:            "client with ownership disabled",
			enableOwnership: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			sim, err := simulator.Get()
			require.NoError(err)
			defer sim.Close()

			// Set up fake RSA endorsement key certificate in the simulator
			err = setupFakeEKCertificate(sim)
			require.NoError(err)

			rw := fileio.NewReadWriter(fileio.WithTestRootDir(t.TempDir()))

			c, err := newClientWithConnection(sim, log.NewPrefixLogger("test"), rw, &agent_config.Config{
				TPM: agent_config.TPM{
					Enabled:         true,
					Path:            agent_config.DefaultTPMDevicePath,
					PersistencePath: agent_config.DefaultTPMKeyBlobFile,
					EnableOwnership: tc.enableOwnership,
				},
			}, "test-model", "test-serial")
			require.NoError(err)

			ctx := context.Background()

			// Perform initial client operations and store the public key for comparison
			originalPublic := performClientOperations(t, c, ctx, "initial")
			err = c.Close(ctx)
			require.NoError(err)

			// Reset the TPM simulator to simulate a reboot
			err = sim.Reset()
			require.NoError(err)

			// Create a new client with the same configuration
			c2, err := newClientWithConnection(sim, log.NewPrefixLogger("test"), rw, &agent_config.Config{
				TPM: agent_config.TPM{
					Enabled:         true,
					Path:            agent_config.DefaultTPMDevicePath,
					PersistencePath: agent_config.DefaultTPMKeyBlobFile,
					EnableOwnership: tc.enableOwnership,
				},
			}, "test-model", "test-serial")
			require.NoError(err)
			// Perform the same client operations after reset
			public2 := performClientOperations(t, c2, ctx, "after-reset")

			// Verify that the public key is the same after reset (persistent key)
			require.Equal(originalPublic, public2, "Public key should remain the same after TPM reset")

			// Test Clear operation - should reset TPM hierarchy and clear storage
			err = c2.Clear()
			require.NoError(err)

			err = c2.Close(ctx)
			require.NoError(err)

			// Create a third client to verify Clear worked - TPM hierarchy reset and storage cleared
			c3, err := newClientWithConnection(sim, log.NewPrefixLogger("test"), rw, &agent_config.Config{
				TPM: agent_config.TPM{
					Enabled:         true,
					Path:            agent_config.DefaultTPMDevicePath,
					PersistencePath: agent_config.DefaultTPMKeyBlobFile,
					EnableOwnership: tc.enableOwnership,
				},
			}, "test-model", "test-serial")
			require.NoError(err)

			// Verify the third client can operate (TPM hierarchy reset successful)
			public3 := performClientOperations(t, c3, ctx, "after-clear")

			// Verify that the public key is different after Clear (fresh keys generated)
			require.NotEqual(originalPublic, public3, "Public key should be different after TPM Clear operation")

			err = c3.Close(ctx)
			require.NoError(err)
		})
	}
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

// generateEKCertWithRealPublicKey creates a certificate using the actual TPM EK public key
func generateEKCertWithRealPublicKey(tmpPublic *tpm2.TPM2BPublic) ([]byte, error) {
	// Get the TPM public key contents
	publicContents, err := tmpPublic.Contents()
	if err != nil {
		return nil, err
	}

	// Extract the RSA public key
	rsaUnique, err := publicContents.Unique.RSA()
	if err != nil {
		return nil, err
	}

	// Create Go RSA public key from TPM data
	rsaPublicKey := &rsa.PublicKey{
		N: new(big.Int).SetBytes(rsaUnique.Buffer),
		E: 65537, // Standard RSA exponent
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"FlightCTL Test EK"},
			Country:      []string{"US"},
			Locality:     []string{"Test"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
	}

	// Generate a temporary private key for signing the certificate
	tempPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	// Create self-signed certificate using the actual EK public key
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, rsaPublicKey, tempPrivateKey)
	if err != nil {
		return nil, err
	}

	return certDER, nil
}

// writeEKCertToTPM writes the endorsement key certificate to TPM NVRAM
func writeEKCertToTPM(conn io.ReadWriter, index tpm2.TPMHandle, certDER []byte) error {
	// Define NVRAM space with appropriate attributes for EK certificate
	derLen := len(certDER)
	if derLen > math.MaxUint16 {
		panic(fmt.Errorf("cannot store more than %d bytes in nvram", math.MaxUint16))
	}
	nvPublic := tpm2.TPMSNVPublic{
		NVIndex: index,
		NameAlg: tpm2.TPMAlgSHA256,
		Attributes: tpm2.TPMANV{
			PPWrite:        true, // Platform hierarchy can write
			WriteDefine:    true,
			PPRead:         true, // Platform hierarchy can read
			OwnerRead:      true, // Owner hierarchy can read
			AuthRead:       true,
			NoDA:           true, // No dictionary attack protection
			PlatformCreate: true, // Platform created
		},
		DataSize: uint16(derLen),
	}

	cmd := tpm2.NVDefineSpace{
		AuthHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHPlatform,
			Auth:   tpm2.PasswordAuth(nil),
		},
		Auth:       tpm2.TPM2BAuth{},
		PublicInfo: tpm2.New2B(nvPublic),
	}

	_, err := cmd.Execute(transport.FromReadWriter(conn))
	if err != nil {
		return err
	}

	// Write certificate data to NVRAM
	writeCmd := tpm2.NVWrite{
		AuthHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHPlatform,
			Auth:   tpm2.PasswordAuth(nil),
		},
		NVIndex: tpm2.NamedHandle{
			Handle: index,
			Name:   tpm2.TPM2BName{}, // Empty name for platform auth
		},
		Data: tpm2.TPM2BMaxNVBuffer{
			Buffer: certDER,
		},
		Offset: 0,
	}

	_, err = writeCmd.Execute(transport.FromReadWriter(conn))
	return err
}

// setupFakeEKCertificate sets up a fake RSA endorsement key certificate in the TPM simulator
func setupFakeEKCertificate(conn io.ReadWriter) error {
	// First, create the RSA endorsement key in the TPM
	createEKCmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHEndorsement,
			Auth:   tpm2.PasswordAuth(nil),
		},
		InPublic: tpm2.New2B(tpm2.RSAEKTemplate),
	}

	createResp, err := createEKCmd.Execute(transport.FromReadWriter(conn))
	if err != nil {
		return err
	}

	// Ensure the EK handle is always flushed to prevent resource leaks
	defer func() {
		flushCmd := tpm2.FlushContext{
			FlushHandle: createResp.ObjectHandle,
		}
		_, _ = flushCmd.Execute(transport.FromReadWriter(conn)) // Ignore errors during cleanup
	}()

	// Get the public key from the created EK
	readPublicCmd := tpm2.ReadPublic{
		ObjectHandle: createResp.ObjectHandle,
	}

	publicResp, err := readPublicCmd.Execute(transport.FromReadWriter(conn))
	if err != nil {
		return err
	}

	// Generate a certificate using the actual EK public key
	certDER, err := generateEKCertWithRealPublicKey(&publicResp.OutPublic)
	if err != nil {
		return err
	}

	return writeEKCertToTPM(conn, tpm2.TPMHandle(client.EKCertNVIndexRSA), certDER)
}

// performClientOperations performs a standard set of client operations and returns the public key
func performClientOperations(t *testing.T, c *Client, ctx context.Context, testSuffix string) crypto.PublicKey {
	require := require.New(t)

	// Ensure the CSR generation flow doesn't fail
	csr, err := c.MakeCSR("test-name", make([]byte, 32))
	require.NoError(err)
	require.NotEmpty(csr)

	// Test VendorInfoCollector
	s := c.VendorInfoCollector(ctx)
	require.NotEmpty(s)

	// Ensure basic signing methods work
	signer := c.GetSigner()
	require.NotNil(signer)

	public := c.Public()
	require.NotNil(public)

	// Test signing functionality
	testData1 := []byte("test data for signing " + testSuffix)
	hash1 := sha256.Sum256(testData1)
	signature1, err := signer.Sign(rand.Reader, hash1[:], crypto.SHA256)
	require.NoError(err)
	require.NotEmpty(signature1)

	// Verify the signature using the public key - handle both ECDSA and RSA
	switch pubKey := public.(type) {
	case *ecdsa.PublicKey:
		// For ECDSA, we need to parse the DER-encoded signature
		// The signer should return DER-encoded signatures
		valid := ecdsa.VerifyASN1(pubKey, hash1[:], signature1)
		require.True(valid, "ECDSA signature verification failed")
	case *rsa.PublicKey:
		err = rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, hash1[:], signature1)
		require.NoError(err)
	default:
		require.Fail("unsupported public key type")
	}

	// Test signing different data
	testData2 := []byte("different test data " + testSuffix)
	hash2 := sha256.Sum256(testData2)
	signature2, err := signer.Sign(rand.Reader, hash2[:], crypto.SHA256)
	require.NoError(err)
	require.NotEmpty(signature2)

	// Verify the second signature
	switch pubKey := public.(type) {
	case *ecdsa.PublicKey:
		valid := ecdsa.VerifyASN1(pubKey, hash2[:], signature2)
		require.True(valid, "ECDSA signature verification failed")
	case *rsa.PublicKey:
		err = rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, hash2[:], signature2)
		require.NoError(err)
	}

	// Ensure signatures are different for different data
	require.NotEqual(signature1, signature2)

	return public
}
