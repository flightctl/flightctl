package firmware

import (
	"encoding/json"
	"strings"
	"testing"

	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/stretchr/testify/require"
)

func phaseProtocolFixture(t *testing.T) (*Manifest, *policy.ReleaseDescriptor, phaseRequest) {
	t.Helper()
	objectSchema := phaseSchemaFixture(`{"type":"object"}`)
	requestSchema := phaseSchemaFixture(`{"type":"object","additionalProperties":false,"required":["identity","config"],"properties":{"identity":{"type":"object"},"config":{"type":"string"},"checkpoint":{"type":"object"},"observation":{"type":"string"}}}`)
	configSchema := phaseSchemaFixture(`{"type":"object","required":["value"],"additionalProperties":false,"properties":{"value":{"type":"number"}}}`)
	checkpointSchema := phaseSchemaFixture(`{"type":"object","required":["step"],"additionalProperties":false,"properties":{"step":{"type":"integer"}}}`)
	d := &policy.ReleaseDescriptor{
		Name: "test-updater", Version: "1.0.0", Protocol: SupportedProtocol,
		PackageDigest: policy.Hash([]byte("package")),
		Schemas: policy.SchemaBundle{
			Request: requestSchema, Result: objectSchema, Observation: objectSchema,
			Config: configSchema, Checkpoint: checkpointSchema,
		},
		Limits: policy.ExecutionLimits{
			MaxRequestBytes: 4096, MaxResultBytes: 4096, MaxCheckpointBytes: 64,
			MaxStderrBytes: 256, MaxScratchBytes: 4096, MaxMemoryBytes: 1 << 20,
			MaxProcesses: 4, MaxInvocationSeconds: 5, MaxCycles: 2, MaxReboots: 1, MaxAttempts: 2,
		},
	}
	m := &Manifest{Name: d.Name, Version: d.Version, Protocol: d.Protocol,
		Schemas: ManifestSchemas{
			Config: configSchema.Digest, Checkpoint: checkpointSchema.Digest,
			Observation: objectSchema.Digest, Request: requestSchema.Digest, Result: objectSchema.Digest,
		},
	}
	cfg := []byte(`{"value":1.25}`)
	observation := []byte(`{"boot":"active"}`)
	checkpointBytes := []byte(`{ "step": 1 }`)
	input := &phaseCheckpoint{Format: "test/v1", Revision: 1, Bytes: checkpointBytes, Digest: policy.Hash(checkpointBytes)}
	req := phaseRequest{
		Identity: phaseIdentity{
			Protocol: SupportedProtocol, Action: phaseVerify, AttemptID: "attempt", CallID: "call", Cycle: 1,
			Name: d.Name, Version: d.Version, DescriptorDigest: policy.Hash([]byte("descriptor")),
			PackageDigest: d.PackageDigest, ManifestDigest: policy.Hash([]byte("manifest")),
			ConfigDigest: policy.Hash(cfg), IntentDigest: policy.Hash([]byte("intent")),
			PlanDigest: policy.Hash([]byte("plan")), ObservationDigest: policy.Hash(observation), Schemas: m.Schemas,
			GrantDigest: policy.Hash([]byte("grant")), CheckpointDigest: input.Digest,
			CheckpointFormat: input.Format, CheckpointRevision: input.Revision,
		},
		Config: cfg, Checkpoint: input, Observation: observation,
	}
	return m, d, req
}

func phaseSchemaFixture(data string) policy.SchemaDocument {
	return policy.SchemaDocument{Digest: policy.Hash([]byte(data)), Bytes: data}
}

