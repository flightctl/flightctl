package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

// ErrExternalDatabase is returned when an external database is detected.
// External databases must be backed up separately by the user.
var ErrExternalDatabase = errors.New("external database detected: please back up the database separately using your database backup tools")

// DeploymentType represents the deployment environment type
type DeploymentType string

const (
	DeploymentTypePodman     DeploymentType = "podman"
	DeploymentTypeKubernetes DeploymentType = "kubernetes"
	DeploymentTypeUnknown    DeploymentType = "unknown"
)

// PKI backup permission modes
const (
	// pkiDirMode is the permission mode for PKI backup output directories.
	// 0700 (owner-only access) ensures PKI materials are only accessible
	// to the backup process owner.
	pkiDirMode os.FileMode = 0700

	// pkiFileMode is the permission mode for sensitive PKI files in backups.
	// 0600 (owner-only read/write) protects private keys and certificates
	// from unauthorized access.
	pkiFileMode os.FileMode = 0600
)

// Deployer interface for backup operations across deployment types
type Deployer interface {
	Type() DeploymentType
	BackupDatabase(ctx context.Context, outputDir string) error
	BackupPKI(ctx context.Context, outputDir string) error
	BackupConfig(ctx context.Context, outputDir string) error
}

// detectionIndicators holds environment detection check results
type detectionIndicators struct {
	podmanPKIExists       bool
	podmanConfigDirExists bool
	kubernetesEnvSet      bool
}

// DetectDeployment determines deployment type and returns appropriate deployer.
// basePath is the root directory for Podman deployment detection (default: /etc/flightctl).
// Pass empty string to use the default, or specify a custom path for testing.
func DetectDeployment(cfg *config.Config, log logrus.FieldLogger, basePath string) (Deployer, error) {
	if basePath == "" {
		basePath = "/etc/flightctl"
	}
	indicators := checkEnvironment(basePath)

	podmanDetected := indicators.podmanPKIExists || indicators.podmanConfigDirExists
	k8sDetected := indicators.kubernetesEnvSet

	// Validate mutual exclusivity
	if podmanDetected && k8sDetected {
		return nil, fmt.Errorf(
			"conflicting deployment indicators detected: "+
				"Podman indicators (pki=%v, config=%v) and Kubernetes indicator (env=%v); "+
				"unable to determine deployment type",
			indicators.podmanPKIExists,
			indicators.podmanConfigDirExists,
			indicators.kubernetesEnvSet,
		)
	}

	if podmanDetected {
		log.Debug("Podman deployment detected")
		return NewPodmanDeployer(cfg, log, ""), nil
	}

	if k8sDetected {
		log.Debug("Kubernetes deployment detected")
		return NewKubernetesDeployer(cfg, log, "", "", nil), nil
	}

	return nil, fmt.Errorf(
		"unable to detect deployment type: no Podman or Kubernetes indicators found " +
			"(checked: /etc/flightctl/pki/ca.crt, /etc/flightctl/, KUBERNETES_SERVICE_HOST)",
	)
}

// isInternalDB returns true if the database is internal (managed by FlightCtl deployment).
// Internal databases have hostnames: localhost, 127.0.0.1, flightctl-db, or flightctl-db.<namespace>
// patterns (including full DNS like flightctl-db.flightctl-internal.svc.cluster.local).
// External databases (any other hostname) must be backed up separately by the user.
func isInternalDB(cfg *config.Config) bool {
	if cfg == nil || cfg.Database == nil {
		return false
	}

	hostname := cfg.Database.Hostname
	switch hostname {
	case "localhost", "127.0.0.1", "flightctl-db":
		return true
	default:
		// Support flightctl-db in Kubernetes:
		// - flightctl-db.<namespace> (single namespace segment)
		// - flightctl-db.<namespace>.svc.cluster.local (full cluster DNS)
		// Reject anything else like flightctl-db.evil.com
		if strings.HasPrefix(hostname, "flightctl-db.") {
			suffix := strings.TrimPrefix(hostname, "flightctl-db.")
			// Check if it's full cluster DNS (ends with .svc.cluster.local)
			if strings.HasSuffix(suffix, ".svc.cluster.local") {
				return true
			}
			// Check if it's a simple namespace (no additional dots)
			if !strings.Contains(suffix, ".") {
				return true
			}
		}
		return false
	}
}

// checkEnvironment examines the system to detect deployment indicators.
// basePath is the root directory for Podman deployment detection (e.g., /etc/flightctl).
func checkEnvironment(basePath string) detectionIndicators {
	return detectionIndicators{
		podmanPKIExists:       pathExists(basePath + "/pki/ca.crt"),
		podmanConfigDirExists: pathExists(basePath),
		kubernetesEnvSet:      os.Getenv("KUBERNETES_SERVICE_HOST") != "",
	}
}

// pathExists checks if a path exists
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// shellEscape escapes a string for safe use in a shell command by wrapping it in single quotes
// and escaping any single quotes within the string.
func shellEscape(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}
