package util

import (
	"net"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/test/util/testdb"
)

// IntegrationRedisHost returns the Redis hostname for integration tests (discovered from
// published ports on flightctl-integration-redis when running; otherwise localhost).
func IntegrationRedisHost() string {
	return testdb.IntegrationRedisHost()
}

// IntegrationRedisPort returns the Redis port for integration tests.
func IntegrationRedisPort() uint {
	return testdb.IntegrationRedisPort()
}

// IntegrationRedisPassword returns the Redis password for integration tests.
func IntegrationRedisPassword() domain.SecureString {
	return testdb.IntegrationRedisPassword()
}

// IntegrationRedisAddr returns host:port for Redis.
func IntegrationRedisAddr() string {
	return net.JoinHostPort(IntegrationRedisHost(), strconv.FormatUint(uint64(IntegrationRedisPort()), 10))
}
