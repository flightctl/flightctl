package v1alpha1

import (
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCatalogItemValidate(t *testing.T) {
	require := require.New(t)

	// Helper to create versions (Cincinnati model: versions with channel labels)
	makeVersions := func(versions ...CatalogItemVersion) []CatalogItemVersion {
		return versions
	}

	v := func(version string, channels ...string) CatalogItemVersion {
		// Strip "v" prefix for Version field (strict semver), but keep original for Tag
		semverVersion := strings.TrimPrefix(version, "v")
		return CatalogItemVersion{Version: semverVersion, Tag: lo.ToPtr(version), Channels: channels}
	}

	tests := []struct {
		name        string
		itemType    CatalogItemType
		reference   CatalogItemReference
		versions    []CatalogItemVersion
		wantErr     bool
		errContains string
	}{
		{
			name:      "valid catalog item",
			itemType:  CatalogItemTypeContainer,
			reference: CatalogItemReference{Uri: "quay.io/example/app"},
			versions:  makeVersions(v("v1.0.0", "stable")),
			wantErr:   false,
		},
		{
			name:      "valid with multiple versions and channels",
			itemType:  CatalogItemTypeContainer,
			reference: CatalogItemReference{Uri: "quay.io/example/app"},
			versions: makeVersions(
				v("v2.0.0", "stable", "fast"),
				v("v1.9.0", "stable"),
			),
			wantErr: false,
		},
		{
			name:        "missing type",
			itemType:    "",
			reference:   CatalogItemReference{Uri: "quay.io/example/app"},
			versions:    makeVersions(v("v1.0.0", "stable")),
			wantErr:     true,
			errContains: "spec.type is required",
		},
		{
			name:        "missing reference uri",
			itemType:    CatalogItemTypeContainer,
			reference:   CatalogItemReference{Uri: ""},
			versions:    makeVersions(v("v1.0.0", "stable")),
			wantErr:     true,
			errContains: "spec.reference.uri is required",
		},
		{
			name:        "empty versions",
			itemType:    CatalogItemTypeContainer,
			reference:   CatalogItemReference{Uri: "quay.io/example/app"},
			versions:    []CatalogItemVersion{},
			wantErr:     true,
			errContains: "spec.versions must have at least one entry",
		},
		{
			name:        "missing tag and digest",
			itemType:    CatalogItemTypeContainer,
			reference:   CatalogItemReference{Uri: "quay.io/example/app"},
			versions:    makeVersions(CatalogItemVersion{Version: "1.0.0", Channels: []string{"stable"}}),
			wantErr:     true,
			errContains: "exactly one of tag or digest must be specified",
		},
		{
			name:        "empty version channels",
			itemType:    CatalogItemTypeContainer,
			reference:   CatalogItemReference{Uri: "quay.io/example/app"},
			versions:    makeVersions(CatalogItemVersion{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{}}),
			wantErr:     true,
			errContains: "must have at least one channel",
		},
		{
			name:        "duplicate version",
			itemType:    CatalogItemTypeContainer,
			reference:   CatalogItemReference{Uri: "quay.io/example/app"},
			versions:    makeVersions(v("1.0.0", "stable"), v("1.0.0", "fast")),
			wantErr:     true,
			errContains: "duplicate version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ci := CatalogItem{
				ApiVersion: "flightctl.io/v1alpha1",
				Kind:       "CatalogItem",
				Metadata: CatalogItemMeta{
					Name: lo.ToPtr("test-item"),
				},
				Spec: CatalogItemSpec{
					Type:      tt.itemType,
					Reference: tt.reference,
					Versions:  tt.versions,
				},
			}

			errs := ci.Validate()
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestCatalogItemValuesValidation(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		category    CatalogItemCategory
		itemType    CatalogItemType
		config      *map[string]interface{}
		wantErr     bool
		errContains string
	}{
		// container valid cases
		{
			name:     "container valid values",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"envVars": map[string]interface{}{
					"LOG_LEVEL": "info",
				},
				"ports": []interface{}{"8080:80", "9090:9090"},
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{
						"cpu":    "0.5",
						"memory": "256m",
					},
				},
				"volumes": []interface{}{
					map[string]interface{}{
						"name": "data-volume",
						"mount": map[string]interface{}{
							"path": "/data",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:     "container envVars only",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"envVars": map[string]interface{}{
					"KEY": "value",
				},
			},
			wantErr: false,
		},
		// container invalid cases
		{
			name:     "container invalid port format",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"ports": []interface{}{"invalid-port"},
			},
			wantErr:     true,
			errContains: "must be in format",
		},
		{
			name:     "container invalid cpu format",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{
						"cpu": "invalid",
					},
				},
			},
			wantErr:     true,
			errContains: "cpu",
		},
		{
			name:     "container invalid memory format",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{
						"memory": "invalid",
					},
				},
			},
			wantErr:     true,
			errContains: "memory",
		},
		{
			name:     "container volume missing name",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"volumes": []interface{}{
					map[string]interface{}{
						"mount": map[string]interface{}{
							"path": "/data",
						},
					},
				},
			},
			wantErr:     true,
			errContains: "name: required",
		},
		{
			name:     "container envVars wrong type",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"envVars": "not-a-map",
			},
			wantErr:     true,
			errContains: "must be an object",
		},
		{
			name:     "container ports wrong type",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"ports": "not-an-array",
			},
			wantErr:     true,
			errContains: "must be an array",
		},
		// helm cases
		{
			name:     "helm valid values",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeHelm,
			config: &map[string]interface{}{
				"namespace": "monitoring",
				"values": map[string]interface{}{
					"replicaCount": 3,
				},
				"valuesFiles": []interface{}{"values.yaml", "values-prod.yml"},
			},
			wantErr: false,
		},
		{
			name:     "helm invalid valuesFiles extension",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeHelm,
			config: &map[string]interface{}{
				"valuesFiles": []interface{}{"values.json"},
			},
			wantErr:     true,
			errContains: ".yaml or .yml extension",
		},
		// quadlet cases
		{
			name:     "quadlet valid values",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeQuadlet,
			config: &map[string]interface{}{
				"envVars": map[string]interface{}{
					"KEY": "value",
				},
				"volumes": []interface{}{
					map[string]interface{}{
						"name": "config",
					},
				},
			},
			wantErr: false,
		},
		// compose cases
		{
			name:     "compose valid values",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeCompose,
			config: &map[string]interface{}{
				"envVars": map[string]interface{}{
					"DB_PASSWORD": "secret",
				},
			},
			wantErr: false,
		},
		// unknown fields
		{
			name:     "container unknown field envVar",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"envVar": map[string]interface{}{
					"KEY": "value",
				},
			},
			wantErr:     true,
			errContains: `"envVar" is not a valid key`,
		},
		{
			name:     "container unknown field port",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"port": "8080:80",
			},
			wantErr:     true,
			errContains: `"port" is not a valid key`,
		},
		{
			name:     "container unknown field arbitrary",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"foo": "bar",
			},
			wantErr:     true,
			errContains: `"foo" is not a valid key`,
		},
		{
			name:     "helm unknown field value",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeHelm,
			config: &map[string]interface{}{
				"value": map[string]interface{}{
					"key": "data",
				},
			},
			wantErr:     true,
			errContains: `"value" is not a valid key`,
		},
		{
			name:     "quadlet unknown field ports",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeQuadlet,
			config: &map[string]interface{}{
				"ports": []interface{}{"8080:80"},
			},
			wantErr:     true,
			errContains: `"ports" is not a valid key`,
		},
		// complete examples for each type
		{
			name:     "container complete example",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"envVars": map[string]interface{}{
					"LOG_LEVEL":    "info",
					"DB_HOST":      "localhost",
					"ENABLE_DEBUG": "false",
				},
				"ports": []interface{}{"8080:80", "9090:9090", "443:443"},
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{
						"cpu":    "1.5",
						"memory": "512m",
					},
				},
				"volumes": []interface{}{
					map[string]interface{}{
						"name": "data",
						"mount": map[string]interface{}{
							"path": "/var/data",
						},
					},
					map[string]interface{}{
						"name": "config",
						"mount": map[string]interface{}{
							"path": "/etc/app:ro",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:     "helm complete example",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeHelm,
			config: &map[string]interface{}{
				"namespace": "monitoring",
				"values": map[string]interface{}{
					"replicaCount": 3,
					"image": map[string]interface{}{
						"repository": "nginx",
						"tag":        "1.21",
					},
					"service": map[string]interface{}{
						"type": "ClusterIP",
						"port": 80,
					},
				},
				"valuesFiles": []interface{}{"values.yaml", "values-prod.yml"},
			},
			wantErr: false,
		},
		{
			name:     "quadlet complete example",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeQuadlet,
			config: &map[string]interface{}{
				"envVars": map[string]interface{}{
					"NGINX_WORKER_PROCESSES": "auto",
				},
				"volumes": []interface{}{
					map[string]interface{}{
						"name": "html",
					},
				},
			},
			wantErr: false,
		},
		{
			name:     "compose complete example",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeCompose,
			config: &map[string]interface{}{
				"envVars": map[string]interface{}{
					"MYSQL_ROOT_PASSWORD": "secret",
					"MYSQL_DATABASE":      "app",
				},
				"volumes": []interface{}{
					map[string]interface{}{
						"name": "db-data",
					},
				},
			},
			wantErr: false,
		},
		// mixing fields from wrong type
		{
			name:     "quadlet with ports field (container only)",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeQuadlet,
			config: &map[string]interface{}{
				"envVars": map[string]interface{}{"KEY": "value"},
				"ports":   []interface{}{"8080:80"},
			},
			wantErr:     true,
			errContains: `"ports" is not a valid key`,
		},
		{
			name:     "compose with resources field (container only)",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeCompose,
			config: &map[string]interface{}{
				"envVars": map[string]interface{}{"KEY": "value"},
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{"cpu": "0.5"},
				},
			},
			wantErr:     true,
			errContains: `"resources" is not a valid key`,
		},
		{
			name:     "helm with envVars field (not allowed)",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeHelm,
			config: &map[string]interface{}{
				"namespace": "default",
				"envVars":   map[string]interface{}{"KEY": "value"},
			},
			wantErr:     true,
			errContains: `"envVars" is not a valid key`,
		},
		{
			name:     "container with namespace field (helm only)",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config: &map[string]interface{}{
				"envVars":   map[string]interface{}{"KEY": "value"},
				"namespace": "default",
			},
			wantErr:     true,
			errContains: `"namespace" is not a valid key`,
		},
		// edge cases
		{
			name:     "unknown type skips validation",
			category: CatalogItemCategoryApplication,
			itemType: "future-type",
			config: &map[string]interface{}{
				"anything": "goes",
			},
			wantErr: false,
		},
		{
			name:     "nil values returns no errors",
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			config:   nil,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateCatalogItemConfig(tt.category, tt.itemType, tt.config, "spec.defaults")
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestCatalogItemCategoryValidation(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		category    *CatalogItemCategory
		itemType    CatalogItemType
		wantErr     bool
		errContains string
	}{
		{
			name:     "system category valid",
			category: lo.ToPtr(CatalogItemCategorySystem),
			itemType: CatalogItemTypeOS,
			wantErr:  false,
		},
		{
			name:     "application category valid",
			category: lo.ToPtr(CatalogItemCategoryApplication),
			itemType: CatalogItemTypeContainer,
			wantErr:  false,
		},
		{
			name:     "application data type valid",
			category: lo.ToPtr(CatalogItemCategoryApplication),
			itemType: CatalogItemTypeData,
			wantErr:  false,
		},
		{
			name:     "system driver type valid",
			category: lo.ToPtr(CatalogItemCategorySystem),
			itemType: CatalogItemTypeDriver,
			wantErr:  false,
		},
		{
			name:        "invalid category",
			category:    lo.ToPtr(CatalogItemCategory("invalid")),
			itemType:    CatalogItemTypeContainer,
			wantErr:     true,
			errContains: "spec.category must be",
		},
		{
			name:     "nil category defaults to application",
			category: nil,
			itemType: CatalogItemTypeContainer,
			wantErr:  false,
		},
		{
			name:        "system category with container type is invalid",
			category:    lo.ToPtr(CatalogItemCategorySystem),
			itemType:    CatalogItemTypeContainer,
			wantErr:     true,
			errContains: "not valid for category",
		},
		{
			name:        "application category with os type is invalid",
			category:    lo.ToPtr(CatalogItemCategoryApplication),
			itemType:    CatalogItemTypeOS,
			wantErr:     true,
			errContains: "not valid for category",
		},
		{
			name:        "system category with data type is invalid",
			category:    lo.ToPtr(CatalogItemCategorySystem),
			itemType:    CatalogItemTypeData,
			wantErr:     true,
			errContains: "not valid for category",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ci := CatalogItem{
				ApiVersion: "flightctl.io/v1alpha1",
				Kind:       "CatalogItem",
				Metadata: CatalogItemMeta{
					Name: lo.ToPtr("test-item"),
				},
				Spec: CatalogItemSpec{
					Category:  tt.category,
					Type:      tt.itemType,
					Reference: CatalogItemReference{Uri: "quay.io/example/app"},
					Versions:  []CatalogItemVersion{{Version: "1.0.0", Tag: lo.ToPtr("v1.0.0"), Channels: []string{"stable"}}},
				},
			}

			errs := ci.Validate()
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestConfigSchemaValidation(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name         string
		configSchema *map[string]any
		wantErr      bool
		errContains  string
	}{
		{
			name:         "nil schema is valid",
			configSchema: nil,
			wantErr:      false,
		},
		{
			name:         "empty schema is valid",
			configSchema: &map[string]any{},
			wantErr:      false,
		},
		{
			name: "valid JSON Schema with properties",
			configSchema: &map[string]any{
				"type": "object",
				"properties": map[string]any{
					"envVars": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"LOG_LEVEL": map[string]any{
								"type":        "string",
								"description": "Logging verbosity",
								"enum":        []any{"debug", "info", "warn", "error"},
								"default":     "info",
							},
							"PORT": map[string]any{
								"type":    "integer",
								"minimum": 1,
								"maximum": 65535,
							},
							"ENABLED": map[string]any{
								"type": "boolean",
							},
						},
						"required": []any{"LOG_LEVEL"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid JSON Schema with oneOf",
			configSchema: &map[string]any{
				"oneOf": []any{
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"mode": map[string]any{"const": "simple"},
						},
					},
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"mode":     map[string]any{"const": "advanced"},
							"advanced": map[string]any{"type": "object"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid JSON Schema with if/then/else",
			configSchema: &map[string]any{
				"type": "object",
				"properties": map[string]any{
					"enabled": map[string]any{"type": "boolean"},
					"config":  map[string]any{"type": "object"},
				},
				"if": map[string]any{
					"properties": map[string]any{
						"enabled": map[string]any{"const": true},
					},
				},
				"then": map[string]any{
					"required": []any{"config"},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid JSON Schema type",
			configSchema: &map[string]any{
				"type": "invalid-type",
			},
			wantErr:     true,
			errContains: "invalid JSON Schema",
		},
		{
			name: "invalid JSON Schema structure",
			configSchema: &map[string]any{
				"type": "object",
				"properties": map[string]any{
					"field": map[string]any{
						"type":    "string",
						"minimum": "not-a-number", // minimum should be a number
					},
				},
			},
			wantErr:     true,
			errContains: "invalid JSON Schema",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateConfigSchema(tt.configSchema, "spec.configSchema")
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestCatalogItemWithConfigSchema(t *testing.T) {
	require := require.New(t)

	ci := CatalogItem{
		ApiVersion: "flightctl.io/v1alpha1",
		Kind:       "CatalogItem",
		Metadata: CatalogItemMeta{
			Name: lo.ToPtr("prometheus"),
		},
		Spec: CatalogItemSpec{
			Category:  lo.ToPtr(CatalogItemCategoryApplication),
			Type:      CatalogItemTypeContainer,
			Reference: CatalogItemReference{Uri: "quay.io/prometheus/prometheus"},
			Versions:  []CatalogItemVersion{{Version: "2.45.0", Tag: lo.ToPtr("v2.45.0"), Channels: []string{"stable"}}},
			Defaults: &CatalogItemConfigurable{
				Config: &map[string]interface{}{
					"envVars": map[string]interface{}{
						"RETENTION": "15d",
					},
				},
				ConfigSchema: &map[string]any{
					"type": "object",
					"properties": map[string]any{
						"envVars": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"RETENTION": map[string]any{
									"type":        "string",
									"description": "How long to keep metrics data",
									"default":     "15d",
									"pattern":     "^[0-9]+[dhm]$",
								},
							},
							"required": []any{"RETENTION"},
						},
					},
				},
			},
		},
	}

	errs := ci.Validate()
	require.Empty(errs, "CatalogItem with valid JSON Schema configSchema should pass validation")
}

func TestSemverValidation(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{"valid major.minor.patch", "1.0.0", false},
		{"invalid with v prefix", "v1.0.0", true},
		{"valid major.minor", "1.0", false},
		{"valid with prerelease", "1.0.0-alpha", false},
		{"valid with prerelease rc", "2.1.0-rc.1", false},
		{"valid with build metadata", "1.0.0+build.123", false},
		{"valid with prerelease and build", "1.0.0-alpha+build", false},
		{"invalid empty", "", true},
		{"invalid no dots", "100", true},
		{"invalid letters in version", "1.a.0", true},
		{"invalid too many parts", "1.0.0.0.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSemver(tt.version)
			if tt.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestSemverRangeValidation(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name    string
		range_  string
		wantErr bool
	}{
		{"valid single constraint", ">=1.0.0", false},
		{"valid range", ">=1.0.0 <2.0.0", false},
		{"valid with caret", "^1.0.0", false},
		{"valid with tilde", "~1.0.0", false},
		{"valid exact", "=1.0.0", false},
		{"invalid empty", "", true},
		{"invalid missing version", ">=", true},
		{"invalid bad version", ">=abc", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSemverRange(tt.range_)
			if tt.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestCatalogItemVersionValidation(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		version     CatalogItemVersion
		wantErr     bool
		errContains string
	}{
		{
			name: "valid with tag",
			version: CatalogItemVersion{
				Version:  "1.0.0",
				Tag:      lo.ToPtr("v1.0.0"),
				Channels: []string{"stable"},
			},
			wantErr: false,
		},
		{
			name: "valid with digest",
			version: CatalogItemVersion{
				Version:  "1.0.0",
				Digest:   lo.ToPtr("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"),
				Channels: []string{"stable"},
			},
			wantErr: false,
		},
		{
			name: "missing version",
			version: CatalogItemVersion{
				Tag:      lo.ToPtr("v1.0.0"),
				Channels: []string{"stable"},
			},
			wantErr:     true,
			errContains: "version: required",
		},
		{
			name: "invalid semver",
			version: CatalogItemVersion{
				Version:  "not-semver",
				Tag:      lo.ToPtr("v1.0.0"),
				Channels: []string{"stable"},
			},
			wantErr:     true,
			errContains: "must be valid semver",
		},
		{
			name: "missing tag and digest",
			version: CatalogItemVersion{
				Version:  "1.0.0",
				Channels: []string{"stable"},
			},
			wantErr:     true,
			errContains: "exactly one of tag or digest",
		},
		{
			name: "both tag and digest",
			version: CatalogItemVersion{
				Version:  "1.0.0",
				Tag:      lo.ToPtr("v1.0.0"),
				Digest:   lo.ToPtr("sha256:abc123"),
				Channels: []string{"stable"},
			},
			wantErr:     true,
			errContains: "mutually exclusive",
		},
		{
			name: "invalid replaces semver",
			version: CatalogItemVersion{
				Version:  "1.0.0",
				Tag:      lo.ToPtr("v1.0.0"),
				Channels: []string{"stable"},
				Replaces: lo.ToPtr("not-semver"),
			},
			wantErr:     true,
			errContains: "replaces",
		},
		{
			name: "valid replaces",
			version: CatalogItemVersion{
				Version:  "2.0.0",
				Tag:      lo.ToPtr("v2.0.0"),
				Channels: []string{"stable"},
				Replaces: lo.ToPtr("1.0.0"),
			},
			wantErr: false,
		},
		{
			name: "invalid skips semver",
			version: CatalogItemVersion{
				Version:  "2.0.0",
				Tag:      lo.ToPtr("v2.0.0"),
				Channels: []string{"stable"},
				Skips:    &[]string{"not-semver"},
			},
			wantErr:     true,
			errContains: "skips",
		},
		{
			name: "valid skips",
			version: CatalogItemVersion{
				Version:  "2.0.0",
				Tag:      lo.ToPtr("v2.0.0"),
				Channels: []string{"stable"},
				Skips:    &[]string{"1.0.0", "1.5.0"},
			},
			wantErr: false,
		},
		{
			name: "invalid skipRange",
			version: CatalogItemVersion{
				Version:   "2.0.0",
				Tag:       lo.ToPtr("v2.0.0"),
				Channels:  []string{"stable"},
				SkipRange: lo.ToPtr(">=invalid"),
			},
			wantErr:     true,
			errContains: "skipRange",
		},
		{
			name: "valid skipRange",
			version: CatalogItemVersion{
				Version:   "2.0.0",
				Tag:       lo.ToPtr("v2.0.0"),
				Channels:  []string{"stable"},
				SkipRange: lo.ToPtr(">=1.0.0 <2.0.0"),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seenVersions := make(map[string]struct{})
			errs := validateCatalogItemVersion(tt.version, 0, seenVersions, CatalogItemCategoryApplication, CatalogItemTypeContainer)
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestCatalogItemArtifactValidation(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		ref         CatalogItemReference
		wantErr     bool
		errContains string
	}{
		{
			name: "valid single artifact without type",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Uri: "quay.io/example/app-qcow2"},
				},
			},
			wantErr: false,
		},
		{
			name: "valid single artifact with type",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Type: lo.ToPtr(CatalogItemArtifactTypeQcow2), Uri: "quay.io/example/app-qcow2"},
				},
			},
			wantErr: false,
		},
		{
			name: "multiple artifacts require type",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Uri: "quay.io/example/app-qcow2"},
					{Uri: "quay.io/example/app-iso"},
				},
			},
			wantErr:     true,
			errContains: "type is required when multiple",
		},
		{
			name: "multiple artifacts with types",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Type: lo.ToPtr(CatalogItemArtifactTypeQcow2), Uri: "quay.io/example/app-qcow2"},
					{Type: lo.ToPtr(CatalogItemArtifactTypeIso), Uri: "quay.io/example/app-iso"},
					{Type: lo.ToPtr(CatalogItemArtifactTypeAmi), Uri: "quay.io/example/app-ami"},
				},
			},
			wantErr: false,
		},
		{
			name: "duplicate types",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Type: lo.ToPtr(CatalogItemArtifactTypeQcow2), Uri: "quay.io/example/app-qcow2"},
					{Type: lo.ToPtr(CatalogItemArtifactTypeQcow2), Uri: "quay.io/example/app-qcow2-v2"},
				},
			},
			wantErr:     true,
			errContains: "duplicate type",
		},
		{
			name: "artifact missing uri",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Type: lo.ToPtr(CatalogItemArtifactTypeQcow2)},
				},
			},
			wantErr:     true,
			errContains: "uri is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateCatalogItemReference(&tt.ref)
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestValidateConfigSchemaExternalRefs(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		schema      map[string]any
		wantErr     bool
		errContains string
	}{
		{
			name: "valid simple schema",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
			wantErr: false,
		},
		{
			name: "external http ref forbidden",
			schema: map[string]any{
				"$ref": "http://evil.com/schema.json",
			},
			wantErr:     true,
			errContains: "external schema references are forbidden",
		},
		{
			name: "external https ref forbidden",
			schema: map[string]any{
				"$ref": "https://example.com/schema.json",
			},
			wantErr:     true,
			errContains: "external schema references are forbidden",
		},
		{
			name: "external file ref forbidden",
			schema: map[string]any{
				"$ref": "file:///etc/passwd",
			},
			wantErr:     true,
			errContains: "external schema references are forbidden",
		},
		{
			name: "local ref allowed",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"address": map[string]any{"$ref": "#/$defs/address"},
				},
				"$defs": map[string]any{
					"address": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"street": map[string]any{"type": "string"},
						},
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateConfigSchema(&tt.schema, "spec.configSchema")
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestValidateArtifactTypeValidation(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		ref         CatalogItemReference
		wantErr     bool
		errContains string
	}{
		{
			name: "valid artifact type container",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Type: lo.ToPtr(CatalogItemArtifactTypeContainer), Uri: "quay.io/example/app"},
				},
			},
			wantErr: false,
		},
		{
			name: "valid artifact type qcow2",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Type: lo.ToPtr(CatalogItemArtifactTypeQcow2), Uri: "quay.io/example/app-qcow2"},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid artifact type",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Type: lo.ToPtr(CatalogItemArtifactType("invalid-type")), Uri: "quay.io/example/app"},
				},
			},
			wantErr:     true,
			errContains: "invalid value",
		},
		{
			name: "unknown artifact type",
			ref: CatalogItemReference{
				Uri: "quay.io/example/app",
				Artifacts: &[]CatalogItemArtifact{
					{Type: lo.ToPtr(CatalogItemArtifactType("tar.gz")), Uri: "quay.io/example/app"},
				},
			},
			wantErr:     true,
			errContains: "invalid value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateCatalogItemReference(&tt.ref)
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestValidateContainerConfigVolumes(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		config      map[string]interface{}
		wantErr     bool
		errContains string
	}{
		{
			name: "valid volumes",
			config: map[string]interface{}{
				"volumes": []interface{}{
					map[string]interface{}{"name": "data-vol", "mountPath": "/data"},
				},
			},
			wantErr: false,
		},
		{
			name: "non-object volume entry",
			config: map[string]interface{}{
				"volumes": []interface{}{
					"not-an-object",
				},
			},
			wantErr:     true,
			errContains: "must be an object",
		},
		{
			name: "mixed valid and invalid volumes",
			config: map[string]interface{}{
				"volumes": []interface{}{
					map[string]interface{}{"name": "valid-vol"},
					123,
				},
			},
			wantErr:     true,
			errContains: "must be an object",
		},
		{
			name: "volume missing name",
			config: map[string]interface{}{
				"volumes": []interface{}{
					map[string]interface{}{"mountPath": "/data"},
				},
			},
			wantErr:     true,
			errContains: "name: required",
		},
		{
			name: "volumes not an array",
			config: map[string]interface{}{
				"volumes": "not-an-array",
			},
			wantErr:     true,
			errContains: "must be an array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateContainerConfig(tt.config, "spec.defaults.config")
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestValidateQuadletConfigVolumes(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		config      map[string]interface{}
		wantErr     bool
		errContains string
	}{
		{
			name: "valid volumes",
			config: map[string]interface{}{
				"volumes": []interface{}{
					map[string]interface{}{"name": "config-vol"},
				},
			},
			wantErr: false,
		},
		{
			name: "non-object volume entry",
			config: map[string]interface{}{
				"volumes": []interface{}{
					nil,
				},
			},
			wantErr:     true,
			errContains: "must be an object",
		},
		{
			name: "array as volume entry",
			config: map[string]interface{}{
				"volumes": []interface{}{
					[]interface{}{"a", "b"},
				},
			},
			wantErr:     true,
			errContains: "must be an object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateQuadletConfig(tt.config, "spec.defaults.config")
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestValidateVersionConfig(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		version     CatalogItemVersion
		category    CatalogItemCategory
		itemType    CatalogItemType
		wantErr     bool
		errContains string
	}{
		{
			name: "valid version with config",
			version: CatalogItemVersion{
				Version:  "1.0.0",
				Tag:      lo.ToPtr("v1.0.0"),
				Channels: []string{"stable"},
				Config: &map[string]interface{}{
					"envVars": map[string]interface{}{"LOG_LEVEL": "info"},
				},
			},
			category: CatalogItemCategoryApplication,
			itemType: CatalogItemTypeContainer,
			wantErr:  false,
		},
		{
			name: "version with invalid port in config",
			version: CatalogItemVersion{
				Version:  "1.0.0",
				Tag:      lo.ToPtr("v1.0.0"),
				Channels: []string{"stable"},
				Config: &map[string]interface{}{
					"ports": []interface{}{"invalid-port"},
				},
			},
			category:    CatalogItemCategoryApplication,
			itemType:    CatalogItemTypeContainer,
			wantErr:     true,
			errContains: "ports",
		},
		{
			name: "version with invalid configSchema",
			version: CatalogItemVersion{
				Version:  "1.0.0",
				Tag:      lo.ToPtr("v1.0.0"),
				Channels: []string{"stable"},
				ConfigSchema: &map[string]interface{}{
					"$ref": "http://evil.com/schema.json",
				},
			},
			category:    CatalogItemCategoryApplication,
			itemType:    CatalogItemTypeContainer,
			wantErr:     true,
			errContains: "external schema references are forbidden",
		},
		{
			name: "version with non-object volume",
			version: CatalogItemVersion{
				Version:  "1.0.0",
				Tag:      lo.ToPtr("v1.0.0"),
				Channels: []string{"stable"},
				Config: &map[string]interface{}{
					"volumes": []interface{}{"not-an-object"},
				},
			},
			category:    CatalogItemCategoryApplication,
			itemType:    CatalogItemTypeContainer,
			wantErr:     true,
			errContains: "must be an object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seenVersions := make(map[string]struct{})
			errs := validateCatalogItemVersion(tt.version, 0, seenVersions, tt.category, tt.itemType)
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestValidateReplacesGraph(t *testing.T) {
	require := require.New(t)

	v := func(version string, replaces string) CatalogItemVersion {
		var r *string
		if replaces != "" {
			r = lo.ToPtr(replaces)
		}
		return CatalogItemVersion{
			Version:  version,
			Tag:      lo.ToPtr("v" + version),
			Channels: []string{"stable"},
			Replaces: r,
		}
	}

	tests := []struct {
		name        string
		versions    []CatalogItemVersion
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid linear chain",
			versions: []CatalogItemVersion{v("3.0.0", "2.0.0"), v("2.0.0", "1.0.0"), v("1.0.0", "")},
			wantErr:  false,
		},
		{
			name:     "no replaces at all",
			versions: []CatalogItemVersion{v("1.0.0", ""), v("2.0.0", "")},
			wantErr:  false,
		},
		{
			name:        "self-referencing",
			versions:    []CatalogItemVersion{v("1.0.0", "1.0.0")},
			wantErr:     true,
			errContains: "cannot replace itself",
		},
		{
			name:     "replaces target not in versions list is accepted",
			versions: []CatalogItemVersion{v("2.0.0", "0.9.0"), v("1.0.0", "")},
			wantErr:  false,
		},
		{
			name:        "circular two versions",
			versions:    []CatalogItemVersion{v("1.0.0", "2.0.0"), v("2.0.0", "1.0.0")},
			wantErr:     true,
			errContains: "circular replaces chain",
		},
		{
			name: "circular three versions",
			versions: []CatalogItemVersion{
				v("1.0.0", "3.0.0"),
				v("2.0.0", "1.0.0"),
				v("3.0.0", "2.0.0"),
			},
			wantErr:     true,
			errContains: "circular replaces chain",
		},
		{
			name: "valid single replaces",
			versions: []CatalogItemVersion{
				v("2.0.0", "1.0.0"),
				v("1.0.0", ""),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateReplacesGraph(tt.versions)
			if tt.wantErr {
				require.NotEmpty(errs)
				found := false
				for _, e := range errs {
					if strings.Contains(e.Error(), tt.errContains) {
						found = true
						break
					}
				}
				require.True(found, "expected error containing %q, got %v", tt.errContains, errs)
			} else {
				require.Empty(errs)
			}
		})
	}
}
