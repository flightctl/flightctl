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
// Rules come from cfg.byType keyed by package type ID (segment after pkg:).
// When no rule block exists for that type, the PURL is returned unchanged.
// For a configured type:
// 1. Namespace: NamespaceMapping replaces when a key matches the namespace (case-insensitive).
// 2. AllowedQualifiers: when non-empty, only those qualifier keys are kept (distro uses DistroMapping);
// when empty or unset, qualifiers are preserved as in the original PURL.
func TransformPurl(purl string, cfg *config.PurlTransformConfig) string {
	if cfg == nil || !cfg.EffectivePurlTransformEnabled() {
		return purl
	}

	matches := purlPattern.FindStringSubmatch(purl)
	if len(matches) < 4 {
		return purl
	}

	typePrefix := matches[1] // "pkg:rpm"
	typeID := config.NormalizedPURLPackageTypeID(typePrefix)

	rules, ok := lookupTypeRules(cfg, typeID)
	if !ok {
		return purl
	}

	namespace := matches[2] // "centos"
	name := matches[3]      // "acl"
	version := ""
	if len(matches) > 4 && matches[4] != "" {
		version = matches[4] // "@2.3.1-4.el9"
	}
	qualifiers := ""
	if len(matches) > 5 && matches[5] != "" {
		qualifiers = matches[5] // "?arch=x86_64&distro=centos-9&..."
	}

	// 1. Map namespace (only within this type's mappings)
	nsMap := rules.NamespaceMapping
	if mapped, ok := nsMap[strings.ToLower(namespace)]; ok {
		namespace = mapped
	}

	// 2. Qualifiers
	var qualOut string
	if qualifiers != "" {
		if len(rules.AllowedQualifiers) > 0 {
			var filteredQualifiers []string
			q, err := url.ParseQuery(strings.TrimPrefix(qualifiers, "?"))
			if err == nil {
				for _, allowed := range rules.AllowedQualifiers {
					if val := q.Get(allowed); val != "" {
						if allowed == "distro" {
							val = mapDistroQualifier(val, rules.DistroMapping)
						}
						filteredQualifiers = append(filteredQualifiers, allowed+"="+url.QueryEscape(val))
					}
				}
			}
			if len(filteredQualifiers) > 0 {
				qualOut = "?" + strings.Join(filteredQualifiers, "&")
			}
		} else {
			qualOut = qualifiers
		}
	}

	result := typePrefix + "/" + namespace + "/" + name + version + qualOut
	return result
}

