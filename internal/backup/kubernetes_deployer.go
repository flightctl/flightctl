package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/yaml"
)

const (
	// dbAppSecretName is the Kubernetes Secret containing the application DB credentials.
	dbAppSecretName = "flightctl-db-app-secret"
	dbUserKey       = "user"
	dbPasswordKey   = "userPassword"
	// dbDefaultName and dbDefaultPort are the well-known values used by the FlightCtl Helm chart.
	dbDefaultName = "flightctl"
	dbDefaultPort = 5432
)

// requiredPKISecretNames lists PKI/TLS Secrets that must exist in every deployment.
// Missing any of these is a hard error.
var requiredPKISecretNames = []string{
	"flightctl-ca",
	"flightctl-client-signer-ca",
	"flightctl-ca-bundle",
	"flightctl-api-server-tls",
	"flightctl-telemetry-gateway-server-tls",
	"flightctl-alertmanager-proxy-server-tls",
	"flightctl-imagebuilder-api-server-tls",
}

// optionalPKISecretNames lists PKI/TLS Secrets for optional services.
// Controlled by Helm values: ui.enabled, cliArtifacts.enabled, alertmanagerProxy.enabled.
// Missing secrets are silently skipped.
var optionalPKISecretNames = []string{
	"flightctl-ui-server-tls",            // ui.enabled
	"flightctl-ui-certs",                 // ui.enabled
	"flightctl-cli-artifacts-server-tls", // cliArtifacts.enabled
	"flightctl-alertmanager-proxy-certs", // alertmanagerProxy.enabled
}

// KubernetesDeployer implements Deployer for Kubernetes/Helm deployments
type KubernetesDeployer struct {
	log               logrus.FieldLogger
	namespace         string // For PKI Secrets (Release namespace, defaults to "flightctl")
	internalNamespace string // For DB pod (internal namespace, defaults to namespace if empty)
	helmReleaseName   string // Helm release name (defaults to namespace if empty)
	clientset         kubernetes.Interface
	restCfg           *rest.Config
}

// KubernetesDeployerOption configures a KubernetesDeployer.
type KubernetesDeployerOption func(*KubernetesDeployer)

// WithNamespace sets the Kubernetes namespace for PKI Secrets (Release namespace).
func WithNamespace(ns string) KubernetesDeployerOption {
	return func(d *KubernetesDeployer) {
		d.namespace = ns
	}
}

// WithInternalNamespace sets the Kubernetes namespace for the DB pod.
func WithInternalNamespace(ns string) KubernetesDeployerOption {
	return func(d *KubernetesDeployer) {
		d.internalNamespace = ns
	}
}

// WithHelmReleaseName sets the Helm release name for values extraction.
func WithHelmReleaseName(name string) KubernetesDeployerOption {
	return func(d *KubernetesDeployer) {
		d.helmReleaseName = name
	}
}

// WithClientset injects a Kubernetes clientset (for testing).
func WithClientset(cs kubernetes.Interface) KubernetesDeployerOption {
	return func(d *KubernetesDeployer) {
		d.clientset = cs
	}
}

// WithRestConfig injects a REST config (for testing).
func WithRestConfig(cfg *rest.Config) KubernetesDeployerOption {
	return func(d *KubernetesDeployer) {
		d.restCfg = cfg
	}
}

