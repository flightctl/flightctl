package updatercatalog

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/distribution/reference"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"
)

const (
	maxCatalogBytes = 4 * 1024 * 1024
	maxSchemaBytes  = 256 * 1024
	maxRecords      = 256
	maxDepth        = 64
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	namePattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

func ParseAdministratorConfig(data []byte) (AdministratorConfig, error) {
	var candidate AdministratorConfig
	if len(data) > maxCatalogBytes {
		return candidate, errors.New("catalog exceeds size limit")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return candidate, errors.New("invalid catalog document")
	}
	if err := validateNode(&document, 0, true); err != nil {
		return candidate, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return candidate, errors.New("catalog must contain exactly one document")
	}
	decoder = yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&candidate); err != nil {
		return AdministratorConfig{}, errors.New("catalog contains unknown, duplicate or invalid fields")
	}
	if err := validateConfig(&candidate); err != nil {
		return AdministratorConfig{}, err
	}
	return candidate, nil
}

func validateNode(n *yaml.Node, depth int, integersOnly bool) error {
	if integersOnly && n.Tag == "!!float" {
		return errors.New("catalog numeric fields must be integers")
	}
	if depth > maxDepth || n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("catalog aliases or excessive nesting are not allowed")
	}
	if n.Kind == yaml.MappingNode {
		keys := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || keys[k.Value] {
				return errors.New("catalog requires unique string keys")
			}
			keys[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := validateNode(child, depth+1, integersOnly); err != nil {
			return err
		}
	}
	return nil
}

func validateConfig(c *AdministratorConfig) error {
	if c.APIVersion != "firmware-catalog/v1" || c.Revision <= 0 {
		return errors.New("catalog requires version firmware-catalog/v1 and a positive revision")
	}
	if len(c.Releases)+len(c.Revocations) > maxRecords {
		return errors.New("catalog exceeds record limit")
	}
	identities := map[string]bool{}
	retained := map[string]string{}
	for i := range c.Releases {
		r := &c.Releases[i]
		key := r.Name + "/" + r.Version
		if identities[key] {
			return errors.New("duplicate catalog release identity")
		}
		identities[key] = true
		if err := validateRelease(r); err != nil {
			return fmt.Errorf("release %d: %w", i, err)
		}
		evidence, err := r.RetainedEvidence()
		if err != nil {
			return fmt.Errorf("release %d retained evidence: %w", i, err)
		}
		for d, e := range evidence {
			hash, err := canonicalDigest(struct {
				Schema  string
				Budgets []string
			}{string(e.ObservationSchema), e.BudgetKeySet})
			if err != nil {
				return err
			}
			k := r.Name + "@" + d
			if old, ok := retained[k]; ok && old != hash {
				return errors.New("conflicting retained schema evidence")
			}
			retained[k] = hash
		}
	}
	for _, r := range c.Revocations {
		key := r.Name + "/" + r.Version
		if !namePattern.MatchString(r.Name) || !namePattern.MatchString(r.Version) || !digestPattern.MatchString(r.DescriptorDigest) || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 1024 || identities[key] {
			return errors.New("invalid, duplicate or active revocation identity")
		}
		identities[key] = true
	}
	return nil
}

func validateRelease(r *ReleaseBinding) error {
	if !namePattern.MatchString(r.Name) || !namePattern.MatchString(r.Version) || !digestPattern.MatchString(r.PackageDigest) || r.Protocol != 1 {
		return errors.New("invalid release identity, package digest or protocol")
	}
	if err := validatePublisher(r.Publisher); err != nil {
		return err
	}
	for _, s := range []SchemaDocument{r.Schemas.Config, r.Schemas.Observation, r.Schemas.Request, r.Schemas.Result, r.Schemas.Checkpoint} {
		if err := validateSchema(s); err != nil {
			return err
		}
	}
	if err := validateAccess(r.Access); err != nil {
		return err
	}
	if err := validateLimits(r); err != nil {
		return err
	}
	dependencies := map[string]bool{}
	for _, d := range r.Dependencies {
		if !namePattern.MatchString(d.Name) || strings.TrimSpace(d.Constraint) == "" || len(d.Constraint) > 256 || dependencies[d.Name] {
			return errors.New("invalid or duplicate runtime dependency")
		}
		dependencies[d.Name] = true
	}
	for _, p := range r.ReferencePaths {
		if !validConfigPath(p.Path) || p.Kind != "artifact" && p.Kind != "signature" {
			return errors.New("invalid reference path or kind")
		}
	}
	for _, p := range append(append([]string(nil), r.GUIDPaths...), r.TemplatablePaths...) {
		if !validConfigPath(p) {
			return errors.New("invalid configuration path")
		}
	}
	return nil
}

