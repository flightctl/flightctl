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

// chart is one chart rendered from helm-chart-opts.yaml.
//
// profilePrefix selects the chart's family of profile keys. The main chart
// uses the bare "<edition>-<os>" keys; every other chart prefixes them, which
// also keeps the extra keys out of scripts/air-gap/generate-embed — that tool
// resolves exactly the four unprefixed variant names, so a prefixed profile's
// images never join the default air-gap bundle.
type chart struct {
	profilePrefix string
	chartTmpl     string
	chartOut      string
	valuesTmpl    string
	valuesOut     string
}

const optsPath = "helm-chart-opts.yaml"

// charts lists every chart generated from helm-chart-opts.yaml. A chart that
// is not here ships whatever its committed Chart.yaml and values.yaml say,
// which for a downstream rebuild means a chart still naming quay.io.
var charts = []chart{
	{
		profilePrefix: "",
		chartTmpl:     "flightctl/Chart.yaml.gotmpl",
		chartOut:      "flightctl/Chart.yaml",
		valuesTmpl:    "flightctl/values.yaml.gotmpl",
		valuesOut:     "flightctl/values.yaml",
	},
	{
		profilePrefix: "catalog-collector-",
		chartTmpl:     "flightctl-catalog-collector/Chart.yaml.gotmpl",
		chartOut:      "flightctl-catalog-collector/Chart.yaml",
		valuesTmpl:    "flightctl-catalog-collector/values.yaml.gotmpl",
		valuesOut:     "flightctl-catalog-collector/values.yaml",
	},
}

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

// selectProfile returns the profile for one chart, falling back to that
// chart's community-el9 profile when the requested one is absent.
func selectProfile(profiles map[string]templateContext, prefix, profileKey string) (templateContext, error) {
	if ctx, ok := profiles[prefix+profileKey]; ok {
		return ctx, nil
	}
	fallback := prefix + "community-el9"
	if ctx, ok := profiles[fallback]; ok {
		return ctx, nil
	}
	return templateContext{}, fmt.Errorf("neither profile key %q nor fallback %q found in %s",
		prefix+profileKey, fallback, optsPath)
}

// qualifyImages appends the OS suffix to flightctl images that do not already
// carry one. All builds now have OS-qualified image names in
// helm-chart-opts.yaml, so this is a no-op for them.
func qualifyImages(templateData templateContext, osVersion string) {
	for name, img := range templateData.Images {
		if strings.Contains(img.Image, "flightctl/flightctl-") && !strings.HasSuffix(img.Image, "-"+osVersion) {
			// Transform quay.io/flightctl/flightctl-api to quay.io/flightctl/flightctl-api-el9 or el10
			img.Image = img.Image + "-" + osVersion
			templateData.Images[name] = img
		}
	}
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
		profileKey = "rhem-" + osVersion
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

	for _, c := range charts {
		templateData, err := selectProfile(profiles, c.profilePrefix, profileKey)
		if err != nil {
			log.Fatalf("%v", err)
		}
		qualifyImages(templateData, osVersion)

		if err := runTemplate(c.chartTmpl, c.chartOut, templateData); err != nil {
			log.Fatalf("rendering %s: %v", c.chartOut, err)
		}
		if err := runTemplate(c.valuesTmpl, c.valuesOut, templateData); err != nil {
			log.Fatalf("rendering %s: %v", c.valuesOut, err)
		}
	}
}
