package firmware_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/stretchr/testify/require"
)

const canonicalManifest = `{
  "apiVersion": "updater-manifest/v1",
  "name": "vendor-uefi",
  "version": "1.4.2",
  "protocol": 1,
  "entrypoint": "payload/bin/updater",
  "dependencies": [],
  "schemas": {
    "config": "sha256:30f8846d7c81099cd8df2bc46ff49e8773141872fd5f3b945c0daff540c18aff",
    "observation": "sha256:c27ee3be6dbff3e2ea17796af0cc2c87e5efb40d177ca205ee2929923f54669e",
    "request": "sha256:1c5318c38d3cc8e1a6805576610151adf4cd8146444942e885a9daa3adda179a",
    "result": "sha256:d43b7a6fa394712f500bd5f8ff4488f0943fcad506d1efa5eb332921538db563",
    "checkpoint": "sha256:5382edc26276a0bedb506412928054659ef9d95c05f9d9083afe1cd916c1afac"
  },
  "files": [
    {
      "path": "payload/bin/updater",
      "sha256": "905820073c735d475b79b03ae8617e4a495027dba3c83f5df6fe6536a3d1ed8b",
      "size": 23,
      "executable": true
    },
    {
      "path": "payload/share/vendor/README",
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "size": 0,
      "executable": false
    }
  ]
}`

var schemaKeys = []string{"config", "observation", "request", "result", "checkpoint"}

func schemaDocument(key string) policy.SchemaDocument {
	data := []byte(fmt.Sprintf(`{"title":"%s","type":"object"}`, key))
	return policy.SchemaDocument{Digest: policy.Hash(data), Bytes: string(data)}
}

// exampleDescriptor is the release the canonical manifest binds to.
func exampleDescriptor() *policy.ReleaseDescriptor {
	return &policy.ReleaseDescriptor{
		Name:     "vendor-uefi",
		Version:  "1.4.2",
		Protocol: 1,
		Schemas: policy.SchemaBundle{
			Config:      schemaDocument("config"),
			Observation: schemaDocument("observation"),
			Request:     schemaDocument("request"),
			Result:      schemaDocument("result"),
			Checkpoint:  schemaDocument("checkpoint"),
		},
		Dependencies: nil,
	}
}

func manifestMap(t *testing.T) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(canonicalManifest))
	dec.UseNumber()
	var m map[string]any
	require.NoError(t, dec.Decode(&m))
	return m
}

func encode(t *testing.T, m map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return data
}

func mutate(edit func(m map[string]any)) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		m := manifestMap(t)
		edit(m)
		return encode(t, m)
	}
}

func replaceText(old, replacement string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		require.Contains(t, canonicalManifest, old)
		return []byte(strings.Replace(canonicalManifest, old, replacement, 1))
	}
}

func files(m map[string]any) []any                { return m["files"].([]any) }
func file(m map[string]any, i int) map[string]any { return files(m)[i].(map[string]any) }
func schemas(m map[string]any) map[string]any     { return m["schemas"].(map[string]any) }

func TestParseManifestAcceptsTheCanonicalExample(t *testing.T) {
	require := require.New(t)
	m, err := firmware.ParseManifest([]byte(canonicalManifest))
	require.NoError(err)
	require.Equal(&firmware.Manifest{
		APIVersion:   firmware.ManifestAPIVersion,
		Name:         "vendor-uefi",
		Version:      "1.4.2",
		Protocol:     1,
		Entrypoint:   "payload/bin/updater",
		Dependencies: []policy.RuntimeDependency{},
		Schemas: firmware.ManifestSchemas{
			Config:      "sha256:30f8846d7c81099cd8df2bc46ff49e8773141872fd5f3b945c0daff540c18aff",
			Observation: "sha256:c27ee3be6dbff3e2ea17796af0cc2c87e5efb40d177ca205ee2929923f54669e",
			Request:     "sha256:1c5318c38d3cc8e1a6805576610151adf4cd8146444942e885a9daa3adda179a",
			Result:      "sha256:d43b7a6fa394712f500bd5f8ff4488f0943fcad506d1efa5eb332921538db563",
			Checkpoint:  "sha256:5382edc26276a0bedb506412928054659ef9d95c05f9d9083afe1cd916c1afac",
		},
		Files: []firmware.ManifestFile{
			{Path: "payload/bin/updater", SHA256: "905820073c735d475b79b03ae8617e4a495027dba3c83f5df6fe6536a3d1ed8b", Size: 23, Executable: true},
			{Path: "payload/share/vendor/README", SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Size: 0, Executable: false},
		},
	}, m)
	require.NoError(firmware.VerifyManifestBinding(m, exampleDescriptor()))
}