func validateLimits(r *ReleaseBinding) error {
	limits := r.Limits.values()
	for _, value := range limits {
		if value <= 0 {
			return errors.New("all execution limits must be positive integers")
		}
	}
	if r.Limits.MaxInvocationSeconds > 86400 || r.Limits.MaxProcesses > 1048576 || r.Limits.MaxCycles > 1048576 || r.Limits.MaxReboots > 1048576 || r.Limits.MaxAttempts > 1048576 {
		return errors.New("execution count or timeout exceeds supported bound")
	}
	if len(r.BudgetKeySet) == 0 {
		return errors.New("budget key evidence is required")
	}
	slices.Sort(r.BudgetKeySet)
	for i, k := range r.BudgetKeySet {
		if _, ok := limits[k]; !ok || i > 0 && r.BudgetKeySet[i-1] == k {
			return errors.New("invalid or duplicate budget key")
		}
	}
	return nil
}

func (l ExecutionLimits) values() map[string]int64 {
	return map[string]int64{"maxRequestBytes": l.MaxRequestBytes, "maxResultBytes": l.MaxResultBytes, "maxStderrBytes": l.MaxStderrBytes, "maxCheckpointBytes": l.MaxCheckpointBytes, "maxScratchBytes": l.MaxScratchBytes, "maxMemoryBytes": l.MaxMemoryBytes, "maxProcesses": l.MaxProcesses, "maxInvocationSeconds": l.MaxInvocationSeconds, "maxCycles": l.MaxCycles, "maxReboots": l.MaxReboots, "maxAttempts": l.MaxAttempts}
}

func validatePublisher(p PublisherBinding) error {
	if !namePattern.MatchString(p.Identity) || p.Policy.Version != 1 || p.Policy.Type != "sigstoreSigned" || p.Policy.SignedIdentity.Type != "exactRepository" {
		return errors.New("publisher requires a versioned keyed sigstoreSigned exactRepository policy")
	}
	repo := p.Policy.SignedIdentity.DockerRepository
	named, err := reference.ParseNormalizedNamed(repo)
	if err != nil || !reference.IsNameOnly(named) || named.Name() != repo {
		return errors.New("publisher repository must be an explicit canonical repository without tag or digest")
	}
	keyData, err := base64.StdEncoding.DecodeString(p.Policy.KeyData)
	if err != nil {
		return errors.New("invalid publisher public key encoding")
	}
	block, rest := pem.Decode(keyData)
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("publisher requires exactly one PKIX public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return errors.New("invalid publisher public key")
	}
	switch k := key.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return errors.New("publisher RSA key is too small")
		}
	case *ecdsa.PublicKey:
	case ed25519.PublicKey:
	default:
		return errors.New("unsupported publisher public key")
	}
	hash, err := canonicalDigest(p.Policy)
	if err != nil || !digestPattern.MatchString(p.PolicyDigest) || hash != p.PolicyDigest {
		return errors.New("publisher policy digest mismatch")
	}
	return nil
}

