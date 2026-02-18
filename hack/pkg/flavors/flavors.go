package flavors

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadMerged reads the base flavors YAML file and applies overlays in order.
// Each overlay is deep-merged on top of the base, with overlay values winning.
func LoadMerged(basePath string, overlays []string) (map[string]any, error) {
	base, err := loadYAML(basePath)
	if err != nil {
		return nil, fmt.Errorf("loading base %s: %w", basePath, err)
	}
	for _, ov := range overlays {
		overlay, err := loadYAML(ov)
		if err != nil {
			return nil, fmt.Errorf("loading overlay %s: %w", ov, err)
		}
		deepMerge(base, overlay)
	}
	return base, nil
}

// Navigate walks a dot-separated path into the data tree.
func Navigate(data map[string]any, dotPath string) (any, error) {
	parts := strings.Split(dotPath, ".")
	var current any = data
	for _, p := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("key %q: not a map", p)
		}
		current, ok = m[p]
		if !ok {
			return nil, fmt.Errorf("key %q not found", p)
		}
	}
	return current, nil
}

// ListFlavors returns top-level keys that don't start with "_" (anchors).
func ListFlavors(data map[string]any) []string {
	var out []string
	for k := range data {
		if !strings.HasPrefix(k, "_") {
			out = append(out, k)
		}
	}
	return out
}

// PrintExportBuild emits shell-compatible KEY=VALUE lines for the build section of a flavor.
func PrintExportBuild(data map[string]any, flavor string) error {
	flavorData, ok := data[flavor]
	if !ok {
		return fmt.Errorf("flavor %q not found", flavor)
	}
	fm, ok := flavorData.(map[string]any)
	if !ok {
		return fmt.Errorf("flavor %q is not a map", flavor)
	}

	fmt.Printf("export EL_FLAVOR=%q\n", flavor)
	if elv, ok := fm["el_version"]; ok {
		fmt.Printf("export EL_VERSION=%q\n", fmt.Sprint(elv))
	}

	buildRaw, ok := fm["build"]
	if !ok {
		return fmt.Errorf("flavor %q has no build section", flavor)
	}
	build, ok := buildRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("flavor %q build is not a map", flavor)
	}

	for k, v := range build {
		envKey := strings.ToUpper(k)
		fmt.Printf("export %s=%q\n", envKey, fmt.Sprint(v))
	}
	return nil
}

func loadYAML(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MergeImages merges the top-level "images" section with tool-specific overrides
// from "<target>.images" (e.g. "helm.images" or "quadlets.images").
// Images without an explicit tag get "{flavor}-latest" injected.
// For "helm", keys are additionally converted to camelCase.
func MergeImages(flavorData map[string]any, flavor, target string) (map[string]any, error) {
	baseRaw, ok := flavorData["images"]
	if !ok {
		return nil, fmt.Errorf("flavor has no images section")
	}
	baseImages, ok := baseRaw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("images section is not a map")
	}

	merged := make(map[string]any, len(baseImages))
	for k, v := range baseImages {
		if vm, ok := v.(map[string]any); ok {
			cp := make(map[string]any, len(vm))
			for ck, cv := range vm {
				cp[ck] = cv
			}
			merged[k] = cp
		} else {
			merged[k] = v
		}
	}

	targetRaw, err := Navigate(flavorData, target+".images")
	if err == nil {
		if overrides, ok := targetRaw.(map[string]any); ok {
			for k, v := range overrides {
				merged[k] = v
			}
		}
	}

	// Inject "{flavor}-latest" for images without an explicit tag
	for _, v := range merged {
		vm, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if _, hasTag := vm["tag"]; !hasTag {
			vm["tag"] = flavor + "-latest"
		}
	}

	switch target {
	case "quadlets":
		return merged, nil
	case "helm":
		camel := make(map[string]any, len(merged))
		for k, v := range merged {
			camel[KebabToCamel(k)] = v
		}
		return camel, nil
	default:
		return nil, fmt.Errorf("unknown target %q, must be helm or quadlets", target)
	}
}

// KebabToCamel converts kebab-case to camelCase: "alert-exporter" -> "alertExporter".
func KebabToCamel(s string) string {
	parts := strings.Split(s, "-")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// deepMerge recursively merges src into dst, with src values winning.
func deepMerge(dst, src map[string]any) {
	for k, srcVal := range src {
		dstVal, exists := dst[k]
		if !exists {
			dst[k] = srcVal
			continue
		}
		srcMap, srcOk := srcVal.(map[string]any)
		dstMap, dstOk := dstVal.(map[string]any)
		if srcOk && dstOk {
			deepMerge(dstMap, srcMap)
			continue
		}
		dst[k] = srcVal
	}
}
