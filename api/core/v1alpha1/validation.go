package v1alpha1

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Catalog validation

func (c Catalog) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(c.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(c.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(c.Metadata.Annotations)...)
	return allErrs
}

// ValidateUpdate ensures immutable fields are unchanged for Catalog.
func (c *Catalog) ValidateUpdate(newObj *Catalog) []error {
	return validateImmutableCoreFields(c.Metadata.Name, newObj.Metadata.Name,
		c.ApiVersion, newObj.ApiVersion,
		c.Kind, newObj.Kind,
		c.Status, newObj.Status)
}

func (ci CatalogItem) Validate() []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(ci.Metadata.Name)...)
	allErrs = append(allErrs, validation.ValidateLabels(ci.Metadata.Labels)...)
	allErrs = append(allErrs, validation.ValidateAnnotations(ci.Metadata.Annotations)...)

	// Validate type is present and is a known enum value
	if ci.Spec.Type == "" {
		allErrs = append(allErrs, errors.New("spec.type is required"))
	} else if !isValidCatalogItemType(ci.Spec.Type) {
		allErrs = append(allErrs, fmt.Errorf("spec.type must be one of: %s", validCatalogItemTypes()))
	}

	allErrs = append(allErrs, validateCatalogItemReference(&ci.Spec.Reference)...)

	// Resolve category (defaults to application)
	category := CatalogItemCategoryApplication
	if ci.Spec.Category != nil && *ci.Spec.Category != "" {
		category = *ci.Spec.Category
	}
	if category != CatalogItemCategorySystem && category != CatalogItemCategoryApplication {
		allErrs = append(allErrs, fmt.Errorf("spec.category must be %q or %q", CatalogItemCategorySystem, CatalogItemCategoryApplication))
	}

	// Validate category↔type compatibility
	if ci.Spec.Type != "" {
		if err := validateCategoryTypeCompatibility(category, ci.Spec.Type); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	if len(ci.Spec.Versions) == 0 {
		allErrs = append(allErrs, errors.New("spec.versions must have at least one entry"))
	}
	seenVersions := make(map[string]struct{})
	for i, version := range ci.Spec.Versions {
		allErrs = append(allErrs, validateCatalogItemVersion(version, i, seenVersions, category, ci.Spec.Type)...)
	}

	allErrs = append(allErrs, validateReplacesGraph(ci.Spec.Versions)...)

	if ci.Spec.Defaults != nil {
		allErrs = append(allErrs, validateCatalogItemConfig(category, ci.Spec.Type, ci.Spec.Defaults.Config, "spec.defaults")...)
		allErrs = append(allErrs, validateConfigSchema(ci.Spec.Defaults.ConfigSchema, "spec.defaults.configSchema")...)
	}

	if ci.Spec.Deprecation != nil {
		allErrs = append(allErrs, validateCatalogItemDeprecation(ci.Spec.Deprecation, "spec.deprecation")...)
	}

	if ci.Spec.Visibility != nil {
		if *ci.Spec.Visibility != CatalogItemVisibilityDraft && *ci.Spec.Visibility != CatalogItemVisibilityPublished {
			allErrs = append(allErrs, fmt.Errorf("spec.visibility must be %q or %q", CatalogItemVisibilityDraft, CatalogItemVisibilityPublished))
		}
	}

	return allErrs
}