// NewKubernetesDeployer creates a new Kubernetes deployer.
// Defaults: namespace "flightctl"; internalNamespace inherits namespace; helmReleaseName is
// left empty for auto-discovery (see resolveHelmReleaseSecret).
// clientset nil → in-cluster client is created on first use.
func NewKubernetesDeployer(log logrus.FieldLogger, opts ...KubernetesDeployerOption) *KubernetesDeployer {
	d := &KubernetesDeployer{
		log: log,
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

// Type returns the deployment type
func (k *KubernetesDeployer) Type() DeploymentType {
	return DeploymentTypeKubernetes
}

func (k *KubernetesDeployer) resolveRestConfig() (*rest.Config, error) {
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

func (k *KubernetesDeployer) resolveClientset() (kubernetes.Interface, error) {
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

// getSecretStringValue reads a single key from a Kubernetes Secret and returns
// the value as a plain string (Secret.Data values are already decoded from base64).
func (k *KubernetesDeployer) getSecretStringValue(ctx context.Context, ns, name, key string) (string, error) {
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

// BackupDatabase backs up the PostgreSQL database using pg_dump via Kubernetes client-go.
// Credentials are read from the flightctl-db-app-secret Kubernetes Secret in internalNamespace.
// If that Secret does not exist, the database is assumed to be external and ErrExternalDatabase
// is returned without creating a backup.
func (k *KubernetesDeployer) BackupDatabase(ctx context.Context, outputDir string) error {
	// Extract DB credentials from the cluster Secret.  A missing Secret means
	// the DB is externally managed; any other error is propagated.
	dbUser, err := k.getSecretStringValue(ctx, k.internalNamespace, dbAppSecretName, dbUserKey)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ErrExternalDatabase
		}
		return fmt.Errorf("failed to get database credentials: %w", err)
	}
	dbPassword, err := k.getSecretStringValue(ctx, k.internalNamespace, dbAppSecretName, dbPasswordKey)
	if err != nil {
		return fmt.Errorf("failed to get database password: %w", err)
	}

	// Create db directory
	dbDir := filepath.Join(outputDir, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("failed to create db directory: %w", err)
	}

	// Use internal namespace for finding DB pod (defaults to namespace if not set)
	namespace := k.internalNamespace
	k.log.Debugf("Using namespace for DB pod: %s", namespace)

	labelSelector := "flightctl.service=flightctl-db"

	clientset, err := k.resolveClientset()
	if err != nil {
		return err
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

	config, err := k.resolveRestConfig()
	if err != nil {
		return err
	}

	podName := pods.Items[0].Name
	k.log.Infof("Starting database backup from pod %s...", podName)

	// Execute pg_dump in pod with password from stdin and safely escaped parameters.
	// Use shell escaping to prevent injection attacks from user/database names.
	command := []string{
		"sh", "-c",
		fmt.Sprintf("PGPASSWORD=$(cat -) pg_dump --clean --if-exists -h 127.0.0.1 -p %s -U %s -d %s",
			ShellEscape(strconv.Itoa(dbDefaultPort)),
			ShellEscape(dbUser),
			ShellEscape(dbDefaultName)),
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

	executor, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("failed to create executor: %w", err)
	}

	// Prepare stdin and stderr; stream stdout directly to file
	stdin := strings.NewReader(dbPassword)
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
	namespace := k.namespace
	k.log.Infof("Starting PKI backup from namespace %s...", namespace)

	clientset, err := k.resolveClientset()
	if err != nil {
		return err
	}

	// Verify at least one required Secret exists before creating output directory
	_, err = clientset.CoreV1().Secrets(namespace).Get(ctx, requiredPKISecretNames[0], metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to verify PKI secrets exist (checked %s): %w", requiredPKISecretNames[0], err)
	}

	pkiDir := filepath.Join(outputDir, "pki")

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

	backedUp := 0
	exportSecret := func(secretName string, required bool) error {
		select {
		case <-ctx.Done():
			return fmt.Errorf("PKI backup cancelled: %w", ctx.Err())
		default:
		}

		secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			if !required && apierrors.IsNotFound(err) {
				k.log.Debugf("Optional Secret %s not found, skipping", secretName)
				return nil
			}
			return fmt.Errorf("failed to get Secret %s: %w", secretName, err)
		}

		yamlBytes, err := yaml.Marshal(secret)
		if err != nil {
			return fmt.Errorf("failed to marshal Secret %s to YAML: %w", secretName, err)
		}

		yamlPath := filepath.Join(pkiDir, secretName+".yaml")
		if err := os.WriteFile(yamlPath, yamlBytes, pkiFileMode); err != nil {
			return fmt.Errorf("failed to write Secret YAML %s: %w", secretName, err)
		}
		backedUp++
		return nil
	}

	for _, name := range requiredPKISecretNames {
		if err := exportSecret(name, true); err != nil {
			return err
		}
	}
	for _, name := range optionalPKISecretNames {
		if err := exportSecret(name, false); err != nil {
			return err
		}
	}

	k.log.Infof("PKI backup completed. Backed up %d Secrets to %s", backedUp, pkiDir)

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
// Returns error if no Helm release Secrets are found or if multiple are found without an explicit release name.
func (k *KubernetesDeployer) BackupConfig(ctx context.Context, outputDir string) error {
	configDir := filepath.Join(outputDir, "config")
	if err := os.MkdirAll(configDir, pkiDirMode); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	namespace := k.namespace
	k.log.Infof("Starting Helm release backup from namespace %s...", namespace)

	clientset, err := k.resolveClientset()
	if err != nil {
		return err
	}

	deployedSecret, releaseName, err := k.resolveHelmReleaseSecret(ctx, clientset, namespace)
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("config backup cancelled: %w", ctx.Err())
	default:
	}

	yamlBytes, err := yaml.Marshal(deployedSecret)
	if err != nil {
		return fmt.Errorf("failed to marshal Helm Secret %s to YAML: %w", deployedSecret.Name, err)
	}

	helmSecretPath := filepath.Join(configDir, fmt.Sprintf("helm-release-%s.yaml", releaseName))
	if err := os.WriteFile(helmSecretPath, yamlBytes, pkiFileMode); err != nil {
		return fmt.Errorf("failed to write Helm Secret YAML %s: %w", helmSecretPath, err)
	}

	k.log.Infof("Helm release backup completed. Secret: %s (contains chart + config + manifest)", deployedSecret.Name)
	return nil
}

// resolveHelmReleaseSecret returns the deployed Helm release Secret.
// If helmReleaseName is set, it looks up that release directly.
// Otherwise it discovers all deployed releases in the namespace; if exactly one is found it is used,
// if multiple are found the caller must specify --helm-release-name to disambiguate.
func (k *KubernetesDeployer) resolveHelmReleaseSecret(ctx context.Context, clientset kubernetes.Interface, namespace string) (*corev1.Secret, string, error) {
	if k.helmReleaseName != "" {
		selector := fmt.Sprintf("owner=helm,name=%s,status=deployed", k.helmReleaseName)
		secrets, err := clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return nil, "", fmt.Errorf("failed to list Helm release Secrets with selector %s: %w", selector, err)
		}
		if len(secrets.Items) == 0 {
			return nil, "", fmt.Errorf("no deployed Helm release Secret found for release %q in namespace %s", k.helmReleaseName, namespace)
		}
		return &secrets.Items[0], k.helmReleaseName, nil
	}

	// Auto-discover: list all deployed Helm releases in the namespace.
	secrets, err := clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "owner=helm,status=deployed",
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to list deployed Helm release Secrets in namespace %s: %w", namespace, err)
	}
	if len(secrets.Items) == 0 {
		return nil, "", fmt.Errorf("no deployed Helm release found in namespace %s", namespace)
	}
	if len(secrets.Items) > 1 {
		names := make([]string, len(secrets.Items))
		for i, s := range secrets.Items {
			names[i] = s.Labels["name"]
		}
		return nil, "", fmt.Errorf("multiple deployed Helm releases found in namespace %s (%v): use --helm-release-name to specify which one to back up", namespace, names)
	}

	secret := &secrets.Items[0]
	releaseName := secret.Labels["name"]
	k.log.Infof("Auto-discovered Helm release %q in namespace %s", releaseName, namespace)
	return secret, releaseName, nil
}
