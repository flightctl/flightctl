package deltaprepare

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type recordingStore struct {
	prepare *model.DeltaPrepare
	ids     []uuid.UUID
	filter  int
}

func (s *recordingStore) CreateDeltaPrepare(_ context.Context, prepare *model.DeltaPrepare) error {
	s.prepare = prepare
	return nil
}

func (s *recordingStore) CreateOrReplaceWaitingDeltaPrepare(_ context.Context, prepare *model.DeltaPrepare) (deltastore.PrepareAdmission, error) {
	s.prepare = prepare
	return deltastore.PrepareAdmission{Prepare: prepare, Accepted: true}, nil
}

func (s *recordingStore) GetDeltaPrepare(context.Context, deltastore.PrepareKey, ...deltastore.PrepareGetOption) (*model.DeltaPrepare, error) {
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

func (s *recordingStore) CountDeltaPrepareGenerations(context.Context, uuid.UUID) (int, int, error) {
	s.filter++
	return 2, 3, nil
}

var _ deltastore.DeltaPrepareStore = (*recordingStore)(nil)

func TestServiceDelegatesPrepareOperations(t *testing.T) {
	ctx := context.Background()
	prepare := &model.DeltaPrepare{ID: uuid.New()}
	store := &recordingStore{}
	h := NewServiceHandler(store, nil)

	require.NoError(t, h.CreateDeltaPrepare(ctx, prepare))
	got, err := h.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: prepare.ID})
	require.NoError(t, err)
	require.Same(t, prepare, got)

	ids := []uuid.UUID{prepare.ID}
	_, err = h.ListDeltaPrepares(ctx, ids)
	require.NoError(t, err)
	require.Equal(t, ids, store.ids)

	updated, err := h.UpdateDeltaPrepare(ctx, 4, prepare)
	require.NoError(t, err)
	require.Same(t, prepare, updated)

	completed, total, err := h.CountDeltaPrepareGenerations(ctx, prepare.ID)
	require.NoError(t, err)
	require.Equal(t, 2, completed)
	require.Equal(t, 3, total)
	require.Equal(t, 1, store.filter)
}

func TestServicePropagatesStoreErrors(t *testing.T) {
	store := &errorStore{err: errors.New("store failure")}
	h := NewServiceHandler(store, nil)

	err := h.CreateDeltaPrepare(context.Background(), &model.DeltaPrepare{})
	require.ErrorIs(t, err, store.err)
}

type errorStore struct{ err error }

func (s *errorStore) CreateDeltaPrepare(context.Context, *model.DeltaPrepare) error { return s.err }
func (s *errorStore) CreateOrReplaceWaitingDeltaPrepare(context.Context, *model.DeltaPrepare) (deltastore.PrepareAdmission, error) {
	return deltastore.PrepareAdmission{}, s.err
}
func (s *errorStore) GetDeltaPrepare(context.Context, deltastore.PrepareKey, ...deltastore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return nil, s.err
}
func (s *errorStore) ListDeltaPrepares(context.Context, []uuid.UUID) ([]model.DeltaPrepare, error) {
	return nil, s.err
}
func (s *errorStore) UpdateDeltaPrepare(context.Context, int64, *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	return nil, s.err
}
func (s *errorStore) CountDeltaPrepareGenerations(context.Context, uuid.UUID) (int, int, error) {
	return 0, 0, s.err
}

var _ deltastore.DeltaPrepareStore = (*errorStore)(nil)
