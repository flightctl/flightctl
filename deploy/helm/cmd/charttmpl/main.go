package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

type annotations struct {
	Name               string `yaml:"name"`
	Provider           string `yaml:"provider"`
	SupportURL         string `yaml:"supportURL"`
	RedhatEdition      string `yaml:"redhatEdition,omitempty"`
	TargetPlatform     string `yaml:"targetPlatform,omitempty"`
	SecurityCompliance string `yaml:"securityCompliance,omitempty"`
}

type image struct {
	Image string `yaml:"image"`
	Tag   string `yaml:"tag"`
}

type templateContext struct {
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Home        string           `yaml:"home"`
	Icon        string           `yaml:"icon"`
	Annotations annotations      `yaml:"annotations"`
	Images      map[string]image `yaml:"images"`
}

const (
	chartTmplPath  = "flightctl/Chart.yaml.gotmpl"
	chartOutPath   = "flightctl/Chart.yaml"
	valuesTmplPath = "flightctl/values.yaml.gotmpl"
	valuesOutPath  = "flightctl/values.yaml"
	optsPath       = "helm-chart-opts.yaml"
)

func runTemplate(in string, out string, templateData templateContext) error {
	tplBytes, err := os.ReadFile(in)
	if err != nil {
		return fmt.Errorf("reading template %s: %w", in, err)
	}

	tpl, err := template.New(in).Option("missingkey=error").Parse(string(tplBytes))
	if err != nil {
		return fmt.Errorf("parsing template %s: %w", in, err)
	}

	outFile, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("creating output %s: %w", out, err)
	}
	defer outFile.Close()

	if err := tpl.Execute(outFile, templateData); err != nil {
		return fmt.Errorf("executing template %s: %w", out, err)
	}
	return nil
}

func main() {
	// Determine OS version (el9 vs el10), default to el9
	osVersion := os.Getenv("OS")
	if osVersion == "" {
		osVersion = "el9"
	}

	// Build profile key based on RHEM environment variable
	var profileKey string
	if _, isRHEM := os.LookupEnv("RHEM"); isRHEM {
		profileKey = "redhat-" + osVersion
	} else {
		profileKey = "community-" + osVersion
	}

	// Multi-profile opts file
	optsBytes, err := os.ReadFile(optsPath)
	if err != nil {
		log.Fatalf("reading opts %s: %v", optsPath, err)
	}
	var profiles map[string]templateContext
	if err := yaml.Unmarshal(optsBytes, &profiles); err != nil {
		log.Fatalf("parsing opts %s: %v", optsPath, err)
	}
	// Try OS-specific profile first, fallback to community-el9 profile
	templateData, ok := profiles[profileKey]
	if !ok {
		// Fallback to community-el9 profile
		templateData, ok = profiles["community-el9"]
		if !ok {
			log.Fatalf("neither profile key %q nor fallback %q found in %s", profileKey, "community-el9", optsPath)
		}
	}

	// Transform image names to include OS suffix for flightctl images (only if not already qualified)
	// All builds now have OS-qualified image names in helm-chart-opts.yaml
	for name, img := range templateData.Images {
		if strings.Contains(img.Image, "flightctl/flightctl-") && !strings.HasSuffix(img.Image, "-"+osVersion) {
			// Transform quay.io/flightctl/flightctl-api to quay.io/flightctl/flightctl-api-el9 or el10
			img.Image = img.Image + "-" + osVersion
			templateData.Images[name] = img
		}
	}

	// Render Chart.yaml
	if err := runTemplate(chartTmplPath, chartOutPath, templateData); err != nil {
		log.Fatalf("rendering Chart.yaml: %v", err)
	}

	// Render values.yaml
	if err := runTemplate(valuesTmplPath, valuesOutPath, templateData); err != nil {
		log.Fatalf("rendering values.yaml: %v", err)
	}
}