func validateCatalogItemReference(ref *CatalogItemReference) []error {
	allErrs := []error{}

	if ref == nil {
		allErrs = append(allErrs, errors.New("spec.reference is required"))
		return allErrs
	}

	if ref.Uri == "" {
		allErrs = append(allErrs, errors.New("spec.reference.uri is required"))
	} else {
		allErrs = append(allErrs, validateArtifactURI(ref.Uri, "spec.reference.uri")...)
	}

	if ref.Artifacts != nil {
		artifactCount := len(*ref.Artifacts)
		seenTypes := make(map[CatalogItemArtifactType]struct{})
		for i, artifact := range *ref.Artifacts {
			// type is required if more than 1 artifact, defaults to "container" if only 1
			artifactType := CatalogItemArtifactType("")
			if artifact.Type != nil {
				artifactType = *artifact.Type
			}
			if artifactType == "" {
				if artifactCount > 1 {
					allErrs = append(allErrs, fmt.Errorf("spec.reference.artifacts[%d].type is required when multiple artifacts exist", i))
				}
				artifactType = CatalogItemArtifactTypeContainer // default for single artifact
			} else if !isValidCatalogItemArtifactType(artifactType) {
				allErrs = append(allErrs, fmt.Errorf("spec.reference.artifacts[%d].type: invalid value %q", i, artifactType))
			}
			if artifactType != "" {
				if _, exists := seenTypes[artifactType]; exists {
					allErrs = append(allErrs, fmt.Errorf("spec.reference.artifacts[%d].type: duplicate type %q", i, artifactType))
				}
				seenTypes[artifactType] = struct{}{}
			}
			if artifact.Uri == "" {
				allErrs = append(allErrs, fmt.Errorf("spec.reference.artifacts[%d].uri is required", i))
			} else {
				allErrs = append(allErrs, validateArtifactURI(artifact.Uri, fmt.Sprintf("spec.reference.artifacts[%d].uri", i))...)
			}
		}
	}

	return allErrs
}

func isValidCatalogItemArtifactType(t CatalogItemArtifactType) bool {
	switch t {
	case CatalogItemArtifactTypeContainer,
		CatalogItemArtifactTypeQcow2,
		CatalogItemArtifactTypeAmi,
		CatalogItemArtifactTypeIso,
		CatalogItemArtifactTypeAnacondaIso,
		CatalogItemArtifactTypeVmdk,
		CatalogItemArtifactTypeVhd,
		CatalogItemArtifactTypeRaw,
		CatalogItemArtifactTypeGce:
		return true
	default:
		return false
	}
}

func validateArtifactURI(uri string, path string) []error {
	allErrs := []error{}
	if uri == "" {
		return allErrs
	}

	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		if len(uri) < 10 {
			allErrs = append(allErrs, fmt.Errorf("%s: invalid URL format", path))
		}
	} else if strings.HasPrefix(uri, "s3://") {
		if len(uri) < 8 || !strings.Contains(uri[5:], "/") {
			allErrs = append(allErrs, fmt.Errorf("%s: invalid S3 URI format (expected s3://bucket/key)", path))
		}
	} else if strings.HasPrefix(uri, "oci://") {
		ociRef := uri[6:]
		if len(ociRef) < 3 || !strings.Contains(ociRef, "/") {
			allErrs = append(allErrs, fmt.Errorf("%s: invalid OCI URI format", path))
		}
	} else {
		if !strings.Contains(uri, "/") && !strings.Contains(uri, ".") {
			allErrs = append(allErrs, fmt.Errorf("%s: invalid artifact URI format", path))
		}
	}

	return allErrs
}

