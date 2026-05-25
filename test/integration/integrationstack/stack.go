// Package integrationstack starts or stops the named Postgres and Alertmanager
// testcontainers used by integration tests. Host ports are assigned by the runtime (ephemeral);
// tests resolve them via PublishedTCPPort which queries podman/docker port.
// Redis is NOT part of the shared stack - each test suite creates its own ephemeral Redis
// via testdb.CreateTestRedis() to enable parallel test execution.
package integrationstack

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/migration"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Container names for integration stack services.
// Note: Redis is NOT part of the shared stack - each test suite creates its own ephemeral Redis.
const (
	PostgresContainerName     = "flightctl-integration-postgres"
	AlertmanagerContainerName = "flightctl-integration-alertmanager"
)

const (
	postgresImage     = "docker.io/library/postgres:16-alpine"
	alertmanagerImage = "docker.io/prom/alertmanager:v0.27.0"
	// defaultIntegrationPassword matches test/test.mk when integration env vars are unset (e.g. go run preflight alone).
	defaultIntegrationPassword = "adminpass"
	// migrationSentinelPath stores the database ID after successful migrations to skip redundant runs.
	migrationSentinelPath = "/tmp/flightctl-integration-migrations.done"
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

// getFlightctlDatabaseID returns a unique identifier for the flightctl database instance.
// It combines the cluster's system_identifier with the database OID to ensure uniqueness:
// - system_identifier changes when the cluster is reinitialized (new container)
// - database OID changes when the database is dropped and recreated within the same cluster
func getFlightctlDatabaseID(ctx context.Context) (string, bool) {
	h, p, ok := PublishedTCPPort(PostgresContainerName, "5432/tcp")
	if !ok {
		return "", false
	}

	masterPW := envOrDefault("FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD", defaultIntegrationPassword)

	sub, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	//nolint:gosec // G204: arguments are from controlled integration test environment
	cmd := exec.CommandContext(sub, "psql",
		"-h", h,
		"-p", strconv.FormatUint(uint64(p), 10),
		"-U", "postgres",
		"-d", "postgres",
		"-t", "-A",
		"-c", "SELECT system_identifier || ':' || (SELECT oid FROM pg_database WHERE datname = 'flightctl') FROM pg_control_system()")
	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", masterPW))

	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(out))
	if id == "" || strings.HasSuffix(id, ":") {
		return "", false
	}
	return id, true
}

// migrationsSentinelValid returns true if the sentinel exists AND matches the current flightctl database ID.
// This ensures migrations are re-run if the container was recreated OR the database was dropped/recreated.
func migrationsSentinelValid(ctx context.Context) bool {
	data, err := os.ReadFile(migrationSentinelPath)
	if err != nil {
		return false
	}
	savedID := strings.TrimSpace(string(data))
	currentID, ok := getFlightctlDatabaseID(ctx)
	if !ok {
		return false
	}
	return savedID == currentID
}

// createMigrationsSentinel creates the sentinel file with the current flightctl database ID.
func createMigrationsSentinel(ctx context.Context) error {
	id, ok := getFlightctlDatabaseID(ctx)
	if !ok {
		return fmt.Errorf("could not get flightctl database ID")
	}
	return os.WriteFile(migrationSentinelPath, []byte(id), 0600)
}

