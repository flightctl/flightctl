package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_String_ObfuscatesSensitiveData(t *testing.T) {
	cfg := &Config{
		Database: &dbConfig{
			Type:              "pgsql",
			Hostname:          "localhost",
			Port:              5432,
			Name:              "testdb",
			User:              "testuser",
			Password:          "secretpassword",
			MigrationUser:     "migrator",
			MigrationPassword: "migrationsecret",
		},
		KV: &kvConfig{
			Hostname: "redis-host",
			Port:     6379,
			Password: "redispassword",
		},
	}

	result := cfg.String()

	// Verify sensitive data is redacted
	if strings.Contains(result, "secretpassword") {
		t.Error("Database password should be redacted")
	}
	if strings.Contains(result, "migrationsecret") {
		t.Error("Migration password should be redacted")
	}
	if strings.Contains(result, "redispassword") {
		t.Error("KV password should be redacted")
	}

	// Verify redaction markers are present
	if !strings.Contains(result, "[REDACTED]") {
		t.Error("String() should contain [REDACTED] markers")
	}

	// Verify non-sensitive data is preserved
	if !strings.Contains(result, "localhost") {
		t.Error("Non-sensitive hostname should be preserved")
	}
	if !strings.Contains(result, "testuser") {
		t.Error("Non-sensitive username should be preserved")
	}
}

func TestValidateTrustedProxies(t *testing.T) {
	t.Run("ValidCIDRs", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Service.TrustedProxies = []string{
			"192.168.1.0/24",
			"10.0.0.0/8",
			"2001:db8::/32",
		}

		err := Validate(cfg)
		assert.NoError(t, err)
	})

	t.Run("ValidIPs", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Service.TrustedProxies = []string{
			"192.168.1.100",
			"10.0.0.1",
			"2001:db8::1",
			"::1",
		}

		err := Validate(cfg)
		assert.NoError(t, err)
	})

	t.Run("MixedValidEntries", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Service.TrustedProxies = []string{
			"192.168.1.0/24",
			"10.0.0.1",
			"2001:db8::/32",
			"::1",
		}

		err := Validate(cfg)
		assert.NoError(t, err)
	})

	t.Run("EmptyEntries", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Service.TrustedProxies = []string{
			"",
			"   ",
			"192.168.1.100",
			"\t",
			"10.0.0.1",
		}

		err := Validate(cfg)
		assert.NoError(t, err)
	})

	t.Run("SingleInvalidEntry", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Service.TrustedProxies = []string{
			"192.168.1.100",
			"invalid-ip",
			"10.0.0.1",
		}

		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid TrustedProxies entry: invalid-ip")
		assert.Contains(t, err.Error(), "must be a valid CIDR block or IP address")
	})

	t.Run("MultipleInvalidEntries", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Service.TrustedProxies = []string{
			"192.168.1.100",
			"invalid-ip",
			"not-a-cidr",
			"10.0.0.1",
			"bad-format",
		}

		err := Validate(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid TrustedProxies entries:")
		assert.Contains(t, err.Error(), "invalid-ip, not-a-cidr, bad-format")
		assert.Contains(t, err.Error(), "must be valid CIDR blocks or IP addresses")
	})

	t.Run("NoTrustedProxies", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Service.TrustedProxies = []string{}

		err := Validate(cfg)
		assert.NoError(t, err)
	})

	t.Run("NilTrustedProxies", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Service.TrustedProxies = nil

		err := Validate(cfg)
		assert.NoError(t, err)
	})

	t.Run("NilService", func(t *testing.T) {
		cfg := NewDefault()
		cfg.Service = nil

		err := Validate(cfg)
		assert.NoError(t, err)
	})
}