func validateCatalogItemVersion(version CatalogItemVersion, index int, seenVersions map[string]struct{}, category CatalogItemCategory, itemType CatalogItemType) []error {
	allErrs := []error{}
	pathPrefix := fmt.Sprintf("spec.versions[%d]", index)

	// version is required and must be semver
	if version.Version == "" {
		allErrs = append(allErrs, fmt.Errorf("%s.version: required", pathPrefix))
	} else {
		if err := validateSemver(version.Version); err != nil {
			allErrs = append(allErrs, fmt.Errorf("%s.version: %v", pathPrefix, err))
		}
		if _, exists := seenVersions[version.Version]; exists {
			allErrs = append(allErrs, fmt.Errorf("%s.version: duplicate version %q", pathPrefix, version.Version))
		}
		seenVersions[version.Version] = struct{}{}
	}

	// exactly one of tag or digest must be specified
	hasTag := version.Tag != nil && *version.Tag != ""
	hasDigest := version.Digest != nil && *version.Digest != ""
	if !hasTag && !hasDigest {
		allErrs = append(allErrs, fmt.Errorf("%s: exactly one of tag or digest must be specified", pathPrefix))
	} else if hasTag && hasDigest {
		allErrs = append(allErrs, fmt.Errorf("%s: tag and digest are mutually exclusive", pathPrefix))
	}

	if hasDigest {
		allErrs = append(allErrs, validateOCIDigest(*version.Digest, pathPrefix+".digest")...)
	}

	if len(version.Channels) == 0 {
		allErrs = append(allErrs, fmt.Errorf("%s.channels: must have at least one channel", pathPrefix))
	}

	// validate replaces is semver
	if version.Replaces != nil && *version.Replaces != "" {
		if err := validateSemver(*version.Replaces); err != nil {
			allErrs = append(allErrs, fmt.Errorf("%s.replaces: %v", pathPrefix, err))
		}
	}

	// validate each skip is semver
	if version.Skips != nil {
		for i, skip := range *version.Skips {
			if err := validateSemver(skip); err != nil {
				allErrs = append(allErrs, fmt.Errorf("%s.skips[%d]: %v", pathPrefix, i, err))
			}
		}
	}

	// skipRange is a semver range constraint, not a single version - validate format
	if version.SkipRange != nil && *version.SkipRange != "" {
		if err := validateSemverRange(*version.SkipRange); err != nil {
			allErrs = append(allErrs, fmt.Errorf("%s.skipRange: %v", pathPrefix, err))
		}
	}

	if version.Config != nil {
		allErrs = append(allErrs, validateCatalogItemConfig(category, itemType, version.Config, pathPrefix)...)
	}

	if version.ConfigSchema != nil {
		allErrs = append(allErrs, validateConfigSchema(version.ConfigSchema, pathPrefix+".configSchema")...)
	}

	if version.Deprecation != nil {
		allErrs = append(allErrs, validateCatalogItemDeprecation(version.Deprecation, pathPrefix+".deprecation")...)
	}

	return allErrs
}

func validateReplacesGraph(versions []CatalogItemVersion) []error {
	allErrs := []error{}

	replacesMap := make(map[string]string)
	for _, v := range versions {
		if v.Replaces == nil || *v.Replaces == "" || v.Version == "" {
			continue
		}
		target := *v.Replaces

		if v.Version == target {
			allErrs = append(allErrs, fmt.Errorf("version %q cannot replace itself", v.Version))
			continue
		}

		replacesMap[v.Version] = target
	}

	for start := range replacesMap {
		visited := make(map[string]bool)
		current := start
		for {
			if visited[current] {
				allErrs = append(allErrs, fmt.Errorf("circular replaces chain detected involving version %q", start))
				break
			}
			visited[current] = true
			next, hasNext := replacesMap[current]
			if !hasNext {
				break
			}
			current = next
		}
	}

	return allErrs
}

// validateSemver checks if a string is a valid semantic version.
// Version must be strict semver without a "v" prefix (e.g., "1.0.0", not "v1.0.0").
// The tag field can have any format including "v" prefix.
func validateSemver(v string) error {
	if strings.HasPrefix(v, "v") {
		return fmt.Errorf("version must not have 'v' prefix; use semver format (e.g., 1.0.0)")
	}

	// Handle build metadata (+build.123)
	v = strings.SplitN(v, "+", 2)[0]

	// Basic semver pattern: MAJOR.MINOR.PATCH with optional pre-release
	// Examples: 1.0.0, 1.2.3-alpha, 1.2.3-rc.1
	parts := strings.SplitN(v, "-", 2)
	coreParts := strings.Split(parts[0], ".")

	if len(coreParts) < 2 || len(coreParts) > 3 {
		return fmt.Errorf("must be valid semver (e.g., 1.0.0, 2.1.0-rc1)")
	}

	for i, part := range coreParts {
		if part == "" {
			return fmt.Errorf("must be valid semver (e.g., 1.0.0, 2.1.0-rc1)")
		}
		// Check that each part is numeric
		for _, c := range part {
			if c < '0' || c > '9' {
				return fmt.Errorf("version component %d must be numeric", i+1)
			}
		}
	}

	return nil
}

