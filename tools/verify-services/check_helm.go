package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func checkHelm(repoRoot string, services []ExpandedService) []Issue {
	var issues []Issue
	for _, s := range services {
		if !s.Helm {
			continue
		}
		issues = append(issues, checkHelmService(repoRoot, s)...)
	}
	issues = append(issues, checkHelmChartOpts(repoRoot, services)...)
	return issues
}

func checkHelmService(repoRoot string, s ExpandedService) []Issue {
	const check = "helm"
	var issues []Issue

	valuesPath := filepath.Join(repoRoot, "deploy/helm/flightctl/values.yaml")
	valuesData, err := os.ReadFile(valuesPath)
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	var values map[string]any
	if err := yaml.Unmarshal(valuesData, &values); err != nil {
		return []Issue{{Check: check, Message: fmt.Sprintf("parse values.yaml: %v", err)}}
	}
	if _, ok := values[s.HelmValuesKey]; !ok {
		issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: missing values.yaml key %q", s.Name, s.HelmValuesKey)})
	}

	schemaPath := filepath.Join(repoRoot, "deploy/helm/flightctl/values.schema.json")
	schemaData, err := os.ReadFile(schemaPath)
	if err != nil {
		issues = append(issues, Issue{Check: check, Message: err.Error()})
	} else {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(schemaData, &schema); err != nil {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("parse values.schema.json: %v", err)})
		} else if _, ok := schema.Properties[s.HelmValuesKey]; !ok {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: missing values.schema.json property %q", s.Name, s.HelmValuesKey)})
		}
	}

	dir := filepath.Join(repoRoot, "deploy/helm/flightctl/templates", s.HelmDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: templates dir missing: %s", s.Name, dir)})
		return issues
	}

	hasDeployment := false
	hasSA := false
	hasRoute := false
	hasService := false
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if strings.Contains(name, "deployment") {
			hasDeployment = true
		}
		if strings.Contains(name, "serviceaccount") || strings.Contains(name, "serviceaccout") {
			hasSA = true
		}
		if strings.Contains(name, "service") && !strings.Contains(name, "serviceaccount") && !strings.Contains(name, "serviceaccout") && !strings.Contains(name, "service-metrics") {
			hasService = true
		}
		if strings.Contains(name, "route") {
			content, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: read route template %s: %v", s.Name, e.Name(), err)})
				continue
			}
			if isOpenShiftRouteManifest(string(content)) {
				hasRoute = true
			}
		}
	}
	if !hasDeployment {
		issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: no *deployment* template under templates/%s/", s.Name, s.HelmDir)})
	}
	if s.RequireServiceAccount && !hasSA {
		issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: missing ServiceAccount template under templates/%s/", s.Name, s.HelmDir)})
	}
	if s.RequireRoute && !hasRoute {
		issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: missing OpenShift Route template under templates/%s/", s.Name, s.HelmDir)})
	}
	if s.RequireService && !hasService {
		issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: missing Service template under templates/%s/", s.Name, s.HelmDir)})
	}
	return issues
}

// isOpenShiftRouteManifest reports whether content defines an OpenShift Route
// (route.openshift.io), not a Gateway API HTTPRoute.
func isOpenShiftRouteManifest(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "kind: Route" {
			return true
		}
	}
	return false
}

func chartOptsImageKey(s ExpandedService) string {
	if s.HelmValuesKey != "" {
		return s.HelmValuesKey
	}
	return toCamelCase(s.Name)
}

func checkHelmChartOpts(repoRoot string, services []ExpandedService) []Issue {
	const check = "helm-chart-opts"
	data, err := os.ReadFile(filepath.Join(repoRoot, "deploy/helm/helm-chart-opts.yaml"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}

	variantNames := []string{"community-el9", "rhem-el9", "community-el10", "rhem-el10"}
	var issues []Issue
	for _, vn := range variantNames {
		v, ok := doc[vn].(map[string]any)
		if !ok {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("missing variant %q", vn)})
			continue
		}
		images, _ := v["images"].(map[string]any)
		if images == nil {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("variant %q missing images", vn)})
			continue
		}
		for _, s := range services {
			if !s.InHelmChartOpts {
				continue
			}
			key := chartOptsImageKey(s)
			if _, ok := images[key]; !ok {
				issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("variant %s: missing images.%s (service %s)", vn, key, s.Name)})
			}
		}
	}
	return issues
}
