package backup

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

// DeploymentType represents the deployment environment type
type DeploymentType string

const (
	DeploymentTypePodman     DeploymentType = "podman"
	DeploymentTypeKubernetes DeploymentType = "kubernetes"
	DeploymentTypeUnknown    DeploymentType = "unknown"
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

// DetectDeployment determines deployment type and returns appropriate deployer
func DetectDeployment(cfg *config.Config, log logrus.FieldLogger) (Deployer, error) {
	indicators := checkEnvironment()

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
		return NewPodmanDeployer(log), nil
	}

	if k8sDetected {
		log.Debug("Kubernetes deployment detected")
		return NewKubernetesDeployer(log), nil
	}

	return nil, fmt.Errorf(
		"unable to detect deployment type: no Podman or Kubernetes indicators found " +
			"(checked: /etc/flightctl/pki/ca.crt, /etc/flightctl/, KUBERNETES_SERVICE_HOST)",
	)
}

// flightctlBasePath is the base path for Podman deployment detection.
// Can be overridden in tests to avoid modifying global system paths.
var flightctlBasePath = "/etc/flightctl"

// checkEnvironment examines the system to detect deployment indicators
func checkEnvironment() detectionIndicators {
	return detectionIndicators{
		podmanPKIExists:       pathExists(flightctlBasePath + "/pki/ca.crt"),
		podmanConfigDirExists: pathExists(flightctlBasePath),
		kubernetesEnvSet:      os.Getenv("KUBERNETES_SERVICE_HOST") != "",
	}
}

// pathExists checks if a path exists
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
