package quadlet

import (
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
)

var workerRegistriesDirs = []string{
	"/etc/flightctl/flightctl-delta-worker/registries.conf.d",
	"/etc/flightctl/flightctl-worker/registries.conf.d",
}

func (p *InfraProvider) ApplyDeltaWorkerRegistryRemap(registryURL string) error {
	remap, insecure := infra.DeltaWorkerRegistryRemapFiles(registryURL)
	for _, dir := range workerRegistriesDirs {
		if err := p.writeRegistriesDir(dir, remap, insecure); err != nil {
			return err
		}
	}
	logrus.Infof("Quadlet: wrote worker registry remap for %s", registryURL)
	return nil
}

func (p *InfraProvider) writeRegistriesDir(dir, remap, insecure string) error {
	if _, err := p.RunCommand("mkdir", "-p", dir); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := p.WriteHostFile(filepath.Join(dir, "flightctl-remap.conf"), []byte(remap)); err != nil {
		return err
	}
	return p.WriteHostFile(filepath.Join(dir, "flightctl-e2e.conf"), []byte(insecure))
}
