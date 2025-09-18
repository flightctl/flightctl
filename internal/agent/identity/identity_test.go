package identity

import (
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestFileProvider_WipeCertificateOnly(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock dependencies
	mockRW := fileio.NewMockReadWriter(ctrl)
	logger := log.NewPrefixLogger("test")

	// Create file provider
	provider := &fileProvider{
		clientCertPath: "/tmp/test/client.crt",
		clientKeyPath:  "/tmp/test/client.key",
		rw:             mockRW,
		log:            logger,
	}

	t.Run("wipes certificate file successfully", func(t *testing.T) {
		// Mock the file operations
		mockRW.EXPECT().OverwriteAndWipe("/tmp/test/client.crt").Return(nil)

		// Call WipeCertificateOnly
		err := provider.WipeCertificateOnly()

		// Verify success
		require.NoError(err)
	})

	t.Run("handles certificate file wipe error", func(t *testing.T) {
		expectedError := errors.New("file operation failed")

		// Mock the file operations to return an error
		mockRW.EXPECT().OverwriteAndWipe("/tmp/test/client.crt").Return(expectedError)

		// Call WipeCertificateOnly
		err := provider.WipeCertificateOnly()

		// Verify error is returned
		require.Error(err)
		require.Contains(err.Error(), "failed to wipe certificate")
	})

	t.Run("handles empty certificate path", func(t *testing.T) {
		// Create provider with empty certificate path
		provider := &fileProvider{
			clientCertPath: "", // Empty path
			clientKeyPath:  "/tmp/test/client.key",
			rw:             mockRW,
			log:            logger,
		}

		// Call WipeCertificateOnly
		err := provider.WipeCertificateOnly()

		// Should succeed without doing anything
		require.NoError(err)
	})
}

func TestTpmProvider_WipeCertificateOnly(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock dependencies
	logger := log.NewPrefixLogger("test")

	// Create TPM provider
	provider := &tpmProvider{
		certificateData: []byte("test-certificate-data"),
		log:             logger,
	}

	t.Run("wipes certificate data from memory successfully", func(t *testing.T) {
		// Call WipeCertificateOnly
		err := provider.WipeCertificateOnly()

		// Verify success
		require.NoError(err)

		// Verify certificate data is cleared
		require.Nil(provider.certificateData)
	})

	t.Run("handles nil certificate data", func(t *testing.T) {
		// Create provider with nil certificate data
		provider := &tpmProvider{
			certificateData: nil, // Already nil
			log:             logger,
		}

		// Call WipeCertificateOnly
		err := provider.WipeCertificateOnly()

		// Should succeed without doing anything
		require.NoError(err)
		require.Nil(provider.certificateData)
	})
}
