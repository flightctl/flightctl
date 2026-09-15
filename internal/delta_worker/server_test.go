package delta_worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type deltaWorkerTestProducer struct {
	err      error
	payloads [][]byte
}

func (p *deltaWorkerTestProducer) Enqueue(_ context.Context, payload []byte, _ int64) error {
	if p.err != nil {
		return p.err
	}
	p.payloads = append(p.payloads, append([]byte(nil), payload...))
	return nil
}

func (p *deltaWorkerTestProducer) Close() {}

func TestEnqueueDeltaWorkerEvent(t *testing.T) {
	orgID := uuid.New()
	event := &domain.Event{Reason: domain.EventReasonGenerateDelta}

	t.Run("When producer is nil it should return an error", func(t *testing.T) {
		err := enqueueDeltaWorkerEvent(context.Background(), nil, orgID, event)
		require.ErrorContains(t, err, "queue producer is nil")
	})

	t.Run("When enqueue fails it should return the producer error", func(t *testing.T) {
		producer := &deltaWorkerTestProducer{err: errors.New("redis down")}
		err := enqueueDeltaWorkerEvent(context.Background(), producer, orgID, event)
		require.ErrorContains(t, err, "redis down")
	})

	t.Run("When enqueue succeeds it should write the worker event payload", func(t *testing.T) {
		producer := &deltaWorkerTestProducer{}
		require.NoError(t, enqueueDeltaWorkerEvent(context.Background(), producer, orgID, event))
		require.Len(t, producer.payloads, 1)

		var got worker_client.EventWithOrgId
		require.NoError(t, json.Unmarshal(producer.payloads[0], &got))
		require.Equal(t, orgID, got.OrgId)
		require.Equal(t, event.Reason, got.Event.Reason)
	})
}