func validateAccess(a *PhaseAccess) error {
	if a == nil || a.Prepare == nil || a.Apply == nil || a.Verify == nil {
		return errors.New("explicit access grants are required for every phase")
	}
	for _, g := range []*AccessGrant{a.Apply, a.Verify} {
		if len(g.NetworkRepositories) > 0 || len(g.CredentialRepositories) > 0 {
			return errors.New("only prepare may acquire network or source credentials")
		}
	}
	if len(a.Prepare.WritePaths) > 0 || len(a.Prepare.WriteDevices) > 0 || len(a.Verify.WritePaths) > 0 || len(a.Verify.WriteDevices) > 0 || a.Verify.WorkspaceWrite {
		return errors.New("prepare may only write its workspace and verify must be read-only")
	}
	for _, g := range []*AccessGrant{a.Prepare, a.Apply, a.Verify} {
		for _, paths := range [][]string{g.ReadPaths, g.WritePaths, g.ReadDevices, g.WriteDevices} {
			seen := map[string]bool{}
			for _, p := range paths {
				if !path.IsAbs(p) || p == "/" || path.Clean(p) != p || strings.ContainsAny(p, "*?[]\x00\r\n") || seen[p] {
					return errors.New("access paths must be unique explicit clean absolute paths")
				}
				seen[p] = true
			}
		}
		for _, p := range append(append([]string(nil), g.ReadDevices...), g.WriteDevices...) {
			if !strings.HasPrefix(p, "/dev/") {
				return errors.New("device grants must name paths under /dev")
			}
		}
		for _, repos := range [][]string{g.NetworkRepositories, g.CredentialRepositories} {
			seen := map[string]bool{}
			for _, repo := range repos {
				if !namePattern.MatchString(repo) || seen[repo] {
					return errors.New("repository grants require unique names")
				}
				seen[repo] = true
			}
		}
		for _, repo := range g.CredentialRepositories {
			if !slices.Contains(g.NetworkRepositories, repo) {
				return errors.New("credential grant requires matching network repository grant")
			}
		}
	}
	return nil
}

func validateSchema(s SchemaDocument) error {
	if len(s.Bytes) == 0 || len(s.Bytes) > maxSchemaBytes || !digestPattern.MatchString(s.Digest) || digestBytes([]byte(s.Bytes)) != s.Digest {
		return errors.New("schema size or exact-byte digest is invalid")
	}
	if !json.Valid([]byte(s.Bytes)) {
		return errors.New("schema must contain valid JSON")
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s.Bytes), &n); err != nil {
		return errors.New("invalid schema document")
	}
	if err := validateNode(&n, 0, false); err != nil {
		return err
	}
	var schema any
	if err := json.Unmarshal([]byte(s.Bytes), &schema); err != nil {
		return err
	}
	root, ok := schema.(map[string]any)
	if !ok || root["type"] != "object" {
		return errors.New("schema root must be a closed object")
	}
	if err := validateClosedSchema(schema); err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external schema references are not allowed")
	}
	const schemaURL = "https://flightctl.invalid/updater-schema.json"
	if err := compiler.AddResource(schemaURL, strings.NewReader(s.Bytes)); err != nil {
		return errors.New("invalid schema")
	}
	if _, err := compiler.Compile(schemaURL); err != nil {
		return errors.New("schema compilation failed")
	}
	return nil
}

func validateClosedSchema(value any) error {
	switch v := value.(type) {
	case map[string]any:
		typ, _ := v["type"].(string)
		_, typePresent := v["type"]
		_, hasProperties := v["properties"]
		_, hasPatternProperties := v["patternProperties"]
		objectType := !typePresent && (hasProperties || hasPatternProperties)
		if typ == "object" {
			objectType = true
		}
		if types, ok := v["type"].([]any); ok {
			for _, typ := range types {
				if typ == "object" {
					objectType = true
				}
			}
		}
		if objectType {
			if v["additionalProperties"] != false {
				return errors.New("object schemas must forbid additional properties")
			}
		}
		for key, child := range v {
			switch key {
			case "$ref", "$dynamicRef", "$recursiveRef":
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#") {
					return errors.New("only local schema references are allowed")
				}
			case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies":
				if entries, ok := child.(map[string]any); ok {
					for _, entry := range entries {
						if err := validateClosedSchema(entry); err != nil {
							return err
						}
					}
				}
			case "additionalProperties", "unevaluatedProperties", "propertyNames", "contains", "items", "additionalItems", "not", "if", "then", "else", "contentSchema", "unevaluatedItems", "allOf", "anyOf", "oneOf", "prefixItems":
				if err := validateClosedSchema(child); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, child := range v {
			if err := validateClosedSchema(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func validConfigPath(p string) bool {
	if p == "" {
		return false
	}
	for _, part := range strings.Split(p, ".") {
		if !namePattern.MatchString(strings.TrimSuffix(part, "[]")) {
			return false
		}
	}
	return true
}

func canonicalDigest(value any) (string, error) {
	data, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err = decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func digestBytes(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
