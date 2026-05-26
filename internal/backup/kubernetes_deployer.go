package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/yaml"
)

// pkiSecretNames lists all PKI/TLS Secrets required for backup.
// These Secrets contain CA keys, TLS certificates, and private keys needed
// for device mTLS reconnection after restore.
var pkiSecretNames = []string{
	"flightctl-ca",
	"flightctl-client-signer-ca",
	"flightctl-api-server-tls",
	"flightctl-telemetry-gateway-server-tls",
	"flightctl-alertmanager-proxy-server-tls",
	"flightctl-imagebuilder-api-server-tls",
	"flightctl-ui-server-tls",
	"flightctl-cli-artifacts-server-tls",
	"flightctl-ca-bundle",
	"flightctl-ui-certs",
	"flightctl-alertmanager-proxy-certs",
}

// KubernetesDeployer implements Deployer for Kubernetes/Helm deployments
type KubernetesDeployer struct {
	cfg               *config.Config
	log               logrus.FieldLogger
	namespace         string // For PKI Secrets (Release namespace, defaults to "flightctl")
	internalNamespace string // For DB pod (internal namespace, defaults to namespace if empty)
	helmReleaseName   string // Helm release name (defaults to "flightctl")
	clientset         kubernetes.Interface
}

// NewKubernetesDeployer creates a new Kubernetes deployer.
// namespace: For PKI Secrets (Release namespace). Defaults to "flightctl" if empty.
// internalNamespace: For DB pod (internal namespace). Defaults to namespace if empty (single-namespace deployment).
// helmReleaseName: Helm release name for values extraction. Defaults to "flightctl" if empty.
// clientset: If nil, creates in-cluster client automatically (use for production). Pass explicit clientset for testing.
func NewKubernetesDeployer(cfg *config.Config, log logrus.FieldLogger, namespace string, internalNamespace string, helmReleaseName string, clientset kubernetes.Interface) *KubernetesDeployer {
	// Set default namespace at construction time
	if namespace == "" {
		namespace = "flightctl"
	}
	// Default internal namespace to main namespace (single-namespace deployment)
	if internalNamespace == "" {
		internalNamespace = namespace
	}
	// Default Helm release name
	if helmReleaseName == "" {
		helmReleaseName = "flightctl"
	}
	return &KubernetesDeployer{
		cfg:               cfg,
		log:               log,
		namespace:         namespace,
		internalNamespace: internalNamespace,
		helmReleaseName:   helmReleaseName,
		clientset:         clientset,
	}
}

// Type returns the deployment type
func (k *KubernetesDeployer) Type() DeploymentType {
	return DeploymentTypeKubernetes
}