func TestParseManifestAcceptsValidVariants(t *testing.T) {
	testCases := []struct {
		name     string
		manifest func(t *testing.T) []byte
	}{
		{"When one file path is only a string prefix of another it should accept", mutate(func(m map[string]any) {
			m["entrypoint"] = "payload/bin"
			file(m, 0)["path"] = "payload/bin"
			file(m, 1)["path"] = "payload/binary"
		})},
		{"When a file size is zero it should accept", mutate(func(m map[string]any) { file(m, 0)["size"] = json.Number("0") })},
		{"When a file size is the largest int64 it should accept", mutate(func(m map[string]any) {
			file(m, 0)["size"] = json.Number("9223372036854775807")
		})},
		{"When keys are reordered and the encoding is compact it should accept", func(t *testing.T) []byte {
			data := encode(t, manifestMap(t))
			require.NotEqual(t, canonicalManifest, string(data))
			require.NotContains(t, string(data), "\n")
			return data
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := firmware.ParseManifest(tc.manifest(t))
			require.NoError(t, err)
			require.NotNil(t, m)
		})
	}
}

func TestParseManifestRejectsContractViolations(t *testing.T) {
	hex64 := strings.Repeat("a", 64)
	topLevel := []string{"apiVersion", "name", "version", "protocol", "entrypoint", "dependencies", "schemas", "files"}
	fileFields := []string{"path", "sha256", "size", "executable"}

	type rejection struct {
		name     string
		manifest func(t *testing.T) []byte
	}
	raw := func(s string) func(t *testing.T) []byte { return func(t *testing.T) []byte { return []byte(s) } }
	cases := []rejection{
		{"When the top level is an array it should reject", raw(`[]`)},
		{"When the top level is null it should reject", raw(`null`)},
		{"When a value follows the manifest it should reject", raw(canonicalManifest + ` {}`)},
		{"When bytes follow the manifest it should reject", raw(canonicalManifest + ` x`)},
		{"When the manifest is empty it should reject", raw(``)},
		{"When an object key is not a string it should reject", raw(`{"apiVersion": "updater-manifest/v1", 5: 1}`)},
		{"When the manifest object is truncated it should reject", raw(`{"apiVersion": "updater-manifest/v1"`)},
		{"When a manifest array is truncated it should reject", raw(`{"files": [{"path": "payload/bin/updater"}`)},
		{"When nesting exceeds the depth bound it should reject", replaceText(`"dependencies": []`, `"dependencies": `+strings.Repeat("[", 40)+strings.Repeat("]", 40))},
		{"When the manifest exceeds the read bound it should reject", raw(canonicalManifest + strings.Repeat(" ", int(firmware.MaxManifestBytes)))},
		{"When the top level has an unknown key it should reject", mutate(func(m map[string]any) { m["extra"] = json.Number("1") })},
		{"When schemas has an unknown key it should reject", mutate(func(m map[string]any) { schemas(m)["extra"] = "sha256:" + hex64 })},
		{"When a file entry has an unknown key it should reject", mutate(func(m map[string]any) { file(m, 0)["mode"] = json.Number("493") })},
		{"When a key differs only by case it should reject", replaceText(`"name": "vendor-uefi"`, `"Name": "vendor-uefi"`)},
		{"When a top-level key is duplicated it should reject", replaceText(`"name": "vendor-uefi",`, `"name": "vendor-uefi", "name": "vendor-uefi",`)},
		{"When a schemas key is duplicated it should reject", replaceText(`"config": "sha256:30f8`, `"config": "sha256:`+hex64+`", "config": "sha256:30f8`)},
		{"When a file entry key is duplicated it should reject", replaceText(`"size": 23,`, `"size": 23, "size": 23,`)},
		{"When apiVersion is not updater-manifest/v1 it should reject", mutate(func(m map[string]any) { m["apiVersion"] = "updater-manifest/v2" })},
		{"When the protocol is unsupported it should reject", mutate(func(m map[string]any) { m["protocol"] = json.Number("2") })},
		{"When the protocol is a fraction it should reject", replaceText(`"protocol": 1,`, `"protocol": 1.0,`)},
		{"When the protocol is a string it should reject", mutate(func(m map[string]any) { m["protocol"] = "1" })},
		{"When a dependency is declared it should reject", mutate(func(m map[string]any) {
			m["dependencies"] = []any{map[string]any{"name": "fwupd", "constraint": ">=1.9"}}
		})},
		{"When dependencies is an object it should reject", mutate(func(m map[string]any) { m["dependencies"] = map[string]any{} })},
		{"When no files are listed it should reject", mutate(func(m map[string]any) { m["files"] = []any{} })},
		{"When a file digest carries a sha256 prefix it should reject", mutate(func(m map[string]any) { file(m, 1)["sha256"] = "sha256:" + hex64 })},
		{"When a file size is negative it should reject", mutate(func(m map[string]any) { file(m, 1)["size"] = json.Number("-1") })},
		{"When a file size is negative zero it should reject", mutate(func(m map[string]any) { file(m, 1)["size"] = json.Number("-0") })},
		{"When a file size is a fraction it should reject", mutate(func(m map[string]any) { file(m, 1)["size"] = json.Number("1.5") })},
		{"When a file size uses an exponent it should reject", mutate(func(m map[string]any) { file(m, 1)["size"] = json.Number("1e3") })},
		{"When a file size overflows int64 it should reject", mutate(func(m map[string]any) {
			file(m, 1)["size"] = json.Number("9223372036854775808")
		})},
		{"When a file size is a string it should reject", mutate(func(m map[string]any) { file(m, 1)["size"] = "23" })},
		{"When executable is a string it should reject", mutate(func(m map[string]any) { file(m, 1)["executable"] = "true" })},
		{"When a file path is absolute it should reject", mutate(func(m map[string]any) { file(m, 1)["path"] = "/updater/payload/share/vendor/README" })},
		{"When a file path traverses a parent it should reject", mutate(func(m map[string]any) { file(m, 1)["path"] = "payload/../share/vendor/README" })},
		{"When a file path is listed twice it should reject", mutate(func(m map[string]any) { m["files"] = append([]any{file(m, 0)}, files(m)...) })},
		{"When a file path is a directory prefix of another it should reject", mutate(func(m map[string]any) {
			m["files"] = append(files(m), map[string]any{"path": "payload/bin", "sha256": hex64, "size": json.Number("1"), "executable": false})
		})},
		{"When an unrelated path sorts between a prefix and descendant it should still reject the prefix conflict", mutate(func(m map[string]any) {
			m["files"] = append(files(m),
				map[string]any{"path": "payload/binx", "sha256": hex64, "size": json.Number("1"), "executable": false},
				map[string]any{"path": "payload/bin", "sha256": hex64, "size": json.Number("1"), "executable": false},
			)
		})},
		{"When the entrypoint is not listed it should reject", mutate(func(m map[string]any) { m["entrypoint"] = "payload/bin/other" })},
		{"When the entrypoint is not executable it should reject", mutate(func(m map[string]any) { file(m, 0)["executable"] = false })},
		{"When the entrypoint is not a payload path it should reject", mutate(func(m map[string]any) { m["entrypoint"] = "/updater/payload/bin/updater" })},
	}
	for _, key := range topLevel {
		cases = append(cases,
			rejection{fmt.Sprintf("When %s is missing it should reject", key), mutate(func(m map[string]any) { delete(m, key) })},
			rejection{fmt.Sprintf("When %s is null it should reject", key), mutate(func(m map[string]any) { m[key] = nil })},
		)
	}
	for _, key := range fileFields {
		cases = append(cases,
			rejection{fmt.Sprintf("When files[].%s is missing it should reject", key), mutate(func(m map[string]any) { delete(file(m, 1), key) })},
			rejection{fmt.Sprintf("When files[].%s is null it should reject", key), mutate(func(m map[string]any) { file(m, 1)[key] = nil })},
		)
	}
	for _, key := range schemaKeys {
		cases = append(cases,
			rejection{fmt.Sprintf("When schemas.%s is missing it should reject", key), mutate(func(m map[string]any) { delete(schemas(m), key) })},
			rejection{fmt.Sprintf("When schemas.%s is null it should reject", key), mutate(func(m map[string]any) { schemas(m)[key] = nil })},
		)
	}
	for _, digest := range []string{"sha256:" + strings.ToUpper(hex64), hex64, "sha256:" + hex64[:63], "sha512:" + hex64} {
		cases = append(cases, rejection{fmt.Sprintf("When schema digest %q is malformed it should reject", digest), mutate(func(m map[string]any) { schemas(m)["config"] = digest })})
	}
	for _, digest := range []string{strings.ToUpper(hex64), hex64[:63], hex64[:63] + "g"} {
		cases = append(cases, rejection{fmt.Sprintf("When file digest %q is malformed it should reject", digest), mutate(func(m map[string]any) { file(m, 1)["sha256"] = digest })})
	}
	for _, p := range []string{"./payload/share/vendor/README", "payload/./share/vendor/README", "payload//share/vendor/README", "payload/share/vendor/README/"} {
		cases = append(cases, rejection{fmt.Sprintf("When file path %q is not clean it should reject", p), mutate(func(m map[string]any) { file(m, 1)["path"] = p })})
	}
	for _, p := range []string{"payload", "payload/", "bin/updater", "payloadx/bin/updater", "manifest.json"} {
		cases = append(cases, rejection{fmt.Sprintf("When file path %q is not strictly beneath payload it should reject", p), mutate(func(m map[string]any) { file(m, 1)["path"] = p })})
	}
	for _, p := range []string{"", "payload/share/vendor/READ\x00ME"} {
		cases = append(cases, rejection{fmt.Sprintf("When file path %q is empty or contains NUL it should reject", p), mutate(func(m map[string]any) { file(m, 1)["path"] = p })})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			m, err := firmware.ParseManifest(tc.manifest(t))
			require.Error(err)
			require.Nil(m)
		})
	}
}

