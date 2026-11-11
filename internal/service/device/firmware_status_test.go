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
			name:     "When a patch adds an unknown observation data field it should reject it with the retained schema",
			stored:   outstandingFirmwareStatus,
			patch:    domain.PatchRequest{{Op: "add", Path: "/status/firmware/components/0/observation/backend/data/flashCount", Value: 3}},
			wantCode: http.StatusBadRequest,
			wantErr:  "flashCount",
			// The early check rejects the request before the write.
			wantLookups: 1,
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
