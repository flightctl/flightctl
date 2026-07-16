package restore

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

const dbDumpRelPath = "db/dump.sql"

// Well-known default ports for FlightCtl internal services.
const (
	dbInternalPort = 5432
	kvInternalPort = 6379
)

//go:generate go run -modfile=../../tools/go.mod go.uber.org/mock/mockgen -source=deployer.go -destination=mock_deployer.go -package=restore

// ServiceHandler manages the lifecycle of FlightCtl services on a Podman host.
type ServiceHandler interface {
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
}

// SystemctlServiceHandler implements ServiceHandler using systemctl.
type SystemctlServiceHandler struct {
	serviceNames []string
}

// NewSystemctlServiceHandler creates a SystemctlServiceHandler.
// serviceNames nil → default FlightCtl service names.
func NewSystemctlServiceHandler(serviceNames []string) *SystemctlServiceHandler {
	if serviceNames == nil {
		serviceNames = defaultPodmanServiceNames
	}
	return &SystemctlServiceHandler{serviceNames: serviceNames}
}

func (s *SystemctlServiceHandler) Stop(ctx context.Context) error {
	if len(s.serviceNames) == 0 {
		return nil
	}
	args := append([]string{"stop"}, s.serviceNames...)
	if out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl stop failed: %w (output: %s)", err, out)
	}
	return nil
}

func (s *SystemctlServiceHandler) Start(ctx context.Context) error {
	if len(s.serviceNames) == 0 {
		return nil
	}
	args := append([]string{"start"}, s.serviceNames...)
	if out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl start failed: %w (output: %s)", err, out)
	}
	return nil
}

// defaultPodmanServiceNames lists the FlightCtl systemd units that must be
// stopped before restore and started again afterwards.
//
// The list includes flightctl-gateway because it declares
// Requires=flightctl-api.service in its unit file: systemd propagates the stop
// to the gateway when the API is stopped, but does NOT propagate the start back
// when the API is restarted. Without an explicit start the gateway remains down
// and port 3443 stays closed after restore.
//
// Units that are never stopped by the restore (e.g. flightctl-db, flightctl-kv,
// flightctl-ui, flightctl-cli-artifacts) are intentionally omitted.
var defaultPodmanServiceNames = []string{
	"flightctl-api",
	"flightctl-worker",
	"flightctl-periodic",
	"flightctl-imagebuilder-api",
	"flightctl-imagebuilder-worker",
	"flightctl-alert-exporter",
	"flightctl-alertmanager-proxy",
	"flightctl-gateway",
}

// deploymentInfo holds per-deployment metadata for Kubernetes stop/start operations.
type deploymentInfo struct {
	// Name is the Kubernetes Deployment name.
	Name string
	// Internal controls which namespace is used: true → internalNamespace, false → namespace (external).
	Internal bool
}

// defaultDeploymentRegistry is the ordered list of FlightCtl deployments that
// hold database connections and must be stopped before restore. Namespace
// assignment mirrors the Helm chart layout:
//   - External namespace (Internal=false): api, imagebuilder-api, alertmanager-proxy
//   - Internal namespace (Internal=true):  worker, periodic, imagebuilder-worker, alert-exporter
var defaultDeploymentRegistry = []deploymentInfo{
	{Name: "flightctl-api", Internal: false},
	{Name: "flightctl-worker", Internal: true},
	{Name: "flightctl-periodic", Internal: true},
	{Name: "flightctl-imagebuilder-api", Internal: false},
	{Name: "flightctl-imagebuilder-worker", Internal: true},
	{Name: "flightctl-alert-exporter", Internal: true},
	{Name: "flightctl-alertmanager-proxy", Internal: false},
}

// Deployer performs deployment-type-specific restore operations.
// The caller (restore.Restore) is responsible for orchestrating StopServices and
// StartServices around all individual restore steps so that services remain stopped
// for the full duration of the restore, regardless of how many steps are involved.
type Deployer interface {
	// Type returns the deployment type this deployer targets.
	Type() backup.DeploymentType
	// StopServices stops the FlightCtl services that interact with the database
	// and other restored state. Must be called before any restore steps.
	StopServices(ctx context.Context) error
	// StartServices starts the FlightCtl services previously stopped by StopServices.
	// Must be called after all restore steps complete, whether they succeed or fail.
	StartServices(ctx context.Context) error
	// RestoreDatabase imports the database dump from the extracted archive.
	// If db/dump.sql is absent in extractDir the database is external — the
	// function logs restore instructions and returns nil without error.
	// StopServices must be called before RestoreDatabase.
	RestoreDatabase(ctx context.Context, extractDir string) error
	// ExposeService returns the host and port to use for connecting to the named
	// service ("flightctl-db" or "flightctl-kv"). For Kubernetes it starts a
	// kubectl port-forward and returns 127.0.0.1:<localPort>; cleanup() must be
	// deferred by the caller. For Podman it returns the original host/port from
	// the service config with a no-op cleanup (services are accessible on the same host).
	ExposeService(ctx context.Context, serviceName string) (host string, port int, cleanup func(), err error)
	// GetConfig extracts service configuration (DB and KV credentials) from the
	// running infrastructure. For Kubernetes, reads from cluster Secrets; for
	// Podman, reads the service config file on the host.
	GetConfig(ctx context.Context) (*config.Config, error)
	// RestorePKI restores PKI materials from the extracted archive directory.
	// For Podman, it copies <extractDir>/pki/ to the configured PKI destination
	// (default: /etc/flightctl/pki/), preserving file permissions.
	// For Kubernetes, it applies each <extractDir>/pki/*.yaml file as a Secret via the Go client (create or update).
	// Returns an error if the pki/ subdirectory is absent from the archive.
	// StopServices must be called before RestorePKI.
	RestorePKI(ctx context.Context, extractDir string) error
	// RestoreEncryptionKeys restores data-at-rest encryption keys from the archive.
	// For Podman, copies <extractDir>/encryption/ to the configured encryption
	// destination (default: /etc/flightctl/encryption/), preserving file permissions.
	// For Kubernetes, applies <extractDir>/encryption/flightctl-encryption-key.yaml
	// as a Secret and duplicates it to the internal namespace if different.
	// Silently skips if the encryption directory is absent from the archive
	// (backwards compatible with pre-encryption backups).
	RestoreEncryptionKeys(ctx context.Context, extractDir string) error
	// RestoreConfig restores service configuration from the extracted archive directory.
	// For Podman: copies <extractDir>/config/service-config.yaml to the configured service
	// config path and imports the PAM Issuer volume from <extractDir>/volumes/pam-issuer-etc.tar
	// (optional — logs a warning if absent or if the import fails).
	// For Kubernetes: decodes the backed-up Helm release Secret, applies it to the cluster,
	// reconstructs the chart and user values from the release data, and runs helm upgrade.
	// StopServices must be called before RestoreConfig.
	RestoreConfig(ctx context.Context, extractDir string) error
	// SetupExternalDBCerts prepares TLS certificates for connecting to an external database.
	// For Kubernetes: extracts certificates from ConfigMap/Secret to temporary files and
	// updates the config to point to those files. Returns a cleanup function to remove temp files.
	// For Podman: no-op (certificates are already accessible as filesystem paths in service-config.yaml).
	// Returns nil cleanup function when no temporary files were created.
	SetupExternalDBCerts(ctx context.Context, cfg *config.Config) (cleanup func(), err error)
}

// PodmanRestoreDeployer implements Deployer for Podman/quadlet deployments.
type PodmanRestoreDeployer struct {
	log                 logrus.FieldLogger
	containerName       string
	containerCLI        string
	serviceHandler      ServiceHandler
	serviceConfigPath   string
	dbName              string
	kvContainerName     string
	pkiDestPath         string
	encryptionDestPath  string
	pamIssuerVolumeName string
	keepOldDB           bool
	cachedCfg           *config.Config
	dbSecretName        string
	kvSecretName        string
}

// PodmanRestoreOption configures a PodmanRestoreDeployer.
type PodmanRestoreOption func(*PodmanRestoreDeployer)

// WithContainerName sets the database container name.
// Deprecated: use WithDBContainerName.
func WithContainerName(name string) PodmanRestoreOption {
	return WithDBContainerName(name)
}

// WithDBContainerName sets the database container name.
func WithDBContainerName(name string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.containerName = name
	}
}

// WithContainerCLI sets the container CLI command (e.g. "podman" or "docker").
func WithContainerCLI(cli string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.containerCLI = cli
	}
}

// WithServiceHandler sets the service lifecycle handler.
func WithServiceHandler(h ServiceHandler) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.serviceHandler = h
	}
}

// WithServiceConfigPath sets the path to the service configuration file used to
// extract DB and KV credentials. Defaults to /etc/flightctl/service-config.yaml.
func WithServiceConfigPath(path string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.serviceConfigPath = path
	}
}

// WithDBName overrides the database name used for the restore swap target.
// When empty, cfg.Database.Name from the service config is used.
func WithDBName(name string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.dbName = name
	}
}

