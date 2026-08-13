package main

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	profileBackendInternal = "backend-internal"
	profileBackendExternal = "backend-external"
	profileImage           = "image"
	profileImagesYAMLOnly  = "images-yaml-only"
)

func expandService(e serviceEntry) (ExpandedService, error) {
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
		exp.RequireNginx = true
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
	applyBoolOverride(&exp.RequireNginx, e.RequireNginx)
	applyBoolOverride(&exp.RequireServiceAccount, e.RequireServiceAccount)
	applyBoolOverride(&exp.RequireRoute, e.RequireRoute)
	applyBoolOverride(&exp.RequireService, e.RequireService)

	if e.MakeContainerTarget != nil {
		exp.MakeContainerTarget = "flightctl-" + *e.MakeContainerTarget + "-container"
	}
	if e.HelmDir != nil {
		exp.HelmDir = *e.HelmDir
	}
	if e.HelmValuesKey != nil {
		exp.HelmValuesKey = *e.HelmValuesKey
	}
	if e.HelmNamespace != nil {
		exp.HelmNamespace = *e.HelmNamespace
	}
	if e.CertSanFlag != nil {
		exp.CertSanFlag = *e.CertSanFlag
	}

	return exp, nil
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
