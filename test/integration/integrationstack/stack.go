// Package integrationstack starts or stops the named Postgres, Redis, and Alertmanager
// testcontainers used by integration tests. Host ports are assigned by the runtime (ephemeral);
// tests resolve them via PublishedTCPPort which queries podman/docker port.
package integrationstack

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Container names for integration stack services.
const (
	PostgresContainerName     = "flightctl-integration-postgres"
	RedisContainerName        = "flightctl-integration-redis"
	AlertmanagerContainerName = "flightctl-integration-alertmanager"
)

const (
	postgresImage     = "docker.io/library/postgres:16-alpine"
	redisImage        = "docker.io/library/redis:7-alpine"
	alertmanagerImage = "docker.io/prom/alertmanager:v0.27.0"
	// defaultIntegrationPassword matches test/test.mk when integration env vars are unset (e.g. go run preflight alone).
	defaultIntegrationPassword = "adminpass"
)

const alertmanagerYAML = `
route:
  receiver: default
receivers:
  - name: default
`

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func integrationStackAlreadyRunning() bool {
	for _, n := range []string{PostgresContainerName, RedisContainerName, AlertmanagerContainerName} {
		if !containers.ContainerRunningByName(n) {
			return false
		}
	}
	return true
}

// PublishedTCPPort resolves the host-published TCP port for a named container.
func PublishedTCPPort(containerName, containerTCPPort string) (host string, port uint, found bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, cli := range []string{"docker", "podman"} {
		//nolint:gosec // G204: cli is docker|podman; name/port are fixed integration constants.
		cmd := exec.CommandContext(ctx, cli, "port", containerName, containerTCPPort)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		h, p, ok := parseHostPort(string(out))
		if ok {
			return h, p, true
		}
	}
	return "", 0, false
}

func parseHostPort(output string) (host string, port uint, ok bool) {
	line := strings.TrimSpace(output)
	if line == "" {
		return "", 0, false
	}
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	lastColon := strings.LastIndex(line, ":")
	if lastColon <= 0 || lastColon >= len(line)-1 {
		return "", 0, false
	}
	hostRaw := strings.TrimSpace(line[:lastColon])
	portStr := strings.TrimSpace(line[lastColon+1:])
	p64, err := strconv.ParseUint(portStr, 10, 32)
	if err != nil {
		return "", 0, false
	}
	hostRaw = strings.Trim(hostRaw, "[]")
	switch hostRaw {
	case "0.0.0.0", "::":
		host = "127.0.0.1"
	default:
		host = hostRaw
	}
	return host, uint(p64), true
}

// integrationStackTCPReachable is true when host-published ports for the integration
// Postgres, Redis, and Alertmanager containers accept a TCP connection.
func integrationStackTCPReachable() bool {
	probes := []struct {
		name string
		spec string
	}{
		{PostgresContainerName, "5432/tcp"},
		{RedisContainerName, "6379/tcp"},
		{AlertmanagerContainerName, "9093/tcp"},
	}
	for _, p := range probes {
		h, prt, ok := PublishedTCPPort(p.name, p.spec)
		if !ok {
			return false
		}
		addr := net.JoinHostPort(h, strconv.FormatUint(uint64(prt), 10))
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return false
		}
		_ = c.Close()
	}
	return true
}

func inspectPostgresMasterPassword(ctx context.Context) (string, bool) {
	cli := containers.RuntimeCLIName()
	sub, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	//nolint:gosec // G204: cli is docker|podman; container name is a package constant.
	cmd := exec.CommandContext(sub, cli, "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", PostgresContainerName)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		const pfx = "POSTGRES_PASSWORD="
		if strings.HasPrefix(line, pfx) {
			return strings.TrimPrefix(line, pfx), true
		}
	}
	return "", false
}

func inspectRedisRequirepass(ctx context.Context) (string, bool) {
	cli := containers.RuntimeCLIName()
	sub, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	//nolint:gosec // G204: cli is docker|podman; container name is a package constant.
	cmd := exec.CommandContext(sub, cli, "inspect", "-f", "{{json .Config.Cmd}}", RedisContainerName)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	var argv []string
	if err := json.Unmarshal(out, &argv); err != nil {
		return "", false
	}
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--requirepass" {
			return argv[i+1], true
		}
	}
	return "", false
}

