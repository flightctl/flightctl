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
	"strconv"
	"strings"

	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
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

var (
	schemaDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	fileDigestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	integerToken        = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
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

// ParseManifest strictly decodes and structurally validates manifest bytes
// against the closed updater-manifest/v1 contract.
func ParseManifest(data []byte) (*Manifest, error) {
	doc, err := decodeManifestJSON(data)
	if err != nil {
		return nil, err
	}
	top := newClosedObject(doc, "manifest", "apiVersion", "name", "version", "protocol", "entrypoint", "dependencies", "schemas", "files")
	m := &Manifest{
		APIVersion:   top.str("apiVersion"),
		Name:         top.str("name"),
		Version:      top.str("version"),
		Entrypoint:   top.str("entrypoint"),
		Dependencies: []policy.RuntimeDependency{},
	}
	protocol := top.integer("protocol")
	dependencies := top.array("dependencies")
	if top.err != nil {
		return nil, top.err
	}
	if m.APIVersion != ManifestAPIVersion {
		return nil, fmt.Errorf("unsupported updater manifest version %q", m.APIVersion)
	}
	if protocol != SupportedProtocol {
		return nil, fmt.Errorf("unsupported updater protocol %d", protocol)
	}
	m.Protocol = SupportedProtocol
	if len(dependencies) != 0 {
		return nil, errors.New("updater manifest declares runtime dependencies; only dependency-free updaters are supported")
	}
	if !validPayloadPath(m.Entrypoint) {
		return nil, fmt.Errorf("manifest entrypoint %q must be a clean path strictly beneath payload/", m.Entrypoint)
	}
	if m.Schemas, err = parseManifestSchemas(top.values["schemas"]); err != nil {
		return nil, err
	}
	if m.Files, err = parseManifestFiles(top.values["files"]); err != nil {
		return nil, err
	}
	i := slices.IndexFunc(m.Files, func(f ManifestFile) bool { return f.Path == m.Entrypoint })
	if i < 0 {
		return nil, fmt.Errorf("manifest entrypoint %q is not a listed file", m.Entrypoint)
	}
	if !m.Files[i].Executable {
		return nil, fmt.Errorf("manifest entrypoint %q is not marked executable", m.Entrypoint)
	}
	return m, nil
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

func parseManifestSchemas(v any) (ManifestSchemas, error) {
	o := newClosedObject(v, "manifest.schemas", "config", "observation", "request", "result", "checkpoint")
	s := ManifestSchemas{
		Config:      o.str("config"),
		Observation: o.str("observation"),
		Request:     o.str("request"),
		Result:      o.str("result"),
		Checkpoint:  o.str("checkpoint"),
	}
	if o.err != nil {
		return ManifestSchemas{}, o.err
	}
	for _, d := range []struct{ name, digest string }{
		{"config", s.Config}, {"observation", s.Observation}, {"request", s.Request}, {"result", s.Result}, {"checkpoint", s.Checkpoint},
	} {
		if !schemaDigestPattern.MatchString(d.digest) {
			return ManifestSchemas{}, fmt.Errorf("manifest.schemas.%s must be a sha256:<lowercase hex> digest", d.name)
		}
	}
	return s, nil
}

func parseManifestFiles(v any) ([]ManifestFile, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("manifest.files must be an array")
	}
	if len(list) == 0 {
		return nil, errors.New("manifest.files must list at least one file")
	}
	files := make([]ManifestFile, 0, len(list))
	paths := make(map[string]bool, len(list))
	for i, item := range list {
		where := fmt.Sprintf("manifest.files[%d]", i)
		o := newClosedObject(item, where, "path", "sha256", "size", "executable")
		f := ManifestFile{Path: o.str("path"), SHA256: o.str("sha256"), Size: o.integer("size"), Executable: o.boolean("executable")}
		if o.err != nil {
			return nil, o.err
		}
		if !validPayloadPath(f.Path) {
			return nil, fmt.Errorf("%s.path %q must be a clean path strictly beneath payload/", where, f.Path)
		}
		if !fileDigestPattern.MatchString(f.SHA256) {
			return nil, fmt.Errorf("%s.sha256 must be 64 lowercase hex digits", where)
		}
		if paths[f.Path] {
			return nil, fmt.Errorf("%s.path %q is listed more than once", where, f.Path)
		}
		paths[f.Path] = true
		files = append(files, f)
	}
	for _, f := range files {
		for i := 0; i < len(f.Path); i++ {
			if f.Path[i] == '/' && paths[f.Path[:i]] {
				return nil, fmt.Errorf("manifest file %q is also the parent directory of %q", f.Path[:i], f.Path)
			}
		}
	}
	return files, nil
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

// closedObject reads typed members of a JSON object whose key set is exact.
// The first violation is kept in err and later reads return zero values.
type closedObject struct {
	where  string
	values map[string]any
	err    error
}

func newClosedObject(v any, where string, keys ...string) *closedObject {
	o := &closedObject{where: where}
	obj, ok := v.(map[string]any)
	if !ok {
		o.err = fmt.Errorf("%s must be an object", where)
		return o
	}
	for k := range obj {
		if !slices.Contains(keys, k) {
			o.err = fmt.Errorf("%s has unknown key %q", where, k)
			return o
		}
	}
	for _, k := range keys {
		if _, ok := obj[k]; !ok {
			o.err = fmt.Errorf("%s is missing %q", where, k)
			return o
		}
	}
	o.values = obj
	return o
}

func (o *closedObject) str(key string) string {
	if o.err != nil {
		return ""
	}
	s, ok := o.values[key].(string)
	if !ok {
		o.err = fmt.Errorf("%s.%s must be a string", o.where, key)
	}
	return s
}

func (o *closedObject) integer(key string) int64 {
	if o.err != nil {
		return 0
	}
	n, ok := o.values[key].(json.Number)
	if !ok || !integerToken.MatchString(string(n)) {
		o.err = fmt.Errorf("%s.%s must be a non-negative integer", o.where, key)
		return 0
	}
	v, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil {
		o.err = fmt.Errorf("%s.%s exceeds the integer range", o.where, key)
		return 0
	}
	return v
}

func (o *closedObject) boolean(key string) bool {
	if o.err != nil {
		return false
	}
	b, ok := o.values[key].(bool)
	if !ok {
		o.err = fmt.Errorf("%s.%s must be a boolean", o.where, key)
	}
	return b
}

func (o *closedObject) array(key string) []any {
	if o.err != nil {
		return nil
	}
	a, ok := o.values[key].([]any)
	if !ok {
		o.err = fmt.Errorf("%s.%s must be an array", o.where, key)
	}
	return a
}
