package deltageneration

import (
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestGenerationCompleteEventRoundTrip(t *testing.T) {
	orgID := uuid.New()
	generation := &model.DeltaGeneration{
		OrgID:           orgID,
		ImageRepository: "quay.io/example/os",
		SourceDigest:    "sha256:source",
		TargetDigest:    "sha256:target",
		Status:          model.DeltaGenerationFailed,
	}

	event, err := NewGenerationCompleteEvent(generation)
	require.NoError(t, err)
	require.NotNil(t, event)

	key, status, err := ParseGenerationCompleteEvent(orgID, event.Message)
	require.NoError(t, err)
	require.Equal(t, domain.DeltaGenerationProgressFailed, status)
	require.Equal(t, deltastore.GenerationKey{
		OrgID:           orgID,
		ImageRepository: generation.ImageRepository,
		SourceDigest:    generation.SourceDigest,
		TargetDigest:    generation.TargetDigest,
	}, key)
}

func TestNewGenerationCompleteEventRejectsNonTerminalGeneration(t *testing.T) {
	_, err := NewGenerationCompleteEvent(&model.DeltaGeneration{Status: model.DeltaGenerationInProgress})
	require.Error(t, err)
}

func TestParseGenerationCompleteEventRejectsInvalidPayload(t *testing.T) {
	orgID := uuid.New()
	tests := []struct {
		name    string
		message string
	}{
		{name: "When payload is malformed", message: "not-json"},
		{name: "When generation key is missing", message: `{"status":"succeeded"}`},
		{name: "When status is non-terminal", message: `{"imageRepository":"repo","sourceDigest":"source","targetDigest":"target","status":"pending"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParseGenerationCompleteEvent(orgID, tt.message)
			require.Error(t, err)
		})
	}
}
