package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("OpenAPI Schema Validation", func() {
	Context("additionalProperties verification", func() {
		It("should have explicit additionalProperties for main resource schemas", func() {
			// Find all OpenAPI YAML files using absolute path resolution
			_, currentFile, _, _ := runtime.Caller(0)
			testDir := filepath.Dir(currentFile)
			apiDir := filepath.Join(testDir, "..", "..", "..", "api")
			openAPIFiles := []string{}

			err := filepath.Walk(apiDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if strings.HasSuffix(info.Name(), "openapi.yaml") || strings.HasSuffix(info.Name(), "openapi.yml") {
					openAPIFiles = append(openAPIFiles, path)
				}
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(openAPIFiles).ToNot(BeEmpty(), "Should find at least one OpenAPI file")

			// Check each OpenAPI file
			for _, filePath := range openAPIFiles {
				By(fmt.Sprintf("Checking %s", filePath))

				// Read and parse the YAML file
				yamlData, err := os.ReadFile(filePath)
				Expect(err).ToNot(HaveOccurred())

				var spec map[string]interface{}
				err = yaml.Unmarshal(yamlData, &spec)
				Expect(err).ToNot(HaveOccurred())

				// Check schemas in components/schemas
				components, exists := spec["components"]
				if !exists {
					continue // Skip files without components
				}

				componentsMap, ok := components.(map[string]interface{})
				if !ok {
					continue
				}

				schemas, exists := componentsMap["schemas"]
				if !exists {
					continue // Skip files without schemas
				}

				schemasMap, ok := schemas.(map[string]interface{})
				if !ok {
					continue
				}

				// Validate main API resource schemas (ones that have apiVersion, kind, metadata, spec patterns)
				violations := []string{}
				for schemaName, schemaData := range schemasMap {
					schemaMap, ok := schemaData.(map[string]interface{})
					if !ok {
						continue
					}

					// Only check the specific resources we're targeting for this validation fix
					if isTargetResourceForValidationFix(schemaName) {
						violations = append(violations, validateSchemaAdditionalProperties(schemaName, schemaMap, "")...)
					}
				}

				if len(violations) > 0 {
					Fail(fmt.Sprintf("File %s has schemas missing explicit additionalProperties:\n%s",
						filePath, strings.Join(violations, "\n")))
				}
			}
		})
	})
})

func validateSchemaAdditionalProperties(name string, schema map[string]interface{}, path string) []string {
	violations := []string{}

	// Build the current path for error reporting
	currentPath := path
	if currentPath == "" {
		currentPath = name
	} else {
		currentPath = currentPath + "." + name
	}

	// Check if this is an object type schema with properties
	schemaType, hasType := schema["type"]
	_, hasProperties := schema["properties"]
	_, hasAdditionalProperties := schema["additionalProperties"]

	// If this is an object with properties but no explicit additionalProperties, that's a violation
	if hasType && schemaType == "object" && hasProperties && !hasAdditionalProperties {
		// Check if this schema has composition keywords (allOf, anyOf, oneOf)
		// These are often used for inheritance and may not need additionalProperties
		hasComposition := false
		for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
			if _, exists := schema[keyword]; exists {
				hasComposition = true
				break
			}
		}

		if !hasComposition {
			violations = append(violations, fmt.Sprintf("  - %s: object schema with properties but no explicit additionalProperties", currentPath))
		}
	}

	// Recursively check nested schemas
	violations = append(violations, checkNestedSchemas(schema, currentPath)...)

	return violations
}

func checkNestedSchemas(schema map[string]interface{}, path string) []string {
	violations := []string{}

	// Check properties
	if properties, exists := schema["properties"]; exists {
		if propsMap, ok := properties.(map[string]interface{}); ok {
			for propName, propSchema := range propsMap {
				if propSchemaMap, ok := propSchema.(map[string]interface{}); ok {
					violations = append(violations, validateSchemaAdditionalProperties(propName, propSchemaMap, path+".properties")...)
				}
			}
		}
	}

	// Check patternProperties
	if patternProps, exists := schema["patternProperties"]; exists {
		if patternPropsMap, ok := patternProps.(map[string]interface{}); ok {
			for pattern, patternSchema := range patternPropsMap {
				if patternSchemaMap, ok := patternSchema.(map[string]interface{}); ok {
					violations = append(violations, validateSchemaAdditionalProperties(pattern, patternSchemaMap, path+".patternProperties")...)
				}
			}
		}
	}

	// Check additionalProperties (if it's a schema object)
	if additionalProps, exists := schema["additionalProperties"]; exists {
		if addPropsMap, ok := additionalProps.(map[string]interface{}); ok {
			violations = append(violations, validateSchemaAdditionalProperties("additionalProperties", addPropsMap, path)...)
		}
	}

	// Check items (for arrays)
	if items, exists := schema["items"]; exists {
		if itemsMap, ok := items.(map[string]interface{}); ok {
			violations = append(violations, validateSchemaAdditionalProperties("items", itemsMap, path)...)
		}
	}

	// Check composition schemas (allOf, anyOf, oneOf)
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if composition, exists := schema[keyword]; exists {
			if compositionArray, ok := composition.([]interface{}); ok {
				for i, compositionSchema := range compositionArray {
					if compositionSchemaMap, ok := compositionSchema.(map[string]interface{}); ok {
						indexName := fmt.Sprintf("%s[%d]", keyword, i)
						violations = append(violations, validateSchemaAdditionalProperties(indexName, compositionSchemaMap, path)...)
					}
				}
			}
		}
	}

	return violations
}

func isTargetResourceForValidationFix(name string) bool {
	// Resources that should have strict validation (additionalProperties: false)
	targetResources := []string{
		"Fleet", "Device", "FleetSpec", "FleetStatus", "DeviceStatus",
		"Catalog", "CatalogSpec", "CatalogStatus", // v1alpha1 resources
		"ImageBuild", "ImageBuildSpec", "ImageExport", "ImageExportSpec", // imagebuilder resources
		"TokenRequest", // pam-issuer request schemas (responses allow additional properties for OIDC compliance)
	}

	// Resources explicitly excluded from strict validation with reasons
	excludedResources := map[string]string{
		"DeviceSpec":              "Used in allOf compositions (e.g., TemplateVersionStatus)",
		"CatalogItemVersion":      "Used in allOf compositions with CatalogItemConfigurable",
		"CatalogItemConfigurable": "Used in allOf compositions",
		"TokenResponse":           "OIDC/OAuth2 responses should allow additional fields",
		"UserInfoResponse":        "OIDC UserInfo responses should allow arbitrary claims",
		"JWKSResponse":            "JWKS responses may contain additional standard fields",
		"OAuth2Error":             "OAuth2 error responses may contain additional fields",
	}

	// Check if it's in our target list
	for _, target := range targetResources {
		if name == target {
			return true
		}
	}

	// Check if it's explicitly excluded
	if _, excluded := excludedResources[name]; excluded {
		return false
	}

	// For resources not in either list, we should consider adding them
	// This helps ensure new resources are explicitly handled
	return false
}