// WithKVContainerName sets the KV container name used to resolve published ports
// in ExposeService for flightctl-kv.
func WithKVContainerName(name string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.kvContainerName = name
	}
}

// WithPKIDestPath sets the destination directory for PKI file restoration.
// Defaults to /etc/flightctl/pki.
func WithPKIDestPath(path string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.pkiDestPath = path
	}
}

// WithEncryptionDestPath sets the destination directory for encryption keys restoration.
// Defaults to /etc/flightctl/encryption.
func WithEncryptionDestPath(path string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.encryptionDestPath = path
	}
}

// WithPodmanKeepOldDB controls whether the pre-restore database is dropped after
// a successful swap (false, default) or preserved under <dbname>_old_<timestamp> (true).
func WithPodmanKeepOldDB(keep bool) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.keepOldDB = keep
	}
}

// WithPAMIssuerVolumeName sets the Podman volume name for PAM Issuer volume restore.
// Defaults to "flightctl-pam-issuer-etc".
func WithPAMIssuerVolumeName(name string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.pamIssuerVolumeName = name
	}
}

// WithDBSecretName sets the Podman secret name for the database password.
// Defaults to "flightctl-postgresql-user-password". Set to "" to skip secret lookup.
func WithDBSecretName(name string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.dbSecretName = name
	}
}

// WithKVSecretName sets the Podman secret name for the KV store password.
// Defaults to "flightctl-kv-password". Set to "" to skip secret lookup.
func WithKVSecretName(name string) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.kvSecretName = name
	}
}

// NewPodmanRestoreDeployer creates a PodmanRestoreDeployer.
// Defaults: containerName "flightctl-db", containerCLI "podman",
// kvContainerName "flightctl-kv",
// serviceHandler NewSystemctlServiceHandler(nil) with the default service list,
// serviceConfigPath "/etc/flightctl/service-config.yaml".
func NewPodmanRestoreDeployer(
	log logrus.FieldLogger,
	opts ...PodmanRestoreOption,
) *PodmanRestoreDeployer {
	d := &PodmanRestoreDeployer{
		log: log,
	}
	for _, opt := range opts {
		opt(d)
	}
	if d.containerName == "" {
		d.containerName = "flightctl-db"
	}
	if d.containerCLI == "" {
		d.containerCLI = "podman"
	}
	if d.kvContainerName == "" {
		d.kvContainerName = "flightctl-kv"
	}
	if d.serviceHandler == nil {
		d.serviceHandler = NewSystemctlServiceHandler(nil)
	}
	if d.serviceConfigPath == "" {
		d.serviceConfigPath = "/etc/flightctl/service-config.yaml"
	}
	if d.pkiDestPath == "" {
		d.pkiDestPath = "/etc/flightctl/pki"
	}
	if d.encryptionDestPath == "" {
		d.encryptionDestPath = "/etc/flightctl/encryption"
	}
	if d.pamIssuerVolumeName == "" {
		d.pamIssuerVolumeName = "flightctl-pam-issuer-etc"
	}
	if d.dbSecretName == "" {
		d.dbSecretName = "flightctl-postgresql-user-password"
	}
	if d.kvSecretName == "" {
		d.kvSecretName = "flightctl-kv-password"
	}
	return d
}

func (p *PodmanRestoreDeployer) Type() backup.DeploymentType {
	return backup.DeploymentTypePodman
}

// StopServices delegates to the ServiceHandler.
func (p *PodmanRestoreDeployer) StopServices(ctx context.Context) error {
	p.log.Info("Stopping FlightCtl services")
	return p.serviceHandler.Stop(ctx)
}

// StartServices delegates to the ServiceHandler.
func (p *PodmanRestoreDeployer) StartServices(ctx context.Context) error {
	p.log.Info("Starting FlightCtl services")
	return p.serviceHandler.Start(ctx)
}

// GetConfig loads DB and KV credentials from the service configuration file,
// then overrides passwords from Podman secrets (same approach as the e2e SecretsProvider).
// The service-config.yaml does not contain DB/KV passwords; they are injected into
// containers via Podman secrets. The restore binary runs on the host, so credentials
// must be read directly via `podman secret inspect --showsecret`.
// The result is cached after the first successful load.
func (p *PodmanRestoreDeployer) GetConfig(ctx context.Context) (*config.Config, error) {
	if p.cachedCfg != nil {
		return p.cachedCfg, nil
	}
	cfg, err := config.Load(p.serviceConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load service configuration from %s: %w", p.serviceConfigPath, err)
	}
	if p.dbSecretName != "" {
		if dbPass, ok := readPodmanSecret(ctx, p.containerCLI, p.dbSecretName); ok {
			cfg.Database.Password = api.SecureString(dbPass)
		} else {
			p.log.Debugf("Could not read DB password from Podman secret %q; using config value", p.dbSecretName)
		}
	}
	if p.kvSecretName != "" {
		if kvPass, ok := readPodmanSecret(ctx, p.containerCLI, p.kvSecretName); ok {
			cfg.KV.Password = api.SecureString(kvPass)
		} else {
			p.log.Debugf("Could not read KV password from Podman secret %q; using config value", p.kvSecretName)
		}
	}
	p.cachedCfg = cfg
	return cfg, nil
}

// RestoreDatabase imports db/dump.sql from extractDir into the Podman DB container
// using the temp-DB-then-rename strategy.
//
// The dump is restored into a fresh temporary database as the postgres OS user
// (UNIX socket auth, no credentials needed). Once the restore succeeds the live
// database is atomically swapped via ALTER DATABASE RENAME and then dropped
// (unless keepOldDB is true, in which case it is preserved for operator recovery).
func (p *PodmanRestoreDeployer) RestoreDatabase(ctx context.Context, extractDir string) error {
	dumpPath := filepath.Join(extractDir, dbDumpRelPath)

	if _, err := os.Stat(dumpPath); os.IsNotExist(err) {
		p.log.Info("No database dump found in archive — external database detected.")
		p.log.Info("To restore the database, import the dump manually using your database backup tools and then re-run the restore command.")
		return nil
	}

	cfg, err := p.GetConfig(ctx)
	if err != nil {
		return err
	}

	dbName := cfg.Database.Name
	if p.dbName != "" {
		dbName = p.dbName
	}
	restoreID := time.Now().UnixNano()
	tempDBName := fmt.Sprintf("%s_restore_%d", dbName, restoreID)
	oldDBName := fmt.Sprintf("%s_old_%d", dbName, restoreID)

	p.log.Infof("Creating temporary restore database %q in container %s", tempDBName, p.containerName)
	if err := p.execDBCommand(ctx, "postgres", fmt.Sprintf(`CREATE DATABASE "%s"`, tempDBName)); err != nil {
		return fmt.Errorf("failed to create temporary database: %w", err)
	}

	restoreSucceeded := false
	liveDBRenamed := false
	swapCompleted := false
	defer func() {
		if restoreSucceeded {
			return
		}
		if liveDBRenamed && !swapCompleted {
			p.log.Infof("Restoring original database name %q after failed restore", dbName)
			if err := p.execDBCommand(ctx, "postgres", fmt.Sprintf(`ALTER DATABASE "%s" RENAME TO "%s"`, oldDBName, dbName)); err != nil {
				p.log.Errorf("CRITICAL: could not restore original database name — live data is in %q, rename it manually to %q: %v", oldDBName, dbName, err)
			}
		}
		if !swapCompleted {
			p.log.Infof("Cleaning up temporary database %q after failed restore", tempDBName)
			if err := p.execDBCommand(ctx, "postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, tempDBName)); err != nil {
				p.log.Warnf("Failed to drop temporary database %q: %v", tempDBName, err)
			}
		}
	}()

	p.log.Infof("Restoring dump from %s into %q in container %s", dumpPath, tempDBName, p.containerName)
	if err := p.execRestoreDump(ctx, tempDBName, dumpPath); err != nil {
		return fmt.Errorf("failed to restore dump into %q: %w", tempDBName, err)
	}

	// Terminate active connections to the target database (use $1 placeholder to prevent SQL injection)
	if err := p.execDBCommand(ctx, "postgres",
		fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, strings.ReplaceAll(dbName, "'", "''")),
	); err != nil {
		p.log.Warnf("Failed to terminate connections to %q (continuing): %v", dbName, err)
	}

	p.log.Infof("Renaming %q → %q", dbName, oldDBName)
	if err := p.execDBCommand(ctx, "postgres", fmt.Sprintf(`ALTER DATABASE "%s" RENAME TO "%s"`, dbName, oldDBName)); err != nil {
		return fmt.Errorf("failed to rename %q to %q: %w", dbName, oldDBName, err)
	}
	liveDBRenamed = true

	p.log.Infof("Renaming %q → %q", tempDBName, dbName)
	if err := p.execDBCommand(ctx, "postgres", fmt.Sprintf(`ALTER DATABASE "%s" RENAME TO "%s"`, tempDBName, dbName)); err != nil {
		return fmt.Errorf("failed to rename %q to %q: %w", tempDBName, dbName, err)
	}
	swapCompleted = true
	restoreSucceeded = true

	// Sync restored database passwords with current deployment secrets
	p.log.Info("Synchronizing database passwords with current deployment")
	if err := p.syncDatabasePasswords(ctx); err != nil {
		return fmt.Errorf("failed to sync database passwords: %w", err)
	}

	if p.keepOldDB {
		p.log.Infof("Database restore completed. Pre-restore database preserved as %q", oldDBName)
	} else {
		p.log.Infof("Dropping pre-restore database %q", oldDBName)
		if err := p.execDBCommand(ctx, "postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, oldDBName)); err != nil {
			p.log.Warnf("Failed to drop pre-restore database %q (manual cleanup may be required): %v", oldDBName, err)
		}
	}

	p.log.Info("Database restore completed successfully")
	return nil
}