// validateSemverRange checks if a string is a valid semver range constraint.
func validateSemverRange(r string) error {
	// Basic validation for semver range patterns like ">=1.0.0 <2.0.0"
	// Allow common operators: >=, <=, >, <, =, ~, ^
	if r == "" {
		return fmt.Errorf("semver range cannot be empty")
	}

	// Split by space to handle compound ranges like ">=1.0.0 <2.0.0"
	parts := strings.Fields(r)
	if len(parts) == 0 {
		return fmt.Errorf("semver range cannot be empty")
	}

	for _, part := range parts {
		// Strip operators
		version := strings.TrimLeft(part, ">=<~^")
		if version == "" {
			return fmt.Errorf("invalid semver range: missing version after operator in %q", part)
		}
		if err := validateSemver(version); err != nil {
			return fmt.Errorf("invalid semver range: %v in %q", err, part)
		}
	}

	return nil
}

func validateOCIDigest(digest string, path string) []error {
	allErrs := []error{}

	parts := strings.SplitN(digest, ":", 2)
	if len(parts) != 2 {
		allErrs = append(allErrs, fmt.Errorf("%s: must be in format 'algorithm:hex' (e.g., sha256:abc123...)", path))
		return allErrs
	}

	algorithm := parts[0]
	hex := parts[1]

	expectedLengths := map[string]int{
		"sha256": 64,
		"sha384": 96,
		"sha512": 128,
	}

	expectedLen, ok := expectedLengths[algorithm]
	if !ok {
		allErrs = append(allErrs, fmt.Errorf("%s: unsupported digest algorithm %q (supported: sha256, sha384, sha512)", path, algorithm))
		return allErrs
	}

	if len(hex) != expectedLen {
		allErrs = append(allErrs, fmt.Errorf("%s: %s digest must be %d hex characters, got %d", path, algorithm, expectedLen, len(hex)))
	}

	hexPattern := regexp.MustCompile(`^[a-fA-F0-9]+$`)
	if !hexPattern.MatchString(hex) {
		allErrs = append(allErrs, fmt.Errorf("%s: digest must contain only hex characters", path))
	}

	return allErrs
}

func validateCatalogItemDeprecation(dep *CatalogItemDeprecation, path string) []error {
	allErrs := []error{}

	if dep.Message == "" {
		allErrs = append(allErrs, fmt.Errorf("%s.message is required", path))
	}

	if dep.Replacement != nil && *dep.Replacement != "" {
		name := *dep.Replacement
		if len(name) > 253 {
			allErrs = append(allErrs, fmt.Errorf("%s.replacement: must be at most 253 characters", path))
		}
	}

	return allErrs
}

// isValidCatalogItemType checks if the given type is a known CatalogItemType enum value.
func isValidCatalogItemType(t CatalogItemType) bool {
	switch t {
	case CatalogItemTypeOS, CatalogItemTypeFirmware, CatalogItemTypeDriver,
		CatalogItemTypeContainer, CatalogItemTypeHelm, CatalogItemTypeQuadlet, CatalogItemTypeCompose,
		CatalogItemTypeData:
		return true
	default:
		return false
	}
}

// validCatalogItemTypes returns a comma-separated string of valid CatalogItemType values.
func validCatalogItemTypes() string {
	return "os, firmware, driver, container, helm, quadlet, compose, data"
}

// validateCategoryTypeCompatibility ensures the category and type are compatible.
// - system: os, firmware, driver
// - application: container, helm, quadlet, compose, data
func validateCategoryTypeCompatibility(category CatalogItemCategory, itemType CatalogItemType) error {
	switch category {
	case CatalogItemCategorySystem:
		if itemType != CatalogItemTypeOS && itemType != CatalogItemTypeFirmware && itemType != CatalogItemTypeDriver {
			return fmt.Errorf("spec.type %q is not valid for category %q; must be %q, %q, or %q",
				itemType, category, CatalogItemTypeOS, CatalogItemTypeFirmware, CatalogItemTypeDriver)
		}
	case CatalogItemCategoryApplication:
		if itemType != CatalogItemTypeContainer && itemType != CatalogItemTypeHelm &&
			itemType != CatalogItemTypeQuadlet && itemType != CatalogItemTypeCompose &&
			itemType != CatalogItemTypeData {
			return fmt.Errorf("spec.type %q is not valid for category %q; must be %q, %q, %q, %q, or %q",
				itemType, category, CatalogItemTypeContainer, CatalogItemTypeHelm,
				CatalogItemTypeQuadlet, CatalogItemTypeCompose, CatalogItemTypeData)
		}
	}
	return nil
}

