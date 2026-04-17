package e2e

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/util"
)

const (
	localPortTimeout = 15 * time.Second
	dbPortDefault    = 5432
)

// BackupRestore performs backup/restore operations. Create with Harness.NewBackupRestore.
type BackupRestore struct {
	*Harness
	providers       *infra.Providers
	dbSecretName    string
	dbUserKey       string
	dbPasswordKey   string
	adminSecretName string
	adminSecretKey  string
	kvSecretName    string
	kvSecretKey     string
}

// NewBackupRestore returns a BackupRestore that uses this harness and the given providers and secret names/keys.
// If p is nil, setup.GetDefaultProviders() is used.
func (h *Harness) NewBackupRestore(
	p *infra.Providers,
	dbSecretName, dbUserKey, dbPasswordKey,
	adminSecretName, adminSecretKey, kvSecretName, kvSecretKey string,
) *BackupRestore {
	if p == nil {
		p = setup.GetDefaultProviders()
	}
	return &BackupRestore{
		Harness:         h,
		providers:       p,
		dbSecretName:    dbSecretName,
		dbUserKey:       dbUserKey,
		dbPasswordKey:   dbPasswordKey,
		adminSecretName: adminSecretName,
		adminSecretKey:  adminSecretKey,
		kvSecretName:    kvSecretName,
		kvSecretKey:     kvSecretKey,
	}
}

// ScaleDownFlightCtlServices scales down API, worker, periodic, alert-exporter, alertmanager-proxy (DB and KV stay up).
func (br *BackupRestore) ScaleDownFlightCtlServices() error {
	services := []infra.ServiceName{
		infra.ServiceAPI,
		infra.ServiceWorker,
		infra.ServicePeriodic,
		infra.ServiceAlertExporter,
		infra.ServiceAlertmanagerProxy,
	}
	for _, svc := range services {
		if err := br.providers.Lifecycle.Stop(svc); err != nil {
			return fmt.Errorf("scale down %s: %w", svc, err)
		}
	}
	return nil
}

// ScaleUpFlightCtlServices scales up the same deployments to 1 replica.
func (br *BackupRestore) ScaleUpFlightCtlServices() error {
	services := []infra.ServiceName{
		infra.ServiceAPI,
		infra.ServiceWorker,
		infra.ServicePeriodic,
		infra.ServiceAlertExporter,
		infra.ServiceAlertmanagerProxy,
	}
	for _, svc := range services {
		if err := br.providers.Lifecycle.Start(svc); err != nil {
			return fmt.Errorf("scale up %s: %w", svc, err)
		}
	}
	for _, svc := range services {
		if err := br.providers.Lifecycle.WaitForReady(svc, 2*time.Minute); err != nil {
			return fmt.Errorf("wait for ready %s: %w", svc, err)
		}
	}
	return nil
}

// getDBPort returns the DB service port (for use in exec pg_dump/psql; inside container).
func (br *BackupRestore) getDBPort() int {
	_, port, err := br.providers.Infra.GetServiceEndpoint(infra.ServiceDB)
	if err != nil {
		return dbPortDefault
	}
	if port <= 0 {
		return dbPortDefault
	}
	return port
}