func lookupTypeRules(cfg *config.PurlTransformConfig, typeID string) (config.PurlTransformTypeRules, bool) {
	if cfg.ByType == nil {
		return config.PurlTransformTypeRules{}, false
	}
	rules, ok := cfg.ByType[typeID]
	return rules, ok
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

// EnrichSBOMMetadata adds image metadata (digest, purl) to the SBOM's metadata.component section.
// This ensures the SBOM is self-contained and can be correlated with the image it describes.
func EnrichSBOMMetadata(sbomData []byte, imageRef, imageDigest string) ([]byte, error) {
	if imageRef == "" || imageDigest == "" {
		return sbomData, nil
	}

	var sbom map[string]interface{}
	if err := json.Unmarshal(sbomData, &sbom); err != nil {
		return nil, err
	}

	metadata, ok := sbom["metadata"].(map[string]interface{})
	if !ok {
		metadata = make(map[string]interface{})
		sbom["metadata"] = metadata
	}

	component, ok := metadata["component"].(map[string]interface{})
	if !ok {
		component = make(map[string]interface{})
		metadata["component"] = component
	}

	imageName, imageTag := parseImageRef(imageRef)
	ociPurl := buildOCIPurl(imageName, imageDigest)

	component["type"] = "container"
	component["name"] = imageRef
	if imageTag != "" {
		component["version"] = imageTag
	}
	component["purl"] = ociPurl

	if component["bom-ref"] == nil || component["bom-ref"] == "" {
		component["bom-ref"] = ociPurl
	}

	return json.MarshalIndent(sbom, "", "  ")
}

// parseImageRef extracts the image name and tag from a reference like "registry/name:tag" or "registry/name@sha256:..."
func parseImageRef(imageRef string) (name, tag string) {
	if idx := strings.LastIndex(imageRef, "@"); idx != -1 {
		return imageRef[:idx], imageRef[idx+1:]
	}
	if idx := strings.LastIndex(imageRef, ":"); idx != -1 {
		prefix := imageRef[:idx]
		suffix := imageRef[idx+1:]
		if strings.Contains(prefix, "/") {
			return prefix, suffix
		}
	}
	return imageRef, ""
}

// buildOCIPurl constructs an OCI PURL from image name and digest.
// Format: pkg:oci/<name>@<digest>?repository_url=<registry>
// Per PURL spec, namespace segments can contain "/" without encoding.
// Only the qualifier values need URL encoding.
func buildOCIPurl(imageName, digest string) string {
	parts := strings.SplitN(imageName, "/", 2)
	registry := ""
	name := imageName
	if len(parts) == 2 && strings.Contains(parts[0], ".") {
		registry = parts[0]
		name = parts[1]
	}

	purl := "pkg:oci/" + name + "@" + digest
	if registry != "" {
		purl += "?repository_url=" + url.QueryEscape(registry)
	}
	return purl
}

// GetEffectivePurlTransformConfig merges user-level purlTransform with defaults per package type.
// Map keys under byType are normalized to lowercase type IDs (e.g. rpm, npm).
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

	typeIDs := mergedTypeIDs(defaults.ByType, userCfg.ByType)
	result.ByType = make(map[string]config.PurlTransformTypeRules)
	for tid := range typeIDs {
		base := config.PurlTransformTypeRules{}
		if defaults.ByType != nil {
			if b, ok := defaults.ByType[tid]; ok {
				base = cloneTypeRules(b)
			}
		}

		userRules := lookupUserRulesByNormalizedID(userCfg.ByType, tid)
		result.ByType[tid] = mergeOneTypeRules(base, userRules)
	}

	return result
}

func mergedTypeIDs(defaults, user map[string]config.PurlTransformTypeRules) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range defaults {
		out[config.NormalizedPURLPackageTypeID(k)] = struct{}{}
	}
	for k := range user {
		out[config.NormalizedPURLPackageTypeID(k)] = struct{}{}
	}
	return out
}

func lookupUserRulesByNormalizedID(user map[string]config.PurlTransformTypeRules, wantID string) config.PurlTransformTypeRules {
	if user == nil {
		return config.PurlTransformTypeRules{}
	}
	for k, rules := range user {
		if config.NormalizedPURLPackageTypeID(k) == wantID {
			return rules
		}
	}
	return config.PurlTransformTypeRules{}
}

func cloneTypeRules(r config.PurlTransformTypeRules) config.PurlTransformTypeRules {
	out := config.PurlTransformTypeRules{
		NamespaceMapping:  maps.Clone(r.NamespaceMapping),
		DistroMapping:     maps.Clone(r.DistroMapping),
		AllowedQualifiers: append([]string(nil), r.AllowedQualifiers...),
	}
	return out
}

func mergeOneTypeRules(base, overlay config.PurlTransformTypeRules) config.PurlTransformTypeRules {
	out := cloneTypeRules(base)

	if overlay.NamespaceMapping != nil {
		if out.NamespaceMapping == nil {
			out.NamespaceMapping = map[string]string{}
		}
		for k, v := range overlay.NamespaceMapping {
			out.NamespaceMapping[strings.ToLower(k)] = v
		}
	}

	if overlay.DistroMapping != nil {
		if out.DistroMapping == nil {
			out.DistroMapping = map[string]string{}
		}
		for k, v := range overlay.DistroMapping {
			out.DistroMapping[strings.ToLower(k)] = v
		}
	}

	if len(overlay.AllowedQualifiers) > 0 {
		out.AllowedQualifiers = append([]string(nil), overlay.AllowedQualifiers...)
	}

	return out
}
