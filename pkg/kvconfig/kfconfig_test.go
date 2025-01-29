package kvconfig

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
	tests := []struct {
		name        string
		cfg         *KvConfig
		expectedErr error
	}{
		{
			name: "config without certs",
			cfg: &KvConfig{
				Hostname: "localhost",
				Port:     6379,
				Password: "secret",
				DB:       1,
			},
		},
		{
			name: "config with ca cert file",
			cfg: &KvConfig{
				Hostname:   "localhost",
				Port:       6379,
				Password:   "secret",
				CaCertFile: "testdata/ca.crt",
				DB:         2,
			},
		},
		{
			name: "config with ca cert and client certs",
			cfg: &KvConfig{
				Hostname:   "localhost",
				Port:       6379,
				Password:   "secret",
				CaCertFile: "testdata/ca.crt",
				CertFile:   "testdata/client.crt",
				KeyFile:    "testdata/client.key",
				DB:         3,
			},
		},
		{
			name: "invalid CA cert file",
			cfg: &KvConfig{
				Hostname:   "localhost",
				Port:       6379,
				CaCertFile: "testdata/nonexistent.crt",
			},
			expectedErr: os.ErrNotExist,
		},
		{
			name: "invalid client cert file",
			cfg: &KvConfig{
				Hostname:   "localhost",
				Port:       6379,
				CaCertFile: "testdata/ca.crt",
				CertFile:   "testdata/nonexistent.crt",
			},
			expectedErr: os.ErrNotExist,
		},
		{
			name: "invalid client key file",
			cfg: &KvConfig{
				Hostname:   "localhost",
				Port:       6379,
				CaCertFile: "testdata/ca.crt",
				CertFile:   "testdata/client.crt",
				KeyFile:    "testdata/nonexistent.crt",
			},
			expectedErr: os.ErrNotExist,
		},
	}

	// Create test certificate directory
	testDir := filepath.Join("testdata")
	err := os.MkdirAll(testDir, 0755)
	require.NoError(t, err)
	defer os.RemoveAll(testDir)

	// Create test CA and client certs
	ca, _, err := crypto.EnsureCA(filepath.Join(testDir, "ca.crt"), filepath.Join(testDir, "ca.key"), "", "ca", 2)
	require.NoError(t, err)
	_, _, err = ca.EnsureClientCertificate(filepath.Join(testDir, "client.crt"), filepath.Join(testDir, "client.key"), "test-client", 2)
	require.NoError(t, err)

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