// BackupDatabase backs up the PostgreSQL database using pg_dump via Kubernetes client-go.
// For internal databases, executes pg_dump in the database pod and writes dump to <outputDir>/db/dump.sql.
// For external databases, returns ErrExternalDatabase without creating a backup.
func (k *KubernetesDeployer) BackupDatabase(ctx context.Context, outputDir string) error {
	// Check if database is external
	if !isInternalDB(k.cfg) {
		return ErrExternalDatabase
	}

	// Create db directory
	dbDir := filepath.Join(outputDir, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("failed to create db directory: %w", err)
	}

	// Use internal namespace for finding DB pod (defaults to namespace if not set)
	namespace := k.internalNamespace
	k.log.Debugf("Using namespace for DB pod: %s", namespace)

	labelSelector := "app=flightctl-db"

	// Get or create Kubernetes clientset
	clientset := k.clientset
	var config *rest.Config
	var err error

	if clientset == nil {
		// Create in-cluster client
		config, err = rest.InClusterConfig()
		if err != nil {
			return fmt.Errorf("failed to get in-cluster config: %w", err)
		}

		clientset, err = kubernetes.NewForConfig(config)
		if err != nil {
			return fmt.Errorf("failed to create Kubernetes client: %w", err)
		}
	} else {
		// Clientset was injected; still need config for SPDY executor
		config, err = rest.InClusterConfig()
		if err != nil {
			// In testing scenarios, this may fail; executor creation will fail later if needed
			k.log.Debugf("Could not get in-cluster config (clientset was injected): %v", err)
		}
	}

	// List pods with label selector
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
		Limit:         1,
	})
	if err != nil {
		return fmt.Errorf("failed to list database pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("database pod not found in namespace %s with label %s", namespace, labelSelector)
	}

	podName := pods.Items[0].Name
	k.log.Infof("Starting database backup from pod %s...", podName)

	// Build password from config
	password := string(k.cfg.Database.Password)

	// Execute pg_dump in pod with password from stdin and safely escaped parameters.
	// Use shell escaping to prevent injection attacks from user/database names.
	command := []string{
		"sh", "-c",
		fmt.Sprintf("PGPASSWORD=$(cat -) pg_dump -h 127.0.0.1 -p %s -U %s -d %s",
			shellEscape(strconv.Itoa(int(k.cfg.Database.Port))),
			shellEscape(k.cfg.Database.User),
			shellEscape(k.cfg.Database.Name)),
	}

	// Create exec request
	req := clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: command,
			Stdin:   true,
			Stdout:  true,
			Stderr:  true,
			TTY:     false,
		}, scheme.ParameterCodec)

	// Create dump file to stream output directly (avoids holding entire dump in memory)
	dumpFile := filepath.Join(dbDir, "dump.sql")
	outFile, err := os.OpenFile(dumpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create dump file: %w", err)
	}
	defer outFile.Close()

	// Create executor (requires in-cluster config)
	if config == nil {
		return fmt.Errorf("cannot create SPDY executor: in-cluster config not available")
	}
	executor, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("failed to create executor: %w", err)
	}

	// Prepare stdin and stderr; stream stdout directly to file
	stdin := strings.NewReader(password)
	var stderr bytes.Buffer

	// Execute command, streaming SQL dump directly to file
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: outFile,
		Stderr: &stderr,
	}); err != nil {
		return fmt.Errorf("pg_dump in pod failed: %w (stderr: %s)", err, stderr.String())
	}

	// Get file size for logging
	fileInfo, err := outFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat dump file: %w", err)
	}

	k.log.Infof("Database backup completed. Dump file size: %d bytes", fileInfo.Size())

	return nil
}

// BackupPKI backs up PKI materials by exporting all PKI/TLS Secrets to YAML files.
// Writes one YAML file per Secret to <outputDir>/pki/<secretName>.yaml.
// All YAML files are created with pkiFileMode permissions (contain sensitive data).
func (k *KubernetesDeployer) BackupPKI(ctx context.Context, outputDir string) error {
	// Use namespace (set in constructor, defaults to "flightctl")
	namespace := k.namespace
	k.log.Infof("Starting PKI backup from namespace %s...", namespace)

	// Get or create Kubernetes clientset
	clientset := k.clientset
	if clientset == nil {
		// Create in-cluster client
		config, err := rest.InClusterConfig()
		if err != nil {
			return fmt.Errorf("failed to get in-cluster config: %w", err)
		}

		var clientErr error
		clientset, clientErr = kubernetes.NewForConfig(config)
		if clientErr != nil {
			return fmt.Errorf("failed to create Kubernetes client: %w", clientErr)
		}
	}

	// Verify at least one required Secret exists before creating output directory
	_, err := clientset.CoreV1().Secrets(namespace).Get(ctx, pkiSecretNames[0], metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to verify PKI secrets exist (checked %s): %w", pkiSecretNames[0], err)
	}

	pkiDir := filepath.Join(outputDir, "pki")

	// Create PKI output directory only after validation
	if err := os.MkdirAll(pkiDir, pkiDirMode); err != nil {
		return fmt.Errorf("failed to create PKI output directory: %w", err)
	}

	// Clean up PKI directory on error (ensures all-or-nothing semantics)
	success := false
	defer func() {
		if !success {
			os.RemoveAll(pkiDir)
		}
	}()

	// Export each Secret to YAML
	for _, secretName := range pkiSecretNames {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return fmt.Errorf("PKI backup cancelled: %w", ctx.Err())
		default:
		}

		k.log.Debugf("Exporting Secret: %s", secretName)

		// Get Secret from Kubernetes API
		secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get Secret %s: %w", secretName, err)
		}

		// Marshal to YAML
		yamlBytes, err := yaml.Marshal(secret)
		if err != nil {
			return fmt.Errorf("failed to marshal Secret %s to YAML: %w", secretName, err)
		}

		// Write YAML file with restrictive permissions
		yamlPath := filepath.Join(pkiDir, secretName+".yaml")
		if err := os.WriteFile(yamlPath, yamlBytes, pkiFileMode); err != nil {
			return fmt.Errorf("failed to write Secret YAML %s: %w", secretName, err)
		}
	}

	k.log.Infof("PKI backup completed. Backed up %d Secrets to %s", len(pkiSecretNames), pkiDir)

	success = true
	return nil
}

