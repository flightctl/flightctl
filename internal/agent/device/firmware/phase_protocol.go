package firmware

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

type phaseAction string

const (
	phasePrepare phaseAction = "prepare"
	phaseApply   phaseAction = "apply"
	phaseVerify  phaseAction = "verify"
)

type phaseResultKind string

const (
	phaseCheckpointResult phaseResultKind = "Checkpoint"
	phasePrepared         phaseResultKind = "Prepared"
	phaseApplied          phaseResultKind = "Applied"
	phaseRebootRequired   phaseResultKind = "RebootRequired"
	phaseVerified         phaseResultKind = "Verified"
	phaseActivationArmed  phaseResultKind = "ActivationArmed"
	phaseMoreWork         phaseResultKind = "MoreWork"
	phasePending          phaseResultKind = "Pending"
	phaseUncertain        phaseResultKind = "Uncertain"
	phaseFailed           phaseResultKind = "Failed"
)

type phaseIdentity struct {
	Protocol           int             `json:"protocol"`
	Action             phaseAction     `json:"action"`
	AttemptID          string          `json:"attemptID"`
	CallID             string          `json:"callID"`
	Cycle              int64           `json:"cycle"`
	Name               string          `json:"name"`
	Version            string          `json:"version"`
	DescriptorDigest   string          `json:"descriptorDigest"`
	PackageDigest      string          `json:"packageDigest"`
	ManifestDigest     string          `json:"manifestDigest"`
	ConfigDigest       string          `json:"configDigest"`
	IntentDigest       string          `json:"intentDigest"`
	PlanDigest         string          `json:"planDigest,omitempty"`
	ObservationDigest  string          `json:"observationDigest,omitempty"`
	Schemas            ManifestSchemas `json:"schemas"`
	GrantDigest        string          `json:"grantDigest"`
	CheckpointDigest   string          `json:"checkpointDigest,omitempty"`
	CheckpointFormat   string          `json:"checkpointFormat,omitempty"`
	CheckpointRevision int64           `json:"checkpointRevision,omitempty"`
}

type phaseCheckpoint struct {
	Format   string `json:"format"`
	Revision int64  `json:"revision"`
	Bytes    []byte `json:"bytes"`
	Digest   string `json:"digest"`
}

type phaseRequest struct {
	Identity    phaseIdentity    `json:"identity"`
	Config      []byte           `json:"config"`
	Checkpoint  *phaseCheckpoint `json:"checkpoint,omitempty"`
	Observation []byte           `json:"observation,omitempty"`
}

type phaseTargetKind string

const (
	phaseComponent phaseTargetKind = "component"
	phaseOverlay   phaseTargetKind = "overlay"
)

type phaseTarget struct {
	Kind    phaseTargetKind `json:"kind"`
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Digest  string          `json:"digest"`
}

type phaseObservation struct {
	phaseTarget
	Active bool `json:"active"`
}

type phasePreparedEvidence struct {
	Artifacts       []phaseTarget          `json:"artifacts"`
	SelectionDigest string                 `json:"selectionDigest"`
	Authentication  *phaseSelectionReceipt `json:"authentication"`
	Preflight       *phaseSelectionReceipt `json:"preflight"`
}

type phaseSelectionReceipt struct {
	SelectionDigest string          `json:"selectionDigest"`
	Evidence        json.RawMessage `json:"evidence"`
}

type phaseTargetKey struct {
	kind phaseTargetKind
	name string
}

func (t phaseTarget) key() phaseTargetKey {
	return phaseTargetKey{t.Kind, t.Name}
}

type phaseResult struct {
	Identity          phaseIdentity          `json:"identity"`
	Kind              phaseResultKind        `json:"kind"`
	Checkpoint        *phaseCheckpoint       `json:"checkpoint,omitempty"`
	Observations      []phaseObservation     `json:"observations,omitempty"`
	Evidence          json.RawMessage        `json:"evidence,omitempty"`
	Prepared          *phasePreparedEvidence `json:"prepared,omitempty"`
	OSReadiness       string                 `json:"osReadiness,omitempty"`
	ActivationReceipt json.RawMessage        `json:"activationReceipt,omitempty"`
}

type phaseValidationContext struct {
	request          phaseRequest
	limits           policy.ExecutionLimits
	targets          []phaseTarget
	configSchema     *jsonschema.Schema
	requestSchema    *jsonschema.Schema
	resultSchema     *jsonschema.Schema
	observation      *jsonschema.Schema
	checkpointSchema *jsonschema.Schema
}