// syncDatabasePasswords updates the restored database user passwords to match current deployment secrets.
// Passwords are read directly from Podman secrets, not from the config file (which redacts them).
// If a secret cannot be read the corresponding user's password is left as-is and the sync is skipped
// for that user — this allows integration tests (which have no Podman secrets) to run unaffected.
func (p *PodmanRestoreDeployer) syncDatabasePasswords(ctx context.Context) error {
	if p.dbSecretName != "" {
		if appPass, ok := readPodmanSecret(ctx, p.containerCLI, p.dbSecretName); ok {
			sql := fmt.Sprintf(`ALTER USER flightctl_app WITH PASSWORD '%s'`, strings.ReplaceAll(appPass, "'", "''"))
			if err := p.execDBCommand(ctx, "postgres", sql); err != nil {
				return fmt.Errorf("failed to update flightctl_app password: %w", err)
			}
		} else {
			p.log.Debugf("Could not read DB password from Podman secret %q; skipping flightctl_app password sync", p.dbSecretName)
		}
	}

	masterPassword, err := p.readSecret(ctx, "flightctl-postgresql-master-password")
	if err != nil {
		p.log.Warnf("Failed to read master password secret: %v (skipping admin password sync)", err)
	} else {
		sql := fmt.Sprintf(`ALTER USER admin WITH PASSWORD '%s'`, strings.ReplaceAll(masterPassword, "'", "''"))
		if err := p.execDBCommand(ctx, "postgres", sql); err != nil {
			return fmt.Errorf("failed to update admin password: %w", err)
		}
	}

	p.log.Info("Database passwords synchronized successfully")
	return nil
}

// readSecret reads a podman secret value
func (p *PodmanRestoreDeployer) readSecret(ctx context.Context, secretName string) (string, error) {
	cmd := exec.CommandContext(ctx, p.containerCLI, "secret", "inspect", "--format", "{{.Spec.Data}}", secretName)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to inspect secret %s: %w", secretName, err)
	}
	// Secret data is base64 encoded
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return "", fmt.Errorf("failed to decode secret %s: %w", secretName, err)
	}
	return string(decoded), nil
}

// execDBCommand runs a single SQL command inside the DB container as the postgres OS user.
// The postgres user authenticates via UNIX socket without a password.
func (p *PodmanRestoreDeployer) execDBCommand(ctx context.Context, dbName, sql string) error {
	cmd := exec.CommandContext(ctx, p.containerCLI, "exec", p.containerName,
		"psql", "-U", "postgres", "-d", dbName, "-c", sql)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql in container %q failed: %w (output: %s)", p.containerName, err, out.String())
	}
	return nil
}

// execRestoreDump streams dumpPath into psql inside the DB container as the postgres user.
func (p *PodmanRestoreDeployer) execRestoreDump(ctx context.Context, dbName, dumpPath string) error {
	dumpFile, err := os.Open(dumpPath)
	if err != nil {
		return fmt.Errorf("failed to open dump file %q: %w", dumpPath, err)
	}
	defer dumpFile.Close()

	cmd := exec.CommandContext(ctx, p.containerCLI, "exec", "-i", p.containerName,
		"psql", "-U", "postgres", "-d", dbName, "-v", "ON_ERROR_STOP=1", "-f", "-")
	cmd.Stdin = dumpFile
	cmd.Stdout = os.Stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql restore in container %q failed: %w (stderr: %s)", p.containerName, err, stderr.String())
	}
	return nil
}

// ExposeService dynamically exposes the named service via localhost port-forwarding.
// Returns host, port, cleanup function. Cleanup must be called to stop the port-forward.
// Tries published ports first; if none, creates TCP forward to container IP.
func (p *PodmanRestoreDeployer) ExposeService(ctx context.Context, serviceName string) (string, int, func(), error) {
	var containerName string
	var targetPort int

	switch serviceName {
	case "flightctl-db":
		containerName = p.containerName
		targetPort = dbInternalPort
	case "flightctl-kv":
		containerName = p.kvContainerName
		targetPort = kvInternalPort
	default:
		return "", 0, func() {}, fmt.Errorf("unknown service %q: must be flightctl-db or flightctl-kv", serviceName)
	}

	// First, check if port is already published
	if host, port, ok := containerPublishedTCPPort(ctx, p.containerCLI, containerName, targetPort); ok {
		return host, port, func() {}, nil
	}

	// Port not published - create dynamic TCP forward to container IP
	containerIP, err := getContainerIP(ctx, p.containerCLI, containerName)
	if err != nil {
		return "", 0, func() {}, fmt.Errorf("failed to get container IP for %s: %w", serviceName, err)
	}

	localPort, err := getFreePort()
	if err != nil {
		return "", 0, func() {}, fmt.Errorf("failed to get free local port: %w", err)
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return "", 0, func() {}, fmt.Errorf("failed to listen on 127.0.0.1:%d: %w", localPort, err)
	}

	target := net.JoinHostPort(containerIP, strconv.Itoa(targetPort))
	go runTCPForward(listener, target, serviceName)

	cleanup := func() {
		_ = listener.Close()
	}

	return "127.0.0.1", localPort, cleanup, nil
}

// getContainerIP returns the first container IP (Podman bridge network).
func getContainerIP(ctx context.Context, cli, containerName string) (string, error) {
	if containerName == "" {
		return "", fmt.Errorf("container name is empty")
	}
	format := "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}"
	cmd := exec.CommandContext(ctx, cli, "inspect", "-f", format, containerName)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to inspect container %s: %w", containerName, err)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("container %s has no network IP", containerName)
	}
	return ip, nil
}

// runTCPForward accepts connections on listener and forwards each to target (host:port).
func runTCPForward(listener net.Listener, target string, serviceName string) {
	for {
		client, err := listener.Accept()
		if err != nil {
			// Listener closed, exit goroutine
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			backend, err := net.Dial("tcp", target)
			if err != nil {
				logrus.Warnf("Restore port-forward %s: dial %s: %v", serviceName, target, err)
				return
			}
			defer backend.Close()
			// Bidirectional copy
			go func() { _, _ = io.Copy(backend, c) }()
			_, _ = io.Copy(c, backend)
		}(client)
	}
}

// podmanSecretData is the minimal JSON shape returned by `podman secret inspect --showsecret`.
type podmanSecretData []struct {
	SecretData string `json:"SecretData"`
}

// readPodmanSecret reads a secret value via `podman secret inspect --showsecret <name>`.
// Returns ("", false) if the secret cannot be read for any reason.
func readPodmanSecret(ctx context.Context, cli, secretName string) (string, bool) {
	if cli == "" || secretName == "" {
		return "", false
	}
	out, err := exec.CommandContext(ctx, cli, "secret", "inspect", "--showsecret", secretName).Output()
	if err != nil {
		return "", false
	}
	var parsed podmanSecretData
	if err := json.Unmarshal(out, &parsed); err != nil || len(parsed) == 0 {
		return "", false
	}
	val := strings.TrimSpace(parsed[0].SecretData)
	return val, val != ""
}

// containerPublishedTCPPort resolves the host-published TCP port for a named container.
func containerPublishedTCPPort(ctx context.Context, cli, containerName string, containerPort int) (host string, port int, ok bool) {
	if containerName == "" {
		return "", 0, false
	}
	portSpec := fmt.Sprintf("%d/tcp", containerPort)
	cmd := exec.CommandContext(ctx, cli, "port", containerName, portSpec)
	out, err := cmd.Output()
	if err != nil {
		return "", 0, false
	}
	h, p, parsed := parseContainerHostPort(string(out))
	if !parsed {
		return "", 0, false
	}
	return h, p, true
}

func parseContainerHostPort(output string) (host string, port int, ok bool) {
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
	if hostRaw == "" {
		hostRaw = "127.0.0.1"
	}
	return hostRaw, int(p64), true
}