// validateCatalogItemConfig validates the config field based on category and type.
func validateCatalogItemConfig(category CatalogItemCategory, itemType CatalogItemType, config *map[string]interface{}, pathPrefix string) []error {
	if config == nil {
		return nil
	}

	allErrs := []error{}
	configPath := pathPrefix + ".config"

	switch category {
	case CatalogItemCategorySystem:
		// System types (os, firmware) don't have config validation yet
	case CatalogItemCategoryApplication:
		switch itemType {
		case CatalogItemTypeContainer:
			allErrs = append(allErrs, validateContainerConfig(*config, configPath)...)
		case CatalogItemTypeHelm:
			allErrs = append(allErrs, validateHelmConfig(*config, configPath)...)
		case CatalogItemTypeQuadlet:
			allErrs = append(allErrs, validateQuadletConfig(*config, configPath)...)
		case CatalogItemTypeCompose:
			allErrs = append(allErrs, validateComposeConfig(*config, configPath)...)
		}
	}

	return allErrs
}

// Known fields for each CatalogItem type
var containerKnownFields = []string{"envVars", "ports", "resources", "volumes"}
var helmKnownFields = []string{"namespace", "values", "valuesFiles"}
var quadletKnownFields = []string{"envVars", "volumes"}

// validateContainerConfig validates config for container type.
func validateContainerConfig(config map[string]interface{}, pathPrefix string) []error {
	allErrs := []error{}

	// Check for unknown fields
	allErrs = append(allErrs, validateKnownFields(config, pathPrefix, containerKnownFields)...)

	// Validate envVars
	if envVars, err := extractStringMap(config, "envVars"); err != nil {
		allErrs = append(allErrs, fmt.Errorf("%s.envVars: %v", pathPrefix, err))
	} else if envVars != nil {
		allErrs = append(allErrs, validateEnvVars(envVars, pathPrefix)...)
	}

	// Validate ports
	if ports, err := extractStringSlice(config, "ports"); err != nil {
		allErrs = append(allErrs, fmt.Errorf("%s.ports: %v", pathPrefix, err))
	} else if ports != nil {
		allErrs = append(allErrs, validateContainerPorts(ports, pathPrefix+".ports")...)
	}

	// Validate resources
	if resources, ok := config["resources"].(map[string]interface{}); ok {
		if limits, ok := resources["limits"].(map[string]interface{}); ok {
			if cpu, ok := limits["cpu"].(string); ok {
				allErrs = append(allErrs, validatePodmanCPULimit(&cpu, pathPrefix+".resources.limits.cpu")...)
			}
			if memory, ok := limits["memory"].(string); ok {
				allErrs = append(allErrs, validatePodmanMemoryLimit(&memory, pathPrefix+".resources.limits.memory")...)
			}
		}
	}

	// Validate volumes
	if raw, exists := config["volumes"]; exists {
		volumesRaw, ok := raw.([]interface{})
		if !ok {
			allErrs = append(allErrs, fmt.Errorf("%s.volumes: must be an array", pathPrefix))
		} else {
			for i, volRaw := range volumesRaw {
				if vol, ok := volRaw.(map[string]interface{}); ok {
					if name, ok := vol["name"].(string); ok {
						allErrs = append(allErrs, validation.ValidateString(&name, fmt.Sprintf("%s.volumes[%d].name", pathPrefix, i), 1, 253, validation.GenericNameRegexp, "")...)
					} else {
						allErrs = append(allErrs, fmt.Errorf("%s.volumes[%d].name: required", pathPrefix, i))
					}
				} else {
					allErrs = append(allErrs, fmt.Errorf("%s.volumes[%d]: must be an object", pathPrefix, i))
				}
			}
		}
	}

	return allErrs
}

