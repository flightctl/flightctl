package v1beta1

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// firmwareClosedObjectPaths lists every fixed firmware object. Each one must
// reject fields that the API schema does not define.
var firmwareClosedObjectPaths = []string{
	"spec.firmware",
	"spec.firmware.components[]",
	"spec.firmware.components[].version",
	"spec.firmware.overlays[]",
	"spec.firmware.overlays[].compatibleFirmware[]",
	"spec.firmware.updater",
	"status.firmware",
	"status.firmware.attempt",
	"status.firmware.attempt.error",
	"status.firmware.attempt.recovery[]",
	"status.firmware.attempt.updater",
	"status.firmware.components[]",
	"status.firmware.components[].actual",
	"status.firmware.components[].error",
	"status.firmware.components[].observation",
	"status.firmware.components[].observation.backend",
	"status.firmware.components[].selected",
	"status.firmware.components[].selected.source",
	"status.firmware.support",
	"status.firmware.support.updaters[]",
}

// firmwareOpenObjectPaths lists the firmware objects whose content is defined
// by the selected updater release rather than by the API schema. They keep
// every field so the service can validate them against that release's schema.
var firmwareOpenObjectPaths = []string{
	"spec.firmware.updater.config",
	"status.firmware.components[].observation.backend.data",
}

func firmwareRootSchemas(t *testing.T) map[string]*openapi3.Schema {
	t.Helper()
	doc, err := GetSpec()
	require.NoError(t, err)

	roots := map[string]*openapi3.Schema{}
	for root, owner := range map[string]string{"spec.firmware": "DeviceSpec", "status.firmware": "DeviceStatus"} {
		ownerSchema, ok := doc.Components.Schemas[owner]
		require.True(t, ok, "missing schema %s", owner)
		firmware, ok := ownerSchema.Value.Properties["firmware"]
		require.True(t, ok, "%s must define a firmware property", owner)
		roots[root] = firmware.Value
	}
	return roots
}

func splitFirmwarePath(path string) []string {
	var segments []string
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		name, isArray := strings.CutSuffix(part, "[]")
		if name != "" {
			segments = append(segments, name)
		}
		if isArray {
			segments = append(segments, "[]")
		}
	}
	return segments
}

func firmwareSchemaAt(t *testing.T, roots map[string]*openapi3.Schema, path string) *openapi3.Schema {
	t.Helper()
	for root, schema := range roots {
		relative, ok := strings.CutPrefix(path, root)
		if !ok {
			continue
		}
		current := schema
		for _, segment := range splitFirmwarePath(relative) {
			if segment == "[]" {
				require.NotNil(t, current.Items, "%s: expected an array", path)
				current = current.Items.Value
				continue
			}
			property, ok := current.Properties[segment]
			require.True(t, ok, "%s: missing property %q", path, segment)
			current = property.Value
		}
		return current
	}
	t.Fatalf("%s: unknown firmware root", path)
	return nil
}

func isObjectSchema(s *openapi3.Schema) bool {
	return s.Type.Is(openapi3.TypeObject) || len(s.Properties) > 0
}

// collectFirmwareObjects records every object schema reachable from schema,
// keyed by its JSON path, descending through properties and array items.
func collectFirmwareObjects(path string, schema *openapi3.Schema, objects map[string]*openapi3.Schema) {
	if schema.Type.Is(openapi3.TypeArray) && schema.Items != nil {
		collectFirmwareObjects(path+"[]", schema.Items.Value, objects)
		return
	}
	if !isObjectSchema(schema) {
		return
	}
	objects[path] = schema
	for name, property := range schema.Properties {
		collectFirmwareObjects(path+"."+name, property.Value, objects)
	}
}

func allFirmwareObjects(t *testing.T) map[string]*openapi3.Schema {
	t.Helper()
	objects := map[string]*openapi3.Schema{}
	for root, schema := range firmwareRootSchemas(t) {
		collectFirmwareObjects(root, schema, objects)
	}
	return objects
}

