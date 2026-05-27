package restore

import (
	"bytes"
	"context"
	"fmt"
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

// defaultPodmanServiceNames lists every FlightCtl systemd unit that holds a
// database connection. They must all be stopped before the restore and started
// again afterwards. Units that do not connect to the DB (e.g. flightctl-db,
// flightctl-kv, flightctl-ui, flightctl-cli-artifacts) are intentionally omitted.
var defaultPodmanServiceNames = []string{
	"flightctl-api",
	"flightctl-worker",
	"flightctl-periodic",
	"flightctl-imagebuilder-api",
	"flightctl-imagebuilder-worker",
	"flightctl-alert-exporter",
	"flightctl-alertmanager-proxy",
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
	{Name: "flightctl-api",                Internal: false},
	{Name: "flightctl-worker",             Internal: true},
	{Name: "flightctl-periodic",           Internal: true},
	{Name: "flightctl-imagebuilder-api",   Internal: false},
	{Name: "flightctl-imagebuilder-worker", Internal: true},
	{Name: "flightctl-alert-exporter",     Internal: true},
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
}

// PodmanRestoreDeployer implements Deployer for Podman/quadlet deployments.
type PodmanRestoreDeployer struct {
	log               logrus.FieldLogger
	containerName     string
	containerCLI      string
	serviceHandler    ServiceHandler
	serviceConfigPath string
	dbName            string
	kvContainerName   string
	pkiDestPath       string
	keepOldDB         bool
	cachedCfg         *config.Config
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

// WithPodmanKeepOldDB controls whether the pre-restore database is dropped after
// a successful swap (false, default) or preserved under <dbname>_old_<timestamp> (true).
func WithPodmanKeepOldDB(keep bool) PodmanRestoreOption {
	return func(d *PodmanRestoreDeployer) {
		d.keepOldDB = keep
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

// GetConfig loads DB and KV credentials from the service configuration file.
// The result is cached after the first successful load.
func (p *PodmanRestoreDeployer) GetConfig(ctx context.Context) (*config.Config, error) {
	if p.cachedCfg != nil {
		return p.cachedCfg, nil
	}
	cfg, err := config.Load(p.serviceConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load service configuration from %s: %w", p.serviceConfigPath, err)
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

	if err := p.execDBCommand(ctx, "postgres",
		fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, dbName),
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

// ExposeService returns the original host and port from the service config for the named service.
// For flightctl-kv, when a published port is available on kvContainerName, that is used instead.
func (p *PodmanRestoreDeployer) ExposeService(ctx context.Context, serviceName string) (string, int, func(), error) {
	cfg, err := p.GetConfig(ctx)
	if err != nil {
		return "", 0, func() {}, err
	}
	switch serviceName {
	case "flightctl-db":
		return cfg.Database.Hostname, int(cfg.Database.Port), func() {}, nil
	case "flightctl-kv":
		if host, port, ok := containerPublishedTCPPort(ctx, p.containerCLI, p.kvContainerName, kvInternalPort); ok {
			return host, port, func() {}, nil
		}
		return cfg.KV.Hostname, int(cfg.KV.Port), func() {}, nil
	default:
		return "", 0, func() {}, fmt.Errorf("unknown service %q: must be flightctl-db or flightctl-kv", serviceName)
	}
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

	count := 0
	err := filepath.Walk(pkiSrcDir, func(srcPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("error walking PKI source directory: %w", walkErr)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		relPath, err := filepath.Rel(pkiSrcDir, srcPath)
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", srcPath, err)
		}
		dstPath := filepath.Join(p.pkiDestPath, relPath)

		if info.IsDir() {
			if relPath == "." {
				return nil
			}
			return os.MkdirAll(dstPath, info.Mode())
		}

		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("failed to read PKI file %s: %w", relPath, err)
		}
		if err := os.WriteFile(dstPath, data, info.Mode()); err != nil {
			return fmt.Errorf("failed to write PKI file %s: %w", relPath, err)
		}
		p.log.Debugf("Restored PKI file: %s", relPath)
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("PKI restore failed: %w", err)
	}

	p.log.Infof("PKI restore completed. Restored %d files to %s", count, p.pkiDestPath)
	return nil
}

// KubernetesRestoreDeployer implements Deployer for Kubernetes/Helm deployments.
type KubernetesRestoreDeployer struct {
	log               logrus.FieldLogger
	namespace         string // external namespace (api, ui, PKI secrets)
	internalNamespace string // internal namespace (worker, periodic, db)
	clientset         kubernetes.Interface
	restCfg           *rest.Config
	keepOldDB         bool
	// originalReplicas holds pre-stop replica counts so StartServices can restore them.
	originalReplicas map[string]int32
	cachedCfg        *config.Config
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

	cfg := config.NewDefault()
	cfg.Database.Hostname = "flightctl-db"
	cfg.Database.Port = dbInternalPort
	cfg.Database.Name = "flightctl"
	cfg.Database.User = dbUser
	cfg.Database.Password = api.SecureString(dbPassword)
	cfg.KV.Hostname = "flightctl-kv"
	cfg.KV.Port = kvInternalPort
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

	if err := k.execDBCommand(ctx, "postgres",
		fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, dbName),
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
	portForwardReadyTimeout  = 30 * time.Second
	portForwardPollInterval  = 200 * time.Millisecond
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

// applySecretFromFile reads a Secret YAML file, unmarshals it, and creates or
// updates the Secret in the cluster. The namespace is taken from the YAML
// metadata; if absent it falls back to k.namespace.
// Server-assigned fields (ResourceVersion, UID, ManagedFields) are cleared so
// that create and update both work regardless of prior cluster state.
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

	// Clear server-assigned fields so the object can be created or updated
	// without conflicts from stale metadata recorded at backup time.
	secret.ResourceVersion = ""
	secret.UID = ""
	secret.Generation = 0
	secret.ManagedFields = nil

	_, err = clientset.CoreV1().Secrets(ns).Create(ctx, &secret, metav1.CreateOptions{})
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
	if _, err := clientset.CoreV1().Secrets(ns).Update(ctx, &secret, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update Secret %s/%s: %w", ns, secret.Name, err)
	}
	return nil
}

// getFreePort asks the OS for a free local TCP port by binding to :0.
func getFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}