// copyDirSecure recursively copies srcDir to dstDir, preserving file permissions.
// It rejects symlinks and any non-regular, non-directory entries to prevent path
// traversal attacks. Returns the number of regular files copied.
func copyDirSecure(ctx context.Context, srcDir, dstDir string, log logrus.FieldLogger) (int, error) {
	count := 0
	err := filepath.Walk(srcDir, func(srcPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("error walking source directory: %w", walkErr)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		relPath, err := filepath.Rel(srcDir, srcPath)
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", srcPath, err)
		}
		dstPath := filepath.Join(dstDir, relPath)

		if info.IsDir() {
			if relPath == "." {
				return nil
			}
			return os.MkdirAll(dstPath, info.Mode())
		}

		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular entry %s (mode %s)", relPath, info.Mode())
		}

		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", relPath, err)
		}
		if err := os.WriteFile(dstPath, data, info.Mode()); err != nil {
			return fmt.Errorf("failed to write file %s: %w", relPath, err)
		}
		log.Debugf("Restored file: %s", relPath)
		count++
		return nil
	})
	return count, err
}

// RestorePKI copies the pki/ directory from the extracted archive into the configured
// PKI destination, preserving each file's mode. Returns an error if pki/ is absent.
func (p *PodmanRestoreDeployer) RestorePKI(ctx context.Context, extractDir string) error {
	pkiSrcDir := filepath.Join(extractDir, "pki")
	if _, err := os.Stat(pkiSrcDir); os.IsNotExist(err) {
		return fmt.Errorf("PKI materials missing from archive: pki/ directory not found in %s", extractDir)
	} else if err != nil {
		return fmt.Errorf("failed to access PKI directory in archive: %w", err)
	}

	if err := os.MkdirAll(p.pkiDestPath, 0700); err != nil {
		return fmt.Errorf("failed to create PKI destination directory %s: %w", p.pkiDestPath, err)
	}

	count, err := copyDirSecure(ctx, pkiSrcDir, p.pkiDestPath, p.log)
	if err != nil {
		return fmt.Errorf("PKI restore failed: %w", err)
	}

	p.log.Infof("PKI restore completed. Restored %d files to %s", count, p.pkiDestPath)
	return nil
}

// RestoreEncryptionKeys restores the data-at-rest encryption key directory from the archive.
// Copies <extractDir>/encryption/ to <encryptionDestPath>, preserving file permissions.
// Logs a warning and skips if the encryption directory is absent from the archive
// (backwards compatible with pre-encryption backups).
func (p *PodmanRestoreDeployer) RestoreEncryptionKeys(ctx context.Context, extractDir string) (retErr error) {
	encSrcDir := filepath.Join(extractDir, "encryption")

	if _, err := os.Stat(encSrcDir); os.IsNotExist(err) {
		p.log.Warnf("No encryption key directory in archive, skipping encryption key restore. If this deployment uses data-at-rest encryption, encrypted database fields will be unrecoverable.")
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to access encryption directory in archive: %w", err)
	}

	p.log.Infof("Starting encryption key restore to %s...", p.encryptionDestPath)

	// Stage into a temp directory next to the destination so that a failure
	// mid-walk (e.g. symlink rejection) does not leave a partially written
	// destination. On success the staging dir is renamed atomically.
	stagingDir, err := os.MkdirTemp(filepath.Dir(p.encryptionDestPath), ".enc-restore-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(stagingDir); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("failed to clean up staging directory %s: %w", stagingDir, cleanupErr))
		}
	}()

	count, err := copyDirSecure(ctx, encSrcDir, stagingDir, p.log)
	if err != nil {
		return fmt.Errorf("encryption key restore failed: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("encryption archive directory is empty — refusing to overwrite live key material")
	}

	// Atomic swap: rename the existing destination aside, then rename staging
	// into place. If the final rename fails, roll back by restoring the backup
	// so live key material is never lost.
	backupDir := p.encryptionDestPath + ".bak"
	if _, statErr := os.Stat(p.encryptionDestPath); statErr == nil {
		if err := os.RemoveAll(backupDir); err != nil {
			p.log.Warnf("Failed to clean up stale encryption key backup at %s: %v", backupDir, err)
		}
		if err := os.Rename(p.encryptionDestPath, backupDir); err != nil {
			return fmt.Errorf("failed to move existing encryption directory aside: %w", err)
		}
	} else if _, bakErr := os.Stat(backupDir); bakErr == nil {
		p.log.Warnf("Destination %s is missing but backup %s exists — likely an interrupted prior restore; preserving it for recovery", p.encryptionDestPath, backupDir)
	}
	if err := os.Rename(stagingDir, p.encryptionDestPath); err != nil {
		// Roll back: restore the original directory so keys are not lost.
		if rbErr := os.Rename(backupDir, p.encryptionDestPath); rbErr != nil {
			return errors.Join(
				fmt.Errorf("failed to move staged encryption keys to destination: %w", err),
				fmt.Errorf("rollback also failed: %w", rbErr),
			)
		}
		return fmt.Errorf("failed to move staged encryption keys to destination: %w", err)
	}
	if err := os.RemoveAll(backupDir); err != nil {
		p.log.Warnf("Failed to clean up old encryption key backup at %s: %v", backupDir, err)
	}

	p.log.Infof("Encryption key restore completed. Restored %d files to %s", count, p.encryptionDestPath)
	return nil
}

// RestoreConfig copies service-config.yaml from the archive to the configured service
// config path and imports the PAM Issuer volume (optional). Returns an error only if
// service-config.yaml is absent from the archive; PAM Issuer volume failures are logged
// as warnings since the component is optional.
func (p *PodmanRestoreDeployer) RestoreConfig(ctx context.Context, extractDir string) error {
	srcConfig := filepath.Join(extractDir, "config", "service-config.yaml")
	info, err := os.Stat(srcConfig)
	if os.IsNotExist(err) {
		return fmt.Errorf("service configuration not found in archive: %s", srcConfig)
	}
	if err != nil {
		return fmt.Errorf("failed to access service configuration in archive: %w", err)
	}

	data, err := os.ReadFile(srcConfig)
	if err != nil {
		return fmt.Errorf("failed to read service configuration from archive: %w", err)
	}
	if err := os.WriteFile(p.serviceConfigPath, data, info.Mode().Perm()); err != nil {
		return fmt.Errorf("failed to write service configuration to %s: %w", p.serviceConfigPath, err)
	}
	p.log.Infof("Service configuration restored: %s", p.serviceConfigPath)
	p.cachedCfg = nil

	volumeArchive := filepath.Join(extractDir, "volumes", "pam-issuer-etc.tar")
	if _, err := os.Stat(volumeArchive); os.IsNotExist(err) {
		p.log.Warn("PAM Issuer volume archive not found in backup — skipping PAM Issuer volume restore")
		return nil
	} else if err != nil {
		p.log.Warnf("Failed to access PAM Issuer volume archive: %v — skipping", err)
		return nil
	}

	// Remove the volume first so that import gives exact backup state rather than
	// overlaying new content onto stale files. Ignore the remove error — it is
	// expected when the volume does not yet exist.
	exec.CommandContext(ctx, p.containerCLI, "volume", "rm", p.pamIssuerVolumeName).Run() //nolint:errcheck

	if out, err := exec.CommandContext(ctx, p.containerCLI, "volume", "create", p.pamIssuerVolumeName).CombinedOutput(); err != nil {
		p.log.Warnf("Failed to create PAM Issuer volume %q: %v (output: %s) — service may need manual configuration",
			p.pamIssuerVolumeName, err, out)
		return nil
	}

	cmd := exec.CommandContext(ctx, p.containerCLI, "volume", "import", p.pamIssuerVolumeName, volumeArchive)
	if out, err := cmd.CombinedOutput(); err != nil {
		p.log.Warnf("Failed to import PAM Issuer volume %q: %v (output: %s) — service may need manual configuration",
			p.pamIssuerVolumeName, err, out)
		return nil
	}
	p.log.Infof("PAM Issuer volume restored: %s", p.pamIssuerVolumeName)
	return nil
}

// SetupExternalDBCerts is a no-op for Podman deployments.
// TLS certificate paths are already specified in service-config.yaml as filesystem paths
// (e.g., /etc/flightctl/certs/ca.crt) and are directly accessible to the restore tool
// running on the same host.
func (p *PodmanRestoreDeployer) SetupExternalDBCerts(ctx context.Context, cfg *config.Config) (func(), error) {
	// No-op: certificates are already accessible as filesystem paths
	return nil, nil
}

// KubernetesRestoreDeployer implements Deployer for Kubernetes/Helm deployments.
type KubernetesRestoreDeployer struct {
	log               logrus.FieldLogger
	namespace         string // external namespace (api, ui, PKI secrets)
	internalNamespace string // internal namespace (worker, periodic, db)
	clientset         kubernetes.Interface
	restCfg           *rest.Config
	keepOldDB         bool
	helmUpgradeFunc   func(ctx context.Context, releaseName, chartDir, namespace, valuesFile string) error
	// originalReplicas holds pre-stop replica counts so StartServices can restore them.
	originalReplicas map[string]int32
	cachedCfg        *config.Config
}

