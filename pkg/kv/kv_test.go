package kv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigToRedisOptions(t *testing.T) {
	testDir := t.TempDir()

	caCertFile := filepath.Join(testDir, "ca.crt")
	caKeyFile := filepath.Join(testDir, "ca.key")
	clientCertFile := filepath.Join(testDir, "client.crt")
	clientKeyFile := filepath.Join(testDir, "client.key")

	// Create test CA and client certs
	ca, _, err := crypto.EnsureCA(caCertFile, caKeyFile, "", "ca", 2)
	require.NoError(t, err)
	_, _, err = ca.EnsureClientCertificate(clientCertFile, clientKeyFile, "test-client", 2)
	require.NoError(t, err)

	tests := []struct {
		name        string
		cfg         *Config
		expectedErr error
	}{
		{
			name: "config without certs",
			cfg: &Config{
				Hostname: "localhost",
				Port:     6379,
				Password: "secret",
				DB:       1,
			},
		},
		{
			name: "config with ca cert file",
			cfg: &Config{
				Hostname:   "localhost",
				Port:       6379,
				Password:   "secret",
				CaCertFile: caCertFile,
				DB:         2,
			},
		},
		{
			name: "config with ca cert and client certs",
			cfg: &Config{
				Hostname:   "localhost",
				Port:       6379,
				Password:   "secret",
				CaCertFile: caCertFile,
				CertFile:   clientCertFile,
				KeyFile:    clientKeyFile,
				DB:         3,
			},
		},
		{
			name: "invalid CA cert file",
			cfg: &Config{
				Hostname:   "localhost",
				Port:       6379,
				CaCertFile: "testdata/nonexistent.crt",
			},
			expectedErr: os.ErrNotExist,
		},
		{
			name: "invalid client cert file",
			cfg: &Config{
				Hostname:   "localhost",
				Port:       6379,
				CaCertFile: caCertFile,
				CertFile:   "testdata/nonexistent.crt",
			},
			expectedErr: os.ErrNotExist,
		},
		{
			name: "invalid client key file",
			cfg: &Config{
				Hostname:   "localhost",
				Port:       6379,
				CaCertFile: caCertFile,
				CertFile:   clientCertFile,
				KeyFile:    "testdata/nonexistent.crt",
			},
			expectedErr: os.ErrNotExist,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			options, err := ConfigToRedisOptions(testCase.cfg)
			if testCase.expectedErr != nil {
				assert.ErrorIs(t, err, testCase.expectedErr)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, fmt.Sprintf("%s:%d", testCase.cfg.Hostname, testCase.cfg.Port), options.Addr)
			assert.Equal(t, testCase.cfg.Password, options.Password)
			assert.Equal(t, testCase.cfg.DB, options.DB)

			if testCase.cfg.CaCertFile != "" {
				assert.NotNil(t, options.TLSConfig.RootCAs)

				if testCase.cfg.CertFile != "" {
					assert.NotEmpty(t, options.TLSConfig.Certificates)
				}
			}
		})
	}
}
