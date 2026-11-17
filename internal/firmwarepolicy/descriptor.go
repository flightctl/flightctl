package firmwarepolicy

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/distribution/reference"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ReleaseDescriptor is the closed approved-release descriptor carried as exact
// bytes in a policy Binding.
type ReleaseDescriptor struct {
	Name             string              `json:"name"`
	Version          string              `json:"version"`
	PackageDigest    string              `json:"packageDigest"`
	Publisher        PublisherBinding    `json:"publisher"`
	Protocol         int                 `json:"protocol"`
	Schemas          SchemaBundle        `json:"schemas"`
	Dependencies     []RuntimeDependency `json:"dependencies"`
	Access           *PhaseAccess        `json:"access"`
	Limits           ExecutionLimits     `json:"limits"`
	BudgetKeySet     []string            `json:"budgetKeySet"`
	Extraction       *ExtractionLimits   `json:"extraction,omitempty"`
	ReferencePaths   []ReferencePath     `json:"referencePaths,omitempty"`
	GUIDPaths        []string            `json:"guidPaths,omitempty"`
	TemplatablePaths []string            `json:"templatablePaths,omitempty"`
}

type PublisherBinding struct {
	Identity     string          `json:"identity"`
	PolicyDigest string          `json:"policyDigest"`
	Policy       PublisherPolicy `json:"policy"`
}

// PublisherPolicy is a keyed sigstoreSigned requirement for one exact
// repository. KeyData is the base64-encoded PEM public key.
type PublisherPolicy struct {
	Version        int               `json:"version"`
	Type           string            `json:"type"`
	KeyData        string            `json:"keyData"`
	SignedIdentity PublisherIdentity `json:"signedIdentity"`
}

type PublisherIdentity struct {
	Type             string `json:"type"`
	DockerRepository string `json:"dockerRepository"`
}

type SchemaBundle struct {
	Config      SchemaDocument `json:"config"`
	Observation SchemaDocument `json:"observation"`
	Request     SchemaDocument `json:"request"`
	Result      SchemaDocument `json:"result"`
	Checkpoint  SchemaDocument `json:"checkpoint"`
}

// SchemaDocument holds exact JSON schema text; Digest covers those bytes.
type SchemaDocument struct {
	Digest string `json:"digest"`
	Bytes  string `json:"bytes"`
}

type RuntimeDependency struct {
	Name       string `json:"name"`
	Constraint string `json:"constraint"`
}

type PhaseAccess struct {
	Prepare *AccessGrant `json:"prepare"`
	Apply   *AccessGrant `json:"apply"`
	Verify  *AccessGrant `json:"verify"`
}

// AccessGrant names logical repositories, host paths and devices an updater
// phase may use. Workspace writes target the agent-owned private workspace.
type AccessGrant struct {
	NetworkRepositories    []string `json:"networkRepositories,omitempty"`
	CredentialRepositories []string `json:"credentialRepositories,omitempty"`
	ReadPaths              []string `json:"readPaths,omitempty"`
	WritePaths             []string `json:"writePaths,omitempty"`
	ReadDevices            []string `json:"readDevices,omitempty"`
	WriteDevices           []string `json:"writeDevices,omitempty"`
	WorkspaceWrite         bool     `json:"workspaceWrite,omitempty"`
}

type ExecutionLimits struct {
	MaxRequestBytes      int64 `json:"maxRequestBytes"`
	MaxResultBytes       int64 `json:"maxResultBytes"`
	MaxStderrBytes       int64 `json:"maxStderrBytes"`
	MaxCheckpointBytes   int64 `json:"maxCheckpointBytes"`
	MaxScratchBytes      int64 `json:"maxScratchBytes"`
	MaxMemoryBytes       int64 `json:"maxMemoryBytes"`
	MaxProcesses         int64 `json:"maxProcesses"`
	MaxInvocationSeconds int64 `json:"maxInvocationSeconds"`
	MaxCycles            int64 `json:"maxCycles"`
	MaxReboots           int64 `json:"maxReboots"`
	MaxAttempts          int64 `json:"maxAttempts"`
}

// ExtractionLimits is the optional authenticated budget for extracting an
// updater payload. Its JSON field names are its budget keys.
type ExtractionLimits struct {
	MaxExtractedManifestBytes int64 `json:"maxExtractedManifestBytes"`
	MaxExtractedFiles         int64 `json:"maxExtractedFiles"`
	MaxExtractedFileBytes     int64 `json:"maxExtractedFileBytes"`
	MaxExtractedTotalBytes    int64 `json:"maxExtractedTotalBytes"`
	// ExtractionScratchBytes is the free space required before extraction
	// starts; it is not a reservation.
	ExtractionScratchBytes int64 `json:"extractionScratchBytes"`
	MaxExtractionSeconds   int64 `json:"maxExtractionSeconds"`
}

