package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/util"
)

const (
	localPortTimeout = 15 * time.Second
)

// BackupRestore performs backup/restore operations. Create with Harness.NewBackupRestore.
type BackupRestore struct {
	*Harness
	cliCommand      string
	externalNS      string
	internalNS      string
	dbService       string
	kvService       string
	dbPort          int
	kvPort          int
	dbSecretName    string
	dbUserKey       string
	dbPasswordKey   string
	adminSecretName string
	adminSecretKey  string
	kvSecretName    string
	kvSecretKey     string
}

// NewBackupRestore returns a BackupRestore that uses this harness and the given parameters.
func (h *Harness) NewBackupRestore(
	cliCommand, externalNS, internalNS, dbService, kvService,
	dbSecretName, dbUserKey, dbPasswordKey,
	adminSecretName, adminSecretKey, kvSecretName, kvSecretKey string,
	dbPort, kvPort int,
) *BackupRestore {
	return &BackupRestore{
		Harness:         h,
		cliCommand:      cliCommand,
		externalNS:      externalNS,
		internalNS:      internalNS,
		dbService:       dbService,
		kvService:       kvService,
		dbPort:          dbPort,
		kvPort:          kvPort,
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
	deployments := []struct{ ns, name string }{
		{br.externalNS, "flightctl-api"},
		{br.internalNS, "flightctl-worker"},
		{br.internalNS, "flightctl-periodic"},
		{br.internalNS, "flightctl-alert-exporter"},
		{br.externalNS, "flightctl-alertmanager-proxy"},
	}
	for _, d := range deployments {
		// #nosec G204
		cmd := exec.Command(br.cliCommand, "scale", "deployment", d.name, "-n", d.ns, "--replicas=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("scale down %s/%s: %w: %s", d.ns, d.name, err, string(out))
		}
	}
	for _, d := range deployments {
		// Wait for the deployment to observe the scaled state before restore starts.
		// #nosec G204
		wait := exec.Command(br.cliCommand, "rollout", "status", "deployment", d.name, "-n", d.ns, "--timeout=2m")
		if out, err := wait.CombinedOutput(); err != nil {
			return fmt.Errorf("wait scale down %s/%s: %w: %s", d.ns, d.name, err, string(out))
		}
	}
	return nil
}

// ScaleUpFlightCtlServices scales up the same deployments to 1 replica.
func (br *BackupRestore) ScaleUpFlightCtlServices() error {
	deployments := []struct{ ns, name string }{
		{br.externalNS, "flightctl-api"},
		{br.internalNS, "flightctl-worker"},
		{br.internalNS, "flightctl-periodic"},
		{br.internalNS, "flightctl-alert-exporter"},
		{br.externalNS, "flightctl-alertmanager-proxy"},
	}
	for _, d := range deployments {
		// #nosec G204
		cmd := exec.Command(br.cliCommand, "scale", "deployment", d.name, "-n", d.ns, "--replicas=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("scale up %s/%s: %w: %s", d.ns, d.name, err, string(out))
		}
	}
	return nil
}

// dbPodLabel is the label selector for the database server pod (see deploy/helm/.../flightctl-db-deployment.yaml and deploy/podman/flightctl-db/flightctl-db.container).
const dbPodLabel = "flightctl.service=flightctl-db"

// getDBPodName returns the name of the database server pod in the given namespace.
// It selects by label flightctl.service=flightctl-db so we get the DB pod only, not flightctl-db-migrate.
// Using exec from inside the pod avoids pg_dump/psql version mismatch with the server.
func (br *BackupRestore) getDBPodName(ctx context.Context) (string, error) {
	// #nosec G204
	cmd := exec.CommandContext(ctx, br.cliCommand, "get", "pods", "-n", br.internalNS, "-l", dbPodLabel, "-o", "jsonpath={.items[0].metadata.name}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s get pods -l %s: %w: %s", br.cliCommand, dbPodLabel, err, string(out))
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "", fmt.Errorf("no pod with label %s found in namespace %s", dbPodLabel, br.internalNS)
	}
	return name, nil
}

// CreateDBBackup runs pg_dump inside the database pod via kubectl/oc exec and saves the output locally.
// This avoids version mismatch between local pg_dump and the database server; no port-forward is needed.
func (br *BackupRestore) CreateDBBackup() (string, func(), error) {
	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	podName, err := br.getDBPodName(ctx)
	if err != nil {
		return "", func() {}, err
	}

	dbUser, err := br.getSecretDataDecoded(br.dbSecretName, br.internalNS, br.dbUserKey)
	if err != nil {
		return "", func() {}, err
	}
	dbPassword, err := br.getSecretDataDecoded(br.dbSecretName, br.internalNS, br.dbPasswordKey)
	if err != nil {
		return "", func() {}, err
	}

	tmpDir, err := os.MkdirTemp("", "flightctl-backup-")
	if err != nil {
		return "", func() {}, err
	}
	backupPath := filepath.Join(tmpDir, "flightctl-backup.sql")
	backupFile, err := os.Create(backupPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", func() {}, fmt.Errorf("create backup file: %w", err)
	}

	// Run pg_dump inside the DB pod; connect to localhost so no port-forward is needed.
	// #nosec G204
	execCmd := exec.CommandContext(ctx, br.cliCommand, "exec", "-n", br.internalNS, podName, "--",
		"env", "PGPASSWORD="+dbPassword,
		"pg_dump", "-h", "127.0.0.1", "-p", fmt.Sprintf("%d", br.dbPort), "-U", dbUser, "-d", "flightctl")
	execCmd.Stdout = backupFile
	execCmd.Stderr = os.Stderr
	if err := execCmd.Run(); err != nil {
		backupFile.Close()
		os.RemoveAll(tmpDir)
		return "", func() {}, fmt.Errorf("pg_dump in pod %s: %w", podName, err)
	}
	if err := backupFile.Close(); err != nil {
		os.RemoveAll(tmpDir)
		return "", func() {}, fmt.Errorf("close backup file: %w", err)
	}

	cleanup := func() { os.RemoveAll(tmpDir) }
	return backupPath, cleanup, nil
}