// defaultHelmUpgrade runs helm upgrade using the system helm CLI.
func defaultHelmUpgrade(ctx context.Context, releaseName, chartDir, namespace, valuesFile string) error {
	cmd := exec.CommandContext(ctx, "helm", "upgrade", releaseName, chartDir,
		"--namespace", namespace, "--values", valuesFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm upgrade failed: %w (output: %s)", err, out)
	}
	return nil
}

// helmReleaseJSON is a minimal representation of the Helm 3 release JSON used
// to extract chart and user-values for helm upgrade during restore.
type helmReleaseJSON struct {
	Name      string                 `json:"name"`
	Chart     *helmChartJSON         `json:"chart"`
	Config    map[string]interface{} `json:"config"`
	Namespace string                 `json:"namespace"`
}

type helmChartJSON struct {
	Metadata  map[string]interface{} `json:"metadata"`
	Templates []*helmFileJSON        `json:"templates"`
	Values    map[string]interface{} `json:"values"`
	Files     []*helmFileJSON        `json:"files"`
}

// helmFileJSON represents a file in a Helm chart. Data is a raw byte slice;
// encoding/json automatically decodes the base64 string from JSON into []byte.
type helmFileJSON struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// KubernetesRestoreOption configures a KubernetesRestoreDeployer.
type KubernetesRestoreOption func(*KubernetesRestoreDeployer)

// WithRestoreNamespace sets the external Kubernetes namespace (api, ui).
func WithRestoreNamespace(ns string) KubernetesRestoreOption {
	return func(d *KubernetesRestoreDeployer) {
		d.namespace = ns
	}
}

// WithRestoreInternalNamespace sets the internal Kubernetes namespace (worker, periodic, db).
func WithRestoreInternalNamespace(ns string) KubernetesRestoreOption {
	return func(d *KubernetesRestoreDeployer) {
		d.internalNamespace = ns
	}
}

// WithRestoreClientset injects a Kubernetes clientset (for testing).
func WithRestoreClientset(cs kubernetes.Interface) KubernetesRestoreOption {
	return func(d *KubernetesRestoreDeployer) {
		d.clientset = cs
	}
}

// WithRestoreRestConfig injects a REST config (for testing).
func WithRestoreRestConfig(cfg *rest.Config) KubernetesRestoreOption {
	return func(d *KubernetesRestoreDeployer) {
		d.restCfg = cfg
	}
}

// WithKeepOldDB controls whether the pre-restore database is dropped after a
// successful swap (false, default) or preserved under <dbname>_old_<timestamp>
// (true) for operator recovery.
func WithKeepOldDB(keep bool) KubernetesRestoreOption {
	return func(d *KubernetesRestoreDeployer) {
		d.keepOldDB = keep
	}
}

// WithHelmUpgradeFunc injects a custom helm upgrade function (for testing).
// The default runs the system helm CLI.
func WithHelmUpgradeFunc(fn func(ctx context.Context, releaseName, chartDir, namespace, valuesFile string) error) KubernetesRestoreOption {
	return func(d *KubernetesRestoreDeployer) {
		d.helmUpgradeFunc = fn
	}
}

// NewKubernetesRestoreDeployer creates a KubernetesRestoreDeployer.
// Defaults: namespace "flightctl"; internalNamespace inherits namespace;
// clientset nil → in-cluster client is created on first use;
// restCfg nil → in-cluster config is resolved on first use.
func NewKubernetesRestoreDeployer(
	log logrus.FieldLogger,
	opts ...KubernetesRestoreOption,
) *KubernetesRestoreDeployer {
	d := &KubernetesRestoreDeployer{
		log:              log,
		originalReplicas: map[string]int32{},
	}
	for _, opt := range opts {
		opt(d)
	}
	if d.namespace == "" {
		d.namespace = "flightctl"
	}
	if d.internalNamespace == "" {
		d.internalNamespace = d.namespace
	}
	if d.helmUpgradeFunc == nil {
		d.helmUpgradeFunc = defaultHelmUpgrade
	}
	return d
}

// namespaceFor returns the resolved namespace for a deploymentInfo entry.
func (k *KubernetesRestoreDeployer) namespaceFor(dep deploymentInfo) string {
	if dep.Internal {
		return k.internalNamespace
	}
	return k.namespace
}

func (k *KubernetesRestoreDeployer) Type() backup.DeploymentType {
	return backup.DeploymentTypeKubernetes
}

func (k *KubernetesRestoreDeployer) resolveRestConfig() (*rest.Config, error) {
	if k.restCfg != nil {
		return k.restCfg, nil
	}
	cfg, err := rest.InClusterConfig()
	if err == nil {
		k.restCfg = cfg
		return cfg, nil
	}
	// Fall back to kubeconfig for out-of-cluster use (e.g. admin machine).
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get REST config (tried in-cluster and kubeconfig): %w", err)
	}
	k.restCfg = cfg
	return cfg, nil
}

func (k *KubernetesRestoreDeployer) resolveClientset() (kubernetes.Interface, error) {
	if k.clientset != nil {
		return k.clientset, nil
	}
	cfg, err := k.resolveRestConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}
	k.clientset = cs
	return cs, nil
}

// getSecretStringValue reads a single key from a Kubernetes Secret.
func (k *KubernetesRestoreDeployer) getSecretStringValue(ctx context.Context, ns, name, key string) (string, error) {
	cs, err := k.resolveClientset()
	if err != nil {
		return "", err
	}
	secret, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", ns, name, err)
	}
	data, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, ns, name)
	}
	return string(data), nil
}

// GetConfig reads DB and KV credentials from Kubernetes Secrets and returns a
// populated config.Config. The result is cached after the first successful call.
func (k *KubernetesRestoreDeployer) GetConfig(ctx context.Context) (*config.Config, error) {
	if k.cachedCfg != nil {
		return k.cachedCfg, nil
	}

	dbUser, err := k.getSecretStringValue(ctx, k.internalNamespace, "flightctl-db-app-secret", "user")
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("flightctl-db-app-secret not found in namespace %q — is this a FlightCtl Kubernetes deployment?", k.internalNamespace)
		}
		return nil, fmt.Errorf("failed to read DB credentials: %w", err)
	}

	dbPassword, err := k.getSecretStringValue(ctx, k.internalNamespace, "flightctl-db-app-secret", "userPassword")
	if err != nil {
		return nil, fmt.Errorf("failed to read DB password: %w", err)
	}

	kvPassword, err := k.getSecretStringValue(ctx, k.internalNamespace, "flightctl-kv-secret", "password")
	if err != nil {
		return nil, fmt.Errorf("failed to read KV password: %w", err)
	}

	// Read actual database hostname from api config (handles both internal and external DB)
	apiConfigMap, err := k.clientset.CoreV1().ConfigMaps(k.namespace).Get(ctx, "flightctl-api-config", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to read flightctl-api-config: %w", err)
	}

	configYAML, ok := apiConfigMap.Data["config.yaml"]
	if !ok {
		return nil, fmt.Errorf("config.yaml not found in flightctl-api-config ConfigMap")
	}

	cfg := &config.Config{}
	if err := yaml.Unmarshal([]byte(configYAML), cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config.yaml from ConfigMap: %w", err)
	}

	// Override with credentials from secrets (config may have placeholders)
	cfg.Database.User = dbUser
	cfg.Database.Password = api.SecureString(dbPassword)
	cfg.KV.Password = api.SecureString(kvPassword)

	k.cachedCfg = cfg
	return cfg, nil
}

const (
	// stopWaitTimeout is the maximum time to wait for a deployment to reach zero
	// replicas after being scaled down before StopServices returns an error.
	stopWaitTimeout  = 5 * time.Minute
	stopWaitInterval = 3 * time.Second
)

// StopServices scales FlightCtl deployments to zero replicas, recording the
// original counts so StartServices can restore them, then waits until each
// deployment has no running pods before returning.
func (k *KubernetesRestoreDeployer) StopServices(ctx context.Context) error {
	clientset, err := k.resolveClientset()
	if err != nil {
		return err
	}
	names := make([]string, len(defaultDeploymentRegistry))
	for i, d := range defaultDeploymentRegistry {
		names[i] = d.Name
	}
	k.log.Infof("Scaling down FlightCtl deployments: %v", names)
	for _, dep := range defaultDeploymentRegistry {
		ns := k.namespaceFor(dep)
		deployment, err := clientset.AppsV1().Deployments(ns).Get(ctx, dep.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get deployment %q in namespace %q: %w", dep.Name, ns, err)
		}
		if deployment.Spec.Replicas != nil {
			k.originalReplicas[dep.Name] = *deployment.Spec.Replicas
		} else {
			k.originalReplicas[dep.Name] = 1
		}
		zero := int32(0)
		deployment.Spec.Replicas = &zero
		if _, err := clientset.AppsV1().Deployments(ns).Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to scale down deployment %q: %w", dep.Name, err)
		}

		k.log.Infof("Waiting for deployment %q to reach zero replicas", dep.Name)
		deadline := time.Now().Add(stopWaitTimeout)
		for {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("context cancelled waiting for deployment %q to scale down: %w", dep.Name, err)
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out after %s waiting for deployment %q to reach zero replicas", stopWaitTimeout, dep.Name)
			}
			current, err := clientset.AppsV1().Deployments(ns).Get(ctx, dep.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to poll deployment %q status: %w", dep.Name, err)
			}
			if current.Status.Replicas == 0 {
				break
			}
			k.log.Debugf("Deployment %q still has %d replica(s), waiting...", dep.Name, current.Status.Replicas)
			time.Sleep(stopWaitInterval)
		}
		k.log.Infof("Deployment %q scaled to zero", dep.Name)
	}
	return nil
}

