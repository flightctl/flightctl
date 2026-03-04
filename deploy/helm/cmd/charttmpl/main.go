package main

import (
	"fmt"
	"log"
	"os"
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
		return fmt.Errorf("reading template %s: %v", in, err)
	}

	tpl, err := template.New(in).Option("missingkey=error").Parse(string(tplBytes))
	if err != nil {
		return fmt.Errorf("parsing template %s: %v", in, err)
	}

	outFile, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("creating output %s: %v", out, err)
	}
	defer outFile.Close()

	if err := tpl.Execute(outFile, templateData); err != nil {
		return fmt.Errorf("executing template %s: %v", out, err)
	}
	return nil
}

func main() {
	profileKey := "community"
	if os.Getenv("RHEM") != "" {
		profileKey = "redhat"
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
	templateData, ok := profiles[profileKey]
	if !ok {
		log.Fatalf("profile key %q not found in %s", profileKey, optsPath)
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