func newPhaseValidationContext(manifest *Manifest, descriptor *policy.ReleaseDescriptor, req phaseRequest, targets []phaseTarget) (phaseValidationContext, error) {
	if manifest == nil || descriptor == nil {
		return phaseValidationContext{}, errors.New("pinned manifest and release descriptor are required")
	}
	if err := VerifyManifestBinding(manifest, descriptor); err != nil {
		return phaseValidationContext{}, err
	}
	if err := validatePhaseIdentity(req.Identity, manifest, descriptor); err != nil {
		return phaseValidationContext{}, err
	}
	limits := descriptor.Limits
	if err := validatePhaseLimits(limits); err != nil {
		return phaseValidationContext{}, err
	}
	ctx := phaseValidationContext{request: req, limits: limits, targets: slices.Clone(targets)}
	for _, schema := range []struct {
		name   string
		doc    policy.SchemaDocument
		target **jsonschema.Schema
	}{
		{"config", descriptor.Schemas.Config, &ctx.configSchema},
		{"request", descriptor.Schemas.Request, &ctx.requestSchema},
		{"result", descriptor.Schemas.Result, &ctx.resultSchema},
		{"observation", descriptor.Schemas.Observation, &ctx.observation},
		{"checkpoint", descriptor.Schemas.Checkpoint, &ctx.checkpointSchema},
	} {
		compiled, err := compilePhaseSchema(schema.doc)
		if err != nil {
			return phaseValidationContext{}, fmt.Errorf("approved %s schema: %w", schema.name, err)
		}
		*schema.target = compiled
	}
	if int64(len(req.Config)) > limits.MaxRequestBytes || int64(len(req.Observation)) > limits.MaxRequestBytes {
		return phaseValidationContext{}, errors.New("firmware phase input exceeds request limit")
	}
	ctx.request.Config = bytes.Clone(req.Config)
	ctx.request.Observation = bytes.Clone(req.Observation)
	if req.Identity.Action == phaseVerify {
		if len(req.Observation) == 0 || policy.Hash(req.Observation) != req.Identity.ObservationDigest ||
			validatePhaseJSON(req.Observation, ctx.observation) != nil {
			return phaseValidationContext{}, errors.New("phase observation input differs from its approved identity or schema")
		}
	} else if len(req.Observation) != 0 || req.Identity.ObservationDigest != "" {
		return phaseValidationContext{}, errors.New("observation input is only allowed for verify")
	}
	if req.Checkpoint != nil {
		checkpoint := *req.Checkpoint
		if int64(len(checkpoint.Bytes)) > limits.MaxCheckpointBytes {
			return phaseValidationContext{}, errors.New("input checkpoint exceeds its byte limit")
		}
		checkpoint.Bytes = bytes.Clone(checkpoint.Bytes)
		ctx.request.Checkpoint = &checkpoint
	}
	return ctx, nil
}

func validatePhaseIdentity(id phaseIdentity, manifest *Manifest, descriptor *policy.ReleaseDescriptor) error {
	if id.Protocol != SupportedProtocol || id.Protocol != manifest.Protocol ||
		id.Name != manifest.Name || id.Version != manifest.Version || id.PackageDigest != descriptor.PackageDigest ||
		id.Schemas != manifest.Schemas {
		return errors.New("phase identity differs from the pinned release")
	}
	switch id.Action {
	case phasePrepare, phaseVerify, phaseApply:
	default:
		return errors.New("unknown phase action")
	}
	if id.AttemptID == "" || id.CallID == "" || id.Cycle <= 0 {
		return errors.New("phase attempt, call and cycle are required")
	}
	for _, digest := range []string{id.DescriptorDigest, id.PackageDigest, id.ManifestDigest, id.ConfigDigest, id.IntentDigest, id.GrantDigest} {
		if !schemaDigestPattern.MatchString(digest) {
			return errors.New("invalid phase identity digest")
		}
	}
	if id.PlanDigest != "" && !schemaDigestPattern.MatchString(id.PlanDigest) {
		return errors.New("invalid phase plan digest")
	}
	if id.ObservationDigest != "" && !schemaDigestPattern.MatchString(id.ObservationDigest) {
		return errors.New("invalid phase observation digest")
	}
	return nil
}