// integrationStackCredentialMismatch is true when Postgres/Redis are up but env passwords differ from
// running container config (inspect), or inspect failed — caller should recreate the stack.
func integrationStackCredentialMismatch(ctx context.Context, postgresMaster, redisPass string) bool {
	if !containers.ContainerRunningByName(PostgresContainerName) || !containers.ContainerRunningByName(RedisContainerName) {
		return false
	}
	pm, ok1 := inspectPostgresMasterPassword(ctx)
	rp, ok2 := inspectRedisRequirepass(ctx)
	if !ok1 || !ok2 {
		return true
	}
	return pm != postgresMaster || rp != redisPass
}

// EnsureRunning starts Postgres, Redis, and Alertmanager with reuse if they are not already running,
// then runs database migrations using the flightctl-db-migrate binary (same code path as production).
// If all three containers are running and Postgres/Redis credentials match FLIGHTCTL_* env, skips container start.
// Migrations are always run (idempotent via schema_migrations table).
// If credentials differ from running containers, removes them so init SQL and Redis requirepass apply.
func EnsureRunning(ctx context.Context) error {
	if err := ensureContainersRunning(ctx); err != nil {
		return err
	}
	return RunMigrations(ctx)
}

// EnsureContainersOnly starts containers without running migrations.
// Use this when you need to run migrations separately (e.g., from Makefile).
func EnsureContainersOnly(ctx context.Context) error {
	return ensureContainersRunning(ctx)
}

func ensureContainersRunning(ctx context.Context) error {
	containers.ConfigureDockerHost()

	masterPW := envOrDefault("FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD", defaultIntegrationPassword)
	kvPW := envOrDefault("FLIGHTCTL_KV_PASSWORD", defaultIntegrationPassword)

	if integrationStackAlreadyRunning() {
		credMismatch := integrationStackCredentialMismatch(ctx, masterPW, kvPW)
		reachable := integrationStackTCPReachable()
		if !credMismatch && reachable {
			logrus.Info("Integration stack already running; skipping container start")
			return nil
		}
		if credMismatch {
			logrus.Warn("Integration stack credentials differ from environment (or inspect failed); removing containers")
		} else {
			logrus.Warn("Integration stack containers are running but services are not reachable on published ports; removing containers")
		}
		_ = Stop(ctx)
	}

	network := containers.GetDockerNetwork()
	reuse := true

	amDir, err := os.MkdirTemp("", "flightctl-integration-am-*")
	if err != nil {
		return fmt.Errorf("temp dir for alertmanager: %w", err)
	}
	defer func() { _ = os.RemoveAll(amDir) }()
	amPath := filepath.Join(amDir, "alertmanager.yml")
	if err := os.WriteFile(amPath, []byte(alertmanagerYAML), 0600); err != nil {
		return fmt.Errorf("write alertmanager config: %w", err)
	}

	// Start postgres with admin password and create the flightctl database.
	// User setup and migrations are done via setup_database_users.sql + flightctl-db-migrate (same as production).
	pgReq := testcontainers.ContainerRequest{
		Image:        postgresImage,
		Name:         PostgresContainerName,
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_PASSWORD": masterPW,
			"POSTGRES_DB":       "flightctl", // Same as production deployment
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(120 * time.Second),
		SkipReaper: reuse,
	}
	if _, err := containers.GenericStart(ctx, pgReq, reuse, containers.WithNetwork(network), containers.WithHostAccess()); err != nil {
		return fmt.Errorf("postgres container: %w", err)
	}
	logrus.Info("Postgres integration container is up")

	redisReq := testcontainers.ContainerRequest{
		Image:        redisImage,
		Name:         RedisContainerName,
		ExposedPorts: []string{"6379/tcp"},
		Cmd:          []string{"redis-server", "--requirepass", kvPW},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
		SkipReaper:   reuse,
	}
	if _, err := containers.GenericStart(ctx, redisReq, reuse, containers.WithNetwork(network), containers.WithHostAccess()); err != nil {
		return fmt.Errorf("redis container: %w", err)
	}
	logrus.Info("Redis integration container is up")

	amReq := testcontainers.ContainerRequest{
		Image:        alertmanagerImage,
		Name:         AlertmanagerContainerName,
		ExposedPorts: []string{"9093/tcp"},
		Cmd:          []string{"--config.file=/etc/alertmanager/alertmanager.yml", "--storage.path=/tmp/am"},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: amPath, ContainerFilePath: "/etc/alertmanager/alertmanager.yml", FileMode: 0644},
		},
		WaitingFor: wait.ForHTTP("/-/ready").WithPort("9093/tcp").WithStartupTimeout(60 * time.Second),
		SkipReaper: reuse,
	}
	if _, err := containers.GenericStart(ctx, amReq, reuse, containers.WithNetwork(network), containers.WithHostAccess()); err != nil {
		return fmt.Errorf("alertmanager container: %w", err)
	}
	logrus.Info("Alertmanager integration container is up")
	return nil
}