// maxExtractionSeconds bounds the extraction time a release may declare.
const maxExtractionSeconds = 86400

// Values returns the limits keyed by their budget keys.
func (l ExtractionLimits) Values() map[string]int64 {
	return map[string]int64{
		"maxExtractedManifestBytes": l.MaxExtractedManifestBytes,
		"maxExtractedFiles":         l.MaxExtractedFiles,
		"maxExtractedFileBytes":     l.MaxExtractedFileBytes,
		"maxExtractedTotalBytes":    l.MaxExtractedTotalBytes,
		"extractionScratchBytes":    l.ExtractionScratchBytes,
		"maxExtractionSeconds":      l.MaxExtractionSeconds,
	}
}

// Validate requires positive budgets, a per-file budget within the total, a
// scratch budget that holds the files plus the manifest, and an extraction
// time of at most one day.
func (l ExtractionLimits) Validate() error {
	for key, value := range l.Values() {
		if value <= 0 {
			return fmt.Errorf("extraction budget %s must be a positive integer", key)
		}
	}
	if l.MaxExtractedFileBytes > l.MaxExtractedTotalBytes {
		return errors.New("extraction budget maxExtractedFileBytes exceeds maxExtractedTotalBytes")
	}
	if l.MaxExtractedTotalBytes > math.MaxInt64-l.MaxExtractedManifestBytes ||
		l.ExtractionScratchBytes < l.MaxExtractedTotalBytes+l.MaxExtractedManifestBytes {
		return errors.New("extraction budget extractionScratchBytes must hold maxExtractedTotalBytes plus maxExtractedManifestBytes")
	}
	if l.MaxExtractionSeconds > maxExtractionSeconds {
		return fmt.Errorf("extraction budget maxExtractionSeconds exceeds %d", maxExtractionSeconds)
	}
	return nil
}

type ReferencePath struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

const (
	publisherPolicyType   = "sigstoreSigned"
	publisherIdentityType = "exactRepository"
)

// DecodeDescriptor strictly decodes b.Descriptor and verifies its digest, its
// name/version against b, its package digest form, its v1 keyed sigstoreSigned
// exactRepository publisher policy, the exact-byte digest of every schema, and
// its access against ValidateAccess, and its optional extraction budget,
// whose keys must all be listed in budgetKeySet.
func DecodeDescriptor(b Binding) (*ReleaseDescriptor, error) {
	if len(b.Descriptor) == 0 || Hash(b.Descriptor) != b.DescriptorDigest {
		return nil, errors.New("release descriptor digest differs from its policy binding")
	}
	var d ReleaseDescriptor
	if err := decodeDescriptorJSON(b.Descriptor, &d); err != nil {
		return nil, fmt.Errorf("decode release descriptor: %w", err)
	}
	if d.Name != b.Name || d.Version != b.Version {
		return nil, errors.New("release descriptor identity differs from its policy binding")
	}
	if !digestPattern.MatchString(d.PackageDigest) {
		return nil, errors.New("release descriptor package digest is malformed")
	}
	if err := validatePublisher(d.Publisher); err != nil {
		return nil, err
	}
	for _, s := range []struct {
		name string
		doc  SchemaDocument
	}{
		{"config", d.Schemas.Config},
		{"observation", d.Schemas.Observation},
		{"request", d.Schemas.Request},
		{"result", d.Schemas.Result},
		{"checkpoint", d.Schemas.Checkpoint},
	} {
		if len(s.doc.Bytes) == 0 || !digestPattern.MatchString(s.doc.Digest) || Hash([]byte(s.doc.Bytes)) != s.doc.Digest {
			return nil, fmt.Errorf("release descriptor %s schema digest differs from its exact bytes", s.name)
		}
	}
	if err := ValidateAccess(d.Access); err != nil {
		return nil, fmt.Errorf("release descriptor access: %w", err)
	}
	if d.Extraction != nil {
		if err := d.Extraction.Validate(); err != nil {
			return nil, fmt.Errorf("release descriptor extraction: %w", err)
		}
		for key := range d.Extraction.Values() {
			if !slices.Contains(d.BudgetKeySet, key) {
				return nil, fmt.Errorf("release descriptor extraction budget %s is not listed in budgetKeySet", key)
			}
		}
	}
	return &d, nil
}

// PackageReference returns the digest-pinned reference of the approved package.
func (d *ReleaseDescriptor) PackageReference() string {
	return d.Publisher.Policy.SignedIdentity.DockerRepository + "@" + d.PackageDigest
}