func TestPhaseProtocolRequest(t *testing.T) {
	t.Run("When inputs match their approved identities it should preserve exact bytes", func(t *testing.T) {
		m, d, req := phaseProtocolFixture(t)
		ctx, err := newPhaseValidationContext(m, d, req, nil)
		require.NoError(t, err)
		data, err := ctx.encodeRequest()
		require.NoError(t, err)
		var received phaseRequest
		require.NoError(t, json.Unmarshal(data, &received))
		require.Equal(t, req.Config, received.Config)
		require.Equal(t, req.Checkpoint.Bytes, received.Checkpoint.Bytes)
		require.Equal(t, req.Observation, received.Observation)
	})

	for _, tc := range []struct {
		name string
		edit func(*Manifest, *policy.ReleaseDescriptor, *phaseRequest)
	}{
		{"When normalized config differs from its digest it should reject", func(_ *Manifest, _ *policy.ReleaseDescriptor, r *phaseRequest) {
			r.Config = []byte(`{"value":3}`)
		}},
		{"When verify observation bytes differ from their call digest it should reject", func(_ *Manifest, _ *policy.ReleaseDescriptor, r *phaseRequest) {
			r.Observation = []byte(`{"boot":"unknown"}`)
		}},
		{"When verify observation input has no digest it should reject", func(_ *Manifest, _ *policy.ReleaseDescriptor, r *phaseRequest) {
			r.Identity.ObservationDigest = ""
		}},
		{"When checkpoint identity is absent it should reject", func(_ *Manifest, _ *policy.ReleaseDescriptor, r *phaseRequest) {
			r.Identity.CheckpointRevision = 0
		}},
		{"When a schema differs from its approved bytes it should reject", func(_ *Manifest, d *policy.ReleaseDescriptor, _ *phaseRequest) {
			d.Schemas.Config.Bytes = `{}`
		}},
		{"When schema requires external loading it should reject", func(m *Manifest, d *policy.ReleaseDescriptor, r *phaseRequest) {
			d.Schemas.Result = phaseSchemaFixture(`{"$ref":"https://example.invalid/schema"}`)
			m.Schemas.Result = d.Schemas.Result.Digest
			r.Identity.Schemas = m.Schemas
		}},
		{"When the request limit cannot hold the envelope it should reject", func(_ *Manifest, d *policy.ReleaseDescriptor, _ *phaseRequest) {
			d.Limits.MaxRequestBytes = 1
		}},
		{"When any descriptor limit is absent it should reject", func(_ *Manifest, d *policy.ReleaseDescriptor, _ *phaseRequest) {
			d.Limits.MaxProcesses = 0
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, d, req := phaseProtocolFixture(t)
			tc.edit(m, d, &req)
			ctx, err := newPhaseValidationContext(m, d, req, nil)
			if err == nil {
				_, err = ctx.encodeRequest()
			}
			require.Error(t, err)
		})
	}
}

