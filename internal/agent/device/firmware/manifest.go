package firmware

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	// ManifestPath is the image-relative location of the updater manifest.
	ManifestPath = "updater/manifest.json"
	// ManifestAPIVersion is the only manifest version this agent accepts.
	ManifestAPIVersion = "updater-manifest/v1"
	// MaxManifestBytes bounds the manifest read from an image.
	MaxManifestBytes int64 = 1 << 20
	// SupportedProtocol is the only updater workflow protocol this agent accepts.
	SupportedProtocol = 1

	maxManifestDepth = 16
)

const manifestSchemaURL = "https://flightctl.invalid/updater-manifest-v1.json"

const manifestSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "apiVersion",
    "name",
    "version",
    "protocol",
    "entrypoint",
    "dependencies",
    "schemas",
    "files"
  ],
  "properties": {
    "apiVersion": {"type": "string"},
    "name": {"type": "string"},
    "version": {"type": "string"},
    "protocol": {"type": "integer"},
    "entrypoint": {"type": "string"},
    "dependencies": {"type": "array"},
    "schemas": {
      "type": "object",
      "additionalProperties": false,
      "required": ["config", "observation", "request", "result", "checkpoint"],
      "properties": {
        "config": {"type": "string"},
        "observation": {"type": "string"},
        "request": {"type": "string"},
        "result": {"type": "string"},
        "checkpoint": {"type": "string"}
      }
    },
    "files": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["path", "sha256", "size", "executable"],
        "properties": {
          "path": {"type": "string"},
          "sha256": {"type": "string"},
          "size": {"type": "integer"},
          "executable": {"type": "boolean"}
        }
      }
    }
  }
}`

var (
	schemaDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	fileDigestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	integerToken        = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	manifestSchema      = mustCompileManifestSchema()
)

// Manifest is the closed updater-manifest/v1 document. Entrypoint and file
// paths are relative to /updater/ and lie strictly beneath payload/.
type Manifest struct {
	APIVersion string `json:"apiVersion"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	Protocol   int    `json:"protocol"`
	Entrypoint string `json:"entrypoint"`
	// Dependencies is always empty: only dependency-free updaters are accepted.
	Dependencies []policy.RuntimeDependency `json:"dependencies"`
	Schemas      ManifestSchemas            `json:"schemas"`
	Files        []ManifestFile             `json:"files"`
}

// ManifestSchemas holds the sha256:<hex> digests of the updater schemas.
type ManifestSchemas struct {
	Config      string `json:"config"`
	Observation string `json:"observation"`
	Request     string `json:"request"`
	Result      string `json:"result"`
	Checkpoint  string `json:"checkpoint"`
}

// ManifestFile describes one payload file. SHA256 is bare lowercase hex.
type ManifestFile struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}