func validatePublisher(p PublisherBinding) error {
	policy := p.Policy
	if !identityPattern.MatchString(p.Identity) || policy.Version != 1 || policy.Type != publisherPolicyType || policy.SignedIdentity.Type != publisherIdentityType {
		return errors.New("publisher requires a versioned keyed sigstoreSigned exactRepository policy")
	}
	if key, err := base64.StdEncoding.DecodeString(policy.KeyData); err != nil || len(key) == 0 {
		return errors.New("publisher key data must be non-empty base64")
	}
	repo := policy.SignedIdentity.DockerRepository
	named, err := reference.ParseNormalizedNamed(repo)
	if err != nil || !reference.IsNameOnly(named) || named.Name() != repo {
		return errors.New("publisher repository must be an explicit canonical repository without tag or digest")
	}
	digest, err := canonicalDigest(policy)
	if err != nil || digest != p.PolicyDigest {
		return errors.New("publisher policy digest differs from the policy")
	}
	return nil
}

// ValidateAccess applies the catalog's mandatory per-phase restrictions and
// entry syntax: network and credentials only in prepare, host and device writes
// only in apply, a read-only verify, credentials matched by network grants,
// logical repository names, and clean absolute paths with devices under /dev.
func ValidateAccess(a *PhaseAccess) error {
	if a == nil || a.Prepare == nil || a.Apply == nil || a.Verify == nil {
		return errors.New("explicit access grants are required for every phase")
	}
	phases := []struct {
		name  string
		grant *AccessGrant
	}{{"prepare", a.Prepare}, {"apply", a.Apply}, {"verify", a.Verify}}
	for _, p := range phases[1:] {
		if len(p.grant.NetworkRepositories) > 0 || len(p.grant.CredentialRepositories) > 0 {
			return fmt.Errorf("%s: only prepare may acquire network or source credentials", p.name)
		}
	}
	if len(a.Prepare.WritePaths) > 0 || len(a.Prepare.WriteDevices) > 0 {
		return errors.New("prepare: only apply may write host paths or devices")
	}
	if len(a.Verify.WritePaths) > 0 || len(a.Verify.WriteDevices) > 0 || a.Verify.WorkspaceWrite {
		return errors.New("verify: verify must be read-only")
	}
	for _, p := range phases {
		if err := validateGrantEntries(p.grant); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
	}
	return nil
}

func validateGrantEntries(g *AccessGrant) error {
	for _, paths := range [][]string{g.ReadPaths, g.WritePaths, g.ReadDevices, g.WriteDevices} {
		seen := map[string]bool{}
		for _, p := range paths {
			if !path.IsAbs(p) || p == "/" || path.Clean(p) != p || strings.ContainsAny(p, "*?[]\x00\r\n") || seen[p] {
				return fmt.Errorf("access path %q must be a unique explicit clean absolute path", p)
			}
			seen[p] = true
		}
	}
	for _, p := range append(slices.Clone(g.ReadDevices), g.WriteDevices...) {
		if !strings.HasPrefix(p, "/dev/") {
			return fmt.Errorf("device grant %q must name a path under /dev", p)
		}
	}
	for _, repos := range [][]string{g.NetworkRepositories, g.CredentialRepositories} {
		seen := map[string]bool{}
		for _, repo := range repos {
			if !identityPattern.MatchString(repo) || seen[repo] {
				return fmt.Errorf("repository grant %q must be a unique logical repository name", repo)
			}
			seen[repo] = true
		}
	}
	for _, repo := range g.CredentialRepositories {
		if !slices.Contains(g.NetworkRepositories, repo) {
			return fmt.Errorf("credential grant %q requires a matching network repository grant", repo)
		}
	}
	return nil
}

func canonicalDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var normalized any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&normalized); err != nil {
		return "", err
	}
	if data, err = json.Marshal(normalized); err != nil {
		return "", err
	}
	return Hash(data), nil
}

//go:embed release-descriptor.schema.json
var descriptorSchemaJSON string

var descriptorSchema, descriptorSchemaErr = compileDescriptorSchema()

func compileDescriptorSchema() (*jsonschema.Schema, error) {
	const name = "release-descriptor.schema.json"
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external schema references are not allowed")
	}
	if err := compiler.AddResource(name, strings.NewReader(descriptorSchemaJSON)); err != nil {
		return nil, err
	}
	return compiler.Compile(name)
}

// The schema enforces exact field names, including in nested objects. Typed
// decoding still defines value types and preserves their existing null behavior.
func decodeDescriptorJSON(data []byte, d *ReleaseDescriptor) error {
	if descriptorSchemaErr != nil {
		return fmt.Errorf("compile release descriptor schema: %w", descriptorSchemaErr)
	}
	if err := uniqueJSON(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	if err := descriptorSchema.Validate(doc); err != nil {
		return fmt.Errorf("validate release descriptor schema: %w", err)
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(d)
}