// Stop removes integration containers by name (best effort for each).
func Stop(_ context.Context) error {
	for _, name := range []string{
		AlertmanagerContainerName,
		RedisContainerName,
		PostgresContainerName,
	} {
		if err := containers.RemoveContainerByName(name); err != nil {
			logrus.Warnf("remove %s: %v", name, err)
		}
	}
	return nil
}

// RunMigrations sets up database users and runs migrations against the integration Postgres container.
// This matches the exact production flow used by Helm and Quadlet deployments:
// 1. Run setup_database_users.sql to create both migrator and app users with privileges
// 2. Run flightctl-db-migrate binary to apply schema migrations
// The process is idempotent - users are created only if they don't exist, and migrations use
// the schema_migrations table to skip already-applied migrations.
// Requires: psql (postgresql-client), envsubst, and flightctl-db-migrate binary on $PATH or in bin/.
func RunMigrations(ctx context.Context) error {
	h, p, ok := PublishedTCPPort(PostgresContainerName, "5432/tcp")
	if !ok {
		return fmt.Errorf("postgres container %q is not running or has no published port 5432/tcp", PostgresContainerName)
	}

	if _, err := exec.LookPath("psql"); err != nil {
		return fmt.Errorf("psql not found: install postgresql-client (e.g., 'dnf install postgresql' or 'apt install postgresql-client')")
	}

	if _, err := exec.LookPath("envsubst"); err != nil {
		return fmt.Errorf("envsubst not found: install gettext (e.g., 'dnf install gettext' or 'apt install gettext')")
	}

	sqlPath, err := findSetupDatabaseUsersSQL()
	if err != nil {
		return err
	}

	migrateBinary, err := findMigrateBinary()
	if err != nil {
		return err
	}

	masterPW := envOrDefault("FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD", defaultIntegrationPassword)
	migratorPW := envOrDefault("FLIGHTCTL_POSTGRESQL_MIGRATOR_PASSWORD", defaultIntegrationPassword)
	appUserPW := envOrDefault("FLIGHTCTL_POSTGRESQL_USER_PASSWORD", defaultIntegrationPassword)

	// Environment variables for setup_database_users.sql (same as production Helm/Quadlets)
	env := append(os.Environ(),
		fmt.Sprintf("DB_HOST=%s", h),
		fmt.Sprintf("DB_PORT=%d", p),
		"DB_NAME=flightctl",
		"DB_ADMIN_USER=postgres",
		fmt.Sprintf("DB_ADMIN_PASSWORD=%s", masterPW),
		"DB_MIGRATION_USER=flightctl_migrator",
		fmt.Sprintf("DB_MIGRATION_PASSWORD=%s", migratorPW),
		"DB_APP_USER=flightctl_app",
		fmt.Sprintf("DB_APP_PASSWORD=%s", appUserPW),
	)

	// Step 1: Run setup_database_users.sql (matching production Helm/Quadlet flow)
	logrus.Infof("Setting up database users via setup_database_users.sql against %s:%d", h, p)
	if err := runSetupDatabaseUsers(ctx, sqlPath, h, p, masterPW, env); err != nil {
		return err
	}

	// Step 2: Run flightctl-db-migrate binary (same as production)
	logrus.Info("Running flightctl-db-migrate...")
	if err := runMigrateBinary(ctx, migrateBinary, env); err != nil {
		return err
	}

	logrus.Info("Database setup and migrations completed successfully")
	return nil
}

