package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"sigs.k8s.io/yaml"
)

// DropInConfigProvider reads a base certificate configuration file and merges
// overrides from a derived ".d" directory. Certificates are keyed by Name; a
// drop-in with the same certificate name overrides the base definition.
// Example:
//   - Base: /etc/flightctl/certs.yaml
//   - Drop-ins dir: /etc/flightctl/certs.d/
//
// All files in the drop-ins dir with .yaml/.yml extensions are applied in lexical order.
// Only YAML is supported for base and drop-ins.
type DropInConfigProvider struct {
	// File I/O interface used to read configuration files and directories
	readWriter fileio.ReadWriter
	// Base configuration file path (e.g., /etc/flightctl/certs.yaml).
	basePath string
}

// NewDropInConfigProvider creates a configuration provider that loads a base
// YAML config and merges any drop-ins from a derived ".d" directory. Drop-ins
// override base certificates by matching Name.
func NewDropInConfigProvider(rw fileio.ReadWriter, basePath string) *DropInConfigProvider {
	return &DropInConfigProvider{readWriter: rw, basePath: basePath}
}

// Name returns the unique identifier for this provider, including the base path
func (p *DropInConfigProvider) Name() string { return fmt.Sprintf("dropin[%s]", p.basePath) }

// GetCertificateConfigs loads the base YAML (optional) and merges drop-ins from
// "<basename>.d/" (e.g., /etc/flightctl/certs.d/). Drop-ins override base by Name.
func (p *DropInConfigProvider) GetCertificateConfigs() ([]provider.CertificateConfig, error) {
	if strings.TrimSpace(p.basePath) == "" {
		return nil, nil
	}

	byName := make(map[string]provider.CertificateConfig)

	// Base (optional, YAML only)
	if baseCfgs, err := readYAMLConfigsFile(p.readWriter, p.basePath); err == nil {
		for _, c := range baseCfgs {
			if n := strings.TrimSpace(c.Name); n != "" {
				byName[n] = c
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read base config %s: %w", p.basePath, err)
	}

	// Drop-ins (optional): "<basename>.d/" (e.g., certs.d/)
	dropDir := fmt.Sprintf("%s.d", strings.TrimSuffix(p.basePath, filepath.Ext(p.basePath)))
	if entries, err := p.readWriter.ReadDir(dropDir); err == nil {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if e.IsDir() || !hasYAMLExt(e.Name()) {
				continue
			}

			path := filepath.Join(dropDir, e.Name())
			cfgs, derr := readYAMLConfigsFile(p.readWriter, path)
			if derr != nil {
				return nil, fmt.Errorf("failed to parse drop-in %s: %w", path, derr)
			}

			for _, c := range cfgs {
				if n := strings.TrimSpace(c.Name); n != "" {
					byName[n] = c
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read drop-ins dir %s: %w", dropDir, err)
	}

	if len(byName) == 0 {
		return nil, nil
	}

	// Deterministic order
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]provider.CertificateConfig, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out, nil
}

// hasYAMLExt returns true if the file name has a YAML extension
func hasYAMLExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

// readYAMLConfigsFile accepts either a YAML list ([]CertificateConfig) or a single object.
func readYAMLConfigsFile(rw fileio.ReadWriter, path string) ([]provider.CertificateConfig, error) {
	data, err := rw.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Try list first
	var list []provider.CertificateConfig
	if err := yaml.Unmarshal(data, &list); err == nil {
		return list, nil
	}

	// Try single object
	var single provider.CertificateConfig
	if err := yaml.Unmarshal(data, &single); err == nil {
		return []provider.CertificateConfig{single}, nil
	}

	return nil, fmt.Errorf("invalid YAML config: %s", path)
}
