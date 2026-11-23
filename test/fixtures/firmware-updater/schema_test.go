package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	catalog "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/stretchr/testify/require"
)

var schemaSlots = []string{"config", "observation", "request", "result", "checkpoint"}

const unknownProperty = "unexpectedField"

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readSchema(t *testing.T, slot string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("schemas", slot+".schema.json"))
	require.NoError(t, err)
	return data
}

func compileSchema(t *testing.T, data []byte) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external schema loading is disabled")
	}
	const url = "https://flightctl.invalid/fixture-schema.json"
	require.NoError(t, compiler.AddResource(url, bytes.NewReader(data)))
	schema, err := compiler.Compile(url)
	require.NoError(t, err)
	return schema
}

// decodeJSON keeps JSON numbers as json.Number for schema validation.
func decodeJSON(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	require.NoError(t, decoder.Decode(&value))
	return value
}

func roundTrip(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return decodeJSON(t, data)
}

func identityInstance() map[string]any {
	d := func(s string) string { return digestOf([]byte(s)) }
	return map[string]any{
		"protocol": 1, "action": "verify", "attemptID": "attempt-1", "callID": "2", "cycle": 1,
		"name": "updater-contract-fixture-amd64", "version": "0.1.0",
		"descriptorDigest": d("descriptor"), "packageDigest": d("package"), "manifestDigest": d("manifest"),
		"configDigest": d("config"), "intentDigest": d("intent"), "planDigest": d("plan"), "observationDigest": d("observation"),
		"schemas": map[string]any{
			"config": d("config schema"), "observation": d("observation schema"), "request": d("request schema"),
			"result": d("result schema"), "checkpoint": d("checkpoint schema"),
		},
		"grantDigest": d("grant"), "checkpointDigest": d("checkpoint"),
		"checkpointFormat": "updater-contract-fixture/v1", "checkpointRevision": 1,
	}
}

// validInstance returns a document for slot that contains every object shape
// that slot's schema defines.
func validInstance(slot string) map[string]any {
	digest := digestOf([]byte("system"))
	component := func() map[string]any {
		return map[string]any{"name": "system", "version": "2", "digest": digest, "active": true}
	}
	target := func() map[string]any {
		return map[string]any{"kind": "component", "name": "system", "version": "2", "digest": digest}
	}
	receipt := func() map[string]any {
		return map[string]any{"selectionDigest": digestOf([]byte("selection")), "evidence": map[string]any{"method": "synthetic"}}
	}
	switch slot {
	case "config":
		return map[string]any{"artifacts": []any{map[string]any{"name": "system", "version": "2", "digest": digest}}}
	case "observation":
		return map[string]any{"components": []any{component()}}
	case "checkpoint":
		return map[string]any{"step": 1}
	case "request":
		return map[string]any{
			"identity": identityInstance(), "config": "e30=", "observation": "e30=",
			"checkpoint": map[string]any{"format": "updater-contract-fixture/v1", "revision": 1, "bytes": "eyJzdGVwIjoxfQ==", "digest": digestOf([]byte(`{"step":1}`))},
		}
	case "result":
		observed := target()
		observed["active"] = true
		return map[string]any{
			"identity": identityInstance(), "kind": "Verified", "osReadiness": "Current",
			"observations": []any{observed},
			"evidence":     map[string]any{"components": []any{component()}},
			"prepared": map[string]any{
				"artifacts": []any{target()}, "selectionDigest": digestOf([]byte("selection")),
				"authentication": receipt(), "preflight": receipt(),
			},
		}
	}
	panic("unknown schema slot " + slot)
}

type pathToken struct {
	name  string
	index bool
}

func pointer(location []pathToken) string {
	var b strings.Builder
	for _, token := range location {
		b.WriteString("/" + token.name)
	}
	return b.String()
}

// schemaShape maps an instance location to the schema location that describes it.
func schemaShape(location []pathToken) string {
	var b strings.Builder
	for _, token := range location {
		if token.index {
			b.WriteString("/items")
		} else {
			b.WriteString("/properties/" + token.name)
		}
	}
	return b.String()
}

func objectLocations(value any, location []pathToken) [][]pathToken {
	var out [][]pathToken
	switch v := value.(type) {
	case map[string]any:
		out = append(out, slices.Clone(location))
		for _, key := range slices.Sorted(maps.Keys(v)) {
			out = append(out, objectLocations(v[key], append(slices.Clone(location), pathToken{name: key}))...)
		}
	case []any:
		for i, child := range v {
			out = append(out, objectLocations(child, append(slices.Clone(location), pathToken{name: strconv.Itoa(i), index: true}))...)
		}
	}
	return out
}

