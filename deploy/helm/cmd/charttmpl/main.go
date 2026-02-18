package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/flightctl/flightctl/hack/pkg/flavors"
	"gopkg.in/yaml.v3"
)

type annotations struct {
	Name       string `yaml:"name"`
	Provider   string `yaml:"provider"`
	SupportURL string `yaml:"supportURL"`
}

type image struct {
	Image   string `yaml:"image"`
	Tag     string `yaml:"tag"`
	Command string `yaml:"command"`
}

type templateContext struct {
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Home        string           `yaml:"home"`
	Icon        string           `yaml:"icon"`
	Annotations annotations      `yaml:"annotations"`
	Images      map[string]image `yaml:"images"`
}

type chartMeta struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Home        string      `yaml:"home"`
	Icon        string      `yaml:"icon"`
	Annotations annotations `yaml:"annotations"`
}

type helmConfig struct {
	Chart chartMeta `yaml:"chart"`
}

const (
	chartTmplPath  = "flightctl/Chart.yaml.gotmpl"
	chartOutPath   = "flightctl/Chart.yaml"
	valuesTmplPath = "flightctl/values.yaml.gotmpl"
	valuesOutPath  = "flightctl/values.yaml"
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
		log.Fatalf("creating output %s: %v", out, err)
	}
	defer outFile.Close()

	if err := tpl.Execute(outFile, templateData); err != nil {
		log.Fatalf("executing template %s: %v", out, err)
	}
}

func main() {
	flavor := os.Getenv("FLAVOR")
	if flavor == "" {
		flavor = "el9"
	}

	flavorsPath := findFlavorsYAML()

	var overlayPaths []string
	if ov := os.Getenv("FLAVOR_OVERLAYS"); ov != "" {
		overlayPaths = strings.Split(ov, ":")
	}

	data, err := flavors.LoadMerged(flavorsPath, overlayPaths)
	if err != nil {
		log.Fatalf("loading flavors: %v", err)
	}

	flavorData, ok := data[flavor]
	if !ok {
		log.Fatalf("flavor %q not found in %s", flavor, flavorsPath)
	}

	fm := flavorData.(map[string]any)

	helmRaw, err := flavors.Navigate(fm, "helm")
	if err != nil {
		log.Fatalf("navigating to helm section: %v", err)
	}

	var helm helmConfig
	helmBytes, err := yaml.Marshal(helmRaw)
	if err != nil {
		log.Fatalf("marshaling helm section: %v", err)
	}
	if err := yaml.Unmarshal(helmBytes, &helm); err != nil {
		log.Fatalf("parsing helm section: %v", err)
	}

	mergedRaw, err := flavors.MergeImages(fm, flavor, "helm")
	if err != nil {
		log.Fatalf("merging helm images: %v", err)
	}
	images, err := unmarshalImages(mergedRaw)
	if err != nil {
		log.Fatalf("unmarshaling merged images: %v", err)
	}

	templateData := templateContext{
		Name:        helm.Chart.Name,
		Description: helm.Chart.Description,
		Home:        helm.Chart.Home,
		Icon:        helm.Chart.Icon,
		Annotations: helm.Chart.Annotations,
		Images:      images,
	}

	runTemplate(chartTmplPath, chartOutPath, templateData)
	runTemplate(valuesTmplPath, valuesOutPath, templateData)
}

func unmarshalImages(raw map[string]any) (map[string]image, error) {
	b, err := yaml.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out map[string]image
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func findFlavorsYAML() string {
	if env := os.Getenv("FLAVORS_YAML"); env != "" {
		return env
	}
	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "hack", "flavors.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	log.Fatal("cannot find hack/flavors.yaml; set FLAVORS_YAML or run from the repo")
	return ""
}