// runSetupDatabaseUsers runs envsubst on the SQL file and pipes to psql (matching production).
func runSetupDatabaseUsers(ctx context.Context, sqlPath, host string, port uint, adminPW string, env []string) error {
	sub, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Create a temp file for the substituted SQL (same approach as Helm/Quadlets)
	tmpFile, err := os.CreateTemp("", "setup_database_users_*.sql")
	if err != nil {
		return fmt.Errorf("create temp SQL file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Run envsubst to substitute environment variables in the SQL file
	envsubstCmd := exec.CommandContext(sub, "envsubst")
	envsubstCmd.Env = env
	sqlContent, err := os.ReadFile(sqlPath)
	if err != nil {
		return fmt.Errorf("read SQL file: %w", err)
	}
	envsubstCmd.Stdin = strings.NewReader(string(sqlContent))
	substitutedSQL, err := envsubstCmd.Output()
	if err != nil {
		return fmt.Errorf("envsubst failed: %w", err)
	}
	if err := os.WriteFile(tmpPath, substitutedSQL, 0600); err != nil {
		return fmt.Errorf("write substituted SQL: %w", err)
	}

	// Run psql with the substituted SQL file (matching production exactly)
	//nolint:gosec // G204: arguments are from controlled integration test environment
	psqlCmd := exec.CommandContext(sub, "psql",
		"-v", "ON_ERROR_STOP=1",
		"-h", host,
		"-p", strconv.FormatUint(uint64(port), 10),
		"-U", "postgres",
		"-d", "flightctl",
		"-f", tmpPath,
	)
	psqlCmd.Env = append(env, fmt.Sprintf("PGPASSWORD=%s", adminPW))
	psqlCmd.Stdout = os.Stdout
	psqlCmd.Stderr = os.Stderr

	if err := psqlCmd.Run(); err != nil {
		return fmt.Errorf("setup_database_users.sql failed: %w", err)
	}
	return nil
}

// runMigrateBinary executes the flightctl-db-migrate binary.
func runMigrateBinary(ctx context.Context, binaryPath string, env []string) error {
	sub, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	//nolint:gosec // G204: binaryPath is resolved from known locations within the repository
	cmd := exec.CommandContext(sub, binaryPath)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flightctl-db-migrate failed: %w", err)
	}
	return nil
}

// findSetupDatabaseUsersSQL locates deploy/scripts/setup_database_users.sql relative to repository root.
func findSetupDatabaseUsersSQL() (string, error) {
	candidates := []string{}

	if repoRoot := findRepoRoot(); repoRoot != "" {
		candidates = append(candidates, filepath.Join(repoRoot, "deploy", "scripts", "setup_database_users.sql"))
	}

	candidates = append(candidates,
		"deploy/scripts/setup_database_users.sql",
		"./deploy/scripts/setup_database_users.sql",
	)

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, err := filepath.Abs(c)
			if err != nil {
				return c, nil
			}
			return abs, nil
		}
	}

	return "", fmt.Errorf("deploy/scripts/setup_database_users.sql not found (are you running from the repository root?)")
}

// findMigrateBinary locates the flightctl-db-migrate binary.
// Checks bin/flightctl-db-migrate relative to repository root (found via go.mod),
// then current directory, then $PATH.
func findMigrateBinary() (string, error) {
	candidates := []string{}

	// Try to find repository root by looking for go.mod
	if repoRoot := findRepoRoot(); repoRoot != "" {
		candidates = append(candidates, filepath.Join(repoRoot, "bin", "flightctl-db-migrate"))
	}

	// Also check relative to current directory
	candidates = append(candidates,
		"bin/flightctl-db-migrate",
		"./bin/flightctl-db-migrate",
	)

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, err := filepath.Abs(c)
			if err != nil {
				return c, nil
			}
			return abs, nil
		}
	}

	path, err := exec.LookPath("flightctl-db-migrate")
	if err == nil {
		return path, nil
	}

	return "", fmt.Errorf("flightctl-db-migrate binary not found (build with: make build-db-migrate)")
}

// findRepoRoot finds the repository root by looking for go.mod in parent directories.
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