// BackupConfig backs up Helm release configuration by exporting the Helm release Secret.
// The Secret contains the complete release state: chart (templates, values), config (user values), and manifest.
// Exports to <outputDir>/config/helm-release-<name>-v<revision>.yaml.
//
// Why backup the Helm Secret (not ConfigMaps):
// - ConfigMaps are generated artifacts from chart + user values
// - Helm Secret is the source of truth for the release
// - Backing up ConfigMaps directly would be overwritten on next helm upgrade
//
// Restore process:
// 1. Apply the Secret: kubectl apply -f helm-release-<name>-v<revision>.yaml
// 2. Run helm upgrade: helm upgrade <release> <chart> -n <namespace>
//   - Helm sees the existing release (from the restored Secret)
//   - Re-evaluates values and regenerates ConfigMaps automatically
//   - ConfigMaps are recreated with correct configuration
//
// 3. ConfigMaps backup is not needed - they are regenerated by Helm
//
// Returns error if no Helm release Secrets are found.
func (k *KubernetesDeployer) BackupConfig(ctx context.Context, outputDir string) error {
	// Create config directory
	configDir := filepath.Join(outputDir, "config")
	if err := os.MkdirAll(configDir, pkiDirMode); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Use namespace (set in constructor, defaults to "flightctl")
	namespace := k.namespace
	releaseName := k.helmReleaseName

	k.log.Infof("Starting Helm release backup from namespace %s...", namespace)

	// Get or create Kubernetes clientset
	clientset := k.clientset
	if clientset == nil {
		// Create in-cluster client
		config, err := rest.InClusterConfig()
		if err != nil {
			return fmt.Errorf("failed to get in-cluster config: %w", err)
		}

		var clientErr error
		clientset, clientErr = kubernetes.NewForConfig(config)
		if clientErr != nil {
			return fmt.Errorf("failed to create Kubernetes client: %w", clientErr)
		}
	}

	// List Helm release Secrets
	// Helm 3 stores releases as Secrets with labels: owner=helm, name=<release-name>, status=deployed|superseded
	// First try to get the deployed release directly
	deployedSelector := fmt.Sprintf("owner=helm,name=%s,status=deployed", releaseName)

	secrets, err := clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: deployedSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list Helm release Secrets with selector %s: %w", deployedSelector, err)
	}

	if len(secrets.Items) == 0 {
		return fmt.Errorf("no deployed Helm release Secret found for release %s in namespace %s (selector: %s)", releaseName, namespace, deployedSelector)
	}

	// Use the deployed release Secret
	deployedSecret := &secrets.Items[0]

	k.log.Infof("Found deployed Helm release Secret: %s", deployedSecret.Name)

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return fmt.Errorf("config backup cancelled: %w", ctx.Err())
	default:
	}

	// Marshal Helm Secret to YAML
	yamlBytes, err := yaml.Marshal(deployedSecret)
	if err != nil {
		return fmt.Errorf("failed to marshal Helm Secret %s to YAML: %w", deployedSecret.Name, err)
	}

	// Write Helm Secret to file
	// The Secret contains the entire release state: chart, config (values), and manifest.
	// At restore: kubectl apply restores the Secret, then helm upgrade regenerates ConfigMaps.
	helmSecretPath := filepath.Join(configDir, fmt.Sprintf("helm-release-%s.yaml", releaseName))
	if err := os.WriteFile(helmSecretPath, yamlBytes, pkiFileMode); err != nil {
		return fmt.Errorf("failed to write Helm Secret YAML %s: %w", helmSecretPath, err)
	}

	k.log.Infof("Helm release backup completed. Secret: %s (contains chart + config + manifest)", deployedSecret.Name)
	return nil
}
