package backup

import (
	"context"

	"github.com/sirupsen/logrus"
)

// KubernetesDeployer implements Deployer for Kubernetes/Helm deployments
type KubernetesDeployer struct {
	log logrus.FieldLogger
}

// NewKubernetesDeployer creates a new Kubernetes deployer
func NewKubernetesDeployer(log logrus.FieldLogger) *KubernetesDeployer {
	return &KubernetesDeployer{log: log}
}

// Type returns the deployment type
func (k *KubernetesDeployer) Type() DeploymentType {
	return DeploymentTypeKubernetes
}

// BackupDatabase is a stub (implemented in EDM-3890)
func (k *KubernetesDeployer) BackupDatabase(ctx context.Context, outputDir string) error {
	k.log.Debug("BackupDatabase called (stub implementation)")
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
