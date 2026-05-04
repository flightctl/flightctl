package service

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCVELifecycleHandler(t *testing.T) (*ServiceHandler, *TestStore) {
	t.Helper()
	log := logrus.New()
	log.SetOutput(io.Discard)
	ts := &TestStore{}
	ts.init()
	h := &ServiceHandler{
		eventHandler: NewEventHandler(ts, nil, log),
		store:        ts,
		log:          log,
	}
	return h, ts
}

func TestSyncDeviceCVELifecycleEvents_When_list_errors_it_should_abort(t *testing.T) {
	h, ts := testCVELifecycleHandler(t)
	dbErr := errors.New("list failed")
	ts.dummyVulnerabilityFinding.StubCVELifecycleResponses = true
	ts.dummyVulnerabilityFinding.CVELifecycleResolutionErr = dbErr

	err := h.SyncDeviceCVELifecycleEvents(context.Background())
	require.ErrorIs(t, err, dbErr)
	require.Len(t, *ts.events.events, 0)
}

func TestSyncDeviceCVELifecycleEvents_When_stub_lists_return_candidates_it_should_create_events_in_phase_order(t *testing.T) {
	h, ts := testCVELifecycleHandler(t)
	orgID := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	ts.dummyVulnerabilityFinding.StubCVELifecycleResponses = true
	ts.dummyVulnerabilityFinding.CVELifecycleResolution = []store.CVEEventResolutionCandidate{
		{
			OrgID: orgID, DeviceName: "dev-resolve", CveID: "CVE-R1", ImageDigest: "sha256:a",
			ImageRef: "img/r:1", CvssScore: 8.0, Severity: "Critical",
		},
	}
	ts.dummyVulnerabilityFinding.CVELifecycleSupersede = []store.CVEEventCandidate{
		{
			OrgID: orgID, DeviceName: "dev-super", CveID: "CVE-S1", ImageDigest: "sha256:b",
			ImageRef: "img/s:1", CvssScore: 9.0, Severity: "Critical",
		},
	}
	ts.dummyVulnerabilityFinding.CVELifecycleCritical = []store.CVEEventCandidate{
		{
			OrgID: orgID, DeviceName: "dev-crit", CveID: "CVE-C1", ImageDigest: "sha256:c",
			ImageRef: "img/c:1", CvssScore: 9.5, Severity: "Critical",
		},
	}
	ts.dummyVulnerabilityFinding.CVELifecycleWarning = []store.CVEEventCandidate{
		{
			OrgID: orgID, DeviceName: "dev-warn", CveID: "CVE-W1", ImageDigest: "sha256:d",
			ImageRef: "img/w:1", CvssScore: 5.0, Severity: "High",
		},
	}

	err := h.SyncDeviceCVELifecycleEvents(context.Background())
	require.NoError(t, err)

	evs := *ts.events.events
	require.Len(t, evs, 4)

	wantReasons := []domain.EventReason{
		domain.EventReasonDeviceVulnerabilityCVEResolved,
		domain.EventReasonDeviceVulnerabilityCVEResolved,
		domain.EventReasonDeviceVulnerabilityCVECritical,
		domain.EventReasonDeviceVulnerabilityCVEWarning,
	}
	for i := range wantReasons {
		assert.Equal(t, wantReasons[i], evs[i].Reason)
		assert.Equal(t, string(domain.DeviceKind), evs[i].InvolvedObject.Kind)
	}

	assert.Contains(t, evs[2].Message, "CVE-C1")
	assert.Contains(t, evs[2].Message, "9.5")
	assert.Equal(t, domain.EventTypeWarning, evs[2].Type)

	assert.Contains(t, evs[3].Message, "CVE-W1")
	assert.Equal(t, domain.EventTypeWarning, evs[3].Type)

	assert.Equal(t, domain.EventTypeNormal, evs[0].Type)

	d0, err := evs[0].Details.AsDeviceVulnerabilityCveDetails()
	require.NoError(t, err)
	assert.Equal(t, "CVE-R1", d0.CveId)
	require.NotNil(t, d0.FirstImageDigest)
	assert.Equal(t, "sha256:a", *d0.FirstImageDigest)
	require.NotNil(t, d0.FirstImageRef)
	assert.Equal(t, "img/r:1", *d0.FirstImageRef)
}

func TestFormatCVEDeviceEventMessage(t *testing.T) {
	tests := []struct {
		name string
		r    domain.EventReason
		cvss float64
		sev  string
		ref  string
		want string
	}{
		{
			name: "resolved without imageRef",
			r:    domain.EventReasonDeviceVulnerabilityCVEResolved,
			ref:  "",
			want: "CVE-2024-1 resolved",
		},
		{
			name: "resolved with imageRef",
			r:    domain.EventReasonDeviceVulnerabilityCVEResolved,
			ref:  "registry.io/os:9.4",
			want: "CVE-2024-1 resolved for image registry.io/os:9.4",
		},
		{
			name: "critical with imageRef shows severity",
			r:    domain.EventReasonDeviceVulnerabilityCVECritical,
			cvss: 9.8,
			sev:  "Critical",
			ref:  "registry.io/os:9.4",
			want: "CVE-2024-1 (Critical, CVSS 9.8) detected on image registry.io/os:9.4",
		},
		{
			name: "warning shows High severity",
			r:    domain.EventReasonDeviceVulnerabilityCVEWarning,
			cvss: 5.0,
			sev:  "High",
			ref:  "",
			want: "CVE-2024-1 (High, CVSS 5.0) detected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatCVEDeviceEventMessage(tt.r, "CVE-2024-1", tt.cvss, tt.sev, tt.ref)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildDeviceCVELifecycleEvent_involved_object_and_warning_type(t *testing.T) {
	ctx := context.Background()
	ev, err := buildDeviceCVELifecycleEvent(ctx, "my-device", "CVE-2024-X", "sha256:x", "",
		8.5, "Critical", domain.EventReasonDeviceVulnerabilityCVECritical)
	require.NoError(t, err)
	require.NotNil(t, ev)
	assert.Equal(t, "my-device", ev.InvolvedObject.Name)
	assert.Equal(t, string(domain.DeviceKind), ev.InvolvedObject.Kind)
	assert.Equal(t, domain.EventTypeWarning, ev.Type)
	assert.Equal(t, domain.EventReasonDeviceVulnerabilityCVECritical, ev.Reason)

	evResolved, err := buildDeviceCVELifecycleEvent(ctx, "d2", "CVE-2024-Y", "sha256:y", "img:v",
		0, "", domain.EventReasonDeviceVulnerabilityCVEResolved)
	require.NoError(t, err)
	assert.Equal(t, domain.EventTypeNormal, evResolved.Type)
}