func integrationStackAlreadyRunning() bool {
	for _, n := range []string{PostgresContainerName, AlertmanagerContainerName} {
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
// Postgres and Alertmanager containers accept a TCP connection.
func integrationStackTCPReachable() bool {
	probes := []struct {
		name string
		spec string
	}{
		{PostgresContainerName, "5432/tcp"},
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

// integrationStackCredentialMismatch is true when Postgres is up but env password differs from
// running container config (inspect), or inspect failed — caller should recreate the stack.
func integrationStackCredentialMismatch(ctx context.Context, postgresMaster string) bool {
	if !containers.ContainerRunningByName(PostgresContainerName) {
		return false
	}
	pm, ok := inspectPostgresMasterPassword(ctx)
	if !ok {
		return true
	}
	return pm != postgresMaster
}

// EnsureRunning starts Postgres and Alertmanager with reuse if they are not already running,
// then runs database migrations using the flightctl-db-migrate binary (same code path as production).
// If both containers are running and Postgres credentials match FLIGHTCTL_* env, skips container start.
// Migrations are run with a file lock to prevent parallel test suites from deadlocking on CREATE INDEX.
// If credentials differ from running containers, removes them so init SQL applies.
// Note: Redis is NOT started here - each test suite creates its own ephemeral Redis via testdb.CreateTestRedis().
func EnsureRunning(ctx context.Context) error {
	if err := ensureContainersRunning(ctx); err != nil {
		return err
	}
	return RunMigrationsWithLock(ctx)
}

// EnsureContainersOnly starts containers without running migrations.
// Use this when you need to run migrations separately (e.g., from Makefile).
func EnsureContainersOnly(ctx context.Context) error {
	return ensureContainersRunning(ctx)
}

func ensureContainersRunning(ctx context.Context) error {
	containers.ConfigureDockerHost()

	masterPW := envOrDefault("FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD", defaultIntegrationPassword)

	if integrationStackAlreadyRunning() {
		credMismatch := integrationStackCredentialMismatch(ctx, masterPW)
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
// Also removes the migrations sentinel so the next run will re-migrate.
func Stop(_ context.Context) error {
	// Remove migrations sentinel so next run will re-migrate
	_ = os.Remove(migrationSentinelPath)

	for _, name := range []string{
		AlertmanagerContainerName,
		PostgresContainerName,
	} {
		if err := containers.RemoveContainerByName(name); err != nil {
			logrus.Warnf("remove %s: %v", name, err)
		}
	}
	return nil
}

// RunMigrationsWithLock runs migrations with a file lock to prevent parallel test suites
// from deadlocking when they all try to run CREATE INDEX statements simultaneously.
// The lock file is stored in /tmp and is automatically released when the process exits.
// A sentinel file tracks which database instance has been migrated to avoid redundant runs.
func RunMigrationsWithLock(ctx context.Context) error {
	// Fast path: check sentinel BEFORE acquiring lock
	// Sentinel is only valid if it matches the current database ID (system_identifier:oid)
	if migrationsSentinelValid(ctx) {
		logrus.Info("Migrations already completed for this database; skipping")
		return nil
	}

	lockPath := "/tmp/flightctl-integration-migrations.lock"

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open migration lock file: %w", err)
	}
	defer lockFile.Close()

	logrus.Debug("Acquiring migration lock...")
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	}()
	logrus.Debug("Migration lock acquired")

	// Double-check after acquiring lock (another proc may have completed migrations while we waited)
	if migrationsSentinelValid(ctx) {
		logrus.Info("Migrations completed by another process while waiting for lock; skipping")
		return nil
	}

	// Run migrations
	if err := RunMigrations(ctx); err != nil {
		return err
	}

	// Create sentinel with current database ID (system_identifier:oid)
	if err := createMigrationsSentinel(ctx); err != nil {
		logrus.Warnf("Failed to create migrations sentinel: %v", err)
	}
	return nil
}

// RunMigrations sets up database users and runs migrations against the integration Postgres container.
// This matches the exact production flow used by Helm and Quadlet deployments:
// 1. Run setup_database_users.sql to create both migrator and app users with privileges
// 2. Run flightctl-db-migrate to apply schema migrations
// The process is idempotent - users are created only if they don't exist, and migrations use
// the schema_migrations table to skip already-applied migrations.
// Requires: psql (postgresql-client) and envsubst.
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

	// Step 2: Run database migrations using the same code path as flightctl-db-migrate
	logrus.Info("Running database migrations...")
	if err := runMigrate(ctx, h, p, "flightctl_migrator", migratorPW); err != nil {
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

// runMigrate runs database migrations using the same code path as flightctl-db-migrate.
func runMigrate(ctx context.Context, host string, port uint, migrationUser, migrationPassword string) error {
	cfg := config.NewDefault()
	cfg.Database.Hostname = host
	cfg.Database.Port = port
	cfg.Database.MigrationUser = migrationUser
	cfg.Database.MigrationPassword = api.SecureString(migrationPassword)

	log := logrus.StandardLogger()

	migrationDB, err := store.InitMigrationDB(cfg, log)
	if err != nil {
		return fmt.Errorf("initializing migration database: %w", err)
	}
	defer func() {
		if sqlDB, err := migrationDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	return migration.Run(ctx, migrationDB, log, false)
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
