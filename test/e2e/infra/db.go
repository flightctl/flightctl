package infra

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// ParseDBParamsFromAPIConfig parses database connection parameters from the raw YAML
// content of the API service config (the "database" block). It applies defaults for
// port (5432) and dbname (flightctl), and validates that hostname is present.
// Provider-specific credential fields (user, password) are left empty if absent —
// each provider fills them from its own secret source.
func ParseDBParamsFromAPIConfig(raw string) (DBConnectionParams, error) {
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		return DBConnectionParams{}, fmt.Errorf("ParseDBParamsFromAPIConfig: parse config YAML: %w", err)
	}

	params := DBConnectionParams{
		Port:   "5432",
		DBName: "flightctl",
	}

	if db, ok := root["database"].(map[string]interface{}); ok {
		if v, ok := db["hostname"].(string); ok && v != "" {
			params.Hostname = v
		}
		if v, ok := db["port"]; ok {
			params.Port = fmt.Sprintf("%v", v)
		}
		if v, ok := db["name"].(string); ok && v != "" {
			params.DBName = v
		}
		if v, ok := db["user"].(string); ok && v != "" {
			params.User = v
		}
		if v, ok := db["password"].(string); ok && v != "" {
			params.Password = v
		}
		if v, ok := db["sslmode"].(string); ok {
			params.SSLMode = v
		}
		if v, ok := db["sslrootcert"].(string); ok {
			params.SSLRootCert = v
		}
		if v, ok := db["sslcert"].(string); ok {
			params.SSLCert = v
		}
		if v, ok := db["sslkey"].(string); ok {
			params.SSLKey = v
		}
	}

	if params.Hostname == "" {
		return DBConnectionParams{}, fmt.Errorf("ParseDBParamsFromAPIConfig: database.hostname not found in API config")
	}

	return params, nil
}

// QueryDB executes a psql query against the flightctl PostgreSQL database and returns
// the trimmed output.
//
// When the built-in flightctl-db pod/container is available (Helm db.type=builtin or
// Quadlet db.type=builtin), the query is executed via psql inside that pod using
// ExecInService. When the built-in workload is absent (external DB), the query is
// delegated to InfraProvider.QueryDBExternal, which uses a deployment-specific
// mechanism (a short-lived K8s Job or a direct pgx connection from the test binary).
func QueryDB(p *Providers, sql string) (string, error) {
	if p == nil || p.Infra == nil {
		return "", fmt.Errorf("QueryDB: providers not initialized")
	}
	if p.Infra.BuiltinDatabaseWorkloadAvailable() {
		output, err := p.Infra.ExecInService(ServiceDB, []string{
			"psql", "-d", "flightctl", "-t", "-A", "-c", sql,
		})
		if err != nil {
			return "", fmt.Errorf("psql query failed: %w; output: %s", err, strings.TrimSpace(output))
		}
		return strings.TrimSpace(output), nil
	}
	return p.Infra.QueryDBExternal(sql)
}
