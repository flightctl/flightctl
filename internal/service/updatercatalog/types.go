package updatercatalog

import (
	"encoding/json"

	"github.com/flightctl/flightctl/internal/service/common"
)

type AdministratorConfig struct {
	APIVersion  string           `json:"apiVersion" yaml:"apiVersion"`
	Revision    int64            `json:"revision" yaml:"revision"`
	Releases    []ReleaseBinding `json:"releases" yaml:"releases"`
	Revocations []Revocation     `json:"revocations,omitempty" yaml:"revocations,omitempty"`
}

type ReleaseBinding struct {
	Name             string              `json:"name" yaml:"name"`
	Version          string              `json:"version" yaml:"version"`
	PackageDigest    string              `json:"packageDigest" yaml:"packageDigest"`
	Publisher        PublisherBinding    `json:"publisher" yaml:"publisher"`
	Protocol         int                 `json:"protocol" yaml:"protocol"`
	Schemas          SchemaBundle        `json:"schemas" yaml:"schemas"`
	Dependencies     []RuntimeDependency `json:"dependencies" yaml:"dependencies"`
	Access           *PhaseAccess        `json:"access" yaml:"access"`
	Limits           ExecutionLimits     `json:"limits" yaml:"limits"`
	BudgetKeySet     []string            `json:"budgetKeySet" yaml:"budgetKeySet"`
	ReferencePaths   []ReferencePath     `json:"referencePaths,omitempty" yaml:"referencePaths,omitempty"`
	GUIDPaths        []string            `json:"guidPaths,omitempty" yaml:"guidPaths,omitempty"`
	TemplatablePaths []string            `json:"templatablePaths,omitempty" yaml:"templatablePaths,omitempty"`
}

type ReferencePath struct {
	Path string                       `json:"path" yaml:"path"`
	Kind common.FirmwareReferenceKind `json:"kind" yaml:"kind"`
}

type PublisherBinding struct {
	Identity     string          `json:"identity" yaml:"identity"`
	PolicyDigest string          `json:"policyDigest" yaml:"policyDigest"`
	Policy       PublisherPolicy `json:"policy" yaml:"policy"`
}

// PublisherPolicy is a versioned keyed sigstoreSigned requirement. Key paths,
// permissive fallback rules and keyless trust discovery are intentionally absent.
type PublisherPolicy struct {
	Version        int               `json:"version" yaml:"version"`
	Type           string            `json:"type" yaml:"type"`
	KeyData        string            `json:"keyData" yaml:"keyData"`
	SignedIdentity PublisherIdentity `json:"signedIdentity" yaml:"signedIdentity"`
}

type PublisherIdentity struct {
	Type             string `json:"type" yaml:"type"`
	DockerRepository string `json:"dockerRepository" yaml:"dockerRepository"`
}

type SchemaBundle struct {
	Config      SchemaDocument `json:"config" yaml:"config"`
	Observation SchemaDocument `json:"observation" yaml:"observation"`
	Request     SchemaDocument `json:"request" yaml:"request"`
	Result      SchemaDocument `json:"result" yaml:"result"`
	Checkpoint  SchemaDocument `json:"checkpoint" yaml:"checkpoint"`
}

// Bytes contains exact JSON text, including whitespace, rather than a YAML object.
// Its digest remains stable across catalog serialization formats.
type SchemaDocument struct {
	Digest string `json:"digest" yaml:"digest"`
	Bytes  string `json:"bytes" yaml:"bytes"`
}

type RuntimeDependency struct {
	Name       string `json:"name" yaml:"name"`
	Constraint string `json:"constraint" yaml:"constraint"`
}

type PhaseAccess struct {
	Prepare *AccessGrant `json:"prepare" yaml:"prepare"`
	Apply   *AccessGrant `json:"apply" yaml:"apply"`
	Verify  *AccessGrant `json:"verify" yaml:"verify"`
}

// Workspace writes are confined to the agent-owned private workspace. Host and
// device writes are expressible only for apply; no phase grants a host reboot.
type AccessGrant struct {
	NetworkRepositories    []string `json:"networkRepositories,omitempty" yaml:"networkRepositories,omitempty"`
	CredentialRepositories []string `json:"credentialRepositories,omitempty" yaml:"credentialRepositories,omitempty"`
	ReadPaths              []string `json:"readPaths,omitempty" yaml:"readPaths,omitempty"`
	WritePaths             []string `json:"writePaths,omitempty" yaml:"writePaths,omitempty"`
	ReadDevices            []string `json:"readDevices,omitempty" yaml:"readDevices,omitempty"`
	WriteDevices           []string `json:"writeDevices,omitempty" yaml:"writeDevices,omitempty"`
	WorkspaceWrite         bool     `json:"workspaceWrite,omitempty" yaml:"workspaceWrite,omitempty"`
}

type ExecutionLimits struct {
	MaxRequestBytes      int64 `json:"maxRequestBytes" yaml:"maxRequestBytes"`
	MaxResultBytes       int64 `json:"maxResultBytes" yaml:"maxResultBytes"`
	MaxStderrBytes       int64 `json:"maxStderrBytes" yaml:"maxStderrBytes"`
	MaxCheckpointBytes   int64 `json:"maxCheckpointBytes" yaml:"maxCheckpointBytes"`
	MaxScratchBytes      int64 `json:"maxScratchBytes" yaml:"maxScratchBytes"`
	MaxMemoryBytes       int64 `json:"maxMemoryBytes" yaml:"maxMemoryBytes"`
	MaxProcesses         int64 `json:"maxProcesses" yaml:"maxProcesses"`
	MaxInvocationSeconds int64 `json:"maxInvocationSeconds" yaml:"maxInvocationSeconds"`
	MaxCycles            int64 `json:"maxCycles" yaml:"maxCycles"`
	MaxReboots           int64 `json:"maxReboots" yaml:"maxReboots"`
	MaxAttempts          int64 `json:"maxAttempts" yaml:"maxAttempts"`
}

type Revocation struct {
	Name             string `json:"name" yaml:"name"`
	Version          string `json:"version" yaml:"version"`
	DescriptorDigest string `json:"descriptorDigest" yaml:"descriptorDigest"`
	Reason           string `json:"reason" yaml:"reason"`
}

func (c AdministratorConfig) Digest() (string, error) { return canonicalDigest(c) }
func (r ReleaseBinding) Digest() (string, error)      { return canonicalDigest(r) }

// RetainedEvidence returns both legacy pinned-config and observation identities.
// Neither retained alias confers current execution authorization.
func (r ReleaseBinding) RetainedEvidence() (map[string]common.RetainedSchemaEvidence, error) {
	evidence := make(map[string]common.RetainedSchemaEvidence)
	for _, d := range []string{r.Schemas.Config.Digest, r.Schemas.Observation.Digest} {
		evidence[d] = common.RetainedSchemaEvidence{SchemaDigest: d, ObservationSchema: json.RawMessage([]byte(r.Schemas.Observation.Bytes)), BudgetKeySet: append([]string(nil), r.BudgetKeySet...)}
	}
	return evidence, nil
}
