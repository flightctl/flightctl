package util

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/test/util/testdb"
)

// IntegrationRedisPassword returns the Redis password for integration tests.
// Note: IntegrationRedisHost/Port/Addr functions have been removed because there is no longer
// a shared Redis container. Each test suite should create its own ephemeral Redis using
// testdb.CreateTestRedis() and use the returned host/port directly.
func IntegrationRedisPassword() domain.SecureString {
	return testdb.IntegrationRedisPassword()
}
