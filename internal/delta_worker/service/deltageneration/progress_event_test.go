package deltageneration

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestDeltaGenerationProgressEvent_WhenGenerationIsTerminalItShouldUseATerminalReason(t *testing.T) {
	tests := []struct {
		name       string
		status     domain.DeltaGenerationProgressDetailsGenerationStatus
		wantReason domain.EventReason
		wantType   domain.EventType
	}{
		{
			name:       "When generation succeeds it should use the succeeded reason",
			status:     domain.DeltaGenerationProgressSucceeded,
			wantReason: domain.EventReasonDeltaGenerationSucceeded,
			wantType:   domain.EventTypeNormal,
		},
		{
			name:       "When generation fails it should use the failed reason and warning type",
			status:     domain.DeltaGenerationProgressFailed,
			wantReason: domain.EventReasonDeltaGenerationFailed,
			wantType:   domain.EventTypeWarning,
		},
		{
			name:       "When generation is rejected it should use the rejected reason and normal type",
			status:     domain.DeltaGenerationProgressRejected,
			wantReason: domain.EventReasonDeltaGenerationRejected,
			wantType:   domain.EventTypeNormal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepare := model.DeltaPrepare{
				Kind: domain.DeviceKind,
				Name: "device-1",
			}
			key := deltastore.GenerationKey{
				ImageRepository: "quay.io/example/os",
				SourceDigest:    "sha256:source",
				TargetDigest:    "sha256:target",
			}

			event, err := DeltaGenerationProgressEvent(context.Background(), prepare, key, tt.status, nil)

			require.NoError(t, err)
			require.Equal(t, tt.wantReason, event.Reason)
			require.Equal(t, tt.wantType, event.Type)
			details, err := event.Details.AsDeltaGenerationProgressDetails()
			require.NoError(t, err)
			require.Equal(t, tt.status, details.GenerationStatus)
		})
	}
}
