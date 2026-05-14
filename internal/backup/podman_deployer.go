package backup

import (
	"context"

	"github.com/sirupsen/logrus"
)

// PodmanDeployer implements Deployer for Podman/quadlet deployments
type PodmanDeployer struct {
	log logrus.FieldLogger
}

// NewPodmanDeployer creates a new Podman deployer
func NewPodmanDeployer(log logrus.FieldLogger) *PodmanDeployer {
	return &PodmanDeployer{log: log}
}

// Type returns the deployment type
func (p *PodmanDeployer) Type() DeploymentType {
	return DeploymentTypePodman
}

// BackupDatabase is a stub (implemented in EDM-3890)
func (p *PodmanDeployer) BackupDatabase(ctx context.Context, outputDir string) error {
	p.log.Debug("BackupDatabase called (stub implementation)")
	return nil
}

// BackupPKI is a stub (implemented in EDM-3891)
func (p *PodmanDeployer) BackupPKI(ctx context.Context, outputDir string) error {
	p.log.Debug("BackupPKI called (stub implementation)")
	return nil
}

// BackupConfig is a stub (implemented in EDM-3892)
func (p *PodmanDeployer) BackupConfig(ctx context.Context, outputDir string) error {
	p.log.Debug("BackupConfig called (stub implementation)")
	return nil
}
