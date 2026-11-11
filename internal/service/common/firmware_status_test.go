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
    "error": {"reason": "ResourceLimitExceeded", "message": "the capsule exceeds the parser size budget", "retryable": false, "exceededBudget": "maxArtifactBytes"},
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
				status.Attempt.Error = nil
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
				status.Attempt.Error = nil
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
				status.Components[0].Error = &domain.FirmwareError{Reason: domain.FirmwareErrorReasonResourceLimitExceeded, Message: "too many FMP items", ExceededBudget: toPtr("maxFmpItems")}
			},
			wantCode:    http.StatusOK,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When a component error names an unknown budget it should reject it with its own path",
			mutate: func(status *domain.FirmwareStatus) {
				status.Components[0].Error = &domain.FirmwareError{Reason: domain.FirmwareErrorReasonResourceLimitExceeded, Message: "limit", ExceededBudget: toPtr("unknownBudget")}
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
			name: "When a component without an attempt reports an exceeded budget and has no observation it should reject it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components[0].Observation = nil
				status.Components[0].Error = &domain.FirmwareError{Reason: domain.FirmwareErrorReasonResourceLimitExceeded, Message: "limit", ExceededBudget: toPtr("maxFmpItems")}
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
			name: "When an error with another reason and no exceeded budget is reported it should accept it",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt.Error.Reason = "ArtifactUnauthenticated"
				status.Attempt.Error.ExceededBudget = nil
				status.Components[0].Observation = nil
			},
			wantCode: http.StatusOK,
		},
		{
			name: "When an error with another reason names an exceeded budget it should reject it before any lookup",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt.Error.Reason = "ArtifactUnauthenticated"
				status.Components[0].Observation = nil
			},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"status.firmware.attempt.error.exceededBudget", "Forbidden"},
		},
		{
			name: "When a limit error names no exceeded budget it should reject it before any lookup",
			mutate: func(status *domain.FirmwareStatus) {
				status.Attempt.Error.ExceededBudget = nil
			},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"status.firmware.attempt.error.exceededBudget", "Required value"},
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

func firmwareAttempt(id string, phase domain.FirmwareAttemptPhase) *domain.FirmwareStatus {
	return &domain.FirmwareStatus{
		Support: domain.FirmwareSupportStatus{ApiVersion: 1},
		Attempt: &domain.FirmwareAttemptStatus{Id: id, Phase: phase, Recovery: []domain.FirmwareRecoveryAction{}},
	}
}

func TestCheckFirmwareAttemptRetained(t *testing.T) {
	const erasure = "status.firmware.attempt: cannot erase an outstanding attempt"
	withoutAttempt := &domain.FirmwareStatus{Support: domain.FirmwareSupportStatus{ApiVersion: 1}}
	tests := []struct {
		name      string
		stored    *domain.FirmwareStatus
		candidate *domain.FirmwareStatus
		wantErr   bool
	}{
		{name: "When nothing is stored it should accept any status", candidate: nil},
		{name: "When the stored status has no attempt it should accept a status without one", stored: withoutAttempt, candidate: nil},
		{name: "When the stored attempt is terminal it should accept removing the firmware status", stored: firmwareAttempt("a1", domain.FirmwareAttemptPhaseSucceeded), candidate: nil},
		{name: "When the stored attempt is terminal it should accept a status without the attempt", stored: firmwareAttempt("a1", domain.FirmwareAttemptPhaseFailed), candidate: withoutAttempt},
		{name: "When an outstanding attempt is stored it should reject removing the firmware status", stored: firmwareAttempt("a1", domain.FirmwareAttemptPhaseStaging), candidate: nil, wantErr: true},
		{name: "When an outstanding attempt is stored it should reject a firmware status without the attempt", stored: firmwareAttempt("a1", domain.FirmwareAttemptPhaseAwaitingReboot), candidate: withoutAttempt, wantErr: true},
		{name: "When an outstanding attempt is stored it should reject a different attempt", stored: firmwareAttempt("a1", domain.FirmwareAttemptPhasePreparing), candidate: firmwareAttempt("a2", domain.FirmwareAttemptPhasePreparing), wantErr: true},
		{name: "When an outstanding attempt is stored it should accept progress of the same attempt", stored: firmwareAttempt("a1", domain.FirmwareAttemptPhaseStaging), candidate: firmwareAttempt("a1", domain.FirmwareAttemptPhaseVerifying)},
		{name: "When an outstanding attempt is stored it should accept the same attempt becoming terminal", stored: firmwareAttempt("a1", domain.FirmwareAttemptPhaseVerifying), candidate: firmwareAttempt("a1", domain.FirmwareAttemptPhaseCanceled)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckFirmwareAttemptRetained(tt.stored, tt.candidate)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, erasure, err.Error())
		})
	}
}

