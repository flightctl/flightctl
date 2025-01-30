package kv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/stretchr/testify/require"
)

func TestConfigToRedisOptions(t *testing.T) {
	testDir := t.TempDir()
	require := require.New(t)

	caCertFile := filepath.Join(testDir, "ca.crt")
	caKeyFile := filepath.Join(testDir, "ca.key")
	clientCertFile := filepath.Join(testDir, "client.crt")
	clientKeyFile := filepath.Join(testDir, "client.key")

	// Create test CA and client certs
	ca, _, err := crypto.EnsureCA(caCertFile, caKeyFile, "", "ca", 2)
	require.NoError(err)
	_, _, err = ca.EnsureClientCertificate(clientCertFile, clientKeyFile, "test-client", 2)
	require.NoError(err)

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
				Username: "test-kv",
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
				Username:   "test-kv",
				CaCertFile: caCertFile,
				DB:         2,
			},
		},
		{
			name: "config with ca cert and client certs",
			cfg: &Config{
				Hostname:   "localhost",
				Port:       6379,
				Username:   "test-kv",
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
				require.ErrorIs(err, testCase.expectedErr)
				return
			}
			require.NoError(err)

			require.Equal(fmt.Sprintf("%s:%d", testCase.cfg.Hostname, testCase.cfg.Port), options.Addr)
			require.Equal(testCase.cfg.DB, options.DB)
			require.Equal(testCase.cfg.Password, options.Password)
			require.Equal(testCase.cfg.DB, options.DB)

			if testCase.cfg.CaCertFile != "" {
				require.NotNil(options.TLSConfig.RootCAs)

				if testCase.cfg.CertFile != "" {
					require.NotEmpty(options.TLSConfig.Certificates)
				}
			}
		})
	}
}
