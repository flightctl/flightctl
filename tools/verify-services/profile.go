package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var (
	kebabIdent = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	camelIdent = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)
)

const (
	profileBackendInternal = "backend-internal"
	profileBackendExternal = "backend-external"
	profileImage           = "image"
	profileImagesYAMLOnly  = "images-yaml-only"
)

func expandService(e serviceEntry) (ExpandedService, error) {
	if err := validateKebabIdent("name", e.Name); err != nil {
		return ExpandedService{}, err
	}

	exp := ExpandedService{Name: e.Name}

	switch e.Profile {
	case profileBackendInternal:
		exp.Publish = true
		exp.BuildContainer = true
		exp.BuildBinary = true
		exp.CollectLogs = true
		exp.Helm = true
		exp.Quadlet = true
		exp.TagOverride = true
		exp.NeedsTLS = false
		exp.InImagesYaml = true
		exp.HelmNamespace = "internal"
		exp.InFlightctlTarget = true
	case profileBackendExternal:
		exp.Publish = true
		exp.BuildContainer = true
		exp.BuildBinary = true
		exp.CollectLogs = true
		exp.Helm = true
		exp.Quadlet = true
		exp.TagOverride = true
		exp.NeedsTLS = true
		exp.InImagesYaml = true
		exp.HelmNamespace = "external"
		exp.RequireRoute = true
		exp.RequireService = true
		exp.RequireGateway = true
		exp.InFlightctlTarget = true
	case profileImage:
		exp.Publish = true
		exp.BuildContainer = true
		exp.BuildBinary = false
		exp.CollectLogs = false
		exp.Helm = false
		exp.Quadlet = false
		exp.TagOverride = true
		exp.NeedsTLS = false
		exp.InImagesYaml = true
		exp.InFlightctlTarget = false
	case profileImagesYAMLOnly:
		exp.Publish = false
		exp.BuildContainer = false
		exp.BuildBinary = false
		exp.CollectLogs = false
		exp.Helm = false
		exp.Quadlet = false
		exp.TagOverride = false
		exp.NeedsTLS = false
		exp.InImagesYaml = true
		exp.InFlightctlTarget = false
	default:
		return ExpandedService{}, fmt.Errorf("unknown profile %q", e.Profile)
	}

	exp.HelmDir = e.Name
	exp.HelmValuesKey = toCamelCase(e.Name)
	exp.CertSanFlag = e.Name
	exp.MakeContainerTarget = "flightctl-" + e.Name + "-container"
	exp.InHelmChartOpts = exp.Helm

	applyBoolOverride(&exp.Publish, e.Publish)
	applyBoolOverride(&exp.BuildContainer, e.BuildContainer)
	applyBoolOverride(&exp.BuildBinary, e.BuildBinary)
	applyBoolOverride(&exp.CollectLogs, e.CollectLogs)
	applyBoolOverride(&exp.Helm, e.Helm)
	applyBoolOverride(&exp.Quadlet, e.Quadlet)
	applyBoolOverride(&exp.TagOverride, e.TagOverride)
	applyBoolOverride(&exp.NeedsTLS, e.NeedsTLS)
	applyBoolOverride(&exp.InImagesYaml, e.InImagesYaml)
	applyBoolOverride(&exp.ObservabilityOnly, e.ObservabilityOnly)
	applyBoolOverride(&exp.InFlightctlTarget, e.InFlightctlTarget)
	applyBoolOverride(&exp.RequireGateway, e.RequireGateway)
	applyBoolOverride(&exp.RequireServiceAccount, e.RequireServiceAccount)
	applyBoolOverride(&exp.RequireRoute, e.RequireRoute)
	applyBoolOverride(&exp.RequireService, e.RequireService)
	applyBoolOverride(&exp.InHelmChartOpts, e.InHelmChartOpts)

	if e.MakeContainerTarget != nil {
		if err := validateKebabIdent("makeContainerTarget", *e.MakeContainerTarget); err != nil {
			return ExpandedService{}, err
		}
		exp.MakeContainerTarget = "flightctl-" + *e.MakeContainerTarget + "-container"
	}
	if e.HelmDir != nil {
		if err := validatePathSegment("helmDir", *e.HelmDir); err != nil {
			return ExpandedService{}, err
		}
		exp.HelmDir = *e.HelmDir
	}
	if e.HelmValuesKey != nil {
		if err := validateCamelIdent("helmValuesKey", *e.HelmValuesKey); err != nil {
			return ExpandedService{}, err
		}
		exp.HelmValuesKey = *e.HelmValuesKey
	}
	if e.HelmNamespace != nil {
		if err := validateKebabIdent("helmNamespace", *e.HelmNamespace); err != nil {
			return ExpandedService{}, err
		}
		exp.HelmNamespace = *e.HelmNamespace
	}
	if e.CertSanFlag != nil {
		if err := validateKebabIdent("certSanFlag", *e.CertSanFlag); err != nil {
			return ExpandedService{}, err
		}
		exp.CertSanFlag = *e.CertSanFlag
	}

	return exp, nil
}

func validateKebabIdent(field, value string) error {
	if !kebabIdent.MatchString(value) {
		return fmt.Errorf("%s %q must match %s", field, value, kebabIdent.String())
	}
	return nil
}

func validateCamelIdent(field, value string) error {
	if !camelIdent.MatchString(value) {
		return fmt.Errorf("%s %q must match %s", field, value, camelIdent.String())
	}
	return nil
}

func validatePathSegment(field, value string) error {
	if value == "" || strings.ContainsAny(value, `/\`) || value == "." || value == ".." || strings.Contains(value, "..") {
		return fmt.Errorf("%s %q is not a safe path segment", field, value)
	}
	return validateKebabIdent(field, value)
}

func applyBoolOverride(dst *bool, src *bool) {
	if src != nil {
		*dst = *src
	}
}

func toCamelCase(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) == 0 {
		return name
	}
	var b strings.Builder
	b.WriteString(parts[0])
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		r := []rune(p)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}
