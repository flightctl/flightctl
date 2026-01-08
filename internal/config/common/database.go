package common

import (
	"fmt"
	"os"

	api "github.com/flightctl/flightctl/api/v1beta1"
)

// DatabaseConfig holds database connection configuration.
type DatabaseConfig struct {
	Type     string           `json:"type,omitempty"`
	Hostname string           `json:"hostname,omitempty"`
	Port     uint             `json:"port,omitempty"`
	Name     string           `json:"name,omitempty"`
	User     string           `json:"user,omitempty"`
	Password api.SecureString `json:"password,omitempty"`
	// Migration user configuration for schema changes
	MigrationUser     string           `json:"migrationUser,omitempty"`
	MigrationPassword api.SecureString `json:"migrationPassword,omitempty"`
	// SSL configuration
	SSLMode     string `json:"sslmode,omitempty"`
	SSLCert     string `json:"sslcert,omitempty"`
	SSLKey      string `json:"sslkey,omitempty"`
	SSLRootCert string `json:"sslrootcert,omitempty"`
}

// NewDefaultDatabase returns a default database configuration.
func NewDefaultDatabase() *DatabaseConfig {
	return &DatabaseConfig{
		Type:              "pgsql",
		Hostname:          "localhost",
		Port:              5432,
		Name:              "flightctl",
		User:              "flightctl_app",
		Password:          "adminpass",
		MigrationUser:     "flightctl_migrator",
		MigrationPassword: "adminpass",
	}
}

// ApplyDefaults applies default values and environment variable overrides.
func (c *DatabaseConfig) ApplyDefaults() {
	if c == nil {
		return
	}
	c.ApplyEnvOverrides()
}

// ApplyEnvOverrides applies environment variable overrides to the database config.
func (c *DatabaseConfig) ApplyEnvOverrides() {
	if c == nil {
		return
	}
	if dbUser := os.Getenv("DB_USER"); dbUser != "" {
		c.User = dbUser
	}
	if dbPass := os.Getenv("DB_PASSWORD"); dbPass != "" {
		c.Password = api.SecureString(dbPass)
	}
	if dbMigrationUser := os.Getenv("DB_MIGRATION_USER"); dbMigrationUser != "" {
		c.MigrationUser = dbMigrationUser
	}
	if dbMigrationPass := os.Getenv("DB_MIGRATION_PASSWORD"); dbMigrationPass != "" {
		c.MigrationPassword = api.SecureString(dbMigrationPass)
	}
}

// CreateDSN creates a PostgreSQL data source name for the given user.
func (c *DatabaseConfig) CreateDSN(user string, password api.SecureString) string {
	dsn := fmt.Sprintf("host=%s user=%s password=%s port=%d",
		c.Hostname, user, string(password), c.Port)

	if c.Name != "" {
		dsn += fmt.Sprintf(" dbname=%s", c.Name)
	}
	if c.SSLMode != "" {
		dsn += fmt.Sprintf(" sslmode=%s", c.SSLMode)
	}
	if c.SSLCert != "" {
		dsn += fmt.Sprintf(" sslcert=%s", c.SSLCert)
	}
	if c.SSLKey != "" {
		dsn += fmt.Sprintf(" sslkey=%s", c.SSLKey)
	}
	if c.SSLRootCert != "" {
		dsn += fmt.Sprintf(" sslrootcert=%s", c.SSLRootCert)
	}

	return dsn
}
