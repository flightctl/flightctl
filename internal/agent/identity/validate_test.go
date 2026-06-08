package identity

import (
	"crypto"
	"errors"
	"os"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestValidateCSRIdentity(t *testing.T) {
	_, priv, err := fccrypto.NewKeyPair()
	require.NoError(t, err)
	signer := priv.(crypto.Signer)

	makeCSR := func(t *testing.T, cn string) []byte {
		t.Helper()
		csr, err := fccrypto.MakeCSR(signer, cn)
		require.NoError(t, err)
		return csr
	}

	testCases := []struct {
		name       string
		csrBytes   []byte
		deviceName string
		wantErr    string
		isMismatch bool
	}{
		{
			name:       "When CN matches device name it should succeed",
			csrBytes:   makeCSR(t, "device-abc123"),
			deviceName: "device-abc123",
		},
		{
			name:       "When CN does not match device name it should return mismatch error",
			csrBytes:   makeCSR(t, "old-device-name"),
			deviceName: "new-device-name",
			wantErr:    `persisted CSR CN "old-device-name" does not match device name "new-device-name"`,
			isMismatch: true,
		},
		{
			name:       "When CN is empty it should return mismatch error",
			csrBytes:   makeCSR(t, ""),
			deviceName: "device-abc123",
			wantErr:    `persisted CSR CN "" does not match device name "device-abc123"`,
			isMismatch: true,
		},
		{
			name:       "When CSR bytes are empty it should return parse error",
			csrBytes:   []byte{},
			deviceName: "device-name",
			wantErr:    "parsing CSR",
		},
		{
			name:       "When CSR bytes are invalid it should return parse error",
			csrBytes:   []byte("not-a-valid-csr"),
			deviceName: "device-name",
			wantErr:    "parsing CSR",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			err := validateCSRIdentity(tc.csrBytes, tc.deviceName)
			if tc.wantErr != "" {
				require.ErrorContains(err, tc.wantErr)
				if tc.isMismatch {
					require.ErrorIs(err, errCSRIdentityMismatch)
				} else {
					require.False(errors.Is(err, errCSRIdentityMismatch))
				}
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestResolveCSR(t *testing.T) {
	_, priv, err := fccrypto.NewKeyPair()
	require.NoError(t, err)
	signer := priv.(crypto.Signer)

	makeCSR := func(t *testing.T, cn string) []byte {
		t.Helper()
		csr, err := fccrypto.MakeCSR(signer, cn)
		require.NoError(t, err)
		return csr
	}

	const (
		dataDir    = "/data"
		deviceName = "device-abc123"
	)
	csrPath := GetCSRPath(dataDir)

	testCases := []struct {
		name    string
		setup   func(t *testing.T, mockRW *fileio.MockReadWriter, mockProvider *MockProvider) []byte
		wantErr string
	}{
		{
			name: "When no persisted CSR exists it should generate and store a new one",
			setup: func(t *testing.T, mockRW *fileio.MockReadWriter, mockProvider *MockProvider) []byte {
				expectedCSR := makeCSR(t, deviceName)
				mockRW.EXPECT().PathExists(csrPath).Return(false, nil)
				mockProvider.EXPECT().GenerateCSR(deviceName).Return(expectedCSR, nil)
				mockRW.EXPECT().WriteFile(csrPath, expectedCSR, os.FileMode(0600)).Return(nil)
				return expectedCSR
			},
		},
		{
			name: "When persisted CSR matches identity it should return it",
			setup: func(t *testing.T, mockRW *fileio.MockReadWriter, mockProvider *MockProvider) []byte {
				existingCSR := makeCSR(t, deviceName)
				mockRW.EXPECT().PathExists(csrPath).Return(true, nil)
				mockRW.EXPECT().ReadFile(csrPath).Return(existingCSR, nil)
				return existingCSR
			},
		},
		{
			name: "When persisted CSR has mismatched CN it should regenerate",
			setup: func(t *testing.T, mockRW *fileio.MockReadWriter, mockProvider *MockProvider) []byte {
				staleCSR := makeCSR(t, "old-device-name")
				freshCSR := makeCSR(t, deviceName)
				gomock.InOrder(
					mockRW.EXPECT().PathExists(csrPath).Return(true, nil),
					mockRW.EXPECT().ReadFile(csrPath).Return(staleCSR, nil),
					mockProvider.EXPECT().GenerateCSR(deviceName).Return(freshCSR, nil),
					mockRW.EXPECT().WriteFile(csrPath, freshCSR, os.FileMode(0600)).Return(nil),
				)
				return freshCSR
			},
		},
		{
			name: "When CSR fails to load it should return error without regenerating",
			setup: func(t *testing.T, mockRW *fileio.MockReadWriter, mockProvider *MockProvider) []byte {
				mockRW.EXPECT().PathExists(csrPath).Return(false, errors.New("disk error"))
				return nil
			},
			wantErr: "loading CSR",
		},
		{
			name: "When persisted CSR fails to parse it should return error without regenerating",
			setup: func(t *testing.T, mockRW *fileio.MockReadWriter, mockProvider *MockProvider) []byte {
				mockRW.EXPECT().PathExists(csrPath).Return(true, nil)
				mockRW.EXPECT().ReadFile(csrPath).Return([]byte("corrupt-data"), nil)
				return nil
			},
			wantErr: "validating persisted CSR",
		},
		{
			name: "When CSR generation fails it should return error",
			setup: func(t *testing.T, mockRW *fileio.MockReadWriter, mockProvider *MockProvider) []byte {
				mockRW.EXPECT().PathExists(csrPath).Return(false, nil)
				mockProvider.EXPECT().GenerateCSR(deviceName).Return(nil, errors.New("keygen failed"))
				return nil
			},
			wantErr: "generating CSR",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			mockRW := fileio.NewMockReadWriter(ctrl)
			mockProvider := NewMockProvider(ctrl)
			logger := log.NewPrefixLogger("test")

			expectedCSR := tc.setup(t, mockRW, mockProvider)

			csr, err := ResolveCSR(mockRW, dataDir, mockProvider, deviceName, logger)
			if tc.wantErr != "" {
				require.Error(err)
				require.ErrorContains(err, tc.wantErr)
			} else {
				require.NoError(err)
				require.Equal(expectedCSR, csr)
			}
		})
	}
}