// ParseManifest strictly decodes and validates manifest bytes against the
// closed updater-manifest/v1 contract.
func ParseManifest(data []byte) (*Manifest, error) {
	doc, err := decodeManifestJSON(data)
	if err != nil {
		return nil, err
	}
	if err := manifestSchema.Validate(doc); err != nil {
		return nil, fmt.Errorf("validate updater manifest schema: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode updater manifest: %w", err)
	}
	if m.Dependencies == nil {
		m.Dependencies = []policy.RuntimeDependency{}
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.APIVersion != ManifestAPIVersion {
		return fmt.Errorf("unsupported updater manifest version %q", m.APIVersion)
	}
	if m.Protocol != SupportedProtocol {
		return fmt.Errorf("unsupported updater protocol %d", m.Protocol)
	}
	if len(m.Dependencies) != 0 {
		return errors.New("updater manifest declares runtime dependencies; only dependency-free updaters are supported")
	}
	if !validPayloadPath(m.Entrypoint) {
		return fmt.Errorf("manifest entrypoint %q must be a clean path strictly beneath payload/", m.Entrypoint)
	}
	for _, s := range []struct{ name, digest string }{
		{"config", m.Schemas.Config},
		{"observation", m.Schemas.Observation},
		{"request", m.Schemas.Request},
		{"result", m.Schemas.Result},
		{"checkpoint", m.Schemas.Checkpoint},
	} {
		if !schemaDigestPattern.MatchString(s.digest) {
			return fmt.Errorf("manifest.schemas.%s must be a sha256:<lowercase hex> digest", s.name)
		}
	}
	paths := make(map[string]bool, len(m.Files))
	for i, f := range m.Files {
		where := fmt.Sprintf("manifest.files[%d]", i)
		if !validPayloadPath(f.Path) {
			return fmt.Errorf("%s.path %q must be a clean path strictly beneath payload/", where, f.Path)
		}
		if !fileDigestPattern.MatchString(f.SHA256) {
			return fmt.Errorf("%s.sha256 must be 64 lowercase hex digits", where)
		}
		if paths[f.Path] {
			return fmt.Errorf("%s.path %q is listed more than once", where, f.Path)
		}
		paths[f.Path] = true
	}
	for _, f := range m.Files {
		for i := 0; i < len(f.Path); i++ {
			if f.Path[i] == '/' && paths[f.Path[:i]] {
				return fmt.Errorf("manifest file %q is also the parent directory of %q", f.Path[:i], f.Path)
			}
		}
	}
	i := slices.IndexFunc(m.Files, func(f ManifestFile) bool { return f.Path == m.Entrypoint })
	if i < 0 {
		return fmt.Errorf("manifest entrypoint %q is not a listed file", m.Entrypoint)
	}
	if !m.Files[i].Executable {
		return fmt.Errorf("manifest entrypoint %q is not marked executable", m.Entrypoint)
	}
	return nil
}

// VerifyManifestBinding reports whether m declares exactly the identities d approves.
func VerifyManifestBinding(m *Manifest, d *policy.ReleaseDescriptor) error {
	if m.Name != d.Name || m.Version != d.Version {
		return fmt.Errorf("manifest identity %q/%q differs from the approved release %s/%s", m.Name, m.Version, d.Name, d.Version)
	}
	if m.Protocol != d.Protocol {
		return fmt.Errorf("manifest protocol %d differs from the approved protocol %d", m.Protocol, d.Protocol)
	}
	if len(m.Dependencies) != 0 || len(d.Dependencies) != 0 {
		return errors.New("only dependency-free updater releases are supported")
	}
	for _, s := range []struct{ name, declared, approved string }{
		{"config", m.Schemas.Config, d.Schemas.Config.Digest},
		{"observation", m.Schemas.Observation, d.Schemas.Observation.Digest},
		{"request", m.Schemas.Request, d.Schemas.Request.Digest},
		{"result", m.Schemas.Result, d.Schemas.Result.Digest},
		{"checkpoint", m.Schemas.Checkpoint, d.Schemas.Checkpoint.Digest},
	} {
		if s.declared != s.approved {
			return fmt.Errorf("manifest %s schema digest differs from the approved release", s.name)
		}
	}
	return nil
}

// validPayloadPath reports whether p is a clean relative path strictly beneath
// payload/. Clean paths contain no empty, "." or ".." components after the
// leading payload component.
func validPayloadPath(p string) bool {
	if p == "" || strings.ContainsRune(p, 0) || path.Clean(p) != p {
		return false
	}
	first, rest, found := strings.Cut(p, "/")
	return found && first == "payload" && rest != ""
}

// decodeManifestJSON decodes exactly one JSON value into maps, slices and
// scalars, keeping numbers as their literal text and rejecting duplicate keys.
func decodeManifestJSON(data []byte) (any, error) {
	if int64(len(data)) > MaxManifestBytes {
		return nil, errors.New("updater manifest exceeds its size bound")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	doc, err := decodeValue(dec, 0)
	if err != nil {
		return nil, fmt.Errorf("decode updater manifest: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("updater manifest has trailing content")
	}
	return doc, nil
}

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxManifestDepth {
		return nil, errors.New("nesting exceeds limit")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if number, ok := tok.(json.Number); ok && !integerToken.MatchString(string(number)) {
		return nil, fmt.Errorf("number %s must be a non-negative integer in canonical decimal form", number)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			if _, dup := obj[key]; dup {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			if obj[key], err = decodeValue(dec, depth+1); err != nil {
				return nil, err
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			value, err := decodeValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, value)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", delim)
	}
}

func mustCompileManifestSchema() *jsonschema.Schema {
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external schema references are not allowed")
	}
	if err := compiler.AddResource(manifestSchemaURL, strings.NewReader(manifestSchemaJSON)); err != nil {
		panic(err)
	}
	schema, err := compiler.Compile(manifestSchemaURL)
	if err != nil {
		panic(err)
	}
	return schema
}
