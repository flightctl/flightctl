package store

import (
	"testing"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/config/common"
	"github.com/stretchr/testify/assert"
)

func TestCreateDSN(t *testing.T) {

	tests := []struct {
		name     string
		setupCfg func() *common.DatabaseConfig
		user     string
		password api.SecureString
		expected string
	}{
		{
			name: "basic DSN without database name",
			setupCfg: func() *common.DatabaseConfig {
				cfg := common.NewDefaultDatabase()
				cfg.Hostname = "localhost"
				cfg.Port = 5432
				cfg.Name = ""
				cfg.SSLMode = ""
				cfg.SSLCert = ""
				cfg.SSLKey = ""
				cfg.SSLRootCert = ""
				return cfg
			},
			user:     "testuser",
			password: api.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432",
		},
		{
			name: "DSN with database name",
			setupCfg: func() *common.DatabaseConfig {
				cfg := common.NewDefaultDatabase()
				cfg.Hostname = "localhost"
				cfg.Port = 5432
				cfg.Name = "testdb"
				cfg.SSLMode = ""
				cfg.SSLCert = ""
				cfg.SSLKey = ""
				cfg.SSLRootCert = ""
				return cfg
			},
			user:     "testuser",
			password: api.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 dbname=testdb",
		},
		{
			name: "DSN with SSL mode",
			setupCfg: func() *common.DatabaseConfig {
				cfg := common.NewDefaultDatabase()
				cfg.Hostname = "localhost"
				cfg.Port = 5432
				cfg.Name = "testdb"
				cfg.SSLMode = "require"
				cfg.SSLCert = ""
				cfg.SSLKey = ""
				cfg.SSLRootCert = ""
				return cfg
			},
			user:     "testuser",
			password: api.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 dbname=testdb sslmode=require",
		},
		{
			name: "DSN with all SSL parameters",
			setupCfg: func() *common.DatabaseConfig {
				cfg := common.NewDefaultDatabase()
				cfg.Hostname = "localhost"
				cfg.Port = 5432
				cfg.Name = "testdb"
				cfg.SSLMode = "verify-full"
				cfg.SSLCert = "/path/to/client.crt"
				cfg.SSLKey = "/path/to/client.key"
				cfg.SSLRootCert = "/path/to/ca.crt"
				return cfg
			},
			user:     "testuser",
			password: api.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 dbname=testdb sslmode=verify-full sslcert=/path/to/client.crt sslkey=/path/to/client.key sslrootcert=/path/to/ca.crt",
		},
		{
			name: "DSN with partial SSL parameters",
			setupCfg: func() *common.DatabaseConfig {
				cfg := common.NewDefaultDatabase()
				cfg.Hostname = "localhost"
				cfg.Port = 5432
				cfg.Name = ""
				cfg.SSLMode = "require"
				cfg.SSLCert = ""
				cfg.SSLKey = ""
				cfg.SSLRootCert = "/path/to/ca.crt"
				return cfg
			},
			user:     "testuser",
			password: api.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 sslmode=require sslrootcert=/path/to/ca.crt",
		},
		{
			name: "DSN with empty SSL parameters (should be ignored)",
			setupCfg: func() *common.DatabaseConfig {
				cfg := common.NewDefaultDatabase()
				cfg.Hostname = "localhost"
				cfg.Port = 5432
				cfg.Name = "testdb"
				cfg.SSLMode = ""
				cfg.SSLCert = ""
				cfg.SSLKey = ""
				cfg.SSLRootCert = ""
				return cfg
			},
			user:     "testuser",
			password: api.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 dbname=testdb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.setupCfg()
			result := cfg.CreateDSN(tt.user, tt.password)
			assert.Equal(t, tt.expected, result)
		})
	}
}
