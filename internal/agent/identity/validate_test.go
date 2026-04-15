package identity

import (
	"crypto"
	"testing"

	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/stretchr/testify/require"
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
	}{
		{
			name:       "When CN matches device name it should succeed",
			csrBytes:   makeCSR(t, "device-abc123"),
			deviceName: "device-abc123",
		},
		{
			name:       "When CN does not match device name it should return error",
			csrBytes:   makeCSR(t, "old-device-name"),
			deviceName: "new-device-name",
			wantErr:    `persisted CSR CN "old-device-name" does not match device name "new-device-name"`,
		},
		{
			name:       "When CN is empty it should return error",
			csrBytes:   makeCSR(t, ""),
			deviceName: "device-abc123",
			wantErr:    `persisted CSR CN "" does not match device name "device-abc123"`,
		},
		{
			name:       "When CSR bytes are empty it should return error",
			csrBytes:   []byte{},
			deviceName: "device-name",
			wantErr:    "parsing CSR",
		},
		{
			name:       "When CSR bytes are invalid it should return error",
			csrBytes:   []byte("not-a-valid-csr"),
			deviceName: "device-name",
			wantErr:    "parsing CSR",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			err := ValidateCSRIdentity(tc.csrBytes, tc.deviceName)
			if tc.wantErr != "" {
				require.ErrorContains(err, tc.wantErr)
			} else {
				require.NoError(err)
			}
		})
	}
}
