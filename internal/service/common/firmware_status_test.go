package common

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	currentSchemaDigest    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	historicalSchemaDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// testObservationSchema is a closed observation schema for the test-only
// retained evidence below, shaped like an updater that reports a firmware
// version string and its ESRT entry.
const testObservationSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "dmiVersion": {"type": "string"},
    "esrt": {
      "type": "object",
      "additionalProperties": false,
      "required": ["resourceGuid"],
      "properties": {
        "resourceGuid": {"type": "string", "format": "uuid"},
        "currentVersion": {"type": "string", "pattern": "^[0-9]+$"},
        "lastAttemptStatus": {"type": "integer", "minimum": 0, "maximum": 4294967295}
      }
    }
  }
}`

var testBudgetKeys = []string{"maxArtifactBytes", "maxFmpItems", "operationTimeoutSeconds"}

// testFirmwareStatusJSON is a firmware status whose attempt pins the current
// release and whose observation was produced under the same release.
const testFirmwareStatusJSON = `{
  "support": {"apiVersion": 1, "supported": true, "updaters": []},
  "attempt": {
    "id": "attempt-1",
    "renderedVersion": "7",
    "specHash": "` + testFirmwareSHA + `",
    "retryGeneration": 0,
    "updater": {
      "name": "nvidia-orin", "version": "0.1.0", "buildDigest": "` + testFirmwareSHA + `",
      "protocol": 1, "schemaDigest": "` + currentSchemaDigest + `", "configDigest": "` + testFirmwareSHA + `"
    },
    "phase": "Failed",
    "startedAt": "2026-11-09T10:00:00Z",
    "updatedAt": "2026-11-09T10:05:00Z",
    "changed": false,
    "error": {"reason": "ArtifactUnauthenticated", "message": "the capsule exceeds the parser size budget", "retryable": false, "exceededBudget": "maxArtifactBytes"},
    "recovery": []
  },
  "components": [{
    "name": "system",
    "type": "firmware",
    "managed": true,
    "state": "Failed",
    "observation": {
      "producerIdentity": "nvidia-orin/0.1.0",
      "backend": {
        "updater": "nvidia-orin",
        "schemaDigest": "` + currentSchemaDigest + `",
        "data": {"dmiVersion": "36.4.4-gcid-1", "esrt": {"resourceGuid": "00000000-0000-4000-8000-000000000001", "currentVersion": "2359554", "lastAttemptStatus": 0}}
      }
    },
    "observedAt": "2026-11-09T10:05:00Z",
    "bootID": "boot-1"
  }],
  "observedAt": "2026-11-09T10:05:00Z",
  "bootID": "boot-1"
}`

// schemaEvidenceProvider is a test stub of the approved descriptor provider
// that serves retained schema evidence and records every lookup. Release
// lookups are recorded too, so tests can check that status validation never
// asks for current authorization.
type schemaEvidenceProvider struct {
	evidence       map[string]*RetainedSchemaEvidence
	err            error
	schemaLookups  []string
	releaseLookups []string
}

func newSchemaEvidenceProvider(evidence ...*RetainedSchemaEvidence) *schemaEvidenceProvider {
	p := &schemaEvidenceProvider{evidence: map[string]*RetainedSchemaEvidence{}}
	for _, e := range evidence {
		p.evidence["nvidia-orin@"+e.SchemaDigest] = e
	}
	return p
}

func (p *schemaEvidenceProvider) LookupRelease(_ context.Context, name, version string) (*ApprovedDescriptor, error) {
	p.releaseLookups = append(p.releaseLookups, name+"/"+version)
	return nil, errors.New("status validation must not look up current release authorization")
}

func (p *schemaEvidenceProvider) LookupSchemaByDigest(_ context.Context, updater, schemaDigest string) (*RetainedSchemaEvidence, error) {
	p.schemaLookups = append(p.schemaLookups, updater+"@"+schemaDigest)
	if p.err != nil {
		return nil, p.err
	}
	return p.evidence[updater+"@"+schemaDigest], nil
}

func retainedEvidence(schemaDigest string) *RetainedSchemaEvidence {
	return &RetainedSchemaEvidence{
		SchemaDigest:      schemaDigest,
		ObservationSchema: json.RawMessage(testObservationSchema),
		BudgetKeySet:      append([]string{}, testBudgetKeys...),
	}
}

func newTestFirmwareStatus(t *testing.T) *domain.FirmwareStatus {
	t.Helper()
	var status domain.FirmwareStatus
	require.NoError(t, json.Unmarshal([]byte(testFirmwareStatusJSON), &status))
	return &status
}

func observationData(status *domain.FirmwareStatus, component int) map[string]any {
	return status.Components[component].Observation.Backend.Data
}

func TestValidateFirmwareStatus(t *testing.T) {
	ctx := context.Background()
	current := currentSchemaDigest
	historical := historicalSchemaDigest
	tests := []struct {
		name        string
		mutate      func(status *domain.FirmwareStatus)
		provider    func() *schemaEvidenceProvider
		unavailable bool
		nilStatus   bool
		wantCode    int32
		wantErr     []string
		wantLookups []string
	}{
		{
			name:      "When there is no firmware status it should accept it without lookups",
			nilStatus: true,
			wantCode:  http.StatusOK,
		},
		{
			name: "When the status has neither observations nor budgets it should accept it without lookups even if evidence is unavailable",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt.Error.ExceededBudget = nil
				status.Components[0].Observation = nil
			},
			unavailable: true,
			wantCode:    http.StatusOK,
		},
		{
			name:        "When the observation and the budget use the pinned release it should validate both with one lookup",
			wantCode:    http.StatusOK,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When an observation has no attempt it should validate the observation on its own",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt = nil
			},
			wantCode:    http.StatusOK,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When an observation was produced under an older release it should validate it against that release's retained schema",
			mutate: func(status *domain.FirmwareStatus) {
				status.Components[0].Observation.Backend.SchemaDigest = historical
			},
			provider: func() *schemaEvidenceProvider {
				return newSchemaEvidenceProvider(retainedEvidence(current), retainedEvidence(historical))
			},
			wantCode:    http.StatusOK,
			wantLookups: []string{"nvidia-orin@" + historical, "nvidia-orin@" + current},
		},
		{
			name: "When observation data has a field the retained schema does not define it should reject it",
			mutate: func(status *domain.FirmwareStatus) {
				observationData(status, 0)["flashCount"] = 3
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.components[0].observation.backend.data", "flashCount"},
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When observation data violates a format of the retained schema it should reject it with the field path",
			mutate: func(status *domain.FirmwareStatus) {
				observationData(status, 0)["esrt"].(map[string]any)["resourceGuid"] = "not-a-guid"
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.components[0].observation.backend.data.esrt.resourceGuid"},
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When the observation schema digest is unknown for its updater it should reject it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Components[0].Observation.Backend.SchemaDigest = historical
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.components[0].observation.backend.schemaDigest", "no retained observation schema"},
			wantLookups: []string{"nvidia-orin@" + historical, "nvidia-orin@" + current},
		},
		{
			name: "When the observation names a different updater than the digest belongs to it should reject it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Components[0].Observation.Backend.Updater = "other-updater"
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.components[0].observation.backend.schemaDigest"},
			wantLookups: []string{"other-updater@" + current, "nvidia-orin@" + current},
		},
		{
			name: "When the observation schema digest is missing it should reject it without a lookup",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt.Error.ExceededBudget = nil
				status.Components[0].Observation.Backend.SchemaDigest = ""
			},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"status.firmware.components[0].observation.backend.schemaDigest", "Required value"},
		},
		{
			name: "When the provider returns evidence for another digest it should fail closed",
			provider: func() *schemaEvidenceProvider {
				p := newSchemaEvidenceProvider()
				p.evidence["nvidia-orin@"+current] = retainedEvidence(historical)
				return p
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When the retained observation schema is not a valid schema it should fail closed",
			provider: func() *schemaEvidenceProvider {
				evidence := retainedEvidence(current)
				evidence.ObservationSchema = json.RawMessage(`{"type": 7}`)
				return newSchemaEvidenceProvider(evidence)
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When the retained evidence has no observation schema it should fail closed",
			provider: func() *schemaEvidenceProvider {
				evidence := retainedEvidence(current)
				evidence.ObservationSchema = nil
				return newSchemaEvidenceProvider(evidence)
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When retained schemas reference external documents it should not load them",
			provider: func() *schemaEvidenceProvider {
				evidence := retainedEvidence(current)
				evidence.ObservationSchema = json.RawMessage(`{"$ref": "https://schemas.example.com/observation.json"}`)
				return newSchemaEvidenceProvider(evidence)
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name:        "When retained evidence is unavailable it should reject the observation",
			unavailable: true,
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.components[0].observation.backend", "unavailable"},
		},
		{
			name: "When the evidence lookup fails it should return an internal error",
			provider: func() *schemaEvidenceProvider {
				p := newSchemaEvidenceProvider()
				p.err = errors.New("evidence backend timeout")
				return p
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When the exceeded budget is not a budget of the pinned release it should reject it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt.Error.ExceededBudget = toPtr("maxCapsuleCount")
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.attempt.error.exceededBudget", "maxCapsuleCount"},
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When a component error names a budget of the pinned release it should accept it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Components[0].Error = &domain.FirmwareError{Reason: "ArtifactUnauthenticated", Message: "too many FMP items", ExceededBudget: toPtr("maxFmpItems")}
			},
			wantCode:    http.StatusOK,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When a component error names an unknown budget it should reject it with its own path",
			mutate: func(status *domain.FirmwareStatus) {
				status.Components[0].Error = &domain.FirmwareError{Reason: "ArtifactUnauthenticated", Message: "limit", ExceededBudget: toPtr("unknownBudget")}
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.components[0].error.exceededBudget", "unknownBudget"},
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When an exceeded budget is reported without a pinned updater it should reject it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt.Updater = nil
				status.Components[0].Observation = nil
			},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"status.firmware.attempt.error.exceededBudget", "pinned updater"},
		},
		{
			name: "When a component reports an exceeded budget without any attempt it should reject it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components[0].Observation = nil
				status.Components[0].Error = &domain.FirmwareError{Reason: "ArtifactUnauthenticated", Message: "limit", ExceededBudget: toPtr("maxFmpItems")}
			},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"status.firmware.components[0].error.exceededBudget", "pinned updater"},
		},
		{
			name: "When the pinned release defines no budgets it should reject any exceeded budget",
			provider: func() *schemaEvidenceProvider {
				evidence := retainedEvidence(current)
				evidence.BudgetKeySet = nil
				return newSchemaEvidenceProvider(evidence)
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.attempt.error.exceededBudget"},
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When the pinned release of an attempt with an exceeded budget is unknown it should reject it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt.Updater.SchemaDigest = historical
				status.Components[0].Observation = nil
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.attempt.updater.schemaDigest", "no retained observation schema"},
			wantLookups: []string{"nvidia-orin@" + historical},
		},
		{
			name: "When an error without an exceeded budget is reported it should accept it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt.Error.ExceededBudget = nil
				status.Components[0].Observation = nil
			},
			wantCode: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var status *domain.FirmwareStatus
			if !tt.nilStatus {
				status = newTestFirmwareStatus(t)
				if tt.mutate != nil {
					tt.mutate(status)
				}
			}
			original, err := json.Marshal(status)
			require.NoError(t, err)

			var provider ApprovedDescriptorProvider
			var recorder *schemaEvidenceProvider
			switch {
			case tt.unavailable:
				provider = UnavailableDescriptorProvider{}
			case tt.provider != nil:
				recorder = tt.provider()
				provider = recorder
			default:
				recorder = newSchemaEvidenceProvider(retainedEvidence(current))
				provider = recorder
			}

			result := ValidateFirmwareStatus(ctx, provider, status)

			require.Equal(t, tt.wantCode, result.Code, result.Message)
			for _, want := range tt.wantErr {
				assert.Contains(t, result.Message, want)
			}
			if recorder != nil {
				assert.Equal(t, tt.wantLookups, recorder.schemaLookups)
				assert.Empty(t, recorder.releaseLookups, "status validation must never look up current release authorization")
			}
			after, err := json.Marshal(status)
			require.NoError(t, err)
			assert.JSONEq(t, string(original), string(after), "validation must not modify the status")
		})
	}
}

func toPtr[T any](v T) *T { return &v }

func TestFirmwareStatusEqual(t *testing.T) {
	a := newTestFirmwareStatus(t)
	b := newTestFirmwareStatus(t)
	assert.True(t, FirmwareStatusEqual(a, b))
	assert.True(t, FirmwareStatusEqual(nil, nil))
	assert.False(t, FirmwareStatusEqual(a, nil))
	b.Attempt.Phase = domain.FirmwareAttemptPhaseSucceeded
	assert.False(t, FirmwareStatusEqual(a, b))
}
