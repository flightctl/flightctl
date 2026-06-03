package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/util"
	ginkgo "github.com/onsi/ginkgo/v2"
)

const (
	dbPortDefault = 5432
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

// isRemoteHost returns true when backup/restore commands must run on a remote host
// (remote quadlet deployment reachable via SSH).
func (br *BackupRestore) isRemoteHost() bool {
	return br.providers.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet &&
		os.Getenv("E2E_SSH_HOST") != "" && os.Getenv("E2E_SSH_USER") != ""
}

func (br *BackupRestore) getBackupBinaryPath() string {
	if br.isRemoteHost() {
		return "flightctl-backup"
	}
	return br.GetFlightctlBackupPath()
}

func (br *BackupRestore) getRestoreBinaryPath() string {
	if br.isRemoteHost() {
		return "flightctl-restore"
	}
	return br.GetFlightctlRestorePath()
}

// buildSCPArgs returns base SCP arguments for file transfer (mirrors quadlet SSH config).
func buildSCPArgs() []string {
	args := []string{"-o", "StrictHostKeyChecking=no"}
	keyPath := os.Getenv("E2E_SSH_KEY_PATH")
	password := os.Getenv("E2E_SSH_PASSWORD")
	if keyPath != "" {
		args = append(args, "-o", "BatchMode=yes", "-i", keyPath)
	} else if password == "" {
		args = append(args, "-o", "BatchMode=yes")
	}
	return args
}

func (br *BackupRestore) scpFrom(remotePath, localPath string) error {
	host := os.Getenv("E2E_SSH_HOST")
	user := os.Getenv("E2E_SSH_USER")
	scpArgs := buildSCPArgs()
	scpArgs = append(scpArgs, fmt.Sprintf("%s@%s:%s", user, host, remotePath), localPath)

	password := os.Getenv("E2E_SSH_PASSWORD")
	var cmd *exec.Cmd
	if os.Getenv("E2E_SSH_KEY_PATH") == "" && password != "" {
		cmd = exec.Command("sshpass", append([]string{"-e", "scp"}, scpArgs...)...) //nolint:gosec // G204: scp args from internal test config (host, user, key path)
		cmd.Env = append(os.Environ(), "SSHPASS="+password)
	} else {
		cmd = exec.Command("scp", scpArgs...) //nolint:gosec // G204,G702: scp args from internal test config
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp from %s:%s: %w: %s", host, remotePath, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (br *BackupRestore) scpTo(localPath, remotePath string) error {
	host := os.Getenv("E2E_SSH_HOST")
	user := os.Getenv("E2E_SSH_USER")
	scpArgs := buildSCPArgs()
	scpArgs = append(scpArgs, localPath, fmt.Sprintf("%s@%s:%s", user, host, remotePath))

	password := os.Getenv("E2E_SSH_PASSWORD")
	var cmd *exec.Cmd
	if os.Getenv("E2E_SSH_KEY_PATH") == "" && password != "" {
		cmd = exec.Command("sshpass", append([]string{"-e", "scp"}, scpArgs...)...) //nolint:gosec // G204: scp args from internal test config (host, user, key path)
		cmd.Env = append(os.Environ(), "SSHPASS="+password)
	} else {
		cmd = exec.Command("scp", scpArgs...) //nolint:gosec // G204,G702: scp args from internal test config
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp to %s:%s: %w: %s", host, remotePath, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (br *BackupRestore) createRemoteTempDir(prefix string) (string, error) {
	output, err := br.providers.Infra.RunOnHost([]string{"mktemp", "-d", "-t", prefix + "XXXXXX"})
	if err != nil {
		return "", fmt.Errorf("mktemp on host: %w", err)
	}
	return strings.TrimSpace(output), nil
}

func (br *BackupRestore) removeRemotePath(remotePath string) {
	_, _ = br.providers.Infra.RunOnHost([]string{"rm", "-rf", remotePath})
}

// replaceOutputDirForRemote scans args for --output and replaces any local directory
// with a remote temp directory. Returns the modified args.
func (br *BackupRestore) replaceOutputDirForRemote(args []string) []string {
	result := make([]string, len(args))
	copy(result, args)
	for i, arg := range result {
		if arg == "--output" && i+1 < len(result) {
			remoteTmp, err := br.createRemoteTempDir("flightctl-backup-")
			if err == nil {
				result[i+1] = remoteTmp
			}
			break
		}
	}
	return result
}

// uploadLocalFilesForRestore checks if any positional arg (non-flag) is a local file
// and uploads it (plus .sha256 sidecar) to a remote temp dir. Returns modified args
// and a cleanup function.
func (br *BackupRestore) uploadLocalFilesForRestore(args []string) ([]string, func(), error) {
	result := make([]string, len(args))
	copy(result, args)
	var remoteTmpDir string
	var cleanupNeeded bool

	for i, arg := range result {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if _, statErr := os.Stat(arg); statErr == nil {
			if remoteTmpDir == "" {
				var err error
				remoteTmpDir, err = br.createRemoteTempDir("flightctl-restore-")
				if err != nil {
					return nil, nil, fmt.Errorf("create remote temp dir: %w", err)
				}
				// createRemoteTempDir may run with sudo, creating a root-owned 0700 dir.
				// Make it writable so SCP (which runs without sudo) can upload files.
				if _, err := br.providers.Infra.RunOnHost([]string{"chmod", "777", remoteTmpDir}); err != nil {
					br.removeRemotePath(remoteTmpDir)
					return nil, nil, fmt.Errorf("chmod remote restore dir: %w", err)
				}
				cleanupNeeded = true
			}
			remotePath := remoteTmpDir + "/" + filepath.Base(arg)
			if err := br.scpTo(arg, remotePath); err != nil {
				br.removeRemotePath(remoteTmpDir)
				return nil, nil, fmt.Errorf("upload %s: %w", arg, err)
			}
			checksumPath := arg + ".sha256"
			if _, statErr := os.Stat(checksumPath); statErr == nil {
				remoteChecksum := remotePath + ".sha256"
				if err := br.scpTo(checksumPath, remoteChecksum); err != nil {
					br.removeRemotePath(remoteTmpDir)
					return nil, nil, fmt.Errorf("upload checksum for %s: %w", arg, err)
				}
			}
			result[i] = remotePath
		}
	}

	var cleanup func()
	if cleanupNeeded {
		dir := remoteTmpDir
		cleanup = func() { br.removeRemotePath(dir) }
	}
	return result, cleanup, nil
}

// RunFlightCtlBackup runs the flightctl-backup binary on the infrastructure host.
// The binary auto-detects the deployment type and writes backup artifacts to outputDir.
// For remote quadlet hosts, archives are created remotely then downloaded to the local outputDir.
func (br *BackupRestore) RunFlightCtlBackup(outputDir string) (archivePath string, checksumPath string, err error) {
	backupBinary := br.getBackupBinaryPath()

	if !br.isRemoteHost() {
		if _, statErr := os.Stat(backupBinary); os.IsNotExist(statErr) {
			return "", "", fmt.Errorf("binary not found: %s (build with: make flightctl-backup)", backupBinary)
		}
	}

	args := []string{backupBinary, "--output"}

	remoteOutputDir := outputDir
	if br.isRemoteHost() {
		remoteOutputDir, err = br.createRemoteTempDir("flightctl-backup-")
		if err != nil {
			return "", "", err
		}
		defer br.removeRemotePath(remoteOutputDir)
	}
	args = append(args, remoteOutputDir)

	if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
		args = append(args, "--namespace", ns)
	}
	if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
		args = append(args, "--internal-namespace", ns)
	}

	output, err := br.providers.Infra.RunOnHost(args)
	_, _ = fmt.Fprintln(ginkgo.GinkgoWriter, output)
	if err != nil {
		return "", "", fmt.Errorf("flightctl-backup failed: %w", err)
	}

	if br.isRemoteHost() {
		globOutput, err := br.providers.Infra.RunOnHost([]string{"sh", "-c", "ls " + remoteOutputDir + "/flightctl-backup-*.tar.gz"})
		if err != nil {
			return "", "", fmt.Errorf("no backup archive found on remote host in %s: %w", remoteOutputDir, err)
		}
		remoteArchive := strings.TrimSpace(strings.Split(strings.TrimSpace(globOutput), "\n")[0])
		remoteChecksum := remoteArchive + ".sha256"

		// RunOnHost may use sudo, creating root-owned files with 0700/0600 permissions.
		// Make them accessible to the SSH user so SCP (which runs without sudo) can read them.
		if _, err := br.providers.Infra.RunOnHost([]string{"chmod", "755", remoteOutputDir}); err != nil {
			return "", "", fmt.Errorf("chmod remote output dir: %w", err)
		}
		if _, err := br.providers.Infra.RunOnHost([]string{"chmod", "644", remoteArchive, remoteChecksum}); err != nil {
			return "", "", fmt.Errorf("chmod remote backup files: %w", err)
		}

		localArchive := filepath.Join(outputDir, filepath.Base(remoteArchive))
		localChecksum := localArchive + ".sha256"

		if err := br.scpFrom(remoteArchive, localArchive); err != nil {
			return "", "", fmt.Errorf("download archive: %w", err)
		}
		if err := br.scpFrom(remoteChecksum, localChecksum); err != nil {
			return "", "", fmt.Errorf("download checksum: %w", err)
		}
		// Restore the permissions the backup binary intended (0600 for archive,
		// 0644 for checksum) — the remote chmod to 0644 was only needed for SCP.
		_ = os.Chmod(localArchive, 0600)
		return localArchive, localChecksum, nil
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
// For remote quadlet hosts, the command runs on the remote host via SSH.
func (br *BackupRestore) RunFlightCtlBackupRaw(args ...string) (output string, err error) {
	backupBinary := br.getBackupBinaryPath()

	if !br.isRemoteHost() {
		if _, statErr := os.Stat(backupBinary); os.IsNotExist(statErr) {
			return "", fmt.Errorf("binary not found: %s (build with: make build-backup)", backupBinary)
		}
	}

	if br.isRemoteHost() {
		hostArgs := br.replaceOutputDirForRemote(args)
		cmd := append([]string{backupBinary}, hostArgs...)
		return br.providers.Infra.RunOnHost(cmd)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	cmd := exec.CommandContext(ctx, backupBinary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunFlightCtlRestoreRaw runs the flightctl-restore binary with the given arguments without
// setting up services or writing config. Useful for negative tests (bad args, corrupted archives, etc.).
// For remote quadlet hosts, local archive files are uploaded before running the command remotely.
func (br *BackupRestore) RunFlightCtlRestoreRaw(args ...string) (output string, err error) {
	restoreBinary := br.getRestoreBinaryPath()

	if !br.isRemoteHost() {
		if _, statErr := os.Stat(restoreBinary); os.IsNotExist(statErr) {
			return "", fmt.Errorf("binary not found: %s (build with: make flightctl-restore)", restoreBinary)
		}
	}

	if br.isRemoteHost() {
		hostArgs, cleanup, err := br.uploadLocalFilesForRestore(args)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return "", err
		}
		cmd := append([]string{restoreBinary}, hostArgs...)
		return br.providers.Infra.RunOnHost(cmd)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	cmd := exec.CommandContext(ctx, restoreBinary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunFlightCtlRestoreFromArchive runs the flightctl-restore binary with the given archive path.
// The binary reads credentials from the cluster and handles service stop/start internally.
// For remote quadlet hosts, the archive and checksum are uploaded before running remotely.
func (br *BackupRestore) RunFlightCtlRestoreFromArchive(archivePath string) error {
	restoreBinary := br.getRestoreBinaryPath()

	if !br.isRemoteHost() {
		if _, err := os.Stat(restoreBinary); os.IsNotExist(err) {
			return fmt.Errorf("binary not found: %s (build with: make flightctl-restore)", restoreBinary)
		}
	}

	restoreArchivePath := archivePath
	if br.isRemoteHost() {
		remoteTmpDir, err := br.createRemoteTempDir("flightctl-restore-")
		if err != nil {
			return err
		}
		defer br.removeRemotePath(remoteTmpDir)

		// createRemoteTempDir may run with sudo, creating a root-owned 0700 dir.
		// Make it writable so SCP (which runs without sudo) can upload files.
		if _, err := br.providers.Infra.RunOnHost([]string{"chmod", "777", remoteTmpDir}); err != nil {
			return fmt.Errorf("chmod remote restore dir: %w", err)
		}

		remoteArchive := remoteTmpDir + "/" + filepath.Base(archivePath)
		if err := br.scpTo(archivePath, remoteArchive); err != nil {
			return fmt.Errorf("upload archive: %w", err)
		}
		checksumPath := archivePath + ".sha256"
		if _, statErr := os.Stat(checksumPath); statErr == nil {
			remoteChecksum := remoteArchive + ".sha256"
			if err := br.scpTo(checksumPath, remoteChecksum); err != nil {
				return fmt.Errorf("upload checksum: %w", err)
			}
		}
		restoreArchivePath = remoteArchive
	}

	args := []string{restoreBinary, restoreArchivePath}
	if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
		args = append(args, "--namespace", ns)
	}
	if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
		args = append(args, "--internal-namespace", ns)
	}

	if br.isRemoteHost() {
		output, err := br.providers.Infra.RunOnHost(args)
		_, _ = fmt.Fprintln(ginkgo.GinkgoWriter, output)
		if err != nil {
			return fmt.Errorf("flightctl-restore failed: %w", err)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	restoreCmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // G204: args from internal test harness
	restoreCmd.Stdout = ginkgo.GinkgoWriter
	restoreCmd.Stderr = ginkgo.GinkgoWriter
	if err := restoreCmd.Run(); err != nil {
		return fmt.Errorf("flightctl-restore failed: %w", err)
	}
	return nil
}
