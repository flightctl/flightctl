package main

import (
	"fmt"
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
	valuesTmplPath = "flightctl/values.yaml.gotmpl"
	valuesOutPath  = "flightctl/values.yaml"
	optsPath       = "helm-chart-opts.yaml"
)

var chartOutPath = "flightctl/Chart.yaml"

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

// deepMergeMaps recursively merges src into dst, preserving nested map structures
func deepMergeMaps(dst, src map[string]interface{}) {
	for key, srcValue := range src {
		if dstValue, exists := dst[key]; exists {
			// Both dst and src have this key - check if both are maps
			if dstMap, dstIsMap := dstValue.(map[string]interface{}); dstIsMap {
				if srcMap, srcIsMap := srcValue.(map[string]interface{}); srcIsMap {
					// Both are maps - recursively merge
					deepMergeMaps(dstMap, srcMap)
					continue
				}
			}
		}
		// Either key doesn't exist in dst, or one/both values aren't maps - overwrite
		dst[key] = srcValue
	}
}

func applyFlavorChartOverride(profileKey string) error {
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
		// Red Hat flavors must have Chart.yaml files, community flavors are optional
		if distro == "redhat" {
			return fmt.Errorf("Red Hat flavor Chart.yaml missing: %s - Red Hat flavors require chart overrides", flavorChartPath)
		}
		// No flavor override for community, use generated Chart.yaml as-is
		return nil
	}

	// Read the generated Chart.yaml
	generatedBytes, err := os.ReadFile(chartOutPath)
	if err != nil {
		return fmt.Errorf("reading generated chart %s: %v", chartOutPath, err)
	}

	var generatedChart map[string]interface{}
	if err := yaml.Unmarshal(generatedBytes, &generatedChart); err != nil {
		return fmt.Errorf("parsing generated chart: %v", err)
	}

	// Read the flavor override Chart.yaml
	flavorBytes, err := os.ReadFile(flavorChartPath)
	if err != nil {
		return fmt.Errorf("reading flavor chart %s: %v", flavorChartPath, err)
	}

	var flavorOverrides map[string]interface{}
	if err := yaml.Unmarshal(flavorBytes, &flavorOverrides); err != nil {
		return fmt.Errorf("parsing flavor chart %s: %v", flavorChartPath, err)
	}

	// Deep merge flavor overrides into generated chart (preserves nested maps like annotations)
	deepMergeMaps(generatedChart, flavorOverrides)

	// Write back the merged chart
	mergedBytes, err := yaml.Marshal(generatedChart)
	if err != nil {
		return fmt.Errorf("marshaling merged chart: %v", err)
	}

	if err := os.WriteFile(chartOutPath, mergedBytes, 0644); err != nil {
		return fmt.Errorf("writing merged chart: %v", err)
	}

	log.Printf("Applied Chart.yaml value overrides from %s", flavorChartPath)
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

	// Apply flavor-specific Chart.yaml override if it exists
	if err := applyFlavorChartOverride(profileKey); err != nil {
		log.Fatalf("applying flavor chart override: %v", err)
	}

	// Render values.yaml
	if err := runTemplate(valuesTmplPath, valuesOutPath, templateData); err != nil {
		log.Fatalf("rendering values.yaml: %v", err)
	}
}