func validatePhaseLimits(limits policy.ExecutionLimits) error {
	for _, limit := range []int64{
		limits.MaxRequestBytes, limits.MaxResultBytes, limits.MaxStderrBytes, limits.MaxCheckpointBytes,
		limits.MaxScratchBytes, limits.MaxMemoryBytes, limits.MaxProcesses, limits.MaxInvocationSeconds,
		limits.MaxAttempts, limits.MaxCycles, limits.MaxReboots,
	} {
		if limit <= 0 {
			return errors.New("all phase execution limits must be positive")
		}
	}
	if limits.MaxInvocationSeconds > math.MaxInt64/int64(time.Second) {
		return errors.New("phase invocation duration is unrepresentable")
	}
	return nil
}

func compilePhaseSchema(doc policy.SchemaDocument) (*jsonschema.Schema, error) {
	if doc.Digest != policy.Hash([]byte(doc.Bytes)) {
		return nil, errors.New("digest differs from exact schema bytes")
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external schema references are not allowed")
	}
	const schemaURL = "https://flightctl.invalid/phase-schema.json"
	if err := compiler.AddResource(schemaURL, strings.NewReader(doc.Bytes)); err != nil {
		return nil, errors.New("invalid schema document")
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, errors.New("schema compilation failed")
	}
	return schema, nil
}

