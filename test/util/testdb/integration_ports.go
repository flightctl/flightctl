package testdb

import (
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	"github.com/sirupsen/logrus"
)

func integrationPostgresPublished() (host string, port uint, ok bool) {
	return integrationstack.PublishedTCPPort(integrationstack.PostgresContainerName, "5432/tcp")
}

// ApplyIntegrationConnectionOverrides points DB and Alertmanager (when present in cfg) at
// published ports for the integration stack when flightctl-integration-postgres is running.
// If that container is absent, cfg is unchanged (e.g. unit tests using localhost defaults).
// Note: KV (Redis) is NOT configured here - each test suite creates its own ephemeral Redis
// via CreateTestRedis() and passes connection params explicitly.
func ApplyIntegrationConnectionOverrides(cfg *config.Config) {
	h, p, ok := integrationPostgresPublished()
	if !ok {
		return
	}
	cfg.Database.Hostname = h
	cfg.Database.Port = p

	if cfg.Alertmanager != nil {
		ah, ap, ok := integrationstack.PublishedTCPPort(integrationstack.AlertmanagerContainerName, "9093/tcp")
		if !ok {
			logrus.Fatalf("integration Alertmanager container %q is not running or has no published port 9093/tcp (start with: make start-integration-services)", integrationstack.AlertmanagerContainerName)
		}
		cfg.Alertmanager.Hostname = ah
		cfg.Alertmanager.Port = ap
	}
}

// IntegrationRedisPassword returns the Redis password for integration tests.
// Reads KV_PASSWORD, then FLIGHTCTL_KV_PASSWORD (same as make integration-test), else adminpass
// to match test/integration/integrationstack Redis --requirepass.
func IntegrationRedisPassword() domain.SecureString {
	if p := strings.TrimSpace(os.Getenv("KV_PASSWORD")); p != "" {
		return domain.SecureString(p)
	}
	if p := strings.TrimSpace(os.Getenv("FLIGHTCTL_KV_PASSWORD")); p != "" {
		return domain.SecureString(p)
	}
	return domain.SecureString("adminpass")
}

// IntegrationPostgresAdminUser returns "postgres" (the admin user for test infrastructure ops
// like CREATE/DROP DATABASE). Production app/migrator users don't have CREATEDB privilege.
func IntegrationPostgresAdminUser() string {
	return "postgres"
}

// IntegrationPostgresAdminPassword returns the postgres admin password for integration tests.
// Reads FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD (same as integrationstack), else adminpass.
func IntegrationPostgresAdminPassword() domain.SecureString {
	if p := strings.TrimSpace(os.Getenv("FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD")); p != "" {
		return domain.SecureString(p)
	}
	return domain.SecureString("adminpass")
}
