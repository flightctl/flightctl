package main

import (
	"log"
	"os"
	"text/template"

	"gopkg.in/yaml.v3"
)

type annotations struct {
	Name       string `yaml:"name"`
	Provider   string `yaml:"provider"`
	SupportURL string `yaml:"supportURL"`
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

func runTemplate(in string, out string, templateData templateContext) {
	tplBytes, err := os.ReadFile(in)
	if err != nil {
		log.Fatalf("reading template %s: %v", in, err)
	}

	tpl, err := template.New(in).Option("missingkey=error").Parse(string(tplBytes))
	if err != nil {
		log.Fatalf("parsing template %s: %v", in, err)
	}

	outFile, err := os.Create(out)
	if err != nil {
		log.Fatalf("creating output %s: %v", chartOutPath, err)
	}
	defer outFile.Close()

	if err := tpl.Execute(outFile, templateData); err != nil {
		log.Fatalf("executing template %s: %v", chartOutPath, err)
	}
}

func main() {
	// Get FLAVOR, default to cs9
	flavor := os.Getenv("FLAVOR")
	if flavor == "" {
		flavor = "cs9"
	}

	// Construct profile key based on RHEM and FLAVOR
	profileKey := "community-" + flavor
	if os.Getenv("RHEM") != "" {
		profileKey = "redhat-" + flavor
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
	runTemplate(chartTmplPath, chartOutPath, templateData)

	// Render values.yaml
	runTemplate(valuesTmplPath, valuesOutPath, templateData)
}
