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
)

// KubernetesDeployer implements Deployer for Kubernetes/Helm deployments
type KubernetesDeployer struct {
	cfg       *config.Config
	log       logrus.FieldLogger
	namespace string // Optional: if empty, defaults to "flightctl"
}

// NewKubernetesDeployer creates a new Kubernetes deployer.
// If namespace is empty, defaults to "flightctl" (production namespace).
func NewKubernetesDeployer(cfg *config.Config, log logrus.FieldLogger, namespace string) *KubernetesDeployer {
	return &KubernetesDeployer{
		cfg:       cfg,
		log:       log,
		namespace: namespace,
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

	// Determine namespace: use provided namespace, otherwise use production default.
	namespace := k.namespace
	if namespace == "" {
		namespace = "flightctl"
	}
	k.log.Debugf("Using namespace: %s", namespace)

	labelSelector := "app=flightctl-db"

	// Create Kubernetes clientset
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
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

	// Create executor
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

// BackupPKI is a stub (implemented in EDM-3891)
func (k *KubernetesDeployer) BackupPKI(ctx context.Context, outputDir string) error {
	k.log.Debug("BackupPKI called (stub implementation)")
	return nil
}

// BackupConfig is a stub (implemented in EDM-3892)
func (k *KubernetesDeployer) BackupConfig(ctx context.Context, outputDir string) error {
	k.log.Debug("BackupConfig called (stub implementation)")
	return nil
}

