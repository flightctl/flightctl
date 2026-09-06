package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const registryPath = "hack/services.yaml"

type registryFile struct {
	Version  int            `yaml:"version"`
	Services []serviceEntry `yaml:"services"`
}

type serviceEntry struct {
	Name    string `yaml:"name"`
	Profile string `yaml:"profile"`

	MakeContainerTarget   *string `yaml:"makeContainerTarget"`
	HelmDir               *string `yaml:"helmDir"`
	HelmValuesKey         *string `yaml:"helmValuesKey"`
	HelmNamespace         *string `yaml:"helmNamespace"`
	CertSanFlag           *string `yaml:"certSanFlag"`
	TagOverride           *bool   `yaml:"tagOverride"`
	ObservabilityOnly     *bool   `yaml:"observabilityOnly"`
	Quadlet               *bool   `yaml:"quadlet"`
	BuildBinary           *bool   `yaml:"buildBinary"`
	CollectLogs           *bool   `yaml:"collectLogs"`
	Helm                  *bool   `yaml:"helm"`
	InFlightctlTarget     *bool   `yaml:"inFlightctlTarget"`
	RequireGateway        *bool   `yaml:"requireGateway"`
	NeedsTLS              *bool   `yaml:"needsTLS"`
	Publish               *bool   `yaml:"publish"`
	BuildContainer        *bool   `yaml:"buildContainer"`
	InImagesYaml          *bool   `yaml:"inImagesYaml"`
	InHelmChartOpts       *bool   `yaml:"inHelmChartOpts"`
	RequireServiceAccount *bool   `yaml:"requireServiceAccount"`
	RequireRoute          *bool   `yaml:"requireRoute"`
	RequireService        *bool   `yaml:"requireService"`
}

// ExpandedService is the profile-expanded view used by checks.
type ExpandedService struct {
	Name string

	Publish           bool
	BuildContainer    bool
	BuildBinary       bool
	CollectLogs       bool
	Helm              bool
	Quadlet           bool
	TagOverride       bool
	NeedsTLS          bool
	InImagesYaml      bool
	ObservabilityOnly bool

	HelmDir             string
	HelmValuesKey       string
	HelmNamespace       string
	CertSanFlag         string
	MakeContainerTarget string

	RequireServiceAccount bool
	RequireRoute          bool
	RequireService        bool
	RequireGateway        bool
	InHelmChartOpts       bool
	InFlightctlTarget     bool
}

func loadRegistry(repoRoot string) ([]ExpandedService, error) {
	path := filepath.Join(repoRoot, registryPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var reg registryFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if reg.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported version %d (want 1)", path, reg.Version)
	}
	if len(reg.Services) == 0 {
		return nil, fmt.Errorf("%s: no services defined", path)
	}

	out := make([]ExpandedService, 0, len(reg.Services))
	seen := map[string]struct{}{}
	for _, e := range reg.Services {
		if e.Name == "" {
			return nil, fmt.Errorf("%s: service entry missing name", path)
		}
		if _, ok := seen[e.Name]; ok {
			return nil, fmt.Errorf("%s: duplicate service name %q", path, e.Name)
		}
		seen[e.Name] = struct{}{}
		exp, err := expandService(e)
		if err != nil {
			return nil, fmt.Errorf("%s: service %q: %w", path, e.Name, err)
		}
		out = append(out, exp)
	}
	return out, nil
}