// CreateDBBackup runs pg_dump inside the database pod/container via infra and saves the output locally.
func (br *BackupRestore) CreateDBBackup() (string, func(), error) {
	dbUser, err := br.providers.Infra.GetSecretValue(br.dbSecretName, br.dbUserKey)
	if err != nil {
		return "", func() {}, err
	}
	dbPassword, err := br.providers.Infra.GetSecretValue(br.dbSecretName, br.dbPasswordKey)
	if err != nil {
		return "", func() {}, err
	}

	dbPort := br.getDBPort()
	command := []string{
		"env", "PGPASSWORD=" + dbPassword,
		"pg_dump", "-h", "127.0.0.1", "-p", strconv.Itoa(dbPort), "-U", dbUser, "-d", "flightctl",
	}
	output, err := br.providers.Infra.ExecInService(infra.ServiceDB, command)
	if err != nil {
		return "", func() {}, fmt.Errorf("pg_dump: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "flightctl-backup-")
	if err != nil {
		return "", func() {}, err
	}
	backupPath := filepath.Join(tmpDir, "flightctl-backup.sql")
	if err := os.WriteFile(backupPath, []byte(output), 0o600); err != nil {
		os.RemoveAll(tmpDir)
		return "", func() {}, fmt.Errorf("write backup file: %w", err)
	}

	cleanup := func() { os.RemoveAll(tmpDir) }
	return backupPath, cleanup, nil
}

// RestoreDBFromBackup runs psql inside the database pod/container to drop, recreate, and restore the flightctl database.
func (br *BackupRestore) RestoreDBFromBackup(backupPath string) error {
	adminPassword, err := br.providers.Infra.GetSecretValue(br.adminSecretName, br.adminSecretKey)
	if err != nil {
		return fmt.Errorf("get admin secret: %w", err)
	}

	dbPort := br.getDBPort()
	psqlCmd := func(args ...string) error {
		cmd := append([]string{"env", "PGPASSWORD=" + adminPassword, "psql", "-h", "127.0.0.1", "-p", strconv.Itoa(dbPort), "-U", "admin"}, args...)
		_, err := br.providers.Infra.ExecInService(infra.ServiceDB, cmd)
		return err
	}

	if err := psqlCmd("-d", "postgres", "-c", "DROP DATABASE IF EXISTS flightctl WITH (FORCE);"); err != nil {
		return err
	}
	if err := psqlCmd("-d", "postgres", "-c", "CREATE DATABASE flightctl OWNER flightctl_app;"); err != nil {
		return err
	}

	backupFile, err := os.Open(backupPath)
	if err != nil {
		return fmt.Errorf("open backup file: %w", err)
	}
	defer backupFile.Close()

	restoreCmd := []string{
		"env", "PGPASSWORD=" + adminPassword, "psql", "-h", "127.0.0.1", "-p", strconv.Itoa(dbPort), "-U", "admin", "-d", "flightctl",
	}
	_, err = br.providers.Infra.ExecInServiceWithStdin(infra.ServiceDB, restoreCmd, backupFile)
	if err != nil {
		return fmt.Errorf("psql restore from stdin: %w", err)
	}
	return nil
}

// RunFlightCtlRestore runs the flightctl-restore binary with ExposeService for DB and KV.
func (br *BackupRestore) RunFlightCtlRestore() error {
	restoreBinary := br.GetFlightctlRestorePath()
	if _, err := os.Stat(restoreBinary); os.IsNotExist(err) {
		return fmt.Errorf("binary not found: %s (build with: make build-restore)", restoreBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	dbURL, dbCleanup, err := br.providers.Infra.ExposeService(infra.ServiceDB, "tcp")
	if err != nil {
		return fmt.Errorf("expose DB: %w", err)
	}
	defer dbCleanup()

	kvURL, kvCleanup, err := br.providers.Infra.ExposeService(infra.ServiceRedis, "tcp")
	if err != nil {
		return fmt.Errorf("expose KV: %w", err)
	}
	defer kvCleanup()

	dbHost, dbPort, err := parseHostPort(dbURL)
	if err != nil {
		return fmt.Errorf("parse DB URL %q: %w", dbURL, err)
	}
	kvHost, kvPort, err := parseHostPort(kvURL)
	if err != nil {
		return fmt.Errorf("parse KV URL %q: %w", kvURL, err)
	}

	if err := waitForLocalPort(ctx, dbPort); err != nil {
		return err
	}
	if err := waitForLocalPort(ctx, kvPort); err != nil {
		return err
	}

	dbPassword, err := br.providers.Infra.GetSecretValue(br.dbSecretName, br.dbPasswordKey)
	if err != nil {
		return err
	}
	kvPassword, err := br.providers.Infra.GetSecretValue(br.kvSecretName, br.kvSecretKey)
	if err != nil {
		return err
	}

	configDir, err := os.MkdirTemp("", "flightctl-restore-config-")
	if err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	defer os.RemoveAll(configDir)
	configPath := filepath.Join(configDir, ".flightctl", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create .flightctl dir: %w", err)
	}
	configYAML := fmt.Sprintf(`database:
  type: pgsql
  hostname: %s
  port: %d
  name: flightctl
  user: flightctl_app
  password: %q
kv:
  hostname: %s
  port: %d
  password: %q
`, dbHost, dbPort, dbPassword, kvHost, kvPort, kvPassword)
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	homeDir := configDir

	restoreCmd := exec.CommandContext(ctx, restoreBinary)
	restoreCmd.Env = append(os.Environ(), "HOME="+homeDir)
	if out, err := restoreCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("flightctl-restore: %w: %s", err, string(out))
	}
	return nil
}

// parseHostPort parses a URL like "tcp://127.0.0.1:54321" and returns host, port, error.
func parseHostPort(rawURL string) (host string, port int, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", 0, err
	}
	host = u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	portStr := u.Port()
	if portStr == "" {
		return "", 0, fmt.Errorf("no port in URL %q", rawURL)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func waitForLocalPort(ctx context.Context, port int) error {
	deadline := time.NewTimer(localPortTimeout)
	defer deadline.Stop()

	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timeout waiting for 127.0.0.1:%d", port)
		case <-tick.C:
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}
	}
}
