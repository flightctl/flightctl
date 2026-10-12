package quadlet

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
)

var workerRegistryConfigs = []struct {
	serviceDir    string
	containerFile string
}{
	{
		serviceDir:    "flightctl-delta-worker",
		containerFile: "flightctl-delta-worker.container",
	},
	{
		serviceDir:    "flightctl-worker",
		containerFile: "flightctl-worker.container",
	},
}

const registryCertDropInName = "e2e-registry-ca.conf"

func (p *InfraProvider) ApplyDeltaWorkerRegistryRemap(ctx context.Context, registryURL string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remap, insecure := infra.DeltaWorkerRegistryRemapFiles(registryURL)
	caCert, err := infra.DeltaWorkerRegistryCACert()
	if err != nil {
		return err
	}
	certDir, err := infra.DeltaWorkerRegistryCertDir(registryURL)
	if err != nil {
		return err
	}
	for _, worker := range workerRegistryConfigs {
		registriesDir := filepath.Join(p.configDir, worker.serviceDir, "registries.conf.d")
		certsDir := filepath.Join(p.configDir, worker.serviceDir, "certs.d")
		if err := p.writeRegistriesDir(ctx, registriesDir, remap, insecure); err != nil {
			return err
		}
		registryCertDir := filepath.Join(certsDir, certDir)
		if _, err := p.RunCommandContext(ctx, "mkdir", "-p", registryCertDir); err != nil {
			return fmt.Errorf("mkdir %s: %w", registryCertDir, err)
		}
		if err := p.writeHostFileContext(ctx, filepath.Join(registryCertDir, "ca.crt"), caCert); err != nil {
			return err
		}
		if err := p.writeRegistryCertMount(ctx, worker.containerFile, registryCertDir, certDir); err != nil {
			return err
		}
	}
	if _, err := p.RunCommandContext(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload Quadlet units after writing registry CA mounts: %w", err)
	}
	logrus.Infof("Quadlet: wrote worker registry remap for %s", registryURL)
	return nil
}

func (p *InfraProvider) writeRegistryCertMount(ctx context.Context, containerFile, sourceDir, certDir string) error {
	dropInDir := p.quadletDropInDir(containerFile)
	if _, err := p.RunCommandContext(ctx, "mkdir", "-p", dropInDir); err != nil {
		return fmt.Errorf("mkdir %s: %w", dropInDir, err)
	}
	content := fmt.Sprintf("[Container]\nMount=type=bind,source=%s,destination=/etc/containers/certs.d/%s,ro,relabel=shared\n", sourceDir, certDir)
	dropInPath := filepath.Join(dropInDir, registryCertDropInName)
	if err := p.writeHostFileContext(ctx, dropInPath, []byte(content)); err != nil {
		return err
	}
	return nil
}

func (p *InfraProvider) writeRegistriesDir(ctx context.Context, dir, remap, insecure string) error {
	if _, err := p.RunCommandContext(ctx, "mkdir", "-p", dir); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := p.writeHostFileContext(ctx, filepath.Join(dir, "flightctl-remap.conf"), []byte(remap)); err != nil {
		return err
	}
	return p.writeHostFileContext(ctx, filepath.Join(dir, "flightctl-e2e.conf"), []byte(insecure))
}
