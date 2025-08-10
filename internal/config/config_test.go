package config

import (
	"strings"
	"testing"
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
