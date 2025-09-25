package identity

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"text/template"

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
	mockRW := fileio.NewMockReadWriter(ctrl)

	t.Run("wipes certificate data from memory and file successfully", func(t *testing.T) {
		// Create TPM provider with proper setup
		provider := &tpmProvider{
			certificateData: []byte("test-certificate-data"),
			clientCertPath:  "/path/to/client.crt",
			rw:              mockRW,
			log:             logger,
		}

		// Expect file wipe to be called
		mockRW.EXPECT().OverwriteAndWipe("/path/to/client.crt").Return(nil)

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
			clientCertPath:  "/path/to/client.crt",
			rw:              mockRW,
			log:             logger,
		}

		// Expect file wipe to be called even if certificate data is nil
		mockRW.EXPECT().OverwriteAndWipe("/path/to/client.crt").Return(nil)

		// Call WipeCertificateOnly
		err := provider.WipeCertificateOnly()

		// Should succeed
		require.NoError(err)
		require.Nil(provider.certificateData)
	})

	t.Run("returns error when client cert path is empty", func(t *testing.T) {
		// Create provider without client cert path
		provider := &tpmProvider{
			certificateData: []byte("test-certificate-data"),
			clientCertPath:  "", // Empty path
			rw:              mockRW,
			log:             logger,
		}

		// Call WipeCertificateOnly
		err := provider.WipeCertificateOnly()

		// Should return error
		require.Error(err)
		require.Contains(err.Error(), "client certificate path is not set")

		// Certificate data should still be cleared
		require.Nil(provider.certificateData)
	})
}

func TestCredentialTemplateRendering(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		serviceName string
		credName    string
		sealedPath  string
		envVarName  string
		validate    func(t *testing.T, output string)
	}{
		{
			name:        "parent service credential",
			serviceName: "flightctl-agent",
			credName:    "tpm-storage-password",
			sealedPath:  "/etc/flightctl/credentials/tpm-storage-password.sealed",
			envVarName:  "TPM_STORAGE_PASSWORD_FILE",
			validate: func(t *testing.T, output string) {
				require.Contains(output, "[Service]")
				require.Contains(output, "configuration for flightctl-agent")
				require.Contains(output, "LoadCredentialEncrypted=tpm-storage-password:/etc/flightctl/credentials/tpm-storage-password.sealed")
				require.Contains(output, "Environment=\"TPM_STORAGE_PASSWORD_FILE=%d/tpm-storage-password\"")
				require.Contains(output, "%d/tpm-storage-password")
				require.NotContains(output, "{{")
				require.NotContains(output, "}}")
			},
		},
		{
			name:        "child service credential",
			serviceName: "my-app",
			credName:    "my-app-password",
			sealedPath:  "/etc/flightctl/credentials/children/my-app.sealed",
			envVarName:  "TPM_CHILD_PASSWORD_FILE",
			validate: func(t *testing.T, output string) {
				require.Contains(output, "[Service]")
				require.Contains(output, "configuration for my-app")
				require.Contains(output, "LoadCredentialEncrypted=my-app-password:/etc/flightctl/credentials/children/my-app.sealed")
				require.Contains(output, "Environment=\"TPM_CHILD_PASSWORD_FILE=%d/my-app-password\"")
				require.Contains(output, "%d/my-app-password")
				require.NotContains(output, "{{")
				require.NotContains(output, "}}")
			},
		},
		{
			name:        "special characters in service name",
			serviceName: "my-app-123.service",
			credName:    "my-app-123.service-password",
			sealedPath:  "/etc/flightctl/credentials/children/my-app-123.service.sealed",
			envVarName:  "TPM_CHILD_PASSWORD_FILE",
			validate: func(t *testing.T, output string) {
				require.Contains(output, "[Service]")
				require.Contains(output, "configuration for my-app-123.service")
				require.Contains(output, "LoadCredentialEncrypted=my-app-123.service-password:")
				require.Contains(output, "Environment=\"TPM_CHILD_PASSWORD_FILE=%d/my-app-123.service-password\"")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := template.New("dropin").Parse(credentialTemplate)
			require.NoError(err, "Template should parse successfully")

			data := struct {
				ServiceName    string
				CredentialName string
				SealedPath     string
				EnvVarName     string
			}{
				ServiceName:    tt.serviceName,
				CredentialName: tt.credName,
				SealedPath:     tt.sealedPath,
				EnvVarName:     tt.envVarName,
			}

			var buf bytes.Buffer
			err = tmpl.Execute(&buf, data)
			require.NoError(err, "Template execution should succeed")

			output := buf.String()

			require.NotEmpty(output, "Output should not be empty")
			require.True(strings.HasPrefix(output, "#"), "Output should start with comment")

			tt.validate(t, output)
		})
	}
}

func TestCredentialTemplateStructure(t *testing.T) {
	require := require.New(t)

	tmpl, err := template.New("dropin").Parse(credentialTemplate)
	require.NoError(err, "Template must parse without errors")

	data := struct {
		ServiceName    string
		CredentialName string
		SealedPath     string
		EnvVarName     string
	}{
		ServiceName:    "test-service",
		CredentialName: "test-cred",
		SealedPath:     "/test/path.sealed",
		EnvVarName:     "TEST_VAR",
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	require.NoError(err)

	output := buf.String()

	require.Contains(output, "[Service]", "Must contain [Service] section")
	require.Contains(output, "LoadCredentialEncrypted=", "Must contain LoadCredentialEncrypted directive")
	require.Contains(output, "Environment=", "Must contain Environment directive")

	// verify the %d specifier is present (not replaced or mangled)
	require.Contains(output, "%d/", "Must preserve systemd %d specifier")

	require.NotContains(output, "{{", "All template placeholders should be replaced")
	require.NotContains(output, "}}", "All template placeholders should be replaced")

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "Environment=") {
			require.Contains(line, "\"", "Environment value should be quoted")
		}
	}
}
