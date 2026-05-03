package tasks

import (
	"encoding/json"
	"maps"
	"net/url"
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
)

// purlPattern parses PURL: pkg:type/namespace/name@version?qualifiers
var purlPattern = regexp.MustCompile(`^(pkg:[^/]+)/([^/]+)/([^@?]+)(@[^?]+)?(\?.+)?$`)

// TransformPurl normalizes a PURL for Trustify advisory matching.
// It applies only configuration-driven rules:
// 1. Namespace: if NamespaceMapping contains a key matching the namespace (case-insensitive), replace; otherwise unchanged.
// 2. Distro qualifier: if DistroMapping contains a key matching the distro value (case-insensitive), replace; otherwise unchanged.
// 3. Qualifiers: only keys listed in AllowedQualifiers are kept; others are dropped.
func TransformPurl(purl string, cfg *config.PurlTransformConfig) string {
	if cfg == nil || !cfg.EffectivePurlTransformEnabled() {
		return purl
	}

	matches := purlPattern.FindStringSubmatch(purl)
	if len(matches) < 4 {
		return purl
	}

	typePrefix := matches[1] // "pkg:rpm"
	namespace := matches[2]  // "centos"
	name := matches[3]       // "acl"
	version := ""
	if len(matches) > 4 && matches[4] != "" {
		version = matches[4] // "@2.3.1-4.el9"
	}
	qualifiers := ""
	if len(matches) > 5 && matches[5] != "" {
		qualifiers = matches[5] // "?arch=x86_64&distro=centos-9&..."
	}

	// 1. Map namespace
	if mapped, ok := cfg.NamespaceMapping[strings.ToLower(namespace)]; ok {
		namespace = mapped
	}

	// 2. Process qualifiers
	var filteredQualifiers []string
	if qualifiers != "" {
		q, err := url.ParseQuery(strings.TrimPrefix(qualifiers, "?"))
		if err == nil {
			for _, allowed := range cfg.AllowedQualifiers {
				if val := q.Get(allowed); val != "" {
					if allowed == "distro" {
						val = mapDistroQualifier(val, cfg.DistroMapping)
					}
					filteredQualifiers = append(filteredQualifiers, allowed+"="+url.QueryEscape(val))
				}
			}
		}
	}

	// Rebuild PURL
	result := typePrefix + "/" + namespace + "/" + name + version
	if len(filteredQualifiers) > 0 {
		result += "?" + strings.Join(filteredQualifiers, "&")
	}

	return result
}

// mapDistroQualifier replaces a distro qualifier only when DistroMapping defines a key for it (case-insensitive key match).
func mapDistroQualifier(distro string, mapping map[string]string) string {
	if len(mapping) == 0 {
		return distro
	}
	distroLower := strings.ToLower(distro)
	if mapped, ok := mapping[distroLower]; ok {
		return mapped
	}
	return distro
}

// TransformSBOMPurls transforms all PURLs in a CycloneDX SBOM.
// It parses the SBOM JSON, transforms each component's PURL, and returns the modified SBOM.
func TransformSBOMPurls(sbomData []byte, cfg *config.PurlTransformConfig) ([]byte, error) {
	if cfg == nil || !cfg.EffectivePurlTransformEnabled() {
		return sbomData, nil
	}

	var sbom map[string]interface{}
	if err := json.Unmarshal(sbomData, &sbom); err != nil {
		return nil, err
	}

	// Transform components array
	if components, ok := sbom["components"].([]interface{}); ok {
		for _, comp := range components {
			if c, ok := comp.(map[string]interface{}); ok {
				if purl, ok := c["purl"].(string); ok {
					c["purl"] = TransformPurl(purl, cfg)
				}
			}
		}
	}

	return json.MarshalIndent(sbom, "", "  ")
}

// GetEffectivePurlTransformConfig returns the PURL transform config to use,
// merging user config with defaults where not specified.
func GetEffectivePurlTransformConfig(userCfg *config.PurlTransformConfig) *config.PurlTransformConfig {
	defaults := config.NewDefaultPurlTransformConfig()

	if userCfg == nil {
		return defaults
	}

	result := &config.PurlTransformConfig{}

	if userCfg.Enabled != nil {
		v := *userCfg.Enabled
		result.Enabled = &v
	} else {
		result.Enabled = defaults.Enabled
	}

	// Use user mappings if provided, otherwise use defaults
	if len(userCfg.NamespaceMapping) > 0 {
		result.NamespaceMapping = maps.Clone(userCfg.NamespaceMapping)
	} else {
		result.NamespaceMapping = maps.Clone(defaults.NamespaceMapping)
	}

	if len(userCfg.DistroMapping) > 0 {
		result.DistroMapping = maps.Clone(userCfg.DistroMapping)
	} else {
		result.DistroMapping = maps.Clone(defaults.DistroMapping)
	}

	if len(userCfg.AllowedQualifiers) > 0 {
		result.AllowedQualifiers = append([]string(nil), userCfg.AllowedQualifiers...)
	} else {
		result.AllowedQualifiers = append([]string(nil), defaults.AllowedQualifiers...)
	}

	return result
}