// StartServices scales FlightCtl deployments back to the replica counts recorded by StopServices.
func (k *KubernetesRestoreDeployer) StartServices(ctx context.Context) error {
	clientset, err := k.resolveClientset()
	if err != nil {
		return fmt.Errorf("failed to resolve clientset for scale-up: %w", err)
	}
	names := make([]string, len(defaultDeploymentRegistry))
	for i, d := range defaultDeploymentRegistry {
		names[i] = d.Name
	}
	k.log.Infof("Scaling FlightCtl deployments back up: %v", names)
	var firstErr error
	for _, dep := range defaultDeploymentRegistry {
		ns := k.namespaceFor(dep)
		replicas, ok := k.originalReplicas[dep.Name]
		if !ok {
			continue
		}
		deployment, err := clientset.AppsV1().Deployments(ns).Get(ctx, dep.Name, metav1.GetOptions{})
		if err != nil {
			k.log.Warnf("Failed to get deployment %q for scale-up (manual restart required): %v", dep.Name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		r := replicas
		deployment.Spec.Replicas = &r
		if _, err := clientset.AppsV1().Deployments(ns).Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
			k.log.Warnf("Failed to scale deployment %q back to %d replicas (manual restart required): %v", dep.Name, replicas, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// RestoreDatabase imports db/dump.sql from extractDir into the Kubernetes DB pod
// using the temp-DB-then-rename strategy.
//
// The dump is restored into a fresh temporary database using kubectl exec into
// the flightctl-db deployment as the postgres OS user (UNIX socket auth, no
// credentials required). Once the restore succeeds the live database is
// atomically swapped via ALTER DATABASE RENAME and then dropped (unless
// keepOldDB is true, in which case it is preserved for operator recovery).
func (k *KubernetesRestoreDeployer) RestoreDatabase(ctx context.Context, extractDir string) error {
	dumpPath := filepath.Join(extractDir, dbDumpRelPath)

	if _, err := os.Stat(dumpPath); os.IsNotExist(err) {
		k.log.Info("No database dump found in archive — external database detected.")
		k.log.Info("To restore the database, import the dump manually using your database backup tools and then re-run the restore command.")
		return nil
	}

	cfg, err := k.GetConfig(ctx)
	if err != nil {
		return err
	}

	dbName := cfg.Database.Name
	restoreID := time.Now().UnixNano()
	tempDBName := fmt.Sprintf("%s_restore_%d", dbName, restoreID)
	oldDBName := fmt.Sprintf("%s_old_%d", dbName, restoreID)

	k.log.Infof("Creating temporary restore database %q in deploy/flightctl-db (namespace %s)", tempDBName, k.internalNamespace)
	if err := k.execDBCommand(ctx, "postgres", fmt.Sprintf(`CREATE DATABASE "%s"`, tempDBName)); err != nil {
		return fmt.Errorf("failed to create temporary database: %w", err)
	}

	restoreSucceeded := false
	liveDBRenamed := false
	swapCompleted := false
	defer func() {
		if restoreSucceeded {
			return
		}
		if liveDBRenamed && !swapCompleted {
			k.log.Infof("Restoring original database name %q after failed restore", dbName)
			if err := k.execDBCommand(ctx, "postgres", fmt.Sprintf(`ALTER DATABASE "%s" RENAME TO "%s"`, oldDBName, dbName)); err != nil {
				k.log.Errorf("CRITICAL: could not restore original database name — live data is in %q, rename it manually to %q: %v", oldDBName, dbName, err)
			}
		}
		if !swapCompleted {
			k.log.Infof("Cleaning up temporary database %q after failed restore", tempDBName)
			if err := k.execDBCommand(ctx, "postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, tempDBName)); err != nil {
				k.log.Warnf("Failed to drop temporary database %q: %v", tempDBName, err)
			}
		}
	}()

	k.log.Infof("Restoring dump from %s into %q", dumpPath, tempDBName)
	if err := k.execRestoreDump(ctx, tempDBName, dumpPath); err != nil {
		return fmt.Errorf("failed to restore dump into %q: %w", tempDBName, err)
	}

	// Terminate active connections to the target database (escape single quotes to prevent SQL injection)
	if err := k.execDBCommand(ctx, "postgres",
		fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, strings.ReplaceAll(dbName, "'", "''")),
	); err != nil {
		k.log.Warnf("Failed to terminate connections to %q (continuing): %v", dbName, err)
	}

	k.log.Infof("Renaming %q → %q", dbName, oldDBName)
	if err := k.execDBCommand(ctx, "postgres", fmt.Sprintf(`ALTER DATABASE "%s" RENAME TO "%s"`, dbName, oldDBName)); err != nil {
		return fmt.Errorf("failed to rename %q to %q: %w", dbName, oldDBName, err)
	}
	liveDBRenamed = true

	k.log.Infof("Renaming %q → %q", tempDBName, dbName)
	if err := k.execDBCommand(ctx, "postgres", fmt.Sprintf(`ALTER DATABASE "%s" RENAME TO "%s"`, tempDBName, dbName)); err != nil {
		return fmt.Errorf("failed to rename %q to %q: %w", tempDBName, dbName, err)
	}
	swapCompleted = true
	restoreSucceeded = true

	if k.keepOldDB {
		k.log.Infof("Database restore completed. Pre-restore database preserved as %q", oldDBName)
	} else {
		k.log.Infof("Dropping pre-restore database %q", oldDBName)
		if err := k.execDBCommand(ctx, "postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, oldDBName)); err != nil {
			k.log.Warnf("Failed to drop pre-restore database %q (manual cleanup may be required): %v", oldDBName, err)
		}
	}

	k.log.Info("Database restore completed successfully")
	return nil
}

// execDBCommand runs a single SQL statement inside deploy/flightctl-db as the
// postgres OS user (UNIX socket auth, no password needed).
func (k *KubernetesRestoreDeployer) execDBCommand(ctx context.Context, dbName, sql string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "exec",
		"-n", k.internalNamespace, "deploy/flightctl-db", "--",
		"psql", "-U", "postgres", "-d", dbName, "-c", sql)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl exec psql failed: %w (output: %s)", err, out.String())
	}
	return nil
}

// execRestoreDump streams dumpPath into psql inside deploy/flightctl-db as the
// postgres OS user.
func (k *KubernetesRestoreDeployer) execRestoreDump(ctx context.Context, dbName, dumpPath string) error {
	dumpFile, err := os.Open(dumpPath)
	if err != nil {
		return fmt.Errorf("failed to open dump file %q: %w", dumpPath, err)
	}
	defer dumpFile.Close()

	cmd := exec.CommandContext(ctx, "kubectl", "exec", "-i",
		"-n", k.internalNamespace, "deploy/flightctl-db", "--",
		"psql", "-U", "postgres", "-d", dbName, "-v", "ON_ERROR_STOP=1", "-f", "-")
	cmd.Stdin = dumpFile
	cmd.Stdout = os.Stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl exec psql restore failed: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

const (
	// portForwardReadyTimeout is the maximum time to wait for a kubectl port-forward
	// tunnel to become reachable before returning an error.
	portForwardReadyTimeout = 30 * time.Second
	portForwardPollInterval = 200 * time.Millisecond
)

// ExposeService starts a kubectl port-forward to the named service and returns
// the localhost address and a free local port. cleanup() must be deferred.
// Supported service names: "flightctl-db", "flightctl-kv".
// Well-known default ports (5432/6379) are used as the port-forward target.
// The function blocks until the tunnel accepts TCP connections (up to portForwardReadyTimeout).
func (k *KubernetesRestoreDeployer) ExposeService(ctx context.Context, serviceName string) (string, int, func(), error) {
	var targetPort int
	switch serviceName {
	case "flightctl-db":
		targetPort = dbInternalPort
	case "flightctl-kv":
		targetPort = kvInternalPort
	default:
		return "", 0, func() {}, fmt.Errorf("unknown service %q: must be flightctl-db or flightctl-kv", serviceName)
	}

	localPort, err := getFreePort()
	if err != nil {
		return "", 0, func() {}, fmt.Errorf("failed to get free local port for %s: %w", serviceName, err)
	}

	portMapping := fmt.Sprintf("%d:%d", localPort, targetPort)
	cmd := exec.Command("kubectl", "port-forward", "-n", k.internalNamespace, "svc/"+serviceName, portMapping)
	if err := cmd.Start(); err != nil {
		return "", 0, func() {}, fmt.Errorf("failed to start port-forward for %s: %w", serviceName, err)
	}

	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}

	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	deadline := time.Now().Add(portForwardReadyTimeout)
	for {
		if err := ctx.Err(); err != nil {
			cleanup()
			return "", 0, func() {}, fmt.Errorf("context cancelled waiting for port-forward to %s: %w", serviceName, err)
		}
		conn, dialErr := net.DialTimeout("tcp", addr, portForwardPollInterval)
		if dialErr == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cleanup()
			return "", 0, func() {}, fmt.Errorf("timed out after %s waiting for port-forward to %s to be ready: %w", portForwardReadyTimeout, serviceName, dialErr)
		}
		time.Sleep(portForwardPollInterval)
	}

	k.log.Infof("Port-forwarding %s to localhost:%d", serviceName, localPort)
	return "127.0.0.1", localPort, cleanup, nil
}

// RestorePKI reads each *.yaml file from the pki/ subdirectory of the extracted
// archive and creates or updates the corresponding Kubernetes Secret via the Go
// client. Returns an error if pki/ is absent or contains no YAML files.
func (k *KubernetesRestoreDeployer) RestorePKI(ctx context.Context, extractDir string) error {
	pkiSrcDir := filepath.Join(extractDir, "pki")
	if _, err := os.Stat(pkiSrcDir); os.IsNotExist(err) {
		return fmt.Errorf("PKI materials missing from archive: pki/ directory not found in %s", extractDir)
	} else if err != nil {
		return fmt.Errorf("failed to access PKI directory in archive: %w", err)
	}

	yamlFiles, err := filepath.Glob(filepath.Join(pkiSrcDir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("failed to list PKI YAML files: %w", err)
	}
	if len(yamlFiles) == 0 {
		return fmt.Errorf("PKI materials missing from archive: no Secret YAML files found in pki/ directory")
	}

	clientset, err := k.resolveClientset()
	if err != nil {
		return fmt.Errorf("failed to resolve Kubernetes clientset for PKI restore: %w", err)
	}

	for _, yamlPath := range yamlFiles {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("PKI restore cancelled: %w", err)
		}
		if err := k.applySecretFromFile(ctx, clientset, yamlPath); err != nil {
			return fmt.Errorf("failed to apply PKI Secret %s: %w", filepath.Base(yamlPath), err)
		}
		k.log.Infof("Restored PKI Secret from %s", filepath.Base(yamlPath))
	}

	k.log.Infof("PKI restore completed. Applied %d Secrets", len(yamlFiles))
	return nil
}

// RestoreEncryptionKeys restores the data-at-rest encryption key Secret from the archive.
// Applies <extractDir>/encryption/flightctl-encryption-key.yaml to the release namespace,
// and duplicates it to the internal namespace if different.
// Logs a warning and skips if the encryption directory is absent (backwards compatible
// with pre-encryption backups).
func (k *KubernetesRestoreDeployer) RestoreEncryptionKeys(ctx context.Context, extractDir string) error {
	const expectedSecretName = "flightctl-encryption-key"

	encKeyPath := filepath.Join(extractDir, "encryption", expectedSecretName+".yaml")

	if _, err := os.Stat(encKeyPath); os.IsNotExist(err) {
		k.log.Warnf("No encryption key in archive, skipping encryption key restore. If this deployment uses data-at-rest encryption, encrypted database fields will be unrecoverable.")
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to access encryption key in archive: %w", err)
	}

	data, err := os.ReadFile(encKeyPath)
	if err != nil {
		return fmt.Errorf("failed to read encryption key Secret: %w", err)
	}
	var secret corev1.Secret
	if err := yaml.Unmarshal(data, &secret); err != nil {
		return fmt.Errorf("failed to unmarshal encryption key Secret: %w", err)
	}
	if secret.Name != expectedSecretName {
		return fmt.Errorf("unexpected Secret name %q in encryption key archive (expected %q)", secret.Name, expectedSecretName)
	}
	if len(secret.Data) == 0 {
		return fmt.Errorf("encryption key Secret %q has no data — refusing to overwrite live key material", expectedSecretName)
	}

	clientset, err := k.resolveClientset()
	if err != nil {
		return fmt.Errorf("failed to resolve Kubernetes clientset for encryption key restore: %w", err)
	}

	if err := k.applySecret(ctx, clientset, &secret, k.namespace); err != nil {
		return fmt.Errorf("failed to apply encryption key Secret: %w", err)
	}
	k.log.Infof("Restored encryption key Secret to namespace %s", k.namespace)

	// Duplicate to internal namespace if it differs from release namespace.
	// The Helm hook creates the encryption key in both namespaces; restore must do the same.
	if k.internalNamespace != "" && k.internalNamespace != k.namespace {
		if err := k.applySecret(ctx, clientset, &secret, k.internalNamespace); err != nil {
			return fmt.Errorf("failed to apply encryption key Secret to internal namespace %s: %w", k.internalNamespace, err)
		}
		k.log.Infof("Duplicated encryption key Secret to internal namespace %s", k.internalNamespace)
	}

	return nil
}

// applySecretFromFile reads a Secret YAML file, unmarshals it, and creates or
// updates the Secret in the cluster. The namespace is taken from the YAML
// metadata; if absent it falls back to k.namespace.
func (k *KubernetesRestoreDeployer) applySecretFromFile(ctx context.Context, clientset kubernetes.Interface, yamlPath string) error {
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	var secret corev1.Secret
	if err := yaml.Unmarshal(data, &secret); err != nil {
		return fmt.Errorf("failed to unmarshal Secret YAML: %w", err)
	}

	ns := secret.Namespace
	if ns == "" {
		ns = k.namespace
	}

	return k.applySecret(ctx, clientset, &secret, ns)
}

// applySecret creates or updates a Secret in the given namespace.
// Server-assigned fields (ResourceVersion, UID, ManagedFields) are cleared so
// that create and update both work regardless of prior cluster state.
func (k *KubernetesRestoreDeployer) applySecret(ctx context.Context, clientset kubernetes.Interface, secret *corev1.Secret, ns string) error {
	secret.Namespace = ns
	secret.ResourceVersion = ""
	secret.UID = ""
	secret.Generation = 0
	secret.ManagedFields = nil

	_, err := clientset.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create Secret %s/%s: %w", ns, secret.Name, err)
	}

	// Secret already exists — fetch its current ResourceVersion for the update.
	existing, err := clientset.CoreV1().Secrets(ns).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get existing Secret %s/%s: %w", ns, secret.Name, err)
	}
	secret.ResourceVersion = existing.ResourceVersion
	if _, err := clientset.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update Secret %s/%s: %w", ns, secret.Name, err)
	}
	return nil
}

