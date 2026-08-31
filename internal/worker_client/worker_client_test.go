package worker_client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type recordingProducer struct {
	payloads [][]byte
}

func (p *recordingProducer) Enqueue(_ context.Context, payload []byte, _ int64) error {
	p.payloads = append(p.payloads, append([]byte(nil), payload...))
	return nil
}

func (p *recordingProducer) Close() {}

func TestEmitEvent_QueueRouting(t *testing.T) {
	orgID := uuid.New()
	tests := []struct {
		name      string
		reason    domain.EventReason
		withDelta bool
		wantTask  int
		wantDelta int
	}{
		{
			name:      "When PrepareDeltas with delta publisher it should enqueue on the delta producer only",
			reason:    domain.EventReasonPrepareDeltas,
			withDelta: true,
			wantTask:  0,
			wantDelta: 1,
		},
		{
			name:      "When PrepareDeltas without delta publisher it should enqueue on neither producer",
			reason:    domain.EventReasonPrepareDeltas,
			withDelta: false,
			wantTask:  0,
			wantDelta: 0,
		},
		{
			name:      "When DeltaGenerationCompleted it should enqueue on the TaskQueue producer only",
			reason:    domain.EventReasonDeltaGenerationCompleted,
			withDelta: true,
			wantTask:  1,
			wantDelta: 0,
		},
		{
			name:      "When FleetRolloutStarted it should enqueue on the TaskQueue producer",
			reason:    domain.EventReasonFleetRolloutStarted,
			withDelta: true,
			wantTask:  1,
			wantDelta: 0,
		},
		{
			name:      "When DeltaGenerationProgress it should enqueue on neither producer",
			reason:    domain.EventReasonDeltaGenerationProgress,
			withDelta: true,
			wantTask:  0,
			wantDelta: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskProd := &recordingProducer{}
			deltaProd := &recordingProducer{}
			log := logrus.New()
			log.SetLevel(logrus.ErrorLevel)

			opts := []ClientOption{}
			if tt.withDelta {
				opts = append(opts, WithDeltaPublisher(deltaProd))
			}
			client := NewWorkerClient(taskProd, log, opts...)
			client.EmitEvent(context.Background(), orgID, &domain.Event{Reason: tt.reason})

			require.Len(t, taskProd.payloads, tt.wantTask)
			require.Len(t, deltaProd.payloads, tt.wantDelta)

			payloads := taskProd.payloads
			if tt.wantDelta > 0 {
				payloads = deltaProd.payloads
			}
			if len(payloads) == 0 {
				return
			}
			var got EventWithOrgId
			require.NoError(t, json.Unmarshal(payloads[0], &got))
			require.Equal(t, orgID, got.OrgId)
			require.Equal(t, tt.reason, got.Event.Reason)
		})
	}
}

func TestEnqueueEvent(t *testing.T) {
	orgID := uuid.New()
	t.Run("When producer is nil it should return an error", func(t *testing.T) {
		err := EnqueueEvent(context.Background(), nil, orgID, &domain.Event{Reason: domain.EventReasonGenerateDelta})
		require.Error(t, err)
		require.Contains(t, err.Error(), "queue producer is required")
	})
	t.Run("When enqueue fails it should return the producer error", func(t *testing.T) {
		p := &failingProducer{err: errors.New("redis down")}
		err := EnqueueEvent(context.Background(), p, orgID, &domain.Event{Reason: domain.EventReasonGenerateDelta})
		require.Error(t, err)
		require.Contains(t, err.Error(), "redis down")
	})
}

type failingProducer struct {
	err error
}

func (p *failingProducer) Enqueue(_ context.Context, _ []byte, _ int64) error {
	return p.err
}

func (p *failingProducer) Close() {}