func TestVerifyManifestBinding(t *testing.T) {
	other := "sha256:" + strings.Repeat("b", 64)
	testCases := []struct {
		name       string
		manifest   func(m *firmware.Manifest)
		descriptor func(d *policy.ReleaseDescriptor)
	}{
		{name: "When the name differs it should reject", manifest: func(m *firmware.Manifest) { m.Name = "vendor-other" }},
		{name: "When the version differs it should reject", manifest: func(m *firmware.Manifest) { m.Version = "1.4.3" }},
		{name: "When the descriptor protocol differs it should reject", descriptor: func(d *policy.ReleaseDescriptor) { d.Protocol = 2 }},
		{name: "When the config schema differs it should reject", manifest: func(m *firmware.Manifest) { m.Schemas.Config = other }},
		{name: "When the observation schema differs it should reject", manifest: func(m *firmware.Manifest) { m.Schemas.Observation = other }},
		{name: "When the request schema differs it should reject", manifest: func(m *firmware.Manifest) { m.Schemas.Request = other }},
		{name: "When the result schema differs it should reject", manifest: func(m *firmware.Manifest) { m.Schemas.Result = other }},
		{name: "When the checkpoint schema differs it should reject", manifest: func(m *firmware.Manifest) { m.Schemas.Checkpoint = other }},
		{name: "When the descriptor declares a dependency it should reject", descriptor: func(d *policy.ReleaseDescriptor) {
			d.Dependencies = []policy.RuntimeDependency{{Name: "fwupd", Constraint: ">=1.9"}}
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			m, err := firmware.ParseManifest([]byte(canonicalManifest))
			require.NoError(err)
			d := exampleDescriptor()
			if tc.manifest != nil {
				tc.manifest(m)
			}
			if tc.descriptor != nil {
				tc.descriptor(d)
			}
			require.Error(firmware.VerifyManifestBinding(m, d))
		})
	}
}
