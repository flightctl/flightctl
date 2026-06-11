package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
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

// Detector probes the local environment to determine the deployment type.
// Both checker fields are optional: when nil the default implementations are used.
// Inject custom functions in tests to control detection without requiring live services.
//
//	d := &Detector{PodmanChecker: func() bool { return true }}
//	dt, err := d.Detect()
type Detector struct {
	// PodmanChecker reports whether the Podman FlightCtl service is active.
	// Default: podmanServiceIsActive (systemctl is-active flightctl-api.service).
	PodmanChecker func() bool
	// KubeconfigChecker reports whether a kubeconfig file is reachable.
	// Default: kubeconfigFileExists ($KUBECONFIG or ~/.kube/config).
	KubeconfigChecker func() bool
}

// Detect probes the environment and returns the detected deployment type.
// It does not create a deployer; call NewDeployerForType after detection.
// Mirrors the logic used by the e2e infrastructure (test/e2e/infra/factory.go autoDetect):
//   - Podman: flightctl-api.service is active via systemctl (with sudo fallback)
//   - Kubernetes: a kubeconfig file is reachable via $KUBECONFIG or ~/.kube/config
func (d *Detector) Detect() (DeploymentType, error) {
	podmanChecker := d.PodmanChecker
	if podmanChecker == nil {
		podmanChecker = podmanServiceIsActive
	}
	kubeconfigChecker := d.KubeconfigChecker
	if kubeconfigChecker == nil {
		kubeconfigChecker = kubeconfigFileExists
	}

	podmanActive := podmanChecker()
	kubeconfigPresent := kubeconfigChecker()

	if podmanActive && kubeconfigPresent {
		return DeploymentTypeUnknown, fmt.Errorf(
			"conflicting deployment indicators detected: " +
				"Podman (flightctl-api.service is active) and Kubernetes (kubeconfig present); " +
				"use --deployment-type to specify explicitly",
		)
	}

	if podmanActive {
		return DeploymentTypePodman, nil
	}

	if kubeconfigPresent {
		return DeploymentTypeKubernetes, nil
	}

	return DeploymentTypeUnknown, fmt.Errorf(
		"unable to detect deployment type:\n" +
			"  - no systemd service (flightctl-api.service is not active)\n" +
			"  - no kubeconfig (~/.kube/config or $KUBECONFIG)\n" +
			"\n" +
			"This usually means FlightCtl is running on a different host or in a VM.\n" +
			"\n" +
			"To run the backup:\n" +
			"  1. SSH into the host/VM where FlightCtl is running:\n" +
			"     ssh <user>@<host-or-vm>\n" +
			"\n" +
			"  2. Run the backup command with --deployment-type:\n" +
			"     flightctl-backup --output <directory> --deployment-type=podman\n" +
			"     (use --deployment-type=kubernetes for Kubernetes deployments)\n" +
			"\n" +
			"  3. Copy the backup archive to a safe location (off the VM)\n" +
			"\n" +
			"Example for quadlet/podman deployment in a VM:\n" +
			"  ssh root@my-flightctl-vm\n" +
			"  flightctl-backup --output /tmp/backup --deployment-type=podman\n" +
			"  exit\n" +
			"  scp root@my-flightctl-vm:/tmp/backup/*.tar.gz ./backups/",
	)
}

// DetectDeployment probes the environment using default implementations.
// It is a convenience wrapper around (&Detector{}).Detect().
func DetectDeployment() (DeploymentType, error) {
	return (&Detector{}).Detect()
}

// ValidateDeploymentType returns an error if s is not a recognised deployment type string.
func ValidateDeploymentType(s string) error {
	switch DeploymentType(s) {
	case DeploymentTypeKubernetes, DeploymentTypePodman:
		return nil
	default:
		return fmt.Errorf("invalid --deployment-type %q: must be %q or %q",
			s, DeploymentTypeKubernetes, DeploymentTypePodman)
	}
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

// podmanServiceIsActive returns true if the FlightCtl Podman service is running.
// Tries systemctl directly, then falls back to sudo systemctl — matching the
// detection strategy used by the e2e infrastructure (test/e2e/infra/factory.go autoDetect).
func podmanServiceIsActive() bool {
	if exec.Command("systemctl", "is-active", "flightctl-api.service").Run() == nil {
		return true
	}
	return exec.Command("sudo", "systemctl", "is-active", "flightctl-api.service").Run() == nil
}

// kubeconfigFileExists returns true if a kubeconfig file is reachable via $KUBECONFIG
// or the default ~/.kube/config location. This mirrors the detection logic used by the
// e2e infrastructure (test/e2e/infra/factory.go autoDetect).
func kubeconfigFileExists() bool {
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		_, err := os.Stat(kc)
		return err == nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".kube", "config"))
	return err == nil
}

// pathExists checks if a path exists
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ShellEscape escapes a string for safe use in a shell command by wrapping it in single quotes
// and escaping any single quotes within the string.
func ShellEscape(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}
