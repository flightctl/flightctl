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
	ginkgo "github.com/onsi/ginkgo/v2"
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

// ScaleUpFlightCtlServices scales API, worker, periodic, alert-exporter, and alertmanager-proxy
// back up to 1 replica. Used as a safety net after restore in case the binary is killed before
// its own deferred startup.
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

// RunFlightCtlBackup runs the flightctl-backup binary. The binary auto-detects the deployment
// type via kubeconfig and connects to the database via the Kubernetes API (exec into DB pod).
// It writes backup artifacts to outputDir and returns the archive and checksum paths.
func (br *BackupRestore) RunFlightCtlBackup(outputDir string) (archivePath string, checksumPath string, err error) {
	backupBinary := br.GetFlightctlBackupPath()
	if _, statErr := os.Stat(backupBinary); os.IsNotExist(statErr) {
		return "", "", fmt.Errorf("binary not found: %s (build with: make flightctl-backup)", backupBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	args := []string{"--output", outputDir}
	if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
		args = append(args, "--namespace", ns)
	}
	if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
		args = append(args, "--internal-namespace", ns)
	}

	backupCmd := exec.CommandContext(ctx, backupBinary, args...)
	backupCmd.Stdout = ginkgo.GinkgoWriter
	backupCmd.Stderr = ginkgo.GinkgoWriter
	if err := backupCmd.Run(); err != nil {
		return "", "", fmt.Errorf("flightctl-backup failed: %w", err)
	}

	matches, err := filepath.Glob(filepath.Join(outputDir, "flightctl-backup-*.tar.gz"))
	if err != nil {
		return "", "", fmt.Errorf("glob backup archives: %w", err)
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no backup archive found in %s", outputDir)
	}

	archivePath = matches[0]
	checksumPath = archivePath + ".sha256"
	return archivePath, checksumPath, nil
}

// RunFlightCtlBackupRaw runs the flightctl-backup binary with the given arguments without
// setting up services or writing config. Useful for negative tests (bad flags, missing args, etc.).
func (br *BackupRestore) RunFlightCtlBackupRaw(args ...string) (output string, err error) {
	backupBinary := br.GetFlightctlBackupPath()
	if _, statErr := os.Stat(backupBinary); os.IsNotExist(statErr) {
		return "", fmt.Errorf("binary not found: %s (build with: make build-backup)", backupBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	cmd := exec.CommandContext(ctx, backupBinary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunFlightCtlRestoreRaw runs the flightctl-restore binary with the given arguments without
// setting up services or writing config. Useful for negative tests (bad args, corrupted archives, etc.).
func (br *BackupRestore) RunFlightCtlRestoreRaw(args ...string) (output string, err error) {
	restoreBinary := br.GetFlightctlRestorePath()
	if _, statErr := os.Stat(restoreBinary); os.IsNotExist(statErr) {
		return "", fmt.Errorf("binary not found: %s (build with: make flightctl-restore)", restoreBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	cmd := exec.CommandContext(ctx, restoreBinary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunFlightCtlRestoreFromArchive runs the flightctl-restore binary with the given archive path.
// The binary reads credentials from the cluster and handles service stop/start internally.
func (br *BackupRestore) RunFlightCtlRestoreFromArchive(archivePath string) error {
	restoreBinary := br.GetFlightctlRestorePath()
	if _, err := os.Stat(restoreBinary); os.IsNotExist(err) {
		return fmt.Errorf("binary not found: %s (build with: make flightctl-restore)", restoreBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	args := []string{archivePath}
	if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
		args = append(args, "--namespace", ns)
	}
	if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
		args = append(args, "--internal-namespace", ns)
	}

	restoreCmd := exec.CommandContext(ctx, restoreBinary, args...)
	restoreCmd.Stdout = ginkgo.GinkgoWriter
	restoreCmd.Stderr = ginkgo.GinkgoWriter
	if err := restoreCmd.Run(); err != nil {
		return fmt.Errorf("flightctl-restore failed: %w", err)
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