func TestPhaseProtocolResult(t *testing.T) {
	selected := policy.Hash([]byte("selected"))
	expected := []phaseTarget{{Kind: phaseComponent, Name: "system", Version: "2.0", Digest: selected},
		{Kind: phaseOverlay, Name: "carrier", Version: "3.0", Digest: selected}}
	for _, tc := range []struct {
		name  string
		edit  func(*phaseResult)
		wire  func([]byte) []byte
		valid bool
	}{
		{name: "When verification matches every expected member it should accept", valid: true},
		{name: "When verification omits schema-bound observation evidence it should reject", edit: func(r *phaseResult) { r.Evidence = nil }},
		{name: "When a numeric exponent is outside the supported validation range it should reject", edit: func(r *phaseResult) {
			r.Evidence = json.RawMessage(`{"number":1e999999999999999999999}`)
		}},
		{name: "When a valid response omits a required overlay it should reject", edit: func(r *phaseResult) { r.Observations = r.Observations[:1] }},
		{name: "When verification repeats a member it should reject", edit: func(r *phaseResult) { r.Observations[1] = r.Observations[0] }},
		{name: "When observed artifact differs from the plan it should reject", edit: func(r *phaseResult) { r.Observations[0].Digest = policy.Hash(nil) }},
		{name: "When a call identity differs it should reject", edit: func(r *phaseResult) { r.Identity.CallID = "previous" }},
		{name: "When a result is for a different action it should reject", edit: func(r *phaseResult) { r.Kind = phasePrepared }},
		{name: "When a Pending result carries sparse observations it should reject before durable expansion", edit: func(r *phaseResult) {
			r.Kind = phasePending
			r.Observations = []phaseObservation{{}}
		}},
		{name: "When an object contains repeated keys it should reject", wire: func(data []byte) []byte { return append([]byte(`{"kind":"Pending",`), data[1:]...) }},
		{name: "When a case-insensitive alias could override the result kind it should reject", wire: func(data []byte) []byte {
			return append(data[:len(data)-1], []byte(`,"Kind":"Pending"}`)...)
		}},
		{name: "When MoreWork lacks trusted prior-effect evidence it should reject", edit: func(r *phaseResult) { r.Kind = phaseMoreWork }},
		{name: "When an activation receipt has no prior authorization to correlate it should reject", edit: func(r *phaseResult) {
			r.Kind = phaseActivationArmed
			r.ActivationReceipt = json.RawMessage(`{}`)
		}},
		{name: "When multiple JSON values arrive it should reject", wire: func(data []byte) []byte { return append(data, []byte(`{}`)...) }},
		{name: "When trailing whitespace exceeds the result bound it should reject", wire: func(data []byte) []byte { return append(data, strings.Repeat(" ", 4096)...) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, d, req := phaseProtocolFixture(t)
			ctx, err := newPhaseValidationContext(m, d, req, expected)
			require.NoError(t, err)
			r := phaseResult{Identity: req.Identity, Kind: phaseVerified, Evidence: json.RawMessage(`{}`)}
			for _, target := range expected {
				r.Observations = append(r.Observations, phaseObservation{phaseTarget: target, Active: true})
			}
			if tc.edit != nil {
				tc.edit(&r)
			}
			data, err := json.Marshal(r)
			require.NoError(t, err)
			if tc.wire != nil {
				data = tc.wire(data)
			}
			got, err := decodePhaseResult(data, ctx)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, r.Kind, got.Kind)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestPhaseProtocolVerifyRejectsUnpinnedSelection(t *testing.T) {
	m, d, req := phaseProtocolFixture(t)
	target := phaseTarget{Kind: phaseComponent, Name: "system", Version: "2.0"}
	ctx, err := newPhaseValidationContext(m, d, req, []phaseTarget{target})
	require.NoError(t, err)
	data, err := json.Marshal(phaseResult{
		Identity: req.Identity, Kind: phaseVerified, Evidence: json.RawMessage(`{}`),
		Observations: []phaseObservation{{phaseTarget: target, Active: true}},
	})
	require.NoError(t, err)
	_, err = decodePhaseResult(data, ctx)
	require.ErrorContains(t, err, "concrete selection")
}

func TestPhaseProtocolPrepare(t *testing.T) {
	m, d, req := phaseProtocolFixture(t)
	req.Identity.Action = phasePrepare
	req.Identity.PlanDigest = ""
	req.Identity.ObservationDigest = ""
	req.Observation = nil
	d.Schemas.Result = phaseSchemaFixture(`{
		"type":"object","additionalProperties":false,
		"required":["identity","kind","osReadiness","prepared"],
		"properties":{
			"identity":{"type":"object"},"kind":{"const":"Prepared"},"osReadiness":{"type":"string"},
			"checkpoint":{"type":"object"},
			"prepared":{"type":"object","additionalProperties":false,
				"required":["artifacts","selectionDigest","authentication","preflight"],
				"properties":{
					"artifacts":{"type":"array","minItems":1,"items":{
						"type":"object","additionalProperties":false,"required":["kind","name","version","digest"],
						"properties":{"kind":{"type":"string"},"name":{"type":"string"},"version":{"type":"string"},"digest":{"type":"string"}}
					}},
					"selectionDigest":{"type":"string"},
					"authentication":{"$ref":"#/$defs/receipt"},
					"preflight":{"$ref":"#/$defs/receipt"}
				}
			}
		},
		"$defs":{"receipt":{"type":"object","additionalProperties":false,
			"required":["selectionDigest","evidence"],
			"properties":{"selectionDigest":{"type":"string"},"evidence":{
				"type":"object","additionalProperties":false,"required":["passed"],"properties":{"passed":{"const":true}}
			}}
		}}
	}`)
	m.Schemas.Result = d.Schemas.Result.Digest
	req.Identity.Schemas = m.Schemas
	system := phaseTarget{Kind: phaseComponent, Name: "system", Version: "2.0", Digest: policy.Hash([]byte("image"))}
	overlay := phaseTarget{Kind: phaseOverlay, Name: "board", Digest: policy.Hash([]byte("overlay"))}

	for _, tc := range []struct {
		name string
		pin  string
		edit func(*phaseResult)
		ok   bool
	}{
		{name: "When intent has no digest pins it should accept new selection", ok: true},
		{name: "When selection matches an explicit pin it should accept", pin: system.Digest, ok: true},
		{name: "When selection conflicts with an explicit pin it should reject", pin: policy.Hash(nil)},
		{name: "When the echoed identity substitutes the selection digest it should reject", edit: func(r *phaseResult) {
			r.Identity.PlanDigest = r.Prepared.SelectionDigest
		}},
		{name: "When a required member is missing it should reject", edit: func(r *phaseResult) {
			r.Prepared.Artifacts = r.Prepared.Artifacts[:1]
		}},
		{name: "When a required member is repeated it should reject", edit: func(r *phaseResult) {
			r.Prepared.Artifacts[1] = r.Prepared.Artifacts[0]
		}},
		{name: "When the selected digest is not concrete it should reject", edit: func(r *phaseResult) {
			r.Prepared.Artifacts[0].Digest = ""
		}},
		{name: "When manifest bytes differ from the claimed selection it should reject", edit: func(r *phaseResult) {
			r.Prepared.Artifacts[0].Digest = policy.Hash(nil)
		}},
		{name: "When authentication is missing it should reject", edit: func(r *phaseResult) { r.Prepared.Authentication = nil }},
		{name: "When preflight is missing it should reject", edit: func(r *phaseResult) { r.Prepared.Preflight = nil }},
		{name: "When authentication covers another selection it should reject", edit: func(r *phaseResult) {
			r.Prepared.Authentication.SelectionDigest = policy.Hash(nil)
		}},
		{name: "When preflight covers another selection it should reject", edit: func(r *phaseResult) {
			r.Prepared.Preflight.SelectionDigest = policy.Hash(nil)
		}},
		{name: "When receipt evidence violates the pinned Result schema it should reject", edit: func(r *phaseResult) {
			r.Prepared.Authentication.Evidence = json.RawMessage(`{"passed":false}`)
		}},
		{name: "When prepared readiness is an arbitrary operation it should reject", edit: func(r *phaseResult) {
			r.OSReadiness = "custom"
		}},
		{name: "When checkpoint repeats the previous revision it should reject", edit: func(r *phaseResult) {
			r.Checkpoint = req.Checkpoint
		}},
		{name: "When checkpoint advances but exceeds its own limit it should reject", edit: func(r *phaseResult) {
			data := []byte(`{"step":2}` + strings.Repeat(" ", 64))
			r.Checkpoint = &phaseCheckpoint{Format: req.Checkpoint.Format, Revision: 2, Digest: policy.Hash(data), Bytes: data}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := newPhaseValidationContext(m, d, req, []phaseTarget{
				{Kind: system.Kind, Name: system.Name, Digest: tc.pin}, {Kind: overlay.Kind, Name: overlay.Name},
			})
			require.NoError(t, err)
			// The helper hashes the manifest sorted by kind and name.
			canonical, err := json.Marshal([]phaseTarget{system, overlay})
			require.NoError(t, err)
			digest := policy.Hash(canonical)
			result := phaseResult{
				Identity: req.Identity, Kind: phasePrepared, OSReadiness: "Current",
				Prepared: &phasePreparedEvidence{
					Artifacts: []phaseTarget{overlay, system}, SelectionDigest: digest,
					Authentication: &phaseSelectionReceipt{SelectionDigest: digest, Evidence: json.RawMessage(`{"passed":true}`)},
					Preflight:      &phaseSelectionReceipt{SelectionDigest: digest, Evidence: json.RawMessage(`{"passed":true}`)},
				},
			}
			if tc.edit != nil {
				tc.edit(&result)
			}
			data, err := json.Marshal(result)
			require.NoError(t, err)
			actual, err := decodePhaseResult(data, ctx)
			if tc.ok {
				require.NoError(t, err)
				require.Equal(t, phasePrepared, actual.Kind)
				require.Equal(t, req.Identity, actual.Identity)
				require.Equal(t, digest, actual.Prepared.SelectionDigest)
			} else {
				require.Error(t, err)
			}
		})
	}
}
