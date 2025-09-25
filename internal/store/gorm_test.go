package store

import (
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestCreateDSN(t *testing.T) {

	tests := []struct {
		name     string
		setupCfg func() *config.Config
		user     string
		password config.SecureString
		expected string
	}{
		{
			name: "basic DSN without database name",
			setupCfg: func() *config.Config {
				cfg := config.NewDefault()
				cfg.Database.Hostname = "localhost"
				cfg.Database.Port = 5432
				cfg.Database.Name = ""
				cfg.Database.SSLMode = ""
				cfg.Database.SSLCert = ""
				cfg.Database.SSLKey = ""
				cfg.Database.SSLRootCert = ""
				return cfg
			},
			user:     "testuser",
			password: config.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432",
		},
		{
			name: "DSN with database name",
			setupCfg: func() *config.Config {
				cfg := config.NewDefault()
				cfg.Database.Hostname = "localhost"
				cfg.Database.Port = 5432
				cfg.Database.Name = "testdb"
				cfg.Database.SSLMode = ""
				cfg.Database.SSLCert = ""
				cfg.Database.SSLKey = ""
				cfg.Database.SSLRootCert = ""
				return cfg
			},
			user:     "testuser",
			password: config.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 dbname=testdb",
		},
		{
			name: "DSN with SSL mode",
			setupCfg: func() *config.Config {
				cfg := config.NewDefault()
				cfg.Database.Hostname = "localhost"
				cfg.Database.Port = 5432
				cfg.Database.Name = "testdb"
				cfg.Database.SSLMode = "require"
				cfg.Database.SSLCert = ""
				cfg.Database.SSLKey = ""
				cfg.Database.SSLRootCert = ""
				return cfg
			},
			user:     "testuser",
			password: config.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 dbname=testdb sslmode=require",
		},
		{
			name: "DSN with all SSL parameters",
			setupCfg: func() *config.Config {
				cfg := config.NewDefault()
				cfg.Database.Hostname = "localhost"
				cfg.Database.Port = 5432
				cfg.Database.Name = "testdb"
				cfg.Database.SSLMode = "verify-full"
				cfg.Database.SSLCert = "/path/to/client.crt"
				cfg.Database.SSLKey = "/path/to/client.key"
				cfg.Database.SSLRootCert = "/path/to/ca.crt"
				return cfg
			},
			user:     "testuser",
			password: config.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 dbname=testdb sslmode=verify-full sslcert=/path/to/client.crt sslkey=/path/to/client.key sslrootcert=/path/to/ca.crt",
		},
		{
			name: "DSN with partial SSL parameters",
			setupCfg: func() *config.Config {
				cfg := config.NewDefault()
				cfg.Database.Hostname = "localhost"
				cfg.Database.Port = 5432
				cfg.Database.Name = ""
				cfg.Database.SSLMode = "require"
				cfg.Database.SSLCert = ""
				cfg.Database.SSLKey = ""
				cfg.Database.SSLRootCert = "/path/to/ca.crt"
				return cfg
			},
			user:     "testuser",
			password: config.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 sslmode=require sslrootcert=/path/to/ca.crt",
		},
		{
			name: "DSN with empty SSL parameters (should be ignored)",
			setupCfg: func() *config.Config {
				cfg := config.NewDefault()
				cfg.Database.Hostname = "localhost"
				cfg.Database.Port = 5432
				cfg.Database.Name = "testdb"
				cfg.Database.SSLMode = ""
				cfg.Database.SSLCert = ""
				cfg.Database.SSLKey = ""
				cfg.Database.SSLRootCert = ""
				return cfg
			},
			user:     "testuser",
			password: config.SecureString("testpass"),
			expected: "host=localhost user=testuser password=testpass port=5432 dbname=testdb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.setupCfg()
			result := createDSN(cfg, tt.user, tt.password)
			assert.Equal(t, tt.expected, result)
		})
	}
}