// RestoreDBFromBackup runs psql inside the database pod to drop, recreate, and restore the flightctl database.
// The backup is piped to psql via stdin so the dump never has to be copied into the pod.
// Using exec in the pod avoids version mismatch between local psql and the database server; no port-forward is needed.
// Restore flow matches docs/user/installing/performing-database-restore.md (Kubernetes): scale down → restore DB → flightctl-restore → scale up.
func (br *BackupRestore) RestoreDBFromBackup(backupPath string) error {
	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	podName, err := br.getDBPodName(ctx)
	if err != nil {
		return err
	}

	adminPassword, err := br.getSecretDataDecoded(br.adminSecretName, br.internalNS, br.adminSecretKey)
	if err != nil {
		return fmt.Errorf("get admin secret: %w", err)
	}

	psqlBase := []string{"exec", "-n", br.internalNS, podName, "--", "env", "PGPASSWORD=" + adminPassword, "psql", "-h", "127.0.0.1", "-p", fmt.Sprintf("%d", br.dbPort), "-U", "admin"}
	runPsql := func(args ...string) error {
		// #nosec G204
		cmd := exec.CommandContext(ctx, br.cliCommand, append(psqlBase, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("psql %v: %w: %s", args, err, string(out))
		}
		return nil
	}

	if err := runPsql("-d", "postgres", "-c", "DROP DATABASE IF EXISTS flightctl WITH (FORCE);"); err != nil {
		return err
	}
	if err := runPsql("-d", "postgres", "-c", "CREATE DATABASE flightctl OWNER flightctl_app;"); err != nil {
		return err
	}

	// Restore from local backup by piping it to psql stdin; no need to copy the file into the pod.
	backupFile, err := os.Open(backupPath)
	if err != nil {
		return fmt.Errorf("open backup file: %w", err)
	}
	defer backupFile.Close()
	// -i attaches stdin to the exec so we can pipe the backup into psql
	restoreArgs := []string{"exec", "-n", br.internalNS, podName, "-i", "--", "env", "PGPASSWORD=" + adminPassword, "psql", "-h", "127.0.0.1", "-p", fmt.Sprintf("%d", br.dbPort), "-U", "admin", "-d", "flightctl"}
	// #nosec G204
	restoreCmd := exec.CommandContext(ctx, br.cliCommand, restoreArgs...)
	restoreCmd.Stdin = backupFile
	restoreCmd.Stderr = os.Stderr
	if out, err := restoreCmd.Output(); err != nil {
		return fmt.Errorf("psql restore from stdin: %w: %s", err, string(out))
	}
	return nil
}

// RunFlightCtlRestore runs the flightctl-restore binary with port-forwards to DB and KV.
// It writes a temporary config so DB/KV host and port match the port-forwards.
// The binary path is the same location as the CLI (harness.GetFlightctlRestorePath()).
func (br *BackupRestore) RunFlightCtlRestore() error {
	restoreBinary := br.GetFlightctlRestorePath()
	if _, err := os.Stat(restoreBinary); os.IsNotExist(err) {
		return fmt.Errorf("binary not found: %s (build with: make build-restore)", restoreBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	dbPortLocal, err := br.GetFreeLocalPort()
	if err != nil {
		return err
	}
	kvPortLocal, err := br.GetFreeLocalPort()
	if err != nil {
		return err
	}

	dbCmd, dbDone, err := br.StartPortForward(ctx, br.internalNS, br.dbService, dbPortLocal, br.dbPort)
	if err != nil {
		return err
	}
	defer func() { _ = dbCmd.Process.Kill() }()
	go func() { <-dbDone }()

	kvCmd, kvDone, err := br.StartPortForward(ctx, br.internalNS, br.kvService, kvPortLocal, br.kvPort)
	if err != nil {
		return err
	}
	defer func() { _ = kvCmd.Process.Kill() }()
	go func() { <-kvDone }()

	if err = waitForLocalPort(ctx, dbPortLocal); err != nil {
		return err
	}

	if err = waitForLocalPort(ctx, kvPortLocal); err != nil {
		return err
	}

	dbPassword, err := br.getSecretDataDecoded(br.dbSecretName, br.internalNS, br.dbPasswordKey)
	if err != nil {
		return err
	}
	kvPassword, err := br.getSecretDataDecoded(br.kvSecretName, br.internalNS, br.kvSecretKey)
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
  hostname: 127.0.0.1
  port: %d
  name: flightctl
  user: flightctl_app
  password: %q
kv:
  hostname: 127.0.0.1
  port: %d
  password: %q
`, dbPortLocal, dbPassword, kvPortLocal, kvPassword)
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

// getSecretDataDecoded runs kubectl get secret and returns the decoded value for key.
func (br *BackupRestore) getSecretDataDecoded(secretName, namespace, key string) (string, error) {
	// #nosec G204
	cmd := exec.Command(br.cliCommand, "get", "secret", secretName, "-n", namespace, "-o", "jsonpath={.data."+key+"}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl get secret: %w: %s", err, string(out))
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return "", fmt.Errorf("base64 decode secret %s: %w", key, err)
	}
	return string(decoded), nil
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
