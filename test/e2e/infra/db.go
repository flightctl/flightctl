package infra

import (
	"fmt"
	"strings"
)

// QueryDB executes a psql query against the built-in flightctl PostgreSQL database
// and returns the trimmed output.
//
// This requires a reachable flightctl-db pod/container (the built-in deployment).
// It will fail on external-database deployments where no such pod exists.
// Callers that run in mixed environments should guard with
// Providers.Infra.BuiltinDatabaseWorkloadAvailable() before calling.
func QueryDB(p *Providers, sql string) (string, error) {
	if p == nil || p.Infra == nil {
		return "", fmt.Errorf("QueryDB: providers not initialized")
	}
	output, err := p.Infra.ExecInService(ServiceDB, []string{
		"psql", "-d", "flightctl", "-t", "-A", "-c", sql,
	})
	if err != nil {
		return "", fmt.Errorf("psql query failed: %w; output: %s", err, strings.TrimSpace(output))
	}
	return strings.TrimSpace(output), nil
}
