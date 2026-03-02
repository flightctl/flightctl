package main

import (
	"log"
	"os"
	"path/filepath"
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

func applyFlavorChartOverride(profileKey string) {
	// Get distro and release version from environment
	distro := os.Getenv("DISTRO")
	relver := os.Getenv("RELVER")

	// If not set, use defaults based on profile
	if distro == "" {
		distro = profileKey // community or redhat
	}
	if relver == "" {
		relver = "el9" // default to el9
	}

	// Construct flavor path
	flavorName := distro + "-" + relver
	flavorChartPath := filepath.Join("..", "..", "packaging", "flavors", flavorName, "Chart.yaml")

	// Check if flavor Chart.yaml exists
	if _, err := os.Stat(flavorChartPath); os.IsNotExist(err) {
		// No flavor override, use generated Chart.yaml as-is
		return
	}

	// Read the generated Chart.yaml
	generatedBytes, err := os.ReadFile(chartOutPath)
	if err != nil {
		log.Fatalf("reading generated chart %s: %v", chartOutPath, err)
	}

	var generatedChart map[string]interface{}
	if err := yaml.Unmarshal(generatedBytes, &generatedChart); err != nil {
		log.Fatalf("parsing generated chart: %v", err)
	}

	// Read the flavor override Chart.yaml
	flavorBytes, err := os.ReadFile(flavorChartPath)
	if err != nil {
		log.Fatalf("reading flavor chart %s: %v", flavorChartPath, err)
	}

	var flavorOverrides map[string]interface{}
	if err := yaml.Unmarshal(flavorBytes, &flavorOverrides); err != nil {
		log.Fatalf("parsing flavor chart %s: %v", flavorChartPath, err)
	}

	// Merge flavor overrides into generated chart
	for key, value := range flavorOverrides {
		generatedChart[key] = value
	}

	// Write back the merged chart
	mergedBytes, err := yaml.Marshal(generatedChart)
	if err != nil {
		log.Fatalf("marshaling merged chart: %v", err)
	}

	if err := os.WriteFile(chartOutPath, mergedBytes, 0644); err != nil {
		log.Fatalf("writing merged chart: %v", err)
	}

	log.Printf("Applied Chart.yaml value overrides from %s", flavorChartPath)
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
	runTemplate(chartTmplPath, chartOutPath, templateData)

	// Apply flavor-specific Chart.yaml override if it exists
	applyFlavorChartOverride(profileKey)

	// Render values.yaml
	runTemplate(valuesTmplPath, valuesOutPath, templateData)
}