func isClosedObject(s *openapi3.Schema) bool {
	return s.AdditionalProperties.Has != nil && !*s.AdditionalProperties.Has
}

func TestFirmwareSchemasCloseEveryFixedObject(t *testing.T) {
	var closed, open []string
	for path, schema := range allFirmwareObjects(t) {
		if isClosedObject(schema) {
			closed = append(closed, path)
		} else {
			open = append(open, path)
		}
	}
	sort.Strings(closed)
	sort.Strings(open)

	assert.Equal(t, firmwareClosedObjectPaths, closed, "every fixed firmware object must set additionalProperties: false")
	assert.Equal(t, firmwareOpenObjectPaths, open, "only updater-defined firmware payloads may accept fields the API schema does not list")
}

func TestFirmwareSchemasValidateUnknownFieldsByObject(t *testing.T) {
	documents := map[string]string{
		"spec.firmware":   testFirmwareSpecJSON,
		"status.firmware": testFirmwareStatusJSON,
	}
	roots := firmwareRootSchemas(t)
	for root, document := range documents {
		require.NoError(t, roots[root].VisitJSON(decodeFirmwareJSON(t, document)), "the complete %s fixture must satisfy the schema", root)
	}

	objects := allFirmwareObjects(t)
	paths := lo.Keys(objects)
	sort.Strings(paths)
	for _, path := range paths {
		root := "spec.firmware"
		if strings.HasPrefix(path, "status.firmware") {
			root = "status.firmware"
		}
		retained := lo.Contains(firmwareOpenObjectPaths, path)
		name := "When an unknown field is added at " + path + " it should be rejected"
		if retained {
			name = "When an unknown field is added at " + path + " it should be retained for release schema validation"
		}
		t.Run(name, func(t *testing.T) {
			document := decodeFirmwareJSON(t, documents[root])
			target := firmwareJSONObjectAt(t, document, strings.TrimPrefix(path, root))
			target["unexpectedField"] = "value"

			err := roots[root].VisitJSON(document)
			if retained {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unexpectedField")
		})
	}
}

func TestFirmwareSchemaFieldConstraints(t *testing.T) {
	roots := firmwareRootSchemas(t)
	componentNamePattern := `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	digestPattern := `^sha256:[a-f0-9]{64}$`

	tests := []struct {
		path      string
		typ       string
		format    string
		required  []string
		enum      []string
		pattern   string
		minLength uint64
		minItems  uint64
		minimum   *float64
		def       any
	}{
		{path: "spec.firmware", typ: openapi3.TypeObject, required: []string{"profile", "updater", "components", "overlays"}},
		{path: "spec.firmware.retryGeneration", typ: openapi3.TypeInteger, format: "int64", minimum: lo.ToPtr(0.0), def: 0.0},
		{path: "spec.firmware.profile", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.updater", typ: openapi3.TypeObject, required: []string{"name", "version", "config"}},
		{path: "spec.firmware.updater.name", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.updater.version", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.updater.config", typ: openapi3.TypeObject},
		{path: "spec.firmware.components", typ: openapi3.TypeArray, minItems: 1},
		{path: "spec.firmware.components[]", typ: openapi3.TypeObject, required: []string{"name", "version"}},
		{path: "spec.firmware.components[].name", typ: openapi3.TypeString, pattern: componentNamePattern},
		{path: "spec.firmware.components[].artifactDigest", typ: openapi3.TypeString, pattern: digestPattern},
		{path: "spec.firmware.components[].version", typ: openapi3.TypeObject},
		{path: "spec.firmware.components[].version.exact", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.components[].version.minimum", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.components[].version.maximum", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.components[].version.latest", typ: openapi3.TypeBoolean, def: false},
		{path: "spec.firmware.overlays", typ: openapi3.TypeArray},
		{path: "spec.firmware.overlays[]", typ: openapi3.TypeObject, required: []string{"name", "compatibleFirmware"}},
		{path: "spec.firmware.overlays[].name", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.overlays[].compatibleFirmware", typ: openapi3.TypeArray, minItems: 1},
		{path: "spec.firmware.overlays[].compatibleFirmware[]", typ: openapi3.TypeObject, required: []string{"component"}},
		{path: "spec.firmware.overlays[].compatibleFirmware[].component", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.overlays[].compatibleFirmware[].exact", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.overlays[].compatibleFirmware[].minimum", typ: openapi3.TypeString, minLength: 1},
		{path: "spec.firmware.overlays[].compatibleFirmware[].maximum", typ: openapi3.TypeString, minLength: 1},
		{path: "status.firmware", typ: openapi3.TypeObject, required: []string{"support", "components", "observedAt", "bootID"}},
		{path: "status.firmware.observedAt", typ: openapi3.TypeString, format: "date-time"},
		{path: "status.firmware.bootID", typ: openapi3.TypeString, minLength: 1},
		{path: "status.firmware.support", typ: openapi3.TypeObject, required: []string{"apiVersion", "supported", "updaters"}},
		{path: "status.firmware.support.apiVersion", typ: openapi3.TypeInteger, minimum: lo.ToPtr(1.0)},
		{path: "status.firmware.support.supported", typ: openapi3.TypeBoolean},
		{path: "status.firmware.support.updaters[]", typ: openapi3.TypeObject, required: []string{"name", "version", "buildDigest", "protocol", "schemaDigest"}},
		{path: "status.firmware.support.updaters[].protocol", typ: openapi3.TypeInteger},
		{path: "status.firmware.support.updaters[].cached", typ: openapi3.TypeBoolean},
		{path: "status.firmware.attempt", typ: openapi3.TypeObject, required: []string{"id", "renderedVersion", "specHash", "retryGeneration", "phase", "startedAt", "updatedAt", "changed", "recovery"}},
		{path: "status.firmware.attempt.retryGeneration", typ: openapi3.TypeInteger, format: "int64", minimum: lo.ToPtr(0.0)},
		{path: "status.firmware.attempt.phase", typ: openapi3.TypeString, enum: []string{"Preparing", "WaitingForPolicy", "Staging", "AwaitingReboot", "Verifying", "Succeeded", "Failed", "Canceled"}},
		{path: "status.firmware.attempt.startedAt", typ: openapi3.TypeString, format: "date-time"},
		{path: "status.firmware.attempt.updatedAt", typ: openapi3.TypeString, format: "date-time"},
		{path: "status.firmware.attempt.changed", typ: openapi3.TypeBoolean},
		{path: "status.firmware.attempt.cancellationReason", typ: openapi3.TypeString, enum: []string{"Superseded"}},
		{path: "status.firmware.attempt.updater", typ: openapi3.TypeObject, required: []string{"name", "version", "buildDigest", "protocol", "schemaDigest", "configDigest"}},
		{path: "status.firmware.attempt.updater.manifestDigest", typ: openapi3.TypeString},
		{path: "status.firmware.attempt.recovery[]", typ: openapi3.TypeObject, required: []string{"code", "message", "requiresPhysicalAccess"}},
		{path: "status.firmware.attempt.recovery[].documentationURL", typ: openapi3.TypeString, format: "uri"},
		{path: "status.firmware.attempt.error", typ: openapi3.TypeObject, required: []string{"reason", "message", "retryable"}},
		{path: "status.firmware.attempt.error.exceededBudget", typ: openapi3.TypeString},
		{path: "status.firmware.attempt.error.phase", typ: openapi3.TypeString, enum: []string{"Preparing", "WaitingForPolicy", "Staging", "AwaitingReboot", "Verifying", "Succeeded", "Failed", "Canceled"}},
		{path: "status.firmware.components[]", typ: openapi3.TypeObject, required: []string{"name", "type", "managed", "state", "observedAt", "bootID"}},
		{path: "status.firmware.components[].type", typ: openapi3.TypeString, enum: []string{"firmware", "overlay"}},
		{path: "status.firmware.components[].state", typ: openapi3.TypeString, enum: []string{"Unknown", "Pending", "Active", "Failed"}},
		{path: "status.firmware.components[].selected", typ: openapi3.TypeObject, required: []string{"source"}},
		{path: "status.firmware.components[].selected.source", typ: openapi3.TypeObject, required: []string{"kind", "identity", "digest"}},
		{path: "status.firmware.components[].actual", typ: openapi3.TypeObject},
		{path: "status.firmware.components[].actual.active", typ: openapi3.TypeBoolean},
		{path: "status.firmware.components[].observation", typ: openapi3.TypeObject},
		{path: "status.firmware.components[].observation.backend", typ: openapi3.TypeObject, required: []string{"updater", "schemaDigest", "data"}},
		{path: "status.firmware.components[].observation.backend.data", typ: openapi3.TypeObject},
		{path: "status.firmware.components[].error", typ: openapi3.TypeObject, required: []string{"reason", "message", "retryable"}},
	}

	for _, tt := range tests {
		t.Run("When reading "+tt.path+" it should match the firmware contract", func(t *testing.T) {
			schema := firmwareSchemaAt(t, roots, tt.path)
			assert.True(t, schema.Type.Is(tt.typ), "type: got %v", schema.Type)
			assert.Equal(t, tt.format, schema.Format, "format")
			assert.ElementsMatch(t, tt.required, schema.Required, "required")
			assert.ElementsMatch(t, tt.enum, lo.Map(schema.Enum, func(v any, _ int) string { s, _ := v.(string); return s }), "enum")
			assert.Equal(t, tt.pattern, schema.Pattern, "pattern")
			assert.Equal(t, tt.minLength, schema.MinLength, "minLength")
			assert.Equal(t, tt.minItems, schema.MinItems, "minItems")
			assert.Equal(t, tt.minimum, schema.Min, "minimum")
			assert.Equal(t, tt.def, schema.Default, "default")
		})
	}
}

func TestFirmwareSchemasDescribeEveryField(t *testing.T) {
	for path, schema := range allFirmwareObjects(t) {
		assert.NotEmpty(t, schema.Description, "%s needs a description", path)
		for name, property := range schema.Properties {
			assert.NotEmpty(t, property.Value.Description, "%s.%s needs a description", path, name)
		}
	}
}

func TestFirmwareIsOptionalOnDeviceSpecAndStatus(t *testing.T) {
	doc, err := GetSpec()
	require.NoError(t, err)

	deviceSpec := doc.Components.Schemas["DeviceSpec"].Value
	assert.NotContains(t, deviceSpec.Required, "firmware", "absent firmware must stay valid so existing manifests keep working")
	assert.Nil(t, deviceSpec.AdditionalProperties.Has, "DeviceSpec stays open because other schemas compose it")

	deviceStatus := doc.Components.Schemas["DeviceStatus"].Value
	assert.NotContains(t, deviceStatus.Required, "firmware", "status reported by agents without firmware support must stay valid")
}

func decodeFirmwareJSON(t *testing.T, document string) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(document), &decoded))
	return decoded
}

// firmwareJSONObjectAt returns the JSON object at path, taking the first
// element of every array on the way.
func firmwareJSONObjectAt(t *testing.T, document map[string]any, path string) map[string]any {
	t.Helper()
	var current any = document
	for _, segment := range splitFirmwarePath(path) {
		if segment == "[]" {
			items, ok := current.([]any)
			require.True(t, ok && len(items) > 0, "%s: the fixture needs a non-empty array", path)
			current = items[0]
			continue
		}
		object, ok := current.(map[string]any)
		require.True(t, ok, "%s: expected an object before %q", path, segment)
		current, ok = object[segment]
		require.True(t, ok, "%s: the fixture needs %q", path, segment)
	}
	object, ok := current.(map[string]any)
	require.True(t, ok, "%s: expected an object", path)
	return object
}