// validateHelmConfig validates config for helm type.
func validateHelmConfig(config map[string]interface{}, pathPrefix string) []error {
	allErrs := []error{}

	// Check for unknown fields
	allErrs = append(allErrs, validateKnownFields(config, pathPrefix, helmKnownFields)...)

	// Validate namespace if present
	if namespace, ok := config["namespace"].(string); ok && namespace != "" {
		allErrs = append(allErrs, validation.ValidateString(&namespace, pathPrefix+".namespace", 1, validation.DNS1123MaxLength, validation.GenericNameRegexp, "")...)
	}

	// Validate valuesFiles if present
	if valuesFiles, err := extractStringSlice(config, "valuesFiles"); err != nil {
		allErrs = append(allErrs, fmt.Errorf("%s.valuesFiles: %v", pathPrefix, err))
	} else if valuesFiles != nil {
		for i, file := range *valuesFiles {
			if !strings.HasSuffix(file, ".yaml") && !strings.HasSuffix(file, ".yml") {
				allErrs = append(allErrs, fmt.Errorf("%s.valuesFiles[%d]: must have .yaml or .yml extension, got %q", pathPrefix, i, file))
			}
		}
	}

	// config.values is arbitrary nested YAML for Helm - no validation needed

	return allErrs
}

// validateQuadletConfig validates config for quadlet type.
func validateQuadletConfig(config map[string]interface{}, pathPrefix string) []error {
	allErrs := []error{}

	// Check for unknown fields
	allErrs = append(allErrs, validateKnownFields(config, pathPrefix, quadletKnownFields)...)

	// Validate envVars
	if envVars, err := extractStringMap(config, "envVars"); err != nil {
		allErrs = append(allErrs, fmt.Errorf("%s.envVars: %v", pathPrefix, err))
	} else if envVars != nil {
		allErrs = append(allErrs, validateEnvVars(envVars, pathPrefix)...)
	}

	// Validate volumes
	if raw, exists := config["volumes"]; exists {
		volumesRaw, ok := raw.([]interface{})
		if !ok {
			allErrs = append(allErrs, fmt.Errorf("%s.volumes: must be an array", pathPrefix))
		} else {
			for i, volRaw := range volumesRaw {
				if vol, ok := volRaw.(map[string]interface{}); ok {
					if name, ok := vol["name"].(string); ok {
						allErrs = append(allErrs, validation.ValidateString(&name, fmt.Sprintf("%s.volumes[%d].name", pathPrefix, i), 1, 253, validation.GenericNameRegexp, "")...)
					} else {
						allErrs = append(allErrs, fmt.Errorf("%s.volumes[%d].name: required", pathPrefix, i))
					}
				} else {
					allErrs = append(allErrs, fmt.Errorf("%s.volumes[%d]: must be an object", pathPrefix, i))
				}
			}
		}
	}

	return allErrs
}

// validateComposeConfig validates config for compose type.
func validateComposeConfig(config map[string]interface{}, pathPrefix string) []error {
	// Compose uses same fields as Quadlet
	return validateQuadletConfig(config, pathPrefix)
}

// Helper functions

// extractStringMap extracts a map[string]string from a map.
func extractStringMap(data map[string]interface{}, key string) (*map[string]string, error) {
	raw, ok := data[key]
	if !ok {
		return nil, nil
	}

	mapRaw, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("must be an object")
	}

	result := make(map[string]string)
	for k, v := range mapRaw {
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("value for key %q must be a string", k)
		}
		result[k] = str
	}
	return &result, nil
}

// extractStringSlice extracts a []string from a map.
func extractStringSlice(data map[string]interface{}, key string) (*[]string, error) {
	raw, ok := data[key]
	if !ok {
		return nil, nil
	}

	sliceRaw, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("must be an array")
	}

	result := make([]string, len(sliceRaw))
	for i, v := range sliceRaw {
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("element %d must be a string", i)
		}
		result[i] = str
	}
	return &result, nil
}

// validateKnownFields checks that data only contains known field names.
func validateKnownFields(data map[string]interface{}, pathPrefix string, knownFields []string) []error {
	allErrs := []error{}
	known := make(map[string]struct{}, len(knownFields))
	for _, f := range knownFields {
		known[f] = struct{}{}
	}
	for key := range data {
		if _, ok := known[key]; !ok {
			allErrs = append(allErrs, fmt.Errorf("%s: %q is not a valid key, valid keys are: %v", pathPrefix, key, knownFields))
		}
	}
	return allErrs
}