func (c phaseValidationContext) encodeRequest() ([]byte, error) {
	req := c.request
	if int64(len(req.Config)) > c.limits.MaxRequestBytes || policy.Hash(req.Config) != req.Identity.ConfigDigest {
		return nil, errors.New("phase config differs from its approved identity or exceeds the request bound")
	}
	if err := validatePhaseJSON(req.Config, c.configSchema); err != nil {
		return nil, fmt.Errorf("invalid phase config: %w", err)
	}
	if req.Identity.Action == phaseVerify {
		if len(req.Observation) == 0 || policy.Hash(req.Observation) != req.Identity.ObservationDigest ||
			validatePhaseJSON(req.Observation, c.observation) != nil {
			return nil, errors.New("phase observation input differs from its approved identity or schema")
		}
	} else if len(req.Observation) != 0 || req.Identity.ObservationDigest != "" {
		return nil, errors.New("observation input is only allowed for verify")
	}
	if req.Checkpoint == nil {
		if req.Identity.CheckpointDigest != "" || req.Identity.CheckpointRevision != 0 || req.Identity.CheckpointFormat != "" {
			return nil, errors.New("phase input checkpoint is missing")
		}
	} else if err := c.validateInputCheckpoint(req.Checkpoint); err != nil {
		return nil, err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > c.limits.MaxRequestBytes {
		return nil, errors.New("phase request exceeds its byte limit")
	}
	if err := validatePhaseJSON(data, c.requestSchema); err != nil {
		return nil, fmt.Errorf("invalid phase request: %w", err)
	}
	return data, nil
}

func (c phaseValidationContext) validateInputCheckpoint(checkpoint *phaseCheckpoint) error {
	id := c.request.Identity
	if checkpoint.Digest != id.CheckpointDigest || checkpoint.Format != id.CheckpointFormat || checkpoint.Revision != id.CheckpointRevision {
		return errors.New("phase input checkpoint differs from the pinned checkpoint identity")
	}
	return c.validateCheckpoint(checkpoint)
}

func (c phaseValidationContext) validateCheckpoint(checkpoint *phaseCheckpoint) error {
	if checkpoint.Format == "" || checkpoint.Revision <= 0 || int64(len(checkpoint.Bytes)) > c.limits.MaxCheckpointBytes ||
		checkpoint.Digest != policy.Hash(checkpoint.Bytes) {
		return errors.New("invalid phase checkpoint identity or byte length")
	}
	if err := validatePhaseJSON(checkpoint.Bytes, c.checkpointSchema); err != nil {
		return fmt.Errorf("invalid phase checkpoint: %w", err)
	}
	return nil
}

func decodePhaseResult(data []byte, c phaseValidationContext) (phaseResult, error) {
	if int64(len(data)) > c.limits.MaxResultBytes {
		return phaseResult{}, errors.New("phase result exceeds its byte limit")
	}
	doc, err := parsePhaseJSON(data)
	if err != nil {
		return phaseResult{}, err
	}
	if c.resultSchema.Validate(doc) != nil {
		return phaseResult{}, errors.New("phase result violates its approved schema")
	}
	if !boundedPhaseCheckpoint(doc, c.limits.MaxCheckpointBytes) {
		return phaseResult{}, errors.New("phase result checkpoint exceeds its byte limit or has invalid encoding")
	}
	var result phaseResult
	if err := jsonv2.Unmarshal(data, &result, jsonv2.RejectUnknownMembers(true)); err != nil {
		return phaseResult{}, errors.New("invalid phase result envelope")
	}
	if result.Identity != c.request.Identity {
		return phaseResult{}, errors.New("phase result does not match the dispatched call")
	}
	if err := c.validateResult(result); err != nil {
		return phaseResult{}, err
	}
	return result, nil
}

func boundedPhaseCheckpoint(doc any, maxBytes int64) bool {
	object, ok := doc.(map[string]any)
	if !ok {
		return false
	}
	checkpoint, found := object["checkpoint"]
	if !found {
		return true
	}
	fields, ok := checkpoint.(map[string]any)
	if !ok {
		return false
	}
	encoded, ok := fields["bytes"].(string)
	if !ok || len(encoded)%4 != 0 || strings.ContainsAny(encoded, "\r\n") {
		return false
	}
	decoded := base64.StdEncoding.DecodedLen(len(encoded))
	return int64(decoded-(len(encoded)-len(strings.TrimRight(encoded, "=")))) <= maxBytes
}

func (c phaseValidationContext) validateResult(result phaseResult) error {
	if !result.Kind.validFor(c.request.Identity.Action) {
		return errors.New("phase result kind is not admitted for the dispatched action")
	}
	if result.Kind != phaseVerified && len(result.Observations) != 0 {
		return errors.New("observations are only allowed on a verified result")
	}
	if result.Checkpoint != nil {
		if result.Kind != phaseCheckpointResult && result.Kind != phasePrepared {
			return errors.New("checkpoint is not allowed for this phase result")
		}
		if err := c.validateCheckpoint(result.Checkpoint); err != nil {
			return err
		}
		if input := c.request.Checkpoint; input != nil &&
			(result.Checkpoint.Format != input.Format || result.Checkpoint.Revision <= input.Revision) {
			return errors.New("phase result checkpoint does not advance the input checkpoint")
		}
	} else if result.Kind == phaseCheckpointResult {
		return errors.New("checkpoint result is missing its checkpoint")
	}
	if result.Kind == phasePrepared {
		if len(result.Observations) != 0 || len(result.Evidence) != 0 {
			return errors.New("observation evidence is not allowed on a prepared result")
		}
		if err := c.validatePrepared(result.Prepared); err != nil {
			return err
		}
		switch result.OSReadiness {
		case "Current", "DesiredStaged", "DesiredBooted":
		default:
			return errors.New("invalid prepared OS readiness")
		}
	} else if result.OSReadiness != "" || result.Prepared != nil {
		return errors.New("preparation evidence is only allowed on a prepared result")
	}
	armed := result.Kind == phaseActivationArmed || result.Kind == phaseRebootRequired
	if armed != (len(result.ActivationReceipt) != 0) {
		return errors.New("invalid activation receipt presence")
	}
	// No phase in this increment can establish the prior effect needed to
	// authorize an armed result or another mutating cycle.
	if armed || result.Kind == phaseMoreWork {
		return errors.New("phase result requires evidence of a prior apply")
	}
	if len(result.Evidence) > 0 && validatePhaseJSON(result.Evidence, c.observation) != nil {
		return errors.New("phase evidence violates the approved observation schema")
	}
	if result.Kind == phaseVerified {
		if len(result.Evidence) == 0 {
			return errors.New("verified result is missing observation evidence")
		}
		return validateVerifiedObservations(result.Observations, c.targets)
	}
	return nil
}

func (c phaseValidationContext) validatePrepared(prepared *phasePreparedEvidence) error {
	if prepared == nil || len(c.targets) == 0 || len(prepared.Artifacts) != len(c.targets) {
		return errors.New("prepared result does not cover every required target")
	}
	remaining := make(map[phaseTargetKey]phaseTarget, len(c.targets))
	for _, target := range c.targets {
		remaining[target.key()] = target
	}
	if len(remaining) != len(c.targets) {
		return errors.New("duplicate target in the approved intent")
	}
	for _, artifact := range prepared.Artifacts {
		target, found := remaining[artifact.key()]
		if !found || !matchesPreparedTarget(artifact, target) {
			return errors.New("prepared artifacts differ from the approved intent or explicit pins")
		}
		delete(remaining, artifact.key())
	}
	digest, err := phaseSelectionDigest(prepared.Artifacts)
	if err != nil {
		return err
	}
	if prepared.SelectionDigest != digest {
		return errors.New("prepared selection digest differs from its artifact manifest")
	}
	if !prepared.Authentication.bindsSelection(digest) || !prepared.Preflight.bindsSelection(digest) {
		return errors.New("prepared authentication and preflight receipts must bind the selected manifest")
	}
	return nil
}

func matchesPreparedTarget(artifact, target phaseTarget) bool {
	return artifact.concrete() &&
		(target.Version == "" || artifact.Version == target.Version) &&
		(target.Digest == "" || artifact.Digest == target.Digest)
}

func (t phaseTarget) concrete() bool {
	return (t.Kind == phaseOverlay || (t.Kind == phaseComponent && t.Version != "")) &&
		t.Name != "" && schemaDigestPattern.MatchString(t.Digest)
}

func (r *phaseSelectionReceipt) bindsSelection(digest string) bool {
	return r != nil && r.SelectionDigest == digest && len(r.Evidence) != 0 && !bytes.Equal(bytes.TrimSpace(r.Evidence), []byte("null"))
}

// Hash the fixed kind/name/version/digest JSON fields after sorting by kind and name.
func phaseSelectionDigest(artifacts []phaseTarget) (string, error) {
	ordered := slices.Clone(artifacts)
	slices.SortFunc(ordered, func(a, b phaseTarget) int {
		if order := cmp.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		return cmp.Compare(a.Name, b.Name)
	})
	data, err := json.Marshal(ordered)
	if err != nil {
		return "", fmt.Errorf("encode selected manifest: %w", err)
	}
	return policy.Hash(data), nil
}

func (kind phaseResultKind) validFor(action phaseAction) bool {
	if kind == phaseFailed {
		return true
	}
	switch action {
	case phasePrepare:
		return kind == phaseCheckpointResult || kind == phasePrepared
	case phaseApply:
		return kind == phaseCheckpointResult || kind == phaseApplied || kind == phaseRebootRequired
	case phaseVerify:
		return kind == phaseVerified || kind == phaseActivationArmed || kind == phaseMoreWork || kind == phasePending || kind == phaseUncertain
	default:
		return false
	}
}

func validateVerifiedObservations(observations []phaseObservation, targets []phaseTarget) error {
	if len(targets) == 0 || len(targets) != len(observations) {
		return errors.New("verification does not cover every required target")
	}
	remaining := make(map[phaseTargetKey]phaseTarget, len(targets))
	for _, target := range targets {
		if !target.concrete() {
			return errors.New("verification requires a concrete selection for every target")
		}
		key := target.key()
		if _, exists := remaining[key]; exists {
			return errors.New("duplicate target in the approved verification plan")
		}
		remaining[key] = target
	}
	for _, observation := range observations {
		key := observation.key()
		expected, found := remaining[key]
		if !found || !observation.Active || observation.phaseTarget != expected {
			return errors.New("verification observations differ from the approved targets")
		}
		delete(remaining, key)
	}
	return nil
}

func validatePhaseJSON(data []byte, schema *jsonschema.Schema) error {
	doc, err := parsePhaseJSON(data)
	if err != nil {
		return err
	}
	if schema.Validate(doc) != nil {
		return errors.New("JSON violates the approved schema")
	}
	return nil
}

func parsePhaseJSON(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("phase JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	const maxPhaseJSONDepth = 64
	doc, err := decodeValue(decoder, 0, maxPhaseJSONDepth, validatePhaseNumber)
	if err != nil {
		return nil, errors.New("invalid phase JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("phase JSON has trailing content")
	}
	return doc, nil
}

// Bound rational arithmetic in the schema validator independently of wire size.
func validatePhaseNumber(number json.Number) error {
	const maxNumberDigits = 256
	const maxExponent = 256
	value := string(number)
	if len(value) > maxNumberDigits {
		return errors.New("phase number exceeds its precision bound")
	}
	if i := strings.IndexAny(value, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(value[i+1:])
		if err != nil || exponent < -maxExponent || exponent > maxExponent {
			return errors.New("phase number exceeds its exponent bound")
		}
	}
	return nil
}
