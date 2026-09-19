package deltapreparegeneration

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	store "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type recordingStore struct {
	joins []*model.DeltaPrepareGeneration
}

func (s *recordingStore) CreateDeltaPrepareGenerations(_ context.Context, joins []*model.DeltaPrepareGeneration) (store.CreateDeltaPrepareGenerationsResult, error) {
	s.joins = joins
	return store.CreateDeltaPrepareGenerationsResult{InsertedJoins: joins}, nil
}

func (s *recordingStore) ListDeltaPrepareGenerations(context.Context, store.ListFilter) ([]model.DeltaPrepareGeneration, error) {
	rows := make([]model.DeltaPrepareGeneration, len(s.joins))
	for i, join := range s.joins {
		rows[i] = *join
	}
	return rows, nil
}

type errorStore struct{ err error }

func (s *errorStore) CreateDeltaPrepareGenerations(context.Context, []*model.DeltaPrepareGeneration) (store.CreateDeltaPrepareGenerationsResult, error) {
	return store.CreateDeltaPrepareGenerationsResult{}, s.err
}

func (s *errorStore) ListDeltaPrepareGenerations(context.Context, store.ListFilter) ([]model.DeltaPrepareGeneration, error) {
	return nil, s.err
}

func TestServiceDelegatesPrepareGenerationOperations(t *testing.T) {
	ctx := context.Background()
	join := &model.DeltaPrepareGeneration{PrepareID: uuid.New()}
	backing := &recordingStore{}
	h := NewServiceHandler(backing)

	created, err := h.CreateDeltaPrepareGenerations(ctx, []*model.DeltaPrepareGeneration{join})
	require.NoError(t, err)
	require.Len(t, created.InsertedJoins, 1)
	require.Same(t, join, backing.joins[0])

	rows, err := h.ListDeltaPrepareGenerations(ctx, store.ListFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, join.PrepareID, rows[0].PrepareID)
}

func TestServicePropagatesStoreErrors(t *testing.T) {
	backing := &errorStore{err: errors.New("store failure")}
	h := NewServiceHandler(backing)

	_, err := h.CreateDeltaPrepareGenerations(context.Background(), []*model.DeltaPrepareGeneration{{PrepareID: uuid.New()}})
	require.ErrorIs(t, err, backing.err)
}