// getFreePort asks the OS for a free local TCP port by binding to :0.
// RestoreConfig applies the backed-up Helm release Secret to the cluster, then
// reconstructs the chart and user values from the release data and runs helm upgrade.
func (k *KubernetesRestoreDeployer) RestoreConfig(ctx context.Context, extractDir string) error {
	configDir := filepath.Join(extractDir, "config")
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return fmt.Errorf("failed to list config directory in archive: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		secretFile := filepath.Join(configDir, entry.Name())
		if applyErr := k.applySecretFromFile(ctx, k.clientset, secretFile); applyErr != nil {
			return fmt.Errorf("failed to apply config Secret %s: %w", entry.Name(), applyErr)
		}
		k.log.Infof("Applied Helm release Secret from %s", entry.Name())
	}

	return k.restoreHelmRelease(ctx, extractDir)
}

// restoreHelmRelease decodes the backed-up Helm release Secret, reconstructs the
// chart directory and values file, and runs helm upgrade to bring the cluster state
// in line with the backed-up configuration.
func (k *KubernetesRestoreDeployer) restoreHelmRelease(ctx context.Context, extractDir string) error {
	configDir := filepath.Join(extractDir, "config")
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return fmt.Errorf("failed to list config directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		secretFile := filepath.Join(configDir, entry.Name())
		data, err := os.ReadFile(secretFile)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", secretFile, err)
		}

		var secret corev1.Secret
		if err := yaml.Unmarshal(data, &secret); err != nil {
			k.log.Debugf("Skipping %s: not a valid Secret YAML: %v", entry.Name(), err)
			continue
		}
		releaseBytes, ok := secret.Data["release"]
		if !ok {
			k.log.Debugf("Skipping %s: no 'release' key in Secret data", entry.Name())
			continue
		}

		// Helm encodes release data as base64(gzip(json(release))).
		gzipBytes, err := base64.StdEncoding.DecodeString(string(releaseBytes))
		if err != nil {
			return fmt.Errorf("failed to base64-decode Helm release from %s: %w", entry.Name(), err)
		}
		gr, err := gzip.NewReader(bytes.NewReader(gzipBytes))
		if err != nil {
			return fmt.Errorf("failed to gunzip Helm release from %s: %w", entry.Name(), err)
		}
		defer gr.Close()

		var release helmReleaseJSON
		if err := json.NewDecoder(gr).Decode(&release); err != nil {
			return fmt.Errorf("failed to decode Helm release JSON from %s: %w", entry.Name(), err)
		}

		chartDir, err := os.MkdirTemp("", "flightctl-helm-chart-*")
		if err != nil {
			return fmt.Errorf("failed to create temp chart directory: %w", err)
		}
		defer os.RemoveAll(chartDir)

		if err := k.writeHelmChart(chartDir, &release); err != nil {
			return fmt.Errorf("failed to reconstruct Helm chart: %w", err)
		}

		valuesFile, err := os.CreateTemp("", "flightctl-helm-values-*.yaml")
		if err != nil {
			return fmt.Errorf("failed to create temp values file: %w", err)
		}
		valuesFile.Close()
		defer os.Remove(valuesFile.Name())

		userValues, err := yaml.Marshal(release.Config)
		if err != nil {
			return fmt.Errorf("failed to marshal user values: %w", err)
		}
		if err := os.WriteFile(valuesFile.Name(), userValues, 0600); err != nil {
			return fmt.Errorf("failed to write user values file: %w", err)
		}

		ns := release.Namespace
		if ns == "" {
			ns = k.namespace
		}
		if err := k.helmUpgradeFunc(ctx, release.Name, chartDir, ns, valuesFile.Name()); err != nil {
			return fmt.Errorf("helm upgrade for release %q failed: %w", release.Name, err)
		}
		k.log.Infof("Helm release %q upgraded successfully", release.Name)
		return nil
	}

	k.log.Warn("No Helm release Secret found in archive config — skipping helm upgrade")
	return nil
}