func schemaObjectShapes(node map[string]any, path string, out map[string]bool) {
	if node["type"] == "object" {
		out[path] = true
	}
	if properties, ok := node["properties"].(map[string]any); ok {
		for name, child := range properties {
			if schema, ok := child.(map[string]any); ok {
				schemaObjectShapes(schema, path+"/properties/"+name, out)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		schemaObjectShapes(items, path+"/items", out)
	}
}

func injectProperty(t *testing.T, document any, location []pathToken) any {
	t.Helper()
	copied := roundTrip(t, document)
	node := copied
	for _, token := range location {
		if token.index {
			index, err := strconv.Atoi(token.name)
			require.NoError(t, err)
			node = node.([]any)[index]
		} else {
			node = node.(map[string]any)[token.name]
		}
	}
	node.(map[string]any)[unknownProperty] = true
	return copied
}

func reportsUnknownProperty(err error, location string) bool {
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) {
		return false
	}
	var visit func(*jsonschema.ValidationError) bool
	visit = func(e *jsonschema.ValidationError) bool {
		if strings.TrimSuffix(e.InstanceLocation, "/") == location &&
			strings.HasSuffix(e.KeywordLocation, "/additionalProperties") &&
			strings.Contains(e.Message, "'"+unknownProperty+"'") {
			return true
		}
		return slices.ContainsFunc(e.Causes, visit)
	}
	return visit(validation)
}

func TestFixtureSchemasRejectUnknownProperties(t *testing.T) {
	for _, slot := range schemaSlots {
		t.Run(slot, func(t *testing.T) {
			data := readSchema(t, slot)
			schema := compileSchema(t, data)
			document := roundTrip(t, validInstance(slot))
			require.NoError(t, schema.Validate(document), "When the %s instance is valid it should pass", slot)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(data, &raw))
			declared := map[string]bool{}
			schemaObjectShapes(raw, "", declared)
			covered := map[string]bool{}
			locations := objectLocations(document, nil)
			for _, location := range locations {
				covered[schemaShape(location)] = true
			}
			require.Equal(t, declared, covered, "the valid instance must contain every object shape of the %s schema", slot)

			for _, location := range locations {
				where := pointer(location)
				t.Run("When an unknown property is added at "+strings.TrimPrefix(where+"/", "/")+" it should be rejected there", func(t *testing.T) {
					err := schema.Validate(injectProperty(t, document, location))
					require.Error(t, err)
					require.True(t, reportsUnknownProperty(err, where), "expected an unknown-property failure at %q, got %v", where, err)
				})
			}
		})
	}
}

func schemaDocuments(release *catalog.ReleaseBinding) map[string]*catalog.SchemaDocument {
	return map[string]*catalog.SchemaDocument{
		"config": &release.Schemas.Config, "observation": &release.Schemas.Observation, "request": &release.Schemas.Request,
		"result": &release.Schemas.Result, "checkpoint": &release.Schemas.Checkpoint,
	}
}

// fixtureCatalog returns an administrator catalog whose only release carries
// the fixture schemas, with slot replaced by override when override is set.
func fixtureCatalog(t *testing.T, slot string, override []byte) []byte {
	t.Helper()
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	release := &cfg.Releases[0]
	release.Name, release.Version = "updater-contract-fixture-amd64", "0.1.0"
	for name, doc := range schemaDocuments(release) {
		data := readSchema(t, name)
		if name == slot && override != nil {
			data = override
		}
		*doc = catalog.SchemaDocument{Digest: digestOf(data), Bytes: string(data)}
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	return data
}

func withReferenceProbe(t *testing.T, schema []byte, keyword, reference string) []byte {
	t.Helper()
	var document map[string]any
	require.NoError(t, json.Unmarshal(schema, &document))
	properties, ok := document["properties"].(map[string]any)
	require.True(t, ok)
	properties["referenceProbe"] = map[string]any{keyword: reference}
	data, err := json.MarshalIndent(document, "", "  ")
	require.NoError(t, err)
	return append(data, '\n')
}

func TestFixtureSchemasPassCatalogReferencePolicy(t *testing.T) {
	_, err := catalog.ParseAdministratorConfig(fixtureCatalog(t, "", nil))
	require.NoError(t, err, "When every fixture schema is unmodified the catalog should accept the release")

	external := []struct {
		name      string
		keyword   string
		reference string
	}{
		{"an HTTPS $ref", "$ref", "https://schemas.flightctl.invalid/external.schema.json"},
		{"a relative-file $ref", "$ref", "external.schema.json"},
		{"an HTTPS $dynamicRef", "$dynamicRef", "https://schemas.flightctl.invalid/external.schema.json"},
		{"a relative-file $dynamicRef", "$dynamicRef", "external.schema.json"},
		{"an HTTPS $recursiveRef", "$recursiveRef", "https://schemas.flightctl.invalid/external.schema.json"},
		{"a relative-file $recursiveRef", "$recursiveRef", "external.schema.json"},
	}
	for _, slot := range schemaSlots {
		t.Run(slot, func(t *testing.T) {
			local := withReferenceProbe(t, readSchema(t, slot), "$ref", "#")
			_, err := catalog.ParseAdministratorConfig(fixtureCatalog(t, slot, local))
			require.NoError(t, err, "When the probe location holds a same-document reference the catalog should accept it")

			for _, tc := range external {
				t.Run("When the schema adds "+tc.name+" it should be rejected by the reference policy", func(t *testing.T) {
					probe := withReferenceProbe(t, readSchema(t, slot), tc.keyword, tc.reference)
					_, err := catalog.ParseAdministratorConfig(fixtureCatalog(t, slot, probe))
					require.ErrorContains(t, err, "only local schema references are allowed")
				})
			}
		})
	}
}