func TestClassifyFirmwareStatusPatch(t *testing.T) {
	device := domain.Device{
		ApiVersion: domain.DeviceAPIVersion,
		Kind:       domain.DeviceKind,
		Metadata:   domain.ObjectMeta{Name: toPtr("device-1")},
		Status:     &domain.DeviceStatus{Firmware: newTestFirmwareStatus(t)},
	}
	withoutFirmware := device
	withoutFirmware.Status = &domain.DeviceStatus{}
	statusWithoutFirmware := map[string]any{"conditions": []any{}}

	tests := []struct {
		name    string
		current domain.Device
		patch   domain.PatchRequest
		want    FirmwareStatusPatchOutcome
		wantErr []string
	}{
		{
			name:    "When the request does not touch the firmware status it should keep it",
			current: device,
			patch:   domain.PatchRequest{{Op: "add", Path: "/status/conditions", Value: []any{}}},
			want:    FirmwareStatusKept,
		},
		{
			name:    "When the request changes the firmware status it should keep it for validation",
			current: device,
			patch:   domain.PatchRequest{{Op: "replace", Path: "/status/firmware/attempt/phase", Value: "Succeeded"}},
			want:    FirmwareStatusKept,
		},
		{
			name:    "When the request removes the attempt it should keep the firmware status without it",
			current: device,
			patch:   domain.PatchRequest{{Op: "remove", Path: "/status/firmware/attempt"}},
			want:    FirmwareStatusKept,
		},
		{
			name:    "When the request removes the firmware status it should report an explicit removal",
			current: device,
			patch:   domain.PatchRequest{{Op: "remove", Path: "/status/firmware"}},
			want:    FirmwareStatusRemoved,
		},
		{
			name:    "When the request sets the firmware status to null it should report an explicit removal",
			current: device,
			patch:   domain.PatchRequest{{Op: "replace", Path: "/status/firmware", Value: nil}},
			want:    FirmwareStatusRemoved,
		},
		{
			name:    "When the request removes the whole status it should report an explicit removal",
			current: device,
			patch:   domain.PatchRequest{{Op: "remove", Path: "/status"}},
			want:    FirmwareStatusRemoved,
		},
		{
			name:    "When the request replaces the status with an explicit null firmware it should report an explicit removal",
			current: device,
			patch:   domain.PatchRequest{{Op: "replace", Path: "/status", Value: map[string]any{"conditions": []any{}, "firmware": nil}}},
			want:    FirmwareStatusRemoved,
		},
		{
			name:    "When the request replaces the status without a firmware status it should report an omission",
			current: device,
			patch:   domain.PatchRequest{{Op: "replace", Path: "/status", Value: statusWithoutFirmware}},
			want:    FirmwareStatusOmitted,
		},
		{
			name:    "When nothing is stored and the request does not touch the firmware status it should keep the absent status",
			current: withoutFirmware,
			patch:   domain.PatchRequest{{Op: "add", Path: "/status/conditions", Value: []any{}}},
			want:    FirmwareStatusKept,
		},
		{
			name:    "When the request adds observation data keys it should leave them to the retained schema",
			current: device,
			patch:   domain.PatchRequest{{Op: "add", Path: "/status/firmware/components/0/observation/backend/data/flashCount", Value: 3}},
			want:    FirmwareStatusKept,
		},
		{
			name:    "When the request adds an unknown firmware status field it should reject it",
			current: device,
			patch:   domain.PatchRequest{{Op: "add", Path: "/status/firmware/flashNow", Value: true}},
			wantErr: []string{"status.firmware", `"flashNow"`},
		},
		{
			name:    "When the request adds a field that differs from an attempt field only in case it should reject it",
			current: device,
			patch:   domain.PatchRequest{{Op: "add", Path: "/status/firmware/attempt/Id", Value: "attempt-2"}},
			wantErr: []string{"status.firmware.attempt", `"Id"`},
		},
		{
			name:    "When the patch cannot be applied it should report the error",
			current: withoutFirmware,
			patch:   domain.PatchRequest{{Op: "replace", Path: "/status/firmware/attempt/phase", Value: "Failed"}},
			wantErr: []string{"failed to apply JSON patch"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClassifyFirmwareStatusPatch(&tt.current, tt.patch)
			if len(tt.wantErr) > 0 {
				require.Error(t, err)
				for _, want := range tt.wantErr {
					assert.Contains(t, err.Error(), want)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFirmwareStatusEqual(t *testing.T) {
	a := newTestFirmwareStatus(t)
	b := newTestFirmwareStatus(t)
	assert.True(t, FirmwareStatusEqual(a, b))
	assert.True(t, FirmwareStatusEqual(nil, nil))
	assert.False(t, FirmwareStatusEqual(a, nil))
	b.Attempt.Phase = domain.FirmwareAttemptPhaseSucceeded
	assert.False(t, FirmwareStatusEqual(a, b))
}

// evidenceWithKeys returns retained evidence for a schema digest whose release
// defines exactly the given budget keys.
func evidenceWithKeys(schemaDigest string, keys ...string) *RetainedSchemaEvidence {
	evidence := retainedEvidence(schemaDigest)
	evidence.BudgetKeySet = keys
	return evidence
}

// componentWithObservation returns a component status whose observation was
// produced under the given schema digest.
func componentWithObservation(t *testing.T, name, schemaDigest string) domain.FirmwareComponentStatus {
	t.Helper()
	component := newTestFirmwareStatus(t).Components[0]
	component.Name = name
	component.Observation.Backend.SchemaDigest = schemaDigest
	return component
}

func limitError(key string) *domain.FirmwareError {
	return &domain.FirmwareError{Reason: domain.FirmwareErrorReasonResourceLimitExceeded, Message: "a resource budget was exceeded", ExceededBudget: toPtr(key)}
}

func TestValidateFirmwareStatusBudgetAuthority(t *testing.T) {
	ctx := context.Background()
	current := currentSchemaDigest
	historical := historicalSchemaDigest
	// The pinned and desired release defines maxArtifactBytes; the older
	// release that produced historical observations defines maxFmpItems.
	provider := func() *schemaEvidenceProvider {
		return newSchemaEvidenceProvider(evidenceWithKeys(current, "maxArtifactBytes"), evidenceWithKeys(historical, "maxFmpItems"))
	}
	unpinned := func(status *domain.FirmwareStatus) {
		status.Attempt.Updater = nil
		status.Attempt.Error = nil
	}
	tests := []struct {
		name        string
		mutate      func(t *testing.T, status *domain.FirmwareStatus)
		provider    func() *schemaEvidenceProvider
		wantCode    int32
		wantErr     []string
		wantLookups []string
	}{
		{
			name: "When a component without an attempt names a budget of its own historical observation it should accept it",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components = []domain.FirmwareComponentStatus{componentWithObservation(t, "system", historical)}
				status.Components[0].Error = limitError("maxFmpItems")
			},
			wantCode:    http.StatusOK,
			wantLookups: []string{"nvidia-orin@" + historical},
		},
		{
			name: "When the attempt has no pinned updater a component should fall back to its own observation",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				unpinned(status)
				status.Components = []domain.FirmwareComponentStatus{componentWithObservation(t, "system", historical)}
				status.Components[0].Error = limitError("maxFmpItems")
			},
			wantCode:    http.StatusOK,
			wantLookups: []string{"nvidia-orin@" + historical},
		},
		{
			name: "When a component without an attempt names a budget that only the current release defines it should reject it",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components = []domain.FirmwareComponentStatus{componentWithObservation(t, "system", historical)}
				status.Components[0].Error = limitError("maxArtifactBytes")
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{`status.firmware.components[0].error.exceededBudget: Unsupported value: "maxArtifactBytes"`},
			wantLookups: []string{"nvidia-orin@" + historical},
		},
		{
			name: "When a component without an attempt has an observation without backend data it should reject its budget",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components[0].Observation.Backend = nil
				status.Components[0].Error = limitError("maxArtifactBytes")
			},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"status.firmware.components[0].error.exceededBudget: Forbidden", "own observation"},
		},
		{
			name: "When a component without an attempt has an observation without a schema digest it should reject its budget",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components[0].Observation.Backend.SchemaDigest = ""
				status.Components[0].Error = limitError("maxArtifactBytes")
			},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"status.firmware.components[0].observation.backend.schemaDigest: Required value", "status.firmware.components[0].error.exceededBudget: Forbidden", "own observation"},
		},
		{
			name: "When the own observation of a component without an attempt cannot be resolved it should reject its budget",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components[0].Observation.Backend.SchemaDigest = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
				status.Components[0].Error = limitError("maxArtifactBytes")
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.components[0].observation.backend.schemaDigest", "status.firmware.components[0].error.exceededBudget: Forbidden", "own observation"},
			wantLookups: []string{"nvidia-orin@sha256:3333333333333333333333333333333333333333333333333333333333333333"},
		},
		{
			name: "When the own evidence of a component without an attempt describes another digest it should fail closed",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components[0].Error = limitError("maxArtifactBytes")
			},
			provider: func() *schemaEvidenceProvider {
				p := newSchemaEvidenceProvider()
				p.evidence["nvidia-orin@"+current] = evidenceWithKeys(historical, "maxArtifactBytes")
				return p
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When components without an attempt have different observations each should use its own budget keys",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components = []domain.FirmwareComponentStatus{componentWithObservation(t, "system", historical), componentWithObservation(t, "camera", current)}
				status.Components[0].Error = limitError("maxArtifactBytes")
				status.Components[1].Error = limitError("maxArtifactBytes")
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{`status.firmware.components[0].error.exceededBudget: Unsupported value: "maxArtifactBytes"`},
			wantLookups: []string{"nvidia-orin@" + historical, "nvidia-orin@" + current},
		},
		{
			name: "When components without an attempt share a name their budgets should use their own observations",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components = []domain.FirmwareComponentStatus{componentWithObservation(t, "system", current), componentWithObservation(t, "system", historical)}
				status.Components[1].Error = limitError("maxArtifactBytes")
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{`status.firmware.components[1].error.exceededBudget: Unsupported value: "maxArtifactBytes"`},
			wantLookups: []string{"nvidia-orin@" + current, "nvidia-orin@" + historical},
		},
		{
			name: "When the attempt has a pinned updater a component budget should use the pinned release even if its observation differs",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt.Error = nil
				status.Components = []domain.FirmwareComponentStatus{componentWithObservation(t, "system", historical)}
				status.Components[0].Error = limitError("maxFmpItems")
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{`status.firmware.components[0].error.exceededBudget: Unsupported value: "maxFmpItems"`},
			wantLookups: []string{"nvidia-orin@" + historical, "nvidia-orin@" + current},
		},
		{
			name: "When the attempt has a pinned updater a component budget of the pinned release should be accepted even if its observation differs",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt.Error = nil
				status.Components = []domain.FirmwareComponentStatus{componentWithObservation(t, "system", historical)}
				status.Components[0].Error = limitError("maxArtifactBytes")
			},
			wantCode:    http.StatusOK,
			wantLookups: []string{"nvidia-orin@" + historical, "nvidia-orin@" + current},
		},
		{
			name: "When an attempt without a pinned updater names a budget it should not use a component's observation",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt.Updater = nil
				status.Attempt.Error = limitError("maxArtifactBytes")
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"status.firmware.attempt.error.exceededBudget: Forbidden"},
			wantLookups: []string{"nvidia-orin@" + current},
		},
		{
			name: "When a component without an attempt reports a valid observation and no error it should accept it",
			mutate: func(t *testing.T, status *domain.FirmwareStatus) {
				status.Attempt = nil
				status.Components = []domain.FirmwareComponentStatus{componentWithObservation(t, "system", historical)}
			},
			wantCode:    http.StatusOK,
			wantLookups: []string{"nvidia-orin@" + historical},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := newTestFirmwareStatus(t)
			tt.mutate(t, status)
			recorder := provider()
			if tt.provider != nil {
				recorder = tt.provider()
			}

			result := ValidateFirmwareStatus(ctx, recorder, status)

			require.Equal(t, tt.wantCode, result.Code, result.Message)
			for _, want := range tt.wantErr {
				assert.Contains(t, result.Message, want)
			}
			assert.Equal(t, tt.wantLookups, recorder.schemaLookups)
			assert.Empty(t, recorder.releaseLookups, "status validation must never look up current release authorization")
		})
	}
}
