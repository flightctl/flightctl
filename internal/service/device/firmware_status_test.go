package device

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	statusSchemaDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	statusDigest       = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	attemptErasure     = "status.firmware.attempt: cannot erase an outstanding attempt"
)

const statusObservationSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "dmiVersion": {"type": "string"},
    "esrt": {"type": "object", "additionalProperties": false, "required": ["resourceGuid"],
      "properties": {"resourceGuid": {"type": "string", "format": "uuid"}, "lastAttemptStatus": {"type": "integer", "minimum": 0}}}
  }
}`

// outstandingFirmwareStatusJSON reports an attempt that is still staging and
// an observation produced under the release the attempt pinned.
const outstandingFirmwareStatusJSON = `{
  "support": {"apiVersion": 1, "supported": true, "updaters": []},
  "attempt": {
    "id": "attempt-1", "renderedVersion": "7", "specHash": "` + statusDigest + `", "retryGeneration": 0,
    "updater": {"name": "nvidia-orin", "version": "0.1.0", "buildDigest": "` + statusDigest + `", "protocol": 1,
      "schemaDigest": "` + statusSchemaDigest + `", "configDigest": "` + statusDigest + `"},
    "phase": "Staging", "startedAt": "2026-11-09T10:00:00Z", "updatedAt": "2026-11-09T10:05:00Z", "changed": false, "recovery": []
  },
  "components": [{
    "name": "system", "type": "firmware", "managed": true, "state": "Pending",
    "observation": {"backend": {"updater": "nvidia-orin", "schemaDigest": "` + statusSchemaDigest + `",
      "data": {"dmiVersion": "36.4.4", "esrt": {"resourceGuid": "00000000-0000-4000-8000-000000000001", "lastAttemptStatus": 0}}}},
    "observedAt": "2026-11-09T10:05:00Z", "bootID": "boot-1"
  }],
  "observedAt": "2026-11-09T10:05:00Z",
  "bootID": "boot-1"
}`

// retainedStatusEvidence is a test stub of the approved descriptor provider
// that serves the retained schema evidence of one release and records every
// lookup.
type retainedStatusEvidence struct {
	evidence       *common.RetainedSchemaEvidence
	schemaLookups  []string
	releaseLookups []string
}

func (p *retainedStatusEvidence) LookupRelease(_ context.Context, name, version string) (*common.ApprovedDescriptor, error) {
	p.releaseLookups = append(p.releaseLookups, name+"/"+version)
	return nil, errors.New("status writes must not look up current release authorization")
}

func (p *retainedStatusEvidence) LookupSchemaByDigest(_ context.Context, updater, schemaDigest string) (*common.RetainedSchemaEvidence, error) {
	p.schemaLookups = append(p.schemaLookups, updater+"@"+schemaDigest)
	if updater == "nvidia-orin" && schemaDigest == p.evidence.SchemaDigest {
		return p.evidence, nil
	}
	return nil, nil
}

func outstandingFirmwareStatus(t *testing.T) *domain.FirmwareStatus {
	t.Helper()
	var status domain.FirmwareStatus
	require.NoError(t, json.Unmarshal([]byte(outstandingFirmwareStatusJSON), &status))
	return &status
}

type statusFixture struct {
	store    *fakeStore
	events   *fakeEvents
	svc      Service
	evidence *retainedStatusEvidence
	orgId    uuid.UUID
}

func newStatusFixture(t *testing.T, stored *domain.FirmwareStatus, opts ...HandlerOption) *statusFixture {
	t.Helper()
	f := &statusFixture{
		store:  newFakeStore(),
		events: &fakeEvents{},
		evidence: &retainedStatusEvidence{evidence: &common.RetainedSchemaEvidence{
			SchemaDigest: statusSchemaDigest, ObservationSchema: json.RawMessage(statusObservationSchema), BudgetKeySet: []string{"maxArtifactBytes"},
		}},
		orgId: uuid.New(),
	}
	handlerOpts := append([]HandlerOption{WithApprovedDescriptorProvider(f.evidence)}, opts...)
	f.svc = NewDeviceServiceHandler(f.store.device, f.store.catalog, f.store.fleet, f.events, nil, "agent.example.com", logrus.New(), handlerOpts...)
	status := domain.NewDeviceStatus()
	status.Firmware = stored
	_, err := f.store.device.Create(context.Background(), f.orgId, &domain.Device{
		ApiVersion: domain.DeviceAPIVersion, Kind: domain.DeviceKind,
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo"), ResourceVersion: lo.ToPtr("1")},
		Spec:     &domain.DeviceSpec{},
		Status:   &status,
	}, nil)
	require.NoError(t, err)
	return f
}

func (f *statusFixture) stored(t *testing.T) *domain.Device {
	t.Helper()
	device, err := f.store.device.get("foo")
	require.NoError(t, err)
	return device
}

// statusWrite is a device status document as a client sends it.
func statusWrite(firmware *domain.FirmwareStatus) domain.Device {
	status := domain.NewDeviceStatus()
	status.Firmware = firmware
	status.Os.Image = "quay.io/example/os:reported"
	return domain.Device{
		ApiVersion: domain.DeviceAPIVersion, Kind: domain.DeviceKind,
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("foo")},
		Status:   &status,
	}
}

func TestReplaceDeviceStatusFirmware(t *testing.T) {
	ctx := context.Background()
	for _, agent := range []bool{false, true} {
		route := "user"
		if agent {
			route = "agent"
		}
		t.Run("When the "+route+" route reports a valid firmware status it should validate it against its retained schema and store it", func(t *testing.T) {
			f := newStatusFixture(t, nil)
			_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(outstandingFirmwareStatus(t)), agent)

			require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
			assert.Equal(t, []string{"nvidia-orin@" + statusSchemaDigest}, f.evidence.schemaLookups)
			assert.Empty(t, f.evidence.releaseLookups)
			require.NotNil(t, f.stored(t).Status.Firmware)
			assert.Equal(t, "attempt-1", f.stored(t).Status.Firmware.Attempt.Id)
		})

		t.Run("When the "+route+" route would erase an outstanding attempt it should reject the write and keep the stored status", func(t *testing.T) {
			f := newStatusFixture(t, outstandingFirmwareStatus(t))
			before := f.stored(t)
			firmware := outstandingFirmwareStatus(t)
			firmware.Attempt = nil

			_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), agent)

			require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
			assert.Equal(t, attemptErasure, status.Message)
			assert.Equal(t, before.Status, f.stored(t).Status)
		})
	}

	t.Run("When observation data has an unknown field it should reject the write and keep the stored status", func(t *testing.T) {
		f := newStatusFixture(t, nil)
		before := f.stored(t)
		firmware := outstandingFirmwareStatus(t)
		firmware.Components[0].Observation.Backend.Data["flashCount"] = 3

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), false)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "status.firmware.components[0].observation.backend.data")
		assert.Contains(t, status.Message, "flashCount")
		assert.Equal(t, before.Status, f.stored(t).Status)
	})

	t.Run("When the write omits the firmware status it should keep the stored firmware status without lookups", func(t *testing.T) {
		f := newStatusFixture(t, outstandingFirmwareStatus(t))
		before := f.stored(t)

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(nil), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		after := f.stored(t)
		assert.Equal(t, before.Status.Firmware, after.Status.Firmware)
		assert.Equal(t, "quay.io/example/os:reported", after.Status.Os.Image)
		assert.Empty(t, f.evidence.schemaLookups)
	})

	t.Run("When a different attempt replaces an outstanding one it should reject the write", func(t *testing.T) {
		f := newStatusFixture(t, outstandingFirmwareStatus(t))
		before := f.stored(t)
		firmware := outstandingFirmwareStatus(t)
		firmware.Attempt.Id = "attempt-2"

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Equal(t, attemptErasure, status.Message)
		assert.Equal(t, before.Status, f.stored(t).Status)
	})

	t.Run("When the outstanding attempt progresses to a terminal phase it should accept the write", func(t *testing.T) {
		f := newStatusFixture(t, outstandingFirmwareStatus(t))
		firmware := outstandingFirmwareStatus(t)
		firmware.Attempt.Phase = domain.FirmwareAttemptPhaseSucceeded

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, domain.FirmwareAttemptPhaseSucceeded, f.stored(t).Status.Firmware.Attempt.Phase)
	})

	t.Run("When only a terminal attempt is stored it should accept a firmware status without it", func(t *testing.T) {
		stored := outstandingFirmwareStatus(t)
		stored.Attempt.Phase = domain.FirmwareAttemptPhaseFailed
		f := newStatusFixture(t, stored)
		firmware := outstandingFirmwareStatus(t)
		firmware.Attempt = nil

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Nil(t, f.stored(t).Status.Firmware.Attempt)
	})

	t.Run("When the reported firmware status is unchanged it should keep it without needing retained evidence", func(t *testing.T) {
		f := newStatusFixture(t, outstandingFirmwareStatus(t), WithApprovedDescriptorProvider(common.UnavailableDescriptorProvider{}))

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(outstandingFirmwareStatus(t)), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, "attempt-1", f.stored(t).Status.Firmware.Attempt.Id)
	})

	t.Run("When a changed observation cannot be validated because evidence is unavailable it should reject the write", func(t *testing.T) {
		f := newStatusFixture(t, nil, WithApprovedDescriptorProvider(common.UnavailableDescriptorProvider{}))
		before := f.stored(t)

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(outstandingFirmwareStatus(t)), true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "unavailable")
		assert.Equal(t, before.Status, f.stored(t).Status)
	})

	t.Run("When a firmware status breaks its structure it should reject it without lookups", func(t *testing.T) {
		f := newStatusFixture(t, nil)
		firmware := outstandingFirmwareStatus(t)
		firmware.Attempt.Phase = "Flashing"

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), false)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "status.firmware.attempt.phase")
		assert.Empty(t, f.evidence.schemaLookups)
	})
}

// statusValueWithoutFirmware is a complete device status value, as a JSON
// Patch request carries it, that does not mention the firmware status.
func statusValueWithoutFirmware() map[string]any {
	status := domain.NewDeviceStatus()
	status.Os.Image = "quay.io/example/os:patched"
	raw, err := json.Marshal(status)
	if err != nil {
		panic(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		panic(err)
	}
	delete(value, "firmware")
	return value
}

func TestPatchDeviceStatusFirmware(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		stored      func(t *testing.T) *domain.FirmwareStatus
		patch       domain.PatchRequest
		wantCode    int32
		wantErr     string
		check       func(t *testing.T, before, after *domain.Device)
		wantLookups int
	}{
		{
			name:     "When a patch removes an outstanding attempt it should reject it",
			stored:   outstandingFirmwareStatus,
			patch:    domain.PatchRequest{{Op: "remove", Path: "/status/firmware/attempt"}},
			wantCode: http.StatusBadRequest,
			wantErr:  attemptErasure,
		},
		{
			name:     "When a patch removes the firmware status with an outstanding attempt it should reject it",
			stored:   outstandingFirmwareStatus,
			patch:    domain.PatchRequest{{Op: "remove", Path: "/status/firmware"}},
			wantCode: http.StatusBadRequest,
			wantErr:  attemptErasure,
		},
		{
			name:     "When a patch sets the firmware status to null with an outstanding attempt it should reject it",
			stored:   outstandingFirmwareStatus,
			patch:    domain.PatchRequest{{Op: "replace", Path: "/status/firmware", Value: nil}},
			wantCode: http.StatusBadRequest,
			wantErr:  attemptErasure,
		},
		{
			name:     "When a patch replaces an outstanding attempt with a different one it should reject it",
			stored:   outstandingFirmwareStatus,
			patch:    domain.PatchRequest{{Op: "replace", Path: "/status/firmware/attempt/id", Value: "attempt-2"}},
			wantCode: http.StatusBadRequest,
			wantErr:  attemptErasure,
		},
		{
			name:     "When a patch replaces the status without a firmware status it should keep the stored firmware status",
			stored:   outstandingFirmwareStatus,
			patch:    domain.PatchRequest{{Op: "replace", Path: "/status", Value: statusValueWithoutFirmware()}},
			wantCode: http.StatusOK,
			check: func(t *testing.T, before, after *domain.Device) {
				assert.Equal(t, "quay.io/example/os:patched", after.Status.Os.Image)
				assert.Equal(t, before.Status.Firmware, after.Status.Firmware)
			},
		},
		{
			name: "When a patch removes the whole status after the attempt finished it should reject it",
			stored: func(t *testing.T) *domain.FirmwareStatus {
				status := outstandingFirmwareStatus(t)
				status.Attempt.Phase = domain.FirmwareAttemptPhaseFailed
				return status
			},
			patch:    domain.PatchRequest{{Op: "remove", Path: "/status"}},
			wantCode: http.StatusBadRequest,
			wantErr:  "status cannot be removed while firmware status exists",
		},
		{
			name:     "When a patch moves the outstanding attempt to a terminal phase it should accept it",
			stored:   outstandingFirmwareStatus,
			patch:    domain.PatchRequest{{Op: "replace", Path: "/status/firmware/attempt/phase", Value: "Succeeded"}},
			wantCode: http.StatusOK,
			check: func(t *testing.T, before, after *domain.Device) {
				assert.Equal(t, domain.FirmwareAttemptPhaseSucceeded, after.Status.Firmware.Attempt.Phase)
			},
			// Validated by the early check and by the write.
			wantLookups: 2,
		},
		{
			name: "When a patch removes the firmware status after the attempt finished it should accept it",
			stored: func(t *testing.T) *domain.FirmwareStatus {
				status := outstandingFirmwareStatus(t)
				status.Attempt.Phase = domain.FirmwareAttemptPhaseFailed
				return status
			},
			patch:    domain.PatchRequest{{Op: "remove", Path: "/status/firmware"}},
			wantCode: http.StatusOK,
			check: func(t *testing.T, before, after *domain.Device) {
				assert.Nil(t, after.Status.Firmware)
			},
		},
		{
			name:     "When a patch adds an unknown observation data field it should reject it with the retained schema",
			stored:   outstandingFirmwareStatus,
			patch:    domain.PatchRequest{{Op: "add", Path: "/status/firmware/components/0/observation/backend/data/flashCount", Value: 3}},
			wantCode: http.StatusBadRequest,
			wantErr:  "flashCount",
			// The early check rejects the request before the write.
			wantLookups: 1,
		},
		{
			name:     "When a patch adds a field that differs from an attempt field only in case it should reject it",
			stored:   outstandingFirmwareStatus,
			patch:    domain.PatchRequest{{Op: "add", Path: "/status/firmware/attempt/Id", Value: "attempt-2"}},
			wantCode: http.StatusBadRequest,
			wantErr:  `status.firmware.attempt: property "Id" is unsupported`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newStatusFixture(t, tt.stored(t))
			before := f.stored(t)

			_, status := f.svc.PatchDeviceStatus(ctx, f.orgId, "foo", tt.patch, true)

			require.Equal(t, tt.wantCode, status.Code, status.Message)
			if tt.wantErr != "" {
				assert.Contains(t, status.Message, tt.wantErr)
			}
			after := f.stored(t)
			if tt.wantCode != http.StatusOK {
				assert.Equal(t, before.Status, after.Status)
			}
			if tt.check != nil {
				tt.check(t, before, after)
			}
			assert.Len(t, f.evidence.schemaLookups, tt.wantLookups)
			assert.Empty(t, f.evidence.releaseLookups)
		})
	}
}

func TestPatchDeviceCannotWriteFirmwareStatus(t *testing.T) {
	f := newStatusFixture(t, outstandingFirmwareStatus(t))
	before := f.stored(t)

	_, status := f.svc.PatchDevice(context.Background(), f.orgId, "foo", domain.PatchRequest{{Op: "remove", Path: "/status/firmware/attempt"}}, true, true)

	require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
	assert.Contains(t, status.Message, "status is immutable")
	assert.Equal(t, before.Status, f.stored(t).Status)
}

// olderBudgetFirmwareStatus is a stored firmware status whose attempt error
// names an exceeded budget under a reason other than ResourceLimitExceeded, as
// a producer could report before that reason was required.
func olderBudgetFirmwareStatus(t *testing.T) *domain.FirmwareStatus {
	t.Helper()
	status := outstandingFirmwareStatus(t)
	status.Attempt.Error = &domain.FirmwareError{Reason: "ArtifactUnauthenticated", Message: "the capsule exceeds the parser size budget", ExceededBudget: lo.ToPtr("maxArtifactBytes")}
	return status
}

func TestFirmwareStatusExceededBudgetOnStatusWrites(t *testing.T) {
	ctx := context.Background()
	const budgetPath = "status.firmware.attempt.error.exceededBudget"

	t.Run("When an older stored error names a budget and the status write omits the firmware status it should keep it without validation", func(t *testing.T) {
		f := newStatusFixture(t, olderBudgetFirmwareStatus(t))
		before := f.stored(t)

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(nil), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Status.Firmware, f.stored(t).Status.Firmware)
		assert.Empty(t, f.evidence.schemaLookups)
	})

	t.Run("When an older stored error names a budget and a status patch leaves the firmware status untouched it should keep it without validation", func(t *testing.T) {
		f := newStatusFixture(t, olderBudgetFirmwareStatus(t))
		before := f.stored(t)

		_, status := f.svc.PatchDeviceStatus(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "replace", Path: "/status/os/image", Value: "quay.io/example/os:patched"}}, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		after := f.stored(t)
		assert.Equal(t, before.Status.Firmware, after.Status.Firmware)
		assert.Equal(t, "quay.io/example/os:patched", after.Status.Os.Image)
		assert.Empty(t, f.evidence.schemaLookups)
	})

	t.Run("When an older stored error names a budget and the same firmware status is sent again it should keep it without validation", func(t *testing.T) {
		f := newStatusFixture(t, olderBudgetFirmwareStatus(t))
		before := f.stored(t)

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(olderBudgetFirmwareStatus(t)), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Status.Firmware, f.stored(t).Status.Firmware)
		assert.Empty(t, f.evidence.schemaLookups)
	})

	t.Run("When a new firmware status names a budget for another reason it should reject it before any lookup", func(t *testing.T) {
		f := newStatusFixture(t, nil)
		before := f.stored(t)

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(olderBudgetFirmwareStatus(t)), true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, budgetPath+": Forbidden")
		assert.Equal(t, before.Status, f.stored(t).Status)
		assert.Empty(t, f.evidence.schemaLookups)
	})

	t.Run("When a new limit error names no budget it should reject it before any lookup", func(t *testing.T) {
		f := newStatusFixture(t, nil)
		before := f.stored(t)
		firmware := outstandingFirmwareStatus(t)
		firmware.Attempt.Error = &domain.FirmwareError{Reason: domain.FirmwareErrorReasonResourceLimitExceeded, Message: "the capsule exceeds the parser size budget"}

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, budgetPath+": Required value")
		assert.Equal(t, before.Status, f.stored(t).Status)
		assert.Empty(t, f.evidence.schemaLookups)
	})

	t.Run("When a status patch adds an error that names a budget for another reason it should reject it", func(t *testing.T) {
		f := newStatusFixture(t, outstandingFirmwareStatus(t))
		before := f.stored(t)

		_, status := f.svc.PatchDeviceStatus(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "add", Path: "/status/firmware/attempt/error", Value: map[string]any{
			"reason": "ArtifactDigestMismatch", "message": "digest mismatch", "retryable": false, "exceededBudget": "maxArtifactBytes",
		}}}, true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, budgetPath+": Forbidden")
		assert.Equal(t, before.Status, f.stored(t).Status)
	})

	t.Run("When a new limit error names a budget of the pinned release it should accept it", func(t *testing.T) {
		f := newStatusFixture(t, nil)
		firmware := outstandingFirmwareStatus(t)
		firmware.Attempt.Error = &domain.FirmwareError{Reason: domain.FirmwareErrorReasonResourceLimitExceeded, Message: "the capsule exceeds the parser size budget", ExceededBudget: lo.ToPtr("maxArtifactBytes")}

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, "maxArtifactBytes", lo.FromPtr(f.stored(t).Status.Firmware.Attempt.Error.ExceededBudget))
	})
}

func TestFirmwareStatusComponentBudgetWithoutAttempt(t *testing.T) {
	ctx := context.Background()

	t.Run("When a component without an attempt names a budget of its own observation while the desired firmware names another release it should accept it without a release lookup", func(t *testing.T) {
		f := newStatusFixture(t, nil)
		f.store.device.devices["foo"].Spec.Firmware = &domain.FirmwareSpec{
			Profile: "orin-example-profile",
			Updater: domain.FirmwareUpdaterSpec{Name: "other-updater", Version: "9.9.9", Config: map[string]any{}},
		}
		firmware := outstandingFirmwareStatus(t)
		firmware.Attempt = nil
		firmware.Components[0].Error = &domain.FirmwareError{Reason: domain.FirmwareErrorReasonResourceLimitExceeded, Message: "a resource budget was exceeded", ExceededBudget: lo.ToPtr("maxArtifactBytes")}

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, "maxArtifactBytes", lo.FromPtr(f.stored(t).Status.Firmware.Components[0].Error.ExceededBudget))
		assert.Equal(t, []string{"nvidia-orin@" + statusSchemaDigest}, f.evidence.schemaLookups)
		assert.Empty(t, f.evidence.releaseLookups)
	})

	t.Run("When a component without an attempt names a budget its own observation does not define it should reject it and keep the stored status", func(t *testing.T) {
		f := newStatusFixture(t, nil)
		before := f.stored(t)
		firmware := outstandingFirmwareStatus(t)
		firmware.Attempt = nil
		firmware.Components[0].Error = &domain.FirmwareError{Reason: domain.FirmwareErrorReasonResourceLimitExceeded, Message: "a resource budget was exceeded", ExceededBudget: lo.ToPtr("maxFmpItems")}

		_, status := f.svc.ReplaceDeviceStatus(ctx, f.orgId, "foo", statusWrite(firmware), true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, `status.firmware.components[0].error.exceededBudget: Unsupported value: "maxFmpItems"`)
		assert.Equal(t, before.Status, f.stored(t).Status)
	})
}

func TestServerSideStatusPreservesConcurrentFirmware(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "When server status retries it should preserve a concurrent attempt"
		if force {
			name = "When forced server status retries it should preserve a concurrent attempt"
		}
		t.Run(name, func(t *testing.T) {
			f := newStatusFixture(t, nil)
			concurrent := outstandingFirmwareStatus(t)
			calls := 0
			f.store.device.beforeMutate = func(int) {
				calls++
				f.store.device.devices["foo"].Status.Firmware = concurrent
			}
			var err error
			if force {
				err = f.svc.ForceUpdateServerSideDeviceStatus(context.Background(), f.orgId, "foo")
			} else {
				err = f.svc.UpdateServerSideDeviceStatus(context.Background(), f.orgId, "foo")
			}
			require.NoError(t, err)
			require.Positive(t, calls)
			require.Equal(t, concurrent, f.stored(t).Status.Firmware)
		})
	}
}
