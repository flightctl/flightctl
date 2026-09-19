package deltaprepare

import (
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPrepareCompletionEventRoundTrip(t *testing.T) {
	orgID := uuid.New()
	tv := "tv-1"
	prepare := &model.DeltaPrepare{
		ID:                    uuid.New(),
		OrgID:                 orgID,
		Kind:                  domain.FleetKind,
		Name:                  "fleet-1",
		TemplateVersion:       &tv,
		SourceResourceVersion: 7,
		ResourceVersion:       3,
		Status:                model.DeltaPrepareComplete,
	}

	event, err := NewPrepareCompletionEvent(prepare)
	require.NoError(t, err)
	require.Equal(t, domain.EventReasonDeltaPrepareComplete, event.Reason)

	parsed, err := ParsePrepareCompletionEvent(orgID, event.Message)
	require.NoError(t, err)
	require.Equal(t, prepare.OrgID, parsed.OrgID)
	require.Equal(t, prepare.Kind, parsed.Kind)
	require.Equal(t, prepare.Name, parsed.Name)
	require.Equal(t, prepare.TemplateVersion, parsed.TemplateVersion)
	require.Equal(t, prepare.SourceResourceVersion, parsed.SourceResourceVersion)
	require.Equal(t, model.DeltaPrepareComplete, parsed.Status)
}

func TestPrepareCompletionEventWithoutPrepareRow(t *testing.T) {
	orgID := uuid.New()
	prepare := &model.DeltaPrepare{
		OrgID:                 orgID,
		Kind:                  domain.DeviceKind,
		Name:                  "device-1",
		SourceResourceVersion: 4,
	}

	event, err := NewPrepareCompletionEvent(prepare)
	require.NoError(t, err)
	parsed, err := ParsePrepareCompletionEvent(orgID, event.Message)
	require.NoError(t, err)
	require.Equal(t, prepare.SourceResourceVersion, parsed.SourceResourceVersion)
}

func TestPrepareCompletionEventRejectsInvalidPayload(t *testing.T) {
	_, err := ParsePrepareCompletionEvent(uuid.New(), `{"kind":"Fleet","name":"fleet-1"}`)
	require.Error(t, err)
}