// validateConfigSchema validates the configSchema field is a valid JSON Schema.
func validateConfigSchema(schema *map[string]any, path string) []error {
	if schema == nil {
		return nil
	}

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return []error{fmt.Errorf("%s: failed to marshal schema: %v", path, err)}
	}

	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(u string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("%s: external schema references are forbidden", path)
	}
	if err := compiler.AddResource("configSchema.json", strings.NewReader(string(schemaBytes))); err != nil {
		return []error{fmt.Errorf("%s: invalid JSON Schema: %v", path, err)}
	}

	if _, err := compiler.Compile("configSchema.json"); err != nil {
		return []error{fmt.Errorf("%s: invalid JSON Schema: %v", path, err)}
	}

	return nil
}

// validateEnvVars validates environment variable names and values.
func validateEnvVars(envVars *map[string]string, pathPrefix string) []error {
	return validation.ValidateStringMap(envVars, pathPrefix+".envVars", 1, validation.DNS1123MaxLength, validation.EnvVarNameRegexp, nil, "")
}

// Port validation constants
const (
	privilegedPortRangeStart = 1
	portRangeEnd             = 65535
)

// validateContainerPorts validates container port mappings.
func validateContainerPorts(ports *[]string, path string) []error {
	if ports == nil || len(*ports) == 0 {
		return nil
	}

	var allErrs []error
	portPattern := regexp.MustCompile(`^[0-9]+:[0-9]+$`)

	for i, portString := range *ports {
		formatErr := fmt.Errorf("%s[%d]: must be in format 'portnumber:portnumber', got %q", path, i, portString)
		if !portPattern.MatchString(portString) {
			allErrs = append(allErrs, formatErr)
			continue
		}
		portParts := strings.Split(portString, ":")
		if len(portParts) != 2 {
			allErrs = append(allErrs, formatErr)
			continue
		}

		for _, port := range portParts {
			numberErr := fmt.Errorf("%s[%d]: must be a number in the valid port range of [1, 65535], got: %q", path, i, port)
			portNumber, err := strconv.Atoi(port)
			if err != nil {
				allErrs = append(allErrs, fmt.Errorf("%w: %w", numberErr, err))
				continue
			}
			if portNumber < privilegedPortRangeStart || portNumber > portRangeEnd {
				allErrs = append(allErrs, numberErr)
			}
		}
	}
	return allErrs
}

// validatePodmanCPULimit validates CPU limit format for Podman.
func validatePodmanCPULimit(cpu *string, path string) []error {
	var errs []error
	if cpu == nil {
		return errs
	}

	val, err := strconv.ParseFloat(*cpu, 64)
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: must be a valid number, got %q", path, *cpu))
	} else if val < 0 {
		errs = append(errs, fmt.Errorf("%s: must be positive. got %q", path, *cpu))
	}
	return errs
}

var podmanMemoryLimitPattern = regexp.MustCompile(`^[0-9]+[bkmg]?$`)

// validatePodmanMemoryLimit validates memory limit format for Podman.
func validatePodmanMemoryLimit(memory *string, path string) []error {
	if memory == nil {
		return nil
	}

	if !podmanMemoryLimitPattern.MatchString(*memory) {
		return []error{fmt.Errorf("%s: must be in format 'number[unit]' where unit is b, k, m, or g, got %q", path, *memory)}
	}
	return nil
}

// validateImmutableCoreFields validates that immutable core fields haven't changed.
func validateImmutableCoreFields(oldName *string, newName *string, oldApiVersion string, newApiVersion string, oldKind string, newKind string, oldStatus, newStatus interface{}) []error {
	allErrs := []error{}

	if (oldName == nil) != (newName == nil) {
		allErrs = append(allErrs, errors.New("metadata.name is immutable"))
	} else if oldName != nil && newName != nil && *oldName != *newName {
		allErrs = append(allErrs, errors.New("metadata.name is immutable"))
	}
	if oldApiVersion != newApiVersion {
		allErrs = append(allErrs, errors.New("apiVersion is immutable"))
	}
	if oldKind != newKind {
		allErrs = append(allErrs, errors.New("kind is immutable"))
	}

	return allErrs
}
