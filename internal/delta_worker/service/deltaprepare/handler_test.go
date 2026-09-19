package deltaprepare

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type recordingStore struct {
	prepare *model.DeltaPrepare
	ids     []uuid.UUID
	gets    int
}

func (s *recordingStore) CreateDeltaPrepare(_ context.Context, prepare *model.DeltaPrepare) error {
	s.prepare = prepare
	return nil
}

func (s *recordingStore) CreateOrReplaceWaitingDeltaPrepare(_ context.Context, prepare *model.DeltaPrepare) (deltapreparestore.PrepareAdmission, error) {
	s.prepare = prepare
	return deltapreparestore.PrepareAdmission{Prepare: prepare, Accepted: true}, nil
}

func (s *recordingStore) GetDeltaPrepare(context.Context, deltapreparestore.PrepareKey, ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	s.gets++
	return s.prepare, nil
}

func (s *recordingStore) ListDeltaPrepares(_ context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error) {
	s.ids = ids
	return []model.DeltaPrepare{}, nil
}

func (s *recordingStore) UpdateDeltaPrepare(_ context.Context, _ int64, prepare *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	s.prepare = prepare
	return prepare, nil
}

func (s *recordingStore) DecrementPendingGenerationsForGeneration(context.Context, deltagenerationstore.GenerationKey) ([]deltapreparestore.PrepareProgress, error) {
	return nil, nil
}

var _ deltapreparestore.Store = (*recordingStore)(nil)

func TestServiceDelegatesPrepareOperations(t *testing.T) {
	ctx := context.Background()
	prepare := &model.DeltaPrepare{ID: uuid.New()}
	store := &recordingStore{}
	h := NewServiceHandler(store, nil)

	require.NoError(t, h.CreateDeltaPrepare(ctx, prepare))
	got, err := h.GetDeltaPrepare(ctx, deltapreparestore.PrepareKey{ID: prepare.ID})
	require.NoError(t, err)
	require.Same(t, prepare, got)

	ids := []uuid.UUID{prepare.ID}
	_, err = h.ListDeltaPrepares(ctx, ids)
	require.NoError(t, err)
	require.Equal(t, ids, store.ids)

	updated, err := h.UpdateDeltaPrepare(ctx, 4, prepare)
	require.NoError(t, err)
	require.Same(t, prepare, updated)

}

func TestServicePropagatesStoreErrors(t *testing.T) {
	store := &errorStore{err: errors.New("store failure")}
	h := NewServiceHandler(store, nil)

	err := h.CreateDeltaPrepare(context.Background(), &model.DeltaPrepare{})
	require.ErrorIs(t, err, store.err)
}

type errorStore struct{ err error }

func (s *errorStore) CreateDeltaPrepare(context.Context, *model.DeltaPrepare) error { return s.err }
func (s *errorStore) CreateOrReplaceWaitingDeltaPrepare(context.Context, *model.DeltaPrepare) (deltapreparestore.PrepareAdmission, error) {
	return deltapreparestore.PrepareAdmission{}, s.err
}
func (s *errorStore) GetDeltaPrepare(context.Context, deltapreparestore.PrepareKey, ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return nil, s.err
}
func (s *errorStore) ListDeltaPrepares(context.Context, []uuid.UUID) ([]model.DeltaPrepare, error) {
	return nil, s.err
}
func (s *errorStore) UpdateDeltaPrepare(context.Context, int64, *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	return nil, s.err
}
func (s *errorStore) DecrementPendingGenerationsForGeneration(context.Context, deltagenerationstore.GenerationKey) ([]deltapreparestore.PrepareProgress, error) {
	return nil, s.err
}

var _ deltapreparestore.Store = (*errorStore)(nil)