// writeHelmChart reconstructs a chart directory structure from decoded Helm release data.
func (k *KubernetesRestoreDeployer) writeHelmChart(chartDir string, release *helmReleaseJSON) error {
	if release.Chart == nil {
		return fmt.Errorf("Helm release contains no chart data")
	}

	if release.Chart.Metadata != nil {
		chartYAML, err := yaml.Marshal(release.Chart.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal Chart.yaml: %w", err)
		}
		if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), chartYAML, 0600); err != nil {
			return fmt.Errorf("failed to write Chart.yaml: %w", err)
		}
	}

	if release.Chart.Values != nil {
		defaultValues, err := yaml.Marshal(release.Chart.Values)
		if err != nil {
			return fmt.Errorf("failed to marshal chart default values: %w", err)
		}
		if err := os.WriteFile(filepath.Join(chartDir, "values.yaml"), defaultValues, 0600); err != nil {
			return fmt.Errorf("failed to write chart values.yaml: %w", err)
		}
	}

	for _, tpl := range release.Chart.Templates {
		dstPath, err := safeChartPath(chartDir, tpl.Name)
		if err != nil {
			return fmt.Errorf("unsafe template path %q: %w", tpl.Name, err)
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0700); err != nil {
			return fmt.Errorf("failed to create template directory for %s: %w", tpl.Name, err)
		}
		if err := os.WriteFile(dstPath, tpl.Data, 0600); err != nil {
			return fmt.Errorf("failed to write template %s: %w", tpl.Name, err)
		}
	}

	for _, f := range release.Chart.Files {
		dstPath, err := safeChartPath(chartDir, f.Name)
		if err != nil {
			return fmt.Errorf("unsafe chart file path %q: %w", f.Name, err)
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0700); err != nil {
			return fmt.Errorf("failed to create chart file directory for %s: %w", f.Name, err)
		}
		if err := os.WriteFile(dstPath, f.Data, 0600); err != nil {
			return fmt.Errorf("failed to write chart file %s: %w", f.Name, err)
		}
	}

	return nil
}

// safeChartPath resolves name relative to baseDir and rejects paths that would
// escape the directory (absolute paths, leading "..", or paths with ".." components
// that leave baseDir after filepath.Clean).
func safeChartPath(baseDir, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("path must be relative")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes base directory")
	}
	resolved := filepath.Join(baseDir, clean)
	rel, err := filepath.Rel(baseDir, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path escapes base directory")
	}
	return resolved, nil
}

func getFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// setupExternalDBCerts extracts TLS certificates from Kubernetes ConfigMap/Secret and writes
// them to temporary files for external database connections. The config is updated to reference
// the temp file paths. Returns a cleanup function to delete the temp files when done.
//
// Based on Helm values structure:
//
//	db.external.tlsConfigMapName: postgres-ca-cert (CA certificate in ca-cert.pem key)
//	db.external.tlsSecretName: postgres-client-certs (client cert/key in tls.crt and tls.key)
//
// This is a Kubernetes-specific implementation detail, not part of the Deployer interface.
func (k *KubernetesRestoreDeployer) SetupExternalDBCerts(ctx context.Context, cfg *config.Config) (cleanup func(), err error) {
	// If no TLS root cert configured, nothing to extract
	if cfg.Database.SSLRootCert == "" {
		return nil, nil
	}

	// Get Helm release to find the ConfigMap/Secret names
	secrets, err := k.clientset.CoreV1().Secrets(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "owner=helm,status=deployed",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list Helm release Secrets: %w", err)
	}
	if len(secrets.Items) == 0 {
		// No Helm release found - can't determine TLS resource names
		return nil, nil
	}

	helmSecret := &secrets.Items[0]
	releaseBytes, ok := helmSecret.Data["release"]
	if !ok {
		return nil, fmt.Errorf("no 'release' key in Helm Secret")
	}

	// Helm encodes release data as base64(gzip(json(release)))
	gzipBytes, err := base64.StdEncoding.DecodeString(string(releaseBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to base64-decode Helm release: %w", err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(gzipBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to gunzip Helm release: %w", err)
	}
	defer gr.Close()

	var release helmReleaseJSON
	if err := json.NewDecoder(gr).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to decode Helm release JSON: %w", err)
	}

	// Extract db.external.tlsConfigMapName and db.external.tlsSecretName from Helm values
	var tlsConfigMapName, tlsSecretName string
	if release.Config != nil {
		if db, ok := release.Config["db"].(map[string]interface{}); ok {
			if external, ok := db["external"].(map[string]interface{}); ok {
				if name, ok := external["tlsConfigMapName"].(string); ok {
					tlsConfigMapName = name
				}
				if name, ok := external["tlsSecretName"].(string); ok {
					tlsSecretName = name
				}
			}
		}
	}

	if tlsConfigMapName == "" && tlsSecretName == "" {
		// No TLS resources configured in Helm values
		return nil, nil
	}

	// Create temp directory for certs
	tempDir, err := os.MkdirTemp("", "flightctl-db-certs-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory for certs: %w", err)
	}

	cleanup = func() {
		os.RemoveAll(tempDir)
	}

	// Extract CA cert from ConfigMap if configured
	if tlsConfigMapName != "" {
		caCM, err := k.clientset.CoreV1().ConfigMaps(k.namespace).Get(ctx, tlsConfigMapName, metav1.GetOptions{})
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to read TLS ConfigMap %s: %w", tlsConfigMapName, err)
		}

		caCert, ok := caCM.Data["ca-cert.pem"]
		if !ok {
			cleanup()
			return nil, fmt.Errorf("ca-cert.pem not found in ConfigMap %s", tlsConfigMapName)
		}

		caPath := filepath.Join(tempDir, "ca-cert.pem")
		if err := os.WriteFile(caPath, []byte(caCert), 0600); err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to write CA cert to temp file: %w", err)
		}
		cfg.Database.SSLRootCert = caPath
		k.log.Infof("Extracted CA certificate to %s", caPath)
	}

	// Extract client cert and key from Secret if configured
	if tlsSecretName != "" {
		certSecret, err := k.clientset.CoreV1().Secrets(k.namespace).Get(ctx, tlsSecretName, metav1.GetOptions{})
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to read TLS Secret %s: %w", tlsSecretName, err)
		}

		// Try common key names for client certificates
		clientCert, hasCert := certSecret.Data["tls.crt"]
		clientKey, hasKey := certSecret.Data["tls.key"]
		if !hasCert {
			// Try alternative key name
			clientCert, hasCert = certSecret.Data["client-cert.pem"]
		}
		if !hasKey {
			// Try alternative key name
			clientKey, hasKey = certSecret.Data["client-key.pem"]
		}
		if !hasCert || !hasKey {
			cleanup()
			return nil, fmt.Errorf("client certificate or key not found in Secret %s (expected tls.crt/tls.key or client-cert.pem/client-key.pem)", tlsSecretName)
		}

		certPath := filepath.Join(tempDir, "tls.crt")
		keyPath := filepath.Join(tempDir, "tls.key")
		if err := os.WriteFile(certPath, clientCert, 0600); err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to write client cert to temp file: %w", err)
		}
		if err := os.WriteFile(keyPath, clientKey, 0600); err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to write client key to temp file: %w", err)
		}

		cfg.Database.SSLCert = certPath
		cfg.Database.SSLKey = keyPath
		k.log.Infof("Extracted client certificate and key to %s and %s", certPath, keyPath)
	}

	return cleanup, nil
}
