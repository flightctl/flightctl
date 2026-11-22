package firmwarecatalog

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"

	catalog "github.com/flightctl/flightctl/internal/service/updatercatalog"
)

func New() (catalog.AdministratorConfig, error) {
	key, err := x509.MarshalPKIXPublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public())
	if err != nil {
		return catalog.AdministratorConfig{}, err
	}
	policy := catalog.PublisherPolicy{Version: 1, Type: "sigstoreSigned", KeyData: base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: key})), SignedIdentity: catalog.PublisherIdentity{Type: "exactRepository", DockerRepository: "quay.io/example/updater"}}
	raw, err := json.Marshal(policy)
	if err != nil {
		return catalog.AdministratorConfig{}, err
	}
	var value any
	if err = json.Unmarshal(raw, &value); err != nil {
		return catalog.AdministratorConfig{}, err
	}
	raw, err = json.Marshal(value)
	if err != nil {
		return catalog.AdministratorConfig{}, err
	}
	digest := func(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
	schema := catalog.SchemaDocument{Bytes: `{"type":"object","additionalProperties":false,"properties":{}}`}
	schema.Digest = digest([]byte(schema.Bytes))
	binding := catalog.ReleaseBinding{Name: "nvidia-orin", Version: "0.1.0", PackageDigest: digest([]byte("test-updater")), Protocol: 1, Publisher: catalog.PublisherBinding{Identity: "example", PolicyDigest: digest(raw), Policy: policy}, Schemas: catalog.SchemaBundle{Config: schema, Observation: schema, Request: schema, Result: schema, Checkpoint: schema}, Access: &catalog.PhaseAccess{Prepare: &catalog.AccessGrant{WorkspaceWrite: true}, Apply: &catalog.AccessGrant{}, Verify: &catalog.AccessGrant{}}, Limits: catalog.ExecutionLimits{MaxRequestBytes: 4096, MaxResultBytes: 4096, MaxStderrBytes: 4096, MaxCheckpointBytes: 4096, MaxScratchBytes: 1024 * 1024, MaxMemoryBytes: 64 * 1024 * 1024, MaxProcesses: 4, MaxInvocationSeconds: 60, MaxCycles: 3, MaxReboots: 3, MaxAttempts: 3}, BudgetKeySet: []string{"maxCycles"}}
	return catalog.AdministratorConfig{APIVersion: "firmware-catalog/v1", Revision: 1, Releases: []catalog.ReleaseBinding{binding}}, nil
}
